// Package config provides configuration management for the entropy-tdc-gateway application.
// Configuration is loaded from environment variables with sensible defaults.
package config

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Environment constants define the application runtime environments.
const (
	EnvironmentDevelopment   = "dev"
	EnvironmentProduction    = "prod"
	defaultHTTPAddr          = "127.0.0.1:9797"
	defaultPoolMinBytes      = 512
	defaultPoolMaxBytes      = 16384
	defaultReadyMinBytes     = 1024
	defaultRetryAfterSeconds = 1
	defaultMetricsPort       = 8000
	defaultBatchSize         = 1840   // Default event count for approximately ten seconds of nominal acquisition.
	minBatchSize             = 1840   // Minimum supported batch size.
	maxBatchSize             = 184000 // Maximum supported batch size.
)

// MQTT contains configuration for the MQTT broker connection.
type MQTT struct {
	BrokerURL string   `json:"broker_url"`  // MQTT broker URL (e.g., "tcp://localhost:1883" or "ssl://mqtt.example.com:8883")
	ClientID  string   `json:"client_id"`   // MQTT client ID (auto-generated if empty)
	Topics    []string `json:"topics"`      // MQTT topics to subscribe to (e.g., ["timestamps/channel/1","timestamps/channel/2"])
	QoS       byte     `json:"qos"`         // Quality of Service level (0 or 1)
	Username  string   `json:"username"`    // MQTT username for authentication (optional)
	Password  string   `json:"password"`    // MQTT password for authentication (optional)
	TLSCAFile string   `json:"tls_ca_file"` // Path to CA certificate for TLS verification (optional)
}

// EntropyPool contains entropy pool configuration
type EntropyPool struct {
	PoolMinBytes   int    `json:"pool_min_bytes"`  // Minimum pool size in bytes
	PoolMaxBytes   int    `json:"pool_max_bytes"`  // Maximum pool size in bytes
	ReadyMinBytes  int    `json:"ready_min_bytes"` // Minimum bytes before ready
	HTTPAddr       string `json:"http_addr"`       // HTTP server address
	RetryAfterSec  int    `json:"retry_after_sec"`
	AllowPublic    bool   `json:"allow_public"`
	RateLimitRPS   int    `json:"rate_limit_rps"`   // Rate limit: requests per second (default: 25)
	RateLimitBurst int    `json:"rate_limit_burst"` // Rate limit: burst allowance (default: 25)
	TLSEnabled     bool   `json:"tls_enabled"`      // Enable TLS for HTTP server
	TLSCertFile    string `json:"tls_cert_file"`    // Path to server certificate for TLS
	TLSKeyFile     string `json:"tls_key_file"`     // Path to server private key for TLS
	TLSCAFile      string `json:"tls_ca_file"`      // Path to CA certificate for mTLS client verification (optional)
	TLSClientAuth  string `json:"tls_client_auth"`  // mTLS client auth mode: "none", "request", "require" (default: "none")
}

// Collector contains batch collector configuration
type Collector struct {
	BatchSize int `json:"batch_size"` // Number of events per batch (1840-184000)
}

// Metrics contains Prometheus metrics server configuration
type Metrics struct {
	Port          int    `json:"port"`            // HTTP port for /api/v1/metrics endpoint (deprecated, use Bind)
	Bind          string `json:"bind"`            // Bind address for metrics server (e.g., "127.0.0.1:8080")
	Enabled       bool   `json:"enabled"`         // Enable metrics server
	TLSEnabled    bool   `json:"tls_enabled"`     // Enable TLS for metrics server
	TLSCertFile   string `json:"tls_cert_file"`   // Path to server certificate for TLS
	TLSKeyFile    string `json:"tls_key_file"`    // Path to server private key for TLS
	TLSCAFile     string `json:"tls_ca_file"`     // Path to CA certificate for mTLS client verification (optional)
	TLSClientAuth string `json:"tls_client_auth"` // mTLS client auth mode: "none", "request", "require" (default: "none")
}

// Entropy contains HTTP entropy server configuration
type Entropy struct {
	Bind string `json:"bind"` // Bind address for entropy API server (e.g., "127.0.0.1:8081")
}

