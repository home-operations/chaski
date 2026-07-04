package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/relay"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newSendCmd() *cobra.Command {
	var path, payload, route string
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Fire one route for real — validate's render pipeline with the delivery performed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSend(cmd.OutOrStdout(), path, payload, route)
		},
	}
	addCommonFlags(cmd.Flags(), &path, &payload, &route)
	return cmd
}

// addCommonFlags declares the flags validate and send share.
func addCommonFlags(fs *pflag.FlagSet, path, payload, route *string) {
	fs.StringVarP(path, "config", "c", "",
		"config file or directory (default $CHASKI_CONFIG or "+config.DefaultConfigPath+")")
	fs.StringVar(payload, "payload", "", "sample JSON body (file, or - for stdin)")
	fs.StringVar(route, "route", "", "route to use (default: the only one)")
}

// runSend is the same load → gate → render pipeline as `validate --payload`,
// but the result is delivered for real — a terminal-speed way to see exactly
// what a route produces in the notification client.
func runSend(stdout io.Writer, path, payload, routeName string) error {
	if payload == "" {
		return fmt.Errorf("--payload is required (a file, or - for stdin)")
	}

	rc, err := config.LoadRouteConfig(resolveConfigPath(path))
	if err != nil {
		return err
	}
	// Real env-derived knobs (timeouts, retry) — unlike validate's inert stub,
	// this path sends. Cap apprise's shared client like the server does.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	http.DefaultClient.Timeout = cfg.RequestTimeout
	engine, err := relay.Build(rc, cfg, relay.Options{})
	if err != nil {
		return err
	}

	raw, err := readPayload(payload)
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

	ctx, cancel := context.WithTimeout(context.Background(), cfg.RequestTimeout)
	defer cancel()
	res := rt.Handle(ctx, relay.Input{
		Payload:     pl,
		RawBody:     raw,
		ContentType: ct,
		Method:      "POST",
		Now:         time.Now(),
	})
	return printSendResult(stdout, name, res)
}

// printSendResult reports the delivery outcome: relayed and skipped are
// answers (exit 0), a gate/render/relay fault is an error.
func printSendResult(w io.Writer, name string, res relay.Result) error {
	for _, field := range slices.Sorted(maps.Keys(res.Dropped)) {
		_, _ = fmt.Fprintf(w, "warning: field %q dropped: %v\n", field, res.Dropped[field])
	}
	switch res.Kind {
	case relay.Relayed:
		_, _ = fmt.Fprintf(w, "route %q: relayed\n", name)
		return nil
	case relay.Skipped:
		_, _ = fmt.Fprintf(w, "route %q: skipped (%s) — nothing sent\n", name, res.Reason)
		return nil
	default:
		return fmt.Errorf("route %q: %s: %w", name, res.Kind, res.Err)
	}
}
