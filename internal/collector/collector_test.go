package collector

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"entropy-tdc-gateway/testutil"

	"entropy-tdc-gateway/internal/clock"
	"entropy-tdc-gateway/internal/mqtt"
)

type sendCall struct {
	events   []mqtt.TDCEvent
	sequence uint32
}

type recordingSender struct {
	mu      sync.Mutex
	calls   []sendCall
	errFn   func(uint32) error
	dropped uint32
}

func (s *recordingSender) IncrementDropped(n uint32) {
	s.mu.Lock()
	s.dropped += n
	s.mu.Unlock()
}

func (s *blockingSender) IncrementDropped(n uint32) {
}

func newRecordingSender() *recordingSender {
	return &recordingSender{}
}

type blockingSender struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingSender() *blockingSender {
	return &blockingSender{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (s *blockingSender) SendBatch(events []mqtt.TDCEvent, sequence uint32) error {
	s.started <- struct{}{}
	<-s.release
	return nil
}

func (s *blockingSender) waitForStart(t *testing.T) {
	t.Helper()
	_, err := testutil.WaitForCondition(context.Background(), func() (struct{}, bool) {
		select {
		case <-s.started:
			return struct{}{}, true
		default:
			return struct{}{}, false
		}
	})
	if err != nil {
		t.Fatalf("timeout waiting for SendBatch to start: %v", err)
	}
}

func (s *blockingSender) allowSend() {
	s.release <- struct{}{}
}

func (s *recordingSender) SendBatch(events []mqtt.TDCEvent, sequence uint32) error {
	copied := make([]mqtt.TDCEvent, len(events))
	copy(copied, events)
	call := sendCall{events: copied, sequence: sequence}

	s.mu.Lock()
	s.calls = append(s.calls, call)
	errFn := s.errFn
	s.mu.Unlock()

	if errFn != nil {
		return errFn(sequence)
	}
	return nil
}

func (s *recordingSender) setErrorFunc(fn func(uint32) error) {
	s.mu.Lock()
	s.errFn = fn
	s.mu.Unlock()
}

func (s *recordingSender) awaitSendCalls(t *testing.T, expected int) []sendCall {
	t.Helper()
	calls, err := testutil.WaitForCondition[[]sendCall](context.Background(), func() ([]sendCall, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.calls) >= expected {
			out := make([]sendCall, len(s.calls))
			copy(out, s.calls)
			return out, true
		}
		return nil, false
	})
	if err != nil {
		t.Fatalf("timeout waiting for %d calls: %v", expected, err)
	}
	return calls
}

func TestCollector_FlushesWhenBatchFull(t *testing.T) {
	t.Parallel()
	testutil.ResetRegistryForTest(t)

	sender := newRecordingSender()
	collector := NewBatchCollectorWithFlush(3, time.Hour, sender, WithClock(clock.NewFakeClock()))
	t.Cleanup(collector.Close)

	events := []mqtt.TDCEvent{
		{RpiTimestampUs: 1, Channel: 0, TdcTimestampPs: 1},
		{RpiTimestampUs: 2, Channel: 1, TdcTimestampPs: 2},
		{RpiTimestampUs: 3, Channel: 2, TdcTimestampPs: 3},
		{RpiTimestampUs: 4, Channel: 3, TdcTimestampPs: 4},
	}

	for _, evt := range events[:3] {
		collector.Add(evt)
	}

	call := sender.awaitSendCalls(t, 1)[0]
	if call.sequence != 1 {
		t.Fatalf("expected sequence 1, got %d", call.sequence)
	}
	if len(call.events) != 3 {
		t.Fatalf("expected batch size 3, got %d", len(call.events))
	}

	collector.Add(events[3])
	collector.Close()

	calls := sender.awaitSendCalls(t, 2)
	if len(calls) != 2 {
		t.Fatalf("expected 2 sends, got %d", len(calls))
	}
	if calls[1].sequence != 2 || len(calls[1].events) != 1 {
		t.Fatalf("unexpected final batch: %+v", calls[1])
	}
}

func TestCollector_CloseFlushesPendingEvents(t *testing.T) {
	t.Parallel()
	testutil.ResetRegistryForTest(t)

	sender := newRecordingSender()
	collector := NewBatchCollectorWithFlush(4, time.Hour, sender, WithClock(clock.NewFakeClock()))

	for i := 0; i < 3; i++ {
		collector.Add(mqtt.TDCEvent{RpiTimestampUs: uint64(i + 1), Channel: uint32(i % 2), TdcTimestampPs: uint64(i + 100)})
	}

	collector.Close()

	calls := sender.awaitSendCalls(t, 1)
	if len(calls) != 1 {
		t.Fatalf("expected one batch on close, got %d", len(calls))
	}
	if calls[0].sequence != 1 || len(calls[0].events) != 3 {
		t.Fatalf("unexpected batch sent on close: %+v", calls[0])
	}
}

func TestCollector_ForwarderErrorDoesNotResetSequence(t *testing.T) {
	t.Parallel()
	testutil.ResetRegistryForTest(t)

	sender := newRecordingSender()
	sender.setErrorFunc(func(seq uint32) error {
		if seq == 1 {
			return context.DeadlineExceeded
		}
		return nil
	})

	collector := NewBatchCollectorWithFlush(2, time.Hour, sender, WithClock(clock.NewFakeClock()))

	events := []mqtt.TDCEvent{
		{RpiTimestampUs: 1, Channel: 0, TdcTimestampPs: 1},
		{RpiTimestampUs: 2, Channel: 1, TdcTimestampPs: 2},
		{RpiTimestampUs: 3, Channel: 2, TdcTimestampPs: 3},
		{RpiTimestampUs: 4, Channel: 3, TdcTimestampPs: 4},
	}

	for _, evt := range events {
		collector.Add(evt)
	}
	collector.Close()

	calls := sender.awaitSendCalls(t, 2)
	if len(calls) != 2 {
		t.Fatalf("expected two batches despite initial error, got %d", len(calls))
	}
	if calls[0].sequence != 1 || calls[1].sequence != 2 {
		t.Fatalf("sequence numbers should advance monotonically: %+v", calls)
	}
}

func TestCollector_ConcurrentAddNoRaces(t *testing.T) {
	t.Parallel()
	testutil.ResetRegistryForTest(t)

	sender := newRecordingSender()
	const batchSize = 25
	collector := NewBatchCollectorWithFlush(batchSize, time.Hour, sender, WithClock(clock.NewFakeClock()))

	const goroutines = 50
	const perWorker = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				idx := g*perWorker + i
				collector.Add(mqtt.TDCEvent{
					RpiTimestampUs: uint64(idx + 1),
					Channel:        uint32(idx % 4),
					TdcTimestampPs: uint64(1_000 + idx),
				})
			}
		}()
	}

	wg.Wait()
	collector.Close()

	expectedBatches := (goroutines*perWorker + batchSize - 1) / batchSize
	calls := sender.awaitSendCalls(t, expectedBatches)
	received := make(map[uint64]struct{}, goroutines*perWorker)
	for _, call := range calls {
		for _, evt := range call.events {
			if _, dup := received[evt.RpiTimestampUs]; dup {
				t.Fatalf("duplicate event detected: %d", evt.RpiTimestampUs)
			}
			received[evt.RpiTimestampUs] = struct{}{}
		}
	}

	expected := goroutines * perWorker
	if len(received) != expected {
		t.Fatalf("expected %d unique events, got %d", expected, len(received))
	}
}