// GRPC contains gRPC server configuration
type GRPC struct {
	Enabled       bool   `json:"enabled"`         // Enable gRPC server
	Bind          string `json:"bind"`            // Bind address for gRPC server (e.g., "127.0.0.1:9090")
	TLSEnabled    bool   `json:"tls_enabled"`     // Enable TLS for gRPC server
	TLSCertFile   string `json:"tls_cert_file"`   // Path to server certificate for TLS
	TLSKeyFile    string `json:"tls_key_file"`    // Path to server private key for TLS
	TLSCAFile     string `json:"tls_ca_file"`     // Path to CA certificate for mTLS client verification (optional)
	TLSClientAuth string `json:"tls_client_auth"` // mTLS client auth mode: "none", "request", "require" (default: "none")
}

// CloudForwarder contains gRPC client configuration for forwarding batches to cloud
type CloudForwarder struct {
	Enabled              bool          `json:"enabled"`                // Enable cloud forwarder
	ServerAddr           string        `json:"server_addr"`            // gRPC server address (e.g., "cloud.example.com:9090")
	TLSEnabled           bool          `json:"tls_enabled"`            // Enable TLS for gRPC client
	TLSCAFile            string        `json:"tls_ca_file"`            // Path to CA certificate for server verification
	TLSCertFile          string        `json:"tls_cert_file"`          // Path to client certificate for mTLS (optional)
	TLSKeyFile           string        `json:"tls_key_file"`           // Path to client private key for mTLS (optional)
	TLSServerName        string        `json:"tls_server_name"`        // Expected server name for TLS verification (optional)
	ConnectTimeout       int           `json:"connect_timeout"`        // Connection timeout in seconds (default: 10)
	ReconnectDelay       int           `json:"reconnect_delay"`        // Reconnect delay in seconds (default: 5)
	MaxReconnectSec      int           `json:"max_reconnect"`          // Max reconnect delay in seconds (default: 60)
	StreamRotateInterval time.Duration `json:"stream_rotate_interval"` // Interval to rotate the stream (0 disables)

	// Zitadel OAuth2/OIDC configuration for client credentials authentication
	OAuth2Enabled      bool   `json:"oauth2_enabled"`       // Enable OAuth2 client credentials flow
	OAuth2TokenURL     string `json:"oauth2_token_url"`     // OAuth2 token endpoint (e.g., "https://zitadel.example.com/oauth/v2/token")
	OAuth2ClientID     string `json:"oauth2_client_id"`     // OAuth2 client ID
	OAuth2ClientSecret string `json:"oauth2_client_secret"` // OAuth2 client secret
	OAuth2Scopes       string `json:"oauth2_scopes"`        // OAuth2 scopes (space-separated, e.g., "openid profile")
}

// Config holds the complete application configuration.
type Config struct {
	MQTT           MQTT           `json:"mqtt"`            // MQTT broker configuration
	EntropyPool    EntropyPool    `json:"entropy_pool"`    // Entropy pool configuration
	Collector      Collector      `json:"collector"`       // Batch collector configuration
	Metrics        Metrics        `json:"metrics"`         // Metrics server configuration
	Entropy        Entropy        `json:"entropy"`         // Entropy API server configuration
	GRPC           GRPC           `json:"grpc"`            // gRPC server configuration
	CloudForwarder CloudForwarder `json:"cloud_forwarder"` // Cloud forwarder gRPC client configuration
	Environment    string         `json:"environment"`     // Runtime environment ("dev" or "prod")
}

// Load reads configuration from environment variables and returns a validated Config.
// It applies defaults first, then overrides with environment variables.
// Returns an error if the required configuration is missing or invalid.
func Load() (Config, error) {
	// Initialize with safe defaults
	configuration := Config{
		MQTT: MQTT{
			BrokerURL: "tcp://127.0.0.1:1883",
			ClientID:  "",
			Topics:    []string{"timestamps/channel/#"},
			QoS:       0,
		},
		EntropyPool: EntropyPool{
			PoolMinBytes:  defaultPoolMinBytes,
			PoolMaxBytes:  defaultPoolMaxBytes,
			ReadyMinBytes: defaultReadyMinBytes,
			HTTPAddr:      defaultHTTPAddr,
			RetryAfterSec: defaultRetryAfterSeconds,
		},
		Collector: Collector{
			BatchSize: defaultBatchSize,
		},
		Metrics: Metrics{
			Port:    defaultMetricsPort,
			Bind:    "127.0.0.1:8080", // Default to localhost only
			Enabled: true,
		},
		Entropy: Entropy{
			Bind: "127.0.0.1:8081", // Default to localhost only
		},
		GRPC: GRPC{
			Enabled: false,            // Disabled by default for backward compatibility
			Bind:    "127.0.0.1:9090", // Default to localhost only
		},
		CloudForwarder: CloudForwarder{
			Enabled:              false, // Disabled by default
			ConnectTimeout:       10,    // 10 seconds
			ReconnectDelay:       5,     // 5 seconds
			MaxReconnectSec:      60,    // 60 seconds max
			StreamRotateInterval: 0,
		},
		Environment: EnvironmentDevelopment, // Default to development
	}

	// Apply environment variable overrides
	if err := applyMQTTEnvVars(&configuration); err != nil {
		return configuration, err
	}
	if err := applyEntropyPoolEnvVars(&configuration); err != nil {
		return configuration, err
	}
	if err := applyCollectorEnvVars(&configuration); err != nil {
		return configuration, err
	}
	if err := applyMetricsEnvVars(&configuration); err != nil {
		return configuration, err
	}
	if err := applyEntropyEnvVars(&configuration); err != nil {
		return configuration, err
	}
	if err := applyGRPCEnvVars(&configuration); err != nil {
		return configuration, err
	}
	if err := applyCloudForwarderEnvVars(&configuration); err != nil {
		return configuration, err
	}
	if err := applyEnvironmentEnvVars(&configuration); err != nil {
		return configuration, err
	}

	// Validate final configuration
	if err := validate(&configuration); err != nil {
		return configuration, err
	}

	return configuration, nil
}

