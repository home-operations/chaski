package render_test

import (
	"strings"
	"sync"
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

// TestSetFieldNameDoesNotCollideWithSnippet pins the fix for a snippet named
// like a field role: it must not be clobbered by the field of the same name,
// and {{ template "title" . }} must resolve to the snippet (not self-recurse).
func TestSetFieldNameDoesNotCollideWithSnippet(t *testing.T) {
	s := mustSet(t, map[string]string{"title": "[{{ .payload.sev }}]"})
	tpl, err := s.Compile("title", `{{ template "title" . }} alert`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got, err := tpl.Render(render.Data{Payload: map[string]any{"sev": "CRIT"}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "[CRIT] alert" {
		t.Errorf("got %q, want %q", got, "[CRIT] alert")
	}

	// The required "message" field is the case that 500s on a collision.
	sm := mustSet(t, map[string]string{"message": "body:{{ .payload.x }}"})
	mt, err := sm.Compile("message", `{{ template "message" . }}`)
	if err != nil {
		t.Fatalf("Compile message: %v", err)
	}
	if got, err := mt.Render(render.Data{Payload: map[string]any{"x": "1"}}); err != nil || got != "body:1" {
		t.Errorf("message render = %q, %v; want body:1", got, err)
	}
}

func TestSetReservedNamesRejected(t *testing.T) {
	for _, name := range []string{"_chaski_root", "_chaski_field"} {
		if _, err := render.NewSet(map[string]string{name: "x"}); err == nil {
			t.Errorf("NewSet with reserved name %q = nil error, want rejection", name)
		}
	}
	// The app name itself is not reserved — operators may use it as a snippet.
	if _, err := render.NewSet(map[string]string{"chaski": "x"}); err != nil {
		t.Errorf(`NewSet with snippet "chaski" = %v, want ok`, err)
	}
}

func TestSetIncludeUnknownErrorsAtLoad(t *testing.T) {
	s := mustSet(t, nil)
	if _, err := s.Compile("title", `{{ include "nope" . }}`); err == nil {
		t.Error("include of an undefined snippet = nil error, want a load error")
	}
}

// TestSetConcurrentRender locks in the parse-once / execute-concurrently
// contract: one compiled template (with a snippet + include path) rendered from
// many goroutines must be race-free under `go test -race`.
func TestSetConcurrentRender(t *testing.T) {
	s := mustSet(t, map[string]string{"label": "[{{ .payload.lvl }}]"})
	tpl, err := s.Compile("title", `{{ template "label" . }} {{ include "label" . }}`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if got, err := tpl.Render(render.Data{Payload: map[string]any{"lvl": "I"}}); err != nil || got != "[I] [I]" {
				t.Errorf("concurrent render = %q, %v", got, err)
			}
		})
	}
	wg.Wait()
}

func TestSetUnknownTemplateErrorsAtLoad(t *testing.T) {
	// A reference to an undefined template parses (resolved at execute), but the
	// reference graph is validated at compile, so a dangling name fails fast at
	// load rather than as a render-time HTTP 500.
	s := mustSet(t, nil)
	if _, err := s.Compile("title", `{{ template "missing" . }}`); err == nil {
		t.Error("Compile with an undefined template = nil error, want a load error")
	}
}

// TestSetIncludeFromInsideSnippet exercises the path the clone+rebind fix relies
// on: include called from WITHIN a snippet (not just from the field) must resolve
// to the real include func, not the parse-time placeholder.
func TestSetIncludeFromInsideSnippet(t *testing.T) {
	s := mustSet(t, map[string]string{
		"outer": `<{{ include "inner" . }}>`,
		"inner": `{{ .payload.x }}`,
	})
	tpl, err := s.Compile("title", `{{ template "outer" . }}`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got, err := tpl.Render(render.Data{Payload: map[string]any{"x": "VAL"}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "<VAL>" {
		t.Errorf("got %q, want %q", got, "<VAL>")
	}
}

func TestSetIncludeCycleRejected(t *testing.T) {
	// A self-referential include cycle is the dangerous case: at render it would
	// recurse until the goroutine stack overflows (an unrecoverable crash). It
	// must be rejected at load instead.
	if _, err := render.NewSet(map[string]string{"loop": `{{ include "loop" . }}`}); err == nil {
		t.Error("NewSet with a self-referential include = nil error, want a cycle error")
	}
	// Mutual cycle across two snippets.
	if _, err := render.NewSet(map[string]string{
		"a": `x {{ include "b" . }}`,
		"b": `y {{ include "a" . }}`,
	}); err == nil {
		t.Error("NewSet with a mutual include cycle = nil error, want a cycle error")
	}
}

func TestSetTemplateActionCycleRejected(t *testing.T) {
	if _, err := render.NewSet(map[string]string{"loop": `{{ template "loop" . }}`}); err == nil {
		t.Error("NewSet with a self-referential template action = nil error, want a cycle error")
	}
}

func TestSetReservedReferenceRejected(t *testing.T) {
	// The reserved field name is rejected as a snippet key (TestSetReservedNames-
	// Rejected); it must also be rejected as a reference target, so it can't be
	// reached as a call target to set up a field<->snippet loop.
	if _, err := render.NewSet(map[string]string{"s": `{{ template "_chaski_field" . }}`}); err == nil {
		t.Error("snippet referencing the reserved field name = nil error, want rejection")
	}
	s := mustSet(t, nil)
	if _, err := s.Compile("message", `{{ template "_chaski_field" . }}`); err == nil {
		t.Error("field referencing the reserved field name = nil error, want rejection")
	}
}

func TestSetNonLiteralIncludeRejected(t *testing.T) {
	// A non-literal include name can't be checked for existence or cycles at load
	// (and could be payload-derived), so it is rejected.
	s := mustSet(t, map[string]string{"greet": "hi"})
	if _, err := s.Compile("title", `{{ include .payload.which . }}`); err == nil {
		t.Error("include with a non-literal name = nil error, want rejection")
	}
}

func TestSetEmptyNameRejected(t *testing.T) {
	if _, err := render.NewSet(map[string]string{"": "x"}); err == nil {
		t.Error(`NewSet with an empty template name = nil error, want rejection`)
	}
}

func TestSetNestedDefineRejected(t *testing.T) {
	if _, err := render.NewSet(map[string]string{"s": `{{ define "extra" }}x{{ end }}`}); err == nil {
		t.Error("snippet using {{ define }} = nil error, want rejection")
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
