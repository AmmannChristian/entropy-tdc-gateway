// Package metrics registers and records Prometheus metrics for all gateway
// subsystems including MQTT ingestion, event batching, entropy whitening,
// pool management, gRPC forwarding, and HTTP entropy distribution.
package metrics

import (
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	EventsReceived              *prometheus.CounterVec
	EventsProcessed             prometheus.Counter
	EventsDropped               *prometheus.CounterVec
	EventFormatType             *prometheus.CounterVec
	EventsPerChannel            *prometheus.CounterVec
	ValidationFailures          *prometheus.CounterVec
	ValidationFrequencyRatio    prometheus.Histogram
	ValidationRunsCount         prometheus.Histogram
	BatchesCreated              prometheus.Counter
	BatchesSent                 prometheus.Counter
	BatchesFailed               *prometheus.CounterVec
	BatchSize                   prometheus.Histogram
	BatchSendDuration           prometheus.Histogram
	WhiteningInputBytes         prometheus.Counter
	WhiteningOutputBytes        prometheus.Counter
	WhiteningCompressionRatio   prometheus.Histogram
	EntropyExtractions          prometheus.Counter
	EntropyExtractedBytes       prometheus.Counter
	EntropyPoolSize             prometheus.Gauge
	GRPCConnected               prometheus.Gauge
	GRPCReconnects              prometheus.Counter
	GRPCStreamErrors            *prometheus.CounterVec
	MQTTConnected               prometheus.Gauge
	MQTTReconnects              prometheus.Counter
	MQTTConnects                prometheus.Counter
	MQTTDisconnects             prometheus.Counter
	MQTTInboundMessages         prometheus.Counter
	CollectorPoolSize           prometheus.Gauge
	CollectorBatchSize          prometheus.Gauge
	CollectorFlushDuration      prometheus.Histogram
	ContinuousRCTFailures       prometheus.Counter
	ContinuousAPTFailures       prometheus.Counter
	MinEntropyEstimateMCV       prometheus.Histogram
	MinEntropyEstimateCollision prometheus.Histogram
	ProcessingLatency           prometheus.Histogram
	CloudAcksReceived           prometheus.Counter
	CloudNacksReceived          *prometheus.CounterVec
	EntropyHTTPRequests         *prometheus.CounterVec
	EntropyHTTP503Total         prometheus.Counter
	EntropyHTTPRateLimited      prometheus.Counter
	EntropyHTTPLatency          prometheus.Histogram
	ForwarderMissingSeqTotal    prometheus.Counter
	ForwarderBackpressureTotal  prometheus.Counter
	ControlApplyTotal           *prometheus.CounterVec
	SignatureFailTotal          prometheus.Counter
	GRPCServerConnectedClients  prometheus.Gauge
	GRPCServerBatchesSent       prometheus.Counter
	GRPCServerBatchesDropped    *prometheus.CounterVec
	GRPCServerAcksReceived      *prometheus.CounterVec

	metricsMu         sync.RWMutex
	currentRegisterer prometheus.Registerer = prometheus.DefaultRegisterer
)

func init() {
	resetMetrics(prometheus.DefaultRegisterer)
}

// SetRegisterer sets a new registerer and reinitializes all metrics.
// It returns the previous registerer so it can be restored later.
// This function is thread-safe and designed for use in tests to provide
// isolated metric registries per test.
func SetRegisterer(registerer prometheus.Registerer) prometheus.Registerer {
	metricsMu.Lock()
	defer metricsMu.Unlock()

	previous := currentRegisterer

	// Unregister from previous registerer
	if currentRegisterer != nil {
		unregisterAll(currentRegisterer)
	}

	// Set new registerer and reinitialize metrics
	currentRegisterer = registerer
	initializeMetrics(registerer)

	return previous
}

// ResetForTesting reconfigures all metric collectors against the provided registerer.
// It unregisters the existing metrics from the previous registerer to prevent
// duplicate registrations when invoked repeatedly.
//
// Deprecated: Use SetRegisterer instead for better test isolation.
func ResetForTesting(registerer prometheus.Registerer) {
	resetMetrics(registerer)
}

func resetMetrics(registerer prometheus.Registerer) {
	metricsMu.Lock()
	defer metricsMu.Unlock()

	if currentRegisterer != nil {
		unregisterAll(currentRegisterer)
	}

	currentRegisterer = registerer
	initializeMetrics(registerer)
}

