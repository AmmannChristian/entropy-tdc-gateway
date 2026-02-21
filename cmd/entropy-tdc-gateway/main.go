package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"entropy-tdc-gateway/internal/collector"
	tdcconfig "entropy-tdc-gateway/internal/config"
	"entropy-tdc-gateway/internal/entropy"
	tdcgrpc "entropy-tdc-gateway/internal/grpc"
	"entropy-tdc-gateway/internal/metrics"
	tdcmqtt "entropy-tdc-gateway/internal/mqtt"
	"entropy-tdc-gateway/pkg/pb"

	"github.com/joho/godotenv"
)

var (
	loadConfigFunc           = loadConfig
	setupBatchCollectorFunc  = setupBatchCollector
	setupMQTTFunc            = setupMQTT
	connectMQTTFunc          = connectMQTTWithRetry
	waitForShutdownFunc      = waitForShutdown
	newEntropyHTTPServerFunc = func(addr string, pool *entropy.WhitenedPool, readyThreshold int, allowPublic bool, retryAfter int, rateLimitRPS int, rateLimitBurst int) (entropyServer, error) {
		return entropy.NewHTTPServer(addr, pool, readyThreshold, allowPublic, retryAfter, rateLimitRPS, rateLimitBurst)
	}
	newMetricsServerFunc = func(addr string) metricsServer {
		return metrics.NewServer(addr)
	}
	tdcconfigLoadFunc = tdcconfig.Load
)

var (
	newMQTTClient = func(cfg tdcmqtt.Config, handler tdcmqtt.Handler) (mqttClient, error) {
		return tdcmqtt.NewClient(cfg, handler)
	}
	signalNotifyFunc = signal.Notify
	logFatalfFunc    = log.Fatalf
)

type batchSenderFunc func([]tdcmqtt.TDCEvent, uint32) error

// grpcForwarder adapts a grpc.Client to the collector.BatchSender interface,
// converting TDCEvent values to protobuf and recording batch metrics.
type grpcForwarder struct {
	client *tdcgrpc.Client
}

func (g *grpcForwarder) SendBatch(events []tdcmqtt.TDCEvent, sequence uint32) error {
	pbEvents := make([]*pb.TDCEvent, len(events))
	for i, evt := range events {
		pbEvents[i] = toProtoEvent(evt)
	}

	startTime := time.Now()
	err := g.client.SendBatch(pbEvents, sequence)
	duration := time.Since(startTime).Seconds()

	if err != nil {
		metrics.RecordBatch(len(events), duration, false, err.Error())
		return err
	}

	metrics.RecordBatch(len(events), duration, true, "")
	return nil
}

func (g *grpcForwarder) IncrementDropped(count uint32) {}

type entropyServer interface {
	Start() error
	StartTLS(certFile, keyFile, caFile string, clientAuth tls.ClientAuthType) error
	Shutdown(context.Context) error
}

type metricsServer interface {
	Start() error
	StartTLS(certFile, keyFile, caFile string, clientAuth tls.ClientAuthType) error
	Shutdown(context.Context) error
}

type mqttClient interface {
	Connect() error
	Close()
}

func (fn batchSenderFunc) SendBatch(events []tdcmqtt.TDCEvent, sequence uint32) error {
	return fn(events, sequence)
}

func (fn batchSenderFunc) IncrementDropped(count uint32) {}

// cloudAwareForwarder feeds every batch to the local whitened entropy pool
// and, when a gRPC client is attached, mirrors the batch to the cloud
// processor service. The client can be injected after construction to
// support deferred or retried connections.
type cloudAwareForwarder struct {
	pool   *entropy.WhitenedPool
	client atomic.Pointer[tdcgrpc.Client]
}

func newCloudAwareForwarder(pool *entropy.WhitenedPool) *cloudAwareForwarder {
	return &cloudAwareForwarder{pool: pool}
}

func (f *cloudAwareForwarder) SetClient(c *tdcgrpc.Client) {
	f.client.Store(c)
	metrics.SetGRPCConnected(c != nil)
}

func (f *cloudAwareForwarder) Close() error {
	c := f.client.Swap(nil)
	metrics.SetGRPCConnected(false)
	if c != nil {
		return c.Close()
	}
	return nil
}

