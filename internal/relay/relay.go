// Package relay is the heart of chaski: it compiles every route once at load
// (gate + field templates + verify + resolved target sinks — fail-fast) and runs
// the per-request pipeline — verify, gate, render, relay (with concurrent
// fan-out) — mapping the outcome to the response contract.
package relay

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/gate"
	"github.com/home-operations/chaski/internal/render"
	"github.com/home-operations/chaski/internal/sink"
	"github.com/home-operations/chaski/internal/verify"
	"golang.org/x/sync/errgroup"
)

const (
	defaultOKStatus   = http.StatusOK        // 200, overridable via response.status
	defaultSkipStatus = http.StatusNoContent // 204, overridable via response.skipStatus
)

// Engine holds the compiled routes (and the sinks they share).
type Engine struct {
	routes map[string]*Route
}

// Options inject cross-cutting dependencies (the apprise Notifier, swappable for
// tests/backends).
type Options struct {
	Notifier sink.Notifier
}

// Build compiles the whole config into an Engine, failing fast on any bad gate,
// template, verify block, or target reference.
func Build(rc *config.RouteConfig, cfg *config.Config, opts Options) (*Engine, error) {
	sinkOpts := sink.Options{
		Notifier:       opts.Notifier,
		DefaultRetry:   sink.RetryPolicy{Attempts: cfg.RetryAttempts, Backoff: cfg.RetryBackoff},
		DefaultTimeout: cfg.RequestTimeout, // http target default when it sets no timeout
	}

	sinks := make(map[string]sink.Sink, len(rc.Targets))
	for name, t := range rc.Targets {
		s, err := sink.New(name, t, sinkOpts)
		if err != nil {
			return nil, err
		}
		sinks[name] = s
	}

	// The shared named-template snippets, compiled once and reused by every
	// route's field templates.
	set, err := render.NewSet(rc.Templates)
	if err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}

	routes := make(map[string]*Route, len(rc.Routes))
	for name, rt := range rc.Routes {
		cr, err := buildRoute(name, rt, sinks, set)
		if err != nil {
			return nil, fmt.Errorf("relay: route %q (%s): %w", name, rt.Source, err)
		}
		routes[name] = cr
	}
	return &Engine{routes: routes}, nil
}

// Lookup returns the compiled route for name.
func (e *Engine) Lookup(name string) (*Route, bool) {
	r, ok := e.routes[name]
	return r, ok
}

// RouteCount reports how many routes are configured (0 → the server boots idle).
func (e *Engine) RouteCount() int { return len(e.routes) }

// routeTarget pairs a resolved sink with its per-target gate. The gate (from the
// target's whenExpr, empty ⇒ always fires) selects whether this sink receives a
// given request, so one route can fan out to a request-dependent subset.
type routeTarget struct {
	sink sink.Sink
	gate *gate.Gate
}

// Route is one compiled route.
type Route struct {
	name      string
	gate      *gate.Gate
	verifier  *verify.Verifier
	title     *render.Template // nil if absent
	message   *render.Template // nil if omitted (verbatim forward for http)
	params    *render.Map
	headers   *render.Map
	targets   []routeTarget
	okStatus  int
	skipState int
}

func buildRoute(name string, rt *config.Route, sinks map[string]sink.Sink, set *render.Set) (*Route, error) {
	g, err := gate.Compile(rt.WhenExpr)
	if err != nil {
		return nil, err
	}
	vf, err := verify.Compile(rt.Verify)
	if err != nil {
		return nil, err
	}

	var title *render.Template
	if rt.Title != "" {
		if title, err = set.Compile("title", rt.Title); err != nil {
			return nil, err
		}
	}
	var message *render.Template
	if rt.Message != nil {
		if message, err = set.Compile("message", *rt.Message); err != nil {
			return nil, err
		}
	}
	params, err := set.CompileMap("params", rt.Params)
	if err != nil {
		return nil, err
	}
	headers, err := set.CompileMap("headers", rt.Headers)
	if err != nil {
		return nil, err
	}

	targets := make([]routeTarget, 0, len(rt.Target))
	for _, ref := range rt.Target {
		s, ok := sinks[ref.Name]
		if !ok {
			return nil, fmt.Errorf("unknown target %q", ref.Name) // defensive; config.Validate already checks
		}
		tg, err := gate.Compile(ref.WhenExpr)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", ref.Name, err)
		}
		targets = append(targets, routeTarget{sink: s, gate: tg})
	}

	ok, skip := defaultOKStatus, defaultSkipStatus
	if rt.Response != nil {
		if rt.Response.Status != 0 {
			ok = rt.Response.Status
		}
		if rt.Response.SkipStatus != 0 {
			skip = rt.Response.SkipStatus
		}
	}

	return &Route{
		name: name, gate: g, verifier: vf,
		title: title, message: message, params: params, headers: headers,
		targets: targets, okStatus: ok, skipState: skip,
	}, nil
}

