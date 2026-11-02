package vision

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"testing"
	"time"

	_ "image/jpeg"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResilientRetriesTransientErrorsThenSucceeds(t *testing.T) {
	t.Parallel()

	inner := &scriptedProvider{
		errors: []error{transientf("timeout"), transientf("timeout")},
		result: Result{Provider: "ok", Confidence: 1},
	}
	provider := NewResilient(inner, ResilientOptions{
		Timeout: time.Second, Retries: 2, Backoff: time.Millisecond,
		FailureThreshold: 10, Cooldown: time.Minute,
	})

	result, err := provider.Extract(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "ok", result.Provider)
	assert.Equal(t, 3, inner.calls)
}

func TestResilientDoesNotRetryUnreadable(t *testing.T) {
	t.Parallel()

	inner := &scriptedProvider{errors: []error{ErrUnreadable, ErrUnreadable}}
	provider := NewResilient(inner, ResilientOptions{
		Timeout: time.Second, Retries: 3, Backoff: time.Millisecond,
		FailureThreshold: 10, Cooldown: time.Minute,
	})

	_, err := provider.Extract(context.Background(), Request{})
	assert.ErrorIs(t, err, ErrUnreadable)
	assert.Equal(t, 1, inner.calls)
}

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	inner := &scriptedProvider{errors: []error{transientf("down"), transientf("down")}}
	provider := NewResilient(inner, ResilientOptions{
		Timeout: time.Second, Retries: 0, Backoff: time.Millisecond,
		FailureThreshold: 2, Cooldown: time.Minute,
		Now: func() time.Time { return now },
	})

	_, err := provider.Extract(context.Background(), Request{})
	assert.True(t, IsTransient(err))
	_, err = provider.Extract(context.Background(), Request{})
	assert.True(t, IsTransient(err))
	assert.Equal(t, "open", provider.State())

	_, err = provider.Extract(context.Background(), Request{})
	assert.ErrorIs(t, err, ErrCircuitOpen)
	assert.Equal(t, 2, inner.calls, "an open circuit must not call the provider")

	now = now.Add(2 * time.Minute)
	inner.errors = nil
	inner.result = Result{Provider: "recovered", Confidence: 1}
	result, err := provider.Extract(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "recovered", result.Provider)
	assert.Equal(t, "closed", provider.State())
}

func TestPreprocessResizesLongEdgeAndStripsMetadata(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../../testdata/images/gps.jpg")
	require.NoError(t, err)

	resized, err := Preprocess(data, 30)
	require.NoError(t, err)
	assert.NotContains(t, string(resized), "Exif")
	assert.NotContains(t, string(resized), "GPS")

	config, _, err := image.DecodeConfig(bytes.NewReader(resized))
	require.NoError(t, err)
	assert.Equal(t, 30, config.Width)
	assert.Equal(t, 20, config.Height)
}

func TestPreprocessLeavesSmallImageUnchanged(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../../testdata/images/normal.jpg")
	require.NoError(t, err)
	same, err := Preprocess(data, 4000)
	require.NoError(t, err)
	assert.Equal(t, data, same)
}

func TestFallbackUsesSecondProviderOnlyOnFailure(t *testing.T) {
	t.Parallel()

	primary := &scriptedProvider{errors: []error{errors.New("provider down")}}
	secondary := &scriptedProvider{result: Result{Provider: "secondary", Confidence: 1}}
	provider := Fallback{Primary: primary, Secondary: secondary}

	result, err := provider.Extract(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "secondary", result.Provider)

	unreadable := &scriptedProvider{errors: []error{ErrUnreadable}}
	untouched := &scriptedProvider{result: Result{Provider: "secondary", Confidence: 1}}
	provider = Fallback{Primary: unreadable, Secondary: untouched}
	_, err = provider.Extract(context.Background(), Request{})
	assert.ErrorIs(t, err, ErrUnreadable)
	assert.Equal(t, 0, untouched.calls)
}

type scriptedProvider struct {
	calls  int
	errors []error
	result Result
}

func (p *scriptedProvider) Extract(context.Context, Request) (Result, error) {
	index := p.calls
	p.calls++
	if index < len(p.errors) && p.errors[index] != nil {
		return Result{}, p.errors[index]
	}
	return p.result, nil
}
