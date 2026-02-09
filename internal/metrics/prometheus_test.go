package metrics

import (
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

var resetMu sync.Mutex

func withRegistry(t *testing.T, reg *prometheus.Registry) {
	resetMu.Lock()
	ResetForTesting(reg)
	t.Cleanup(func() {
		ResetForTesting(prometheus.DefaultRegisterer)
		resetMu.Unlock()
	})
}

func TestMetrics_RegisterOnce(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	fams1 := gatherFamilies(t, reg)
	if len(fams1) == 0 {
		t.Fatal("expected metrics registered")
	}

	ResetForTesting(reg)
	fams2 := gatherFamilies(t, reg)
	if len(fams1) != len(fams2) {
		t.Fatalf("metric count changed after second reset: %d vs %d", len(fams1), len(fams2))
	}
}

func TestMetrics_TDCEventCounters(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Record events on channels 0-3 and unknown channel
	RecordEvent(0)
	RecordEvent(1)
	RecordEvent(2)
	RecordEvent(3)
	RecordEvent(99) // unknown channel
	RecordEventDropped("parse_error")

	fams := gatherFamilies(t, reg)

	tests := []struct {
		channel string
		want    float64
	}{
		{"0", 1},
		{"1", 1},
		{"2", 1},
		{"3", 1},
		{"unknown", 1},
	}

	for _, tt := range tests {
		got := counterValue(t, fams, "tdc_events_received_total", map[string]string{"channel": tt.channel})
		if got != tt.want {
			t.Errorf("tdc_events_received_total{channel=%s} = %v, want %v", tt.channel, got, tt.want)
		}
	}

	if got := counterValue(t, fams, "tdc_events_processed_total", nil); got != 5 {
		t.Errorf("tdc_events_processed_total = %v, want 5", got)
	}

	if got := counterValue(t, fams, "tdc_events_dropped_total", map[string]string{"reason": "parse_error"}); got != 1 {
		t.Errorf("tdc_events_dropped_total{reason=parse_error} = %v, want 1", got)
	}
}

func TestMetrics_ValidationMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Record validation with frequency test failure
	RecordValidation(0.48, false, 150, true)

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "validation_failures_total", map[string]string{"test_type": "frequency"}); got != 1 {
		t.Errorf("validation_failures_total{test_type=frequency} = %v, want 1", got)
	}

	if got := histogramCount(t, fams, "validation_frequency_ratio"); got != 1 {
		t.Errorf("validation_frequency_ratio sample count = %d, want 1", got)
	}
}

func TestMetrics_BatchMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Record successful and failed batches
	RecordBatch(3, 0.2, true, "")
	RecordBatch(4, 0.0, false, "timeout")

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "batches_created_total", nil); got != 2 {
		t.Errorf("batches_created_total = %v, want 2", got)
	}

	if got := counterValue(t, fams, "batches_sent_total", nil); got != 1 {
		t.Errorf("batches_sent_total = %v, want 1", got)
	}

	if got := counterValue(t, fams, "batches_failed_total", map[string]string{"reason": "timeout"}); got != 1 {
		t.Errorf("batches_failed_total{reason=timeout} = %v, want 1", got)
	}

	if got := histogramCount(t, fams, "batch_size_events"); got != 2 {
		t.Errorf("batch_size_events sample count = %d, want 2", got)
	}
}

func TestMetrics_GRPCConnectionMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Simulate connection lifecycle
	SetGRPCConnected(true)
	SetGRPCConnected(false)
	RecordGRPCReconnect()
	RecordGRPCError("io")

	fams := gatherFamilies(t, reg)

	if got := gaugeValue(t, fams, "grpc_connection_status", nil); got != 0 {
		t.Errorf("grpc_connection_status = %v, want 0 (disconnected)", got)
	}

	if got := counterValue(t, fams, "forwarder_reconnects_total", nil); got != 1 {
		t.Errorf("forwarder_reconnects_total = %v, want 1", got)
	}

	if got := counterValue(t, fams, "grpc_stream_errors_total", map[string]string{"error_type": "io"}); got != 1 {
		t.Errorf("grpc_stream_errors_total{error_type=io} = %v, want 1", got)
	}
}

