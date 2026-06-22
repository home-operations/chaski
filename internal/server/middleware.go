package server

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// statusRecorder captures the response status code for the access log and
// metrics, defaulting to 200 if the handler writes a body without an explicit
// WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap exposes the wrapped ResponseWriter to http.ResponseController.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// securityHeaders makes any JSON response inert in a browser: nosniff stops
// content-type guessing, and responses are never cached.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// recoverer turns a handler panic into a 500 instead of crashing the process.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("recovered panic", "error", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// observe records Prometheus metrics and emits the per-request access log. It
// wraps the public webhook listener.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start)
		method := methodLabel(r.Method)
		httpRequests.WithLabelValues(method, statusClass(rec.status)).Inc()
		httpDuration.WithLabelValues(method).Observe(duration.Seconds())
		s.logRequest(r, rec.status, duration)
	})
}

// accessLog emits the per-request access log WITHOUT recording request metrics.
// It wraps the monitoring listener, so /metrics scrapes and /healthz probes are
// logged (at debug — see logRequest) without inflating the request counters.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.logRequest(r, rec.status, time.Since(start))
	})
}

// monitoringPaths are the scrape/probe endpoints whose access log is noise at
// the scrape/probe cadence, so it is emitted at Debug — visible only under
// CHASKI_LOG_LEVEL=debug — rather than at Info with real traffic.
var monitoringPaths = map[string]struct{}{
	"/metrics": {}, "/healthz": {}, "/readyz": {},
}

// logRequest emits the access log for one request at the path-appropriate level
// (Debug for the monitoring endpoints, Info otherwise), unless request logs are
// disabled entirely.
func (s *Server) logRequest(r *http.Request, status int, d time.Duration) {
	if s.cfg.DisableRequestLogs {
		return
	}
	level := slog.LevelInfo
	if _, ok := monitoringPaths[r.URL.Path]; ok {
		level = slog.LevelDebug
	}
	s.log.Log(r.Context(), level, "request",
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"remote", r.RemoteAddr,
		"duration", d.String(),
	)
}
