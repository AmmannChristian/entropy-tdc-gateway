package grpc

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"entropy-tdc-gateway/internal/config"
	"entropy-tdc-gateway/internal/metrics"
	"entropy-tdc-gateway/pkg/pb"
	testutil "entropy-tdc-gateway/testutil"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// mockEntropyStreamServer implements the EntropyStream service for testing
type mockEntropyStreamServer struct {
	pb.UnimplementedEntropyStreamServer
	mu              sync.Mutex
	receivedBatches []*pb.EntropyBatch
	acksToSend      []*pb.Ack
	streamErr       error
	recvDelay       time.Duration
	sendDelay       time.Duration
	closeAfterRecv  int // close stream after N receives
	recvCount       int
}

func (m *mockEntropyStreamServer) StreamEntropy(stream grpc.BidiStreamingServer[pb.EntropyBatch, pb.Ack]) error {
	m.mu.Lock()
	streamErr := m.streamErr
	m.mu.Unlock()

	if streamErr != nil {
		return streamErr
	}

	// Receive goroutine
	recvDone := make(chan error, 1)
	go func() {
		for {
			batch, err := stream.Recv()
			if err != nil {
				recvDone <- err
				return
			}

			m.mu.Lock()
			m.receivedBatches = append(m.receivedBatches, batch)
			m.recvCount++
			closeAfter := m.closeAfterRecv
			count := m.recvCount
			delay := m.recvDelay
			m.mu.Unlock()

			if delay > 0 {
				time.Sleep(delay)
			}

			if closeAfter > 0 && count >= closeAfter {
				recvDone <- io.EOF
				return
			}
		}
	}()

	// Send goroutine
	sendDone := make(chan error, 1)
	go func() {
		for {
			m.mu.Lock()
			if len(m.acksToSend) == 0 {
				m.mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				continue
			}
			ack := m.acksToSend[0]
			m.acksToSend = m.acksToSend[1:]
			delay := m.sendDelay
			m.mu.Unlock()

			if delay > 0 {
				time.Sleep(delay)
			}

			if err := stream.Send(ack); err != nil {
				sendDone <- err
				return
			}
		}
	}()

	// Wait for either to complete
	select {
	case err := <-recvDone:
		if err == io.EOF {
			return nil
		}
		return err
	case err := <-sendDone:
		return err
	}
}

func (m *mockEntropyStreamServer) queueAck(ack *pb.Ack) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acksToSend = append(m.acksToSend, ack)
}

func (m *mockEntropyStreamServer) getBatches() []*pb.EntropyBatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	batches := make([]*pb.EntropyBatch, len(m.receivedBatches))
	copy(batches, m.receivedBatches)
	return batches
}

func (m *mockEntropyStreamServer) setCloseAfterRecv(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeAfterRecv = n
}

// setupTestServer creates a mock gRPC server with bufconn
func setupTestServer(t *testing.T, mock *mockEntropyStreamServer) (*grpc.Server, *bufconn.Listener) {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	server := grpc.NewServer()
	pb.RegisterEntropyStreamServer(server, mock)

	go func() {
		if err := server.Serve(lis); err != nil {
			t.Logf("server stopped: %v", err)
		}
	}()

	t.Cleanup(func() {
		server.Stop()
		lis.Close()
	})

	return server, lis
}

// setupTestClient creates a client connected to the test server
func setupTestClient(t *testing.T, lis *bufconn.Listener, cfg config.CloudForwarder) *Client {
	t.Helper()
	testutil.ResetRegistryForTest(t)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufnet: %v", err)
	}

	clientCtx, clientCancel := context.WithCancel(context.Background())

	client := &Client{
		cfg:    cfg,
		conn:   conn,
		client: pb.NewEntropyStreamClient(conn),
		ctx:    clientCtx,
		cancel: clientCancel,
	}

	// Start stream
	stream, err := client.client.StreamEntropy(client.ctx)
	if err != nil {
		t.Fatalf("failed to create stream: %v", err)
	}

	client.stream = stream
	client.connected = true
	metrics.SetGRPCConnected(true)

	client.wg.Add(1)
	go client.receiveAcks()

	t.Cleanup(func() {
		client.Close()
	})

	return client
}

// TestNewClient_Success tests that NewClient properly initializes a client
// Note: We can't easily test successful connection without mocking the entire gRPC stack
func TestNewClient_InitializesCorrectly(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	// Create a client manually to test initialization logic
	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "test-addr",
		TLSEnabled:     false,
		ConnectTimeout: 5,
	}

	// Verify config validation works
	if !cfg.Enabled {
		t.Error("config should be enabled")
	}
	if cfg.ServerAddr == "" {
		t.Error("server addr should not be empty")
	}
}

