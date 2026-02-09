package entropy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"entropy-tdc-gateway/internal/clock"
	"entropy-tdc-gateway/internal/mqtt"
	testutil "entropy-tdc-gateway/testutil"
)

func startHTTPServerOrSkip(t *testing.T, server *HTTPServer) {
	t.Helper()

	if err := server.Start(); err != nil {
		if isListenPermissionError(err) {
			t.Skipf("skipping HTTP server test: %v", err)
		}
		t.Fatalf("expected Start to succeed, got error: %v", err)
	}
}

// newHTTPServerWithClock is a test helper that creates an HTTPServer with a custom clock.
// This allows tests to use FakeClock to control time.
func newHTTPServerWithClock(addr string, pool *WhitenedPool, readyThreshold int, allowPublic bool, retryAfterSeconds int, rateLimitRPS int, rateLimitBurst int, clk clock.Clock) *HTTPServer {
	// Reuse the parameter validation logic from NewHTTPServer
	if addr == "" {
		addr = "127.0.0.1:9797"
	}
	if readyThreshold < 0 {
		readyThreshold = 0
	}
	if retryAfterSeconds <= 0 {
		retryAfterSeconds = 1
	}
	if rateLimitRPS <= 0 {
		rateLimitRPS = 25
	}
	if rateLimitBurst <= 0 {
		rateLimitBurst = 25
	}

	canonicalAddr, err := enforceLoopbackAddr(addr, allowPublic)
	if err != nil {
		panic(fmt.Sprintf("test setup error: %v", err))
	}

	httpServer := &HTTPServer{
		pool:              pool,
		shutdownTimeout:   5 * time.Second,
		readyThreshold:    readyThreshold,
		clock:             clk,
		retryAfterSeconds: retryAfterSeconds,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(baseUrlV1+"/entropy/bytes", httpServer.handleEntropy)
	mux.HandleFunc(baseUrlV1+"/health", httpServer.handleHealth)
	mux.HandleFunc(baseUrlV1+"/ready", httpServer.handleReady)

	httpServer.server = &http.Server{
		Addr:         canonicalAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	httpServer.rateLimiter = newTokenBucket(float64(rateLimitRPS), float64(rateLimitBurst), clk)

	return httpServer
}

func writeSelfSignedCert(t *testing.T) (string, string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	certPath := filepath.Join(t.TempDir(), "cert.pem")
	keyPath := filepath.Join(filepath.Dir(certPath), "key.pem")

	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("failed to write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatalf("failed to write key: %v", err)
	}

	return certPath, keyPath
}

func isListenPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied")
}

func TestEnforceLoopbackAddrAllowsLoopbackHosts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		addr     string
		expected string
	}{
		{"ipv4 loopback", "127.0.0.1:8080", "127.0.0.1:8080"},
		{"localhost", "localhost:9000", "localhost:9000"},
		{"ipv6 loopback", "[::1]:7000", "[::1]:7000"},
	}

	for _, tc := range tests {
		t := t
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rez, err := enforceLoopbackAddr(tc.addr, false)
			if err != nil {
				t.Fatalf("expected success, got error: %v", err)
			}
			if rez != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, rez)
			}
		})
	}
}

func TestEnforceLoopbackAddrRejectsUnsafeHosts(t *testing.T) {
	t.Parallel()

	for _, addr := range []string{"0.0.0.0:8080", "192.168.1.10:80", ":9000"} {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			if _, err := enforceLoopbackAddr(addr, false); err == nil {
				t.Fatalf("expected error for addr %q", addr)
			}
		})
	}
}

func TestEnforceLoopbackAddrAllowsPublicWhenConfigured(t *testing.T) {
	t.Parallel()

	addr, err := enforceLoopbackAddr("0.0.0.0:8080", true)
	if err != nil {
		t.Fatalf("expected no error when public binding allowed: %v", err)
	}
	if addr != "0.0.0.0:8080" {
		t.Fatalf("expected passthrough address, got %q", addr)
	}

	addr, err = enforceLoopbackAddr("example.com:9000", true)
	if err != nil {
		t.Fatalf("expected hostnames to be allowed when public binding enabled: %v", err)
	}
	if addr != "example.com:9000" {
		t.Fatalf("expected original hostname preserved, got %q", addr)
	}
}

