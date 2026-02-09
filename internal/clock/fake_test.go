package clock

import (
	"testing"
	"time"
)

func TestFakeClockFireDeliversToAllWaiters(t *testing.T) {
	t.Parallel()

	clk := NewFakeClock()
	first := clk.After(time.Second)
	second := clk.After(10 * time.Second)

	clk.Fire()

	select {
	case <-first:
	default:
		t.Fatal("first waiter did not receive tick")
	}

	select {
	case <-second:
	default:
		t.Fatal("second waiter did not receive tick")
	}
}

func TestFakeClockReuseAfterFire(t *testing.T) {
	t.Parallel()

	clk := NewFakeClock()
	initial := clk.After(time.Millisecond)
	clk.Fire()
	<-initial

	clk.Advance(42 * time.Second)

	next := clk.After(time.Hour)
	clk.Fire()

	select {
	case ts := <-next:
		if !ts.Equal(clk.Now()) {
			t.Fatalf("expected tick to use clock now=%v, got %v", clk.Now(), ts)
		}
	default:
		t.Fatal("second waiter did not receive tick")
	}
}

func TestFakeClock_PendingFireConsumedByAfter(t *testing.T) {
	clk := NewFakeClock()
	clk.Fire()
	clk.Fire() // two pending
	a := <-clk.After(time.Second)
	b := <-clk.After(time.Second)
	if !a.Equal(clk.Now()) || !b.Equal(clk.Now()) {
		t.Fatal("pending ticks mismatch")
	}
	select {
	case <-clk.After(time.Second):
		t.Fatal("unexpected third immediate tick")
	default:
	}
}