func TestMetrics_MQTTConnectionMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Simulate MQTT connection lifecycle
	SetMQTTConnected(true)
	RecordMQTTConnect()
	SetMQTTConnected(false)
	RecordMQTTDisconnect()
	RecordMQTTReconnect()
	RecordMQTTMessage()
	RecordMQTTMessage()

	fams := gatherFamilies(t, reg)

	if got := gaugeValue(t, fams, "mqtt_connection_status", nil); got != 0 {
		t.Errorf("mqtt_connection_status = %v, want 0 (disconnected)", got)
	}

	if got := counterValue(t, fams, "mqtt_reconnects_total", nil); got != 1 {
		t.Errorf("mqtt_reconnects_total = %v, want 1", got)
	}

	if got := counterValue(t, fams, "mqtt_connects_total", nil); got != 1 {
		t.Errorf("mqtt_connects_total = %v, want 1", got)
	}

	if got := counterValue(t, fams, "mqtt_disconnects_total", nil); got != 1 {
		t.Errorf("mqtt_disconnects_total = %v, want 1", got)
	}

	if got := counterValue(t, fams, "mqtt_in_msgs_total", nil); got != 2 {
		t.Errorf("mqtt_in_msgs_total = %v, want 2", got)
	}
}

func TestMetrics_CollectorPoolSize(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	SetCollectorPoolSize(5)

	fams := gatherFamilies(t, reg)

	if got := gaugeValue(t, fams, "collector_pool_size_events", nil); got != 5 {
		t.Errorf("collector_pool_size_events = %v, want 5", got)
	}
}

func TestMetrics_CloudAcknowledgements(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Record ACK and NACK
	RecordCloudAck(true, "")
	RecordCloudAck(false, "bad_payload")

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "cloud_acks_received_total", nil); got != 1 {
		t.Errorf("cloud_acks_received_total = %v, want 1", got)
	}

	if got := counterValue(t, fams, "cloud_nacks_received_total", map[string]string{"reason": "bad_payload"}); got != 1 {
		t.Errorf("cloud_nacks_received_total{reason=bad_payload} = %v, want 1", got)
	}
}

func TestMetrics_EntropyHTTPMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	RecordEntropyHTTPRequest(200, 10*time.Millisecond)
	RecordEntropyHTTP503()
	RecordEntropyHTTPRateLimited()
	RecordEntropyHTTPRequest(503, 20*time.Millisecond)

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "entropy_http_requests_total", map[string]string{"code": "200"}); got != 1 {
		t.Errorf("entropy_http_requests_total{code=200} = %v, want 1", got)
	}
	if got := counterValue(t, fams, "entropy_http_requests_total", map[string]string{"code": "503"}); got != 1 {
		t.Errorf("entropy_http_requests_total{code=503} = %v, want 1", got)
	}
	if got := counterValue(t, fams, "entropy_http_503_total", nil); got != 1 {
		t.Errorf("entropy_http_503_total = %v, want 1", got)
	}
	if got := counterValue(t, fams, "entropy_http_rate_limited_total", nil); got != 1 {
		t.Errorf("entropy_http_rate_limited_total = %v, want 1", got)
	}
	if got := histogramCount(t, fams, "entropy_http_latency_seconds"); got != 2 {
		t.Errorf("entropy_http_latency_seconds sample count = %d, want 2", got)
	}
}

func gatherFamilies(t *testing.T, reg *prometheus.Registry) map[string]*dto.MetricFamily {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}
	out := make(map[string]*dto.MetricFamily, len(fams))
	for _, fam := range fams {
		out[fam.GetName()] = fam
	}
	return out
}

func counterValue(t *testing.T, fams map[string]*dto.MetricFamily, name string, labels map[string]string) float64 {
	t.Helper()
	metric := metricWithLabels(t, fams, name, labels)
	counter := metric.GetCounter()
	if counter == nil {
		t.Fatalf("metric %s is not a counter", name)
	}
	return counter.GetValue()
}

func gaugeValue(t *testing.T, fams map[string]*dto.MetricFamily, name string, labels map[string]string) float64 {
	t.Helper()
	metric := metricWithLabels(t, fams, name, labels)
	gauge := metric.GetGauge()
	if gauge == nil {
		t.Fatalf("metric %s is not a gauge", name)
	}
	return gauge.GetValue()
}

func histogramCount(t *testing.T, fams map[string]*dto.MetricFamily, name string) uint64 {
	t.Helper()
	metric := metricWithLabels(t, fams, name, nil)
	hist := metric.GetHistogram()
	if hist == nil {
		t.Fatalf("metric %s is not a histogram", name)
	}
	return hist.GetSampleCount()
}