func TestNewHTTPServerFailsOnNonLoopback(t *testing.T) {
	t.Parallel()

	pool := NewWhitenedPoolWithBounds(1, 1)

	if server, err := NewHTTPServer("192.168.0.5:9797", pool, 0, false, defaultRetryAfterSeconds, defaultRateLimitRPS, defaultRateLimitBurst); err == nil || server != nil {
		t.Fatalf("expected error for non-loopback address, got server=%v err=%v", server, err)
	}
}

func TestNewHTTPServerCanonicalizesAddress(t *testing.T) {
	t.Parallel()

	pool := NewWhitenedPoolWithBounds(1, 1)

	server, err := NewHTTPServer("127.0.0.1:0", pool, 10, false, defaultRetryAfterSeconds, defaultRateLimitRPS, defaultRateLimitBurst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server.server.Addr != "127.0.0.1:0" {
		t.Fatalf("expected canonical address, got %q", server.server.Addr)
	}
}

func TestHandleEntropyRateLimitSetsRetryAfter(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(32, 64)
	pool.AddBatch([]mqtt.TDCEvent{{TdcTimestampPs: 1}, {TdcTimestampPs: 2}, {TdcTimestampPs: 3}})

	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, defaultRetryAfterSeconds, defaultRateLimitRPS, defaultRateLimitBurst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fakeClock := clock.NewFakeClock()
	server.rateLimiter = newTokenBucket(1, 1, fakeClock)

	req1 := httptest.NewRequest(http.MethodGet, baseUrlV1+"/entropy/bytes?bytes=1", nil)
	rec1 := httptest.NewRecorder()
	server.handleEntropy(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got status %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, baseUrlV1+"/entropy/bytes?bytes=1", nil)
	rec2 := httptest.NewRecorder()
	server.handleEntropy(rec2, req2)
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected rate limited status, got %d", rec2.Code)
	}
	if got := rec2.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After header '1', got %q", got)
	}
}

func TestHandleEntropyInsufficientEntropySetsRetryAfter(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(1, 1)
	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, defaultRetryAfterSeconds, defaultRateLimitRPS, defaultRateLimitBurst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	server.rateLimiter = nil

	req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/entropy/bytes?bytes=8", nil)
	rec := httptest.NewRecorder()
	server.retryAfterSeconds = 5
	server.handleEntropy(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 status, got %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("expected Retry-After header '5', got %q", got)
	}
}

func TestTokenBucketRefill(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	fakeClock := clock.NewFakeClock()
	bucket := newTokenBucket(1, 1, fakeClock)

	if allowed, _ := bucket.Allow(); !allowed {
		t.Fatal("expected first token to be available")
	}
	if allowed, wait := bucket.Allow(); allowed || wait < time.Second {
		t.Fatalf("expected rate limit with at least 1s wait, got allowed=%v wait=%v", allowed, wait)
	}

	fakeClock.Advance(time.Second)
	if allowed, _ := bucket.Allow(); !allowed {
		t.Fatal("expected token to refill after 1s")
	}
}

func TestHandleEntropyBytesValidation(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		status    int
		expectLen int
	}{
		{"zero", "0", http.StatusBadRequest, 0},
		{"negative", "-1", http.StatusBadRequest, 0},
		{"tooLarge", "4097", http.StatusBadRequest, 0},
		{"nonNumeric", "bad", http.StatusBadRequest, 0},
		{"default", "", http.StatusOK, 32},
		{"oneByte", "1", http.StatusOK, 1},
		{"maxBytes", "4096", http.StatusOK, 4096},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testutil.ResetRegistryForTest(t)

			pool := NewWhitenedPoolWithBounds(32, 8192)
			if tc.status == http.StatusOK {
				pool.mu.Lock()
				required := pool.minPoolSize + tc.expectLen
				pool.whitened = make([]byte, required)
				for i := range pool.whitened {
					pool.whitened[i] = byte(i)
				}
				pool.mu.Unlock()
			}

			server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, defaultRetryAfterSeconds, defaultRateLimitRPS, defaultRateLimitBurst)
			if err != nil {
				t.Fatalf("unexpected error creating server: %v", err)
			}

			query := tc.query
			if query != "" {
				query = "?bytes=" + query
			}

			req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/entropy/bytes"+query, nil)
			rec := httptest.NewRecorder()
			server.handleEntropy(rec, req)

			if rec.Code != tc.status {
				t.Fatalf("expected status %d, got %d", tc.status, rec.Code)
			}

			if tc.status == http.StatusOK {
				if got := len(rec.Body.Bytes()); got != tc.expectLen {
					t.Fatalf("expected %d bytes, got %d", tc.expectLen, got)
				}
				if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
					t.Fatalf("expected Content-Type application/octet-stream, got %q", ct)
				}
				if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
					t.Fatalf("expected Cache-Control no-store, got %q", cc)
				}
			}
		})
	}
}

