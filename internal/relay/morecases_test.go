package relay_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/chaski/internal/relay"
)

// TestHTTPForwardsBodyVerbatimWhenMessageOmitted checks the http-sink path where
// a route omits `message`: the inbound body is forwarded verbatim. The dry-run
// plan surfaces what would be sent, so it exercises the same messageFor logic.
func TestHTTPForwardsBodyVerbatimWhenMessageOmitted(t *testing.T) {
	const y = `
targets: { fwd: { http: { url: 'https://example.internal/ingest' } } }
routes:
  r: { target: fwd }
`
	r := route(t, engine(t, y, &fakeNotifier{}))
	raw := []byte(`{"original":"payload"}`)
	res := r.Handle(context.Background(), relay.Input{
		Payload:     map[string]any{"original": "payload"},
		RawBody:     raw,
		ContentType: "application/json",
		Method:      "POST",
		Now:         time.Unix(0, 0),
		DryRun:      true,
	})
	if res.Plan == nil || len(res.Plan.Targets) != 1 {
		t.Fatalf("plan = %+v", res.Plan)
	}
	if got := res.Plan.Targets[0].Body; got != string(raw) {
		t.Errorf("http body = %q, want the inbound body verbatim %q", got, raw)
	}
}

// TestTitleRenderErrorIsDroppedNotFatal confirms a failing optional field (title)
// is dropped and the relay proceeds — only a failing message is fatal.
func TestTitleRenderErrorIsDroppedNotFatal(t *testing.T) {
	const y = `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  r: { target: po, title: '{{ .payload.name.nope }}', message: 'hi' }
`
	fn := &fakeNotifier{}
	r := route(t, engine(t, y, fn))
	res := handle(r, map[string]any{"name": "x"}, false)
	if res.Kind != relay.Relayed || res.Status != 200 {
		t.Fatalf("kind=%v status=%d, want Relayed 200 (title dropped, not fatal)", res.Kind, res.Status)
	}
	if fn.calls != 1 {
		t.Errorf("calls = %d, want 1 (relayed despite the dropped title)", fn.calls)
	}
	if _, ok := res.Dropped["title"]; !ok {
		t.Errorf("Dropped = %v, want title reported", res.Dropped)
	}
}

// TestDryRunPlanOmitsTargetCredentials is a regression guard: the dry-run plan
// must never echo a target URL or its credentials.
func TestDryRunPlanOmitsTargetCredentials(t *testing.T) {
	r := route(t, engine(t, apprise1, &fakeNotifier{}))
	res := handle(r, map[string]any{"status": "firing"}, true)
	if res.Plan == nil {
		t.Fatal("want a plan")
	}
	b, err := json.Marshal(res.Plan)
	if err != nil {
		t.Fatal(err)
	}
	// apprise1's target is pover://u@t/ — neither the scheme+creds nor the userinfo
	// should appear anywhere in the plan.
	if s := string(b); strings.Contains(s, "pover://") || strings.Contains(s, "u@t") {
		t.Errorf("dry-run plan leaked target credentials: %s", s)
	}
}