// initializeMetrics creates all metrics using the provided registerer.
// This function must be called while holding metricsMu.
func initializeMetrics(registerer prometheus.Registerer) {
	factory := promauto.With(registerer)

	EventsReceived = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tdc_events_received_total",
			Help: "Total number of TDC events received from MQTT",
		},
		[]string{"channel"},
	)

	EventsProcessed = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "tdc_events_processed_total",
			Help: "Total number of TDC events successfully processed",
		},
	)

	EventsDropped = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tdc_events_dropped_total",
			Help: "Total number of TDC events dropped",
		},
		[]string{"reason"},
	)

	EventFormatType = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tdc_event_format_type_total",
			Help: "Total number of TDC events by format type (raw_timestamp)",
		},
		[]string{"format"},
	)

	EventsPerChannel = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tdc_events_per_channel_total",
			Help: "Total number of TDC events per channel",
		},
		[]string{"channel"},
	)

	ValidationFailures = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "validation_failures_total",
			Help: "Total number of quick validation test failures",
		},
		[]string{"test_type"},
	)

	ValidationFrequencyRatio = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "validation_frequency_ratio",
			Help:    "Distribution of frequency test ratios (should be ~0.5)",
			Buckets: prometheus.LinearBuckets(0.4, 0.01, 21),
		},
	)

	ValidationRunsCount = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "validation_runs_count",
			Help:    "Distribution of runs test counts",
			Buckets: prometheus.ExponentialBuckets(100, 1.5, 15),
		},
	)

	BatchesCreated = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "batches_created_total",
			Help: "Total number of batches created",
		},
	)

	BatchesSent = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "batches_sent_total",
			Help: "Total number of batches successfully sent to cloud",
		},
	)

	BatchesFailed = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "batches_failed_total",
			Help: "Total number of batches that failed to send",
		},
		[]string{"reason"},
	)

	BatchSize = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "batch_size_events",
			Help:    "Number of events per batch",
			Buckets: prometheus.LinearBuckets(100, 100, 20),
		},
	)

	BatchSendDuration = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "batch_send_duration_seconds",
			Help:    "Time taken to send a batch to cloud",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
		},
	)

	WhiteningInputBytes = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "whitening_input_bytes_total",
			Help: "Total raw bytes processed by the whitening pipeline",
		},
	)

	WhiteningOutputBytes = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "whitening_output_bytes_total",
			Help: "Total whitened bytes produced by the whitening pipeline",
		},
	)

	WhiteningCompressionRatio = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "whitening_compression_ratio",
			Help:    "Observed compression ratio of the whitening pipeline",
			Buckets: prometheus.LinearBuckets(0.0, 0.05, 21),
		},
	)

	EntropyExtractions = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "entropy_extractions_total",
			Help: "Total number of entropy extractions from the whitened pool",
		},
	)

	EntropyExtractedBytes = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "entropy_extracted_bytes_total",
			Help: "Total number of bytes extracted from the whitened pool",
		},
	)

	EntropyPoolSize = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "entropy_pool_size_bytes",
			Help: "Current size of the whitened entropy pool in bytes",
		},
	)

	GRPCConnected = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "grpc_connection_status",
			Help: "gRPC connection status (1=connected, 0=disconnected)",
		},
	)

	GRPCReconnects = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "forwarder_reconnects_total",
			Help: "Total number of forwarder reconnection attempts",
		},
	)

	GRPCStreamErrors = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "grpc_stream_errors_total",
			Help: "Total number of gRPC stream errors",
		},
		[]string{"error_type"},
	)

	MQTTConnected = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "mqtt_connection_status",
			Help: "MQTT connection status (1=connected, 0=disconnected)",
		},
	)

	MQTTReconnects = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "mqtt_reconnects_total",
			Help: "Total number of MQTT reconnection attempts",
		},
	)

	MQTTConnects = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "mqtt_connects_total",
			Help: "Total number of successful MQTT connections",
		},
	)

	MQTTDisconnects = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "mqtt_disconnects_total",
			Help: "Total number of MQTT disconnects",
		},
	)

	MQTTInboundMessages = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "mqtt_in_msgs_total",
			Help: "Total number of MQTT messages received",
		},
	)

	CollectorPoolSize = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "collector_pool_size_events",
			Help: "Current number of events in collector buffer",
		},
	)

	CollectorBatchSize = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "collector_batch_size_events",
			Help: "Current configured batch size for the collector",
		},
	)

	CollectorFlushDuration = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "collector_flush_duration_seconds",
			Help:    "Time taken to process and flush a batch",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 15),
		},
	)

	ContinuousRCTFailures = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "continuous_rct_failures_total",
			Help: "Total number of NIST SP 800-90B Repetition Count Test failures",
		},
	)

	ContinuousAPTFailures = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "continuous_apt_failures_total",
			Help: "Total number of NIST SP 800-90B Adaptive Proportion Test failures",
		},
	)

	MinEntropyEstimateMCV = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "min_entropy_estimate_mcv_bits_per_byte",
			Help:    "NIST SP 800-90B Most Common Value min-entropy estimate (range: 0.0-8.0)",
			Buckets: prometheus.LinearBuckets(0.0, 0.5, 17), // 0.0, 0.5, 1.0, ..., 8.0
		},
	)

	MinEntropyEstimateCollision = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "min_entropy_estimate_collision_bits_per_byte",
			Help:    "NIST SP 800-90B Collision min-entropy estimate (range: 0.0-8.0)",
			Buckets: prometheus.LinearBuckets(0.0, 0.5, 17), // 0.0, 0.5, 1.0, ..., 8.0
		},
	)

	ProcessingLatency = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "event_processing_latency_microseconds",
			Help:    "Latency from MQTT receive to batch creation",
			Buckets: prometheus.ExponentialBuckets(10, 2, 15),
		},
	)

	CloudAcksReceived = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "cloud_acks_received_total",
			Help: "Total number of ACKs received from cloud",
		},
	)

	CloudNacksReceived = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cloud_nacks_received_total",
			Help: "Total number of NACKs received from cloud",
		},
		[]string{"reason"},
	)

	EntropyHTTPRequests = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "entropy_http_requests_total",
			Help: "Total number of entropy endpoint requests by status code",
		},
		[]string{"code"},
	)

	EntropyHTTP503Total = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "entropy_http_503_total",
			Help: "Total number of entropy endpoint 503 responses",
		},
	)

	EntropyHTTPRateLimited = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "entropy_http_rate_limited_total",
			Help: "Total number of entropy endpoint rate limited responses",
		},
	)

	EntropyHTTPLatency = factory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "entropy_http_latency_seconds",
			Help:    "Latency distribution for entropy HTTP handler",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2},
		},
	)

	ForwarderMissingSeqTotal = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "forwarder_missing_seq_total",
			Help: "Total number of missing sequences reported by cloud",
		},
	)

	ForwarderBackpressureTotal = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "forwarder_backpressure_total",
			Help: "Total number of backpressure events received from cloud",
		},
	)

	ControlApplyTotal = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "control_apply_total",
			Help: "Total number of control plane config updates applied",
		},
		[]string{"field"},
	)

	SignatureFailTotal = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "signature_fail_total",
			Help: "Total number of batch signature failures",
		},
	)

	GRPCServerConnectedClients = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "grpc_server_connected_clients",
			Help: "Number of currently connected gRPC streaming clients",
		},
	)

	GRPCServerBatchesSent = factory.NewCounter(
		prometheus.CounterOpts{
			Name: "grpc_server_batches_sent_total",
			Help: "Total number of batches sent to gRPC clients",
		},
	)

	GRPCServerBatchesDropped = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "grpc_server_batches_dropped_total",
			Help: "Total number of batches dropped due to slow clients",
		},
		[]string{"reason"},
	)

	GRPCServerAcksReceived = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "grpc_server_acks_received_total",
			Help: "Total number of acknowledgments received from gRPC clients",
		},
		[]string{"success"},
	)
}

