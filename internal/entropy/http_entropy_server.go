package entropy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"entropy-tdc-gateway/internal/clock"
	"entropy-tdc-gateway/internal/metrics"

	"github.com/AmmannChristian/go-authx/httpserver"
)

const (
	defaultHTTPAddress       = "127.0.0.1:9797"
	defaultRequestBytes      = 32
	minRequestBytes          = 1
	maxRequestBytes          = 4096
	defaultShutdownTimeout   = 5 * time.Second
	defaultIdleTimeout       = 30 * time.Second
	defaultReadWriteTimeout  = 5 * time.Second
	defaultRateLimitRPS      = 25
	defaultRateLimitBurst    = 25
	defaultRetryAfterSeconds = 1
	baseUrlV1                = "/api/v1"
)

// HTTPServer exposes whitened entropy over a local HTTP interface for
// consumption by external processes such as the drand binary.
type HTTPServer struct {
	pool              *WhitenedPool
	server            *http.Server
	listener          net.Listener
	shutdownTimeout   time.Duration
	readyThreshold    int
	clock             clock.Clock
	rateLimiter       *tokenBucket
	retryAfterSeconds int
}

// NewHTTPServer constructs an HTTPServer bound to addr, which defaults to
// 127.0.0.1:9797. Unless allowPublic is true, the address is restricted to
// loopback interfaces for security. The server exposes three endpoints:
//   - GET /api/v1/entropy/binary?bytes=N -- returns N raw entropy bytes
//     (default 32, min 1, max 4096) as application/octet-stream.
//   - GET /api/v1/health -- reports raw event count, whitened bytes, and
//     available entropy as plain text.
//   - GET /api/v1/ready -- returns 200 when available entropy meets or
//     exceeds readyThreshold, or 503 otherwise.
//
// Token-bucket rate limiting is applied to the entropy endpoint.
// rateLimitRPS sets the sustained request rate and rateLimitBurst sets the
// burst allowance; both default to 25 when non-positive.
func NewHTTPServer(addr string, pool *WhitenedPool, readyThreshold int, allowPublic bool, retryAfterSeconds int, rateLimitRPS int, rateLimitBurst int) (*HTTPServer, error) {
	if addr == "" {
		addr = defaultHTTPAddress
	}
	if readyThreshold < 0 {
		readyThreshold = 0
	}

	if retryAfterSeconds <= 0 {
		retryAfterSeconds = defaultRetryAfterSeconds
	}

	if rateLimitRPS <= 0 {
		rateLimitRPS = defaultRateLimitRPS
	}

	if rateLimitBurst <= 0 {
		rateLimitBurst = defaultRateLimitBurst
	}

	canonicalAddr, err := enforceLoopbackAddr(addr, allowPublic)
	if err != nil {
		return nil, err
	}

	clk := clock.RealClock{}

	httpServer := &HTTPServer{
		pool:              pool,
		shutdownTimeout:   defaultShutdownTimeout,
		readyThreshold:    readyThreshold,
		clock:             clk,
		retryAfterSeconds: retryAfterSeconds,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(baseUrlV1+"/entropy/binary", httpServer.handleEntropy)
	mux.HandleFunc(baseUrlV1+"/health", httpServer.handleHealth)
	mux.HandleFunc(baseUrlV1+"/ready", httpServer.handleReady)
	mux.HandleFunc(baseUrlV1+"/openapi", httpServer.handleOpenAPI)

	httpServer.server = &http.Server{
		Addr:         canonicalAddr,
		Handler:      mux,
		ReadTimeout:  defaultReadWriteTimeout,
		WriteTimeout: defaultReadWriteTimeout,
		IdleTimeout:  defaultIdleTimeout,
	}

	httpServer.rateLimiter = newTokenBucket(float64(rateLimitRPS), float64(rateLimitBurst), clk)
	log.Printf("entropy http server: rate limiter configured (rps=%d, burst=%d)", rateLimitRPS, rateLimitBurst)

	return httpServer, nil
}

// Start begins listening for HTTP requests. It returns an error if the socket
// cannot be bound.
func (s *HTTPServer) Start() error {
	if s.pool == nil {
		return errors.New("entropy http server: pool is nil")
	}

	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return fmt.Errorf("entropy http server: listen: %w", err)
	}

	s.listener = listener

	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("entropy http server: serve error: %v", err)
		}
	}()

	log.Printf("entropy http server: listening on %s", listener.Addr())
	return nil
}

