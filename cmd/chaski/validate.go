package main

import (
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

// runValidate implements `chaski validate [-c path]`: it loads the config (file
// or config.d directory), env-renders it, and compiles every route exactly as
// the server does at boot — so a CI run fails before deploy on any bad gate,
// template, verify block, target reference, or missing env var. On success it
// prints a source-attributed summary (the only inspection path on a no-shell
// image, alongside ?dryRun=1).
func runValidate(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stdout)
	path := fs.String("c", "", "config file or directory (default $CHASKI_CONFIG or "+config.DefaultConfigPath+")")
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
	// runs its full fail-fast checks.
	cfg := &config.Config{RetryAttempts: 3, RetryBackoff: 200 * time.Millisecond}
	if _, err := relay.Build(rc, cfg, relay.Options{}); err != nil {
		return err
	}

	printSummary(stdout, p, rc)
	return nil
}

func printSummary(w io.Writer, path string, rc *config.RouteConfig) {
	// Writing the summary to stdout can't meaningfully fail; discard the result.
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

	p("ok: %s — %d route(s), %d target(s)\n", path, len(rc.Routes), len(rc.Targets))
	for _, name := range slices.Sorted(maps.Keys(rc.Routes)) {
		r := rc.Routes[name]
		p("  route %-24s (%s) → %v\n", name, r.Source, []string(r.Target))
	}
	for _, name := range slices.Sorted(maps.Keys(rc.Targets)) {
		t := rc.Targets[name]
		p("  target %-23s (%s) [%s]\n", name, t.Source, t.Kind())
	}
}