// applyMQTTEnvVars reads MQTT environment variables and applies them to the provided configuration.
// MQTT_BROKER_URL picks the broker, MQTT_CLIENT_ID overrides the identifier,
// MQTT_TOPICS controls the subscription filters (comma-separated), and MQTT_QOS clamps QoS to 0 or 1.
func applyMQTTEnvVars(configuration *Config) error {
	if v := os.Getenv("MQTT_BROKER_URL"); v != "" {
		configuration.MQTT.BrokerURL = v
	}

	if v := os.Getenv("MQTT_CLIENT_ID"); v != "" {
		configuration.MQTT.ClientID = v
	}

	if v := os.Getenv("MQTT_TOPICS"); v != "" {
		topics := strings.Split(v, ",")
		var cleanTopics []string
		for _, topic := range topics {
			trimmed := strings.TrimSpace(topic)
			if trimmed != "" {
				cleanTopics = append(cleanTopics, trimmed)
			}
		}
		if len(cleanTopics) > 0 {
			configuration.MQTT.Topics = cleanTopics
		}
	}

	if v := os.Getenv("MQTT_QOS"); v != "" {
		// Trim whitespace and handle inline comments (e.g., "0  # comment")
		cleaned := strings.TrimSpace(v)
		// Strip inline comments after the value
		if idx := strings.Index(cleaned, "#"); idx >= 0 {
			cleaned = strings.TrimSpace(cleaned[:idx])
		}

		qos, err := strconv.Atoi(cleaned)
		if err != nil {
			return errors.New("config: MQTT_QOS must be a number (0 or 1)")
		}
		// Clamp QoS to valid range [0, 1]
		if qos < 0 {
			qos = 0
		}
		if qos > 1 {
			qos = 1
		}
		configuration.MQTT.QoS = byte(qos)
	}

	// MQTT authentication (TLS mode)
	if v := os.Getenv("MQTT_USERNAME"); v != "" {
		configuration.MQTT.Username = v
	}

	if v := os.Getenv("MQTT_PASSWORD"); v != "" {
		configuration.MQTT.Password = v
	}

	// Read password from file if MQTT_PASSWORD_FILE is set (more secure)
	if passwordFile := os.Getenv("MQTT_PASSWORD_FILE"); passwordFile != "" {
		passwordBytes, err := readSecretFile(passwordFile)
		if err != nil {
			return fmt.Errorf("config: failed to read MQTT_PASSWORD_FILE: %w", err)
		}
		configuration.MQTT.Password = strings.TrimSpace(string(passwordBytes))
	}

	// MQTT TLS CA certificate
	if v := os.Getenv("MQTT_TLS_CA_FILE"); v != "" {
		configuration.MQTT.TLSCAFile = v
	}

	return nil
}

func readSecretFile(path string) ([]byte, error) {
	absPath, err := sanitizeAbsolutePath(path)
	if err != nil {
		return nil, err
	}
	return readFileWithinRoot(absPath)
}

func sanitizeAbsolutePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("config: empty file path")
	}
	clean := filepath.Clean(path)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("config: resolve path %q: %w", path, err)
	}
	return abs, nil
}