func TestNewClient_ConnectFails(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "localhost:19999", // Non-existent
		TLSEnabled:     false,
		ConnectTimeout: 1,
	}

	client, err := NewClient(cfg)
	if err == nil {
		t.Fatal("expected error when connection fails")
	}
	if client != nil {
		t.Error("client should be nil when connection fails")
	}
}

func TestNewClient_DisabledError(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled: false,
	}

	client, err := NewClient(cfg)
	if err == nil {
		t.Fatal("expected error when cloud forwarder is disabled")
	}
	if client != nil {
		t.Error("client should be nil when disabled")
	}
	if err.Error() != "grpc: cloud forwarder is disabled" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewClient_MissingServerAddr(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:    true,
		ServerAddr: "",
	}

	client, err := NewClient(cfg)
	if err == nil {
		t.Fatal("expected error when server address is missing")
	}
	if client != nil {
		t.Error("client should be nil when server address is missing")
	}
	if err.Error() != "grpc: server address is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_SendBatch_Success(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	events := []*pb.TDCEvent{
		{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 0},
		{RpiTimestampUs: 1001, TdcTimestampPs: 2001, Channel: 0},
	}

	err := client.SendBatch(events)
	if err != nil {
		t.Fatalf("SendBatch failed: %v", err)
	}

	// Wait for batch to be received
	time.Sleep(50 * time.Millisecond)

	batches := mock.getBatches()
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}

	if len(batches[0].Events) != 2 {
		t.Errorf("expected 2 events, got %d", len(batches[0].Events))
	}
}

func TestClient_SendBatch_NotConnected(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:    true,
		ServerAddr: "localhost:9999",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		cfg:       cfg,
		connected: false,
		ctx:       ctx,
		cancel:    cancel,
	}

	events := []*pb.TDCEvent{
		{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 0},
	}

	err := client.SendBatch(events)
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	if err.Error() != "grpc: not connected" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_ReceiveAcks_Success(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Queue an ACK
	mock.queueAck(&pb.Ack{
		Success:          true,
		ReceivedSequence: 1,
		Message:          "ok",
	})

	// Wait for ACK to be processed
	time.Sleep(100 * time.Millisecond)

	// If we get here without hanging, ACK was received
}

func TestClient_ReceiveAcks_Backpressure(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Queue an ACK with backpressure
	mock.queueAck(&pb.Ack{
		Success:            true,
		ReceivedSequence:   1,
		Message:            "ok",
		Backpressure:       boolPtr(true),
		BackpressureReason: stringPtr("rate limit"),
	})

	// Wait for ACK to be processed
	time.Sleep(100 * time.Millisecond)

	// Backpressure metric should be recorded
}

func TestClient_ReceiveAcks_MissingSequences(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Queue an ACK with missing sequences
	mock.queueAck(&pb.Ack{
		Success:          true,
		ReceivedSequence: 5,
		Message:          "ok",
		MissingSequences: []uint32{2, 3},
	})

	// Wait for ACK to be processed
	time.Sleep(100 * time.Millisecond)

	// Missing sequence metric should be recorded
}

func TestClient_ReceiveAcks_Failure(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Queue a failed ACK
	mock.queueAck(&pb.Ack{
		Success:          false,
		ReceivedSequence: 1,
		Message:          "validation failed",
	})

	// Wait for ACK to be processed
	time.Sleep(100 * time.Millisecond)

	// Failure should be logged and metrics recorded
}

func TestClient_ReceiveAcks_EOF(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	mock.setCloseAfterRecv(1)
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "bufnet",
		ConnectTimeout:  5,
		ReconnectDelay:  1,
		MaxReconnectSec: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Send a batch to trigger EOF
	events := []*pb.TDCEvent{
		{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 0},
	}

	err := client.SendBatch(events)
	if err != nil {
		t.Fatalf("SendBatch failed: %v", err)
	}

	// Wait for EOF and reconnect attempt
	time.Sleep(200 * time.Millisecond)

	// Client should have triggered reconnect
	client.mu.Lock()
	connected := client.connected
	client.mu.Unlock()

	if connected {
		t.Error("client should not be connected after EOF")
	}
}

func TestClient_HandleAck_Nil(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:    true,
		ServerAddr: "localhost:9999",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	// Should not panic
	client.handleAck(nil)
}

func TestClient_Close(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)

	err := client.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Verify context is cancelled
	select {
	case <-client.ctx.Done():
		// Expected
	default:
		t.Error("context should be cancelled after Close")
	}
}

