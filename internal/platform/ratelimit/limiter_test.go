package ratelimit

import (
	"testing"
	"time"
)

// fakeClock advances only when the test says so.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
func newClock() *fakeClock                   { return &fakeClock{now: time.Unix(0, 0)} }
func newLimiterWith(c *fakeClock) *Limiter   { return NewWithClock(c.Now) }
func TestAllowWithinBurst(t *testing.T) {
	clock := newClock()
	limiter := newLimiterWith(clock)

	for i := 0; i < 5; i++ {
		if result := limiter.Allow("client", 60, 5); !result.Allowed {
			t.Fatalf("request %d rejected inside the burst", i+1)
		}
	}
	if result := limiter.Allow("client", 60, 5); result.Allowed {
		t.Fatal("the bucket must be empty after burst requests")
	}
}

func TestRejectionCarriesRetryAfter(t *testing.T) {
	clock := newClock()
	limiter := newLimiterWith(clock)

	limiter.Allow("client", 60, 1)
	result := limiter.Allow("client", 60, 1)
	if result.Allowed {
		t.Fatal("second request must be rejected")
	}
	if result.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %v, want a positive duration", result.RetryAfter)
	}
	if result.Limit != 60 {
		t.Fatalf("Limit = %d, want 60", result.Limit)
	}
}

func TestTokensRefillOverTime(t *testing.T) {
	clock := newClock()
	limiter := newLimiterWith(clock)

	limiter.Allow("client", 60, 1)
	if limiter.Allow("client", 60, 1).Allowed {
		t.Fatal("bucket should be empty")
	}

	// 60/minute is one per second.
	clock.Advance(time.Second)
	if !limiter.Allow("client", 60, 1).Allowed {
		t.Fatal("a token must refill after one second")
	}
}

func TestRefillIsCappedAtBurst(t *testing.T) {
	clock := newClock()
	limiter := newLimiterWith(clock)

	limiter.Allow("client", 60, 2)
	// A long idle period must not bank more than the burst size.
	clock.Advance(time.Hour)

	if !limiter.Allow("client", 60, 2).Allowed {
		t.Fatal("first request after idle must pass")
	}
	if !limiter.Allow("client", 60, 2).Allowed {
		t.Fatal("second request after idle must pass")
	}
	if limiter.Allow("client", 60, 2).Allowed {
		t.Fatal("refill must stop at the burst size")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	clock := newClock()
	limiter := newLimiterWith(clock)

	limiter.Allow("a", 60, 1)
	if limiter.Allow("a", 60, 1).Allowed {
		t.Fatal("key a should be exhausted")
	}
	if !limiter.Allow("b", 60, 1).Allowed {
		t.Fatal("key b must have its own bucket")
	}
}

func TestIdleBucketsAreEvicted(t *testing.T) {
	clock := newClock()
	limiter := newLimiterWith(clock)

	limiter.Allow("a", 60, 1)
	limiter.Allow("b", 60, 1)
	if limiter.Len() != 2 {
		t.Fatalf("Len = %d, want 2", limiter.Len())
	}

	clock.Advance(idleEviction + time.Second)
	limiter.Allow("c", 60, 1)

	// a and b were idle past the eviction window; only c should remain.
	if limiter.Len() != 1 {
		t.Fatalf("Len = %d, want 1 after eviction", limiter.Len())
	}
}

func TestAllowPanicsOnNonPositiveLimit(t *testing.T) {
	limiter := New()
	defer func() {
		if recover() == nil {
			t.Fatal("a zero rate must panic: it is a configuration bug")
		}
	}()
	limiter.Allow("client", 0, 1)
}