func readFileWithinRoot(absPath string) ([]byte, error) {
	dir := filepath.Dir(absPath)
	base := filepath.Base(absPath)
	f, err := os.OpenInRoot(dir, base)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("error closing file: %v", err)
		}
	}()
	return io.ReadAll(f)
}

// applyEntropyPoolEnvVars reads entropy pool environment variables
func applyEntropyPoolEnvVars(configuration *Config) error {
	configuration.EntropyPool.PoolMinBytes = ParsePositiveEnvInt("ENTROPY_POOL_MIN_BYTES", configuration.EntropyPool.PoolMinBytes)
	configuration.EntropyPool.PoolMaxBytes = ParsePositiveEnvInt("ENTROPY_POOL_MAX_BYTES", configuration.EntropyPool.PoolMaxBytes)
	configuration.EntropyPool.ReadyMinBytes = ParsePositiveEnvInt("ENTROPY_READY_MIN_BYTES", configuration.EntropyPool.ReadyMinBytes)
	configuration.EntropyPool.HTTPAddr = GetEnvDefault("ENTROPY_HTTP_ADDR", configuration.EntropyPool.HTTPAddr)
	configuration.EntropyPool.RetryAfterSec = ParsePositiveEnvInt("ENTROPY_RETRY_AFTER_SEC", configuration.EntropyPool.RetryAfterSec)
	configuration.EntropyPool.AllowPublic = ParseBoolEnv("ALLOW_PUBLIC_HTTP", configuration.EntropyPool.AllowPublic)
	configuration.EntropyPool.RateLimitRPS = ParsePositiveEnvInt("ENTROPY_RATE_LIMIT_RPS", 25)     // Default 25 req/s
	configuration.EntropyPool.RateLimitBurst = ParsePositiveEnvInt("ENTROPY_RATE_LIMIT_BURST", 25) // Default 25 burst

	// Entropy TLS configuration
	configuration.EntropyPool.TLSEnabled = ParseBoolEnv("ENTROPY_TLS_ENABLED", configuration.EntropyPool.TLSEnabled)

	// Use component-specific TLS files if set, otherwise fall back to shared TLS_* variables
	configuration.EntropyPool.TLSCertFile = GetEnvDefault("ENTROPY_TLS_CERT_FILE", os.Getenv("TLS_CERT_FILE"))
	configuration.EntropyPool.TLSKeyFile = GetEnvDefault("ENTROPY_TLS_KEY_FILE", os.Getenv("TLS_KEY_FILE"))
	configuration.EntropyPool.TLSCAFile = GetEnvDefault("ENTROPY_TLS_CA_FILE", os.Getenv("TLS_CA_FILE"))

	if v := os.Getenv("ENTROPY_TLS_CLIENT_AUTH"); v != "" {
		configuration.EntropyPool.TLSClientAuth = strings.ToLower(strings.TrimSpace(v))
	} else {
		configuration.EntropyPool.TLSClientAuth = "none" // Default to no client auth
	}

	// Validate and adjust pool sizes
	if configuration.EntropyPool.PoolMaxBytes < configuration.EntropyPool.PoolMinBytes {
		log.Printf("config: ENTROPY_POOL_MAX_BYTES (%d) lower than min (%d), adjusting to min",
			configuration.EntropyPool.PoolMaxBytes, configuration.EntropyPool.PoolMinBytes)
		configuration.EntropyPool.PoolMaxBytes = configuration.EntropyPool.PoolMinBytes
	}

	return nil
}

// applyCollectorEnvVars reads batch collector environment variables and validates batch size.
// COLLECTOR_BATCH_SIZE must be between minBatchSize (1840) and maxBatchSize (184000).
// Values outside this range are clamped with a warning log.
func applyCollectorEnvVars(configuration *Config) error {
	if v := os.Getenv("COLLECTOR_BATCH_SIZE"); v != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			log.Printf("config: COLLECTOR_BATCH_SIZE invalid (%q), using default %d", v, defaultBatchSize)
			configuration.Collector.BatchSize = defaultBatchSize
			return nil
		}

		// Clamp to valid range
		if parsed < minBatchSize {
			log.Printf("config: COLLECTOR_BATCH_SIZE (%d) below minimum (%d), clamping to min", parsed, minBatchSize)
			configuration.Collector.BatchSize = minBatchSize
		} else if parsed > maxBatchSize {
			log.Printf("config: COLLECTOR_BATCH_SIZE (%d) above maximum (%d), clamping to max", parsed, maxBatchSize)
			configuration.Collector.BatchSize = maxBatchSize
		} else {
			configuration.Collector.BatchSize = parsed
		}
	}

	return nil
}

