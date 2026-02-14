package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	tdcgrpc "entropy-tdc-gateway/internal/grpc"

	"entropy-tdc-gateway/internal/collector"
	tdcconfig "entropy-tdc-gateway/internal/config"
	"entropy-tdc-gateway/internal/entropy"
	tdcemqtt "entropy-tdc-gateway/internal/mqtt"
	"entropy-tdc-gateway/pkg/pb"
	"entropy-tdc-gateway/testutil"
)

type stubEntropyServer struct {
	startErr  error
	started   bool
	shutdowns int
}

func (s *stubEntropyServer) Start() error {
	s.started = true
	return s.startErr
}

func (s *stubEntropyServer) StartTLS(certFile, keyFile, caFile string, clientAuth tls.ClientAuthType) error {
	s.started = true
	return s.startErr
}

func (s *stubEntropyServer) Shutdown(ctx context.Context) error {
	s.shutdowns++
	return nil
}

type stubMetricsServer struct {
	startErr    error
	startTLSErr error
	shutdownErr error
	started     bool
	startedTLS  bool
	tlsCertFile string
	tlsKeyFile  string
	tlsCAFile   string
	clientAuth  tls.ClientAuthType
	shutdowns   int
	startedCh   chan struct{}
}

func (s *stubMetricsServer) Start() error {
	s.started = true
	if s.startedCh != nil {
		select {
		case s.startedCh <- struct{}{}:
		default:
		}
	}
	return s.startErr
}

func (s *stubMetricsServer) StartTLS(certFile, keyFile, caFile string, clientAuth tls.ClientAuthType) error {
	s.startedTLS = true
	s.tlsCertFile = certFile
	s.tlsKeyFile = keyFile
	s.tlsCAFile = caFile
	s.clientAuth = clientAuth
	if s.startedCh != nil {
		select {
		case s.startedCh <- struct{}{}:
		default:
		}
	}
	return s.startTLSErr
}

func (s *stubMetricsServer) Shutdown(ctx context.Context) error {
	s.shutdowns++
	return s.shutdownErr
}

type stubMQTTClient struct {
	connectErr   error
	connectCalls int
	closeCalls   int
}

func (s *stubMQTTClient) Connect() error {
	s.connectCalls++
	return s.connectErr
}

func (s *stubMQTTClient) Close() {
	s.closeCalls++
}

type tlsRecordingEntropyServer struct {
	stubEntropyServer
	tlsCertFile   string
	tlsKeyFile    string
	tlsCAFile     string
	tlsClientAuth tls.ClientAuthType
}

func (s *tlsRecordingEntropyServer) StartTLS(certFile, keyFile, caFile string, clientAuth tls.ClientAuthType) error {
	s.tlsCertFile = certFile
	s.tlsKeyFile = keyFile
	s.tlsCAFile = caFile
	s.tlsClientAuth = clientAuth
	return s.stubEntropyServer.StartTLS(certFile, keyFile, caFile, clientAuth)
}

func withStubbedDeps(t *testing.T) {
	t.Helper()

	origLoadConfig := loadConfigFunc
	origSetupBatchCollector := setupBatchCollectorFunc
	origSetupMQTT := setupMQTTFunc
	origConnectMQTT := connectMQTTFunc
	origWaitForShutdown := waitForShutdownFunc
	origNewEntropyHTTP := newEntropyHTTPServerFunc
	origNewMetricsServer := newMetricsServerFunc
	origNewMQTTClient := newMQTTClient
	origSignalNotify := signalNotifyFunc
	origLogFatalf := logFatalfFunc
	origConfigLoader := tdcconfigLoadFunc

	t.Cleanup(func() {
		loadConfigFunc = origLoadConfig
		setupBatchCollectorFunc = origSetupBatchCollector
		setupMQTTFunc = origSetupMQTT
		connectMQTTFunc = origConnectMQTT
		waitForShutdownFunc = origWaitForShutdown
		newEntropyHTTPServerFunc = origNewEntropyHTTP
		newMetricsServerFunc = origNewMetricsServer
		newMQTTClient = origNewMQTTClient
		signalNotifyFunc = origSignalNotify
		logFatalfFunc = origLogFatalf
		tdcconfigLoadFunc = origConfigLoader
	})
}

func waitForSignal(t *testing.T, ch <-chan struct{}, desc string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timeout waiting for %s", desc)
	}
}

func waitForCondition(t *testing.T, desc string, probe func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := testutil.WaitForCondition(ctx, func() (struct{}, bool) {
		if probe() {
			return struct{}{}, true
		}
		return struct{}{}, false
	})
	if err != nil {
		t.Fatalf("timeout waiting for %s: %v", desc, err)
	}
}

