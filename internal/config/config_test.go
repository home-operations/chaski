package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// t.Setenv ensures a clean, parallel-safe env; unset CHASKI_* vars fall back
	// to the struct-tag defaults.
	t.Setenv("CHASKI_LOG_LEVEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPPort != 8080 {
		t.Errorf("HTTPPort = %d, want 8080", cfg.HTTPPort)
	}
	if cfg.MetricsPort != 8081 {
		t.Errorf("MetricsPort = %d, want 8081", cfg.MetricsPort)
	}
	if cfg.ConfigPath != "/config/chaski.yaml" {
		t.Errorf("ConfigPath = %q, want /config/chaski.yaml", cfg.ConfigPath)
	}
	if cfg.MaxBodyBytes != 1<<20 {
		t.Errorf("MaxBodyBytes = %d, want 1048576", cfg.MaxBodyBytes)
	}
	if cfg.RequestTimeout != 15*time.Second {
		t.Errorf("RequestTimeout = %s, want 15s", cfg.RequestTimeout)
	}
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.RetryAttempts)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]map[string]string{
		"invalid log level": {"CHASKI_LOG_LEVEL": "bogus"},
		"port out of range": {"CHASKI_PORT": "0"},
		"metrics port high": {"CHASKI_METRICS_PORT": "70000"},
		"ports collide":     {"CHASKI_PORT": "9000", "CHASKI_METRICS_PORT": "9000"},
		"bad log format":    {"CHASKI_LOG_FORMAT": "yaml"},
		"negative body":     {"CHASKI_MAX_BODY_BYTES": "-1"},
		"zero attempts":     {"CHASKI_RETRY_ATTEMPTS": "0"},
		"zero timeout":      {"CHASKI_REQUEST_TIMEOUT": "0s"},
	}
	for name, envs := range tests {
		t.Run(name, func(t *testing.T) {
			for k, v := range envs {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatalf("Load() = nil error, want error for %s", name)
			}
		})
	}
}
