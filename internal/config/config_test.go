package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test constants to replace magic numbers
const (
	testPort           = 9999
	testMetricsPort    = 8080
	testTimeout        = 5
	testPoolMinBytes   = 128
	testPoolMaxBytes   = 256
	testReadyMinBytes  = 64
	testRateLimitRPS   = 50
	testRateLimitBurst = 100
	testBatchSize      = 5000
)

// validConfig returns a factory function for creating valid configs
func validConfig() Config {
	return Config{
		MQTT: MQTT{
			BrokerURL: "tcp://127.0.0.1:1883",
			Topics:    []string{"sr90/tdc/#"},
			QoS:       0,
		},
		Environment: EnvironmentDevelopment,
		EntropyPool: EntropyPool{
			PoolMinBytes:  defaultPoolMinBytes,
			PoolMaxBytes:  defaultPoolMaxBytes,
			ReadyMinBytes: defaultReadyMinBytes,
			HTTPAddr:      defaultHTTPAddr,
		},
		Collector: Collector{
			BatchSize: defaultBatchSize,
		},
		Metrics: Metrics{
			Port:    defaultMetricsPort,
			Bind:    "127.0.0.1:8080",
			Enabled: true,
		},
	}
}

func TestConfig_Defaults(t *testing.T) {
	keys := []string{
		"MQTT_BROKER_URL",
		"MQTT_CLIENT_ID",
		"MQTT_TOPICS",
		"MQTT_QOS",
		"ENVIRONMENT",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.MQTT.BrokerURL != "tcp://127.0.0.1:1883" {
		t.Fatalf("BrokerURL default = %s, want tcp://127.0.0.1:1883", cfg.MQTT.BrokerURL)
	}
	if strings.Join(cfg.MQTT.Topics, ",") != "timestamps/channel/#" {
		t.Fatalf("Topic default = %s, want timestamps/channel/#", strings.Join(cfg.MQTT.Topics, ","))
	}
	if cfg.MQTT.QoS != 0 {
		t.Fatalf("QoS default = %d, want 0", cfg.MQTT.QoS)
	}
	if cfg.Environment != EnvironmentDevelopment {
		t.Fatalf("Environment default = %s, want %s", cfg.Environment, EnvironmentDevelopment)
	}
}

func TestConfig_FromEnv(t *testing.T) {
	t.Setenv("MQTT_BROKER_URL", "tcp://custom:1883")
	t.Setenv("MQTT_CLIENT_ID", "client-1")
	t.Setenv("MQTT_TOPICS", "custom/topic")
	t.Setenv("MQTT_QOS", "2")
	t.Setenv("ENVIRONMENT", "production")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.MQTT.BrokerURL != "tcp://custom:1883" {
		t.Fatalf("BrokerURL = %s, want tcp://custom:1883", cfg.MQTT.BrokerURL)
	}
	if cfg.MQTT.ClientID != "client-1" {
		t.Fatalf("ClientID = %s, want client-1", cfg.MQTT.ClientID)
	}
	if strings.Join(cfg.MQTT.Topics, ",") != "custom/topic" {
		t.Fatalf("Topic = %s, want custom/topic", strings.Join(cfg.MQTT.Topics, ","))
	}
	if cfg.MQTT.QoS != 1 {
		t.Fatalf("QoS = %d, want 1 (clamped)", cfg.MQTT.QoS)
	}
	if cfg.Environment != EnvironmentProduction {
		t.Fatalf("Environment = %s, want %s", cfg.Environment, EnvironmentProduction)
	}
	if cfg.EntropyPool.RetryAfterSec != defaultRetryAfterSeconds {
		t.Fatalf("RetryAfterSec = %d, want %d", cfg.EntropyPool.RetryAfterSec, defaultRetryAfterSeconds)
	}
}

func TestConfig_Validation(t *testing.T) {
	valid := func() Config {
		return Config{
			MQTT: MQTT{
				BrokerURL: "tcp://127.0.0.1:1883",
				Topics:    []string{"sr90/tdc/#"},
				QoS:       0,
			},
			Environment: EnvironmentDevelopment,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "missing broker",
			mutate: func(cfg *Config) {
				cfg.MQTT.BrokerURL = ""
			},
			wantErr: "MQTT_BROKER_URL",
		},
		{
			name: "missing topic",
			mutate: func(cfg *Config) {
				cfg.MQTT.Topics = []string{}
			},
			wantErr: "MQTT_TOPIC",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid()
			tc.mutate(&cfg)

			if err := validate(&cfg); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validate error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestConfig_Priority(t *testing.T) {
	cfg := Config{
		MQTT:        MQTT{BrokerURL: "tcp://flag:1883"},
		Environment: EnvironmentDevelopment,
	}

	t.Setenv("MQTT_BROKER_URL", "tcp://env:1883")
	t.Setenv("ENVIRONMENT", "production")

	if err := applyMQTTEnvVars(&cfg); err != nil {
		t.Fatalf("applyMQTTEnvVars error: %v", err)
	}
	if err := applyEnvironmentEnvVars(&cfg); err != nil {
		t.Fatalf("applyEnvironmentEnvVars error: %v", err)
	}

	if cfg.MQTT.BrokerURL != "tcp://env:1883" {
		t.Fatalf("expected env override, got BrokerURL=%s", cfg.MQTT.BrokerURL)
	}
	if cfg.Environment != EnvironmentProduction {
		t.Fatalf("expected env override, got Environment=%s", cfg.Environment)
	}
}

func TestConfig_IsDevelopmentAndString(t *testing.T) {
	cfg := Config{
		MQTT:        MQTT{BrokerURL: "tcp://host:1883", Topics: []string{"sr90/tdc/#"}},
		Environment: EnvironmentDevelopment,
	}

	if !cfg.IsDevelopment() {
		t.Fatal("expected IsDevelopment to return true in dev mode")
	}
	if cfg.IsProduction() {
		t.Fatal("expected IsProduction to be false in dev mode")
	}

	text := cfg.String()
	if !strings.Contains(text, "MQTT.BrokerURL=tcp://host:1883") {
		t.Fatalf("string output missing broker URL: %s", text)
	}
	if strings.Contains(text, "secret.key") {
		t.Fatalf("string output must redact TLS key: %s", text)
	}
}

func TestConfig_InvalidMQTTQoS(t *testing.T) {
	t.Setenv("MQTT_QOS", "NaN")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MQTT_QOS") {
		t.Fatalf("expected invalid MQTT_QOS error, got %v", err)
	}
}

func TestConfig_NegativeQoS(t *testing.T) {
	t.Setenv("MQTT_QOS", "-5")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.MQTT.QoS != 0 {
		t.Errorf("expected negative QoS to be clamped to 0, got %d", cfg.MQTT.QoS)
	}
}

func TestConfig_ExplicitDevelopmentEnv(t *testing.T) {
	t.Setenv("ENVIRONMENT", "development")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Environment != EnvironmentDevelopment {
		t.Errorf("expected environment=dev for 'development', got %s", cfg.Environment)
	}
}

func TestConfig_ExplicitDevEnv(t *testing.T) {
	t.Setenv("ENVIRONMENT", "dev")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Environment != EnvironmentDevelopment {
		t.Errorf("expected environment=dev for 'dev', got %s", cfg.Environment)
	}
}

func TestConfig_InvalidEnvironment(t *testing.T) {
	t.Setenv("ENVIRONMENT", "staging")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid ENVIRONMENT value")
	}

	if !strings.Contains(err.Error(), "ENVIRONMENT must be 'dev' or 'prod'") {
		t.Errorf("expected error about invalid ENVIRONMENT, got: %v", err)
	}
}

func TestConfig_ValidateInvalidEnvironment(t *testing.T) {
	cfg := Config{
		MQTT: MQTT{
			BrokerURL: "tcp://localhost:1883",
			Topics:    []string{"test/topic"},
		},
		Environment: "invalid", // manually corrupt the environment
	}

	err := validate(&cfg)
	if err == nil {
		t.Fatal("expected validation error for invalid environment")
	}

	if !strings.Contains(err.Error(), "environment must be 'dev' or 'prod'") {
		t.Errorf("expected error about invalid environment, got: %v", err)
	}
}

func TestApplyEntropyPoolEnvVarsAdjustsBounds(t *testing.T) {
	cfg := Config{
		EntropyPool: EntropyPool{
			PoolMinBytes:  64,
			PoolMaxBytes:  256,
			ReadyMinBytes: 32,
			HTTPAddr:      "127.0.0.1:9797",
		},
	}

	t.Setenv("ENTROPY_POOL_MIN_BYTES", "128")
	t.Setenv("ENTROPY_POOL_MAX_BYTES", "64") // intentionally lower than min to cover adjustment
	t.Setenv("ENTROPY_READY_MIN_BYTES", "16")
	t.Setenv("ENTROPY_HTTP_ADDR", "127.0.0.1:9999")
	t.Setenv("ENTROPY_RETRY_AFTER_SEC", "5")
	t.Setenv("ALLOW_PUBLIC_HTTP", "true")

	if err := applyEntropyPoolEnvVars(&cfg); err != nil {
		t.Fatalf("applyEntropyPoolEnvVars error: %v", err)
	}

	if cfg.EntropyPool.PoolMinBytes != 128 {
		t.Fatalf("expected PoolMinBytes=128, got %d", cfg.EntropyPool.PoolMinBytes)
	}
	if cfg.EntropyPool.PoolMaxBytes != 128 {
		t.Fatalf("expected PoolMaxBytes adjusted to 128, got %d", cfg.EntropyPool.PoolMaxBytes)
	}
	if cfg.EntropyPool.ReadyMinBytes != 16 {
		t.Fatalf("expected ReadyMinBytes=16, got %d", cfg.EntropyPool.ReadyMinBytes)
	}
	if cfg.EntropyPool.HTTPAddr != "127.0.0.1:9999" {
		t.Fatalf("expected HTTPAddr override, got %s", cfg.EntropyPool.HTTPAddr)
	}
	if cfg.EntropyPool.RetryAfterSec != 5 {
		t.Fatalf("expected RetryAfterSec=5, got %d", cfg.EntropyPool.RetryAfterSec)
	}
	if !cfg.EntropyPool.AllowPublic {
		t.Fatal("expected AllowPublic to be true")
	}
}

func TestGetEnvDefault(t *testing.T) {
	const key = "CONFIG_GET_ENV_DEFAULT"

	t.Setenv(key, "  ")
	if got := GetEnvDefault(key, "fallback"); got != "fallback" {
		t.Fatalf("expected fallback for whitespace env value, got %q", got)
	}

	t.Setenv(key, "value")
	if got := GetEnvDefault(key, "fallback"); got != "value" {
		t.Fatalf("expected concrete env value, got %q", got)
	}
}

func TestParsePositiveEnvInt(t *testing.T) {
	const key = "CONFIG_PARSE_POSITIVE_INT"

	t.Setenv(key, "")
	if got := ParsePositiveEnvInt(key, 7); got != 7 {
		t.Fatalf("expected fallback for empty env, got %d", got)
	}

	t.Setenv(key, "invalid")
	if got := ParsePositiveEnvInt(key, 9); got != 9 {
		t.Fatalf("expected fallback for invalid env, got %d", got)
	}

	t.Setenv(key, "0")
	if got := ParsePositiveEnvInt(key, 11); got != 11 {
		t.Fatalf("expected fallback for zero, got %d", got)
	}

	t.Setenv(key, "-3")
	if got := ParsePositiveEnvInt(key, 13); got != 13 {
		t.Fatalf("expected fallback for negative, got %d", got)
	}

	t.Setenv(key, "42")
	if got := ParsePositiveEnvInt(key, 15); got != 42 {
		t.Fatalf("expected parsed positive value 42, got %d", got)
	}
}

func TestParseDurationEnv(t *testing.T) {
	const key = "CONFIG_PARSE_DURATION"

	t.Setenv(key, "")
	if got := ParseDurationEnv(key, 5*time.Second); got != 5*time.Second {
		t.Fatalf("expected fallback for empty env, got %s", got)
	}

	t.Setenv(key, "15")
	if got := ParseDurationEnv(key, 7*time.Second); got != 7*time.Second {
		t.Fatalf("expected fallback for missing unit, got %s", got)
	}

	t.Setenv(key, "invalid")
	if got := ParseDurationEnv(key, 9*time.Second); got != 9*time.Second {
		t.Fatalf("expected fallback for invalid env, got %s", got)
	}

	t.Setenv(key, "-3s")
	if got := ParseDurationEnv(key, 11*time.Second); got != 11*time.Second {
		t.Fatalf("expected fallback for negative duration, got %s", got)
	}

	t.Setenv(key, "500ms")
	if got := ParseDurationEnv(key, time.Second); got != 500*time.Millisecond {
		t.Fatalf("expected parsed duration 500ms, got %s", got)
	}
}

func TestParseBoolEnv(t *testing.T) {
	const key = "CONFIG_PARSE_BOOL"

	if got := ParseBoolEnv(key, true); !got {
		t.Fatal("expected fallback true when unset")
	}

	t.Setenv(key, "false")
	if got := ParseBoolEnv(key, true); got {
		t.Fatal("expected false from explicit false")
	}

	t.Setenv(key, "YES")
	if got := ParseBoolEnv(key, false); !got {
		t.Fatal("expected true from YES")
	}

	t.Setenv(key, "maybe")
	if got := ParseBoolEnv(key, true); !got {
		t.Fatal("expected fallback true for unknown value")
	}
}

func TestConfig_TLSConfiguration(t *testing.T) {
	t.Run("TLS CA file from env", func(t *testing.T) {
		t.Setenv("MQTT_TLS_CA_FILE", "/path/to/ca.crt")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.MQTT.TLSCAFile != "/path/to/ca.crt" {
			t.Errorf("expected TLSCAFile=/path/to/ca.crt, got %s", cfg.MQTT.TLSCAFile)
		}
	})

	t.Run("MQTT username and password", func(t *testing.T) {
		t.Setenv("MQTT_USERNAME", "testuser")
		t.Setenv("MQTT_PASSWORD", "testpass")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.MQTT.Username != "testuser" {
			t.Errorf("expected Username=testuser, got %s", cfg.MQTT.Username)
		}
		if cfg.MQTT.Password != "testpass" {
			t.Errorf("expected Password=testpass, got %s", cfg.MQTT.Password)
		}
	})

	t.Run("MQTT password from file", func(t *testing.T) {
		// Create a temporary password file
		tmpDir := t.TempDir()
		passwordFile := filepath.Join(tmpDir, "mqtt_password.txt")
		if err := os.WriteFile(passwordFile, []byte("  secret123  \n"), 0o600); err != nil {
			t.Fatalf("failed to write password file: %v", err)
		}

		t.Setenv("MQTT_PASSWORD_FILE", passwordFile)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.MQTT.Password != "secret123" {
			t.Errorf("expected Password=secret123 (trimmed), got %s", cfg.MQTT.Password)
		}
	})

	t.Run("MQTT password file not found", func(t *testing.T) {
		t.Setenv("MQTT_PASSWORD_FILE", "/nonexistent/path/password.txt")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for missing password file")
		}

		if !strings.Contains(err.Error(), "failed to read MQTT_PASSWORD_FILE") {
			t.Errorf("expected password file error, got: %v", err)
		}
	})

	t.Run("MQTT password file overrides MQTT_PASSWORD", func(t *testing.T) {
		tmpDir := t.TempDir()
		passwordFile := filepath.Join(tmpDir, "mqtt_password.txt")
		if err := os.WriteFile(passwordFile, []byte("file-password"), 0o600); err != nil {
			t.Fatalf("failed to write password file: %v", err)
		}

		t.Setenv("MQTT_PASSWORD", "env-password")
		t.Setenv("MQTT_PASSWORD_FILE", passwordFile)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.MQTT.Password != "file-password" {
			t.Errorf("expected password from file to override env, got %s", cfg.MQTT.Password)
		}
	})
}

func TestConfig_EntropyPoolBoundsValidation(t *testing.T) {
	t.Run("Negative pool min bytes", func(t *testing.T) {
		t.Setenv("ENTROPY_POOL_MIN_BYTES", "-100")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// ParsePositiveEnvInt should return fallback for negative values
		if cfg.EntropyPool.PoolMinBytes != defaultPoolMinBytes {
			t.Errorf("expected default PoolMinBytes for negative value, got %d", cfg.EntropyPool.PoolMinBytes)
		}
	})

	t.Run("Zero pool max bytes", func(t *testing.T) {
		t.Setenv("ENTROPY_POOL_MAX_BYTES", "0")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// Zero should use fallback
		if cfg.EntropyPool.PoolMaxBytes != defaultPoolMaxBytes {
			t.Errorf("expected default PoolMaxBytes for zero value, got %d", cfg.EntropyPool.PoolMaxBytes)
		}
	})

	t.Run("Invalid pool bounds", func(t *testing.T) {
		t.Setenv("ENTROPY_POOL_MIN_BYTES", "not-a-number")
		t.Setenv("ENTROPY_POOL_MAX_BYTES", "also-not-a-number")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// Should use defaults for invalid values
		if cfg.EntropyPool.PoolMinBytes != defaultPoolMinBytes {
			t.Errorf("expected default PoolMinBytes for invalid value, got %d", cfg.EntropyPool.PoolMinBytes)
		}
		if cfg.EntropyPool.PoolMaxBytes != defaultPoolMaxBytes {
			t.Errorf("expected default PoolMaxBytes for invalid value, got %d", cfg.EntropyPool.PoolMaxBytes)
		}
	})

	t.Run("Negative ready min bytes", func(t *testing.T) {
		t.Setenv("ENTROPY_READY_MIN_BYTES", "-50")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.ReadyMinBytes != defaultReadyMinBytes {
			t.Errorf("expected default ReadyMinBytes for negative value, got %d", cfg.EntropyPool.ReadyMinBytes)
		}
	})

	t.Run("Negative retry after seconds", func(t *testing.T) {
		t.Setenv("ENTROPY_RETRY_AFTER_SEC", "-5")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.RetryAfterSec != defaultRetryAfterSeconds {
			t.Errorf("expected default RetryAfterSec for negative value, got %d", cfg.EntropyPool.RetryAfterSec)
		}
	})
}

func TestConfig_RateLimitConfiguration(t *testing.T) {
	t.Run("Rate limit RPS from env", func(t *testing.T) {
		t.Setenv("ENTROPY_RATE_LIMIT_RPS", "50")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.RateLimitRPS != testRateLimitRPS {
			t.Errorf("expected RateLimitRPS=50, got %d", cfg.EntropyPool.RateLimitRPS)
		}
	})

	t.Run("Rate limit burst from env", func(t *testing.T) {
		t.Setenv("ENTROPY_RATE_LIMIT_BURST", "100")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.RateLimitBurst != testRateLimitBurst {
			t.Errorf("expected RateLimitBurst=100, got %d", cfg.EntropyPool.RateLimitBurst)
		}
	})

	t.Run("Rate limit defaults", func(t *testing.T) {
		// Don't set any rate limit env vars
		t.Setenv("ENTROPY_RATE_LIMIT_RPS", "")
		t.Setenv("ENTROPY_RATE_LIMIT_BURST", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// Should use hardcoded defaults (25)
		if cfg.EntropyPool.RateLimitRPS != 25 {
			t.Errorf("expected default RateLimitRPS=25, got %d", cfg.EntropyPool.RateLimitRPS)
		}
		if cfg.EntropyPool.RateLimitBurst != 25 {
			t.Errorf("expected default RateLimitBurst=25, got %d", cfg.EntropyPool.RateLimitBurst)
		}
	})

	t.Run("Negative rate limit values", func(t *testing.T) {
		t.Setenv("ENTROPY_RATE_LIMIT_RPS", "-10")
		t.Setenv("ENTROPY_RATE_LIMIT_BURST", "-20")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// ParsePositiveEnvInt should return default for negative
		if cfg.EntropyPool.RateLimitRPS != 25 {
			t.Errorf("expected default RateLimitRPS for negative, got %d", cfg.EntropyPool.RateLimitRPS)
		}
		if cfg.EntropyPool.RateLimitBurst != 25 {
			t.Errorf("expected default RateLimitBurst for negative, got %d", cfg.EntropyPool.RateLimitBurst)
		}
	})

	t.Run("Invalid rate limit values", func(t *testing.T) {
		t.Setenv("ENTROPY_RATE_LIMIT_RPS", "not-a-number")
		t.Setenv("ENTROPY_RATE_LIMIT_BURST", "invalid")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// Should fall back to defaults
		if cfg.EntropyPool.RateLimitRPS != 25 {
			t.Errorf("expected default RateLimitRPS for invalid, got %d", cfg.EntropyPool.RateLimitRPS)
		}
		if cfg.EntropyPool.RateLimitBurst != 25 {
			t.Errorf("expected default RateLimitBurst for invalid, got %d", cfg.EntropyPool.RateLimitBurst)
		}
	})
}

// ============================================================================
// Collector Batch Size Tests
// ============================================================================

func TestConfig_CollectorBatchSize(t *testing.T) {
	t.Run("Valid batch size", func(t *testing.T) {
		t.Setenv("COLLECTOR_BATCH_SIZE", "5000")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Collector.BatchSize != testBatchSize {
			t.Errorf("expected BatchSize=5000, got %d", cfg.Collector.BatchSize)
		}
	})

	t.Run("Batch size below minimum clamped", func(t *testing.T) {
		t.Setenv("COLLECTOR_BATCH_SIZE", "100")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Collector.BatchSize != minBatchSize {
			t.Errorf("expected BatchSize clamped to min (%d), got %d", minBatchSize, cfg.Collector.BatchSize)
		}
	})

	t.Run("Batch size above maximum clamped", func(t *testing.T) {
		t.Setenv("COLLECTOR_BATCH_SIZE", "999999")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Collector.BatchSize != maxBatchSize {
			t.Errorf("expected BatchSize clamped to max (%d), got %d", maxBatchSize, cfg.Collector.BatchSize)
		}
	})

	t.Run("Invalid batch size uses default", func(t *testing.T) {
		t.Setenv("COLLECTOR_BATCH_SIZE", "not-a-number")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Collector.BatchSize != defaultBatchSize {
			t.Errorf("expected default BatchSize for invalid value, got %d", cfg.Collector.BatchSize)
		}
	})

	t.Run("Empty batch size uses default", func(t *testing.T) {
		t.Setenv("COLLECTOR_BATCH_SIZE", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Collector.BatchSize != defaultBatchSize {
			t.Errorf("expected default BatchSize for empty value, got %d", cfg.Collector.BatchSize)
		}
	})
}

// ============================================================================
// Metrics Configuration Tests
// ============================================================================

func TestConfig_MetricsConfiguration(t *testing.T) {
	t.Run("Metrics port from env", func(t *testing.T) {
		t.Setenv("METRICS_PORT", "9090")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Metrics.Port != 9090 {
			t.Errorf("expected Metrics.Port=9090, got %d", cfg.Metrics.Port)
		}
	})

	t.Run("Metrics bind from env", func(t *testing.T) {
		t.Setenv("METRICS_BIND", "0.0.0.0:8080")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Metrics.Bind != "0.0.0.0:8080" {
			t.Errorf("expected Metrics.Bind=0.0.0.0:8080, got %s", cfg.Metrics.Bind)
		}
	})

	t.Run("Metrics disabled", func(t *testing.T) {
		t.Setenv("METRICS_ENABLED", "false")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Metrics.Enabled {
			t.Error("expected Metrics.Enabled=false")
		}
	})

	t.Run("Metrics enabled with various boolean values", func(t *testing.T) {
		tests := []struct {
			name  string
			value string
			want  bool
		}{
			{"true", "true", true},
			{"1", "1", true},
			{"yes", "yes", true},
			{"on", "on", true},
			{"false", "false", false},
			{"0", "0", false},
			{"no", "no", false},
			{"off", "off", false},
		}

		for _, tc := range tests {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("METRICS_ENABLED", tc.value)

				cfg, err := Load()
				if err != nil {
					t.Fatalf("Load returned error: %v", err)
				}

				if cfg.Metrics.Enabled != tc.want {
					t.Errorf("expected Metrics.Enabled=%v for value %q, got %v", tc.want, tc.value, cfg.Metrics.Enabled)
				}
			})
		}
	})
}

// ============================================================================
// Entropy Server Configuration Tests
// ============================================================================

func TestConfig_EntropyServerConfiguration(t *testing.T) {
	t.Run("Entropy bind from env", func(t *testing.T) {
		t.Setenv("ENTROPY_BIND", "0.0.0.0:8081")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Entropy.Bind != "0.0.0.0:8081" {
			t.Errorf("expected Entropy.Bind=0.0.0.0:8081, got %s", cfg.Entropy.Bind)
		}
	})

	t.Run("Entropy bind defaults", func(t *testing.T) {
		t.Setenv("ENTROPY_BIND", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Entropy.Bind != "127.0.0.1:8081" {
			t.Errorf("expected default Entropy.Bind=127.0.0.1:8081, got %s", cfg.Entropy.Bind)
		}
	})
}

// ============================================================================
// Integration Test - All Environment Variables
// ============================================================================

func TestConfig_AllEnvironmentVariablesIntegration(t *testing.T) {
	// Create temporary password file
	tmpDir := t.TempDir()
	passwordFile := filepath.Join(tmpDir, "mqtt_password.txt")
	if err := os.WriteFile(passwordFile, []byte("integration-password"), 0o600); err != nil {
		t.Fatalf("failed to write password file: %v", err)
	}

	// Set ALL supported environment variables
	envVars := map[string]string{
		"MQTT_BROKER_URL":          "ssl://mqtt.example.com:8883",
		"MQTT_CLIENT_ID":           "integration-client",
		"MQTT_TOPICS":              "integration/topic/#",
		"MQTT_QOS":                 "1",
		"MQTT_USERNAME":            "integration-user",
		"MQTT_PASSWORD_FILE":       passwordFile,
		"MQTT_TLS_CA_FILE":         "/path/to/ca.crt",
		"ENTROPY_POOL_MIN_BYTES":   "1024",
		"ENTROPY_POOL_MAX_BYTES":   "32768",
		"ENTROPY_READY_MIN_BYTES":  "2048",
		"ENTROPY_HTTP_ADDR":        "0.0.0.0:9797",
		"ENTROPY_RETRY_AFTER_SEC":  "10",
		"ALLOW_PUBLIC_HTTP":        "true",
		"ENTROPY_RATE_LIMIT_RPS":   "100",
		"ENTROPY_RATE_LIMIT_BURST": "200",
		"ENTROPY_TLS_ENABLED":      "true",
		"ENTROPY_TLS_CERT_FILE":    "/path/to/server.crt",
		"ENTROPY_TLS_KEY_FILE":     "/path/to/server.key",
		"ENTROPY_TLS_CLIENT_AUTH":  "none",
		"COLLECTOR_BATCH_SIZE":     "10000",
		"METRICS_PORT":             "9090",
		"METRICS_BIND":             "0.0.0.0:9090",
		"METRICS_ENABLED":          "true",
		"METRICS_TLS_ENABLED":      "false",
		"ENTROPY_BIND":             "0.0.0.0:8081",
		"ENVIRONMENT":              "production",
	}

	for key, value := range envVars {
		t.Setenv(key, value)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	// Verify MQTT configuration
	if cfg.MQTT.BrokerURL != "ssl://mqtt.example.com:8883" {
		t.Errorf("MQTT.BrokerURL = %s", cfg.MQTT.BrokerURL)
	}
	if cfg.MQTT.ClientID != "integration-client" {
		t.Errorf("MQTT.ClientID = %s", cfg.MQTT.ClientID)
	}
	if strings.Join(cfg.MQTT.Topics, ",") != "integration/topic/#" {
		t.Errorf("MQTT.Topic = %s", strings.Join(cfg.MQTT.Topics, ","))
	}
	if cfg.MQTT.QoS != 1 {
		t.Errorf("MQTT.QoS = %d", cfg.MQTT.QoS)
	}
	if cfg.MQTT.Username != "integration-user" {
		t.Errorf("MQTT.Username = %s", cfg.MQTT.Username)
	}
	if cfg.MQTT.Password != "integration-password" {
		t.Errorf("MQTT.Password = %s", cfg.MQTT.Password)
	}
	if cfg.MQTT.TLSCAFile != "/path/to/ca.crt" {
		t.Errorf("MQTT.TLSCAFile = %s", cfg.MQTT.TLSCAFile)
	}

	// Verify entropy pool configuration
	if cfg.EntropyPool.PoolMinBytes != 1024 {
		t.Errorf("EntropyPool.PoolMinBytes = %d", cfg.EntropyPool.PoolMinBytes)
	}
	if cfg.EntropyPool.PoolMaxBytes != 32768 {
		t.Errorf("EntropyPool.PoolMaxBytes = %d", cfg.EntropyPool.PoolMaxBytes)
	}
	if cfg.EntropyPool.ReadyMinBytes != 2048 {
		t.Errorf("EntropyPool.ReadyMinBytes = %d", cfg.EntropyPool.ReadyMinBytes)
	}
	if cfg.EntropyPool.HTTPAddr != "0.0.0.0:9797" {
		t.Errorf("EntropyPool.HTTPAddr = %s", cfg.EntropyPool.HTTPAddr)
	}
	if cfg.EntropyPool.RetryAfterSec != 10 {
		t.Errorf("EntropyPool.RetryAfterSec = %d", cfg.EntropyPool.RetryAfterSec)
	}
	if !cfg.EntropyPool.AllowPublic {
		t.Error("EntropyPool.AllowPublic should be true")
	}
	if cfg.EntropyPool.RateLimitRPS != 100 {
		t.Errorf("EntropyPool.RateLimitRPS = %d", cfg.EntropyPool.RateLimitRPS)
	}
	if cfg.EntropyPool.RateLimitBurst != 200 {
		t.Errorf("EntropyPool.RateLimitBurst = %d", cfg.EntropyPool.RateLimitBurst)
	}

	// Verify collector configuration
	if cfg.Collector.BatchSize != 10000 {
		t.Errorf("Collector.BatchSize = %d", cfg.Collector.BatchSize)
	}

	// Verify metrics configuration
	if cfg.Metrics.Port != 9090 {
		t.Errorf("Metrics.Port = %d", cfg.Metrics.Port)
	}
	if cfg.Metrics.Bind != "0.0.0.0:9090" {
		t.Errorf("Metrics.Bind = %s", cfg.Metrics.Bind)
	}
	if !cfg.Metrics.Enabled {
		t.Error("Metrics.Enabled should be true")
	}

	// Verify entropy server configuration
	if cfg.Entropy.Bind != "0.0.0.0:8081" {
		t.Errorf("Entropy.Bind = %s", cfg.Entropy.Bind)
	}

	// Verify environment
	if cfg.Environment != EnvironmentProduction {
		t.Errorf("Environment = %s", cfg.Environment)
	}
	if !cfg.IsProduction() {
		t.Error("IsProduction() should return true")
	}
	if cfg.IsDevelopment() {
		t.Error("IsDevelopment() should return false")
	}
}

// ============================================================================
// ParseBoolEnv Additional Edge Cases
// ============================================================================

func TestParseBoolEnv_AdditionalCases(t *testing.T) {
	const key = "PARSE_BOOL_EDGE_CASES"

	tests := []struct {
		name     string
		value    string
		fallback bool
		want     bool
	}{
		{"lowercase y", "y", false, true},
		{"lowercase n", "n", true, false},
		{"uppercase ON", "ON", false, true},
		{"uppercase OFF", "OFF", true, false},
		{"whitespace only", "   ", true, true},
		{"empty string", "", false, false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.value)

			got := ParseBoolEnv(key, tc.fallback)
			if got != tc.want {
				t.Errorf("ParseBoolEnv(%q, %v) = %v, want %v", tc.value, tc.fallback, got, tc.want)
			}
		})
	}
}

// ============================================================================
// Factory Function Refactoring Tests (replacing lines 230-240)
// ============================================================================

func TestConfig_ValidationWithFactory(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "invalid environment via factory",
			mutate: func(cfg *Config) {
				cfg.Environment = "staging"
			},
			wantErr: "environment must be 'dev' or 'prod'",
		},
		{
			name: "empty broker via factory",
			mutate: func(cfg *Config) {
				cfg.MQTT.BrokerURL = ""
			},
			wantErr: "MQTT_BROKER_URL",
		},
		{
			name: "empty topic via factory",
			mutate: func(cfg *Config) {
				cfg.MQTT.Topics = []string{}
			},
			wantErr: "MQTT_TOPIC",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)

			err := validate(&cfg)
			if err == nil {
				t.Fatal("expected validation error")
			}

			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// ============================================================================
// Error Path Coverage Tests
// ============================================================================

func TestConfig_LoadErrorPaths(t *testing.T) {
	t.Run("applyMQTTEnvVars error propagates", func(t *testing.T) {
		t.Setenv("MQTT_QOS", "invalid-number")

		_, err := Load()
		if err == nil {
			t.Fatal("expected error from invalid MQTT_QOS")
		}

		if !strings.Contains(err.Error(), "MQTT_QOS") {
			t.Errorf("expected MQTT_QOS error, got: %v", err)
		}
	})
}

// ============================================================================
// Integer Overflow and Boundary Tests
// ============================================================================

func TestConfig_IntegerOverflowBoundaries(t *testing.T) {
	t.Run("extremely large pool min bytes", func(t *testing.T) {
		// Test with a very large number (but still parseable as int)
		t.Setenv("ENTROPY_POOL_MIN_BYTES", "2147483647") // Max int32

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.PoolMinBytes != 2147483647 {
			t.Errorf("expected large PoolMinBytes to be parsed correctly, got %d", cfg.EntropyPool.PoolMinBytes)
		}

		// PoolMaxBytes should be adjusted to match since it's lower than min
		if cfg.EntropyPool.PoolMaxBytes < cfg.EntropyPool.PoolMinBytes {
			t.Errorf("expected PoolMaxBytes to be adjusted, got %d", cfg.EntropyPool.PoolMaxBytes)
		}
	})

	t.Run("pool bounds adjustment when max < min", func(t *testing.T) {
		t.Setenv("ENTROPY_POOL_MIN_BYTES", "10000")
		t.Setenv("ENTROPY_POOL_MAX_BYTES", "5000")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.PoolMaxBytes != cfg.EntropyPool.PoolMinBytes {
			t.Errorf("expected PoolMaxBytes to be adjusted to PoolMinBytes (10000), got %d", cfg.EntropyPool.PoolMaxBytes)
		}
	})

	t.Run("unparseable integer uses fallback", func(t *testing.T) {
		t.Setenv("ENTROPY_POOL_MIN_BYTES", "99999999999999999999999999999")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// Should fallback to default
		if cfg.EntropyPool.PoolMinBytes != defaultPoolMinBytes {
			t.Errorf("expected default for unparseable value, got %d", cfg.EntropyPool.PoolMinBytes)
		}
	})

	t.Run("rate limit with max int value", func(t *testing.T) {
		t.Setenv("ENTROPY_RATE_LIMIT_RPS", "2147483647")
		t.Setenv("ENTROPY_RATE_LIMIT_BURST", "2147483647")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.RateLimitRPS != 2147483647 {
			t.Errorf("expected max int RateLimitRPS, got %d", cfg.EntropyPool.RateLimitRPS)
		}
		if cfg.EntropyPool.RateLimitBurst != 2147483647 {
			t.Errorf("expected max int RateLimitBurst, got %d", cfg.EntropyPool.RateLimitBurst)
		}
	})
}

// ============================================================================
// Additional TLS Edge Cases
// ============================================================================

func TestConfig_TLSEdgeCases(t *testing.T) {
	t.Run("empty TLS CA file env var", func(t *testing.T) {
		t.Setenv("MQTT_TLS_CA_FILE", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.MQTT.TLSCAFile != "" {
			t.Errorf("expected empty TLSCAFile for empty env var, got %s", cfg.MQTT.TLSCAFile)
		}
	})

	t.Run("whitespace-only password file path", func(t *testing.T) {
		t.Setenv("MQTT_PASSWORD_FILE", "   ")

		// This should attempt to read the file and fail
		_, err := Load()
		if err == nil {
			t.Fatal("expected error for whitespace-only password file path")
		}
	})

	t.Run("empty username and password", func(t *testing.T) {
		t.Setenv("MQTT_USERNAME", "")
		t.Setenv("MQTT_PASSWORD", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.MQTT.Username != "" {
			t.Errorf("expected empty username, got %s", cfg.MQTT.Username)
		}
		if cfg.MQTT.Password != "" {
			t.Errorf("expected empty password, got %s", cfg.MQTT.Password)
		}
	})
}

// ============================================================================
// Additional Entropy Pool Configuration Tests
// ============================================================================

func TestConfig_EntropyPoolAdditionalCases(t *testing.T) {
	t.Run("retry after seconds boundary", func(t *testing.T) {
		t.Setenv("ENTROPY_RETRY_AFTER_SEC", "1")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.RetryAfterSec != 1 {
			t.Errorf("expected RetryAfterSec=1, got %d", cfg.EntropyPool.RetryAfterSec)
		}
	})

	t.Run("allow public false", func(t *testing.T) {
		t.Setenv("ALLOW_PUBLIC_HTTP", "false")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.AllowPublic {
			t.Error("expected AllowPublic=false")
		}
	})

	t.Run("HTTP addr with whitespace", func(t *testing.T) {
		t.Setenv("ENTROPY_HTTP_ADDR", "  127.0.0.1:9999  ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// GetEnvDefault should trim whitespace (and inline comments)
		if cfg.EntropyPool.HTTPAddr != "127.0.0.1:9999" {
			t.Errorf("expected HTTPAddr without whitespace, got %s", cfg.EntropyPool.HTTPAddr)
		}
	})

	t.Run("rate limit zero values", func(t *testing.T) {
		t.Setenv("ENTROPY_RATE_LIMIT_RPS", "0")
		t.Setenv("ENTROPY_RATE_LIMIT_BURST", "0")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// ParsePositiveEnvInt should return default (25) for zero
		if cfg.EntropyPool.RateLimitRPS != 25 {
			t.Errorf("expected default RateLimitRPS for zero, got %d", cfg.EntropyPool.RateLimitRPS)
		}
		if cfg.EntropyPool.RateLimitBurst != 25 {
			t.Errorf("expected default RateLimitBurst for zero, got %d", cfg.EntropyPool.RateLimitBurst)
		}
	})
}

// ============================================================================
// String() Method Coverage
// ============================================================================

func TestConfig_StringRedactionAndContent(t *testing.T) {
	t.Run("string output contains key fields", func(t *testing.T) {
		cfg := validConfig()
		cfg.MQTT.BrokerURL = "tcp://test-broker:1883"
		cfg.MQTT.Topics = []string{"test/topic"}
		cfg.Environment = EnvironmentProduction

		output := cfg.String()

		if !strings.Contains(output, "test-broker") {
			t.Errorf("expected broker in output, got: %s", output)
		}
		if !strings.Contains(output, "test/topic") {
			t.Errorf("expected topic in output, got: %s", output)
		}
		if !strings.Contains(output, EnvironmentProduction) {
			t.Errorf("expected environment in output, got: %s", output)
		}
	})

	t.Run("production mode helpers", func(t *testing.T) {
		cfg := validConfig()
		cfg.Environment = EnvironmentProduction

		if !cfg.IsProduction() {
			t.Error("expected IsProduction() to return true")
		}
		if cfg.IsDevelopment() {
			t.Error("expected IsDevelopment() to return false")
		}
	})
}

// ============================================================================
// Collector Batch Size Additional Edge Cases
// ============================================================================

func TestConfig_CollectorBatchSizeEdgeCases(t *testing.T) {
	t.Run("batch size exactly at minimum", func(t *testing.T) {
		t.Setenv("COLLECTOR_BATCH_SIZE", "1840")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Collector.BatchSize != minBatchSize {
			t.Errorf("expected BatchSize=%d, got %d", minBatchSize, cfg.Collector.BatchSize)
		}
	})

	t.Run("batch size exactly at maximum", func(t *testing.T) {
		t.Setenv("COLLECTOR_BATCH_SIZE", "184000")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Collector.BatchSize != maxBatchSize {
			t.Errorf("expected BatchSize=%d, got %d", maxBatchSize, cfg.Collector.BatchSize)
		}
	})

	t.Run("negative batch size", func(t *testing.T) {
		t.Setenv("COLLECTOR_BATCH_SIZE", "-5000")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// ParsePositiveEnvInt is not used here, so the value gets clamped
		if cfg.Collector.BatchSize != minBatchSize {
			t.Errorf("expected BatchSize clamped to min for negative, got %d", cfg.Collector.BatchSize)
		}
	})

	t.Run("batch size whitespace handling", func(t *testing.T) {
		t.Setenv("COLLECTOR_BATCH_SIZE", "  5000  ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.Collector.BatchSize != 5000 {
			t.Errorf("expected BatchSize=5000 (trimmed), got %d", cfg.Collector.BatchSize)
		}
	})
}

// ============================================================================
// Metrics Configuration Additional Edge Cases
// ============================================================================

func TestConfig_MetricsAdditionalCases(t *testing.T) {
	t.Run("metrics port zero", func(t *testing.T) {
		t.Setenv("METRICS_PORT", "0")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// ParsePositiveEnvInt returns default for zero
		if cfg.Metrics.Port != defaultMetricsPort {
			t.Errorf("expected default port for zero, got %d", cfg.Metrics.Port)
		}
	})

	t.Run("metrics bind empty string", func(t *testing.T) {
		t.Setenv("METRICS_BIND", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// Should use default
		if cfg.Metrics.Bind != "127.0.0.1:8080" {
			t.Errorf("expected default bind address, got %s", cfg.Metrics.Bind)
		}
	})

	t.Run("metrics disabled by default remains enabled", func(t *testing.T) {
		// Don't set METRICS_ENABLED at all
		t.Setenv("METRICS_ENABLED", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		// Default is true
		if !cfg.Metrics.Enabled {
			t.Error("expected Metrics.Enabled=true by default")
		}
	})
}

// ============================================================================
// TLS Validation Coverage (applyMetricsEnvVars, applyGRPCEnvVars, validate)
// ============================================================================

func TestConfig_EntropyTLSValidation(t *testing.T) {
	t.Run("entropy TLS enabled requires cert file", func(t *testing.T) {
		cfg := validConfig()
		cfg.EntropyPool.TLSEnabled = true
		cfg.EntropyPool.TLSKeyFile = "/path/to/key.pem"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "ENTROPY_TLS_CERT_FILE") {
			t.Fatalf("expected ENTROPY_TLS_CERT_FILE error, got: %v", err)
		}
	})

	t.Run("entropy TLS enabled requires key file", func(t *testing.T) {
		cfg := validConfig()
		cfg.EntropyPool.TLSEnabled = true
		cfg.EntropyPool.TLSCertFile = "/path/to/cert.pem"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "ENTROPY_TLS_KEY_FILE") {
			t.Fatalf("expected ENTROPY_TLS_KEY_FILE error, got: %v", err)
		}
	})

	t.Run("entropy TLS invalid client auth mode", func(t *testing.T) {
		cfg := validConfig()
		cfg.EntropyPool.TLSEnabled = true
		cfg.EntropyPool.TLSCertFile = "/path/to/cert.pem"
		cfg.EntropyPool.TLSKeyFile = "/path/to/key.pem"
		cfg.EntropyPool.TLSClientAuth = "invalid"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "ENTROPY_TLS_CLIENT_AUTH") {
			t.Fatalf("expected ENTROPY_TLS_CLIENT_AUTH error, got: %v", err)
		}
	})

	t.Run("entropy TLS client auth request valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.EntropyPool.TLSEnabled = true
		cfg.EntropyPool.TLSCertFile = "/path/to/cert.pem"
		cfg.EntropyPool.TLSKeyFile = "/path/to/key.pem"
		cfg.EntropyPool.TLSClientAuth = "request"

		if err := validate(&cfg); err != nil {
			t.Fatalf("unexpected error for valid 'request' mode: %v", err)
		}
	})

	t.Run("entropy TLS require auth needs CA file", func(t *testing.T) {
		cfg := validConfig()
		cfg.EntropyPool.TLSEnabled = true
		cfg.EntropyPool.TLSCertFile = "/path/to/cert.pem"
		cfg.EntropyPool.TLSKeyFile = "/path/to/key.pem"
		cfg.EntropyPool.TLSClientAuth = "require"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "ENTROPY_TLS_CA_FILE") {
			t.Fatalf("expected ENTROPY_TLS_CA_FILE error, got: %v", err)
		}
	})

	t.Run("entropy TLS require auth with CA file succeeds", func(t *testing.T) {
		cfg := validConfig()
		cfg.EntropyPool.TLSEnabled = true
		cfg.EntropyPool.TLSCertFile = "/path/to/cert.pem"
		cfg.EntropyPool.TLSKeyFile = "/path/to/key.pem"
		cfg.EntropyPool.TLSClientAuth = "require"
		cfg.EntropyPool.TLSCAFile = "/path/to/ca.pem"

		if err := validate(&cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("entropy public without TLS in prod fails", func(t *testing.T) {
		cfg := validConfig()
		cfg.Environment = EnvironmentProduction
		cfg.EntropyPool.AllowPublic = true
		cfg.EntropyPool.TLSEnabled = false

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "SECURITY") {
			t.Fatalf("expected security error for public HTTP in prod, got: %v", err)
		}
	})
}

func TestConfig_MetricsTLSValidation(t *testing.T) {
	t.Run("metrics TLS enabled requires cert file", func(t *testing.T) {
		cfg := validConfig()
		cfg.Metrics.TLSEnabled = true
		cfg.Metrics.TLSKeyFile = "/path/to/key.pem"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "METRICS_TLS_CERT_FILE") {
			t.Fatalf("expected METRICS_TLS_CERT_FILE error, got: %v", err)
		}
	})

	t.Run("metrics TLS enabled requires key file", func(t *testing.T) {
		cfg := validConfig()
		cfg.Metrics.TLSEnabled = true
		cfg.Metrics.TLSCertFile = "/path/to/cert.pem"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "METRICS_TLS_KEY_FILE") {
			t.Fatalf("expected METRICS_TLS_KEY_FILE error, got: %v", err)
		}
	})

	t.Run("metrics TLS env vars applied", func(t *testing.T) {
		t.Setenv("METRICS_TLS_ENABLED", "true")
		t.Setenv("METRICS_TLS_CERT_FILE", "/metrics/cert.pem")
		t.Setenv("METRICS_TLS_KEY_FILE", "/metrics/key.pem")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if !cfg.Metrics.TLSEnabled {
			t.Error("expected Metrics.TLSEnabled=true")
		}
		if cfg.Metrics.TLSCertFile != "/metrics/cert.pem" {
			t.Errorf("TLSCertFile = %s", cfg.Metrics.TLSCertFile)
		}
		if cfg.Metrics.TLSKeyFile != "/metrics/key.pem" {
			t.Errorf("TLSKeyFile = %s", cfg.Metrics.TLSKeyFile)
		}
	})
}

func TestConfig_GRPCTLSValidation(t *testing.T) {
	t.Run("grpc TLS enabled requires cert file", func(t *testing.T) {
		cfg := validConfig()
		cfg.GRPC.Enabled = true
		cfg.GRPC.TLSEnabled = true
		cfg.GRPC.TLSKeyFile = "/path/to/key.pem"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "GRPC_TLS_CERT_FILE") {
			t.Fatalf("expected GRPC_TLS_CERT_FILE error, got: %v", err)
		}
	})

	t.Run("grpc TLS enabled requires key file", func(t *testing.T) {
		cfg := validConfig()
		cfg.GRPC.Enabled = true
		cfg.GRPC.TLSEnabled = true
		cfg.GRPC.TLSCertFile = "/path/to/cert.pem"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "GRPC_TLS_KEY_FILE") {
			t.Fatalf("expected GRPC_TLS_KEY_FILE error, got: %v", err)
		}
	})

	t.Run("grpc TLS invalid client auth mode", func(t *testing.T) {
		cfg := validConfig()
		cfg.GRPC.Enabled = true
		cfg.GRPC.TLSEnabled = true
		cfg.GRPC.TLSCertFile = "/path/to/cert.pem"
		cfg.GRPC.TLSKeyFile = "/path/to/key.pem"
		cfg.GRPC.TLSClientAuth = "bad-mode"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "GRPC_TLS_CLIENT_AUTH") {
			t.Fatalf("expected GRPC_TLS_CLIENT_AUTH error, got: %v", err)
		}
	})

	t.Run("grpc TLS require auth needs CA file", func(t *testing.T) {
		cfg := validConfig()
		cfg.GRPC.Enabled = true
		cfg.GRPC.TLSEnabled = true
		cfg.GRPC.TLSCertFile = "/path/to/cert.pem"
		cfg.GRPC.TLSKeyFile = "/path/to/key.pem"
		cfg.GRPC.TLSClientAuth = "require"

		err := validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "GRPC_TLS_CA_FILE") {
			t.Fatalf("expected GRPC_TLS_CA_FILE error, got: %v", err)
		}
	})

	t.Run("grpc TLS require auth with CA succeeds", func(t *testing.T) {
		cfg := validConfig()
		cfg.GRPC.Enabled = true
		cfg.GRPC.TLSEnabled = true
		cfg.GRPC.TLSCertFile = "/path/to/cert.pem"
		cfg.GRPC.TLSKeyFile = "/path/to/key.pem"
		cfg.GRPC.TLSClientAuth = "require"
		cfg.GRPC.TLSCAFile = "/path/to/ca.pem"

		if err := validate(&cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("grpc disabled skips TLS validation", func(t *testing.T) {
		cfg := validConfig()
		cfg.GRPC.Enabled = false
		cfg.GRPC.TLSEnabled = true

		if err := validate(&cfg); err != nil {
			t.Fatalf("unexpected error when gRPC disabled: %v", err)
		}
	})

	t.Run("grpc env vars applied", func(t *testing.T) {
		t.Setenv("GRPC_ENABLED", "true")
		t.Setenv("GRPC_BIND", "0.0.0.0:9090")
		t.Setenv("GRPC_TLS_ENABLED", "true")
		t.Setenv("GRPC_TLS_CERT_FILE", "/grpc/cert.pem")
		t.Setenv("GRPC_TLS_KEY_FILE", "/grpc/key.pem")
		t.Setenv("GRPC_TLS_CA_FILE", "/grpc/ca.pem")
		t.Setenv("GRPC_TLS_CLIENT_AUTH", "request")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if !cfg.GRPC.Enabled {
			t.Error("expected GRPC.Enabled=true")
		}
		if cfg.GRPC.Bind != "0.0.0.0:9090" {
			t.Errorf("GRPC.Bind = %s", cfg.GRPC.Bind)
		}
		if !cfg.GRPC.TLSEnabled {
			t.Error("expected GRPC.TLSEnabled=true")
		}
		if cfg.GRPC.TLSCertFile != "/grpc/cert.pem" {
			t.Errorf("TLSCertFile = %s", cfg.GRPC.TLSCertFile)
		}
		if cfg.GRPC.TLSKeyFile != "/grpc/key.pem" {
			t.Errorf("TLSKeyFile = %s", cfg.GRPC.TLSKeyFile)
		}
		if cfg.GRPC.TLSCAFile != "/grpc/ca.pem" {
			t.Errorf("TLSCAFile = %s", cfg.GRPC.TLSCAFile)
		}
		if cfg.GRPC.TLSClientAuth != "request" {
			t.Errorf("TLSClientAuth = %s", cfg.GRPC.TLSClientAuth)
		}
	})
}

func TestApplyEntropyPoolTLSEnvVars(t *testing.T) {
	t.Run("entropy TLS env vars applied", func(t *testing.T) {
		t.Setenv("ENTROPY_TLS_ENABLED", "true")
		t.Setenv("ENTROPY_TLS_CERT_FILE", "/entropy/cert.pem")
		t.Setenv("ENTROPY_TLS_KEY_FILE", "/entropy/key.pem")
		t.Setenv("ENTROPY_TLS_CA_FILE", "/entropy/ca.pem")
		t.Setenv("ENTROPY_TLS_CLIENT_AUTH", "require")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if !cfg.EntropyPool.TLSEnabled {
			t.Error("expected EntropyPool.TLSEnabled=true")
		}
		if cfg.EntropyPool.TLSCertFile != "/entropy/cert.pem" {
			t.Errorf("TLSCertFile = %s", cfg.EntropyPool.TLSCertFile)
		}
		if cfg.EntropyPool.TLSKeyFile != "/entropy/key.pem" {
			t.Errorf("TLSKeyFile = %s", cfg.EntropyPool.TLSKeyFile)
		}
		if cfg.EntropyPool.TLSCAFile != "/entropy/ca.pem" {
			t.Errorf("TLSCAFile = %s", cfg.EntropyPool.TLSCAFile)
		}
		if cfg.EntropyPool.TLSClientAuth != "require" {
			t.Errorf("TLSClientAuth = %s", cfg.EntropyPool.TLSClientAuth)
		}
	})

	t.Run("entropy TLS client auth defaults to none", func(t *testing.T) {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if cfg.EntropyPool.TLSClientAuth != "none" {
			t.Errorf("expected default TLSClientAuth='none', got %s", cfg.EntropyPool.TLSClientAuth)
		}
	})
}

func TestMQTTTopicsEdgeCases(t *testing.T) {
	t.Run("mqtt topics with whitespace", func(t *testing.T) {
		t.Setenv("MQTT_TOPICS", "  topic1 ,  topic2  , , topic3  ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		expected := []string{"topic1", "topic2", "topic3"}
		if len(cfg.MQTT.Topics) != len(expected) {
			t.Fatalf("expected %d topics, got %d", len(expected), len(cfg.MQTT.Topics))
		}
		for i, topic := range cfg.MQTT.Topics {
			if topic != expected[i] {
				t.Errorf("topic[%d] = %q, want %q", i, topic, expected[i])
			}
		}
	})

	t.Run("mqtt topics all empty after trim", func(t *testing.T) {
		t.Setenv("MQTT_TOPICS", "  ,  ,  ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}

		if len(cfg.MQTT.Topics) != 1 || cfg.MQTT.Topics[0] != "timestamps/channel/#" {
			t.Errorf("expected default topic when all trimmed empty, got %v", cfg.MQTT.Topics)
		}
	})
}