// applyMetricsEnvVars reads Prometheus metrics server environment variables
func applyMetricsEnvVars(configuration *Config) error {
	configuration.Metrics.Port = ParsePositiveEnvInt("METRICS_PORT", configuration.Metrics.Port)
	configuration.Metrics.Bind = GetEnvDefault("METRICS_BIND", configuration.Metrics.Bind)
	configuration.Metrics.Enabled = ParseBoolEnv("METRICS_ENABLED", configuration.Metrics.Enabled)

	// Metrics TLS configuration
	configuration.Metrics.TLSEnabled = ParseBoolEnv("METRICS_TLS_ENABLED", configuration.Metrics.TLSEnabled)

	// Use component-specific TLS files if set, otherwise fall back to shared TLS_* variables
	configuration.Metrics.TLSCertFile = GetEnvDefault("METRICS_TLS_CERT_FILE", os.Getenv("TLS_CERT_FILE"))
	configuration.Metrics.TLSKeyFile = GetEnvDefault("METRICS_TLS_KEY_FILE", os.Getenv("TLS_KEY_FILE"))
	configuration.Metrics.TLSCAFile = GetEnvDefault("METRICS_TLS_CA_FILE", os.Getenv("TLS_CA_FILE"))

	if v := os.Getenv("METRICS_TLS_CLIENT_AUTH"); v != "" {
		configuration.Metrics.TLSClientAuth = strings.ToLower(strings.TrimSpace(v))
	} else {
		configuration.Metrics.TLSClientAuth = "none"
	}

	return nil
}

// applyEntropyEnvVars reads entropy API server environment variables
func applyEntropyEnvVars(configuration *Config) error {
	configuration.Entropy.Bind = GetEnvDefault("ENTROPY_BIND", configuration.Entropy.Bind)
	return nil
}

// applyGRPCEnvVars reads gRPC server environment variables
func applyGRPCEnvVars(configuration *Config) error {
	configuration.GRPC.Enabled = ParseBoolEnv("GRPC_ENABLED", configuration.GRPC.Enabled)
	configuration.GRPC.Bind = GetEnvDefault("GRPC_BIND", configuration.GRPC.Bind)

	// gRPC TLS configuration
	configuration.GRPC.TLSEnabled = ParseBoolEnv("GRPC_TLS_ENABLED", configuration.GRPC.TLSEnabled)

	if v := os.Getenv("GRPC_TLS_CERT_FILE"); v != "" {
		configuration.GRPC.TLSCertFile = v
	}

	if v := os.Getenv("GRPC_TLS_KEY_FILE"); v != "" {
		configuration.GRPC.TLSKeyFile = v
	}

	if v := os.Getenv("GRPC_TLS_CA_FILE"); v != "" {
		configuration.GRPC.TLSCAFile = v
	}

	if v := os.Getenv("GRPC_TLS_CLIENT_AUTH"); v != "" {
		configuration.GRPC.TLSClientAuth = strings.ToLower(strings.TrimSpace(v))
	} else {
		configuration.GRPC.TLSClientAuth = "none" // Default to no client auth
	}

	return nil
}

