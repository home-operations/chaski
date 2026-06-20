// Package config loads chaski's runtime configuration from CHASKI_*-prefixed
// environment variables. Route and target definitions are loaded separately
// from CHASKI_CONFIG (a YAML file or a config.d directory) — see the loader.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// DefaultConfigPath is the routes + targets source when CHASKI_CONFIG is unset.
const DefaultConfigPath = "/config/chaski.yaml"

// Config is the fully resolved runtime (ops) configuration. The simple fields
// are populated by env.Parse; LogLevel is derived in Load so its parsing fails
// fast with a clear message.
type Config struct {
	// HTTPPort is the public webhook receiver (POST /notify/{route}).
	HTTPPort int `env:"CHASKI_PORT" envDefault:"8080"`

	// MetricsEnabled exposes Prometheus metrics at /metrics and the /healthz
	// probe on MetricsPort, keeping monitoring off the public webhook port.
	MetricsEnabled bool `env:"CHASKI_METRICS_ENABLED" envDefault:"true"`
	MetricsPort    int  `env:"CHASKI_METRICS_PORT" envDefault:"8081"`

	// ConfigPath is the routes + targets source: a single YAML file or a
	// directory of *.yaml/*.yml fragments merged additively (config.d).
	ConfigPath string `env:"CHASKI_CONFIG" envDefault:"/config/chaski.yaml"`

	// WebhookToken is the shared secret required on every inbound request
	// (constant-time compared). Routes may add an HMAC/token verify on top.
	WebhookToken string `env:"CHASKI_WEBHOOK_TOKEN"`

	// MaxBodyBytes caps the inbound request body; larger bodies are rejected.
	MaxBodyBytes int64 `env:"CHASKI_MAX_BODY_BYTES" envDefault:"1048576"`

	// RequestTimeout bounds the whole request: decode + gate + render + send +
	// retry.
	RequestTimeout time.Duration `env:"CHASKI_REQUEST_TIMEOUT" envDefault:"15s"`

	// RetryAttempts / RetryBackoff are the global defaults a target's own retry
	// block overrides (total tries; base backoff for exponential + jitter).
	RetryAttempts int           `env:"CHASKI_RETRY_ATTEMPTS" envDefault:"3"`
	RetryBackoff  time.Duration `env:"CHASKI_RETRY_BACKOFF" envDefault:"200ms"`

	// LogFormat selects the slog handler: "json" (default) or "text".
	LogFormat string `env:"CHASKI_LOG_FORMAT" envDefault:"json"`
	// DisableRequestLogs silences the per-request access log.
	DisableRequestLogs bool `env:"CHASKI_DISABLE_REQUEST_LOGS" envDefault:"false"`

	// ShutdownTimeout bounds the graceful drain on SIGINT/SIGTERM.
	ShutdownTimeout time.Duration `env:"CHASKI_SHUTDOWN_TIMEOUT" envDefault:"15s"`

	// LogLevel is parsed from CHASKI_LOG_LEVEL (debug|info|warn|error) in Load.
	LogLevel slog.Level `env:"-"`
}

// Load reads the configuration from the environment, applies defaults, derives
// the computed fields, and validates the result.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	level := strings.TrimSpace(os.Getenv("CHASKI_LOG_LEVEL"))
	if level == "" {
		level = "info"
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("config: invalid CHASKI_LOG_LEVEL %q: %w", level, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if err := validatePort(c.HTTPPort, "CHASKI_PORT"); err != nil {
		return err
	}
	if err := validatePort(c.MetricsPort, "CHASKI_METRICS_PORT"); err != nil {
		return err
	}
	if c.MetricsEnabled && c.HTTPPort == c.MetricsPort {
		return fmt.Errorf("config: CHASKI_PORT and CHASKI_METRICS_PORT must differ (both %d)", c.HTTPPort)
	}
	switch strings.ToLower(c.LogFormat) {
	case "json", "text":
	default:
		return fmt.Errorf("config: invalid CHASKI_LOG_FORMAT %q (want json or text)", c.LogFormat)
	}
	if c.MaxBodyBytes < 0 {
		return fmt.Errorf("config: CHASKI_MAX_BODY_BYTES must be >= 0, got %d", c.MaxBodyBytes)
	}
	if c.RetryAttempts < 1 {
		return fmt.Errorf("config: CHASKI_RETRY_ATTEMPTS must be >= 1, got %d", c.RetryAttempts)
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("config: CHASKI_REQUEST_TIMEOUT must be > 0, got %s", c.RequestTimeout)
	}
	return nil
}

func validatePort(p int, name string) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("config: %s must be between 1 and 65535, got %d", name, p)
	}
	return nil
}
