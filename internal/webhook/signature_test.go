package webhook

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	t.Parallel()

	body := []byte(`{"event":"scan.completed"}`)
	at := time.Now()
	signature := Sign("s3cret", at, body)

	if err := Verify("s3cret", strconv.FormatInt(at.Unix(), 10), signature, body, 0); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestSignIsPrefixedWithTheVersion(t *testing.T) {
	t.Parallel()

	signature := Sign("s3cret", time.Now(), []byte("{}"))
	if len(signature) < 4 || signature[:3] != "v1=" {
		t.Fatalf("signature = %q, want a v1 prefix", signature)
	}
}

func TestVerifyRejectsAWrongSecret(t *testing.T) {
	t.Parallel()

	body := []byte("{}")
	at := time.Now()
	signature := Sign("right", at, body)

	err := Verify("wrong", strconv.FormatInt(at.Unix(), 10), signature, body, 0)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("Verify error = %v, want ErrSignatureMismatch", err)
	}
}

func TestVerifyRejectsTamperedBody(t *testing.T) {
	t.Parallel()

	at := time.Now()
	signature := Sign("s3cret", at, []byte("{}"))

	err := Verify("s3cret", strconv.FormatInt(at.Unix(), 10), signature, []byte(`{"a":1}`), 0)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("Verify error = %v, want ErrSignatureMismatch", err)
	}
}

func TestVerifyRejectsAnOldTimestamp(t *testing.T) {
	t.Parallel()

	body := []byte("{}")
	old := time.Now().Add(-time.Hour)
	signature := Sign("s3cret", old, body)

	err := Verify("s3cret", strconv.FormatInt(old.Unix(), 10), signature, body, 5*time.Minute)
	if !errors.Is(err, ErrTimestampTooOld) {
		t.Fatalf("Verify error = %v, want ErrTimestampTooOld", err)
	}
}

func TestVerifyRejectsAFutureTimestamp(t *testing.T) {
	t.Parallel()

	body := []byte("{}")
	future := time.Now().Add(time.Hour)
	signature := Sign("s3cret", future, body)

	err := Verify("s3cret", strconv.FormatInt(future.Unix(), 10), signature, body, 5*time.Minute)
	if !errors.Is(err, ErrTimestampTooOld) {
		t.Fatalf("Verify error = %v, want ErrTimestampTooOld", err)
	}
}

func TestVerifyRejectsMalformedHeaders(t *testing.T) {
	t.Parallel()

	body := []byte("{}")
	at := time.Now()
	stamp := strconv.FormatInt(at.Unix(), 10)

	cases := map[string]string{
		"no separator": "deadbeef",
		"wrong prefix": "v2=deadbeef",
		"not hex":      "v1=not-hex",
	}
	for name, signature := range cases {
		err := Verify("s3cret", stamp, signature, body, 0)
		if !errors.Is(err, ErrMalformedSignature) {
			t.Fatalf("%s: Verify error = %v, want ErrMalformedSignature", name, err)
		}
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	t.Parallel()

	first := Backoff(1)
	second := Backoff(2)
	if first != baseBackoff {
		t.Fatalf("Backoff(1) = %v, want %v", first, baseBackoff)
	}
	if second <= first {
		t.Fatalf("Backoff(2) = %v, want more than %v", second, first)
	}
	if capped := Backoff(40); capped != maxBackoff {
		t.Fatalf("Backoff(40) = %v, want the %v cap", capped, maxBackoff)
	}
}