// applyCloudForwarderEnvVars reads cloud forwarder gRPC client environment variables
func applyCloudForwarderEnvVars(configuration *Config) error {
	configuration.CloudForwarder.Enabled = ParseBoolEnv("CLOUD_FORWARDER_ENABLED", configuration.CloudForwarder.Enabled)
	configuration.CloudForwarder.ServerAddr = GetEnvDefault("CLOUD_FORWARDER_SERVER_ADDR", configuration.CloudForwarder.ServerAddr)

	// TLS configuration
	configuration.CloudForwarder.TLSEnabled = ParseBoolEnv("CLOUD_FORWARDER_TLS_ENABLED", configuration.CloudForwarder.TLSEnabled)

	// Use component-specific TLS files if set, otherwise fall back to shared TLS_* variables
	configuration.CloudForwarder.TLSCAFile = GetEnvDefault("CLOUD_FORWARDER_TLS_CA_FILE", os.Getenv("TLS_CA_FILE"))
	configuration.CloudForwarder.TLSCertFile = GetEnvDefault("CLOUD_FORWARDER_TLS_CERT_FILE", os.Getenv("TLS_CERT_FILE"))
	configuration.CloudForwarder.TLSKeyFile = GetEnvDefault("CLOUD_FORWARDER_TLS_KEY_FILE", os.Getenv("TLS_KEY_FILE"))

	if v := os.Getenv("CLOUD_FORWARDER_TLS_SERVER_NAME"); v != "" {
		configuration.CloudForwarder.TLSServerName = v
	}

	// Timeouts and reconnect configuration
	configuration.CloudForwarder.ConnectTimeout = ParsePositiveEnvInt("CLOUD_FORWARDER_CONNECT_TIMEOUT", configuration.CloudForwarder.ConnectTimeout)
	configuration.CloudForwarder.ReconnectDelay = ParsePositiveEnvInt("CLOUD_FORWARDER_RECONNECT_DELAY", configuration.CloudForwarder.ReconnectDelay)
	configuration.CloudForwarder.MaxReconnectSec = ParsePositiveEnvInt("CLOUD_FORWARDER_MAX_RECONNECT", configuration.CloudForwarder.MaxReconnectSec)
	configuration.CloudForwarder.StreamRotateInterval = ParseDurationEnv(
		"CLOUD_FORWARDER_STREAM_ROTATE_INTERVAL",
		configuration.CloudForwarder.StreamRotateInterval,
	)

	// OAuth2/Zitadel configuration
	configuration.CloudForwarder.OAuth2Enabled = ParseBoolEnv("CLOUD_FORWARDER_OAUTH2_ENABLED", configuration.CloudForwarder.OAuth2Enabled)

	if v := os.Getenv("CLOUD_FORWARDER_OAUTH2_TOKEN_URL"); v != "" {
		configuration.CloudForwarder.OAuth2TokenURL = v
	}

	if v := os.Getenv("CLOUD_FORWARDER_OAUTH2_CLIENT_ID"); v != "" {
		configuration.CloudForwarder.OAuth2ClientID = v
	}

	if v := os.Getenv("CLOUD_FORWARDER_OAUTH2_CLIENT_SECRET"); v != "" {
		configuration.CloudForwarder.OAuth2ClientSecret = v
	}

	// Read client secret from file if provided (more secure)
	if secretFile := os.Getenv("CLOUD_FORWARDER_OAUTH2_CLIENT_SECRET_FILE"); secretFile != "" {
		secretBytes, err := readSecretFile(secretFile)
		if err != nil {
			return fmt.Errorf("config: failed to read CLOUD_FORWARDER_OAUTH2_CLIENT_SECRET_FILE: %w", err)
		}
		configuration.CloudForwarder.OAuth2ClientSecret = strings.TrimSpace(string(secretBytes))
	}

	if v := os.Getenv("CLOUD_FORWARDER_OAUTH2_SCOPES"); v != "" {
		configuration.CloudForwarder.OAuth2Scopes = v
	}

	return nil
}

// applyEnvironmentEnvVars normalizes ENVIRONMENT into "dev" or "prod".
// Valid inputs are "dev"/"development" and "prod"/"production"; other values error out.
func applyEnvironmentEnvVars(configuration *Config) error {
	if v := os.Getenv("ENVIRONMENT"); v != "" {
		env := strings.ToLower(strings.TrimSpace(v))

		// Normalize environment values
		switch env {
		case "dev", "development":
			configuration.Environment = EnvironmentDevelopment
		case "prod", "production":
			configuration.Environment = EnvironmentProduction
		default:
			return errors.New("config: ENVIRONMENT must be 'dev' or 'prod'")
		}
	}

	return nil
}

