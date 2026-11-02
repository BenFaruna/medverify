package vision

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrCircuitOpen means the provider failed repeatedly and calls are paused.
// The scan finishes failed and River retries later; it never becomes a guess.
var ErrCircuitOpen = errors.New("vision provider circuit is open")

// TransientError marks a failure worth retrying, such as a timeout or a 5xx.
type TransientError struct {
	Err error
}

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

func transientf(format string, args ...any) error {
	return &TransientError{Err: fmt.Errorf(format, args...)}
}

// IsTransient reports whether a provider error may succeed on retry.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var transient *TransientError
	if errors.As(err, &transient) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCircuitOpen)
}

// ResilientOptions configures retries, per-attempt timeout, and the circuit
// breaker around a provider.
type ResilientOptions struct {
	Timeout          time.Duration
	Retries          int
	Backoff          time.Duration
	FailureThreshold int
	Cooldown         time.Duration
	Now              func() time.Time
}

// Resilient wraps a provider with a timeout, bounded retries, and a circuit
// breaker. ErrUnreadable is a normal result and is never retried.
type Resilient struct {
	inner Provider
	opts  ResilientOptions

	mu            sync.Mutex
	failures      int
	state         circuitState
	openedAt      time.Time
	trialInFlight bool
}

type circuitState int

const (
	circuitClosed circuitState = iota
	circuitOpen
	circuitHalfOpen
)

// NewResilient wraps inner. Zero options get conservative defaults.
func NewResilient(inner Provider, opts ResilientOptions) *Resilient {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Retries < 0 {
		opts.Retries = 0
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 250 * time.Millisecond
	}
	if opts.FailureThreshold <= 0 {
		opts.FailureThreshold = 5
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Resilient{inner: inner, opts: opts}
}

// Extract implements Provider.
func (r *Resilient) Extract(ctx context.Context, request Request) (Result, error) {
	if !r.allow() {
		return Result{}, ErrCircuitOpen
	}

	var lastErr error
	for attempt := 0; attempt <= r.opts.Retries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
		result, err := r.inner.Extract(attemptCtx, request)
		cancel()

		switch {
		case err == nil:
			r.recordSuccess()
			return result, nil
		case errors.Is(err, ErrUnreadable):
			// The provider answered; the image is just not readable.
			r.recordSuccess()
			return Result{}, ErrUnreadable
		case !IsTransient(err):
			// A permanent provider error is not a health signal.
			r.recordSuccess()
			return Result{}, err
		}

		lastErr = err
		r.recordFailure()
		if attempt == r.opts.Retries {
			break
		}
		if !r.allow() {
			return Result{}, fmt.Errorf("%w: %w", ErrCircuitOpen, lastErr)
		}
		if err := waitBackoff(ctx, r.opts.Backoff<<attempt); err != nil {
			return Result{}, err
		}
	}
	return Result{}, lastErr
}

func waitBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Resilient) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch r.state {
	case circuitClosed:
		return true
	case circuitOpen:
		if r.opts.Now().Sub(r.openedAt) < r.opts.Cooldown {
			return false
		}
		r.state = circuitHalfOpen
		r.trialInFlight = true
		return true
	case circuitHalfOpen:
		if r.trialInFlight {
			return false
		}
		r.trialInFlight = true
		return true
	default:
		return false
	}
}

func (r *Resilient) recordSuccess() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = 0
	r.state = circuitClosed
	r.trialInFlight = false
}

func (r *Resilient) recordFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state == circuitHalfOpen {
		r.state = circuitOpen
		r.openedAt = r.opts.Now()
		r.trialInFlight = false
		return
	}
	r.failures++
	if r.state == circuitClosed && r.failures >= r.opts.FailureThreshold {
		r.state = circuitOpen
		r.openedAt = r.opts.Now()
	}
}

// State reports the breaker state for tests and metrics.
func (r *Resilient) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.state {
	case circuitOpen:
		return "open"
	case circuitHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}
