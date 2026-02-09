package testutil

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"entropy-tdc-gateway/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

var registryMu sync.Mutex

// ResetRegistryForTest provides an isolated Prometheus registry for the lifetime
// of the test. It reconfigures the metrics package to use the per-test registry
// and restores the previous registerer once the test completes.
//
// IMPORTANT: This function holds a global lock for the entire test duration to
// prevent data races when swapping metric registries. This means tests using
// metrics will run serially, but this ensures thread safety.
func ResetRegistryForTest(t *testing.T) *prometheus.Registry {
	t.Helper()

	registryMu.Lock()

	reg := prometheus.NewRegistry()
	metrics.ResetForTesting(reg)

	t.Cleanup(func() {
		metrics.ResetForTesting(prometheus.DefaultRegisterer)
		registryMu.Unlock()
	})

	return reg
}

// WaitForCondition cooperatively polls probe until it reports success or the context or iteration budget is exhausted.
func WaitForCondition[T any](ctx context.Context, probe func() (T, bool)) (T, error) {
	var zero T
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()

	for {
		if val, ok := probe(); ok {
			return val, nil
		}

		runtime.Gosched()
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-ticker.C:
		}
	}
}

// WaitForError waits for an error to be sent on the channel with a timeout.
// It returns the error received from the channel, or fails the test on timeout.
func WaitForError(t *testing.T, ch <-chan error, desc string) error {
	t.Helper()
	ctx := context.Background()
	result, err := WaitForCondition(ctx, func() (error, bool) {
		select {
		case err := <-ch:
			return err, true
		default:
			return nil, false
		}
	})
	if err != nil {
		t.Fatalf("timeout waiting for %s: %v", desc, err)
	}
	return result
}