func metricWithLabels(t *testing.T, fams map[string]*dto.MetricFamily, name string, labels map[string]string) *dto.Metric {
	t.Helper()
	fam, ok := fams[name]
	if !ok {
		t.Fatalf("metric %s not found", name)
	}
	for _, metric := range fam.GetMetric() {
		if labelsMatch(metric, labels) {
			return metric
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, labels)
	return nil
}

func labelsMatch(metric *dto.Metric, labels map[string]string) bool {
	if labels == nil {
		return len(metric.GetLabel()) == 0
	}
	if len(metric.GetLabel()) != len(labels) {
		return false
	}
	for _, lp := range metric.GetLabel() {
		if labels[*lp.Name] != lp.GetValue() {
			return false
		}
	}
	return true
}

func TestMetrics_WhiteningMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Test with valid data
	RecordWhitening(1000, 500, 0.5)
	RecordWhitening(2000, 1000, 0.5)

	// Test with ratio clamping
	RecordWhitening(1000, 500, -0.5) // Should clamp to 0
	RecordWhitening(1000, 500, 1.5)  // Should clamp to 1

	// Test with zero input bytes (should not record histogram or input counter)
	RecordWhitening(0, 500, 0.5)
	// Test with zero output bytes (should still count input and histogram)
	RecordWhitening(1000, 0, 0.5)
	// Test with positive output bytes
	RecordWhitening(2000, 500, 0.5)

	fams := gatherFamilies(t, reg)

	// Input bytes: 1000 + 2000 + 1000 + 1000 + 1000 + 2000 = 8000
	// (zero input bytes case is not counted)
	if got := counterValue(t, fams, "whitening_input_bytes_total", nil); got != 8000 {
		t.Errorf("whitening_input_bytes_total = %v, want 8000", got)
	}

	// Output bytes: 500 + 1000 + 500 + 500 + 500 + 0 + 500 = 3500
	if got := counterValue(t, fams, "whitening_output_bytes_total", nil); got != 3500 {
		t.Errorf("whitening_output_bytes_total = %v, want 3500", got)
	}

	// Histogram should have 6 samples (all cases with inputBytes > 0)
	if got := histogramCount(t, fams, "whitening_compression_ratio"); got != 6 {
		t.Errorf("whitening_compression_ratio sample count = %d, want 6", got)
	}
}

func TestMetrics_EntropyExtraction(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	RecordEntropyExtraction(128)
	RecordEntropyExtraction(256)
	RecordEntropyExtraction(0)    // Zero bytes
	RecordEntropyExtraction(-100) // Negative (should be ignored)

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "entropy_extractions_total", nil); got != 4 {
		t.Errorf("entropy_extractions_total = %v, want 4", got)
	}

	// Only positive bytes counted
	if got := counterValue(t, fams, "entropy_extracted_bytes_total", nil); got != 384 {
		t.Errorf("entropy_extracted_bytes_total = %v, want 384", got)
	}
}

func TestMetrics_EntropyPoolSize(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	SetEntropyPoolSize(1024)
	fams := gatherFamilies(t, reg)

	if got := gaugeValue(t, fams, "entropy_pool_size_bytes", nil); got != 1024 {
		t.Errorf("entropy_pool_size_bytes = %v, want 1024", got)
	}

	SetEntropyPoolSize(0)
	fams = gatherFamilies(t, reg)

	if got := gaugeValue(t, fams, "entropy_pool_size_bytes", nil); got != 0 {
		t.Errorf("entropy_pool_size_bytes = %v, want 0", got)
	}
}

func TestMetrics_CollectorBatchSize(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	SetCollectorBatchSize(5000)
	fams := gatherFamilies(t, reg)

	if got := gaugeValue(t, fams, "collector_batch_size_events", nil); got != 5000 {
		t.Errorf("collector_batch_size_events = %v, want 5000", got)
	}
}

func TestMetrics_CollectorFlush(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	RecordCollectorFlush(100 * time.Millisecond)
	RecordCollectorFlush(200 * time.Millisecond)
	RecordCollectorFlush(-50 * time.Millisecond) // Negative should clamp to 0

	fams := gatherFamilies(t, reg)

	if got := histogramCount(t, fams, "collector_flush_duration_seconds"); got != 3 {
		t.Errorf("collector_flush_duration_seconds sample count = %d, want 3", got)
	}
}

