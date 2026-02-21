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
// This function holds a package-level lock for the entire test duration to
// prevent data races while swapping metric registries. As a result, tests that
// depend on this helper execute serially.
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

// WaitForCondition polls probe until it reports success or the context is
// cancelled.
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

// WaitForError blocks until ch yields a value and returns that value.
// The helper fails the test only if WaitForCondition returns an error.
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
