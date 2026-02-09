package clock

import "time"

// Clock abstracts time progression for components that need deterministic tests.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// RealClock delegates to the standard library for production use.
type RealClock struct{}

// Now returns the current wall-clock time.
func (RealClock) Now() time.Time {
	return time.Now()
}

// After relays to time.After for real scheduling.
func (RealClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}