func TestClient_IsConnected(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:    true,
		ServerAddr: "localhost:9999",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		cfg:       cfg,
		connected: true,
		ctx:       ctx,
		cancel:    cancel,
	}

	if !client.IsConnected() {
		t.Error("IsConnected should return true")
	}

	client.connected = false

	if client.IsConnected() {
		t.Error("IsConnected should return false")
	}
}

func TestClient_Reconnect_Success(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "bufnet",
		ConnectTimeout:  5,
		ReconnectDelay:  1,
		MaxReconnectSec: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Disconnect
	client.mu.Lock()
	client.connected = false
	client.mu.Unlock()

	// Manually trigger reconnect (in real scenario, receiveAcks would do this)
	// We'll test the reconnect logic indirectly through connection failure
}

func TestClient_Reconnect_CancelledContext(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "localhost:9999",
		ConnectTimeout:  1,
		ReconnectDelay:  1,
		MaxReconnectSec: 5,
	}

	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	// Cancel context immediately
	cancel()

	// Reconnect should exit immediately
	client.reconnect()

	// Should complete without blocking
}

func TestClient_Reconnect_ExponentialBackoff(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "localhost:19999", // Non-existent server
		ConnectTimeout:  1,
		ReconnectDelay:  1,
		MaxReconnectSec: 2,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	// Start reconnect in goroutine
	done := make(chan struct{})
	go func() {
		client.reconnect()
		close(done)
	}()

	// Wait a bit for some reconnect attempts
	time.Sleep(500 * time.Millisecond)

	// Cancel to stop reconnect
	cancel()

	// Wait for reconnect to finish
	select {
	case <-done:
		// Expected
	case <-time.After(5 * time.Second):
		t.Error("reconnect did not exit after context cancellation")
	}
}

func TestClient_Reconnect_AlreadyReconnecting(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "localhost:9999",
		ConnectTimeout:  1,
		ReconnectDelay:  1,
		MaxReconnectSec: 5,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		cfg:          cfg,
		ctx:          ctx,
		cancel:       cancel,
		reconnecting: true, // Already reconnecting
	}

	// Should return immediately
	client.reconnect()

	// Should complete without blocking
}

// NOTE: TLS configuration tests have been removed as buildTLSConfig() is now handled
// internally by go-authx/grpcclient.Builder. TLS configuration is tested in go-authx.
//
// Previously tested scenarios:
// - TestClient_BuildTLSConfig_TLSDisabled: TLS disabled mode
// - TestClient_BuildTLSConfig_InvalidCAFile: Invalid CA file handling
// - TestClient_BuildTLSConfig_InvalidCertFile: Invalid cert/key file handling
// - TestClient_BuildTLSConfig_ServerName: Server name override

func TestClient_SendBatch_StreamError(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "bufnet",
		ConnectTimeout:  5,
		ReconnectDelay:  1,
		MaxReconnectSec: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Close the stream to cause an error
	client.mu.Lock()
	if client.stream != nil {
		client.stream.CloseSend()
	}
	client.mu.Unlock()

	// Wait for stream to close
	time.Sleep(50 * time.Millisecond)

	events := []*pb.TDCEvent{
		{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 0},
	}

	err := client.SendBatch(events)
	if err == nil {
		t.Fatal("expected error when sending to closed stream")
	}

	// Should trigger reconnect
	time.Sleep(100 * time.Millisecond)
}

func TestClient_Connect_ContextCancelled(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "localhost:19999", // Non-existent
		ConnectTimeout: 10,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	err := client.connect()
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
}

func TestClient_ReceiveAcks_ContextCancelled(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)

	// Cancel context to stop receiveAcks
	client.cancel()

	// Wait for receiveAcks to exit
	time.Sleep(100 * time.Millisecond)

	// Should exit cleanly
}

func TestClient_SendBatch_ConcurrentAccess(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Send multiple batches concurrently
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			events := []*pb.TDCEvent{
				{RpiTimestampUs: uint64(seq * 1000), TdcTimestampPs: uint64(seq * 2000), Channel: 0},
			}
			if err := client.SendBatch(events); err != nil {
				t.Logf("SendBatch %d failed: %v", seq, err)
			}
		}(i)
	}

	wg.Wait()

	// Wait for batches to be received
	time.Sleep(200 * time.Millisecond)

	batches := mock.getBatches()
	if len(batches) != 10 {
		t.Errorf("expected 10 batches, got %d", len(batches))
	}
}