// Verify runs the route's inbound signature check (a nil verifier accepts).
func (r *Route) Verify(h http.Header, rawBody []byte) bool {
	return r.verifier.Verify(h, rawBody)
}

// Input is the per-request data after auth + decode.
type Input struct {
	Payload     any
	RawBody     []byte
	ContentType string
	Headers     map[string]string // lower-cased; for gate + render
	Query       map[string]string
	Method      string
	Now         time.Time
	DryRun      bool
}

// Kind classifies the outcome for the HTTP status mapping and metrics.
type Kind int

const (
	Relayed     Kind = iota // sent to ≥1 target
	Skipped                 // whenExpr false, or every target skipped
	DryRunned               // plan computed, nothing sent
	GateError               // operator fault → 500
	RenderError             // operator fault → 500
	RelayError              // a downstream failed → 502
)

// String is the low-cardinality metric/log label for a Kind.
func (k Kind) String() string {
	switch k {
	case Relayed:
		return "relayed"
	case Skipped:
		return "skipped"
	case DryRunned:
		return "dryrun"
	case GateError:
		return "gate_error"
	case RenderError:
		return "render_error"
	case RelayError:
		return "relay_error"
	default:
		return "unknown"
	}
}

// Result is what the handler turns into an HTTP response.
type Result struct {
	Status  int
	Kind    Kind
	Reason  string           // low-cardinality sub-reason for a Skipped result: "gate" or "no_targets"
	Plan    *Plan            // set for a dry run
	Err     error            // gate/render/relay error (for logging)
	Dropped map[string]error // optional fields dropped at render (for logging)
}

// Handle runs verify-less pipeline (gate → render → relay) for an already
// verified+decoded request and returns the response Result. The deadline is the
// caller's ctx.
func (r *Route) Handle(ctx context.Context, in Input) Result {
	gi := gate.Input{
		Payload: in.Payload, Headers: in.Headers, Query: in.Query,
		Method: in.Method, Route: r.name, Now: in.Now,
	}
	fired, err := r.gate.Eval(ctx, gi)
	if err != nil {
		return Result{Status: http.StatusInternalServerError, Kind: GateError, Err: err}
	}
	if !fired {
		// A dry run still answers "why" — return a plan marked not-fired rather
		// than a bodyless skip, since a false gate is the most common non-action.
		if in.DryRun {
			return Result{Status: http.StatusOK, Kind: DryRunned, Plan: &Plan{Route: r.name, Fired: false}}
		}
		return Result{Status: r.skipState, Kind: Skipped, Reason: "gate"}
	}

	// Per-target gates pick the fan-out subset against the same variables as the
	// route gate. A target gate fault is an operator error (→ 500), like the route.
	matched, err := r.matchTargets(ctx, gi)
	if err != nil {
		return Result{Status: http.StatusInternalServerError, Kind: GateError, Err: err}
	}

	rr, err := r.renderAll(in)
	if err != nil {
		return Result{Status: http.StatusInternalServerError, Kind: RenderError, Err: err, Dropped: rr.dropped}
	}

	if in.DryRun {
		return Result{Status: http.StatusOK, Kind: DryRunned, Plan: r.plan(rr, in, matched), Dropped: rr.dropped}
	}

	sent, err := r.fanOut(ctx, rr, in, matched)
	if err != nil {
		return Result{Status: http.StatusBadGateway, Kind: RelayError, Err: err, Dropped: rr.dropped}
	}
	if sent == 0 {
		return Result{Status: r.skipState, Kind: Skipped, Reason: "no_targets", Dropped: rr.dropped}
	}
	return Result{Status: r.okStatus, Kind: Relayed, Dropped: rr.dropped}
}

// matchTargets evaluates each target's gate against gi, returning a parallel
// mask of which targets the request fans out to. The first gate fault aborts.
func (r *Route) matchTargets(ctx context.Context, gi gate.Input) ([]bool, error) {
	matched := make([]bool, len(r.targets))
	for i, t := range r.targets {
		ok, err := t.gate.Eval(ctx, gi)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", t.sink.Name(), err)
		}
		matched[i] = ok
	}
	return matched, nil
}

