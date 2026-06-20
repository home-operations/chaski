package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "chaski.yaml")
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestValidateOK(t *testing.T) {
	file := writeCfg(t, `
targets:
  po: { apprise: { url: 'pover://u@t/' } }
routes:
  alertmanager:
    target: po
    whenExpr: 'payload.status == "firing"'
    message: 'alert: {{ .payload.status }}'
`)
	var buf bytes.Buffer
	if err := runValidate(&buf, []string{"-c", file}); err != nil {
		t.Fatalf("runValidate: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"ok:", "alertmanager", "po", "apprise"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestValidateErrors(t *testing.T) {
	tests := map[string]string{
		"non-bool whenExpr": `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes: { r: { target: po, whenExpr: '42' } }
`,
		"bad template": `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes: { r: { target: po, message: '{{ .payload. }}' } }
`,
		"unknown target": `
routes: { r: { target: ghost, message: 'x' } }
`,
		"missing env var": `
targets: { po: { apprise: { url: 'pover://{{ env "CHASKI_VALIDATE_DEFINITELY_UNSET" }}@t/' } } }
routes: { r: { target: po, message: 'x' } }
`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if err := runValidate(io.Discard, []string{"-c", writeCfg(t, body)}); err == nil {
				t.Fatalf("runValidate(%s) = nil error, want error", name)
			}
		})
	}
}