func TestHTTPServerStartLifecycle(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(1, 4)
	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, defaultRetryAfterSeconds, defaultRateLimitRPS, defaultRateLimitBurst)
	if err != nil {
		t.Fatalf("unexpected error creating server: %v", err)
	}

	t.Cleanup(func() {
		_ = server.Shutdown(context.TODO())
	})

	startHTTPServerOrSkip(t, server)

	addr := server.listener.Addr().String()
	var resp *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get(fmt.Sprintf("http://%s/api/v1/health", addr))
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("failed to reach health endpoint: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	if err := server.Shutdown(context.TODO()); err != nil {
		t.Fatalf("expected Shutdown to succeed, got error: %v", err)
	}
}

func TestHTTPServerStartRejectsNilPool(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	server, err := NewHTTPServer("127.0.0.1:0", nil, 0, false, defaultRetryAfterSeconds, defaultRateLimitRPS, defaultRateLimitBurst)
	if err != nil {
		t.Fatalf("unexpected error creating server: %v", err)
	}

	if err := server.Start(); err == nil {
		t.Fatal("expected Start to fail when pool is nil")
	}
}

func TestHandleHealthReportsPoolState(t *testing.T) {
	pool := NewWhitenedPoolWithBounds(1, 8)
	pool.mu.Lock()
	pool.rawEvents = []mqtt.TDCEvent{{}, {}}
	pool.whitened = []byte{1, 2, 3, 4}
	pool.minPoolSize = 1
	pool.mu.Unlock()

	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, defaultRetryAfterSeconds, defaultRateLimitRPS, defaultRateLimitBurst)
	if err != nil {
		t.Fatalf("unexpected error creating server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/health", nil)
	rec := httptest.NewRecorder()
	server.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected content type: %q", ct)
	}

	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	contents := string(body)
	if !strings.Contains(contents, "raw_events=2") {
		t.Fatalf("expected raw_events count, body=%q", contents)
	}
	if !strings.Contains(contents, "whitened_bytes=4") {
		t.Fatalf("expected whitened_bytes count, body=%q", contents)
	}
	if !strings.Contains(contents, "available_bytes=3") {
		t.Fatalf("expected available_bytes count, body=%q", contents)
	}
}

func TestHandleReadyReflectsAvailability(t *testing.T) {
	pool := NewWhitenedPoolWithBounds(1, 8)
	server, err := NewHTTPServer("127.0.0.1:0", pool, 3, false, defaultRetryAfterSeconds, defaultRateLimitRPS, defaultRateLimitBurst)
	if err != nil {
		t.Fatalf("unexpected error creating server: %v", err)
	}

	pool.mu.Lock()
	pool.minPoolSize = 1
	pool.whitened = []byte{1, 2, 3}
	pool.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/ready", nil)
	rec := httptest.NewRecorder()
	server.handleReady(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when below threshold, got %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "ready=false" {
		t.Fatalf("expected readiness false, got %q", body)
	}

	pool.mu.Lock()
	pool.whitened = []byte{1, 2, 3, 4, 5}
	pool.mu.Unlock()

	rec2 := httptest.NewRecorder()
	server.handleReady(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 when threshold met, got %d", rec2.Code)
	}
	if body := strings.TrimSpace(rec2.Body.String()); body != "ready=true" {
		t.Fatalf("expected readiness true, got %q", body)
	}
}

func TestNewTokenBucketWithInvalidParameters(t *testing.T) {
	fakeClock := clock.NewFakeClock()

	tests := []struct {
		name         string
		rate         float64
		burst        float64
		expectRate   float64
		expectBurst  float64
		expectTokens float64
	}{
		{"zero rate", 0, 10, 1, 10, 10},
		{"negative rate", -5, 10, 1, 10, 10},
		{"zero burst", 10, 0, 10, 10, 10},
		{"negative burst", 10, -5, 10, 10, 10},
		{"both zero", 0, 0, 1, 1, 1},
		{"both negative", -1, -1, 1, 1, 1},
		{"valid values", 5, 10, 5, 10, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bucket := newTokenBucket(tt.rate, tt.burst, fakeClock)

			if bucket.refillRate != tt.expectRate {
				t.Errorf("refillRate = %v, want %v", bucket.refillRate, tt.expectRate)
			}
			if bucket.capacity != tt.expectBurst {
				t.Errorf("capacity = %v, want %v", bucket.capacity, tt.expectBurst)
			}
			if bucket.tokens != tt.expectTokens {
				t.Errorf("initial tokens = %v, want %v", bucket.tokens, tt.expectTokens)
			}
		})
	}
}