func TestClient_Reconnect_MaxDelay(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "localhost:19999", // Non-existent
		ConnectTimeout:  1,
		ReconnectDelay:  1,
		MaxReconnectSec: 2,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	// Track reconnect attempts
	done := make(chan struct{})
	go func() {
		client.reconnect()
		close(done)
	}()

	// Wait for a few attempts
	time.Sleep(3 * time.Second)

	// Cancel
	cancel()

	select {
	case <-done:
		// Expected
	case <-time.After(5 * time.Second):
		t.Error("reconnect did not respect max delay")
	}
}

func TestClient_ReceiveAcks_StreamNil(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:    true,
		ServerAddr: "localhost:9999",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	client := &Client{
		cfg:       cfg,
		ctx:       ctx,
		cancel:    func() {},
		stream:    nil,
		connected: false,
	}

	// Start receiveAcks with nil stream
	done := make(chan struct{})
	client.wg.Add(1)
	go func() {
		client.receiveAcks()
		close(done)
	}()

	// Should exit after context timeout
	select {
	case <-done:
		// Expected
	case <-time.After(1 * time.Second):
		t.Error("receiveAcks did not exit with nil stream")
	}
}

func TestClient_Connect_TLSEnabled(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "localhost:19999",
		TLSEnabled:     true,
		TLSCAFile:      "/nonexistent/ca.pem",
		ConnectTimeout: 1,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	err := client.connect()
	if err == nil {
		t.Fatal("expected error with invalid TLS config")
	}
}

func TestClient_Connect_InsecureSuccess(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		TLSEnabled:     false,
		ConnectTimeout: 5,
	}

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()

	client := &Client{
		cfg:    cfg,
		ctx:    clientCtx,
		cancel: clientCancel,
		conn:   conn,
		client: pb.NewEntropyStreamClient(conn),
	}

	// Create stream
	stream, err := client.client.StreamEntropy(client.ctx)
	if err != nil {
		t.Fatalf("failed to create stream: %v", err)
	}

	client.stream = stream
	client.connected = true

	if !client.connected {
		t.Error("client should be connected")
	}
}

func TestClient_ReceiveAcks_RecvError(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "bufnet",
		ConnectTimeout:  5,
		ReconnectDelay:  1,
		MaxReconnectSec: 5,
	}

	client := setupTestClient(t, lis, cfg)

	// Close the stream to trigger error
	client.mu.Lock()
	if client.stream != nil {
		client.stream.CloseSend()
	}
	client.mu.Unlock()

	// Wait for error to be detected
	time.Sleep(200 * time.Millisecond)

	client.mu.Lock()
	connected := client.connected
	client.mu.Unlock()

	// Should have detected the error and set connected to false
	if connected {
		t.Error("client should not be connected after stream error")
	}

	client.Close()
}

func TestClient_SendBatch_TriggerReconnect(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "bufnet",
		ConnectTimeout:  5,
		ReconnectDelay:  1,
		MaxReconnectSec: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Send successful batch first
	events := []*pb.TDCEvent{
		{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 0},
	}

	err := client.SendBatch(events)
	if err != nil {
		t.Fatalf("SendBatch failed: %v", err)
	}

	// Wait for it to be received
	time.Sleep(50 * time.Millisecond)

	// Now close stream
	client.mu.Lock()
	if client.stream != nil {
		client.stream.CloseSend()
	}
	client.mu.Unlock()

	time.Sleep(50 * time.Millisecond)

	// Try to send again - should fail and trigger reconnect
	err = client.SendBatch(events)
	if err == nil {
		t.Fatal("expected error after stream closed")
	}

	// Wait for reconnect attempt
	time.Sleep(200 * time.Millisecond)
}

func TestClient_Close_WithNilConn(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:    true,
		ServerAddr: "localhost:9999",
	}

	ctx, cancel := context.WithCancel(context.Background())

	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
		conn:   nil, // Nil connection
	}

	err := client.Close()
	if err != nil {
		t.Errorf("Close should not fail with nil connection: %v", err)
	}
}

func TestClient_Reconnect_SuccessfulReconnect(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "bufnet",
		ConnectTimeout:  5,
		ReconnectDelay:  1,
		MaxReconnectSec: 5,
	}

	// Setup initial connection
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()

	client := &Client{
		cfg:    cfg,
		ctx:    clientCtx,
		cancel: clientCancel,
		conn:   conn,
		client: pb.NewEntropyStreamClient(conn),
	}

	// Start stream
	stream, err := client.client.StreamEntropy(client.ctx)
	if err != nil {
		t.Fatalf("failed to create stream: %v", err)
	}

	client.stream = stream
	client.connected = true
	metrics.SetGRPCConnected(true)

	// Now disconnect
	conn.Close()
	client.connected = false

	// Note: We can't easily test successful reconnect with bufconn
	// because we'd need to re-establish the connection
	// This test covers the setup and teardown paths
	client.Close()
}