// rendered holds a route's rendered fields for one request.
type rendered struct {
	title          string
	message        string
	messageOmitted bool
	params         map[string]string
	headers        map[string]string
	dropped        map[string]error
}

func (r *Route) renderAll(in Input) (rendered, error) {
	d := render.Data{
		Payload: in.Payload, Headers: in.Headers, Query: in.Query,
		Method: in.Method, Route: r.name, Now: in.Now,
	}
	rr := rendered{messageOmitted: r.message == nil}

	if r.title != nil {
		if v, err := r.title.Render(d); err != nil {
			rr.addDropped("title", err) // optional: drop and proceed
		} else {
			rr.title = v
		}
	}
	if r.message != nil {
		v, err := r.message.Render(d)
		if err != nil {
			return rr, fmt.Errorf("message: %w", err) // required: fail the relay
		}
		rr.message = v
	}

	var dropped map[string]error
	rr.params, dropped = r.params.Render(d)
	rr.mergeDropped("params", dropped)
	rr.headers, dropped = r.headers.Render(d)
	rr.mergeDropped("headers", dropped)

	return rr, nil
}

func (rr *rendered) addDropped(key string, err error) {
	if rr.dropped == nil {
		rr.dropped = make(map[string]error)
	}
	rr.dropped[key] = err
}

func (rr *rendered) mergeDropped(prefix string, dropped map[string]error) {
	for k, err := range dropped {
		rr.addDropped(prefix+"."+k, err)
	}
}

// fanOut relays to every gate-matched, non-skipped target concurrently. It
// returns the number attempted (0 → overall skip) and the first error (→ 502). A
// target whose whenExpr is false is skipped; an apprise target with an empty body
// is skipped; an http target always sends.
func (r *Route) fanOut(ctx context.Context, rr rendered, in Input, matched []bool) (int, error) {
	type job struct {
		s   sink.Sink
		msg sink.Message
	}
	var jobs []job
	for i, t := range r.targets {
		if !matched[i] {
			continue
		}
		if msg, ok := messageFor(t.sink, rr, in); ok {
			jobs = append(jobs, job{t.sink, msg})
		}
	}
	if len(jobs) == 0 {
		return 0, nil
	}

	// Plain errgroup (not WithContext): one target failing must not cancel the
	// other targets' in-flight sends and retries — each matched target gets its
	// full attempt, bounded only by the request ctx. Wait returns the first error.
	var g errgroup.Group
	for _, j := range jobs {
		g.Go(func() error { return j.s.Send(ctx, j.msg) })
	}
	return len(jobs), g.Wait()
}

// messageFor builds the Message a sink receives, or ok=false to skip it.
func messageFor(s sink.Sink, rr rendered, in Input) (sink.Message, bool) {
	switch s.Kind() {
	case "apprise":
		if rr.message == "" { // empty/omitted body → pointless notification, skip
			return sink.Message{}, false
		}
		return sink.Message{Title: rr.title, Body: rr.message, Params: rr.params}, true
	case "http":
		if rr.messageOmitted {
			// Forward the original request body verbatim with the inbound type.
			return sink.Message{Body: string(in.RawBody), Headers: rr.headers, ContentType: in.ContentType}, true
		}
		return sink.Message{Body: rr.message, Headers: rr.headers}, true
	default:
		return sink.Message{}, false
	}
}

// Plan is the dry-run preview: which targets would receive what (the apprise
// target URL and its credentials are never included).
type Plan struct {
	Route   string            `json:"route"`
	Fired   bool              `json:"fired"`
	Targets []PlanTarget      `json:"targets"`
	Dropped map[string]string `json:"dropped,omitempty"`
}

// PlanTarget is one target's would-be delivery.
type PlanTarget struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`
	Title   string            `json:"title,omitempty"`
	Body    string            `json:"body,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Skipped bool              `json:"skipped,omitempty"`
}

func (r *Route) plan(rr rendered, in Input, matched []bool) *Plan {
	p := &Plan{Route: r.name, Fired: true}
	for i, t := range r.targets {
		pt := PlanTarget{Name: t.sink.Name(), Kind: t.sink.Kind()}
		if msg, ok := messageFor(t.sink, rr, in); ok && matched[i] {
			pt.Title, pt.Body, pt.Params, pt.Headers = msg.Title, msg.Body, msg.Params, msg.Headers
		} else {
			pt.Skipped = true
		}
		p.Targets = append(p.Targets, pt)
	}
	for k, err := range rr.dropped {
		if p.Dropped == nil {
			p.Dropped = make(map[string]string)
		}
		p.Dropped[k] = err.Error()
	}
	return p
}
