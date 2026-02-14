// Package entropy manages the whitened entropy pool fed by TDC decay events
// and exposes conditioned random bytes over an HTTP API.
package entropy

import (
	"encoding/binary"
	"log"
	"sync"

	"entropy-tdc-gateway/internal/metrics"
	"entropy-tdc-gateway/internal/mqtt"
	"entropy-tdc-gateway/internal/validation"
)

const (
	defaultMinWhitenedBytes   = 32
	whitenedBufferMultiplier  = 4
	defaultMaxRawEventsStored = 4096
)

// WhitenedPool manages a concurrency-safe buffer of conditioned entropy bytes
// derived from TDC decay events. It maintains both a bounded history of raw
// events for diagnostics and a whitened byte pool for consumption. Continuous
// health monitoring is performed via NIST SP 800-90B Repetition Count Test
// (RCT) and Adaptive Proportion Test (APT) on every extraction.
type WhitenedPool struct {
	mu           sync.RWMutex
	rawEvents    []mqtt.TDCEvent
	whitened     []byte
	minPoolSize  int
	maxPoolSize  int
	maxRawEvents int
	rct          *validation.RepetitionCountTest
	apt          *validation.AdaptiveProportionTest
}

// NewWhitenedPool constructs a pool that keeps at least minSize bytes available
// for extraction while bounding total storage. When minSize is non-positive a
// conservative default is used. The maximum pool size defaults to
// minSize * whitenedBufferMultiplier.
func NewWhitenedPool(minSize int) *WhitenedPool {
	if minSize <= 0 {
		minSize = defaultMinWhitenedBytes
	}

	maxSize := minSize * whitenedBufferMultiplier
	if maxSize < minSize {
		maxSize = minSize
	}

	return NewWhitenedPoolWithBounds(minSize, maxSize)
}

// NewWhitenedPoolWithBounds constructs a pool with explicit lower and upper
// bounds on whitened bytes retained. When the provided values are invalid the
// function falls back to safe defaults.
func NewWhitenedPoolWithBounds(minSize, maxSize int) *WhitenedPool {
	if minSize <= 0 {
		minSize = defaultMinWhitenedBytes
	}
	if maxSize <= 0 {
		maxSize = minSize * whitenedBufferMultiplier
	}
	if maxSize < minSize {
		maxSize = minSize
	}

	return &WhitenedPool{
		minPoolSize:  minSize,
		maxPoolSize:  maxSize,
		maxRawEvents: defaultMaxRawEventsStored,
		rct:          validation.NewRepetitionCountTest(40),           // NIST SP 800-90B RCT: cutoff for α=2^-40
		apt:          validation.NewAdaptiveProportionTest(605, 4096), // NIST SP 800-90B APT: cutoff=605, window=4096 for α=2^-40, H=0.5
	}
}

// AddBatch ingests a slice of TDC events, serialises their picosecond
// timestamps into a raw byte stream, applies dual-stage whitening (parity
// preprocessing followed by SHA-256 conditioning), records validation metrics,
// and appends the conditioned output to the pool. Both the raw event history
// and the whitened buffer are trimmed to their respective upper bounds.
//
// NOTE: this local pool whitening path serves the gateway's entropy HTTP API.
// It is intentionally independent from per-event `whitened_entropy` values
// attached to gRPC cloud forwarding messages.
// This method is safe for concurrent use.
func (pool *WhitenedPool) AddBatch(events []mqtt.TDCEvent) {
	if len(events) == 0 {
		return
	}

	rawBytes := make([]byte, len(events)*8)
	for index, event := range events {
		binary.LittleEndian.PutUint64(rawBytes[index*8:], event.TdcTimestampPs)
	}

	whitenedBytes := DualStageWhitening(rawBytes)

	ratio := 0.0
	if len(rawBytes) > 0 && len(whitenedBytes) > 0 {
		ratio = float64(len(whitenedBytes)) / float64(len(rawBytes))
	}

	metrics.RecordWhitening(len(rawBytes), len(whitenedBytes), ratio)

	// Run quick validation tests on whitened bytes
	if len(whitenedBytes) > 0 {
		freqRatio, freqPassed := validation.FrequencyTest(whitenedBytes)
		runsCount, runsPassed := validation.RunsTest(whitenedBytes)
		metrics.RecordValidation(freqRatio, freqPassed, runsCount, runsPassed)

		// Calculate and record min-entropy estimates (NIST SP 800-90B)
		minEntropyMCV := validation.EstimateMCV(whitenedBytes)
		minEntropyCollision := validation.EstimateCollision(whitenedBytes)
		metrics.RecordMinEntropyMCV(minEntropyMCV)
		metrics.RecordMinEntropyCollision(minEntropyCollision)
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	pool.rawEvents = append(pool.rawEvents, events...)
	if len(pool.rawEvents) > pool.maxRawEvents {
		trim := len(pool.rawEvents) - pool.maxRawEvents
		copy(pool.rawEvents, pool.rawEvents[trim:])
		pool.rawEvents = pool.rawEvents[:len(pool.rawEvents)-trim]
	}

	if len(whitenedBytes) == 0 {
		return
	}

	pool.whitened = append(pool.whitened, whitenedBytes...)
	if len(pool.whitened) > pool.maxPoolSize {
		trim := len(pool.whitened) - pool.maxPoolSize
		copy(pool.whitened, pool.whitened[trim:])
		pool.whitened = pool.whitened[:len(pool.whitened)-trim]
	}

	metrics.SetEntropyPoolSize(len(pool.whitened))
}

// ExtractEntropy removes and returns exactly numBytes of whitened entropy from
// the pool. It returns (nil, false) when numBytes is non-positive, the pool is
// below its minimum reserve, insufficient bytes are available, or a NIST
// SP 800-90B continuous health test (RCT or APT) detects a fault. On health
// test failure the corresponding test state is reset to permit recovery.
// This method is safe for concurrent use.
func (pool *WhitenedPool) ExtractEntropy(numBytes int) ([]byte, bool) {
	if numBytes <= 0 {
		return nil, false
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	if len(pool.whitened) < pool.minPoolSize {
		return nil, false
	}

	if len(pool.whitened) < numBytes {
		return nil, false
	}

	output := make([]byte, numBytes)
	copy(output, pool.whitened[:numBytes])

	if !pool.rct.TestBlock(output) {
		log.Printf("entropy: RCT failure detected,potential entropy source fault")
		metrics.RecordContinuousRCTFailure()
		metrics.RecordEventDropped("rct_failure")
		pool.rct.Reset()
		return nil, false
	}

	if !pool.apt.TestBlock(output) {
		log.Printf("entropy: APT failure detected,statistical bias in entropy source")
		metrics.RecordContinuousAPTFailure()
		metrics.RecordEventDropped("apt_failure")
		pool.apt.Reset()
		return nil, false
	}

	pool.whitened = append([]byte(nil), pool.whitened[numBytes:]...)

	metrics.RecordEntropyExtraction(numBytes)
	metrics.SetEntropyPoolSize(len(pool.whitened))

	return output, true
}

// PoolStatus returns the current number of diagnostic raw events tracked along
// with the number of whitened bytes stored in the pool.
func (pool *WhitenedPool) PoolStatus() (int, int) {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return len(pool.rawEvents), len(pool.whitened)
}

// AvailableEntropy reports the number of bytes currently ready for extraction
// after respecting the configured minimum pool size.
func (pool *WhitenedPool) AvailableEntropy() int {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	if len(pool.whitened) <= pool.minPoolSize {
		return 0
	}

	return len(pool.whitened) - pool.minPoolSize
}
