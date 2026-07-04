package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// captureServer records the last request body and counts hits.
func captureServer(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Pointer[string]) {
	t.Helper()
	var hits atomic.Int32
	var body atomic.Pointer[string]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		body.Store(&s)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &body
}

func sendCfg(url string) string {
	return fmt.Sprintf(`
targets: { ops: { http: { url: '%s' } } }
routes:
  r:
    target: ops
    whenExpr: 'payload.status == "firing"'
    message: 'alert: {{ .payload.status }}'
`, url)
}

func TestSendDelivers(t *testing.T) {
	srv, hits, body := captureServer(t)
	var buf bytes.Buffer
	if err := runSend(&buf, writeCfg(t, sendCfg(srv.URL)), writePayload(t, `{"status":"firing"}`), ""); err != nil {
		t.Fatalf("runSend: %v", err)
	}
	if !strings.Contains(buf.String(), "relayed") {
		t.Errorf("want relayed outcome:\n%s", buf.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1", hits.Load())
	}
	if got := *body.Load(); got != "alert: firing" {
		t.Errorf("delivered body = %q, want %q", got, "alert: firing")
	}
}

func TestSendGateFalseSkipsWithoutSending(t *testing.T) {
	srv, hits, _ := captureServer(t)
	var buf bytes.Buffer
	if err := runSend(&buf, writeCfg(t, sendCfg(srv.URL)), writePayload(t, `{"status":"resolved"}`), ""); err != nil {
		t.Fatalf("runSend: %v", err)
	}
	if !strings.Contains(buf.String(), "skipped (gate)") {
		t.Errorf("want skipped (gate) outcome:\n%s", buf.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("target hits = %d, want 0 on a gate miss", hits.Load())
	}
}

func TestSendRequiresPayload(t *testing.T) {
	srv, _, _ := captureServer(t)
	err := runSend(io.Discard, writeCfg(t, sendCfg(srv.URL)), "", "")
	if err == nil || !strings.Contains(err.Error(), "--payload") {
		t.Fatalf("want a --payload-required error, got %v", err)
	}
}
