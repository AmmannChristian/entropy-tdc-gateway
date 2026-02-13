package mqtt

import (
	"log"
	"strconv"
	"strings"
	"time"

	"entropy-tdc-gateway/internal/metrics"
)

var timeNowMicros = func() int64 {
	return time.Now().UnixMicro()
}

// Collector receives parsed TDC events for downstream batching and forwarding.
type Collector interface {
	Add(event TDCEvent)
	IncrementDropped(count uint32)
}

// RxHandler implements Handler by parsing raw MQTT payloads into TDCEvent
// values and forwarding them to the configured Collector.
type RxHandler struct {
	Collector Collector
}

// TDCEvent represents a single timestamp emitted by the Time-to-Digital
// Converter, as decoded from the MQTT payload and annotated with the
// gateway reception timestamp.
type TDCEvent struct {
	RpiTimestampUs uint64  `json:"rpi_timestamp_us"`
	Channel        uint32  `json:"channel"`
	TdcTimestampPs uint64  `json:"tdc_timestamp_ps"`
	DeltaPs        *int64  `json:"delta_ps,omitempty"`
	Flags          *uint32 `json:"flags,omitempty"`
}

// OnMessage parses the raw MQTT payload, validates the resulting TDCEvent, and
// forwards it to the Collector. Invalid or unparseable messages are counted as
// dropped events.
func (handler *RxHandler) OnMessage(topic string, payload []byte) {
	startTime := time.Now()
	metrics.RecordMQTTMessage()

	// lobsang publishes status metadata on ".../meta" using a Python dict string.
	// This is intentionally excluded from the TDC event stream.
	if isMetaTopic(topic) {
		return
	}

	event, err := parseRawTimestamp(topic, payload)
	if err != nil {
		metrics.RecordEventDropped("parse_error")
		log.Printf("parse error: %v", err)
		if handler.Collector != nil {
			handler.Collector.IncrementDropped(1)
		}
		return
	}

	if event.TdcTimestampPs == 0 {
		metrics.RecordEventDropped("invalid_timestamp")
		log.Printf("invalid event: zero tdc timestamp")
		if handler.Collector != nil {
			handler.Collector.IncrementDropped(1)
		}
		return
	}

	metrics.RecordEvent(event.Channel)
	metrics.RecordFormatType("raw_timestamp")
	metrics.RecordChannelEvent(event.Channel)

	if handler.Collector != nil {
		handler.Collector.Add(event)
	} else {
		log.Printf("rx: topic=%s rpi_ts=%d tdc_ts=%d ch=%d",
			topic, event.RpiTimestampUs, event.TdcTimestampPs, event.Channel)
	}

	latencyMicros := time.Since(startTime).Microseconds()
	if latencyMicros > 0 {
		metrics.ProcessingLatency.Observe(float64(latencyMicros))
	}
}

// isMetaTopic reports whether the topic carries non-event metadata.
func isMetaTopic(topic string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(topic)), "/meta")
}

// extractChannelFromTopic parses the trailing numeric segment of the MQTT
// topic path as a channel number. It returns 0 when the segment is absent
// or non-numeric.
func extractChannelFromTopic(topic string) uint32 {
	parts := strings.Split(topic, "/")
	if len(parts) == 0 {
		return 0
	}

	lastPart := parts[len(parts)-1]
	if ch, err := strconv.ParseUint(lastPart, 10, 32); err == nil {
		return uint32(ch)
	}

	return 0
}

// parseRawTimestamp decodes an MQTT payload containing a single decimal
// uint64 picosecond timestamp and constructs a TDCEvent annotated with the
// channel derived from the topic and the current gateway time.
func parseRawTimestamp(topic string, payload []byte) (TDCEvent, error) {
	timestampStr := strings.TrimSpace(string(payload))
	timestamp, err := strconv.ParseUint(timestampStr, 10, 64)
	if err != nil {
		return TDCEvent{}, err
	}

	channel := extractChannelFromTopic(topic)
	rpiTimestamp := nowMicrosSafe()

	return TDCEvent{
		RpiTimestampUs: rpiTimestamp,
		Channel:        channel,
		TdcTimestampPs: timestamp,
		DeltaPs:        nil,
		Flags:          nil,
	}, nil
}

// nowMicrosSafe returns the current time in microseconds, clamping negative
// values to zero.
func nowMicrosSafe() uint64 {
	ts := timeNowMicros()
	if ts <= 0 {
		return 0
	}
	return uint64(ts)
}
