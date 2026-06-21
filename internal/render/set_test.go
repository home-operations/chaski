package render_test

import (
	"strings"
	"testing"

	"github.com/home-operations/chaski/internal/render"
)

func mustSet(t *testing.T, snippets map[string]string) *render.Set {
	t.Helper()
	s, err := render.NewSet(snippets)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	return s
}

func TestSetTemplateAction(t *testing.T) {
	s := mustSet(t, map[string]string{"greet": "Hi {{ .payload.name }}"})
	tpl, err := s.Compile("title", `{{ template "greet" . }}!`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got, err := tpl.Render(render.Data{Payload: map[string]any{"name": "Sam"}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "Hi Sam!" {
		t.Errorf("got %q, want %q", got, "Hi Sam!")
	}
}

func TestSetIncludePipes(t *testing.T) {
	// include returns a string, so its output can be piped (template can't).
	s := mustSet(t, map[string]string{"greet": "hi {{ .payload.name }}"})
	tpl, err := s.Compile("title", `{{ include "greet" . | toUpper }}`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got, err := tpl.Render(render.Data{Payload: map[string]any{"name": "sam"}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "HI SAM" {
		t.Errorf("got %q, want %q", got, "HI SAM")
	}
}

func TestSetSnippetsCompose(t *testing.T) {
	s := mustSet(t, map[string]string{
		"outer": `[{{ template "inner" . }}]`,
		"inner": "X",
	})
	tpl, err := s.Compile("message", `{{ template "outer" . }}`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got, err := tpl.Render(render.Data{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "[X]" {
		t.Errorf("got %q, want [X]", got)
	}
}

func TestSetBadSnippetFailsAtLoad(t *testing.T) {
	if _, err := render.NewSet(map[string]string{"bad": "{{ .x. }}"}); err == nil {
		t.Fatal("NewSet with a malformed snippet = nil error, want a parse error")
	}
}

func TestSetUnknownTemplateErrorsAtRender(t *testing.T) {
	// A reference to an undefined template parses (resolved at execute) but is a
	// render error, surfaced as a route fault rather than silently empty.
	s := mustSet(t, nil)
	tpl, err := s.Compile("title", `{{ template "missing" . }}`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := tpl.Render(render.Data{}); err == nil {
		t.Error("Render with an undefined template = nil error, want a render error")
	}
}

func TestSetCompileMapUsesSnippets(t *testing.T) {
	s := mustSet(t, map[string]string{"pri": "high"})
	m, err := s.CompileMap("params", map[string]string{"priority": `{{ template "pri" . }}`})
	if err != nil {
		t.Fatalf("CompileMap: %v", err)
	}
	out, dropped := m.Render(render.Data{})
	if dropped != nil {
		t.Fatalf("dropped: %v", dropped)
	}
	if out["priority"] != "high" {
		t.Errorf("priority = %q, want high", out["priority"])
	}
}

// The package-level Compile keeps working without a Set (snippet-less).
func TestPackageCompileStillWorks(t *testing.T) {
	tpl, err := render.Compile("title", "{{ .payload.x }}")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got, _ := tpl.Render(render.Data{Payload: map[string]any{"x": "y"}}); got != "y" {
		t.Errorf("got %q, want y", got)
	}
	if !strings.Contains(tpl.Source(), ".payload.x") {
		t.Errorf("source = %q", tpl.Source())
	}
}