// validate checks that required configuration fields are present and valid.
// Returns an error if any validation fails.
func validate(configuration *Config) error {
	// MQTT validation
	if configuration.MQTT.BrokerURL == "" {
		return errors.New("config: MQTT_BROKER_URL is required")
	}
	if len(configuration.MQTT.Topics) == 0 {
		return errors.New("config: MQTT_TOPICS is required (at least one topic)")
	}

	// Environment validation
	if configuration.Environment != EnvironmentDevelopment && configuration.Environment != EnvironmentProduction {
		return errors.New("config: environment must be 'dev' or 'prod'")
	}

	// Entropy TLS validation
	if configuration.EntropyPool.TLSEnabled {
		if configuration.EntropyPool.TLSCertFile == "" {
			return errors.New("config: ENTROPY_TLS_CERT_FILE is required when ENTROPY_TLS_ENABLED=true")
		}
		if configuration.EntropyPool.TLSKeyFile == "" {
			return errors.New("config: ENTROPY_TLS_KEY_FILE is required when ENTROPY_TLS_ENABLED=true")
		}

		// Validate TLS client auth mode
		validClientAuthModes := map[string]bool{
			"none":    true,
			"request": true,
			"require": true,
		}
		if !validClientAuthModes[configuration.EntropyPool.TLSClientAuth] {
			return fmt.Errorf("config: ENTROPY_TLS_CLIENT_AUTH must be 'none', 'request', or 'require', got %q", configuration.EntropyPool.TLSClientAuth)
		}

		// If mTLS is enabled, CA file is required
		if configuration.EntropyPool.TLSClientAuth == "require" && configuration.EntropyPool.TLSCAFile == "" {
			return errors.New("config: ENTROPY_TLS_CA_FILE is required when ENTROPY_TLS_CLIENT_AUTH=require")
		}
	}

	// SECURITY: Warn if public HTTP is enabled without TLS in production
	if configuration.EntropyPool.AllowPublic && !configuration.EntropyPool.TLSEnabled {
		if configuration.IsProduction() {
			return errors.New("config: SECURITY: TLS is required when ALLOW_PUBLIC_HTTP=true in production mode")
		}
		log.Printf("WARNING: Running public HTTP without TLS in development mode - this is insecure!")
	}

	// Metrics TLS validation
	if configuration.Metrics.TLSEnabled {
		if configuration.Metrics.TLSCertFile == "" {
			return errors.New("config: METRICS_TLS_CERT_FILE is required when METRICS_TLS_ENABLED=true")
		}
		if configuration.Metrics.TLSKeyFile == "" {
			return errors.New("config: METRICS_TLS_KEY_FILE is required when METRICS_TLS_ENABLED=true")
		}

		// Validate TLS client auth mode
		validClientAuthModes := map[string]bool{
			"none":    true,
			"request": true,
			"require": true,
		}
		if !validClientAuthModes[configuration.Metrics.TLSClientAuth] {
			return fmt.Errorf("config: METRICS_TLS_CLIENT_AUTH must be 'none', 'request', or 'require', got %q", configuration.Metrics.TLSClientAuth)
		}

		// If mTLS is enabled, CA file is required
		if configuration.Metrics.TLSClientAuth == "require" && configuration.Metrics.TLSCAFile == "" {
			return errors.New("config: METRICS_TLS_CA_FILE is required when METRICS_TLS_CLIENT_AUTH=require")
		}
	}

	// gRPC TLS validation
	if configuration.GRPC.Enabled && configuration.GRPC.TLSEnabled {
		if configuration.GRPC.TLSCertFile == "" {
			return errors.New("config: GRPC_TLS_CERT_FILE is required when GRPC_TLS_ENABLED=true")
		}
		if configuration.GRPC.TLSKeyFile == "" {
			return errors.New("config: GRPC_TLS_KEY_FILE is required when GRPC_TLS_ENABLED=true")
		}

		// Validate TLS client auth mode
		validClientAuthModes := map[string]bool{
			"none":    true,
			"request": true,
			"require": true,
		}
		if !validClientAuthModes[configuration.GRPC.TLSClientAuth] {
			return fmt.Errorf("config: GRPC_TLS_CLIENT_AUTH must be 'none', 'request', or 'require', got %q", configuration.GRPC.TLSClientAuth)
		}

		// If mTLS is enabled, CA file is required
		if configuration.GRPC.TLSClientAuth == "require" && configuration.GRPC.TLSCAFile == "" {
			return errors.New("config: GRPC_TLS_CA_FILE is required when GRPC_TLS_CLIENT_AUTH=require")
		}
	}

	// CloudForwarder TLS validation
	if configuration.CloudForwarder.Enabled {
		if configuration.CloudForwarder.ServerAddr == "" {
			return errors.New("config: CLOUD_FORWARDER_SERVER_ADDR is required when CLOUD_FORWARDER_ENABLED=true")
		}

		if configuration.CloudForwarder.TLSEnabled {
			if configuration.CloudForwarder.TLSCAFile == "" {
				return errors.New("config: CLOUD_FORWARDER_TLS_CA_FILE is required when CLOUD_FORWARDER_TLS_ENABLED=true")
			}

			// If mTLS is configured (both cert and key), validate both are present
			hasCert := configuration.CloudForwarder.TLSCertFile != ""
			hasKey := configuration.CloudForwarder.TLSKeyFile != ""
			if hasCert != hasKey {
				return errors.New("config: CLOUD_FORWARDER_TLS_CERT_FILE and CLOUD_FORWARDER_TLS_KEY_FILE must both be set or both be empty")
			}
		}

		// OAuth2 validation
		if configuration.CloudForwarder.OAuth2Enabled {
			if configuration.CloudForwarder.OAuth2TokenURL == "" {
				return errors.New("config: CLOUD_FORWARDER_OAUTH2_TOKEN_URL is required when CLOUD_FORWARDER_OAUTH2_ENABLED=true")
			}
			if configuration.CloudForwarder.OAuth2ClientID == "" {
				return errors.New("config: CLOUD_FORWARDER_OAUTH2_CLIENT_ID is required when CLOUD_FORWARDER_OAUTH2_ENABLED=true")
			}
			if configuration.CloudForwarder.OAuth2ClientSecret == "" {
				return errors.New("config: CLOUD_FORWARDER_OAUTH2_CLIENT_SECRET is required when CLOUD_FORWARDER_OAUTH2_ENABLED=true")
			}
		}
	}

	return nil
}