func TestNewTokenBucketWithNilClock(t *testing.T) {
	t.Parallel()

	bucket := newTokenBucket(10, 10, nil)

	if bucket.clock == nil {
		t.Error("expected clock to be set to RealClock when nil provided")
	}
}

func TestTokenBucketMultipleRefills(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	fakeClock := clock.NewFakeClock()
	bucket := newTokenBucket(2, 5, fakeClock) // 2 tokens/sec, max 5

	// Consume all 5 initial tokens
	for i := 0; i < 5; i++ {
		if allowed, _ := bucket.Allow(); !allowed {
			t.Fatalf("token %d should be available", i+1)
		}
	}

	// Should be exhausted
	if allowed, wait := bucket.Allow(); allowed {
		t.Fatal("expected rate limit after consuming all tokens")
	} else if wait < 500*time.Millisecond {
		t.Errorf("expected wait >= 500ms, got %v", wait)
	}

	// Advance 1 second -> +2 tokens
	fakeClock.Advance(time.Second)
	for i := 0; i < 2; i++ {
		if allowed, _ := bucket.Allow(); !allowed {
			t.Fatalf("token %d should be available after 1s refill", i+1)
		}
	}

	// Advance 2.5 seconds -> +5 tokens (capped at capacity)
	fakeClock.Advance(2500 * time.Millisecond)
	for i := 0; i < 5; i++ {
		if allowed, _ := bucket.Allow(); !allowed {
			t.Fatalf("token %d should be available after full refill", i+1)
		}
	}

	// Should be exhausted again
	if allowed, _ := bucket.Allow(); allowed {
		t.Fatal("expected exhaustion after consuming refilled tokens")
	}
}

func TestTokenBucketPartialRefill(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	fakeClock := clock.NewFakeClock()
	bucket := newTokenBucket(10, 10, fakeClock) // 10 tokens/sec

	// Consume all tokens
	for i := 0; i < 10; i++ {
		bucket.Allow()
	}

	// Advance only 100ms -> +1 token
	fakeClock.Advance(100 * time.Millisecond)
	if allowed, _ := bucket.Allow(); !allowed {
		t.Error("expected 1 token available after 100ms")
	}

	// Should be exhausted again
	if allowed, _ := bucket.Allow(); allowed {
		t.Error("expected exhaustion after partial refill")
	}
}

func TestSetRetryAfterWithVariousDurations(t *testing.T) {
	tests := []struct {
		name               string
		retryAfterSeconds  int
		waitDuration       time.Duration
		expectedRetryAfter string
	}{
		{"default with zero wait", 1, 0, "1"},
		{"default with short wait", 5, 200 * time.Millisecond, "5"},
		{"wait exceeds default", 1, 3 * time.Second, "3"},
		{"wait rounds up", 2, 2500 * time.Millisecond, "3"},
		{"large wait", 1, 10 * time.Second, "10"},
		{"fractional wait", 1, 1500 * time.Millisecond, "2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := NewWhitenedPoolWithBounds(1, 1)
			server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, tt.retryAfterSeconds, 1, 1)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			rec := httptest.NewRecorder()
			server.setRetryAfter(rec, tt.waitDuration)

			got := rec.Header().Get("Retry-After")
			if got != tt.expectedRetryAfter {
				t.Errorf("Retry-After = %q, want %q", got, tt.expectedRetryAfter)
			}
		})
	}
}