func TestMetrics_ForwarderMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	RecordMissingSequence(5)
	RecordMissingSequence(3)
	RecordMissingSequence(0)  // Zero should be ignored
	RecordMissingSequence(-2) // Negative should be ignored

	RecordBackpressure()
	RecordBackpressure()

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "forwarder_missing_seq_total", nil); got != 8 {
		t.Errorf("forwarder_missing_seq_total = %v, want 8", got)
	}

	if got := counterValue(t, fams, "forwarder_backpressure_total", nil); got != 2 {
		t.Errorf("forwarder_backpressure_total = %v, want 2", got)
	}
}

func TestMetrics_ControlPlane(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	RecordControlApply("batch_size")
	RecordControlApply("rate_limit")
	RecordControlApply("batch_size")

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "control_apply_total", map[string]string{"field": "batch_size"}); got != 2 {
		t.Errorf("control_apply_total{field=batch_size} = %v, want 2", got)
	}

	if got := counterValue(t, fams, "control_apply_total", map[string]string{"field": "rate_limit"}); got != 1 {
		t.Errorf("control_apply_total{field=rate_limit} = %v, want 1", got)
	}
}

func TestMetrics_SignatureFailures(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	RecordSignatureFail()
	RecordSignatureFail()
	RecordSignatureFail()

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "signature_fail_total", nil); got != 3 {
		t.Errorf("signature_fail_total = %v, want 3", got)
	}
}

func TestMetrics_ContinuousHealthTests(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	RecordContinuousRCTFailure()
	RecordContinuousRCTFailure()

	RecordContinuousAPTFailure()
	RecordContinuousAPTFailure()
	RecordContinuousAPTFailure()

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "continuous_rct_failures_total", nil); got != 2 {
		t.Errorf("continuous_rct_failures_total = %v, want 2", got)
	}

	if got := counterValue(t, fams, "continuous_apt_failures_total", nil); got != 3 {
		t.Errorf("continuous_apt_failures_total = %v, want 3", got)
	}
}

func TestMetrics_MinEntropyEstimates(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Test MCV with normal values
	RecordMinEntropyMCV(7.5)
	RecordMinEntropyMCV(7.8)

	// Test MCV with boundary values
	RecordMinEntropyMCV(-1.0) // Should clamp to 0.0
	RecordMinEntropyMCV(10.0) // Should clamp to 8.0

	// Test Collision with normal values
	RecordMinEntropyCollision(7.2)
	RecordMinEntropyCollision(7.9)

	// Test Collision with boundary values
	RecordMinEntropyCollision(-0.5) // Should clamp to 0.0
	RecordMinEntropyCollision(9.0)  // Should clamp to 8.0

	fams := gatherFamilies(t, reg)

	if got := histogramCount(t, fams, "min_entropy_estimate_mcv_bits_per_byte"); got != 4 {
		t.Errorf("min_entropy_estimate_mcv_bits_per_byte sample count = %d, want 4", got)
	}

	if got := histogramCount(t, fams, "min_entropy_estimate_collision_bits_per_byte"); got != 4 {
		t.Errorf("min_entropy_estimate_collision_bits_per_byte sample count = %d, want 4", got)
	}
}

func TestMetrics_GRPCServerMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	SetGRPCServerConnectedClients(5)
	RecordGRPCServerBatchSent()
	RecordGRPCServerBatchSent()
	RecordGRPCServerBatchDropped("slow_client")
	RecordGRPCServerBatchDropped("buffer_full")
	RecordGRPCServerAckReceived(true, "")
	RecordGRPCServerAckReceived(false, "invalid")

	fams := gatherFamilies(t, reg)

	if got := gaugeValue(t, fams, "grpc_server_connected_clients", nil); got != 5 {
		t.Errorf("grpc_server_connected_clients = %v, want 5", got)
	}

	if got := counterValue(t, fams, "grpc_server_batches_sent_total", nil); got != 2 {
		t.Errorf("grpc_server_batches_sent_total = %v, want 2", got)
	}

	if got := counterValue(t, fams, "grpc_server_batches_dropped_total", map[string]string{"reason": "slow_client"}); got != 1 {
		t.Errorf("grpc_server_batches_dropped_total{reason=slow_client} = %v, want 1", got)
	}

	if got := counterValue(t, fams, "grpc_server_acks_received_total", map[string]string{"success": "true"}); got != 1 {
		t.Errorf("grpc_server_acks_received_total{success=true} = %v, want 1", got)
	}

	if got := counterValue(t, fams, "grpc_server_acks_received_total", map[string]string{"success": "false"}); got != 1 {
		t.Errorf("grpc_server_acks_received_total{success=false} = %v, want 1", got)
	}
}

