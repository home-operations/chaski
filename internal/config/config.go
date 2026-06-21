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

	// SMTPEnabled starts an optional SMTP listener that turns received mail into
	// a relay, selecting the route by the recipient localpart (sonarr@... → the
	// route named "sonarr"). Off by default: it is an inbound relay path, so it
	// is opted into explicitly.
	SMTPEnabled bool `env:"CHASKI_SMTP_ENABLED" envDefault:"false"`
	SMTPPort    int  `env:"CHASKI_SMTP_PORT" envDefault:"8025"`
	// SMTPAuth is an optional "user:password" list (comma-separated). When set,
	// SMTP AUTH (PLAIN/LOGIN) is required; when empty the listener is
	// unauthenticated, intended for a trusted cluster network.
	SMTPAuth string `env:"CHASKI_SMTP_AUTH"`
	// SMTPHostname is the name the server announces in its SMTP greeting.
	SMTPHostname string `env:"CHASKI_SMTP_HOSTNAME" envDefault:"chaski"`
	// SMTPMaxMessageBytes caps an inbound message; SMTPMaxRecipients caps the
	// RCPTs per message.
	SMTPMaxMessageBytes int64 `env:"CHASKI_SMTP_MAX_MESSAGE_BYTES" envDefault:"1048576"`
	SMTPMaxRecipients   int   `env:"CHASKI_SMTP_MAX_RECIPIENTS" envDefault:"50"`

	// LogLevel is parsed from CHASKI_LOG_LEVEL (debug|info|warn|error) in Load.
	LogLevel slog.Level `env:"-"`
	// SMTPUsers is parsed from SMTPAuth in Load (username → password).
	SMTPUsers map[string]string `env:"-"`
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

	users, err := parseSMTPAuth(cfg.SMTPAuth)
	if err != nil {
		return nil, err
	}
	cfg.SMTPUsers = users

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// parseSMTPAuth parses CHASKI_SMTP_AUTH ("user:password,user2:password2") into a
// username→password map. An empty string yields a nil map (auth disabled). The
// password may contain ':' (only the first separates user from password).
func parseSMTPAuth(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	users := map[string]string{}
	for pair := range strings.SplitSeq(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		user, pass, ok := strings.Cut(pair, ":")
		user = strings.TrimSpace(user)
		if !ok || user == "" || pass == "" {
			return nil, fmt.Errorf("config: CHASKI_SMTP_AUTH entry %q must be user:password", pair)
		}
		users[user] = pass
	}
	return users, nil
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
	if c.SMTPEnabled {
		if err := validatePort(c.SMTPPort, "CHASKI_SMTP_PORT"); err != nil {
			return err
		}
		if c.SMTPPort == c.HTTPPort {
			return fmt.Errorf("config: CHASKI_SMTP_PORT and CHASKI_PORT must differ (both %d)", c.SMTPPort)
		}
		if c.MetricsEnabled && c.SMTPPort == c.MetricsPort {
			return fmt.Errorf("config: CHASKI_SMTP_PORT and CHASKI_METRICS_PORT must differ (both %d)", c.SMTPPort)
		}
		if c.SMTPMaxMessageBytes < 1 {
			// 0 would make go-smtp treat the message size as unbounded, removing
			// the only in-memory read cap; require an explicit positive limit.
			return fmt.Errorf("config: CHASKI_SMTP_MAX_MESSAGE_BYTES must be >= 1, got %d", c.SMTPMaxMessageBytes)
		}
		if c.SMTPMaxRecipients < 1 {
			return fmt.Errorf("config: CHASKI_SMTP_MAX_RECIPIENTS must be >= 1, got %d", c.SMTPMaxRecipients)
		}
	}
	return nil
}

func validatePort(p int, name string) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("config: %s must be between 1 and 65535, got %d", name, p)
	}
	return nil
}
