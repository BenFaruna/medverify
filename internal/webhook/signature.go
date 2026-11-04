package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Signature headers. The signature covers the timestamp and the exact body, so
// a receiver must verify against the raw bytes it received.
const (
	HeaderSignature = "X-MedVerify-Signature"
	HeaderTimestamp = "X-MedVerify-Timestamp"
	HeaderEvent     = "X-MedVerify-Event"
	HeaderID        = "X-MedVerify-Delivery"
)

// SignatureVersion prefixes the hex digest so the scheme can change later
// without ambiguity.
const SignatureVersion = "v1"

// DefaultTolerance is how old a timestamp may be before a receiver rejects it.
const DefaultTolerance = 5 * time.Minute

var (
	// ErrSignatureMismatch means the digest does not match the body.
	ErrSignatureMismatch = errors.New("webhook: signature mismatch")
	// ErrTimestampTooOld means a replayed or badly skewed delivery.
	ErrTimestampTooOld = errors.New("webhook: timestamp outside the tolerance window")
	// ErrMalformedSignature means the header is not a v1 signature.
	ErrMalformedSignature = errors.New("webhook: malformed signature header")
)

// signingPayload is the exact byte string that is signed.
func signingPayload(timestamp string, body []byte) []byte {
	payload := make([]byte, 0, len(timestamp)+1+len(body))
	payload = append(payload, timestamp...)
	payload = append(payload, '.')
	payload = append(payload, body...)
	return payload
}

// Sign returns the signature header value for one delivery.
func Sign(secret string, at time.Time, body []byte) string {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(signingPayload(timestamp, body))
	return SignatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a delivery. The timestamp header is parsed as unix seconds and
// must be within tolerance of now in both directions, so a clock far in the
// future is rejected too.
func Verify(secret, timestamp, signature string, body []byte, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}

	sent, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return ErrTimestampTooOld
	}
	age := time.Since(time.Unix(sent, 0))
	if age > tolerance || age < -tolerance {
		return ErrTimestampTooOld
	}

	version, digest, found := strings.Cut(signature, "=")
	if !found || version != SignatureVersion {
		return ErrMalformedSignature
	}
	provided, err := hex.DecodeString(digest)
	if err != nil {
		return ErrMalformedSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(signingPayload(strings.TrimSpace(timestamp), body))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return ErrSignatureMismatch
	}
	return nil
}