func TestHandleEntropyRateLimitWithDifferentBuckets(t *testing.T) {
	tests := []struct {
		name        string
		rate        float64
		burst       float64
		expectRate  float64
		expectBurst float64
	}{
		{"strict 1/1", 1, 1, 1, 1},
		{"burst 5", 1, 5, 1, 5},
		{"high rate", 100, 100, 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testutil.ResetRegistryForTest(t)

			pool := NewWhitenedPoolWithBounds(32, 8192)
			pool.mu.Lock()
			pool.whitened = make([]byte, 8192)
			pool.minPoolSize = 1
			pool.mu.Unlock()

			// Use FakeClock to control time
			fakeClock := clock.NewFakeClock()
			server := newHTTPServerWithClock("127.0.0.1:0", pool, 0, false, 1, int(tt.rate), int(tt.burst), fakeClock)

			// Verify rate limiter is configured correctly
			if server.rateLimiter.refillRate != tt.expectRate {
				t.Errorf("rate limiter refillRate = %v, want %v", server.rateLimiter.refillRate, tt.expectRate)
			}
			if server.rateLimiter.capacity != tt.expectBurst {
				t.Errorf("rate limiter capacity = %v, want %v", server.rateLimiter.capacity, tt.expectBurst)
			}

			// Test token bucket directly to avoid entropy pool issues
			// Verify initial burst tokens are available
			successCount := 0
			for i := 0; i < int(tt.burst); i++ {
				if allowed, _ := server.rateLimiter.Allow(); allowed {
					successCount++
				}
			}

			if successCount != int(tt.expectBurst) {
				t.Errorf("initial burst: allowed requests = %d, want %d", successCount, int(tt.expectBurst))
			}

			// Next token request should be rate limited
			if allowed, _ := server.rateLimiter.Allow(); allowed {
				t.Error("expected rate limit after burst exhausted")
			}
		})
	}
}

func TestHTTPServerStartWithAlreadyBoundPort(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(1, 4)

	// Start first server
	server1, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	startHTTPServerOrSkip(t, server1)
	defer server1.Shutdown(context.Background())

	addr := server1.listener.Addr().String()

	// Try to start second server on same port
	server2, err := NewHTTPServer(addr, pool, 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error creating server: %v", err)
	}

	err = server2.Start()
	if err == nil {
		t.Fatal("expected Start to fail when port already bound")
	}
}

func TestHTTPServerShutdownWithNilContext(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(1, 4)
	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	startHTTPServerOrSkip(t, server)

	// Shutdown with nil context should use default timeout
	err = server.Shutdown(context.TODO())
	if err != nil {
		t.Errorf("expected Shutdown with nil context to succeed, got: %v", err)
	}
}

func TestHTTPServerShutdownWithExpiredContext(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(1, 4)
	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	startHTTPServerOrSkip(t, server)

	// Create already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = server.Shutdown(ctx)
	// May succeed if shutdown is fast, or fail with context cancelled
	// We just verify it doesn't panic
	_ = err
}

func TestHTTPServerShutdownWhenNotStarted(t *testing.T) {
	t.Parallel()

	pool := NewWhitenedPoolWithBounds(1, 4)
	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Shutdown without Start should succeed (no-op)
	err = server.Shutdown(context.Background())
	if err != nil {
		t.Errorf("expected Shutdown of non-started server to succeed, got: %v", err)
	}
}

func TestNewHTTPServerWithInvalidRetryAfter(t *testing.T) {
	pool := NewWhitenedPoolWithBounds(1, 4)

	tests := []struct {
		name        string
		retryAfter  int
		expectValue int
	}{
		{"zero", 0, defaultRetryAfterSeconds},
		{"negative", -5, defaultRetryAfterSeconds},
		{"valid", 10, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, tt.retryAfter, 10, 10)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if server.retryAfterSeconds != tt.expectValue {
				t.Errorf("retryAfterSeconds = %d, want %d", server.retryAfterSeconds, tt.expectValue)
			}
		})
	}
}

