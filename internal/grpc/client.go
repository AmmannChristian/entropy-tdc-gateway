// Package grpc implements a bidirectional streaming client for forwarding
// entropy batches to the cloud processor service. It handles connection
// lifecycle, OAuth2 authentication, TLS configuration, acknowledgement
// processing, and automatic reconnection with exponential back-off.
package grpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"entropy-tdc-gateway/internal/config"
	"entropy-tdc-gateway/internal/metrics"
	pb "entropy-tdc-gateway/pkg/pb"

	"github.com/AmmannChristian/go-authx/grpcclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client maintains a bidirectional gRPC stream to the cloud entropy processor
// service. It sends EntropyBatch messages and receives Ack replies
// asynchronously. The stream is automatically rotated after the configured
// interval and re-established on transient failures with exponential
// back-off. All methods are safe for concurrent use.
type Client struct {
	cfg          config.CloudForwarder
	conn         *grpc.ClientConn
	client       pb.EntropyStreamClient
	stream       grpc.BidiStreamingClient[pb.EntropyBatch, pb.Ack]
	mu           sync.Mutex
	connected    bool
	ctx          context.Context
	cancel       context.CancelFunc
	rotateMu     sync.Mutex
	rotateCancel context.CancelFunc
	reconnectMu  sync.Mutex
	reconnecting bool
	wg           sync.WaitGroup
}

// NewClient creates a Client from the given configuration and establishes the
// initial connection and stream. It returns an error when the forwarder is
// disabled, the server address is missing, or the initial connection fails.
func NewClient(cfg config.CloudForwarder) (*Client, error) {
	if !cfg.Enabled {
		return nil, errors.New("grpc: cloud forwarder is disabled")
	}

	if cfg.ServerAddr == "" {
		return nil, errors.New("grpc: server address is required")
	}

	ctx, cancel := context.WithCancel(context.Background())

	c := &Client{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}

	if err := c.connect(); err != nil {
		cancel()
		return nil, fmt.Errorf("grpc: initial connection failed: %w", err)
	}

	return c, nil
}

// connect dials the server, opens a bidirectional StreamEntropy stream, starts
// an acknowledgement receiver goroutine, and schedules stream rotation.
func (c *Client) connect() error {
	builder := grpcclient.NewBuilder().WithAddress(c.cfg.ServerAddr)

	if c.cfg.OAuth2Enabled {
		builder = builder.WithOAuth2(
			c.cfg.OAuth2TokenURL,
			c.cfg.OAuth2ClientID,
			c.cfg.OAuth2ClientSecret,
			c.cfg.OAuth2Scopes,
		)
		log.Printf("grpc: OAuth2 authentication enabled (token URL: %s)", c.cfg.OAuth2TokenURL)
	}

	if c.cfg.TLSEnabled {
		builder = builder.WithTLS(
			c.cfg.TLSCAFile,
			c.cfg.TLSCertFile,
			c.cfg.TLSKeyFile,
			c.cfg.TLSServerName,
		)
		log.Printf("grpc: TLS enabled (CA: %s, mTLS: %v)", c.cfg.TLSCAFile, c.cfg.TLSCertFile != "")
	} else {
		builder = builder.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()))
		log.Printf("grpc: WARNING - using insecure connection (no TLS)")
	}

	conn, err := builder.Build(c.ctx)
	if err != nil {
		return fmt.Errorf("grpc: build client failed: %w", err)
	}

	c.conn = conn
	c.client = pb.NewEntropyStreamClient(conn)

	stream, err := c.client.StreamEntropy(c.ctx)
	if err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			log.Printf("grpc: failed to close connection after stream creation error: %v", closeErr)
		}
		return fmt.Errorf("grpc: stream creation failed: %w", err)
	}

	c.stream = stream
	c.connected = true
	metrics.SetGRPCConnected(true)

	c.wg.Add(1)
	go c.receiveAcks()

	c.resetRotationTimer()

	log.Printf("grpc: connected to %s", c.cfg.ServerAddr)
	return nil
}

// SendBatch transmits an EntropyBatch containing the given events over the
// active stream. If the send fails the client marks itself disconnected and
// triggers an asynchronous reconnection attempt.
func (c *Client) SendBatch(events []*pb.TDCEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return errors.New("grpc: not connected")
	}

	batch := &pb.EntropyBatch{
		Events: events,
	}

	if err := c.stream.Send(batch); err != nil {
		log.Printf("grpc: send failed: %v", err)
		c.connected = false
		metrics.SetGRPCConnected(false)
		metrics.RecordGRPCError("send_failed")

		go c.reconnect()
		return fmt.Errorf("grpc: send batch failed: %w", err)
	}

	return nil
}