func TestMetrics_EntropyHTTPRequestEdgeCases(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Test with zero and negative status codes
	RecordEntropyHTTPRequest(0, 10*time.Millisecond)
	RecordEntropyHTTPRequest(-1, 20*time.Millisecond)

	// Test with negative duration (should clamp to 0)
	RecordEntropyHTTPRequest(200, -50*time.Millisecond)

	fams := gatherFamilies(t, reg)

	// Both zero and negative codes should be labeled as "0"
	if got := counterValue(t, fams, "entropy_http_requests_total", map[string]string{"code": "0"}); got != 2 {
		t.Errorf("entropy_http_requests_total{code=0} = %v, want 2", got)
	}

	// All three should record latency
	if got := histogramCount(t, fams, "entropy_http_latency_seconds"); got != 3 {
		t.Errorf("entropy_http_latency_seconds sample count = %d, want 3", got)
	}
}

func TestMetrics_ValidationBothTestsFail(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Test when both frequency and runs tests fail
	RecordValidation(0.3, false, 50, false)

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "validation_failures_total", map[string]string{"test_type": "frequency"}); got != 1 {
		t.Errorf("validation_failures_total{test_type=frequency} = %v, want 1", got)
	}

	if got := counterValue(t, fams, "validation_failures_total", map[string]string{"test_type": "runs"}); got != 1 {
		t.Errorf("validation_failures_total{test_type=runs} = %v, want 1", got)
	}
}

func TestMetrics_ConcurrentUpdates(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	const numGoroutines = 10
	const updatesPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Launch multiple goroutines updating different metrics concurrently
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < updatesPerGoroutine; j++ {
				RecordEvent(uint32(id % 4))
				RecordMQTTMessage()
				SetCollectorPoolSize(id*100 + j)
				RecordEntropyExtraction(32)
			}
		}(i)
	}

	wg.Wait()

	fams := gatherFamilies(t, reg)

	// Verify totals
	totalEvents := counterValue(t, fams, "tdc_events_processed_total", nil)
	expectedEvents := float64(numGoroutines * updatesPerGoroutine)
	if totalEvents != expectedEvents {
		t.Errorf("tdc_events_processed_total = %v, want %v", totalEvents, expectedEvents)
	}

	totalMsgs := counterValue(t, fams, "mqtt_in_msgs_total", nil)
	if totalMsgs != expectedEvents {
		t.Errorf("mqtt_in_msgs_total = %v, want %v", totalMsgs, expectedEvents)
	}

	totalExtractions := counterValue(t, fams, "entropy_extractions_total", nil)
	if totalExtractions != expectedEvents {
		t.Errorf("entropy_extractions_total = %v, want %v", totalExtractions, expectedEvents)
	}
}

func TestMetrics_ConcurrentLabeledUpdates(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	const numGoroutines = 20
	const updatesPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Concurrently update labeled metrics
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < updatesPerGoroutine; j++ {
				RecordEventDropped("parse_error")
				RecordGRPCError("io")
				RecordControlApply("batch_size")
			}
		}(i)
	}

	wg.Wait()

	fams := gatherFamilies(t, reg)

	expected := float64(numGoroutines * updatesPerGoroutine)

	if got := counterValue(t, fams, "tdc_events_dropped_total", map[string]string{"reason": "parse_error"}); got != expected {
		t.Errorf("tdc_events_dropped_total = %v, want %v", got, expected)
	}

	if got := counterValue(t, fams, "grpc_stream_errors_total", map[string]string{"error_type": "io"}); got != expected {
		t.Errorf("grpc_stream_errors_total = %v, want %v", got, expected)
	}

	if got := counterValue(t, fams, "control_apply_total", map[string]string{"field": "batch_size"}); got != expected {
		t.Errorf("control_apply_total = %v, want %v", got, expected)
	}
}

