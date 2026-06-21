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

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	metricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz: %d %q", rec.Code, rec.Body.String())
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
	rec := do(newServer(emptyEngine(t), ""), http.MethodPost, "/notify/nope", "{}", map[string]string{"Content-Type": jsonCT})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestNonPostIs405(t *testing.T) {
	rec := do(newServer(emptyEngine(t), ""), http.MethodGet, "/notify/x", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestTokenAuth(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "s3cr3t")
	body := `{"status":"firing"}`

	if rec := do(srv, http.MethodPost, "/notify/alertmanager", body, map[string]string{"Content-Type": jsonCT}); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
	if rec := do(srv, http.MethodPost, "/notify/alertmanager", body, map[string]string{"Content-Type": jsonCT, "Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", rec.Code)
	}
	if rec := do(srv, http.MethodPost, "/notify/alertmanager", body, map[string]string{"Content-Type": jsonCT, "Authorization": "Bearer s3cr3t"}); rec.Code != http.StatusOK {
		t.Errorf("correct token: status = %d, want 200", rec.Code)
	}
}

// TestNoTokenConfiguredAcceptsUnauthenticated locks in the optional-token
// default: with no CHASKI_WEBHOOK_TOKEN, an unauthenticated request is accepted
// (the common case for cluster-internal senders).
func TestNoTokenConfiguredAcceptsUnauthenticated(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "")
	rec := do(srv, http.MethodPost, "/notify/alertmanager", `{"status":"firing"}`,
		map[string]string{"Content-Type": jsonCT})
	if rec.Code != http.StatusOK {
		t.Errorf("no token configured: status = %d, want 200 (auth disabled)", rec.Code)
	}
}

func TestRelayAndSkipStatuses(t *testing.T) {
	fn := &fakeNotifier{}
	srv := newServer(engineWithRoute(t, fn), "")

	if rec := do(srv, http.MethodPost, "/notify/alertmanager", `{"status":"firing"}`, map[string]string{"Content-Type": jsonCT}); rec.Code != http.StatusOK {
		t.Errorf("firing: status = %d, want 200", rec.Code)
	}
	if rec := do(srv, http.MethodPost, "/notify/alertmanager", `{"status":"resolved"}`, map[string]string{"Content-Type": jsonCT}); rec.Code != http.StatusNoContent {
		t.Errorf("resolved: status = %d, want 204", rec.Code)
	}
	if fn.calls != 1 {
		t.Errorf("notifier calls = %d, want 1 (fired once, skipped once)", fn.calls)
	}
}

func TestDryRunReturnsPlanWithoutSending(t *testing.T) {
	fn := &fakeNotifier{}
	srv := newServer(engineWithRoute(t, fn), "")
	rec := do(srv, http.MethodPost, "/notify/alertmanager?dryRun=1", `{"status":"firing"}`, map[string]string{"Content-Type": jsonCT})

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

	rec := do(srv, http.MethodPost, "/notify/alertmanager", `{"status":"firing"}`, map[string]string{"Content-Type": jsonCT})
	if got := rec.Header().Get("X-Chaski-Result"); got != "relayed" {
		t.Errorf("relayed header = %q, want relayed", got)
	}
	rec = do(srv, http.MethodPost, "/notify/alertmanager", `{"status":"resolved"}`, map[string]string{"Content-Type": jsonCT})
	if got := rec.Header().Get("X-Chaski-Result"); got != "skipped:gate" {
		t.Errorf("gate-false header = %q, want skipped:gate", got)
	}
}

func TestDryRunGateFalseReturnsPlan(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "")
	rec := do(srv, http.MethodPost, "/notify/alertmanager?dryRun=1", `{"status":"resolved"}`, map[string]string{"Content-Type": jsonCT})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (gate-false dry run still previews)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"fired":false`) {
		t.Errorf("gate-false dry-run body = %s, want fired:false", rec.Body.String())
	}
}

func TestRenderErrorSurfacesCause(t *testing.T) {
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

	rec := do(srv, http.MethodPost, "/notify/r", `{"x":"scalar"}`, map[string]string{"Content-Type": jsonCT})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "message") || strings.Contains(body, "Internal Server Error") {
		t.Errorf("500 body = %s, want the render cause (not the generic text)", body)
	}
	if got := rec.Header().Get("X-Chaski-Result"); got != "render_error" {
		t.Errorf("header = %q, want render_error", got)
	}
}

func TestUnsupportedContentTypeIs400(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "")
	rec := do(srv, http.MethodPost, "/notify/alertmanager", "hello", map[string]string{"Content-Type": "text/plain"})
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