func (f *cloudAwareForwarder) SendBatch(events []tdcmqtt.TDCEvent, sequence uint32) error {
	start := time.Now()
	f.pool.AddBatch(events)
	localDuration := time.Since(start).Seconds()

	client := f.client.Load()
	if client == nil {
		metrics.RecordBatch(len(events), localDuration, true, "")
		return nil
	}

	pbEvents := make([]*pb.TDCEvent, len(events))
	for i, evt := range events {
		pbEvents[i] = toProtoEvent(evt)
	}

	if err := client.SendBatch(pbEvents, sequence); err != nil {
		metrics.RecordBatch(len(events), localDuration, false, err.Error())
		return err
	}

	metrics.RecordBatch(len(events), localDuration, true, "")
	return nil
}

func (f *cloudAwareForwarder) IncrementDropped(count uint32) {
	if count == 0 {
		return
	}
	metrics.RecordDroppedCount("forwarder", count)
}

// parseClientAuth maps a configuration string to the corresponding
// tls.ClientAuthType. Unrecognised values default to tls.NoClientCert.
func parseClientAuth(mode string) tls.ClientAuthType {
	switch mode {
	case "require":
		return tls.RequireAndVerifyClientCert
	case "request":
		return tls.RequestClientCert
	default:
		return tls.NoClientCert
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	if err := godotenv.Overload(".env"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("dotenv: %v", err)
	}

	fs := flag.NewFlagSet("entropy-tdc-gateway", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stdout, "Usage of %s:\n", fs.Name())
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.Usage()
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "parse flags: %v\n", err)
		return 2
	}

	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		fs.Usage()
		return 2
	}

	config, err := loadConfigFunc()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	metricsServer := newMetricsServerFunc(config.Metrics.Bind)
	go func() {
		var err error
		if config.Metrics.TLSEnabled {
			clientAuth := parseClientAuth(config.Metrics.TLSClientAuth)
			err = metricsServer.StartTLS(
				config.Metrics.TLSCertFile,
				config.Metrics.TLSKeyFile,
				config.Metrics.TLSCAFile,
				clientAuth,
			)
		} else {
			err = metricsServer.Start()
		}
		if err != nil {
			logFatalfFunc("metrics: failed to start server: %v", err)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := metricsServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("metrics: shutdown error: %v", err)
		}
	}()

	entropyPool := entropy.NewWhitenedPoolWithBounds(config.EntropyPool.PoolMinBytes, config.EntropyPool.PoolMaxBytes)

	if err := validateEntropyAddr(config.EntropyPool.HTTPAddr, config.EntropyPool.AllowPublic); err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	entropyServer, err := newEntropyHTTPServerFunc(
		config.EntropyPool.HTTPAddr,
		entropyPool,
		config.EntropyPool.ReadyMinBytes,
		config.EntropyPool.AllowPublic,
		config.EntropyPool.RetryAfterSec,
		config.EntropyPool.RateLimitRPS,
		config.EntropyPool.RateLimitBurst,
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "start entropy http server: %v\n", err)
		return 1
	}

	if entropyServer == nil {
		_, _ = fmt.Fprintln(stderr, "start entropy http server: initialization failed")
		return 1
	}

	if config.EntropyPool.TLSEnabled {
		clientAuth := parseClientAuth(config.EntropyPool.TLSClientAuth)
		if err := entropyServer.StartTLS(
			config.EntropyPool.TLSCertFile,
			config.EntropyPool.TLSKeyFile,
			config.EntropyPool.TLSCAFile,
			clientAuth,
		); err != nil {
			_, _ = fmt.Fprintf(stderr, "start entropy http server: %v\n", err)
			return 1
		}
	} else {
		if err := entropyServer.Start(); err != nil {
			_, _ = fmt.Fprintf(stderr, "start entropy http server: %v\n", err)
			return 1
		}
	}
	defer func() {
		if err := entropyServer.Shutdown(context.Background()); err != nil {
			log.Printf("error shutting down entropy server: %v", err)
		}
	}()

	var grpcForwarder *cloudAwareForwarder
	if config.CloudForwarder.Enabled {
		grpcForwarder = newCloudAwareForwarder(entropyPool)
		client, err := tdcgrpc.NewClient(config.CloudForwarder)
		if err != nil {
			log.Printf("grpc: cloud forwarder initial connect failed: %v (continuing offline, will retry)", err)
			startGRPCConnector(rootCtx, config.CloudForwarder, grpcForwarder)
		} else {
			grpcForwarder.SetClient(client)
			log.Printf("grpc: cloud forwarder connected to %s", config.CloudForwarder.ServerAddr)
		}
		defer func() {
			if err := grpcForwarder.Close(); err != nil {
				log.Printf("error closing grpc forwarder: %v", err)
			}
		}()
	}

	batchCollector := setupBatchCollectorFunc(entropyPool, config, grpcForwarder)
	defer batchCollector.Close()

	mqttClient, err := connectMQTTFunc(config, batchCollector)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	defer mqttClient.Close()

	waitForShutdownFunc(mqttClient, batchCollector, entropyServer, metricsServer)

	return 0
}

