// Package clock provides the time source business logic uses instead of
// calling time.Now directly. Tests can substitute a fixed clock so expiry,
// rate limit, and token rules are deterministic.
package clock

import "time"

// Clock reports the current time.
type Clock interface {
	Now() time.Time
}

// System is the real clock.
type System struct{}

// Now returns the current wall clock time.
func (System) Now() time.Time { return time.Now() }

// Fixed is a test clock that only moves when advanced.
type Fixed struct {
	Current time.Time
}

// Now returns the fixed time.
func (f *Fixed) Now() time.Time { return f.Current }

// Advance moves the clock forward.
func (f *Fixed) Advance(d time.Duration) { f.Current = f.Current.Add(d) }
