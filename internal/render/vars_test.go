package render

import (
	"testing"
	"time"
)

// TestRenderVarsAndEnvCompose exercises the template variables not covered by
// the field tests (.query, .now) and confirms env composes with a sprout helper
// in a single template after the funcmap unification.
func TestRenderVarsAndEnvCompose(t *testing.T) {
	t.Setenv("CHASKI_TEST_COMPOSE", "hi")
	d := Data{
		Payload: map[string]any{},
		Query:   map[string]string{"id": "abc"},
		Now:     time.Unix(1_700_000_000, 0),
	}
	for _, tc := range []struct{ tmpl, want string }{
		{`{{ .query.id }}`, "abc"},
		{`{{ .now.Unix }}`, "1700000000"},
		{`{{ env "CHASKI_TEST_COMPOSE" | toUpper }}`, "HI"},
	} {
		tpl, err := Compile("t", tc.tmpl)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.tmpl, err)
		}
		got, err := tpl.Render(d)
		if err != nil {
			t.Fatalf("Render(%q): %v", tc.tmpl, err)
		}
		if got != tc.want {
			t.Errorf("render(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}
