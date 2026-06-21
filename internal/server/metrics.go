package server

import (
	"net/http"
	"runtime"
	"strconv"

	"github.com/home-operations/chaski/internal/relay"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chaski_http_requests_total",
		Help: "Total number of inbound HTTP requests handled, by method and status class.",
	}, []string{"method", "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "chaski_http_request_duration_seconds",
		Help:    "Inbound HTTP request duration in seconds, by method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method"})

	relayResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chaski_relays_total",
		Help: "Relay outcomes, by route and result (relayed/skipped/dryrun/*_error).",
	}, []string{"route", "result"})

	// webhookRejected counts inbound requests refused before relay, by reason
	// (the why behind a 4xx, which the status-class label can't convey). Mirrors
	// the SMTP path's chaski_smtp_rejected_total.
	webhookRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chaski_webhook_rejected_total",
		Help: "Inbound webhook requests rejected before relay, by reason.",
	}, []string{"reason"})

	buildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "chaski_build_info",
		Help: "Build metadata; the value is always 1, the version/commit/goversion are labels.",
	}, []string{"version", "commit", "goversion"})
)

// RecordBuildInfo sets the chaski_build_info gauge from the stamped build vars,
// so the running build is queryable from Prometheus, not just the boot log.
func RecordBuildInfo(version, commit string) {
	buildInfo.WithLabelValues(version, commit, runtime.Version()).Set(1)
}

// observeRelay records a relay outcome and logs operator-fault errors and any
// dropped optional fields. Payload bodies and rendered secrets/URLs are
// never logged.
func (s *Server) observeRelay(route string, res relay.Result) {
	relayResults.WithLabelValues(route, res.Kind.String()).Inc()
	if res.Err != nil {
		s.log.Error("relay error", "route", route, "result", res.Kind.String(), "error", res.Err)
	}
	for field, err := range res.Dropped {
		s.log.Warn("dropped rendered field", "route", route, "field", field, "error", err)
	}
}

// metricsHandler serves Prometheus metrics and the health/readiness probe on
// the dedicated monitoring listener, so scraping and probing share one port
// (8081 by default) separate from the public webhook port.
func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", handleHealth)
	return mux
}

// statusClass buckets a status code into a low-cardinality label (e.g. "2xx").
func statusClass(code int) string {
	return strconv.Itoa(code/100) + "xx"
}

// knownMethods bounds the method metric label. A client can send an arbitrary
// request method, so unrecognised ones collapse to "other" — otherwise distinct
// method strings would grow the metric's cardinality without bound.
var knownMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {},
	http.MethodPut: {}, http.MethodPatch: {}, http.MethodDelete: {},
	http.MethodConnect: {}, http.MethodOptions: {}, http.MethodTrace: {},
}

func methodLabel(method string) string {
	if _, ok := knownMethods[method]; ok {
		return method
	}
	return "other"
}