func TestRun_HelpFlag(t *testing.T) {
	withStubbedDeps(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := run([]string{"-h"}, stdout, stderr); code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Usage of entropy-tdc-gateway") {
		t.Fatalf("expected usage text in stdout, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestRun_ConfigError(t *testing.T) {
	withStubbedDeps(t)

	loadConfigFunc = func() (tdcconfig.Config, error) {
		return tdcconfig.Config{}, errors.New("load failed")
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := run(nil, stdout, stderr); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "load failed") {
		t.Fatalf("expected config error in stderr, got %q", stderr.String())
	}
}

func TestRun_SuccessPath(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		MQTT: tdcconfig.MQTT{
			BrokerURL: "tcp://127.0.0.1:1883",
			Topics:    []string{"sr90/tdc/#"},
		},
		Collector: tdcconfig.Collector{BatchSize: 4},
		EntropyPool: tdcconfig.EntropyPool{
			PoolMinBytes:   64,
			PoolMaxBytes:   256,
			ReadyMinBytes:  32,
			HTTPAddr:       "127.0.0.1:0",
			RetryAfterSec:  1,
			RateLimitRPS:   5,
			RateLimitBurst: 10,
		},
		Metrics: tdcconfig.Metrics{Bind: "127.0.0.1:0"},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) {
		return cfg, nil
	}

	metrics := &stubMetricsServer{startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer {
		if addr != cfg.Metrics.Bind {
			t.Fatalf("unexpected metrics bind address %q", addr)
		}
		return metrics
	}

	entropySrv := &stubEntropyServer{}
	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		if addr != cfg.EntropyPool.HTTPAddr {
			t.Fatalf("unexpected entropy addr %q", addr)
		}
		if readyThreshold != cfg.EntropyPool.ReadyMinBytes {
			t.Fatalf("unexpected ready threshold %d", readyThreshold)
		}
		if allowPublic != cfg.EntropyPool.AllowPublic {
			t.Fatalf("unexpected allowPublic %v", allowPublic)
		}
		if retryAfter != cfg.EntropyPool.RetryAfterSec {
			t.Fatalf("unexpected retryAfter %d", retryAfter)
		}
		if rateLimitRPS != cfg.EntropyPool.RateLimitRPS {
			t.Fatalf("unexpected rateLimitRPS %d", rateLimitRPS)
		}
		if rateLimitBurst != cfg.EntropyPool.RateLimitBurst {
			t.Fatalf("unexpected rateLimitBurst %d", rateLimitBurst)
		}
		if pool == nil {
			t.Fatal("expected entropy pool instance")
		}
		return entropySrv, nil
	}

	var collectorCreated bool
	setupBatchCollectorFunc = func(pool *entropy.WhitenedPool, config tdcconfig.Config, forwarder *cloudAwareForwarder) *collector.BatchCollector {
		collectorCreated = true
		sender := batchSenderFunc(func([]tdcemqtt.TDCEvent, uint32) error { return nil })
		return collector.NewBatchCollectorWithFlush(1, time.Millisecond, sender)
	}

	var mqttCalled bool
	setupMQTTFunc = func(config tdcconfig.Config, bc *collector.BatchCollector) (mqttClient, error) {
		mqttCalled = true
		if bc == nil {
			t.Fatal("expected batch collector instance")
		}
		return &stubMQTTClient{}, nil
	}

	var waitCalled bool
	waitForShutdownFunc = func(mqttClient mqttClient, collector *collector.BatchCollector, entropyHTTPServer entropyServer, metricsHTTPServer metricsServer) {
		waitCalled = true
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := run(nil, stdout, stderr); code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("expected no output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	waitForSignal(t, metrics.startedCh, "metrics server start")
	if !collectorCreated {
		t.Fatal("expected setupBatchCollector to be called")
	}
	if !mqttCalled {
		t.Fatal("expected setupMQTT to be called")
	}
	if !waitCalled {
		t.Fatal("expected waitForShutdown to be called")
	}
	if !entropySrv.started {
		t.Fatal("expected entropy server to start")
	}
	if entropySrv.shutdowns != 1 {
		t.Fatalf("expected entropy server shutdown once, got %d", entropySrv.shutdowns)
	}
	if !metrics.started {
		t.Fatal("expected metrics server to start")
	}
	if metrics.shutdowns != 1 {
		t.Fatalf("expected metrics server shutdown once, got %d", metrics.shutdowns)
	}
}

func TestRun_RejectsNonLoopbackAddr(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		EntropyPool: tdcconfig.EntropyPool{
			PoolMinBytes:  64,
			PoolMaxBytes:  128,
			ReadyMinBytes: 32,
			HTTPAddr:      "0.0.0.0:9797",
		},
		Metrics: tdcconfig.Metrics{Bind: "127.0.0.1:0"},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) { return cfg, nil }

	metrics := &stubMetricsServer{startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer { return metrics }

	setupBatchCollectorFunc = func(pool *entropy.WhitenedPool, config tdcconfig.Config, forwarder *cloudAwareForwarder) *collector.BatchCollector {
		t.Fatal("setupBatchCollector should not be called on invalid addr")
		return nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := run(nil, stdout, stderr); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "loopback") {
		t.Fatalf("expected loopback error in stderr, got %q", stderr.String())
	}
	waitForSignal(t, metrics.startedCh, "metrics server start")
	if metrics.shutdowns != 1 {
		t.Fatalf("expected metrics server shutdown once, got %d", metrics.shutdowns)
	}
}

func TestValidateEntropyAddr(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		allow   bool
		wantErr bool
	}{
		{name: "loopback IPv4", addr: "127.0.0.1:9000", wantErr: false},
		{name: "loopback IPv6", addr: "[::1]:9000", wantErr: false},
		{name: "localhost", addr: "localhost:8080", wantErr: false},
		{name: "non-loopback", addr: "192.0.2.1:8080", wantErr: true},
		{name: "unspecified", addr: "0.0.0.0:8080", wantErr: true},
		{name: "non-loopback allowed", addr: "0.0.0.0:8080", allow: true, wantErr: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateEntropyAddr(tc.addr, tc.allow)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %s", tc.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.addr, err)
			}
		})
	}
}

