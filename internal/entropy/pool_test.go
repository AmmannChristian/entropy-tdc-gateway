package entropy

import (
	"sync"
	"testing"

	"entropy-tdc-gateway/internal/metrics"
	"entropy-tdc-gateway/internal/mqtt"
	"entropy-tdc-gateway/internal/validation"

	"github.com/prometheus/client_golang/prometheus"
)

const testTimestampPattern = 0xAAAAAAAAAAAAAAAA

func resetMetrics(t *testing.T) {
	t.Helper()
	metrics.ResetForTesting(prometheus.NewRegistry())
}

func deterministicEvents(count int) []mqtt.TDCEvent {
	events := make([]mqtt.TDCEvent, count)
	for index := range events {
		events[index] = mqtt.TDCEvent{TdcTimestampPs: testTimestampPattern + uint64(index)}
	}
	return events
}

func TestPool_AddBatch_Empty(t *testing.T) {
	resetMetrics(t)
	pool := NewWhitenedPool(32)
	pool.AddBatch(nil)
	pool.AddBatch([]mqtt.TDCEvent{})

	rawEventCount, whitenedByteCount := pool.PoolStatus()
	if rawEventCount != 0 {
		t.Fatalf("expected zero raw events, got %d", rawEventCount)
	}
	if whitenedByteCount != 0 {
		t.Fatalf("expected zero whitened bytes, got %d", whitenedByteCount)
	}
}

func TestPool_AddBatch_AccumulatesAndBounds(t *testing.T) {
	resetMetrics(t)
	pool := NewWhitenedPool(32)
	pool.maxPoolSize = 64
	pool.maxRawEvents = 3

	for iteration := 0; iteration < 5; iteration++ {
		pool.AddBatch(deterministicEvents(1))
	}

	rawEventCount, whitenedByteCount := pool.PoolStatus()
	if rawEventCount != 3 {
		t.Fatalf("expected raw event history to be trimmed to 3, got %d", rawEventCount)
	}
	if whitenedByteCount != 64 {
		t.Fatalf("expected whitened bytes capped at 64, got %d", whitenedByteCount)
	}

	available := pool.AvailableEntropy()
	if available != 32 {
		t.Fatalf("expected 32 bytes available after reserving minimum, got %d", available)
	}
}

func TestPool_ExtractEntropy_Insufficient(t *testing.T) {
	resetMetrics(t)
	pool := NewWhitenedPool(32)
	if _, ok := pool.ExtractEntropy(16); ok {
		t.Fatal("expected extraction to fail when pool is empty")
	}
}

func TestPool_ExtractEntropy_Consumes(t *testing.T) {
	resetMetrics(t)
	pool := NewWhitenedPool(0)
	pool.maxPoolSize = 128

	pool.AddBatch(deterministicEvents(1))
	pool.AddBatch(deterministicEvents(1))

	first, ok := pool.ExtractEntropy(32)
	if !ok {
		t.Fatal("expected to extract 32 bytes")
	}
	if len(first) != 32 {
		t.Fatalf("expected output length 32, got %d", len(first))
	}

	_, remaining := pool.PoolStatus()
	if remaining == 0 {
		t.Fatal("expected remaining bytes after first extraction")
	}

	second, ok := pool.ExtractEntropy(32)
	if !ok {
		t.Fatal("expected second extraction to succeed")
	}
	if len(second) != 32 {
		t.Fatalf("expected second extraction length 32, got %d", len(second))
	}

	_, remaining = pool.PoolStatus()
	if remaining != 0 {
		t.Fatalf("expected pool to be empty, found %d bytes", remaining)
	}
}

func TestPool_ParallelAccess(t *testing.T) {
	t.Parallel()
	resetMetrics(t)

	pool := NewWhitenedPool(32)
	pool.maxPoolSize = 256
	pool.maxRawEvents = 32

	var waitGroup sync.WaitGroup

	producer := func(iterations int) {
		defer waitGroup.Done()
		for index := 0; index < iterations; index++ {
			pool.AddBatch(deterministicEvents(1))
		}
	}

	consumer := func(iterations int) {
		defer waitGroup.Done()
		for index := 0; index < iterations; index++ {
			pool.ExtractEntropy(32)
		}
	}

	waitGroup.Add(4)
	go producer(64)
	go producer(64)
	go consumer(64)
	go consumer(64)
	waitGroup.Wait()
}

