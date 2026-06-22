// Package server wires chaski's webhook and metrics listeners together and
// manages their lifecycle.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/relay"
	"github.com/home-operations/chaski/internal/smtp"
	"golang.org/x/sync/errgroup"
)

// Connection timeouts applied to every listener, bounding slow-client
// (Slowloris) and idle keep-alive resource exhaustion.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second
)

// newHTTPServer builds an http.Server with chaski's standard connection timeouts.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// Server owns the configured listeners and the compiled relay engine.
type Server struct {
	cfg    *config.Config
	engine *relay.Engine
	log    *slog.Logger
}

// New constructs a Server from the resolved ops config and the compiled engine.
func New(cfg *config.Config, engine *relay.Engine, log *slog.Logger) *Server {
	return &Server{cfg: cfg, engine: engine, log: log}
}

// Run starts every enabled listener and blocks until ctx is cancelled or a
// listener fails, then drains them within the configured shutdown timeout.
func (s *Server) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)

	webhookSrv := newHTTPServer(fmt.Sprintf(":%d", s.cfg.HTTPPort), s.handler())
	g.Go(func() error { return serve(webhookSrv, "webhook", s.log) })

	var metricsSrv *http.Server
	if s.cfg.MetricsEnabled {
		metricsSrv = newHTTPServer(fmt.Sprintf(":%d", s.cfg.MetricsPort), s.accessLog(metricsHandler()))
		g.Go(func() error { return serve(metricsSrv, "metrics", s.log) })
	}

	var smtpSrv *smtp.Server
	if s.cfg.SMTPEnabled {
		smtpSrv = smtp.New(s.cfg, s.engine, s.log, s.observeRelay)
		g.Go(smtpSrv.ListenAndServe)
	}

	g.Go(func() error {
		<-gctx.Done()
		s.log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		shutdown(sctx, webhookSrv)
		shutdown(sctx, metricsSrv)
		smtpSrv.Shutdown(sctx)
		return nil
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func serve(srv *http.Server, name string, log *slog.Logger) error {
	log.Info("listening", "server", name, "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server: %w", name, err)
	}
	return nil
}

func shutdown(ctx context.Context, srv *http.Server) {
	if srv == nil {
		return
	}
	_ = srv.Shutdown(ctx)
}
