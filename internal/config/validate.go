package config

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	"github.com/home-operations/chaski/internal/tmpl"
)

// validMethods bounds the HTTP verbs an http target may use.
var validMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodPost: {}, http.MethodPut: {},
	http.MethodPatch: {}, http.MethodDelete: {},
}

// Validate checks the merged config's structure: every target is a well-formed
// sink, every route names a target that exists, and literal blocks are sane. It
// does NOT compile whenExpr or parse the field templates — that is layered on
// by the gate/render packages via the validate command.
func (rc *RouteConfig) Validate() error {
	for name, t := range rc.Targets {
		if err := t.validate(name); err != nil {
			return err
		}
	}
	for name, r := range rc.Routes {
		if err := r.validate(name, rc.Targets); err != nil {
			return err
		}
	}
	return nil
}

func (t *Target) validate(name string) error {
	switch t.Kind() {
	case "apprise":
		if strings.TrimSpace(t.Apprise.URL) == "" {
			return fmt.Errorf("config: target %q (%s): apprise.url is required", name, t.Source)
		}
	case "http":
		if strings.TrimSpace(t.HTTP.URL) == "" {
			return fmt.Errorf("config: target %q (%s): http.url is required", name, t.Source)
		}
		if m := t.HTTP.Method; m != "" {
			if _, ok := validMethods[strings.ToUpper(m)]; !ok {
				return fmt.Errorf("config: target %q (%s): unsupported http.method %q", name, t.Source, m)
			}
		}
		if r := t.HTTP.Retry; r != nil && r.Attempts < 1 {
			return fmt.Errorf("config: target %q (%s): http.retry.attempts must be >= 1, got %d", name, t.Source, r.Attempts)
		}
	default:
		return fmt.Errorf("config: target %q (%s) must set exactly one of `apprise` or `http`", name, t.Source)
	}
	return nil
}

func (r *Route) validate(name string, targets map[string]*Target) error {
	if len(r.Target) == 0 {
		return fmt.Errorf("config: route %q (%s): target is required", name, r.Source)
	}
	seen := make(map[string]bool, len(r.Target))
	for _, tn := range r.Target {
		if _, ok := targets[tn.Name]; !ok {
			return fmt.Errorf("config: route %q (%s) references unknown target %q", name, r.Source, tn.Name)
		}
		if seen[tn.Name] {
			// Two entries for one sink would deliver to it twice; with per-target
			// whenExpr that reads like an OR but double-sends when both match.
			return fmt.Errorf("config: route %q (%s) lists target %q more than once; combine the conditions into one whenExpr (a || b)", name, r.Source, tn.Name)
		}
		seen[tn.Name] = true
	}
	if resp := r.Response; resp != nil {
		if err := validateStatus(resp.Status, name, r.Source, "status"); err != nil {
			return err
		}
		if err := validateStatus(resp.SkipStatus, name, r.Source, "skipStatus"); err != nil {
			return err
		}
	}
	return nil
}

func validateStatus(code int, route, source, field string) error {
	if code != 0 && (code < 100 || code > 599) {
		return fmt.Errorf("config: route %q (%s): response.%s %d is not a valid HTTP status", route, source, field, code)
	}
	return nil
}

// renderEnv interpolates {{ env "NAME" }} into the plain config values that may
// carry secrets — apprise/http target URLs and headers, and verify secrets —
// from the process environment, once, over the merged config. A missing
// variable is a fatal error (the funcmap's strict env). Route field templates
// (title/message/params/headers) are left verbatim here and rendered per
// request; env is available there too, against the live payload.
func renderEnv(rc *RouteConfig) error {
	fm := tmpl.FuncMap()
	render := func(s, where string) (string, error) {
		out, err := renderEnvString(s, fm)
		if err != nil {
			return "", fmt.Errorf("config: %s: %w", where, err)
		}
		return out, nil
	}

	for name, t := range rc.Targets {
		switch {
		case t.Apprise != nil:
			v, err := render(t.Apprise.URL, fmt.Sprintf("target %q url", name))
			if err != nil {
				return err
			}
			t.Apprise.URL = v
		case t.HTTP != nil:
			v, err := render(t.HTTP.URL, fmt.Sprintf("target %q url", name))
			if err != nil {
				return err
			}
			t.HTTP.URL = v
			for k, hv := range t.HTTP.Headers {
				rv, err := render(hv, fmt.Sprintf("target %q header %q", name, k))
				if err != nil {
					return err
				}
				t.HTTP.Headers[k] = rv
			}
		}
	}

	for name, r := range rc.Routes {
		if r.Verify == nil {
			continue
		}
		for i, secret := range r.Verify.Secret {
			v, err := render(secret, fmt.Sprintf("route %q verify secret", name))
			if err != nil {
				return err
			}
			r.Verify.Secret[i] = v
		}
	}
	return nil
}

// renderEnvString renders one config-load template with the config funcmap
// (sprout's helpers plus a strict env that errors on an unset variable). Values
// with no template action take a fast path.
func renderEnvString(s string, fm template.FuncMap) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	t, err := template.New("env").Funcs(fm).Parse(s)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, nil); err != nil {
		return "", err
	}
	return buf.String(), nil
}
