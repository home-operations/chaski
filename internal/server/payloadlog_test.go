package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/relay"
)

const payloadLogYAML = `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  tapped:
    target: po
    whenExpr: 'payload.status == "firing"'
    message: 'alert: {{ .payload.status }}'
    logPayload: true
  quiet:
    target: po
    message: 'x'
`

func payloadLogServer(t *testing.T, logUnknown bool) (*Server, *bytes.Buffer) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(file, []byte(payloadLogYAML), 0o644); err != nil {
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
	var buf bytes.Buffer
	srv := &Server{
		cfg: &config.Config{
			DisableRequestLogs: true,
			MaxBodyBytes:       1 << 20,
			RequestTimeout:     5 * time.Second,
			LogUnknownRoutes:   logUnknown,
		},
		engine: e,
		log:    slog.New(slog.NewJSONHandler(&buf, nil)),
	}
	return srv, &buf
}

func TestLogPayloadRoute(t *testing.T) {
	srv, buf := payloadLogServer(t, false)

	// Gate miss still logs — that is when the payload is needed most.
	do(srv, http.MethodPost, "/hooks/tapped", `{"status":"resolved"}`, map[string]string{"Content-Type": jsonCT})
	if !strings.Contains(buf.String(), `"payload":{"status":"resolved"}`) {
		t.Errorf("gate miss did not log the payload:\n%s", buf.String())
	}

	buf.Reset()
	do(srv, http.MethodPost, "/hooks/tapped", `{"status":"firing"}`, map[string]string{"Content-Type": jsonCT})
	if !strings.Contains(buf.String(), `"payload":{"status":"firing"}`) {
		t.Errorf("fired request did not log the payload:\n%s", buf.String())
	}
}

func TestLogPayloadOffByDefault(t *testing.T) {
	srv, buf := payloadLogServer(t, false)
	do(srv, http.MethodPost, "/hooks/quiet", `{"status":"firing"}`, map[string]string{"Content-Type": jsonCT})
	if strings.Contains(buf.String(), "inbound payload") {
		t.Errorf("route without logPayload logged a payload:\n%s", buf.String())
	}
}

func TestLogUnknownRoutes(t *testing.T) {
	srv, buf := payloadLogServer(t, true)
	rec := do(srv, http.MethodPost, "/hooks/ghost", `{"eventType":"Download"}`, map[string]string{"Content-Type": jsonCT})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 unchanged", rec.Code)
	}
	out := buf.String()
	if !strings.Contains(out, "unknown route") || !strings.Contains(out, `"payload":{"eventType":"Download"}`) {
		t.Errorf("unknown-route payload not logged:\n%s", out)
	}

	srv, buf = payloadLogServer(t, false)
	do(srv, http.MethodPost, "/hooks/ghost", `{"eventType":"Download"}`, map[string]string{"Content-Type": jsonCT})
	if strings.Contains(buf.String(), "unknown route") {
		t.Errorf("unknown-route logging fired while disabled:\n%s", buf.String())
	}
}
