// Package tmpl builds the go-sprout/sprout function map that chaski renders
// every template with — config-load fields (target URLs/headers, verify
// secrets) and per-request route fields (title, message, params, headers)
// alike. It exposes sprout's safe registries plus a strict `env` (errors on an
// unset variable, so a missing secret fails loudly rather than rendering
// empty), but never filesystem, network, or the sprig-backward aliases, so a
// template can't read a file or make a network call.
package tmpl

import (
	"fmt"
	"os"
	"text/template"

	"github.com/go-sprout/sprout"
	"github.com/go-sprout/sprout/registry/checksum"
	"github.com/go-sprout/sprout/registry/conversion"
	"github.com/go-sprout/sprout/registry/encoding"
	"github.com/go-sprout/sprout/registry/env"
	"github.com/go-sprout/sprout/registry/maps"
	"github.com/go-sprout/sprout/registry/numeric"
	"github.com/go-sprout/sprout/registry/random"
	"github.com/go-sprout/sprout/registry/reflect"
	"github.com/go-sprout/sprout/registry/regexp"
	"github.com/go-sprout/sprout/registry/semver"
	"github.com/go-sprout/sprout/registry/slices"
	"github.com/go-sprout/sprout/registry/std"
	"github.com/go-sprout/sprout/registry/strings"
	"github.com/go-sprout/sprout/registry/time"
	"github.com/go-sprout/sprout/registry/uniqueid"
)

// safeHandler is a sprout handler with the safe registries only — every group
// except env, filesystem, network, and the sprig-backward aliases. sprout.New()
// starts empty, so nothing is present unless added here.
func safeHandler() *sprout.DefaultHandler {
	h := sprout.New()
	// AddRegistries only errors on a duplicate UID; the set below is static.
	if err := h.AddRegistries(
		std.NewRegistry(),
		strings.NewRegistry(),
		conversion.NewRegistry(),
		encoding.NewRegistry(),
		numeric.NewRegistry(),
		slices.NewRegistry(),
		maps.NewRegistry(),
		regexp.NewRegistry(),
		time.NewRegistry(),
		semver.NewRegistry(),
		random.NewRegistry(),
		checksum.NewRegistry(),
		uniqueid.NewRegistry(),
		reflect.NewRegistry(),
	); err != nil {
		panic("tmpl: building safe sprout handler: " + err.Error())
	}
	return h
}

// FuncMap is the function map for every chaski template: sprout's safe helpers
// plus a strict `env`. Filesystem, network, and the sprig-backward aliases stay
// excluded, so a template still can't read a file or make a network call.
//
// SECURITY: `env` reads a process variable, so `{{ env "NAME" }}` can place a
// secret into rendered output. Per-request route fields render against the
// (attacker-influenced) webhook payload, so use only LITERAL keys — a
// payload-derived key like `{{ env .payload.x }}` would let a sender read an
// arbitrary process variable into the relayed notification.
func FuncMap() template.FuncMap {
	h := safeHandler()
	if err := h.AddRegistry(env.NewRegistry()); err != nil {
		panic("tmpl: adding env registry: " + err.Error())
	}
	fm := h.Build()
	// Override sprout's permissive env (returns "" for an unset var) so a
	// missing secret is a hard error, not a silently empty credential.
	fm["env"] = strictEnv
	return fm
}

// strictEnv returns the value of an environment variable, erroring if it is
// unset — surfaced as a template error (a fatal config-load error, or a
// per-request render fault).
func strictEnv(key string) (string, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return "", fmt.Errorf("environment variable %q is not set", key)
	}
	return v, nil
}
