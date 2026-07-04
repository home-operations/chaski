package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/relay"
)

type fakeNotifier struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeNotifier) Send(context.Context, string, string, string, map[string]string) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return nil
}

func emptyEngine(t *testing.T) *relay.Engine {
	t.Helper()
	e, err := relay.Build(
		&config.RouteConfig{Routes: map[string]*config.Route{}, Targets: map[string]*config.Target{}},
		&config.Config{RetryAttempts: 1},
		relay.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

const routeYAML = `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  alertmanager:
    target: po
    whenExpr: 'payload.status == "firing"'
    message: 'alert: {{ .payload.status }}'
`

func engineWithRoute(t *testing.T, n *fakeNotifier) *relay.Engine {
	t.Helper()
	file := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(file, []byte(routeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := config.LoadRouteConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	e, err := relay.Build(rc, &config.Config{RetryAttempts: 1, RetryBackoff: time.Millisecond}, relay.Options{Notifier: n})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func newServer(engine *relay.Engine, token string) *Server {
	return &Server{
		cfg: &config.Config{
			DisableRequestLogs: true,
			MaxBodyBytes:       1 << 20,
			RequestTimeout:     5 * time.Second,
			WebhookToken:       token,
		},
		engine: engine,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func do(srv *Server, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)
	return rec
}

const jsonCT = "application/json"

// capHandler records the level of each emitted log record.
type capHandler struct{ levels *[]slog.Level }

func (h capHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h capHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h capHandler) WithGroup(string) slog.Handler            { return h }
func (h capHandler) Handle(_ context.Context, r slog.Record) error {
	*h.levels = append(*h.levels, r.Level)
	return nil
}

// TestRequestLogLevels: monitoring endpoints log at Debug, real traffic at Info,
// and DisableRequestLogs silences everything.
func TestRequestLogLevels(t *testing.T) {
	cases := map[string]slog.Level{
		"/healthz": slog.LevelDebug,
		"/readyz":  slog.LevelDebug,
		"/metrics": slog.LevelDebug,
		"/hooks/x": slog.LevelInfo,
		"/":        slog.LevelInfo,
	}
	for path, want := range cases {
		var levels []slog.Level
		srv := &Server{cfg: &config.Config{}, log: slog.New(capHandler{&levels})}
		srv.accessLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if len(levels) != 1 || levels[0] != want {
			t.Errorf("path %q: levels=%v, want [%v]", path, levels, want)
		}
	}

	var levels []slog.Level
	srv := &Server{cfg: &config.Config{DisableRequestLogs: true}, log: slog.New(capHandler{&levels})}
	srv.accessLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if len(levels) != 0 {
		t.Errorf("DisableRequestLogs: emitted %d logs, want 0", len(levels))
	}
}

// Health lives on the main webhook listener (the chart's probes target the
// http port), NOT the optional metrics listener — so disabling metrics can
// never break the probes. /readyz aliases /healthz (the pair standard).
func TestHealthOnMainPortNotMetricsPort(t *testing.T) {
	srv := newServer(emptyEngine(t), "")
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
			t.Fatalf("main handler %s: %d %q", path, rec.Code, rec.Body.String())
		}
	}

	// The metrics handler is metrics-only.
	rec := httptest.NewRecorder()
	metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("metrics handler /healthz status = %d, want 404 (metrics-only listener)", rec.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	rec := httptest.NewRecorder()
	metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", rec.Code)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	rec := do(newServer(emptyEngine(t), ""), http.MethodPost, "/hooks/nope", "{}", map[string]string{"Content-Type": jsonCT})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestNonPostIs405(t *testing.T) {
	rec := do(newServer(emptyEngine(t), ""), http.MethodGet, "/hooks/x", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestTokenAuth(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "s3cr3t")
	body := `{"status":"firing"}`

	if rec := do(srv, http.MethodPost, "/hooks/alertmanager", body, map[string]string{"Content-Type": jsonCT}); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
	if rec := do(srv, http.MethodPost, "/hooks/alertmanager", body, map[string]string{"Content-Type": jsonCT, "Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", rec.Code)
	}
	if rec := do(srv, http.MethodPost, "/hooks/alertmanager", body, map[string]string{"Content-Type": jsonCT, "Authorization": "Bearer s3cr3t"}); rec.Code != http.StatusOK {
		t.Errorf("correct token: status = %d, want 200", rec.Code)
	}
}

// TestNoTokenConfiguredAcceptsUnauthenticated locks in the optional-token
// default: with no CHASKI_WEBHOOK_TOKEN, an unauthenticated request is accepted
// (the common case for cluster-internal senders).
func TestNoTokenConfiguredAcceptsUnauthenticated(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "")
	rec := do(srv, http.MethodPost, "/hooks/alertmanager", `{"status":"firing"}`,
		map[string]string{"Content-Type": jsonCT})
	if rec.Code != http.StatusOK {
		t.Errorf("no token configured: status = %d, want 200 (auth disabled)", rec.Code)
	}
}

func TestRelayAndSkipStatuses(t *testing.T) {
	fn := &fakeNotifier{}
	srv := newServer(engineWithRoute(t, fn), "")

	if rec := do(srv, http.MethodPost, "/hooks/alertmanager", `{"status":"firing"}`, map[string]string{"Content-Type": jsonCT}); rec.Code != http.StatusOK {
		t.Errorf("firing: status = %d, want 200", rec.Code)
	}
	if rec := do(srv, http.MethodPost, "/hooks/alertmanager", `{"status":"resolved"}`, map[string]string{"Content-Type": jsonCT}); rec.Code != http.StatusNoContent {
		t.Errorf("resolved: status = %d, want 204", rec.Code)
	}
	if fn.calls != 1 {
		t.Errorf("notifier calls = %d, want 1 (fired once, skipped once)", fn.calls)
	}
}

func TestDryRunReturnsPlanWithoutSending(t *testing.T) {
	fn := &fakeNotifier{}
	srv := newServer(engineWithRoute(t, fn), "")
	rec := do(srv, http.MethodPost, "/hooks/alertmanager?dryRun=1", `{"status":"firing"}`, map[string]string{"Content-Type": jsonCT})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"alert: firing"`) || !strings.Contains(rec.Body.String(), `"fired":true`) {
		t.Errorf("dry-run plan = %s", rec.Body.String())
	}
	if fn.calls != 0 {
		t.Errorf("dry run sent %d notifications", fn.calls)
	}
}

func TestResultHeader(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "")

	rec := do(srv, http.MethodPost, "/hooks/alertmanager", `{"status":"firing"}`, map[string]string{"Content-Type": jsonCT})
	if got := rec.Header().Get("X-Chaski-Result"); got != "relayed" {
		t.Errorf("relayed header = %q, want relayed", got)
	}
	rec = do(srv, http.MethodPost, "/hooks/alertmanager", `{"status":"resolved"}`, map[string]string{"Content-Type": jsonCT})
	if got := rec.Header().Get("X-Chaski-Result"); got != "skipped:gate" {
		t.Errorf("gate-false header = %q, want skipped:gate", got)
	}
}

func TestDryRunGateFalseReturnsPlan(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "")
	rec := do(srv, http.MethodPost, "/hooks/alertmanager?dryRun=1", `{"status":"resolved"}`, map[string]string{"Content-Type": jsonCT})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (gate-false dry run still previews)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"fired":false`) {
		t.Errorf("gate-false dry-run body = %s, want fired:false", rec.Body.String())
	}
}

func TestRenderErrorIsGenericToClient(t *testing.T) {
	const y = `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  r: { target: po, message: '{{ .payload.x.Bad }}' }
`
	file := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(file, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := config.LoadRouteConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	e, err := relay.Build(rc, &config.Config{RetryAttempts: 1, RetryBackoff: time.Millisecond}, relay.Options{Notifier: &fakeNotifier{}})
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(e, "")

	rec := do(srv, http.MethodPost, "/hooks/r", `{"x":"scalar"}`, map[string]string{"Content-Type": jsonCT})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	// Generic to the client; the operator's template source must not leak.
	if !strings.Contains(body, "Internal Server Error") {
		t.Errorf("500 body = %s, want the generic status text", body)
	}
	if strings.Contains(body, "Bad") || strings.Contains(body, "payload") {
		t.Errorf("500 body leaks template source: %s", body)
	}
	// The low-cardinality outcome label is still exposed (not the cause).
	if got := rec.Header().Get("X-Chaski-Result"); got != "render_error" {
		t.Errorf("header = %q, want render_error", got)
	}
}

func TestUnsupportedContentTypeIs400(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "")
	rec := do(srv, http.MethodPost, "/hooks/alertmanager", "hello", map[string]string{"Content-Type": "text/plain"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRecovererTurnsPanicInto500(t *testing.T) {
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := recoverer(slog.New(slog.NewTextHandler(io.Discard, nil)))(panicky)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
