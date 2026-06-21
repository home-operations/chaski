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

func writePayload(t *testing.T, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

const oneRoute = `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  alertmanager:
    target: po
    whenExpr: 'payload.status == "firing"'
    message: 'alert: {{ .payload.status }}'
`

func TestValidatePayloadRendersPlan(t *testing.T) {
	var buf bytes.Buffer
	args := []string{"-c", writeCfg(t, oneRoute), "--payload", writePayload(t, `{"status":"firing"}`)}
	if err := runValidate(&buf, args); err != nil {
		t.Fatalf("runValidate: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"fired": true`) || !strings.Contains(out, "alert: firing") {
		t.Errorf("plan output missing rendered fields:\n%s", out)
	}
}

func TestValidatePayloadGateFalse(t *testing.T) {
	var buf bytes.Buffer
	args := []string{"-c", writeCfg(t, oneRoute), "--payload", writePayload(t, `{"status":"resolved"}`)}
	if err := runValidate(&buf, args); err != nil {
		t.Fatalf("runValidate: %v", err)
	}
	if !strings.Contains(buf.String(), `"fired": false`) {
		t.Errorf("gate-false render, want a plan with fired:false:\n%s", buf.String())
	}
}

func TestValidatePayloadRenderErrorFails(t *testing.T) {
	cfg := writeCfg(t, `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  r: { target: po, message: '{{ .payload.x.Bad }}' }
`)
	args := []string{"-c", cfg, "--payload", writePayload(t, `{"x":"scalar"}`)}
	if err := runValidate(io.Discard, args); err == nil {
		t.Fatal("want a render error for a bad field path against the sample payload")
	}
}

func TestValidatePayloadRouteSelection(t *testing.T) {
	cfg := writeCfg(t, `
targets: { po: { apprise: { url: 'pover://u@t/' } } }
routes:
  a: { target: po, message: 'A {{ .payload.v }}' }
  b: { target: po, message: 'B {{ .payload.v }}' }
`)
	pl := writePayload(t, `{"v":"1"}`)
	if err := runValidate(io.Discard, []string{"-c", cfg, "--payload", pl}); err == nil {
		t.Error("multiple routes with no --route: want an error")
	}
	var buf bytes.Buffer
	if err := runValidate(&buf, []string{"-c", cfg, "--payload", pl, "--route", "b"}); err != nil {
		t.Fatalf("--route b: %v", err)
	}
	if !strings.Contains(buf.String(), "B 1") {
		t.Errorf("--route b, want rendered 'B 1':\n%s", buf.String())
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