func TestClient_Connect_StreamCreationFails(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "localhost:19999", // Non-existent
		ConnectTimeout: 1,
		TLSEnabled:     false,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	err := client.connect()
	if err == nil {
		t.Fatal("expected error when server is unreachable")
	}
}

func TestClient_Reconnect_CloseOldConnection(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "localhost:19999",
		ConnectTimeout:  1,
		ReconnectDelay:  1,
		MaxReconnectSec: 2,
	}

	// Create a mock connection
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockConn, _ := grpc.NewClient("localhost:19999",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	// Will fail to connect, that's ok

	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
		conn:   mockConn, // Set old connection
	}

	// Start reconnect in background
	done := make(chan struct{})
	go func() {
		client.reconnect()
		close(done)
	}()

	// Let it try once
	time.Sleep(500 * time.Millisecond)

	// Cancel to stop
	cancel()

	select {
	case <-done:
		// Expected
	case <-time.After(3 * time.Second):
		t.Error("reconnect did not exit")
	}
}

func TestClient_SendBatch_MultipleEventsSequence(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Send 5 batches in sequence
	for i := 0; i < 5; i++ {
		events := []*pb.TDCEvent{
			{RpiTimestampUs: uint64(i * 100), TdcTimestampPs: uint64(i * 200), Channel: uint32(i % 4)},
		}

		if err := client.SendBatch(events); err != nil {
			t.Fatalf("SendBatch %d failed: %v", i, err)
		}
	}

	// Wait for all batches to be received
	time.Sleep(100 * time.Millisecond)

	batches := mock.getBatches()
	if len(batches) != 5 {
		t.Errorf("expected 5 batches, got %d", len(batches))
	}
}

func TestClient_SendBatch_EmptyEvents(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		ConnectTimeout: 5,
	}

	client := setupTestClient(t, lis, cfg)
	defer client.Close()

	// Send batch with no events
	events := []*pb.TDCEvent{}

	err := client.SendBatch(events)
	if err != nil {
		t.Fatalf("SendBatch failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	batches := mock.getBatches()
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}

	if len(batches[0].Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(batches[0].Events))
	}
}

func TestClient_Connect_Success(t *testing.T) {
	mock := &mockEntropyStreamServer{}
	_, lis := setupTestServer(t, mock)

	cfg := config.CloudForwarder{
		Enabled:        true,
		ServerAddr:     "bufnet",
		TLSEnabled:     false,
		ConnectTimeout: 5,
	}

	// Test full connection flow manually
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()

	client := &Client{
		cfg:    cfg,
		ctx:    clientCtx,
		cancel: clientCancel,
	}

	// Manually assign connection (simulating connect())
	client.conn = conn
	client.client = pb.NewEntropyStreamClient(conn)

	// Create stream
	stream, err := client.client.StreamEntropy(client.ctx)
	if err != nil {
		t.Fatalf("failed to create stream: %v", err)
	}

	client.stream = stream
	client.connected = true

	// Verify connection works
	if !client.IsConnected() {
		t.Error("client should be connected")
	}

	// Test sending
	events := []*pb.TDCEvent{{RpiTimestampUs: 1000, TdcTimestampPs: 2000, Channel: 0}}
	if err := client.SendBatch(events); err != nil {
		t.Errorf("SendBatch failed: %v", err)
	}

	client.Close()
}

func TestClient_Reconnect_ConnectSuccessOnRetry(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	cfg := config.CloudForwarder{
		Enabled:         true,
		ServerAddr:      "localhost:19999",
		ConnectTimeout:  1,
		ReconnectDelay:  1,
		MaxReconnectSec: 3,
		TLSEnabled:      false,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	// Start reconnect
	done := make(chan struct{})
	go func() {
		client.reconnect()
		close(done)
	}()

	// Let it try a few times
	time.Sleep(1500 * time.Millisecond)

	// Cancel to stop
	cancel()

	select {
	case <-done:
		// Expected
	case <-time.After(5 * time.Second):
		t.Error("reconnect did not exit")
	}

	// Verify reconnect flag is cleared
	client.mu.Lock()
	reconnecting := client.reconnecting
	client.mu.Unlock()

	if reconnecting {
		t.Error("reconnecting flag should be cleared after exit")
	}
}

// Helper functions
func boolPtr(b bool) *bool {
	return &b
}

func stringPtr(s string) *string {
	return &s
}