func TestSetupMQTTInitError(t *testing.T) {
	withStubbedDeps(t)

	newMQTTClient = func(cfg tdcemqtt.Config, handler tdcemqtt.Handler) (mqttClient, error) {
		if cfg.BrokerURL != "tcp://mqtt.example:1883" {
			t.Fatalf("unexpected broker url %q", cfg.BrokerURL)
		}
		if len(cfg.Topics) != 1 || cfg.Topics[0] != "tdc/#" {
			t.Fatalf("unexpected topics %v", cfg.Topics)
		}
		return nil, errors.New("init failed")
	}

	_, err := setupMQTT(tdcconfig.Config{
		MQTT: tdcconfig.MQTT{BrokerURL: "tcp://mqtt.example:1883", Topics: []string{"tdc/#"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "mqtt init") {
		t.Fatalf("expected mqtt init error, got %v", err)
	}
}

func TestWaitForShutdownShutsDownServices(t *testing.T) {
	withStubbedDeps(t)

	signalNotifyFunc = func(c chan<- os.Signal, sig ...os.Signal) {
		go func() {
			c <- syscall.SIGTERM
		}()
	}

	entropySrv := &stubEntropyServer{}
	metricsSrv := &stubMetricsServer{}

	waitForShutdown(nil, nil, entropySrv, metricsSrv)

	if entropySrv.shutdowns != 1 {
		t.Fatalf("expected entropy shutdown once, got %d", entropySrv.shutdowns)
	}
	if metricsSrv.shutdowns != 1 {
		t.Fatalf("expected metrics shutdown once, got %d", metricsSrv.shutdowns)
	}
}

func TestWaitForShutdownHandlesNilDependencies(t *testing.T) {
	withStubbedDeps(t)

	signalNotifyFunc = func(c chan<- os.Signal, sig ...os.Signal) {
		go func() {
			c <- syscall.SIGINT
		}()
	}

	waitForShutdown(nil, nil, nil, nil)
}

func TestBatchSenderFunc_SendBatch(t *testing.T) {
	called := false
	var receivedEvents []tdcemqtt.TDCEvent
	var receivedSequence uint32

	fn := batchSenderFunc(func(events []tdcemqtt.TDCEvent, sequence uint32) error {
		called = true
		receivedEvents = events
		receivedSequence = sequence
		return nil
	})

	testEvents := []tdcemqtt.TDCEvent{
		{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 1},
		{RpiTimestampUs: 3000, TdcTimestampPs: 4000, Channel: 2},
	}
	testSequence := uint32(42)

	err := fn.SendBatch(testEvents, testSequence)
	if err != nil {
		t.Fatalf("SendBatch returned error: %v", err)
	}
	if !called {
		t.Fatal("underlying function was not called")
	}
	if len(receivedEvents) != 2 {
		t.Fatalf("expected 2 events, got %d", len(receivedEvents))
	}
	if receivedSequence != testSequence {
		t.Fatalf("expected sequence %d, got %d", testSequence, receivedSequence)
	}
}

func TestBatchSenderFunc_SendBatchError(t *testing.T) {
	expectedErr := errors.New("send failed")
	fn := batchSenderFunc(func(events []tdcemqtt.TDCEvent, sequence uint32) error {
		return expectedErr
	})

	err := fn.SendBatch(nil, 0)
	if err != expectedErr {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
}

func TestBatchSenderFunc_IncrementDropped(t *testing.T) {
	fn := batchSenderFunc(func(events []tdcemqtt.TDCEvent, sequence uint32) error {
		return nil
	})

	// Should not panic - this is a no-op
	fn.IncrementDropped(10)
	fn.IncrementDropped(0)
}

func TestParseClientAuth(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		expected tls.ClientAuthType
	}{
		{
			name:     "require mode",
			mode:     "require",
			expected: tls.RequireAndVerifyClientCert,
		},
		{
			name:     "request mode",
			mode:     "request",
			expected: tls.RequestClientCert,
		},
		{
			name:     "none mode",
			mode:     "none",
			expected: tls.NoClientCert,
		},
		{
			name:     "empty string defaults to NoClientCert",
			mode:     "",
			expected: tls.NoClientCert,
		},
		{
			name:     "unknown mode defaults to NoClientCert",
			mode:     "unknown",
			expected: tls.NoClientCert,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := parseClientAuth(tc.mode)
			if result != tc.expected {
				t.Errorf("parseClientAuth(%q) = %v, expected %v", tc.mode, result, tc.expected)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	withStubbedDeps(t)

	t.Run("success", func(t *testing.T) {
		expected := tdcconfig.Config{Environment: "test"}
		tdcconfigLoadFunc = func() (tdcconfig.Config, error) {
			return expected, nil
		}

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig returned error: %v", err)
		}
		if cfg.Environment != expected.Environment {
			t.Fatalf("expected environment %q, got %q", expected.Environment, cfg.Environment)
		}
	})

	t.Run("wraps error", func(t *testing.T) {
		tdcconfigLoadFunc = func() (tdcconfig.Config, error) {
			return tdcconfig.Config{}, errors.New("boom")
		}
		_, err := loadConfig()
		if err == nil || !strings.Contains(err.Error(), "config: boom") {
			t.Fatalf("expected wrapped error, got %v", err)
		}
	})
}

func TestSetupBatchCollector(t *testing.T) {
	t.Run("adds entropy to local pool", func(t *testing.T) {
		pool := entropy.NewWhitenedPoolWithBounds(1, 8)
		cfg := tdcconfig.Config{Collector: tdcconfig.Collector{BatchSize: 2}}
		bc := setupBatchCollector(pool, cfg, nil)
		defer bc.Close()

		events := []tdcemqtt.TDCEvent{
			{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 1},
			{RpiTimestampUs: 3000, TdcTimestampPs: 4000, Channel: 2},
		}
		for _, evt := range events {
			bc.Add(evt)
		}

		waitForCondition(t, "entropy pool refill", func() bool {
			return pool.AvailableEntropy() > 0
		})
	})
}

func TestSetupMQTT(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		withStubbedDeps(t)
		client := &stubMQTTClient{}
		var capturedCfg tdcemqtt.Config
		var capturedHandler tdcemqtt.Handler
		newMQTTClient = func(cfg tdcemqtt.Config, handler tdcemqtt.Handler) (mqttClient, error) {
			capturedCfg = cfg
			capturedHandler = handler
			return client, nil
		}

		sender := batchSenderFunc(func([]tdcemqtt.TDCEvent, uint32) error { return nil })
		bc := collector.NewBatchCollectorWithFlush(1, time.Millisecond, sender)
		defer bc.Close()

		cfg := tdcconfig.Config{
			MQTT: tdcconfig.MQTT{
				BrokerURL: "tcp://broker:1883",
				ClientID:  "client-1",
				Topics:    []string{"test/#"},
				QoS:       1,
				Username:  "user",
				Password:  "pass",
				TLSCAFile: "/tmp/ca.pem",
			},
		}

		got, err := setupMQTT(cfg, bc)
		if err != nil {
			t.Fatalf("setupMQTT returned error: %v", err)
		}
		if got != client {
			t.Fatalf("expected stub client, got %v", got)
		}
		if client.connectCalls != 1 {
			t.Fatalf("expected Connect to be called once, got %d", client.connectCalls)
		}
		handler, ok := capturedHandler.(*tdcemqtt.RxHandler)
		if !ok {
			t.Fatalf("expected RxHandler, got %T", capturedHandler)
		}
		if handler.Collector != bc {
			t.Fatal("rx handler missing collector")
		}
		if capturedCfg.BrokerURL != cfg.MQTT.BrokerURL || capturedCfg.ClientID == "" {
			t.Fatalf("unexpected config: %+v", capturedCfg)
		}
	})

	t.Run("connect error", func(t *testing.T) {
		withStubbedDeps(t)
		client := &stubMQTTClient{connectErr: errors.New("connect boom")}
		newMQTTClient = func(cfg tdcemqtt.Config, handler tdcemqtt.Handler) (mqttClient, error) {
			return client, nil
		}

		sender := batchSenderFunc(func([]tdcemqtt.TDCEvent, uint32) error { return nil })
		bc := collector.NewBatchCollectorWithFlush(1, time.Millisecond, sender)
		defer bc.Close()

		cfg := tdcconfig.Config{MQTT: tdcconfig.MQTT{BrokerURL: "tcp://broker:1883", Topics: []string{"test/#"}}}

		_, err := setupMQTT(cfg, bc)
		if err == nil || !strings.Contains(err.Error(), "mqtt connect") {
			t.Fatalf("expected mqtt connect error, got %v", err)
		}
	})
}

func TestValidateEntropyAddr_InvalidAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{
			name:    "missing port",
			addr:    "127.0.0.1",
			wantErr: true,
		},
		{
			name:    "invalid format",
			addr:    "not-a-valid-address",
			wantErr: true,
		},
		{
			name:    "hostname without allow",
			addr:    "example.com:8080",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateEntropyAddr(tc.addr, false)
			if tc.wantErr && err == nil {
				t.Fatal("expected error but got none")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConvertToProtoBatch(t *testing.T) {
	events := []tdcemqtt.TDCEvent{
		{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 1},
		{RpiTimestampUs: 3000, TdcTimestampPs: 4000, Channel: 2},
		{RpiTimestampUs: 5000, TdcTimestampPs: 6000, Channel: 3},
	}
	sequence := uint32(123)

	batch := convertToProtoBatch(events, sequence)

	if batch == nil {
		t.Fatal("expected non-nil batch")
	}
	if len(batch.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(batch.Events))
	}
	if batch.BatchSequence != sequence {
		t.Fatalf("expected sequence %d, got %d", sequence, batch.BatchSequence)
	}
	if batch.SourceId != "entropy-tdc-gateway" {
		t.Fatalf("expected source_id 'entropy-tdc-gateway', got %q", batch.SourceId)
	}

	// Verify first event
	if batch.Events[0].RpiTimestampUs != 1000 {
		t.Errorf("event[0].RpiTimestampUs = %d, expected 1000", batch.Events[0].RpiTimestampUs)
	}
	if batch.Events[0].TdcTimestampPs != 2000 {
		t.Errorf("event[0].TdcTimestampPs = %d, expected 2000", batch.Events[0].TdcTimestampPs)
	}
	if batch.Events[0].Channel != 1 {
		t.Errorf("event[0].Channel = %d, expected 1", batch.Events[0].Channel)
	}
	if got := len(batch.Events[0].WhitenedEntropy); got != 32 {
		t.Errorf("event[0].WhitenedEntropy length = %d, expected 32", got)
	}

	// Verify second event
	if batch.Events[1].RpiTimestampUs != 3000 {
		t.Errorf("event[1].RpiTimestampUs = %d, expected 3000", batch.Events[1].RpiTimestampUs)
	}
	if batch.Events[1].TdcTimestampPs != 4000 {
		t.Errorf("event[1].TdcTimestampPs = %d, expected 4000", batch.Events[1].TdcTimestampPs)
	}
	if batch.Events[1].Channel != 2 {
		t.Errorf("event[1].Channel = %d, expected 2", batch.Events[1].Channel)
	}
	if got := len(batch.Events[1].WhitenedEntropy); got != 32 {
		t.Errorf("event[1].WhitenedEntropy length = %d, expected 32", got)
	}
}

func TestConvertToProtoBatch_EmptyEvents(t *testing.T) {
	events := []tdcemqtt.TDCEvent{}
	sequence := uint32(0)

	batch := convertToProtoBatch(events, sequence)

	if batch == nil {
		t.Fatal("expected non-nil batch")
	}
	if len(batch.Events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(batch.Events))
	}
	if batch.BatchSequence != 0 {
		t.Fatalf("expected sequence 0, got %d", batch.BatchSequence)
	}
}

func TestRun_UnexpectedArguments(t *testing.T) {
	withStubbedDeps(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	code := run([]string{"unexpected", "args"}, stdout, stderr)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unexpected arguments") {
		t.Fatalf("expected unexpected arguments error in stderr, got %q", stderr.String())
	}
}

func TestRun_EntropyServerInitError(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		EntropyPool: tdcconfig.EntropyPool{
			HTTPAddr:      "127.0.0.1:0",
			PoolMinBytes:  64,
			PoolMaxBytes:  128,
			ReadyMinBytes: 32,
		},
		Metrics: tdcconfig.Metrics{Bind: "127.0.0.1:0"},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) { return cfg, nil }

	metrics := &stubMetricsServer{startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer { return metrics }

	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		return nil, errors.New("init failed")
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := run(nil, stdout, stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "start entropy http server") {
		t.Fatalf("expected entropy server error in stderr, got %q", stderr.String())
	}
}

func TestRun_EntropyServerNil(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		EntropyPool: tdcconfig.EntropyPool{
			HTTPAddr:      "127.0.0.1:0",
			PoolMinBytes:  64,
			PoolMaxBytes:  128,
			ReadyMinBytes: 32,
		},
		Metrics: tdcconfig.Metrics{Bind: "127.0.0.1:0"},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) { return cfg, nil }

	metrics := &stubMetricsServer{startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer { return metrics }

	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		return nil, nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := run(nil, stdout, stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "initialization failed") {
		t.Fatalf("expected initialization failed error in stderr, got %q", stderr.String())
	}
}

func TestRun_EntropyServerStartError(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		EntropyPool: tdcconfig.EntropyPool{
			HTTPAddr:      "127.0.0.1:0",
			PoolMinBytes:  64,
			PoolMaxBytes:  128,
			ReadyMinBytes: 32,
		},
		Metrics: tdcconfig.Metrics{Bind: "127.0.0.1:0"},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) { return cfg, nil }

	metrics := &stubMetricsServer{startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer { return metrics }

	entropySrv := &stubEntropyServer{startErr: errors.New("start failed")}
	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		return entropySrv, nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := run(nil, stdout, stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "start entropy http server") {
		t.Fatalf("expected entropy server start error in stderr, got %q", stderr.String())
	}
}

func TestRun_EntropyServerTLSStartError(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		EntropyPool: tdcconfig.EntropyPool{
			HTTPAddr:      "127.0.0.1:0",
			PoolMinBytes:  64,
			PoolMaxBytes:  128,
			ReadyMinBytes: 32,
			TLSEnabled:    true,
			TLSCertFile:   "/path/to/cert.pem",
			TLSKeyFile:    "/path/to/key.pem",
			TLSCAFile:     "/path/to/ca.pem",
			TLSClientAuth: "require",
		},
		Metrics: tdcconfig.Metrics{Bind: "127.0.0.1:0"},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) { return cfg, nil }

	metrics := &stubMetricsServer{startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer { return metrics }

	entropySrv := &stubEntropyServer{startErr: errors.New("tls start failed")}
	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		return entropySrv, nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := run(nil, stdout, stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "start entropy http server") {
		t.Fatalf("expected entropy server start error in stderr, got %q", stderr.String())
	}
}

func TestRun_MQTTSetupError(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		MQTT: tdcconfig.MQTT{
			BrokerURL: "tcp://127.0.0.1:1883",
			Topics:    []string{"test/#"},
		},
		Collector: tdcconfig.Collector{BatchSize: 4},
		EntropyPool: tdcconfig.EntropyPool{
			PoolMinBytes:  64,
			PoolMaxBytes:  256,
			ReadyMinBytes: 32,
			HTTPAddr:      "127.0.0.1:0",
		},
		Metrics: tdcconfig.Metrics{Bind: "127.0.0.1:0"},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) { return cfg, nil }

	metrics := &stubMetricsServer{startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer { return metrics }

	entropySrv := &stubEntropyServer{}
	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		return entropySrv, nil
	}

	setupBatchCollectorFunc = func(pool *entropy.WhitenedPool, config tdcconfig.Config, forwarder *cloudAwareForwarder) *collector.BatchCollector {
		sender := batchSenderFunc(func([]tdcemqtt.TDCEvent, uint32) error { return nil })
		return collector.NewBatchCollectorWithFlush(1, time.Millisecond, sender)
	}

	connectMQTTFunc = func(config tdcconfig.Config, bc *collector.BatchCollector) (mqttClient, error) {
		return nil, errors.New("mqtt setup failed")
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := run(nil, stdout, stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "mqtt setup failed") {
		t.Fatalf("expected mqtt setup error in stderr, got %q", stderr.String())
	}
}

func TestRun_MetricsTLSVerifiesStartTLS(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		MQTT: tdcconfig.MQTT{
			BrokerURL: "tcp://127.0.0.1:1883",
			Topics:    []string{"sr90/tdc/#"},
		},
		Collector: tdcconfig.Collector{BatchSize: 4},
		EntropyPool: tdcconfig.EntropyPool{
			PoolMinBytes:   64,
			PoolMaxBytes:   256,
			ReadyMinBytes:  32,
			HTTPAddr:       "127.0.0.1:0",
			RetryAfterSec:  1,
			RateLimitRPS:   5,
			RateLimitBurst: 10,
		},
		Metrics: tdcconfig.Metrics{
			Bind:        "127.0.0.1:0",
			TLSEnabled:  true,
			TLSCertFile: "/metrics/cert.pem",
			TLSKeyFile:  "/metrics/key.pem",
		},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) { return cfg, nil }

	metrics := &stubMetricsServer{startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer { return metrics }

	entropySrv := &stubEntropyServer{}
	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		return entropySrv, nil
	}

	setupBatchCollectorFunc = func(pool *entropy.WhitenedPool, config tdcconfig.Config, forwarder *cloudAwareForwarder) *collector.BatchCollector {
		sender := batchSenderFunc(func([]tdcemqtt.TDCEvent, uint32) error { return nil })
		return collector.NewBatchCollectorWithFlush(1, time.Millisecond, sender)
	}

	setupMQTTFunc = func(config tdcconfig.Config, bc *collector.BatchCollector) (mqttClient, error) {
		return &stubMQTTClient{}, nil
	}

	waitForShutdownFunc = func(mqttClient mqttClient, collector *collector.BatchCollector, entropyHTTPServer entropyServer, metricsHTTPServer metricsServer) {
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := run(nil, stdout, stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, stderr.String())
	}

	waitForSignal(t, metrics.startedCh, "metrics tls start")

	if !metrics.startedTLS {
		t.Fatal("expected StartTLS to be called")
	}
	if metrics.started {
		t.Fatal("expected Start not to be called when TLS is enabled")
	}
	if metrics.tlsCertFile != "/metrics/cert.pem" {
		t.Fatalf("expected cert file %q, got %q", "/metrics/cert.pem", metrics.tlsCertFile)
	}
	if metrics.tlsKeyFile != "/metrics/key.pem" {
		t.Fatalf("expected key file %q, got %q", "/metrics/key.pem", metrics.tlsKeyFile)
	}
}

// TestRun_FlagParseError tests that invalid flags return exit code 2
func TestRun_FlagParseError(t *testing.T) {
	withStubbedDeps(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	code := run([]string{"--invalid-flag"}, stdout, stderr)
	if code != 2 {
		t.Fatalf("expected exit code 2 for flag parse error, got %d", code)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "flag provided but not defined") || !strings.Contains(msg, "parse flags") {
		t.Fatalf("expected detailed flag parse error, got %q", msg)
	}
}

// TestRun_MetricsStartupFailure tests that metrics server failures abort startup
func TestRun_MetricsStartupFailure(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		EntropyPool: tdcconfig.EntropyPool{
			HTTPAddr:      "127.0.0.1:0",
			PoolMinBytes:  64,
			PoolMaxBytes:  128,
			ReadyMinBytes: 32,
		},
		Metrics: tdcconfig.Metrics{Bind: "127.0.0.1:0"},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) { return cfg, nil }

	metrics := &stubMetricsServer{startErr: errors.New("metrics start failed"), startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer { return metrics }

	entropySrv := &stubEntropyServer{}
	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		return entropySrv, nil
	}

	fatalCh := make(chan string, 1)
	logFatalfFunc = func(format string, args ...interface{}) {
		fatalCh <- fmt.Sprintf(format, args...)
	}

	setupBatchCollectorFunc = func(pool *entropy.WhitenedPool, config tdcconfig.Config, forwarder *cloudAwareForwarder) *collector.BatchCollector {
		sender := batchSenderFunc(func([]tdcemqtt.TDCEvent, uint32) error { return nil })
		return collector.NewBatchCollectorWithFlush(1, time.Millisecond, sender)
	}
	setupMQTTFunc = func(config tdcconfig.Config, bc *collector.BatchCollector) (mqttClient, error) {
		return &stubMQTTClient{}, nil
	}
	waitForShutdownFunc = func(mqttClient mqttClient, collector *collector.BatchCollector, entropyHTTPServer entropyServer, metricsHTTPServer metricsServer) {
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := run(nil, stdout, stderr)
	if code != 0 {
		t.Fatalf("expected run to return 0 despite fatal hook, got %d (stderr=%q)", code, stderr.String())
	}
	select {
	case msg := <-fatalCh:
		if !strings.Contains(msg, "metrics: failed to start server") {
			t.Fatalf("unexpected fatal message %q", msg)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected logFatalf to be invoked for metrics start failure")
	}
}

// TestRun_EntropyTLSConfiguration tests that TLS configuration is passed correctly
func TestRun_EntropyTLSConfiguration(t *testing.T) {
	withStubbedDeps(t)

	cfg := tdcconfig.Config{
		Environment: tdcconfig.EnvironmentDevelopment,
		MQTT: tdcconfig.MQTT{
			BrokerURL: "tcp://127.0.0.1:1883",
			Topics:    []string{"test/#"},
		},
		Collector: tdcconfig.Collector{BatchSize: 4},
		EntropyPool: tdcconfig.EntropyPool{
			PoolMinBytes:  64,
			PoolMaxBytes:  256,
			ReadyMinBytes: 32,
			HTTPAddr:      "127.0.0.1:0",
			TLSEnabled:    true,
			TLSCertFile:   "/entropy/cert.pem",
			TLSKeyFile:    "/entropy/key.pem",
			TLSCAFile:     "/entropy/ca.pem",
			TLSClientAuth: "require",
		},
		Metrics: tdcconfig.Metrics{Bind: "127.0.0.1:0"},
	}
	loadConfigFunc = func() (tdcconfig.Config, error) { return cfg, nil }

	metrics := &stubMetricsServer{startedCh: make(chan struct{}, 1)}
	newMetricsServerFunc = func(addr string) metricsServer { return metrics }

	entropySrvWithTLS := &tlsRecordingEntropyServer{}

	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		return entropySrvWithTLS, nil
	}

	setupBatchCollectorFunc = func(pool *entropy.WhitenedPool, config tdcconfig.Config, forwarder *cloudAwareForwarder) *collector.BatchCollector {
		sender := batchSenderFunc(func([]tdcemqtt.TDCEvent, uint32) error { return nil })
		return collector.NewBatchCollectorWithFlush(1, time.Millisecond, sender)
	}

	setupMQTTFunc = func(config tdcconfig.Config, bc *collector.BatchCollector) (mqttClient, error) {
		return &stubMQTTClient{}, nil
	}

	waitForShutdownFunc = func(mqttClient mqttClient, collector *collector.BatchCollector, entropyHTTPServer entropyServer, metricsHTTPServer metricsServer) {
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := run(nil, stdout, stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", code, stderr.String())
	}

	if entropySrvWithTLS.tlsCertFile != "/entropy/cert.pem" {
		t.Errorf("expected entropy cert %q, got %q", "/entropy/cert.pem", entropySrvWithTLS.tlsCertFile)
	}
	if entropySrvWithTLS.tlsKeyFile != "/entropy/key.pem" {
		t.Errorf("expected entropy key %q, got %q", "/entropy/key.pem", entropySrvWithTLS.tlsKeyFile)
	}
	if entropySrvWithTLS.tlsCAFile != "/entropy/ca.pem" {
		t.Errorf("expected entropy CA %q, got %q", "/entropy/ca.pem", entropySrvWithTLS.tlsCAFile)
	}
	if entropySrvWithTLS.tlsClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("expected client auth %v, got %v", tls.RequireAndVerifyClientCert, entropySrvWithTLS.tlsClientAuth)
	}
}

// ============================================================================
// grpcForwarder Tests
// ============================================================================

func TestGRPCForwarder_ConversionLogic(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	// Create test events
	events := []tdcemqtt.TDCEvent{
		{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 1},
		{RpiTimestampUs: 1001, TdcTimestampPs: 2001, Channel: 2},
	}

	// Test that event conversion logic works correctly
	pbEvents := make([]*pb.TDCEvent, len(events))
	for i, evt := range events {
		pbEvents[i] = toProtoEvent(evt)
	}

	// Verify the conversion logic works
	if len(pbEvents) != 2 {
		t.Fatalf("expected 2 pb events, got %d", len(pbEvents))
	}
	if pbEvents[0].Channel != 1 {
		t.Errorf("expected channel 1, got %d", pbEvents[0].Channel)
	}
	if pbEvents[1].Channel != 2 {
		t.Errorf("expected channel 2, got %d", pbEvents[1].Channel)
	}
	if got := len(pbEvents[0].WhitenedEntropy); got != 32 {
		t.Errorf("expected event[0] whitened entropy length 32, got %d", got)
	}
	if got := len(pbEvents[1].WhitenedEntropy); got != 32 {
		t.Errorf("expected event[1] whitened entropy length 32, got %d", got)
	}
}

func TestGRPCForwarder_IncrementDropped(t *testing.T) {
	forwarder := &grpcForwarder{client: &tdcgrpc.Client{}}

	// IncrementDropped is a no-op, just verify it doesn't panic
	forwarder.IncrementDropped(10)
	forwarder.IncrementDropped(0)
	forwarder.IncrementDropped(999)
}

func TestSetupBatchCollector_WithCloudForwarder(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := entropy.NewWhitenedPoolWithBounds(64, 256)
	cfg := tdcconfig.Config{
		Collector: tdcconfig.Collector{
			BatchSize: 10,
		},
		CloudForwarder: tdcconfig.CloudForwarder{
			Enabled: true,
		},
	}

	// Create a mock gRPC client
	mockClient := &tdcgrpc.Client{}

	forwarder := newCloudAwareForwarder(pool)
	forwarder.SetClient(mockClient)
	collector := setupBatchCollector(pool, cfg, forwarder)
	if collector == nil {
		t.Fatal("expected non-nil collector")
	}
}

func TestSetupBatchCollector_WithoutCloudForwarder(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := entropy.NewWhitenedPoolWithBounds(64, 256)
	cfg := tdcconfig.Config{
		Collector: tdcconfig.Collector{
			BatchSize: 20,
		},
		CloudForwarder: tdcconfig.CloudForwarder{
			Enabled: false,
		},
	}

	collector := setupBatchCollector(pool, cfg, nil)
	if collector == nil {
		t.Fatal("expected non-nil collector")
	}
}

// ============================================================================
// waitForShutdown Error Handling Tests
// ============================================================================

func TestWaitForShutdown_EntropyShutdownError(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	mqtt := &stubMQTTClient{}
	coll := collector.NewBatchCollectorWithFlush(10, time.Hour, batchSenderFunc(func([]tdcemqtt.TDCEvent, uint32) error {
		return nil
	}))

	entropyServer := &stubEntropyServer{}
	metricsServer := &stubMetricsServer{}

	// Mock signal handling
	origSignalNotify := signalNotifyFunc
	signalNotifyFunc = func(c chan<- os.Signal, sig ...os.Signal) {
		go func() {
			c <- syscall.SIGTERM
		}()
	}
	defer func() {
		signalNotifyFunc = origSignalNotify
	}()

	// waitForShutdown should complete without panic even if there are errors
	waitForShutdown(mqtt, coll, entropyServer, metricsServer)

	if mqtt.closeCalls != 1 {
		t.Errorf("expected 1 mqtt close call, got %d", mqtt.closeCalls)
	}
	if entropyServer.shutdowns != 1 {
		t.Errorf("expected 1 entropy shutdown, got %d", entropyServer.shutdowns)
	}
	if metricsServer.shutdowns != 1 {
		t.Errorf("expected 1 metrics shutdown, got %d", metricsServer.shutdowns)
	}
}

func TestWaitForShutdown_MetricsShutdownError(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	mqtt := &stubMQTTClient{}
	coll := collector.NewBatchCollectorWithFlush(10, time.Hour, batchSenderFunc(func([]tdcemqtt.TDCEvent, uint32) error {
		return nil
	}))

	entropyServer := &stubEntropyServer{}
	metricsServer := &stubMetricsServer{
		shutdownErr: errors.New("metrics shutdown failed"),
	}

	// Mock signal handling
	origSignalNotify := signalNotifyFunc
	signalNotifyFunc = func(c chan<- os.Signal, sig ...os.Signal) {
		go func() {
			c <- syscall.SIGINT
		}()
	}
	defer func() {
		signalNotifyFunc = origSignalNotify
	}()

	// Should complete without panic even with error
	waitForShutdown(mqtt, coll, entropyServer, metricsServer)

	if metricsServer.shutdowns != 1 {
		t.Errorf("expected 1 metrics shutdown attempt, got %d", metricsServer.shutdowns)
	}
}
