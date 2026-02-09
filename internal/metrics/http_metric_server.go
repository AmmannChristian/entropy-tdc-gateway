package metrics

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/AmmannChristian/go-authx/httpserver"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const baseUrlV1 = "/api/v1"

// Server provides an HTTP server for exposing Prometheus metrics via the /api/v1/metrics endpoint.
// It implements graceful shutdown and proper error handling for production use.
type Server struct {
	addr   string
	server *http.Server
}

// NewServer creates a new metrics HTTP server that will listen on the specified address.
// The address should be in the format "host:port" (e.g., "127.0.0.1:8000" or ":8000").
//
// The server exposes two endpoints:
//   - GET /api/v1/metrics - Prometheus metrics endpoint (uses DefaultGatherer)
//   - GET /api/v1/health - Simple health check
//
// Example:
//
//	metricsServer := metrics.NewServer(":8000")
//	if err := metricsServer.Start(); err != nil {
//	    log.Fatal(err)
//	}
func NewServer(addr string) *Server {
	mux := http.NewServeMux()

	// Register Prometheus metrics handler
	mux.Handle(baseUrlV1+"/metrics", promhttp.Handler())

	// Add a simple health check endpoint
	mux.HandleFunc(baseUrlV1+"/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			log.Printf("metrics: health handler write error: %v", err)
		}
	})

	return &Server{
		addr: addr,
		server: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}
}

// Start begins serving HTTP requests on the configured address.
// This method blocks until the server is shut down or encounters a fatal error.
//
// Returns:
//   - nil if the server shut down gracefully (via Shutdown)
//   - error if the server failed to start or encountered a fatal error
//
// Note: http.ErrServerClosed is not returned as an error,it indicates successful shutdown.
func (s *Server) Start() error {
	if s.server == nil {
		return errors.New("metrics server not initialized")
	}

	log.Printf("metrics: starting HTTP server on %s", s.addr)

	// Validate that the address can be resolved before attempting to listen
	if err := validateAddress(s.addr); err != nil {
		return fmt.Errorf("metrics: invalid address %q: %w", s.addr, err)
	}

	err := s.server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("metrics: HTTP server error: %w", err)
	}

	log.Println("metrics: HTTP server stopped")
	return nil
}

// StartTLS begins serving HTTPS requests with TLS/mTLS support.
// certFile: path to server certificate
// keyFile: path to server private key
// caFile: path to CA certificate for client verification (optional, empty string to skip)
// clientAuth: TLS client authentication mode (NoClientCert, RequestClientCert, RequireAndVerifyClientCert)
//
// Returns:
//   - nil if the server shut down gracefully (via Shutdown)
//   - error if the server failed to start or encountered a fatal error
func (s *Server) StartTLS(certFile, keyFile, caFile string, clientAuth tls.ClientAuthType) error {
	if s.server == nil {
		return errors.New("metrics server not initialized")
	}

	log.Printf("metrics: starting HTTPS server on %s", s.addr)

	// Validate that the address can be resolved before attempting to listen
	if err := validateAddress(s.addr); err != nil {
		return fmt.Errorf("metrics: invalid address %q: %w", s.addr, err)
	}

	// Use go-authx TLS helpers to configure the server
	tlsConfig := &httpserver.TLSConfig{
		CertFile:   certFile,
		KeyFile:    keyFile,
		CAFile:     caFile,
		ClientAuth: clientAuth,
	}

	if err := httpserver.ConfigureServer(s.server, tlsConfig); err != nil {
		return fmt.Errorf("metrics: configure TLS: %w", err)
	}

	log.Printf("metrics: loaded server certificate from %s", certFile)
	if caFile != "" {
		log.Printf("metrics: using custom CA certificate from %s for client verification", caFile)
	}

	err := s.server.ListenAndServeTLS("", "")
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("metrics: HTTPS server error: %w", err)
	}

	log.Println("metrics: HTTPS server stopped")
	return nil
}

// Shutdown gracefully stops the HTTP server, allowing active connections to complete.
// It uses the provided context to control the shutdown timeout.
//
// Example:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
//	defer cancel()
//	if err := metricsServer.Shutdown(ctx); err != nil {
//	    log.Printf("metrics server shutdown error: %v", err)
//	}
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}

	log.Println("metrics: shutting down HTTP server...")

	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("metrics: shutdown error: %w", err)
	}

	log.Println("metrics: HTTP server shutdown complete")
	return nil
}

// validateAddress checks if the given address is valid and can be resolved.
// This provides early validation before attempting to bind to the address.
func validateAddress(addr string) error {
	if addr == "" {
		return errors.New("empty address")
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid host:port format: %w", err)
	}

	if port == "" {
		return errors.New("port is required")
	}

	// If host is empty or wildcard, that's valid (listen on all interfaces)
	if host == "" || host == "0.0.0.0" || host == "::" {
		return nil
	}

	// Validate that the host can be resolved (either IP or hostname)
	ip := net.ParseIP(host)
	if ip != nil {
		return nil // Valid IP address
	}

	// For hostnames (including "localhost"), attempt resolution
	if _, err := net.LookupHost(host); err != nil {
		return fmt.Errorf("cannot resolve host %q: %w", host, err)
	}

	return nil
}
