// Command chaski runs a stateless webhook relay: it accepts JSON over HTTP,
// gates each request with a CEL expression, renders fields with Go templates,
// and relays the result to a configured target (an apprise notification or a
// generic HTTP request).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/relay"
	"github.com/home-operations/chaski/internal/server"
)

// Build metadata, stamped via -ldflags at release time.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	// `chaski validate [-c path]` checks config and exits, without starting the
	// server — the CI gate (same checks the server runs at boot).
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		if err := runValidate(os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chaski validate:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	setMemLimit()

	// The first signal triggers a graceful drain; re-arm the default handler so a
	// second signal force-quits instead of being swallowed during a slow drain.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// apprise-go sends through the shared http.DefaultClient, which has no
	// timeout and takes no context — so a hung notification endpoint would pin a
	// goroutine and socket indefinitely, past the request deadline. Bounding the
	// default client here is the only in-process way to cap those sends. chaski's
	// own HTTP sink uses its own client, so this affects only apprise delivery.
	http.DefaultClient.Timeout = cfg.RequestTimeout

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	routeConfig, err := config.LoadRouteConfig(cfg.ConfigPath)
	if err != nil {
		return err
	}
	engine, err := relay.Build(routeConfig, cfg, relay.Options{})
	if err != nil {
		return err
	}

	if cfg.WebhookToken == "" {
		// Token auth is optional — a fine default for cluster-internal senders.
		// Note it at info level (not a warning); per-route verify still applies.
		logger.Info("inbound token auth disabled (no CHASKI_WEBHOOK_TOKEN); per-route verify still applies")
	}
	if engine.RouteCount() == 0 {
		logger.Warn("no routes configured; relay is idle (every webhook will 404)", "config", cfg.ConfigPath)
	}

	server.RecordBuildInfo(version, commit)

	logger.Info("starting chaski",
		"version", version,
		"commit", commit,
		"http_port", cfg.HTTPPort,
		"metrics_port", cfg.MetricsPort,
		"smtp_enabled", cfg.SMTPEnabled,
		"routes", engine.RouteCount(),
		"gomaxprocs", runtime.GOMAXPROCS(0),
	)

	return server.New(cfg, engine, logger).Run(ctx)
}

// newLogger builds the root logger: JSON by default (the container-friendly
// format), text on request, always to stdout.
func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if strings.EqualFold(cfg.LogFormat, "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

// setMemLimit caps the Go heap (GOMEMLIMIT) at 90% of the cgroup memory limit
// when one is set, so the GC reclaims before the container is OOM-killed. It is
// a silent no-op outside a memory-limited cgroup.
func setMemLimit() {
	_, _ = memlimit.SetGoMemLimitWithOpts(
		memlimit.WithRatio(0.9),
		memlimit.WithProvider(memlimit.FromCgroup),
		memlimit.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
}
