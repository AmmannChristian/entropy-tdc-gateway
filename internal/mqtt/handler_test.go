package mqtt

import (
	"sync"
	"testing"

	testutil "entropy-tdc-gateway/testutil"

	"entropy-tdc-gateway/internal/metrics"

	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

type recordingCollector struct {
	mu      sync.Mutex
	events  []TDCEvent
	dropped uint32
}

func (r *recordingCollector) Add(event TDCEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recordingCollector) IncrementDropped(n uint32) {
	r.mu.Lock()
	r.dropped += n
	r.mu.Unlock()
}

func (r *recordingCollector) Dropped() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

func (r *recordingCollector) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *recordingCollector) Events() []TDCEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TDCEvent, len(r.events))
	copy(out, r.events)
	return out
}

func TestRxHandler_ValidRawTimestamp(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	collector := &recordingCollector{}
	handler := RxHandler{Collector: collector}

	// Raw timestamp format from fifo2mqtt/lobsang
	payload := []byte("987654321000")
	topic := "timestamps/channel/1"

	handler.OnMessage(topic, payload)

	if got := collector.Count(); got != 1 {
		t.Fatalf("collector count mismatch: got %d, want 1", got)
	}

	events := collector.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	event := events[0]
	if event.TdcTimestampPs != 987654321000 {
		t.Errorf("tdc_timestamp_ps mismatch: got %d, want 987654321000", event.TdcTimestampPs)
	}
	if event.Channel != 1 {
		t.Errorf("channel mismatch: got %d, want 1", event.Channel)
	}
	if event.RpiTimestampUs == 0 {
		t.Error("rpi_timestamp_us should not be zero")
	}

	if got := promtest.ToFloat64(metrics.EventsProcessed); got != 1 {
		t.Fatalf("events processed mismatch: got %f, want 1", got)
	}

	if got := promtest.ToFloat64(metrics.EventsReceived.WithLabelValues("1")); got != 1 {
		t.Fatalf("events received mismatch: got %f, want 1", got)
	}
}

func TestRxHandler_InvalidTimestamp(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	collector := &recordingCollector{}
	handler := RxHandler{Collector: collector}

	handler.OnMessage("timestamps/channel/1", []byte("not a number"))

	if got := collector.Count(); got != 0 {
		t.Fatalf("collector should not receive events, got %d", got)
	}

	if got := promtest.ToFloat64(metrics.EventsDropped.WithLabelValues("parse_error")); got != 1 {
		t.Fatalf("parse_error metric mismatch: got %f, want 1", got)
	}
}

func TestRxHandler_ZeroTimestamp(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	collector := &recordingCollector{}
	handler := RxHandler{Collector: collector}

	handler.OnMessage("timestamps/channel/2", []byte("0"))

	if got := collector.Count(); got != 0 {
		t.Fatalf("collector should drop zero timestamp events, got %d", got)
	}

	if got := promtest.ToFloat64(metrics.EventsDropped.WithLabelValues("invalid_timestamp")); got != 1 {
		t.Fatalf("invalid_timestamp metric mismatch: got %f, want 1", got)
	}
}

func TestRxHandler_NilCollectorDoesNotPanic(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	handler := RxHandler{}

	handler.OnMessage("timestamps/channel/3", []byte("123456789"))

	if got := promtest.ToFloat64(metrics.EventsProcessed); got != 1 {
		t.Fatalf("events processed mismatch: got %f, want 1", got)
	}
}

func TestRxHandler_MultipleChannels(t *testing.T) {
	t.Parallel()

	testutil.ResetRegistryForTest(t)

	collector := &recordingCollector{}
	handler := RxHandler{Collector: collector}

	testCases := []struct {
		topic   string
		payload string
		channel uint32
	}{
		{"timestamps/channel/1", "111111111111", 1},
		{"timestamps/channel/2", "222222222222", 2},
		{"timestamps/channel/3", "333333333333", 3},
		{"timestamps/channel/4", "444444444444", 4},
	}

	for _, tc := range testCases {
		handler.OnMessage(tc.topic, []byte(tc.payload))
	}

	if got := collector.Count(); got != 4 {
		t.Fatalf("collector count mismatch: got %d, want 4", got)
	}

	events := collector.Events()
	for i, tc := range testCases {
		if events[i].Channel != tc.channel {
			t.Errorf("event %d: channel mismatch: got %d, want %d", i, events[i].Channel, tc.channel)
		}
	}
}

func TestExtractChannelFromTopic(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		topic   string
		channel uint32
	}{
		{"timestamps/channel/1", 1},
		{"timestamps/channel/2", 2},
		{"timestamps/channel/10", 10},
		{"channel/5", 5},
		{"invalid", 0},
		{"", 0},
	}

	for _, tc := range testCases {
		got := extractChannelFromTopic(tc.topic)
		if got != tc.channel {
			t.Errorf("extractChannelFromTopic(%q) = %d, want %d", tc.topic, got, tc.channel)
		}
	}
}

func TestNowMicrosSafe(t *testing.T) {
	t.Parallel()

	original := timeNowMicros
	t.Cleanup(func() { timeNowMicros = original })

	t.Run("negative timestamp returns zero", func(t *testing.T) {
		timeNowMicros = func() int64 { return -10 }
		if got := nowMicrosSafe(); got != 0 {
			t.Fatalf("expected 0 for negative timestamp, got %d", got)
		}
	})

	t.Run("positive timestamp preserved", func(t *testing.T) {
		expected := int64(123456)
		timeNowMicros = func() int64 { return expected }
		if got := nowMicrosSafe(); got != uint64(expected) {
			t.Fatalf("expected %d, got %d", expected, got)
		}
	})
}
