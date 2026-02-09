package clock

import (
	"sync"
	"time"
)

// FakeClock delivers deterministic timer signals for tests.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []chan time.Time
	pending int
}

// NewFakeClock constructs a fake clock with buffered notifications.
func NewFakeClock() *FakeClock {
	return &FakeClock{now: time.Unix(0, 0)}
}

// Now returns a fixed reference point to keep ordering deterministic.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After registers a waiter for the next Fire invocation.
func (f *FakeClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	f.mu.Lock()
	if f.pending > 0 {
		f.pending--
		now := f.now
		f.mu.Unlock()
		ch <- now
		return ch
	}
	f.waiters = append(f.waiters, ch)
	f.mu.Unlock()
	return ch
}

// Fire delivers a single timer event to all current waiters.
func (f *FakeClock) Fire() {
	f.mu.Lock()
	if len(f.waiters) == 0 {
		f.pending++
		f.mu.Unlock()
		return
	}
	waiters := append([]chan time.Time(nil), f.waiters...)
	now := f.now
	f.waiters = nil
	f.mu.Unlock()

	for _, ch := range waiters {
		ch <- now
	}
}

// Advance moves the notion of current time for subsequent Now() calls.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}