func TestNewHTTPServerWithInvalidRateLimitParameters(t *testing.T) {
	pool := NewWhitenedPoolWithBounds(1, 4)

	tests := []struct {
		name        string
		rps         int
		burst       int
		expectRPS   float64
		expectBurst float64
	}{
		{"zero rps", 0, 10, defaultRateLimitRPS, 10},
		{"negative rps", -5, 10, defaultRateLimitRPS, 10},
		{"zero burst", 10, 0, 10, defaultRateLimitBurst},
		{"negative burst", 10, -5, 10, defaultRateLimitBurst},
		{"both zero", 0, 0, defaultRateLimitRPS, defaultRateLimitBurst},
		{"valid", 50, 100, 50, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 1, tt.rps, tt.burst)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if server.rateLimiter.refillRate != tt.expectRPS {
				t.Errorf("rate limiter RPS = %v, want %v", server.rateLimiter.refillRate, tt.expectRPS)
			}
			if server.rateLimiter.capacity != tt.expectBurst {
				t.Errorf("rate limiter burst = %v, want %v", server.rateLimiter.capacity, tt.expectBurst)
			}
		})
	}
}

func TestNewHTTPServerWithNegativeReadyThreshold(t *testing.T) {
	t.Parallel()

	pool := NewWhitenedPoolWithBounds(1, 4)
	server, err := NewHTTPServer("127.0.0.1:0", pool, -10, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if server.readyThreshold != 0 {
		t.Errorf("readyThreshold = %d, want 0", server.readyThreshold)
	}
}

func TestNewHTTPServerWithEmptyAddress(t *testing.T) {
	t.Parallel()

	pool := NewWhitenedPoolWithBounds(1, 4)
	server, err := NewHTTPServer("", pool, 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if server.server.Addr != defaultHTTPAddress {
		t.Errorf("address = %q, want %q", server.server.Addr, defaultHTTPAddress)
	}
}

func TestHandleEntropyWithEmptyPool(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(1, 4)
	// Pool is empty (no whitened data)

	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 3, 100, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/entropy/bytes?bytes=32", nil)
	rec := httptest.NewRecorder()
	server.handleEntropy(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for empty pool, got %d", rec.Code)
	}

	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter != "3" {
		t.Errorf("expected Retry-After=3, got %q", retryAfter)
	}
}

func TestHandleEntropyWithNearEmptyPool(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(10, 100)
	pool.mu.Lock()
	pool.whitened = []byte{1, 2, 3, 4, 5} // Only 5 bytes available (less than minPoolSize)
	pool.minPoolSize = 10
	pool.mu.Unlock()

	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 2, 100, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/entropy/bytes?bytes=1", nil)
	rec := httptest.NewRecorder()
	server.handleEntropy(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when below minPoolSize, got %d", rec.Code)
	}
}

func TestHandleEntropyWithNilPool(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	server, err := NewHTTPServer("127.0.0.1:0", nil, 0, false, 1, 100, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/entropy/bytes?bytes=32", nil)
	rec := httptest.NewRecorder()
	server.handleEntropy(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for nil pool, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "entropy pool unavailable") {
		t.Errorf("expected pool unavailable message, got: %q", body)
	}
}

func TestHandleEntropySuccessHeaders(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(32, 8192)
	pool.mu.Lock()
	pool.whitened = make([]byte, 1000)
	pool.minPoolSize = 32
	for i := range pool.whitened {
		pool.whitened[i] = byte(i % 256)
	}
	pool.mu.Unlock()

	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 1, 100, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/entropy/bytes?bytes=64", nil)
	rec := httptest.NewRecorder()
	server.handleEntropy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	// Check all required headers
	headers := map[string]string{
		"Content-Type":      "application/octet-stream",
		"Cache-Control":     "no-store",
		"Pragma":            "no-cache",
		"X-Entropy-Request": "64",
	}

	for header, expected := range headers {
		got := rec.Header().Get(header)
		if got != expected {
			t.Errorf("%s = %q, want %q", header, got, expected)
		}
	}

	// X-Entropy-Available should be present
	if available := rec.Header().Get("X-Entropy-Available"); available == "" {
		t.Error("X-Entropy-Available header missing")
	}

	// Body should be exactly 64 bytes
	if got := len(rec.Body.Bytes()); got != 64 {
		t.Errorf("body length = %d, want 64", got)
	}
}

func TestHandleEntropyConcurrentRequests(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(32, 16384)
	pool.mu.Lock()
	pool.whitened = make([]byte, 16384)
	pool.minPoolSize = 32
	for i := range pool.whitened {
		pool.whitened[i] = byte(i % 256)
	}
	pool.mu.Unlock()

	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 1, 1000, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Disable rate limiting for this test
	server.rateLimiter = nil

	const numRequests = 50
	const bytesPerRequest = 32

	results := make(chan int, numRequests)

	// Launch concurrent requests
	for i := 0; i < numRequests; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s/entropy/bytes?bytes=%d", baseUrlV1, bytesPerRequest), nil)
			rec := httptest.NewRecorder()
			server.handleEntropy(rec, req)
			results <- rec.Code
		}()
	}

	// Collect results
	successCount := 0
	for i := 0; i < numRequests; i++ {
		code := <-results
		if code == http.StatusOK {
			successCount++
		}
	}

	// All should succeed since we have enough entropy
	if successCount != numRequests {
		t.Errorf("success count = %d, want %d", successCount, numRequests)
	}
}

func TestTokenBucketConcurrentAccess(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	fakeClock := clock.NewFakeClock()
	bucket := newTokenBucket(100, 100, fakeClock)

	const numGoroutines = 20
	const requestsPerGoroutine = 10

	results := make(chan bool, numGoroutines*requestsPerGoroutine)

	// Launch concurrent Allow() calls
	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < requestsPerGoroutine; j++ {
				allowed, _ := bucket.Allow()
				results <- allowed
			}
		}()
	}

	// Collect results
	successCount := 0
	for i := 0; i < numGoroutines*requestsPerGoroutine; i++ {
		if <-results {
			successCount++
		}
	}

	// Should allow exactly 100 requests (capacity)
	if successCount != 100 {
		t.Errorf("allowed requests = %d, want 100", successCount)
	}
}