// StartTLS begins listening for HTTPS requests with TLS or mutual TLS support.
// certFile and keyFile specify the server certificate and private key paths.
// caFile, when non-empty, provides a CA certificate for client verification.
// clientAuth controls the TLS client authentication policy.
func (s *HTTPServer) StartTLS(certFile, keyFile, caFile string, clientAuth tls.ClientAuthType) error {
	if s.pool == nil {
		return errors.New("entropy http server: pool is nil")
	}

	// Use go-authx TLS helpers to configure the server
	tlsConfig := &httpserver.TLSConfig{
		CertFile:   certFile,
		KeyFile:    keyFile,
		CAFile:     caFile,
		ClientAuth: clientAuth,
	}

	if err := httpserver.ConfigureServer(s.server, tlsConfig); err != nil {
		return fmt.Errorf("entropy http server: configure TLS: %w", err)
	}

	log.Printf("entropy http server: loaded server certificate from %s", certFile)
	if caFile != "" {
		log.Printf("entropy http server: using custom CA certificate from %s for client verification", caFile)
	}

	// Create TLS listener
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return fmt.Errorf("entropy http server: listen: %w", err)
	}

	tlsListener := tls.NewListener(listener, s.server.TLSConfig)
	s.listener = tlsListener

	go func() {
		if err := s.server.Serve(tlsListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("entropy http server: serve error: %v", err)
		}
	}()

	log.Printf("entropy http server: listening on %s (TLS enabled)", listener.Addr())
	return nil
}

// enforceLoopbackAddr validates that addr resolves to a loopback interface.
// When allowPublic is true, non-loopback addresses are permitted with a
// warning log. Returns the canonical host:port string or an error.
func enforceLoopbackAddr(addr string, allowPublic bool) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = defaultHTTPAddress
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("entropy http server: invalid address %q: %w", addr, err)
	}

	if host == "" {
		return "", errors.New("entropy http server: host must be specified")
	}

	hostLower := strings.ToLower(host)
	if hostLower == "localhost" {
		return net.JoinHostPort("localhost", port), nil
	}

	ip := net.ParseIP(host)
	if ip == nil {
		if allowPublic {
			log.Printf("entropy http server: ALLOW_PUBLIC_HTTP=true,binding to %s", addr)
			return addr, nil
		}
		return "", fmt.Errorf("entropy http server: host %q is not loopback", host)
	}

	if !ip.IsLoopback() {
		if allowPublic {
			log.Printf("entropy http server: ALLOW_PUBLIC_HTTP=true,binding to %s", addr)
			return net.JoinHostPort(ip.String(), port), nil
		}
		return "", fmt.Errorf("entropy http server: host %q must be loopback", host)
	}

	return net.JoinHostPort(ip.String(), port), nil
}

// Shutdown gracefully stops the HTTP server, waiting up to shutdownTimeout for
// in-flight requests to complete.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
	}

	err := s.server.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *HTTPServer) handleEntropy(response http.ResponseWriter, request *http.Request) {
	start := time.Now()
	status := http.StatusOK
	rateLimited := false
	defer func() {
		duration := time.Since(start)
		metrics.RecordEntropyHTTPRequest(status, duration)
		if status == http.StatusServiceUnavailable {
			metrics.RecordEntropyHTTP503()
			if rateLimited {
				metrics.RecordEntropyHTTPRateLimited()
			}
		}
	}()

	requestBytes := defaultRequestBytes
	if value := request.URL.Query().Get("bytes"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			status = http.StatusBadRequest
			response.Header().Set("Cache-Control", "no-store")
			http.Error(response, "invalid bytes parameter", http.StatusBadRequest)
			return
		}
		requestBytes = parsed
	}

	if requestBytes < minRequestBytes || requestBytes > maxRequestBytes {
		status = http.StatusBadRequest
		response.Header().Set("Cache-Control", "no-store")
		http.Error(response, fmt.Sprintf("bytes must be between %d and %d", minRequestBytes, maxRequestBytes), http.StatusBadRequest)
		return
	}

	if s.rateLimiter != nil {
		allowed, wait := s.rateLimiter.Allow()
		if !allowed {
			status = http.StatusServiceUnavailable
			rateLimited = true
			setNoStoreHeaders(response)
			s.setRetryAfter(response, wait)
			http.Error(response, "rate limit exceeded", http.StatusServiceUnavailable)
			return
		}
	}

	available := 0
	if s.pool != nil {
		available = s.pool.AvailableEntropy()
	}

	setNoStoreHeaders(response)
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("X-Entropy-Available", strconv.Itoa(available))
	response.Header().Set("X-Entropy-Request", strconv.Itoa(requestBytes))

	if s.pool == nil {
		status = http.StatusServiceUnavailable
		s.setRetryAfter(response, 0)
		http.Error(response, "entropy pool unavailable", http.StatusServiceUnavailable)
		return
	}

	data, ok := s.pool.ExtractEntropy(requestBytes)
	if !ok {
		status = http.StatusServiceUnavailable
		s.setRetryAfter(response, 0)
		http.Error(response, fmt.Sprintf("insufficient entropy: requested %d, available %d", requestBytes, available), http.StatusServiceUnavailable)
		return
	}

	if _, err := response.Write(data); err != nil {
		log.Printf("entropy http server: write failed: %v", err)
	}
}

