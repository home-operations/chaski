package config

import (
	"fmt"
	"time"

	"go.yaml.in/yaml/v4"
)

// RouteConfig is the routes + targets loaded from CHASKI_CONFIG (a single YAML
// file or a config.d directory of fragments merged additively). It is distinct
// from Config, which carries the ops/secret settings from the environment.
type RouteConfig struct {
	Routes  map[string]*Route  `yaml:"routes"`
	Targets map[string]*Target `yaml:"targets"`
	// Templates are shared Go-template snippets, callable from any route field
	// with {{ template "name" . }} or {{ include "name" . }} (the latter pipes).
	// Each value is a Go template, rendered per request like the route fields.
	Templates map[string]string `yaml:"templates"`

	// templateSources maps a template name to the fragment it came from, for
	// config.d duplicate-name errors; set by the loader, never decoded.
	templateSources map[string]string `yaml:"-"`
}

// Route gates an inbound webhook with a CEL expression and renders the fields
// that are relayed to its target(s). whenExpr is the only CEL field; title,
// message, and the params/headers values are Go templates evaluated per request.
type Route struct {
	// Target is one target name or a list to fan out to. Required.
	// (`jsonschema` tags are inert at runtime — read only by cmd/schema.)
	Target StringList `yaml:"target" jsonschema:"required"`
	// WhenExpr is the CEL boolean gate (default true when empty).
	WhenExpr string `yaml:"whenExpr"`
	// Title and Message are Go templates. Message is a pointer so an omitted
	// body (verbatim forward for http) is distinguishable from an empty one.
	Title   string  `yaml:"title"`
	Message *string `yaml:"message"`
	// Params are provider field templates merged into an apprise target's URL
	// query; Headers are templates merged onto an http target's request.
	Params  map[string]string `yaml:"params"`
	Headers map[string]string `yaml:"headers"`
	// Verify optionally requires an inbound signature (HMAC or shared token).
	Verify *Verify `yaml:"verify"`
	// Response optionally overrides the status a sender sees (relay vs skip).
	Response *Response `yaml:"response"`

	// Source is the fragment file a route was loaded from (provenance for
	// errors); set by the loader, never decoded.
	Source string `yaml:"-"`
}

// Target is a config-defined sink. The body is an externally-tagged union:
// exactly one of Apprise or HTTP — the variant key is the discriminator, so an
// invalid sink is unrepresentable.
type Target struct {
	Apprise *AppriseSink `yaml:"apprise"`
	HTTP    *HTTPSink    `yaml:"http"`

	// Source is the fragment file a target was loaded from; set by the loader.
	Source string `yaml:"-"`
}

// Kind returns the sink discriminator ("apprise" or "http"), or "" when the
// target is malformed (zero or both set — caught by Validate).
func (t *Target) Kind() string {
	switch {
	case t.Apprise != nil && t.HTTP == nil:
		return "apprise"
	case t.HTTP != nil && t.Apprise == nil:
		return "http"
	default:
		return ""
	}
}

// AppriseSink is a notification target: an Apprise URL whose scheme selects the
// provider (pover://, ntfy://, …). Credentials live here, never in payload.
type AppriseSink struct {
	URL string `yaml:"url" jsonschema:"required"`
}

// HTTPSink is a generic HTTP forward target.
type HTTPSink struct {
	Method  string            `yaml:"method"`
	URL     string            `yaml:"url" jsonschema:"required"`
	Headers map[string]string `yaml:"headers"`
	Timeout Duration          `yaml:"timeout"`
	Retry   *Retry            `yaml:"retry"`
}

// Retry overrides the global retry defaults for a single target.
type Retry struct {
	Attempts int      `yaml:"attempts"`
	Backoff  Duration `yaml:"backoff"`
}

// Verify is an optional per-route inbound signature check over the raw body.
type Verify struct {
	// Provider is a preset ("github"); Type selects a generic mode
	// ("hmac" | "token") when no preset is used.
	Provider string `yaml:"provider"`
	Type     string `yaml:"type"`
	Header   string `yaml:"header"`
	Algo     string `yaml:"algo"`
	Encoding string `yaml:"encoding"`
	Prefix   string `yaml:"prefix"`
	// Secret is one secret or a list (each tried, so secrets rotate cleanly).
	Secret StringList `yaml:"secret"`
}

// Response overrides the literal status codes a sender observes: Status on a
// successful relay (default 200), SkipStatus on a whenExpr-false skip (default
// 204). The 4xx/5xx error codes are not overridable.
type Response struct {
	Status     int `yaml:"status"`
	SkipStatus int `yaml:"skipStatus"`
}

// StringList decodes either a single scalar or a sequence of scalars into a
// []string, so fields like `target` and `verify.secret` accept both forms.
type StringList []string

// UnmarshalYAML implements the go.yaml.in/yaml/v4 node-based unmarshaler.
func (s *StringList) UnmarshalYAML(node *yaml.Node) error {
	var single string
	if err := node.Decode(&single); err == nil {
		*s = StringList{single}
		return nil
	}
	var list []string
	if err := node.Decode(&list); err != nil {
		return fmt.Errorf("expected a string or list of strings: %w", err)
	}
	*s = list
	return nil
}

// Duration is a time.Duration that decodes from a YAML string like "10s".
type Duration time.Duration

// UnmarshalYAML parses the scalar with time.ParseDuration.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("expected a duration string (e.g. 10s): %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// AsDuration returns the underlying time.Duration.
func (d Duration) AsDuration() time.Duration { return time.Duration(d) }