func unregisterAll(registerer prometheus.Registerer) {
	if EventsReceived != nil {
		registerer.Unregister(EventsReceived)
	}
	if EventsProcessed != nil {
		registerer.Unregister(EventsProcessed)
	}
	if EventsDropped != nil {
		registerer.Unregister(EventsDropped)
	}
	if EventFormatType != nil {
		registerer.Unregister(EventFormatType)
	}
	if EventsPerChannel != nil {
		registerer.Unregister(EventsPerChannel)
	}
	if ValidationFailures != nil {
		registerer.Unregister(ValidationFailures)
	}
	if ValidationFrequencyRatio != nil {
		registerer.Unregister(ValidationFrequencyRatio)
	}
	if ValidationRunsCount != nil {
		registerer.Unregister(ValidationRunsCount)
	}
	if BatchesCreated != nil {
		registerer.Unregister(BatchesCreated)
	}
	if BatchesSent != nil {
		registerer.Unregister(BatchesSent)
	}
	if BatchesFailed != nil {
		registerer.Unregister(BatchesFailed)
	}
	if BatchSize != nil {
		registerer.Unregister(BatchSize)
	}
	if BatchSendDuration != nil {
		registerer.Unregister(BatchSendDuration)
	}
	if WhiteningInputBytes != nil {
		registerer.Unregister(WhiteningInputBytes)
	}
	if WhiteningOutputBytes != nil {
		registerer.Unregister(WhiteningOutputBytes)
	}
	if WhiteningCompressionRatio != nil {
		registerer.Unregister(WhiteningCompressionRatio)
	}
	if EntropyExtractions != nil {
		registerer.Unregister(EntropyExtractions)
	}
	if EntropyExtractedBytes != nil {
		registerer.Unregister(EntropyExtractedBytes)
	}
	if EntropyPoolSize != nil {
		registerer.Unregister(EntropyPoolSize)
	}
	if GRPCConnected != nil {
		registerer.Unregister(GRPCConnected)
	}
	if GRPCReconnects != nil {
		registerer.Unregister(GRPCReconnects)
	}
	if GRPCStreamErrors != nil {
		registerer.Unregister(GRPCStreamErrors)
	}
	if MQTTConnected != nil {
		registerer.Unregister(MQTTConnected)
	}
	if MQTTReconnects != nil {
		registerer.Unregister(MQTTReconnects)
	}
	if MQTTConnects != nil {
		registerer.Unregister(MQTTConnects)
	}
	if MQTTDisconnects != nil {
		registerer.Unregister(MQTTDisconnects)
	}
	if MQTTInboundMessages != nil {
		registerer.Unregister(MQTTInboundMessages)
	}
	if CollectorPoolSize != nil {
		registerer.Unregister(CollectorPoolSize)
	}
	if CollectorBatchSize != nil {
		registerer.Unregister(CollectorBatchSize)
	}
	if CollectorFlushDuration != nil {
		registerer.Unregister(CollectorFlushDuration)
	}
	if ContinuousRCTFailures != nil {
		registerer.Unregister(ContinuousRCTFailures)
	}
	if ContinuousAPTFailures != nil {
		registerer.Unregister(ContinuousAPTFailures)
	}
	if MinEntropyEstimateMCV != nil {
		registerer.Unregister(MinEntropyEstimateMCV)
	}
	if MinEntropyEstimateCollision != nil {
		registerer.Unregister(MinEntropyEstimateCollision)
	}
	if ProcessingLatency != nil {
		registerer.Unregister(ProcessingLatency)
	}
	if CloudAcksReceived != nil {
		registerer.Unregister(CloudAcksReceived)
	}
	if CloudNacksReceived != nil {
		registerer.Unregister(CloudNacksReceived)
	}
	if EntropyHTTPRequests != nil {
		registerer.Unregister(EntropyHTTPRequests)
	}
	if EntropyHTTP503Total != nil {
		registerer.Unregister(EntropyHTTP503Total)
	}
	if EntropyHTTPRateLimited != nil {
		registerer.Unregister(EntropyHTTPRateLimited)
	}
	if EntropyHTTPLatency != nil {
		registerer.Unregister(EntropyHTTPLatency)
	}
	if ForwarderMissingSeqTotal != nil {
		registerer.Unregister(ForwarderMissingSeqTotal)
	}
	if ForwarderBackpressureTotal != nil {
		registerer.Unregister(ForwarderBackpressureTotal)
	}
	if ControlApplyTotal != nil {
		registerer.Unregister(ControlApplyTotal)
	}
	if SignatureFailTotal != nil {
		registerer.Unregister(SignatureFailTotal)
	}
	if GRPCServerConnectedClients != nil {
		registerer.Unregister(GRPCServerConnectedClients)
	}
	if GRPCServerBatchesSent != nil {
		registerer.Unregister(GRPCServerBatchesSent)
	}
	if GRPCServerBatchesDropped != nil {
		registerer.Unregister(GRPCServerBatchesDropped)
	}
	if GRPCServerAcksReceived != nil {
		registerer.Unregister(GRPCServerAcksReceived)
	}
}