// IsProduction returns true if the application is running in production mode.
func (cfg *Config) IsProduction() bool {
	return cfg.Environment == EnvironmentProduction
}

// IsDevelopment returns true if the application is running in development mode.
func (cfg *Config) IsDevelopment() bool {
	return cfg.Environment == EnvironmentDevelopment
}

// String returns a human-readable representation of the configuration.
func (cfg *Config) String() string {
	return "Config{" +
		"Environment=" + cfg.Environment +
		", MQTT.BrokerURL=" + cfg.MQTT.BrokerURL +
		", MQTT.Topics=" + strings.Join(cfg.MQTT.Topics, ",") +
		"}"
}

// cleanEnvValue removes inline comments and trims whitespace from environment variable values.
// This handles systemd EnvironmentFile format where inline comments are included in the value.
// Example: "127.0.0.1:8080 # bind address" becomes "127.0.0.1:8080"
func cleanEnvValue(value string) string {
	cleaned := strings.TrimSpace(value)
	// Strip inline comments after the value
	if idx := strings.Index(cleaned, "#"); idx >= 0 {
		cleaned = strings.TrimSpace(cleaned[:idx])
	}
	return cleaned
}

// GetEnvDefault retrieves an environment variable or returns a fallback value.
// Empty or whitespace-only values are treated as unset.
// Inline comments (e.g., "value # comment") are stripped.
func GetEnvDefault(key string, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		cleaned := cleanEnvValue(value)
		if cleaned != "" {
			return cleaned
		}
	}
	return fallback
}

// ParsePositiveEnvInt reads an integer environment variable with validation.
// Returns the fallback if the variable is unset, invalid, or non-positive.
// Invalid or non-positive values are logged before falling back.
// Inline comments (e.g., "512 # comment") are stripped.
func ParsePositiveEnvInt(key string, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	cleaned := cleanEnvValue(value)
	if cleaned == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(cleaned)
	if err != nil {
		log.Printf("config: %s invalid (%q), using fallback %d", key, value, fallback)
		return fallback
	}
	if parsed <= 0 {
		log.Printf("config: %s non-positive (%d), using fallback %d", key, parsed, fallback)
		return fallback
	}
	return parsed
}

// ParseDurationEnv reads a duration environment variable with validation.
// Values must include a unit suffix (e.g., "500ms", "30s", "5m").
// Returns the fallback if the variable is unset, invalid, or negative.
// Inline comments (e.g., "5s # comment") are stripped.
func ParseDurationEnv(key string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	cleaned := cleanEnvValue(value)
	if cleaned == "" {
		return fallback
	}
	hasUnit := false
	for i := 0; i < len(cleaned); i++ {
		ch := cleaned[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
			hasUnit = true
			break
		}
	}
	if !hasUnit {
		log.Printf("config: %s missing duration unit (%q), using fallback %s", key, value, fallback)
		return fallback
	}
	parsed, err := time.ParseDuration(cleaned)
	if err != nil {
		log.Printf("config: %s invalid (%q), using fallback %s", key, value, fallback)
		return fallback
	}
	if parsed < 0 {
		log.Printf("config: %s negative (%s), using fallback %s", key, parsed, fallback)
		return fallback
	}
	return parsed
}

// ParseBoolEnv interprets typical boolean environment values (true/false, 1/0, yes/no).
// Inline comments (e.g., "true # enable feature") are stripped.
func ParseBoolEnv(key string, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	cleaned := cleanEnvValue(value)
	if cleaned == "" {
		return fallback
	}
	trimmed := strings.ToLower(cleaned)
	switch trimmed {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		log.Printf("config: %s has unrecognised boolean value %q, using fallback %v", key, value, fallback)
		return fallback
	}
}