// loadConfig loads the gateway configuration from environment variables and
// the optional .env file.
func loadConfig() (tdcconfig.Config, error) {
	config, err := tdcconfigLoadFunc()
	if err != nil {
		return config, fmt.Errorf("config: %w", err)
	}

	log.Printf("environment: %s", config.Environment)
	return config, nil
}

// setupBatchCollector creates a BatchCollector that feeds entropy to the local
// whitened pool and, when a cloudAwareForwarder is provided, mirrors batches
// to the cloud gRPC stream.
func setupBatchCollector(pool *entropy.WhitenedPool, config tdcconfig.Config, forwarder *cloudAwareForwarder) *collector.BatchCollector {
	sender := collector.BatchSender(batchSenderFunc(func(events []tdcmqtt.TDCEvent, sequence uint32) error {
		startTime := time.Now()
		pool.AddBatch(events)
		duration := time.Since(startTime).Seconds()
		metrics.RecordBatch(len(events), duration, true, "")
		return nil
	}))

	mode := "local-only"
	if forwarder != nil {
		sender = forwarder
		mode = "cloud-capable"
	}

	batchSize := config.Collector.BatchSize
	flushInterval := time.Duration(float64(batchSize)/184.0) * time.Second

	batchCollector := collector.NewBatchCollectorWithFlush(batchSize, flushInterval, sender)

	metrics.SetCollectorBatchSize(batchSize)

	log.Printf("batch collector: initialized (batch_size=%d, flush_interval=%.1fs, mode=%s)",
		batchSize, flushInterval.Seconds(), mode)

	return batchCollector
}

// setupMQTT creates and connects the MQTT client, wiring the RxHandler to the
// given BatchCollector.
func setupMQTT(config tdcconfig.Config, batchCollector *collector.BatchCollector) (mqttClient, error) {
	handler := &tdcmqtt.RxHandler{
		Collector: batchCollector,
	}

	client, err := newMQTTClient(tdcmqtt.Config{
		BrokerURL: config.MQTT.BrokerURL,
		ClientID:  config.MQTT.ClientID,
		Topics:    config.MQTT.Topics,
		QoS:       config.MQTT.QoS,
		Username:  config.MQTT.Username,
		Password:  config.MQTT.Password,
		TLSCAFile: config.MQTT.TLSCAFile,
	}, handler)
	if err != nil {
		return nil, fmt.Errorf("mqtt init: %w", err)
	}

	if err := client.Connect(); err != nil {
		client.Close()
		return nil, fmt.Errorf("mqtt connect: %w", err)
	}

	log.Printf("mqtt: connected -> %s, subscribed -> %v (QoS=%d)",
		config.MQTT.BrokerURL, config.MQTT.Topics, config.MQTT.QoS)
	log.Println("entropy-tdc-gateway: ready, processing events...")

	return client, nil
}