// TestNewWhitenedPoolWithBounds covers NewWhitenedPoolWithBounds edge cases
func TestNewWhitenedPoolWithBounds(t *testing.T) {
	resetMetrics(t)

	t.Run("negative minSize uses default", func(t *testing.T) {
		pool := NewWhitenedPoolWithBounds(-10, 1024)
		if pool.minPoolSize <= 0 {
			t.Errorf("expected positive minPoolSize, got %d", pool.minPoolSize)
		}
	})

	t.Run("zero minSize uses default", func(t *testing.T) {
		pool := NewWhitenedPoolWithBounds(0, 1024)
		if pool.minPoolSize <= 0 {
			t.Errorf("expected positive minPoolSize, got %d", pool.minPoolSize)
		}
	})

	t.Run("negative maxSize computed from minSize", func(t *testing.T) {
		pool := NewWhitenedPoolWithBounds(512, -1)
		if pool.maxPoolSize <= pool.minPoolSize {
			t.Errorf("expected maxPoolSize > minPoolSize, got max=%d min=%d", pool.maxPoolSize, pool.minPoolSize)
		}
	})

	t.Run("zero maxSize computed from minSize", func(t *testing.T) {
		pool := NewWhitenedPoolWithBounds(512, 0)
		if pool.maxPoolSize <= pool.minPoolSize {
			t.Errorf("expected maxPoolSize > minPoolSize, got max=%d min=%d", pool.maxPoolSize, pool.minPoolSize)
		}
	})

	t.Run("maxSize < minSize adjusted to minSize", func(t *testing.T) {
		pool := NewWhitenedPoolWithBounds(1024, 512)
		if pool.maxPoolSize != pool.minPoolSize {
			t.Errorf("expected maxPoolSize adjusted to minPoolSize (1024), got %d", pool.maxPoolSize)
		}
	})

	t.Run("valid bounds preserved", func(t *testing.T) {
		pool := NewWhitenedPoolWithBounds(256, 2048)
		if pool.minPoolSize != 256 {
			t.Errorf("expected minPoolSize=256, got %d", pool.minPoolSize)
		}
		if pool.maxPoolSize != 2048 {
			t.Errorf("expected maxPoolSize=2048, got %d", pool.maxPoolSize)
		}
	})
}

// TestExtractEntropy_EdgeCases covers ExtractEntropy with edge cases
func TestExtractEntropy_EdgeCases(t *testing.T) {
	resetMetrics(t)

	t.Run("zero bytes returns false", func(t *testing.T) {
		pool := NewWhitenedPool(0)
		pool.AddBatch(deterministicEvents(2))
		_, ok := pool.ExtractEntropy(0)
		if ok {
			t.Error("expected extraction of 0 bytes to fail")
		}
	})

	t.Run("negative bytes returns false", func(t *testing.T) {
		pool := NewWhitenedPool(0)
		pool.AddBatch(deterministicEvents(2))
		_, ok := pool.ExtractEntropy(-10)
		if ok {
			t.Error("expected extraction of negative bytes to fail")
		}
	})

	t.Run("insufficient bytes with minPoolSize gate", func(t *testing.T) {
		pool := NewWhitenedPool(64) // minPoolSize=64
		pool.AddBatch(deterministicEvents(1))
		// Even if some bytes available, minPoolSize gate should prevent extraction
		_, ok := pool.ExtractEntropy(16)
		if ok {
			t.Error("expected extraction to fail when below minPoolSize")
		}
	})

	t.Run("extraction leaves minPoolSize reserve", func(t *testing.T) {
		pool := NewWhitenedPool(32)
		pool.maxPoolSize = 256
		// Add enough events to exceed minPoolSize
		pool.AddBatch(deterministicEvents(10))

		before, _ := pool.PoolStatus()
		if before < 64 {
			t.Skipf("need at least 64 bytes for this test, got %d", before)
		}

		available := pool.AvailableEntropy()
		if available <= 0 {
			t.Fatal("expected some available entropy")
		}

		data, ok := pool.ExtractEntropy(available)
		if !ok {
			t.Fatal("extraction should succeed")
		}
		if len(data) != available {
			t.Errorf("expected %d bytes, got %d", available, len(data))
		}

		_, after := pool.PoolStatus()
		if after != 32 {
			t.Errorf("expected exactly minPoolSize (32) remaining, got %d", after)
		}
	})
}

func TestExtractEntropy_RCTFailure(t *testing.T) {
	resetMetrics(t)
	pool := NewWhitenedPool(0)
	pool.minPoolSize = 0
	pool.maxPoolSize = 8
	pool.whitened = []byte{0xAA, 0xAA}
	pool.rct = validation.NewRepetitionCountTest(1)
	pool.apt = validation.NewAdaptiveProportionTest(605, 4096)

	if _, ok := pool.ExtractEntropy(2); ok {
		t.Fatal("expected extraction to fail due to RCT failure")
	}
}

func TestExtractEntropy_APTFailure(t *testing.T) {
	resetMetrics(t)
	pool := NewWhitenedPool(0)
	pool.minPoolSize = 0
	pool.maxPoolSize = 4
	pool.whitened = []byte{0x01}
	pool.rct = validation.NewRepetitionCountTest(100)
	pool.apt = validation.NewAdaptiveProportionTest(1, 1)

	if _, ok := pool.ExtractEntropy(1); ok {
		t.Fatal("expected extraction to fail due to APT failure")
	}
}
