// Package collector aggregates TDC events into fixed-size batches and
// dispatches them to a configurable sender. Batches are flushed when full
// or after a periodic interval, whichever comes first.
package collector

import (
	"context"
	"log"
	"sync"
	"time"

	"entropy-tdc-gateway/internal/clock"
	"entropy-tdc-gateway/internal/metrics"
	"entropy-tdc-gateway/internal/mqtt"
)

// BatchSender abstracts the downstream consumer of event batches. Production
// implementations forward batches to the whitened entropy pool and optionally
// to a cloud gRPC stream. Test doubles may implement this interface to verify
// dispatch behaviour without network I/O.
type BatchSender interface {
	SendBatch(events []mqtt.TDCEvent, sequence uint32) error
	IncrementDropped(count uint32)
}

// Option applies an optional configuration to a BatchCollector during
// construction.
type Option func(*BatchCollector)

// WithClock injects a custom clock for deterministic flush timing in tests.
func WithClock(clockSource clock.Clock) Option {
	return func(bc *BatchCollector) {
		bc.clockSource = clockSource
	}
}

// BatchCollector buffers TDC events and dispatches them as batches to a
// BatchSender. Events are flushed when the buffer reaches maxSize or when
// the periodic auto-flush interval elapses. A single-goroutine send loop
// preserves dispatch ordering. All methods are safe for concurrent use.
type BatchCollector struct {
	events             []mqtt.TDCEvent
	maxSize            int
	forwarder          BatchSender
	flushInterval      time.Duration
	clockSource        clock.Clock
	mu                 sync.Mutex
	pendingSendGroup   sync.WaitGroup
	sendWorkerGroup    sync.WaitGroup
	autoFlushGroup     sync.WaitGroup
	sendQueue          chan sendRequest
	closeSendQueueOnce sync.Once
	sequence           uint32
	ctx                context.Context
	cancel             context.CancelFunc
}

type sendRequest struct {
	batch []mqtt.TDCEvent
	seq   uint32
}

// NewBatchCollector creates a BatchCollector with a default flush interval of
// ten seconds. See NewBatchCollectorWithFlush for custom intervals.
func NewBatchCollector(maxSize int, fwd BatchSender, opts ...Option) *BatchCollector {
	return NewBatchCollectorWithFlush(maxSize, 10*time.Second, fwd, opts...)
}

// NewBatchCollectorWithFlush creates a BatchCollector with the given flush
// interval. It starts a background send loop and an auto-flush goroutine
// immediately. Call Close to release both goroutines.
func NewBatchCollectorWithFlush(maxSize int, flushInterval time.Duration, fwd BatchSender, opts ...Option) *BatchCollector {
	ctx, cancel := context.WithCancel(context.Background())
	bc := &BatchCollector{
		events:        make([]mqtt.TDCEvent, 0, maxSize),
		maxSize:       maxSize,
		forwarder:     fwd,
		flushInterval: flushInterval,
		clockSource:   clock.RealClock{},
		sequence:      0,
		ctx:           ctx,
		cancel:        cancel,
	}

	bc.sendQueue = make(chan sendRequest, maxInt(1, maxSize/2))
	bc.sendWorkerGroup.Add(1)
	go bc.runSendLoop()

	for _, opt := range opts {
		opt(bc)
	}

	if bc.clockSource == nil {
		bc.clockSource = clock.RealClock{}
	}

	// Auto-flush every 10 seconds even if batch not full
	bc.autoFlushGroup.Add(1)
	go bc.autoFlush()
	return bc
}

// Add appends an event to the current batch. When the batch reaches its
// maximum size it is flushed to the send queue automatically.
func (bc *BatchCollector) Add(event mqtt.TDCEvent) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	bc.events = append(bc.events, event)
	metrics.SetCollectorPoolSize(len(bc.events))

	if len(bc.events) >= bc.maxSize {
		bc.flush()
	}
}

// IncrementDropped delegates the dropped event count to the underlying
// BatchSender for metrics recording.
func (bc *BatchCollector) IncrementDropped(count uint32) {
	if bc.forwarder != nil {
		bc.forwarder.IncrementDropped(count)
	}
}

// flush enqueues the current event buffer for asynchronous dispatch and resets
// the buffer. The caller must hold bc.mu.
func (bc *BatchCollector) flush() {
	if len(bc.events) == 0 {
		return
	}

	start := bc.clockSource.Now()

	batch := make([]mqtt.TDCEvent, len(bc.events))
	copy(batch, bc.events)
	bc.sequence++
	seq := bc.sequence

	bc.events = bc.events[:0]
	metrics.SetCollectorPoolSize(0)

	if cap(bc.events) > bc.maxSize*2 && len(batch) < bc.maxSize/4 {
		oldCap := cap(bc.events)
		bc.events = make([]mqtt.TDCEvent, 0, bc.maxSize)
		log.Printf("collector: buffer reallocated (was %d, now %d)", oldCap, bc.maxSize)
	}

	flushDuration := bc.clockSource.Now().Sub(start)
	metrics.RecordCollectorFlush(flushDuration)

	bc.pendingSendGroup.Add(1)
	bc.sendQueue <- sendRequest{batch: batch, seq: seq}
}

// autoFlush periodically flushes incomplete batches so that low-throughput
// periods do not leave events buffered indefinitely.
func (bc *BatchCollector) autoFlush() {
	defer bc.autoFlushGroup.Done()

	interval := bc.flushInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}

	for {
		select {
		case <-bc.ctx.Done():
			return
		case <-bc.clockSource.After(interval):
			bc.mu.Lock()
			bc.flush()
			bc.mu.Unlock()
		}
	}
}

// Close stops the auto-flush goroutine, flushes remaining events, and waits
// up to five seconds for all pending sends to complete.
func (bc *BatchCollector) Close() {
	bc.cancel()

	bc.mu.Lock()
	bc.flush()
	bc.mu.Unlock()

	bc.closeSendQueueOnce.Do(func() {
		close(bc.sendQueue)
	})

	done := make(chan struct{})
	go func() {
		bc.sendWorkerGroup.Wait()
		bc.pendingSendGroup.Wait()
		bc.autoFlushGroup.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("collector: all batches sent")
	case <-bc.clockSource.After(5 * time.Second):
		log.Println("collector: timeout waiting for sends")
	}
}

func (bc *BatchCollector) runSendLoop() {
	defer bc.sendWorkerGroup.Done()
	for job := range bc.sendQueue {
		if err := bc.forwarder.SendBatch(job.batch, job.seq); err != nil {
			log.Printf("collector: failed to send batch %d: %v", job.seq, err)
		}
		bc.pendingSendGroup.Done()
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