// connectMQTTWithRetry repeatedly invokes setupMQTTFunc until a connection is
// established. It applies exponential back-off with bounded jitter so multiple
// instances do not retry in lockstep during broker outages.
func connectMQTTWithRetry(config tdcconfig.Config, batchCollector *collector.BatchCollector) (mqttClient, error) {
	const (
		initialDelay   = 1 * time.Second
		maxDelay       = 30 * time.Second
		jitterFraction = 0.2
	)

	delay := initialDelay
	attempt := 0
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		attempt++
		client, err := setupMQTTFunc(config, batchCollector)
		if err == nil {
			if attempt > 1 {
				log.Printf("mqtt: connected after %d attempt(s)", attempt)
			}
			return client, nil
		}

		wait := delay
		if jitterFraction > 0 {
			jitter := 1 + (rng.Float64()*2-1)*jitterFraction
			wait = time.Duration(float64(delay) * jitter)
			if wait < 0 {
				wait = 0
			}
		}

		log.Printf("mqtt: connect attempt %d failed: %v (retrying in %s)", attempt, err, wait)
		time.Sleep(wait)

		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

// validateEntropyAddr ensures the entropy HTTP address resolves to a loopback
// interface unless allowPublic is true.
func validateEntropyAddr(addr string, allowPublic bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("entropy http addr: invalid address %q: %w", addr, err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		if host == "localhost" {
			return nil
		}
		if allowPublic {
			return nil
		}
		return fmt.Errorf("entropy http addr: must bind to loopback address (127.0.0.1, ::1, or localhost), got %q", host)
	}

	if !ip.IsLoopback() {
		if allowPublic {
			return nil
		}
		return fmt.Errorf("entropy http addr: must bind to loopback address, got %q", host)
	}

	return nil
}

// convertToProtoBatch converts a slice of TDCEvent values into a protobuf
// EntropyBatch with the given sequence number.
func convertToProtoBatch(events []tdcmqtt.TDCEvent, sequence uint32) *pb.EntropyBatch {
	pbEvents := make([]*pb.TDCEvent, len(events))
	for i, evt := range events {
		pbEvents[i] = toProtoEvent(evt)
	}

	return &pb.EntropyBatch{
		Events:        pbEvents,
		SourceId:      "entropy-tdc-gateway",
		BatchSequence: sequence,
	}
}

// toProtoEvent converts one MQTT event into the protobuf event representation.
// Canonical whitening input is exactly tdc_timestamp_ps encoded as 8-byte
// little-endian; this byte encoding is stable and independent of gateway
// ingestion clock semantics.
func toProtoEvent(evt tdcmqtt.TDCEvent) *pb.TDCEvent {
	whitened := whitenEvent(evt)
	if len(whitened) != sha256.Size {
		metrics.RecordEventDropped("invalid_whitened_entropy")
		log.Printf(
			"grpc: invalid per-event whitening output size=%d (expected=%d) tdc_timestamp_ps=%d",
			len(whitened),
			sha256.Size,
			evt.TdcTimestampPs,
		)
		whitened = nil
	}

	return &pb.TDCEvent{
		RpiTimestampUs:  evt.RpiTimestampUs,
		TdcTimestampPs:  evt.TdcTimestampPs,
		Channel:         evt.Channel,
		WhitenedEntropy: whitened,
	}
}

// whitenEvent derives canonical whitening input from the TDC picosecond
// timestamp only. This keeps per-event whitening deterministic and independent
// of gateway ingestion-time metadata.
func whitenEvent(evt tdcmqtt.TDCEvent) []byte {
	canonical := make([]byte, 8)
	binary.LittleEndian.PutUint64(canonical, evt.TdcTimestampPs)
	return entropy.DualStageWhitening(canonical)
}

// startGRPCConnector launches a background goroutine that retries gRPC
// connection establishment with exponential back-off until it succeeds or
// the context is cancelled.
func startGRPCConnector(ctx context.Context, cfg tdcconfig.CloudForwarder, forwarder *cloudAwareForwarder) {
	if forwarder == nil {
		return
	}

	delay := time.Duration(cfg.ReconnectDelay) * time.Second
	if delay <= 0 {
		delay = 5 * time.Second
	}
	maxDelay := time.Duration(cfg.MaxReconnectSec) * time.Second
	if maxDelay <= 0 {
		maxDelay = 60 * time.Second
	}

	go func() {
		attempt := 0
		for {
			attempt++
			client, err := tdcgrpc.NewClient(cfg)
			if err == nil {
				forwarder.SetClient(client)
				log.Printf("grpc: cloud forwarder connected after %d attempt(s)", attempt)
				return
			}

			log.Printf("grpc: connect attempt %d failed: %v (retrying in %s)", attempt, err, delay)

			select {
			case <-ctx.Done():
				log.Printf("grpc: stopping connector (context cancelled)")
				return
			case <-time.After(delay):
			}

			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}()
}

// waitForShutdown blocks until SIGINT or SIGTERM is received, then tears down
// subsystems in order: MQTT, collector, entropy HTTP server, metrics server.
func waitForShutdown(mqttClient mqttClient, collector *collector.BatchCollector, entropyHTTPServer entropyServer, metricsHTTPServer metricsServer) {
	sig := make(chan os.Signal, 1)
	signalNotifyFunc(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down gracefully...")

	if mqttClient != nil {
		mqttClient.Close()
	}

	if collector != nil {
		collector.Close()
	}

	if entropyHTTPServer != nil {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := entropyHTTPServer.Shutdown(shutdownContext); err != nil {
			log.Printf("entropy http server: shutdown error: %v", err)
		}
	}

	if metricsHTTPServer != nil {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := metricsHTTPServer.Shutdown(shutdownContext); err != nil {
			log.Printf("metrics http server: shutdown error: %v", err)
		}
	}

	log.Println("shutdown complete")
}