// RecordEntropyHTTPRequest tracks latency and status codes for the entropy endpoint.
func RecordEntropyHTTPRequest(code int, duration time.Duration) {
	label := strconv.Itoa(code)
	if code <= 0 {
		label = "0"
	}
	if duration < 0 {
		duration = 0
	}
	EntropyHTTPRequests.WithLabelValues(label).Inc()
	EntropyHTTPLatency.Observe(duration.Seconds())
}

// RecordEntropyHTTP503 increments the total 503 counter for the entropy endpoint.
func RecordEntropyHTTP503() {
	EntropyHTTP503Total.Inc()
}

// RecordEntropyHTTPRateLimited tracks rate-limited responses for the entropy endpoint.
func RecordEntropyHTTPRateLimited() {
	EntropyHTTPRateLimited.Inc()
}

// RecordEvent records a received TDC event
func RecordEvent(channel uint32) {
	EventsReceived.WithLabelValues(channelLabel(channel)).Inc()
	EventsProcessed.Inc()
}

// RecordEventDropped records a dropped event with reason
func RecordEventDropped(reason string) {
	EventsDropped.WithLabelValues(reason).Inc()
}

// RecordDroppedCount records a dropped event count with a reason label.
func RecordDroppedCount(reason string, count uint32) {
	if count == 0 {
		return
	}
	EventsDropped.WithLabelValues(reason).Add(float64(count))
}

