package relay_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/relay"
	"github.com/home-operations/chaski/internal/sink"
)

type fakeNotifier struct {
	mu       sync.Mutex
	calls    int
	fail     bool
	lastBody string
}

func (f *fakeNotifier) Send(_ context.Context, _, body, _ string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastBody = body
	if f.fail {
		return errors.New("boom")
	}
	return nil
}

func engine(t *testing.T, yaml string, n sink.Notifier) *relay.Engine {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(file, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := config.LoadRouteConfig(file)
	if err != nil {
		t.Fatalf("LoadRouteConfig: %v", err)
	}
	cfg := &config.Config{RetryAttempts: 1, RetryBackoff: time.Millisecond, RequestTimeout: 5 * time.Second}
	e, err := relay.Build(rc, cfg, relay.Options{Notifier: n})
	if err != nil {
		t.Fatalf("relay.Build: %v", err)
	}
	return e
}

func route(t *testing.T, e *relay.Engine) *relay.Route {
	t.Helper()
	r, ok := e.Lookup("r")
	if !ok {
		t.Fatal(`route "r" not found`)
	}
	return r
}

const apprise1 = `
targets:
  po: { apprise: { url: 'pover://u@t/' } }
routes:
  r:
    target: po
    whenExpr: 'payload.status == "firing"'
    message: 'alert: {{ .payload.status }}'
`

func handle(r *relay.Route, payload any, dry bool) relay.Result {
	return r.Handle(context.Background(), relay.Input{
		Payload: payload, Method: "POST", Now: time.Unix(0, 0), DryRun: dry,
	})
}

func TestNamedTemplateRendersInField(t *testing.T) {
	const y = `
templates:
  label: '[{{ .payload.level }}]'
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  r:
    target: po
    message: '{{ template "label" . }} {{ .payload.text }}'
`
	fn := &fakeNotifier{}
	r := route(t, engine(t, y, fn))
	res := handle(r, map[string]any{"level": "WARN", "text": "disk full"}, false)
	if res.Kind != relay.Relayed {
		t.Fatalf("kind=%v err=%v, want Relayed", res.Kind, res.Err)
	}
	if fn.lastBody != "[WARN] disk full" {
		t.Errorf("body = %q, want %q", fn.lastBody, "[WARN] disk full")
	}
}

func TestBadNamedTemplateFailsBuild(t *testing.T) {
	const y = `
templates: { bad: '{{ .x. }}' }
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes: { r: { target: po, message: 'x' } }
`
	dir := t.TempDir()
	file := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(file, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := config.LoadRouteConfig(file)
	if err != nil {
		t.Fatalf("LoadRouteConfig: %v", err)
	}
	cfg := &config.Config{RetryAttempts: 1, RetryBackoff: time.Millisecond, RequestTimeout: time.Second}
	if _, err := relay.Build(rc, cfg, relay.Options{Notifier: &fakeNotifier{}}); err == nil {
		t.Fatal("relay.Build with a malformed template = nil error, want a parse error")
	}
}

// TestCyclicTemplateFailsBuild pins the headline fix: an include cycle (which at
// render would recurse until the goroutine stack overflows — an unrecoverable
// crash) is rejected at Build, i.e. boot / `chaski validate`, not at request time.
func TestCyclicTemplateFailsBuild(t *testing.T) {
	const y = `
templates:
  loop: '{{ include "loop" . }}'
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes: { r: { target: po, message: '{{ template "loop" . }}' } }
`
	dir := t.TempDir()
	file := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(file, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := config.LoadRouteConfig(file)
	if err != nil {
		t.Fatalf("LoadRouteConfig: %v", err)
	}
	cfg := &config.Config{RetryAttempts: 1, RetryBackoff: time.Millisecond, RequestTimeout: time.Second}
	if _, err := relay.Build(rc, cfg, relay.Options{Notifier: &fakeNotifier{}}); err == nil {
		t.Fatal("relay.Build with a template cycle = nil error, want a cycle error")
	}
}

const perTargetWhenExpr = `
targets:
  a: { apprise: { url: 'pover://u@t/a' } }
  b: { apprise: { url: 'pover://u@t/b' } }
routes:
  r:
    message: 'm'
    target:
      - { name: a, whenExpr: 'payload.to == "a"' }
      - { name: b, whenExpr: 'payload.to == "b"' }
`

// planTarget finds a named target in a dry-run plan.
func planTarget(t *testing.T, p *relay.Plan, name string) relay.PlanTarget {
	t.Helper()
	for _, pt := range p.Targets {
		if pt.Name == name {
			return pt
		}
	}
	t.Fatalf("target %q not in plan %+v", name, p.Targets)
	return relay.PlanTarget{}
}

// TestPerTargetWhenExpr pins per-target conditional fan-out: a single route
// delivers only to the targets whose own whenExpr matches the request.
func TestPerTargetWhenExpr(t *testing.T) {
	r := route(t, engine(t, perTargetWhenExpr, &fakeNotifier{}))

	// Dry run for {to:a}: target a fires, target b is skipped.
	res := handle(r, map[string]any{"to": "a"}, true)
	if res.Kind != relay.DryRunned {
		t.Fatalf("kind=%v err=%v, want DryRunned", res.Kind, res.Err)
	}
	if got := planTarget(t, res.Plan, "a"); got.Skipped {
		t.Errorf("target a skipped for to=a, want delivered")
	}
	if got := planTarget(t, res.Plan, "b"); !got.Skipped {
		t.Errorf("target b delivered for to=a, want skipped")
	}

	// Live: exactly one target (a) is sent to.
	fn := &fakeNotifier{}
	r = route(t, engine(t, perTargetWhenExpr, fn))
	if res := handle(r, map[string]any{"to": "a"}, false); res.Kind != relay.Relayed {
		t.Fatalf("kind=%v err=%v, want Relayed", res.Kind, res.Err)
	}
	if fn.calls != 1 {
		t.Errorf("sends = %d, want 1 (only target a)", fn.calls)
	}
}

// TestPerTargetWhenExprNoneMatch: route fires but no target matches ⇒ skip.
func TestPerTargetWhenExprNoneMatch(t *testing.T) {
	fn := &fakeNotifier{}
	r := route(t, engine(t, perTargetWhenExpr, fn))
	res := handle(r, map[string]any{"to": "nobody"}, false)
	if res.Kind != relay.Skipped || res.Reason != "no_targets" {
		t.Fatalf("kind=%v reason=%q, want Skipped/no_targets", res.Kind, res.Reason)
	}
	if fn.calls != 0 {
		t.Errorf("sends = %d, want 0", fn.calls)
	}
}

// TestPerTargetWhenExprError: a target gate fault is an operator error ⇒ 500.
func TestPerTargetWhenExprError(t *testing.T) {
	const y = `
targets:
  a: { apprise: { url: 'pover://u@t/a' } }
routes:
  r:
    message: 'm'
    target:
      - { name: a, whenExpr: 'payload.items[5] == "x"' }
`
	r := route(t, engine(t, y, &fakeNotifier{}))
	res := handle(r, map[string]any{"items": []any{}}, false)
	if res.Kind != relay.GateError || res.Status != 500 {
		t.Fatalf("kind=%v status=%d, want GateError/500", res.Kind, res.Status)
	}
}

// TestStaticFanoutUnchanged: a bare target list still fans out to all targets.
func TestStaticFanoutUnchanged(t *testing.T) {
	const y = `
targets:
  a: { apprise: { url: 'pover://u@t/a' } }
  b: { apprise: { url: 'pover://u@t/b' } }
routes:
  r: { message: 'm', target: [a, b] }
`
	fn := &fakeNotifier{}
	r := route(t, engine(t, y, fn))
	if res := handle(r, map[string]any{}, false); res.Kind != relay.Relayed {
		t.Fatalf("kind=%v, want Relayed", res.Kind)
	}
	if fn.calls != 2 {
		t.Errorf("sends = %d, want 2 (both targets)", fn.calls)
	}
}

func TestGateSkips(t *testing.T) {
	fn := &fakeNotifier{}
	r := route(t, engine(t, apprise1, fn))

	res := handle(r, map[string]any{"status": "resolved"}, false)
	if res.Kind != relay.Skipped || res.Status != 204 {
		t.Errorf("kind=%v status=%d, want Skipped 204", res.Kind, res.Status)
	}
	if res.Reason != "gate" {
		t.Errorf("skip reason = %q, want gate", res.Reason)
	}
	if fn.calls != 0 {
		t.Errorf("notifier called %d times on a skip", fn.calls)
	}
}

func TestDryRunGateFalseReturnsPlan(t *testing.T) {
	fn := &fakeNotifier{}
	r := route(t, engine(t, apprise1, fn))

	res := handle(r, map[string]any{"status": "resolved"}, true) // gate is false
	if res.Kind != relay.DryRunned || res.Status != 200 || res.Plan == nil {
		t.Fatalf("kind=%v status=%d plan=%v, want DryRunned 200 with a plan", res.Kind, res.Status, res.Plan)
	}
	if res.Plan.Fired {
		t.Error("Plan.Fired = true, want false for a gate-false dry run")
	}
	if len(res.Plan.Targets) != 0 {
		t.Errorf("Plan.Targets = %+v, want empty when the gate is false", res.Plan.Targets)
	}
	if fn.calls != 0 {
		t.Errorf("gate-false dry run sent %d notifications", fn.calls)
	}
}

func TestRelayed(t *testing.T) {
	fn := &fakeNotifier{}
	r := route(t, engine(t, apprise1, fn))

	res := handle(r, map[string]any{"status": "firing"}, false)
	if res.Kind != relay.Relayed || res.Status != 200 {
		t.Fatalf("kind=%v status=%d err=%v, want Relayed 200", res.Kind, res.Status, res.Err)
	}
	if fn.calls != 1 || fn.lastBody != "alert: firing" {
		t.Errorf("notifier calls=%d body=%q", fn.calls, fn.lastBody)
	}
}

func TestMessageRenderErrorIs500(t *testing.T) {
	const y = `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  r:
    target: po
    message: '{{ .payload.name.Field }}'
`
	r := route(t, engine(t, y, &fakeNotifier{}))
	res := handle(r, map[string]any{"name": "scalar"}, false)
	if res.Kind != relay.RenderError || res.Status != 500 {
		t.Errorf("kind=%v status=%d, want RenderError 500", res.Kind, res.Status)
	}
}

func TestEmptyMessageSkipsApprise(t *testing.T) {
	// An empty message body makes for a pointless notification, so the apprise
	// send is skipped and the route reports Skipped.
	const y = `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  r: { target: po, message: '' }
`
	fn := &fakeNotifier{}
	r := route(t, engine(t, y, fn))
	res := handle(r, map[string]any{}, false)
	if res.Kind != relay.Skipped || fn.calls != 0 {
		t.Errorf("empty apprise body: kind=%v calls=%d, want Skipped 0", res.Kind, fn.calls)
	}
	if res.Reason != "no_targets" {
		t.Errorf("skip reason = %q, want no_targets", res.Reason)
	}
}

func TestDryRunDoesNotSend(t *testing.T) {
	fn := &fakeNotifier{}
	r := route(t, engine(t, apprise1, fn))
	res := handle(r, map[string]any{"status": "firing"}, true)
	if res.Kind != relay.DryRunned || res.Plan == nil {
		t.Fatalf("kind=%v plan=%v, want DryRunned with a plan", res.Kind, res.Plan)
	}
	if fn.calls != 0 {
		t.Errorf("dry run sent %d notifications", fn.calls)
	}
	if len(res.Plan.Targets) != 1 || res.Plan.Targets[0].Body != "alert: firing" {
		t.Errorf("plan = %+v", res.Plan)
	}
}

func TestFanOutAllSucceedElse502(t *testing.T) {
	const y = `
targets:
  a: { apprise: { url: 'pover://a@t/' } }
  b: { apprise: { url: 'pover://b@t/' } }
routes:
  r: { target: [a, b], message: 'hi' }
`
	t.Run("all ok -> 200", func(t *testing.T) {
		fn := &fakeNotifier{}
		res := handle(route(t, engine(t, y, fn)), map[string]any{}, false)
		if res.Status != 200 || fn.calls != 2 {
			t.Errorf("status=%d calls=%d, want 200 and 2", res.Status, fn.calls)
		}
	})
	t.Run("one fails -> 502", func(t *testing.T) {
		fn := &fakeNotifier{fail: true}
		res := handle(route(t, engine(t, y, fn)), map[string]any{}, false)
		if res.Kind != relay.RelayError || res.Status != 502 {
			t.Errorf("kind=%v status=%d, want RelayError 502", res.Kind, res.Status)
		}
	})
}

func TestResponseStatusOverride(t *testing.T) {
	const y = `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  r:
    target: po
    message: 'hi'
    response: { status: 201, skipStatus: 200 }
    whenExpr: 'payload.go == true'
`
	r := route(t, engine(t, y, &fakeNotifier{}))
	if res := handle(r, map[string]any{"go": true}, false); res.Status != 201 {
		t.Errorf("relay status = %d, want 201 (override)", res.Status)
	}
	if res := handle(r, map[string]any{"go": false}, false); res.Status != 200 {
		t.Errorf("skip status = %d, want 200 (override)", res.Status)
	}
}