func TestCollector_AutoFlushOnTimer(t *testing.T) {
	t.Parallel()
	testutil.ResetRegistryForTest(t)

	sender := newRecordingSender()
	fakeClock := clock.NewFakeClock()
	collector := NewBatchCollectorWithFlush(4, time.Hour, sender, WithClock(fakeClock))
	t.Cleanup(collector.Close)

	collector.Add(mqtt.TDCEvent{RpiTimestampUs: 1, Channel: 1, TdcTimestampPs: 100})
	collector.Add(mqtt.TDCEvent{RpiTimestampUs: 2, Channel: 2, TdcTimestampPs: 200})

	fakeClock.Fire()
	calls := sender.awaitSendCalls(t, 1)
	if got := len(calls[0].events); got != 2 {
		t.Fatalf("auto flush batch size = %d, want 2", got)
	}
}

func TestCollector_CloseRespectsTimeoutWhenSendStalls(t *testing.T) {
	t.Parallel()
	testutil.ResetRegistryForTest(t)

	fakeClock := clock.NewFakeClock()
	sender := newBlockingSender()
	collector := NewBatchCollectorWithFlush(1, time.Hour, sender, WithClock(fakeClock))

	collector.Add(mqtt.TDCEvent{RpiTimestampUs: 1, Channel: 0, TdcTimestampPs: 42})
	sender.waitForStart(t)

	done := make(chan struct{})
	go func() {
		collector.Close()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Close returned before timeout event")
	default:
	}

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				fakeClock.Fire()
				runtime.Gosched()
			}
		}
	}()

	if _, err := testutil.WaitForCondition(context.Background(), func() (struct{}, bool) {
		select {
		case <-done:
			return struct{}{}, true
		default:
			return struct{}{}, false
		}
	}); err != nil {
		t.Fatalf("Close did not return after timeout: %v", err)
	}
	close(stop)

	sender.allowSend()
}

// TestNewBatchCollector exercises default constructor
func TestNewBatchCollector(t *testing.T) {
	t.Parallel()
	testutil.ResetRegistryForTest(t)

	sender := newRecordingSender()
	collector := NewBatchCollector(5, sender)
	t.Cleanup(collector.Close)

	if collector.maxSize != 5 {
		t.Errorf("maxSize = %d, want 5", collector.maxSize)
	}
	if collector.flushInterval != 10*time.Second {
		t.Errorf("flushInterval = %v, want 10s", collector.flushInterval)
	}
}

// TestIncrementDropped verifies dropped count forwarding
func TestIncrementDropped(t *testing.T) {
	t.Parallel()
	testutil.ResetRegistryForTest(t)

	sender := newRecordingSender()
	collector := NewBatchCollector(10, sender)
	t.Cleanup(collector.Close)

	collector.IncrementDropped(7)
	collector.IncrementDropped(3)

	sender.mu.Lock()
	got := sender.dropped
	sender.mu.Unlock()

	if got != 10 {
		t.Errorf("dropped = %d, want 10", got)
	}
}

// TestIncrementDropped_NilForwarder ensures no panic when forwarder is nil
func TestIncrementDropped_NilForwarder(t *testing.T) {
	t.Parallel()
	testutil.ResetRegistryForTest(t)

	collector := &BatchCollector{forwarder: nil}
	collector.IncrementDropped(42) // should not panic
}