func TestMetrics_MultipleResets(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Record some metrics
	RecordEvent(0)
	RecordEvent(1)

	fams := gatherFamilies(t, reg)
	if got := counterValue(t, fams, "tdc_events_processed_total", nil); got != 2 {
		t.Errorf("before reset: tdc_events_processed_total = %v, want 2", got)
	}

	// Reset and verify metrics are cleared
	ResetForTesting(reg)
	fams = gatherFamilies(t, reg)
	if got := counterValue(t, fams, "tdc_events_processed_total", nil); got != 0 {
		t.Errorf("after first reset: tdc_events_processed_total = %v, want 0", got)
	}

	// Record new data
	RecordEvent(2)
	fams = gatherFamilies(t, reg)
	if got := counterValue(t, fams, "tdc_events_processed_total", nil); got != 1 {
		t.Errorf("after recording: tdc_events_processed_total = %v, want 1", got)
	}

	// Reset again
	ResetForTesting(reg)
	fams = gatherFamilies(t, reg)
	if got := counterValue(t, fams, "tdc_events_processed_total", nil); got != 0 {
		t.Errorf("after second reset: tdc_events_processed_total = %v, want 0", got)
	}

	// Reset a third time (testing idempotency)
	ResetForTesting(reg)
	ResetForTesting(reg)
	fams = gatherFamilies(t, reg)

	// Verify metrics still exist and are at zero
	if got := counterValue(t, fams, "tdc_events_processed_total", nil); got != 0 {
		t.Errorf("after multiple resets: tdc_events_processed_total = %v, want 0", got)
	}
}

// ============================================================================
// Histogram Bucket Edge Cases
// ============================================================================

func TestMetrics_HistogramBucketEdgeCases(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Test values at bucket boundaries for ValidationFrequencyRatio
	// Buckets: 0.4, 0.41, 0.42, ..., 0.6 (LinearBuckets(0.4, 0.01, 21))
	RecordValidation(0.39, true, 100, true) // Below first bucket
	RecordValidation(0.40, true, 100, true) // Exactly at first bucket
	RecordValidation(0.50, true, 100, true) // Middle bucket
	RecordValidation(0.60, true, 100, true) // Last bucket
	RecordValidation(0.61, true, 100, true) // Above last bucket

	// Test values at bucket boundaries for BatchSize
	// Buckets: 100, 200, 300, ..., 2000 (LinearBuckets(100, 100, 20))
	RecordBatch(50, 0.1, true, "")   // Below first bucket
	RecordBatch(100, 0.1, true, "")  // Exactly at first bucket
	RecordBatch(1000, 0.1, true, "") // Middle bucket
	RecordBatch(2000, 0.1, true, "") // Last bucket
	RecordBatch(3000, 0.1, true, "") // Above last bucket

	// Test exponential buckets for BatchSendDuration
	// Buckets: 0.001, 0.002, 0.004, 0.008, ... (ExponentialBuckets(0.001, 2, 15))
	RecordBatch(100, 0.0005, true, "") // Below first bucket
	RecordBatch(100, 0.001, true, "")  // Exactly at first bucket
	RecordBatch(100, 0.016, true, "")  // Middle bucket
	RecordBatch(100, 16.384, true, "") // Last bucket (0.001 * 2^14)
	RecordBatch(100, 32.0, true, "")   // Above last bucket

	fams := gatherFamilies(t, reg)

	// Verify all samples were recorded
	if got := histogramCount(t, fams, "validation_frequency_ratio"); got != 5 {
		t.Errorf("validation_frequency_ratio sample count = %d, want 5", got)
	}

	// batch_size_events includes all 10 batch calls above
	if got := histogramCount(t, fams, "batch_size_events"); got != 10 {
		t.Errorf("batch_size_events sample count = %d, want 10", got)
	}

	// batch_send_duration_seconds is only recorded for successful batches
	// All 10 batches above are successful
	if got := histogramCount(t, fams, "batch_send_duration_seconds"); got != 10 {
		t.Errorf("batch_send_duration_seconds sample count = %d, want 10", got)
	}
}