func (s *HTTPServer) handleHealth(response http.ResponseWriter, _ *http.Request) {
	rawEvents := 0
	whitened := 0
	available := 0
	if s.pool != nil {
		rawEvents, whitened = s.pool.PoolStatus()
		available = s.pool.AvailableEntropy()
	}

	setNoStoreHeaders(response)
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(response, "raw_events=%d\nwhitened_bytes=%d\navailable_bytes=%d\n", rawEvents, whitened, available)
}

func (s *HTTPServer) handleReady(response http.ResponseWriter, _ *http.Request) {
	available := 0
	if s.pool != nil {
		available = s.pool.AvailableEntropy()
	}

	setNoStoreHeaders(response)
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("X-Entropy-Available", strconv.Itoa(available))
	response.Header().Set("X-Ready-Threshold", strconv.Itoa(s.readyThreshold))

	if available < s.readyThreshold {
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(response, "ready=false\n")
		return
	}

	response.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(response, "ready=true\n")
}

// handleOpenAPI serves the OpenAPI specification YAML file.
// This endpoint is public (no auth required) to allow Swagger UI to load the spec.
func (s *HTTPServer) handleOpenAPI(response http.ResponseWriter, _ *http.Request) {
	// Construct path to openapi.yaml relative to the binary location
	// Assuming the binary runs from the project root or api/ is accessible
	openapiPath := "api/openapi.yaml"

	// Try alternative paths if running from different directories
	candidatePaths := []string{
		openapiPath,
		"../api/openapi.yaml",
		"../../api/openapi.yaml",
		"/app/api/openapi.yaml", // Docker container path
	}

	var specData []byte
	var err error

	for _, path := range candidatePaths {
		specData, err = os.ReadFile(filepath.Clean(path))
		if err == nil {
			break
		}
	}

	if err != nil {
		log.Printf("entropy http server: failed to read OpenAPI spec: %v", err)
		http.Error(response, "OpenAPI specification not found", http.StatusNotFound)
		return
	}

	response.Header().Set("Content-Type", "application/yaml")
	response.Header().Set("Access-Control-Allow-Origin", "*")
	response.Header().Set("Cache-Control", "public, max-age=3600")
	response.WriteHeader(http.StatusOK)

	if _, err := response.Write(specData); err != nil {
		log.Printf("entropy http server: failed to write OpenAPI spec: %v", err)
	}
}

// setNoStoreHeaders sets Cache-Control and Pragma headers to prevent caching
// of entropy responses.
func setNoStoreHeaders(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Pragma", "no-cache")
}

// tokenBucket implements a simple token-bucket rate limiter. Tokens are
// refilled at a constant rate up to a maximum capacity. It is safe for
// concurrent use.
type tokenBucket struct {
	mu         sync.Mutex
	capacity   float64
	tokens     float64
	refillRate float64
	lastRefill time.Time
	clock      clock.Clock
}

// newTokenBucket creates a token bucket that refills at rate tokens per second
// with a maximum burst capacity. The bucket starts full.
func newTokenBucket(rate float64, burst float64, clk clock.Clock) *tokenBucket {
	if rate <= 0 {
		rate = 1
	}

	if burst <= 0 {
		burst = rate
	}

	if clk == nil {
		clk = clock.RealClock{}
	}

	return &tokenBucket{
		capacity:   burst,
		tokens:     burst,
		refillRate: rate,
		lastRefill: clk.Now(),
		clock:      clk,
	}
}

func (b *tokenBucket) Allow() (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.clock.Now()
	elapsed := now.Sub(b.lastRefill)
	if elapsed > 0 {
		refill := elapsed.Seconds() * b.refillRate

		if refill > 0 {
			b.tokens = math.Min(b.capacity, b.tokens+refill)
		}

		b.lastRefill = now
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0

		return true, 0
	}

	deficit := 1.0 - b.tokens
	if deficit < 0 {
		deficit = 0
	}

	waitSeconds := deficit / b.refillRate
	if waitSeconds < 0 {
		waitSeconds = 0
	}

	waitDuration := time.Duration(waitSeconds * float64(time.Second))
	if waitDuration < time.Second {
		waitDuration = time.Second
	}

	return false, waitDuration
}

func (s *HTTPServer) setRetryAfter(response http.ResponseWriter, wait time.Duration) {
	seconds := s.retryAfterSeconds
	if wait > 0 {
		calc := int(math.Ceil(wait.Seconds()))
		if calc > seconds {
			seconds = calc
		}
	}
	if seconds < 1 {
		seconds = defaultRetryAfterSeconds
	}
	response.Header().Set("Retry-After", strconv.Itoa(seconds))
}
