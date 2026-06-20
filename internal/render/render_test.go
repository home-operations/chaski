package render

import (
	"testing"
	"time"
)

func sampleData() Data {
	return Data{
		Payload: map[string]any{
			"name": "world",
			"commonLabels": map[string]any{
				"alertname": "HighCPU",
			},
		},
		Headers: map[string]string{"x-source": "ci"},
		Method:  "POST",
		Route:   "alertmanager",
		Now:     time.Unix(1_700_000_000, 0),
	}
}

func render(t *testing.T, text string) string {
	t.Helper()
	tpl, err := Compile("test", text)
	if err != nil {
		t.Fatalf("Compile(%q): %v", text, err)
	}
	out, err := tpl.Render(sampleData())
	if err != nil {
		t.Fatalf("Render(%q): %v", text, err)
	}
	return out
}

func TestRenderFields(t *testing.T) {
	if got := render(t, `🔥 {{ .payload.commonLabels.alertname }}`); got != "🔥 HighCPU" {
		t.Errorf("got %q, want 🔥 HighCPU", got)
	}
	if got := render(t, `{{ .method }} {{ .route }}`); got != "POST alertmanager" {
		t.Errorf("got %q", got)
	}
	if got := render(t, `{{ index .headers "x-source" }}`); got != "ci" {
		t.Errorf("got %q, want ci", got)
	}
}

func TestMissingKeyTolerated(t *testing.T) {
	// A missing top-level key renders to "<no value>", never an error.
	if got := render(t, `{{ .payload.absent }}`); got != "<no value>" {
		t.Errorf("got %q, want <no value>", got)
	}
}

func TestSproutHelperAvailable(t *testing.T) {
	if got := render(t, `{{ .payload.name | toUpper }}`); got != "WORLD" {
		t.Errorf("got %q, want WORLD", got)
	}
}

// TestEnvAvailable confirms env is usable in a route field and renders the
// process value; filesystem/network helpers stay excluded (compile fails).
func TestEnvAvailable(t *testing.T) {
	t.Setenv("CHASKI_TEST_ENV", "from-env")
	if got := render(t, `{{ env "CHASKI_TEST_ENV" }}`); got != "from-env" {
		t.Errorf("env render = %q, want from-env", got)
	}
	if _, err := Compile("x", `{{ readFile "/etc/passwd" }}`); err == nil {
		t.Error("Compile of a template using readFile should fail (filesystem is excluded)")
	}
}

func TestMapRenderDropsFailingKeys(t *testing.T) {
	m, err := CompileMap("params", map[string]string{
		"priority": "2",
		"label":    `{{ .payload.name }}`,
		"bad":      `{{ .payload.name.Foo }}`, // field access on a string -> render error
	})
	if err != nil {
		t.Fatalf("CompileMap: %v", err)
	}
	out, dropped := m.Render(sampleData())

	if out["priority"] != "2" || out["label"] != "world" {
		t.Errorf("out = %v, want priority=2 label=world", out)
	}
	if _, ok := out["bad"]; ok {
		t.Error("failing key should be dropped from output")
	}
	if _, ok := dropped["bad"]; !ok {
		t.Errorf("failing key should be reported in dropped, got %v", dropped)
	}
}

func TestEmptyMapRenders(t *testing.T) {
	m, err := CompileMap("params", nil)
	if err != nil {
		t.Fatalf("CompileMap(nil): %v", err)
	}
	out, dropped := m.Render(sampleData())
	if out != nil || dropped != nil {
		t.Errorf("empty map: out=%v dropped=%v, want nil nil", out, dropped)
	}
}
