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

// TestSetChainNodeCycleRejected pins the walker-soundness fix: a cycle whose
// include is hidden behind a parenthesized pipeline + field access (a
// *parse.ChainNode, e.g. {{ (include "x" .).F }}) must still be rejected at load.
// Before the fix the walker skipped ChainNode args, so the include edge was lost
// and the cycle reached a render-time stack-overflow crash.
func TestSetChainNodeCycleRejected(t *testing.T) {
	cases := map[string]map[string]string{
		"self":     {"loop": `{{ (include "loop" .).X }}`},
		"mutual":   {"a": `{{ (include "b" .).X }}`, "b": `{{ (include "a" .).Y }}`},
		"in-if":    {"loop": `{{ if (include "loop" .).Ok }}x{{ end }}`},
		"in-range": {"loop": `{{ range (include "loop" .).Items }}x{{ end }}`},
	}
	for name, snippets := range cases {
		if _, err := render.NewSet(snippets); err == nil {
			t.Errorf("%s: NewSet accepted a cycle hidden in a ChainNode, want rejection", name)
		}
	}
}

// TestSetChainNodeReferenceSeen proves the ChainNode walk doesn't over-reject: a
// non-cyclic chained include is accepted, while a chained include of an undefined
// snippet is still caught — i.e. the reference is genuinely seen, not ignored.
func TestSetChainNodeReferenceSeen(t *testing.T) {
	if _, err := render.NewSet(map[string]string{"greet": "hi", "s": `{{ (include "greet" .).X }}`}); err != nil {
		t.Errorf("NewSet rejected a valid chained include: %v", err)
	}
	if _, err := render.NewSet(map[string]string{"s": `{{ (include "nope" .).X }}`}); err == nil {
		t.Error("chained include of an undefined snippet = nil error, want dangling rejection")
	}
}

// TestSetDefineShadowRejected pins both define-shadow holes: a snippet (or a
// field) that {{ define }}s an existing snippet's name would overwrite the
// already-validated tree and reintroduce a cycle. The blanket nested-template
// rejection closes both paths.
func TestSetDefineShadowRejected(t *testing.T) {
	if _, err := render.NewSet(map[string]string{
		"m": "X",
		"z": `{{ define "m" }}{{ include "z" . }}{{ end }}Z{{ include "m" . }}`,
	}); err == nil {
		t.Error("snippet redefining another snippet via {{ define }} = nil error, want rejection")
	}
	s := mustSet(t, map[string]string{"m": "X", "z": `{{ include "m" . }}`})
	if _, err := s.Compile("message", `{{ define "m" }}{{ include "z" . }}{{ end }}{{ include "z" . }}`); err == nil {
		t.Error("field redefining a snippet via {{ define }} = nil error, want rejection")
	}
}

// TestSetIndirectIncludeRejected pins that include can't be invoked indirectly
// (via the call builtin, a variable, or a pipe-supplied name), which would let a
// cycle through include escape the static graph check and crash the process at
// render. include is only valid as a command head with a string-literal name.
func TestSetIndirectIncludeRejected(t *testing.T) {
	for _, body := range []string{
		`{{ call include "loop" . }}`,               // via the call builtin
		`{{ printf "%v" (call include "loop" .) }}`, // call, nested in a pipe arg
		`{{ $f := include }}{{ $f "loop" . }}`,      // bound to a variable
		`{{ "loop" | include }}`,                    // name supplied by a pipe
		`{{ include ("loop") . }}`,                  // name not a bare literal
	} {
		if _, err := render.NewSet(map[string]string{"loop": body}); err == nil {
			t.Errorf("NewSet accepted an indirect include %q, want rejection", body)
		}
	}
	// The direct, literal forms (including a trailing pipe and piped data) stay
	// valid.
	for _, body := range []string{
		`{{ include "greet" . }}`,
		`{{ include "greet" . | toUpper }}`,
		`{{ . | include "greet" }}`,
	} {
		if _, err := render.NewSet(map[string]string{"greet": "hi", "s": body}); err != nil {
			t.Errorf("NewSet rejected a valid include %q: %v", body, err)
		}
	}
}

func TestSetDefineReservedNameRejected(t *testing.T) {
	// Defining the reserved holder name would slip past a name-based allow-list;
	// the blanket define rejection covers it.
	if _, err := render.NewSet(map[string]string{"a": `{{ define "_chaski_root" }}x{{ end }}body`}); err == nil {
		t.Error(`snippet defining "_chaski_root" = nil error, want rejection`)
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
