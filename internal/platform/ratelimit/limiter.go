package ratelimit

import (
	"sync"
	"time"
)

// idleEviction is how long a bucket may sit unused before it is dropped. A
// bucket refills from wall time, so evicting an idle one loses nothing except a
// few bytes; without it the map would grow with every key ever seen.
const idleEviction = 10 * time.Minute

// Limiter is a concurrency-safe set of token buckets keyed by an opaque string.
// The zero value is not usable; call New.
type Limiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
	now       func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a limiter that uses the wall clock.
func New() *Limiter {
	return NewWithClock(time.Now)
}

// NewWithClock returns a limiter driven by now, so tests can advance time
// without sleeping.
func NewWithClock(now func() time.Time) *Limiter {
	return &Limiter{
		buckets:   make(map[string]*bucket),
		lastSweep: now(),
		now:       now,
	}
}

// Result describes one decision.
type Result struct {
	Allowed bool
	// Limit is the configured requests-per-minute.
	Limit int
	// Remaining is the number of whole tokens left after this request.
	Remaining int
	// RetryAfter is how long until the next token is available. It is zero
	// when the request was allowed.
	RetryAfter time.Duration
}

// Allow consumes one token from key's bucket. ratePerMin is the sustained rate
// and burst is the bucket size; both must be positive or the call panics, since
// a zero limit is a configuration bug rather than a runtime condition.
func (l *Limiter) Allow(key string, ratePerMin, burst int) Result {
	if ratePerMin <= 0 || burst <= 0 {
		panic("ratelimit: rate and burst must be positive")
	}

	perSecond := float64(ratePerMin) / 60
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) >= idleEviction {
		l.sweep(now)
		l.lastSweep = now
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(burst), last: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * perSecond
			if b.tokens > float64(burst) {
				b.tokens = float64(burst)
			}
			b.last = now
		}
	}

	if b.tokens < 1 {
		missing := 1 - b.tokens
		retry := time.Duration(missing / perSecond * float64(time.Second))
		if retry < time.Second {
			// Never tell a client to retry "immediately"; a sub-second slot
			// would just burn another request.
			retry = time.Second
		}
		return Result{Allowed: false, Limit: ratePerMin, RetryAfter: retry}
	}

	b.tokens--
	return Result{Allowed: true, Limit: ratePerMin, Remaining: int(b.tokens)}
}

// sweep drops buckets that have been idle long enough to have refilled
// completely, so forgetting them changes no future decision.
func (l *Limiter) sweep(now time.Time) {
	for key, b := range l.buckets {
		if now.Sub(b.last) >= idleEviction {
			delete(l.buckets, key)
		}
	}
}

// Len reports how many buckets are tracked. It exists for tests that assert
// eviction actually happens.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
