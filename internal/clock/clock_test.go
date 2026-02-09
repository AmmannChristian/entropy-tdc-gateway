package clock

import (
	"testing"
	"time"
)

func TestRealClock_Now(t *testing.T) {
	t.Parallel()

	clock := RealClock{}

	before := time.Now()
	now := clock.Now()
	after := time.Now()

	// Verify Now() returns current time within reasonable bounds
	if now.Before(before) {
		t.Fatalf("clock.Now() returned time before test start: %v < %v", now, before)
	}
	if now.After(after) {
		t.Fatalf("clock.Now() returned time after test end: %v > %v", now, after)
	}

	// Verify monotonic property: two successive calls should advance
	now1 := clock.Now()
	time.Sleep(1 * time.Millisecond)
	now2 := clock.Now()

	if !now2.After(now1) {
		t.Fatalf("clock.Now() not monotonic: now1=%v, now2=%v", now1, now2)
	}

	// Verify difference is reasonable (< 10ms for this simple test)
	diff := now2.Sub(now1)
	if diff < 0 || diff > 10*time.Millisecond {
		t.Fatalf("unexpected time difference between successive Now() calls: %v", diff)
	}
}

func TestRealClock_After(t *testing.T) {
	t.Parallel()

	clock := RealClock{}

	// Test short duration
	deadline := 2 * time.Millisecond
	start := time.Now()
	ch := clock.After(deadline)

	select {
	case received := <-ch:
		elapsed := time.Since(start)
		// Verify channel signaled after approximately the deadline
		if elapsed < deadline {
			t.Fatalf("After() signaled too early: elapsed=%v, deadline=%v", elapsed, deadline)
		}
		if elapsed > deadline+5*time.Millisecond {
			t.Fatalf("After() signaled too late: elapsed=%v, deadline=%v", elapsed, deadline)
		}
		// Verify received time is approximately when signal was sent
		if received.Before(start.Add(deadline)) {
			t.Fatalf("received time too early: %v", received)
		}
	case <-time.After(20 * time.Millisecond):
		t.Fatal("After() channel did not signal within timeout")
	}
}

func TestRealClock_AfterZero(t *testing.T) {
	t.Parallel()

	clock := RealClock{}

	// Test zero duration should signal immediately
	ch := clock.After(0)

	select {
	case <-ch:
		// Expected immediate signal
	case <-time.After(10 * time.Millisecond):
		t.Fatal("After(0) did not signal immediately")
	}
}

func TestRealClock_AfterNegative(t *testing.T) {
	t.Parallel()

	clock := RealClock{}

	// Test negative duration (should behave like After(0) per time.After docs)
	ch := clock.After(-1 * time.Second)

	select {
	case <-ch:
		// Expected immediate signal for negative duration
	case <-time.After(10 * time.Millisecond):
		t.Fatal("After(negative) did not signal immediately")
	}
}