// RecordFormatType records the format type of a parsed event
func RecordFormatType(format string) {
	EventFormatType.WithLabelValues(format).Inc()
}

// RecordChannelEvent records an event for a specific channel
func RecordChannelEvent(channel uint32) {
	EventsPerChannel.WithLabelValues(channelLabel(channel)).Inc()
}

// RecordValidation records validation test results
func RecordValidation(freqRatio float64, freqPassed bool, runsCount int, runsPassed bool) {
	ValidationFrequencyRatio.Observe(freqRatio)
	ValidationRunsCount.Observe(float64(runsCount))

	if !freqPassed {
		ValidationFailures.WithLabelValues("frequency").Inc()
	}
	if !runsPassed {
		ValidationFailures.WithLabelValues("runs").Inc()
	}
}

// RecordBatch records batch creation and send metrics
func RecordBatch(size int, duration float64, success bool, failReason string) {
	BatchesCreated.Inc()
	BatchSize.Observe(float64(size))

	if success {
		BatchesSent.Inc()
		BatchSendDuration.Observe(duration)
	} else {
		BatchesFailed.WithLabelValues(failReason).Inc()
	}
}

// RecordWhitening records usage metrics for the whitening pipeline.
func RecordWhitening(inputBytes, outputBytes int, ratio float64) {
	if inputBytes > 0 {
		WhiteningInputBytes.Add(float64(inputBytes))

		if ratio < 0 {
			ratio = 0
		} else if ratio > 1 {
			ratio = 1
		}
		WhiteningCompressionRatio.Observe(ratio)
	}

	if outputBytes > 0 {
		WhiteningOutputBytes.Add(float64(outputBytes))
	}
}

// RecordEntropyExtraction tracks entropy extraction activity and the amount of data consumed.
func RecordEntropyExtraction(bytes int) {
	EntropyExtractions.Inc()
	if bytes > 0 {
		EntropyExtractedBytes.Add(float64(bytes))
	}
}

// SetEntropyPoolSize publishes the current size of the whitened entropy pool.
func SetEntropyPoolSize(bytes int) {
	EntropyPoolSize.Set(float64(bytes))
}

// SetGRPCConnected sets the gRPC connection status
func SetGRPCConnected(connected bool) {
	if connected {
		GRPCConnected.Set(1)
	} else {
		GRPCConnected.Set(0)
	}
}

// RecordGRPCReconnect increments reconnection counter
func RecordGRPCReconnect() {
	GRPCReconnects.Inc()
}

// RecordGRPCError records a gRPC error
func RecordGRPCError(errorType string) {
	GRPCStreamErrors.WithLabelValues(errorType).Inc()
}

