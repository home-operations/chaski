package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/relay"
)

// runValidate implements `chaski validate [-c path] [--payload file] [--route
// name]`: it loads the config (file or config.d directory), env-renders it, and
// compiles every route exactly as the server does at boot — so a CI run fails
// before deploy on any bad gate, template, verify block, target reference, or
// missing env var. With --payload it goes further, rendering a route against a
// sample body like ?dryRun=1 (catching semantic bugs — a wrong field path, a
// typo'd key — that compilation can't). Otherwise it prints a source-attributed
// summary. It is the only inspection path on the no-shell image.
func runValidate(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stdout)
	path := fs.String("c", "", "config file or directory (default $CHASKI_CONFIG or "+config.DefaultConfigPath+")")
	payload := fs.String("payload", "", "render a route against a sample JSON body (file, or - for stdin)")
	routeName := fs.String("route", "", "route to render with --payload (default: the only one)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	p := *path
	if p == "" {
		if p = os.Getenv("CHASKI_CONFIG"); p == "" {
			p = config.DefaultConfigPath
		}
	}

	rc, err := config.LoadRouteConfig(p)
	if err != nil {
		return err
	}
	// Retry defaults don't affect validation (nothing is sent); use sane values
	// so relay.Build — which compiles whenExpr, the field templates, and verify —
	// runs its full fail-fast checks. No Notifier is needed: a dry run never sends.
	cfg := &config.Config{RetryAttempts: 3, RetryBackoff: 200 * time.Millisecond}
	engine, err := relay.Build(rc, cfg, relay.Options{})
	if err != nil {
		return err
	}

	if *payload != "" {
		return renderSample(stdout, engine, rc, *routeName, *payload)
	}

	printSummary(stdout, p, rc)
	return nil
}

// renderSample decodes a sample JSON body and renders the matched route exactly
// as a ?dryRun=1 request would, printing the plan. It never sends; a gate or
// render fault returns a non-nil error so CI fails on a semantic bug, not just a
// syntax one.
func renderSample(stdout io.Writer, engine *relay.Engine, rc *config.RouteConfig, routeName, payloadPath string) error {
	raw, err := readPayload(payloadPath)
	if err != nil {
		return err
	}
	pl, ct, err := relay.DecodeBody("application/json", raw)
	if err != nil {
		return err
	}

	name, rt, err := pickRoute(engine, rc, routeName)
	if err != nil {
		return err
	}

	res := rt.Handle(context.Background(), relay.Input{
		Payload:     pl,
		RawBody:     raw,
		ContentType: ct,
		Method:      "POST",
		Now:         time.Now(),
		DryRun:      true,
	})
	return printResult(stdout, name, res)
}

func readPayload(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// pickRoute resolves the route to render: the named one, or the sole route when
// only one is configured. Otherwise it errors with the available names.
func pickRoute(engine *relay.Engine, rc *config.RouteConfig, name string) (string, *relay.Route, error) {
	names := slices.Sorted(maps.Keys(rc.Routes))
	if name != "" {
		rt, ok := engine.Lookup(name)
		if !ok {
			return "", nil, fmt.Errorf("no route %q; configured routes: %v", name, names)
		}
		return name, rt, nil
	}
	switch len(names) {
	case 0:
		return "", nil, fmt.Errorf("no routes configured")
	case 1:
		rt, _ := engine.Lookup(names[0])
		return names[0], rt, nil
	default:
		return "", nil, fmt.Errorf("multiple routes configured; pass --route (one of %v)", names)
	}
}

// printResult renders a dry-run outcome: the plan JSON when the route fired, a
// note when the gate was false, or a (non-nil) error for a gate/render fault.
func printResult(w io.Writer, name string, res relay.Result) error {
	switch res.Kind {
	case relay.DryRunned:
		// The plan answers fired-or-not (Fired:false for a gate miss) and, when
		// fired, the rendered per-target fields.
		out, err := json.MarshalIndent(res.Plan, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "%s\n", out)
		return nil
	case relay.GateError, relay.RenderError:
		return fmt.Errorf("route %q: %w", name, res.Err)
	default:
		_, _ = fmt.Fprintf(w, "route %q: %s\n", name, res.Kind)
		return nil
	}
}

func printSummary(w io.Writer, path string, rc *config.RouteConfig) {
	// Writing the summary to stdout can't meaningfully fail; discard the result.
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

	p("ok: %s — %d route(s), %d target(s), %d template(s)\n", path, len(rc.Routes), len(rc.Targets), len(rc.Templates))
	for _, name := range slices.Sorted(maps.Keys(rc.Routes)) {
		r := rc.Routes[name]
		p("  route %-24s (%s) → %v\n", name, r.Source, []string(r.Target))
	}
	for _, name := range slices.Sorted(maps.Keys(rc.Targets)) {
		t := rc.Targets[name]
		p("  target %-23s (%s) [%s]\n", name, t.Source, t.Kind())
	}
	for _, name := range slices.Sorted(maps.Keys(rc.Templates)) {
		p("  template %-21s (%s)\n", name, rc.TemplateSource(name))
	}
}