func TestMetrics_HistogramExtremeValues(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Test with very small and very large values
	RecordWhitening(1, 1, 0.0)             // Minimum ratio
	RecordWhitening(1000000, 1000000, 1.0) // Maximum ratio

	RecordMinEntropyMCV(0.0) // Minimum entropy
	RecordMinEntropyMCV(8.0) // Maximum entropy

	RecordMinEntropyCollision(0.0) // Minimum entropy
	RecordMinEntropyCollision(8.0) // Maximum entropy

	RecordCollectorFlush(1 * time.Nanosecond) // Very small duration
	RecordCollectorFlush(10 * time.Second)    // Large duration

	fams := gatherFamilies(t, reg)

	// Verify samples were recorded
	if got := histogramCount(t, fams, "whitening_compression_ratio"); got != 2 {
		t.Errorf("whitening_compression_ratio sample count = %d, want 2", got)
	}

	if got := histogramCount(t, fams, "min_entropy_estimate_mcv_bits_per_byte"); got != 2 {
		t.Errorf("min_entropy_estimate_mcv_bits_per_byte sample count = %d, want 2", got)
	}

	if got := histogramCount(t, fams, "collector_flush_duration_seconds"); got != 2 {
		t.Errorf("collector_flush_duration_seconds sample count = %d, want 2", got)
	}
}

// ============================================================================
// Benchmarks
// ============================================================================

func BenchmarkMetrics_ConcurrentCounterUpdates(b *testing.B) {
	reg := prometheus.NewRegistry()
	resetMetrics(reg)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			RecordEvent(0)
			RecordMQTTMessage()
			RecordEntropyExtraction(32)
		}
	})
}

func BenchmarkMetrics_ConcurrentLabeledCounterUpdates(b *testing.B) {
	reg := prometheus.NewRegistry()
	resetMetrics(reg)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			RecordEventDropped("parse_error")
			RecordGRPCError("io")
			RecordControlApply("batch_size")
		}
	})
}

func BenchmarkMetrics_ConcurrentHistogramUpdates(b *testing.B) {
	reg := prometheus.NewRegistry()
	resetMetrics(reg)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			RecordBatch(1000, 0.5, true, "")
			RecordWhitening(1000, 500, 0.5)
			RecordCollectorFlush(100 * time.Millisecond)
		}
	})
}

func BenchmarkMetrics_ConcurrentGaugeUpdates(b *testing.B) {
	reg := prometheus.NewRegistry()
	resetMetrics(reg)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			SetCollectorPoolSize(i)
			SetEntropyPoolSize(i * 1024)
			SetGRPCServerConnectedClients(i % 10)
			i++
		}
	})
}

func BenchmarkMetrics_MixedConcurrentUpdates(b *testing.B) {
	reg := prometheus.NewRegistry()
	resetMetrics(reg)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			// Mix of different metric types
			RecordEvent(uint32(i % 4))
			RecordBatch(1000, 0.5, true, "")
			SetCollectorPoolSize(i)
			RecordEventDropped("parse_error")
			RecordMinEntropyMCV(7.5)
			i++
		}
	})
}

func TestRecordFormatType(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Record different format types
	RecordFormatType("ascii")
	RecordFormatType("ascii")
	RecordFormatType("binary")
	RecordFormatType("unknown")

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "tdc_event_format_type_total", map[string]string{"format": "ascii"}); got != 2 {
		t.Errorf("expected ascii count 2, got %v", got)
	}
	if got := counterValue(t, fams, "tdc_event_format_type_total", map[string]string{"format": "binary"}); got != 1 {
		t.Errorf("expected binary count 1, got %v", got)
	}
	if got := counterValue(t, fams, "tdc_event_format_type_total", map[string]string{"format": "unknown"}); got != 1 {
		t.Errorf("expected unknown count 1, got %v", got)
	}
}

func TestRecordChannelEvent(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	withRegistry(t, reg)

	// Record events on different channels
	RecordChannelEvent(0)
	RecordChannelEvent(0)
	RecordChannelEvent(1)
	RecordChannelEvent(2)
	RecordChannelEvent(99) // High channel number (maps to "unknown")

	fams := gatherFamilies(t, reg)

	if got := counterValue(t, fams, "tdc_events_per_channel_total", map[string]string{"channel": "0"}); got != 2 {
		t.Errorf("expected channel 0 count 2, got %v", got)
	}
	if got := counterValue(t, fams, "tdc_events_per_channel_total", map[string]string{"channel": "1"}); got != 1 {
		t.Errorf("expected channel 1 count 1, got %v", got)
	}
	if got := counterValue(t, fams, "tdc_events_per_channel_total", map[string]string{"channel": "2"}); got != 1 {
		t.Errorf("expected channel 2 count 1, got %v", got)
	}
	// Channel 99 maps to "unknown" label
	if got := counterValue(t, fams, "tdc_events_per_channel_total", map[string]string{"channel": "unknown"}); got != 1 {
		t.Errorf("expected channel unknown count 1, got %v", got)
	}
}