// SetMQTTConnected sets the MQTT connection status
func SetMQTTConnected(connected bool) {
	if connected {
		MQTTConnected.Set(1)
	} else {
		MQTTConnected.Set(0)
	}
}

// RecordMQTTReconnect increments MQTT reconnection counter
func RecordMQTTReconnect() {
	MQTTReconnects.Inc()
}

// RecordMQTTConnect tracks successful MQTT connections.
func RecordMQTTConnect() {
	MQTTConnects.Inc()
}

// RecordMQTTDisconnect tracks MQTT disconnects, whether expected or due to errors.
func RecordMQTTDisconnect() {
	MQTTDisconnects.Inc()
}

// RecordMQTTMessage counts inbound MQTT messages prior to validation.
func RecordMQTTMessage() {
	MQTTInboundMessages.Inc()
}

// SetCollectorPoolSize updates the current pool size
func SetCollectorPoolSize(size int) {
	CollectorPoolSize.Set(float64(size))
}

// SetCollectorBatchSize updates the configured batch size
func SetCollectorBatchSize(size int) {
	CollectorBatchSize.Set(float64(size))
}

// RecordCollectorFlush records the duration of a collector flush operation
func RecordCollectorFlush(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	CollectorFlushDuration.Observe(duration.Seconds())
}

// RecordCloudAck records an ACK from cloud
func RecordCloudAck(success bool, reason string) {
	if success {
		CloudAcksReceived.Inc()
	} else {
		CloudNacksReceived.WithLabelValues(reason).Inc()
	}
}

// RecordMissingSequence increments missing sequence counter
func RecordMissingSequence(count int) {
	if count > 0 {
		ForwarderMissingSeqTotal.Add(float64(count))
	}
}

// RecordBackpressure increments backpressure counter
func RecordBackpressure() {
	ForwarderBackpressureTotal.Inc()
}

// RecordControlApply records a control plane config update
func RecordControlApply(field string) {
	ControlApplyTotal.WithLabelValues(field).Inc()
}

// RecordSignatureFail increments signature failure counter
func RecordSignatureFail() {
	SignatureFailTotal.Inc()
}

// RecordContinuousRCTFailure increments the RCT failure counter
func RecordContinuousRCTFailure() {
	ContinuousRCTFailures.Inc()
}

// RecordContinuousAPTFailure increments the APT failure counter
func RecordContinuousAPTFailure() {
	ContinuousAPTFailures.Inc()
}

// RecordMinEntropyMCV records a min-entropy estimate using the MCV method
func RecordMinEntropyMCV(minEntropy float64) {
	// Clamp to valid range [0.0, 8.0]
	if minEntropy < 0.0 {
		minEntropy = 0.0
	} else if minEntropy > 8.0 {
		minEntropy = 8.0
	}
	MinEntropyEstimateMCV.Observe(minEntropy)
}

// RecordMinEntropyCollision records a min-entropy estimate using the Collision method
func RecordMinEntropyCollision(minEntropy float64) {
	// Clamp to valid range [0.0, 8.0]
	if minEntropy < 0.0 {
		minEntropy = 0.0
	} else if minEntropy > 8.0 {
		minEntropy = 8.0
	}
	MinEntropyEstimateCollision.Observe(minEntropy)
}

// SetGRPCServerConnectedClients updates the number of connected gRPC streaming clients
func SetGRPCServerConnectedClients(count int) {
	GRPCServerConnectedClients.Set(float64(count))
}

// RecordGRPCServerBatchSent increments the counter for batches sent to gRPC clients
func RecordGRPCServerBatchSent() {
	GRPCServerBatchesSent.Inc()
}

// RecordGRPCServerBatchDropped increments the counter for dropped batches with reason
func RecordGRPCServerBatchDropped(reason string) {
	GRPCServerBatchesDropped.WithLabelValues(reason).Inc()
}

// RecordGRPCServerAckReceived records an acknowledgment received from a gRPC client
func RecordGRPCServerAckReceived(success bool, reason string) {
	successLabel := "true"
	if !success {
		successLabel = "false"
	}
	GRPCServerAcksReceived.WithLabelValues(successLabel).Inc()
}

// channelLabel converts channel number to string label
func channelLabel(channel uint32) string {
	switch channel {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	default:
		return "unknown"
	}
}