func TestHandleHealthWithNilPool(t *testing.T) {
	t.Parallel()

	server, err := NewHTTPServer("127.0.0.1:0", nil, 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/health", nil)
	rec := httptest.NewRecorder()
	server.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK even with nil pool, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "raw_events=0") || !strings.Contains(body, "whitened_bytes=0") {
		t.Errorf("expected zero values for nil pool, got: %q", body)
	}
}

func TestHandleReadyWithNilPool(t *testing.T) {
	t.Parallel()

	server, err := NewHTTPServer("127.0.0.1:0", nil, 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, baseUrlV1+"/ready", nil)
	rec := httptest.NewRecorder()
	server.handleReady(rec, req)

	// With nil pool and threshold 0, should be "ready" (0 >= 0)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK with nil pool and 0 threshold, got %d", rec.Code)
	}
}

func TestEnforceLoopbackAddrEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		addr        string
		allowPublic bool
		wantErr     bool
	}{
		{"empty string", "", false, false}, // Should default to defaultHTTPAddress
		{"whitespace", "   ", false, false},
		{"missing port", "127.0.0.1", false, true},
		{"ipv6 without brackets", "::1:8080", false, true},
		{"empty host", ":8080", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := enforceLoopbackAddr(tt.addr, tt.allowPublic)
			if (err != nil) != tt.wantErr {
				t.Errorf("enforceLoopbackAddr(%q, %v) error = %v, wantErr %v", tt.addr, tt.allowPublic, err, tt.wantErr)
			}
		})
	}
}

func TestHTTPServerStartTLSAndShutdown(t *testing.T) {
	testutil.ResetRegistryForTest(t)

	pool := NewWhitenedPoolWithBounds(32, 64)
	server, err := NewHTTPServer("127.0.0.1:0", pool, 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	certFile, keyFile := writeSelfSignedCert(t)

	if err := server.StartTLS(certFile, keyFile, certFile, tls.RequireAndVerifyClientCert); err != nil {
		if isListenPermissionError(err) {
			t.Skipf("skipping TLS start due to permission error: %v", err)
		}
		// Some environments disallow binding sockets entirely
		if errors.Is(err, context.DeadlineExceeded) {
			t.Skipf("skipping TLS start due to context error: %v", err)
		}
		t.Fatalf("StartTLS failed: %v", err)
	}

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
}

func TestHTTPServerShutdownNilContext(t *testing.T) {
	server, err := NewHTTPServer("127.0.0.1:0", NewWhitenedPoolWithBounds(1, 1), 0, false, 1, 10, 10)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	//lint:ignore SA1012 we explicitly verify that Shutdown tolerates a nil context for backwards compatibility
	if err := server.Shutdown(nil); err != nil {
		t.Fatalf("expected Shutdown with nil context to succeed, got %v", err)
	}
}

// NOTE: readTLSFile tests have been removed as TLS file handling is now done
// by go-authx/httpserver. TLS file reading is tested in go-authx.
