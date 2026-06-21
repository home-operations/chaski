// Package render compiles and evaluates a route's Go-template fields — title,
// message, and the params/headers values. Templates are parsed once at load
// (fail-fast) and rendered per request against the webhook payload with the
// shared funcmap (sprout helpers plus a strict env; no filesystem or network).
// Missing keys are tolerated (`<no value>`), matching the gate's dyn/missing-key
// handling.
package render

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/home-operations/chaski/internal/tmpl"
)

// Data is the variable environment a field template renders against. The typed
// fields are projected (by vars) onto lower-cased template variables —
// .payload, .headers, .query, .method, .route, .now — so a route's templates
// use the same names as the CEL gate.
type Data struct {
	Payload any
	Headers map[string]string
	Query   map[string]string
	Method  string
	Route   string
	Now     time.Time
}

// vars projects Data onto the lower-cased template variable map. Rendering
// against a map (rather than the struct) is what lets templates use lower-case
// names and keeps a missing top-level lookup tolerant (missingkey=default),
// matching how payload sub-keys behave.
func (d Data) vars() map[string]any {
	return map[string]any{
		"payload": d.Payload,
		"headers": d.Headers,
		"query":   d.Query,
		"method":  d.Method,
		"route":   d.Route,
		"now":     d.Now,
	}
}

// Template is one compiled field template.
type Template struct {
	t   *template.Template
	src string
}

// Set is a compiled collection of named template snippets (the top-level
// `templates:` block). It is shared across a config's routes so any field
// template can reuse a snippet via {{ template "name" . }} or {{ include
// "name" . }}; snippets may reference one another. Build it once at load.
type Set struct {
	base *template.Template // holds the snippets; cloned per compiled field
}

// NewSet parses the named snippets into a shared template tree.
func NewSet(snippets map[string]string) (*Set, error) {
	base := template.New("chaski").
		Option("missingkey=default").
		Funcs(tmpl.FuncMap()).
		// include must already exist when a snippet that uses it is parsed; the
		// real implementation is bound per field in Compile (it needs the field's
		// own tree to resolve names).
		Funcs(template.FuncMap{"include": includePlaceholder})
	// Sorted only for a deterministic error on the first bad snippet; a forward
	// {{ template }} reference resolves at execute time, so order is irrelevant.
	for _, name := range slices.Sorted(maps.Keys(snippets)) {
		if _, err := base.New(name).Parse(snippets[name]); err != nil {
			return nil, fmt.Errorf("render: parse template %q: %w", name, err)
		}
	}
	return &Set{base: base}, nil
}

// Compile parses one field template into a clone of the snippet tree, so the
// field can reference any snippet. A reference to an excluded function (e.g.
// readFile) is a parse error here, so the restriction is enforced at load, not
// silently at render.
func (s *Set) Compile(name, text string) (*Template, error) {
	// Clone keeps each field's tree independent and lets include bind to it. The
	// base is never executed, so it is always safe to clone.
	root, err := s.base.Clone()
	if err != nil {
		return nil, fmt.Errorf("render: clone for %s: %w", name, err)
	}
	root.Funcs(template.FuncMap{"include": includeFunc(root)})
	t, err := root.New(name).Parse(text)
	if err != nil {
		return nil, fmt.Errorf("render: parse %s: %w", name, err)
	}
	return &Template{t: t, src: text}, nil
}

// emptySet is the snippet-less set backing the package-level Compile/CompileMap,
// so a caller with no `templates:` block needs no Set.
var emptySet = func() *Set {
	s, err := NewSet(nil)
	if err != nil {
		panic("render: building empty set: " + err.Error())
	}
	return s
}()

// Compile parses one field template with no shared snippets.
func Compile(name, text string) (*Template, error) { return emptySet.Compile(name, text) }

// includeFunc executes a named template and returns its output, so it can be
// piped (e.g. {{ include "x" . | toUpper }}); the {{ template }} action is a
// statement and cannot be.
func includeFunc(t *template.Template) func(string, any) (string, error) {
	return func(name string, data any) (string, error) {
		var buf strings.Builder
		if err := t.ExecuteTemplate(&buf, name, data); err != nil {
			return "", err
		}
		return buf.String(), nil
	}
}

// includePlaceholder satisfies parse-time resolution of `include`; it is
// replaced by includeFunc (bound to the field's tree) before any execution.
func includePlaceholder(string, any) (string, error) { return "", nil }

// Render executes the template against d.
func (t *Template) Render(d Data) (string, error) {
	return t.render(d.vars())
}

// render executes against an already-projected variable map, so a Map can build
// the map once and share it across its entries.
func (t *Template) render(vars map[string]any) (string, error) {
	var buf strings.Builder
	if err := t.t.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("render: %q: %w", t.src, err)
	}
	return buf.String(), nil
}

// Source returns the original template text.
func (t *Template) Source() string { return t.src }

// Map is a compiled set of named value-templates — a route's params or headers.
// Keys are literal; values are templates.
type Map struct {
	entries map[string]*Template
}

// CompileMap parses every value in m against this set's snippets. A parse error
// in any value fails at load, named by its key.
func (s *Set) CompileMap(field string, m map[string]string) (*Map, error) {
	if len(m) == 0 {
		return &Map{}, nil
	}
	entries := make(map[string]*Template, len(m))
	for k, v := range m {
		t, err := s.Compile(fmt.Sprintf("%s[%s]", field, k), v)
		if err != nil {
			return nil, err
		}
		entries[k] = t
	}
	return &Map{entries: entries}, nil
}

// CompileMap parses every value in m with no shared snippets.
func CompileMap(field string, m map[string]string) (*Map, error) {
	return emptySet.CompileMap(field, m)
}

// Render evaluates every entry. A key whose value errors at render is an
// optional-field fault: it is omitted from the result and reported in dropped,
// and the relay proceeds. A nil dropped map means every key rendered.
func (m *Map) Render(d Data) (out map[string]string, dropped map[string]error) {
	if len(m.entries) == 0 {
		return nil, nil
	}
	vars := d.vars()
	out = make(map[string]string, len(m.entries))
	for k, t := range m.entries {
		v, err := t.render(vars)
		if err != nil {
			if dropped == nil {
				dropped = make(map[string]error)
			}
			dropped[k] = err
			continue
		}
		out[k] = v
	}
	return out, dropped
}