// receiveAcks reads Ack messages from the stream until the context is
// cancelled or a receive error occurs. On error it marks the client
// disconnected and triggers reconnection.
func (c *Client) receiveAcks() {
	defer c.wg.Done()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		c.mu.Lock()
		stream := c.stream
		c.mu.Unlock()

		if stream == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		ack, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Printf("grpc: stream closed by server")
			} else {
				log.Printf("grpc: receive error: %v", err)
				metrics.RecordGRPCError("recv_failed")
			}

			c.mu.Lock()
			c.connected = false
			metrics.SetGRPCConnected(false)
			c.mu.Unlock()

			go c.reconnect()
			return
		}

		c.handleAck(ack)
	}
}

// handleAck processes a single Ack, recording metrics for success, back-pressure
// signals, missing sequences, and rejected batches.
func (c *Client) handleAck(ack *pb.Ack) {
	if ack == nil {
		return
	}

	metrics.RecordCloudAck(ack.Success, ack.Message)

	if ack.GetBackpressure() {
		metrics.RecordBackpressure()
		log.Printf("grpc: backpressure signaled by server (reason: %s)", ack.GetBackpressureReason())
	}

	if len(ack.MissingSequences) > 0 {
		metrics.RecordMissingSequence(len(ack.MissingSequences))
		log.Printf("grpc: server reports %d missing sequences", len(ack.MissingSequences))
	}

	if !ack.Success {
		log.Printf("grpc: batch rejected by server: %s", ack.Message)
	}
}

// reconnect re-establishes the gRPC connection using exponential back-off
// derived from the configured ReconnectDelay and MaxReconnectSec. Only one
// reconnection loop runs at a time.
func (c *Client) reconnect() {
	c.reconnectMu.Lock()
	if c.reconnecting {
		c.reconnectMu.Unlock()
		return
	}
	c.reconnecting = true
	c.reconnectMu.Unlock()

	defer func() {
		c.reconnectMu.Lock()
		c.reconnecting = false
		c.reconnectMu.Unlock()
	}()

	delay := time.Duration(c.cfg.ReconnectDelay) * time.Second
	maxDelay := time.Duration(c.cfg.MaxReconnectSec) * time.Second

	attempt := 0
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		attempt++
		log.Printf("grpc: reconnecting (attempt %d) after %v...", attempt, delay)

		select {
		case <-time.After(delay):
		case <-c.ctx.Done():
			return
		}

		c.mu.Lock()
		if c.conn != nil {
			if closeErr := c.conn.Close(); closeErr != nil {
				log.Printf("grpc: failed to close old connection during reconnect: %v", closeErr)
			}
		}
		c.mu.Unlock()

		if err := c.connect(); err != nil {
			log.Printf("grpc: reconnect failed: %v", err)
			metrics.RecordGRPCReconnect()

			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
			continue
		}

		log.Printf("grpc: reconnected successfully")
		metrics.RecordGRPCReconnect()
		return
	}
}

// Close cancels the client context, waits for background goroutines to exit,
// and closes the underlying gRPC connection.
func (c *Client) Close() error {
	c.stopRotationTimer()
	c.cancel()
	c.wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()

	metrics.SetGRPCConnected(false)

	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			return fmt.Errorf("grpc: close connection: %w", err)
		}
	}

	log.Printf("grpc: client closed")
	return nil
}

// resetRotationTimer cancels any pending rotation and, when the configured
// StreamRotateInterval is positive, schedules a new stream rotation.
func (c *Client) resetRotationTimer() {
	c.stopRotationTimer()

	interval := c.cfg.StreamRotateInterval
	if interval <= 0 {
		return
	}

	ctx, cancel := context.WithCancel(c.ctx)
	c.rotateMu.Lock()
	c.rotateCancel = cancel
	c.rotateMu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()

		timer := time.NewTimer(interval)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			log.Printf("grpc: rotating stream after %s", interval)
			go c.reconnect()
		}
	}()
}

// stopRotationTimer cancels the active stream rotation timer, if any.
func (c *Client) stopRotationTimer() {
	c.rotateMu.Lock()
	if c.rotateCancel != nil {
		c.rotateCancel()
		c.rotateCancel = nil
	}
	c.rotateMu.Unlock()
}

// IsConnected reports whether the client currently holds an active stream.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}
