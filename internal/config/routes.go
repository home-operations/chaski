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

// TemplateSource returns the fragment file a named snippet was loaded from, or
// "" if there is no such snippet. It exposes the loader's provenance for the
// validate summary, mirroring Route.Source / Target.Source.
func (rc *RouteConfig) TemplateSource(name string) string {
	return rc.templateSources[name]
}

// Route gates an inbound webhook with a CEL expression and renders the fields
// that are relayed to its target(s). whenExpr is the only CEL field; title,
// message, and the params/headers values are Go templates evaluated per request.
type Route struct {
	// Target is one target name, a list of names, or a list mixing names and
	// {name, whenExpr} objects to fan out to. Required. A target's whenExpr gates
	// that single sink, so the route fans out only to the targets whose
	// expression matches the request. (`jsonschema` tags are inert at runtime —
	// read only by cmd/schema.)
	Target TargetRefs `yaml:"target" jsonschema:"required"`
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

// TargetRef is one fan-out target: a configured target name, optionally gated by
// its own whenExpr — a CEL boolean over the same variables as the route gate. An
// empty whenExpr always fires (whenever the route does), so a bare name behaves
// exactly as before.
type TargetRef struct {
	Name     string
	WhenExpr string
}

// TargetRefs is a route's fan-out list. It decodes a single name, a list of
// names, or a list mixing names and {name, whenExpr} objects, so the common case
// (`target: po` or `target: [a, b]`) stays terse.
type TargetRefs []TargetRef

// UnmarshalYAML implements the go.yaml.in/yaml/v4 node-based unmarshaler.
func (t *TargetRefs) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode, yaml.MappingNode:
		// A single target: a bare name or one {name, whenExpr} object.
		ref, err := decodeTargetRef(node)
		if err != nil {
			return err
		}
		*t = TargetRefs{ref}
		return nil
	case yaml.SequenceNode:
		refs := make(TargetRefs, 0, len(node.Content))
		for _, el := range node.Content {
			ref, err := decodeTargetRef(el)
			if err != nil {
				return err
			}
			refs = append(refs, ref)
		}
		*t = refs
		return nil
	default:
		return fmt.Errorf("expected a target name, a list of names, or a list of {name, whenExpr} objects")
	}
}

// decodeTargetRef reads one fan-out entry: a bare name (scalar) or a
// {name, whenExpr} object. Unknown keys and non-scalar values are rejected, so a
// typo'd whenExpr can't silently leave a target ungated (firing on every request).
func decodeTargetRef(node *yaml.Node) (TargetRef, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value == "" {
			return TargetRef{}, fmt.Errorf("a target name must not be empty")
		}
		return TargetRef{Name: node.Value}, nil
	case yaml.MappingNode:
		var ref TargetRef
		seen := make(map[string]bool, 2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, val := node.Content[i], node.Content[i+1]
			if seen[key.Value] {
				return TargetRef{}, fmt.Errorf("duplicate target key %q", key.Value)
			}
			seen[key.Value] = true
			if val.Kind != yaml.ScalarNode {
				return TargetRef{}, fmt.Errorf("target %q must be a string", key.Value)
			}
			switch key.Value {
			case "name":
				ref.Name = val.Value
			case "whenExpr":
				ref.WhenExpr = val.Value
			default:
				return TargetRef{}, fmt.Errorf("unknown target key %q (allowed: name, whenExpr)", key.Value)
			}
		}
		if ref.Name == "" {
			return TargetRef{}, fmt.Errorf("a target object requires a name")
		}
		return ref, nil
	default:
		return TargetRef{}, fmt.Errorf("a target must be a name or a {name, whenExpr} object")
	}
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
