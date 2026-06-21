package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadTarget loads a config whose route "r" uses the given `target:` YAML, with
// targets a and b defined, and returns the decoded TargetRefs (or the error).
func loadTarget(t *testing.T, targetYAML string) (TargetRefs, error) {
	t.Helper()
	body := "targets:\n  a: { apprise: { url: 'pover://u@t/a' } }\n  b: { apprise: { url: 'pover://u@t/b' } }\nroutes:\n  r:\n    message: m\n    target: " + targetYAML + "\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := LoadRouteConfig(path)
	if err != nil {
		return nil, err
	}
	return rc.Routes["r"].Target, nil
}

func TestTargetRefsDecode(t *testing.T) {
	t.Run("scalar", func(t *testing.T) {
		got, err := loadTarget(t, "a")
		if err != nil || len(got) != 1 || got[0].Name != "a" || got[0].WhenExpr != "" {
			t.Fatalf("got %+v, err %v", got, err)
		}
	})
	t.Run("list of names", func(t *testing.T) {
		got, err := loadTarget(t, "[a, b]")
		if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
			t.Fatalf("got %+v, err %v", got, err)
		}
	})
	t.Run("single object", func(t *testing.T) {
		got, err := loadTarget(t, `{ name: a, whenExpr: 'payload.x == 1' }`)
		if err != nil || len(got) != 1 || got[0].Name != "a" || got[0].WhenExpr != "payload.x == 1" {
			t.Fatalf("got %+v, err %v", got, err)
		}
	})
	t.Run("mixed names and objects", func(t *testing.T) {
		got, err := loadTarget(t, `[a, { name: b, whenExpr: 'payload.x == 1' }]`)
		if err != nil {
			t.Fatalf("err %v", err)
		}
		if len(got) != 2 || got[0].Name != "a" || got[0].WhenExpr != "" ||
			got[1].Name != "b" || got[1].WhenExpr != "payload.x == 1" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("rejections", func(t *testing.T) {
		for _, bad := range []string{
			`[{ name: a, bogus: 1 }]`,      // unknown key
			`[{ whenExpr: 'true' }]`,       // missing name
			`[{ name: a, whenExpr: [x] }]`, // non-scalar value
			`""`,                           // empty name
			`[{ name: a, name: b }]`,       // duplicate key
		} {
			if _, err := loadTarget(t, bad); err == nil {
				t.Errorf("target %q decoded without error, want rejection", bad)
			}
		}
	})
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const targetsYAML = `
targets:
  pushover:
    apprise:
      url: 'pover://{{ env "PUSHOVER_USER" }}@{{ env "PUSHOVER_TOKEN" }}/?sound=pushover'
  ingest:
    http:
      method: POST
      url: 'https://ingest.internal.svc/events'
      headers:
        Authorization: 'Bearer {{ env "INGEST_TOKEN" }}'
      timeout: 10s
      retry: { attempts: 3, backoff: 200ms }
`

const routesYAML = `
routes:
  alertmanager:
    target: pushover
    whenExpr: 'payload.status == "firing"'
    title: '🔥 {{ .payload.commonLabels.alertname }}'
    params: { priority: '2' }
  deploy:
    target: [pushover, ingest]
    title: 'Deployed {{ .payload.app }}'
    headers: { X-Event-Id: '{{ .payload.id }}' }
`

func setSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("PUSHOVER_USER", "u123")
	t.Setenv("PUSHOVER_TOKEN", "t456")
	t.Setenv("INGEST_TOKEN", "bearer789")
}

func TestLoadFile(t *testing.T) {
	setSecrets(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "chaski.yaml")
	write(t, file, targetsYAML+routesYAML)

	rc, err := LoadRouteConfig(file)
	if err != nil {
		t.Fatalf("LoadRouteConfig: %v", err)
	}
	if len(rc.Routes) != 2 || len(rc.Targets) != 2 {
		t.Fatalf("routes=%d targets=%d, want 2 and 2", len(rc.Routes), len(rc.Targets))
	}

	po := rc.Targets["pushover"]
	if po.Kind() != "apprise" {
		t.Errorf("pushover kind = %q, want apprise", po.Kind())
	}
	if !strings.Contains(po.Apprise.URL, "u123@t456") {
		t.Errorf("env not rendered into apprise url: %q", po.Apprise.URL)
	}

	ing := rc.Targets["ingest"]
	if ing.Kind() != "http" {
		t.Fatalf("ingest kind = %q, want http", ing.Kind())
	}
	if ing.HTTP.Timeout.AsDuration() != 10*time.Second {
		t.Errorf("timeout = %s, want 10s", ing.HTTP.Timeout.AsDuration())
	}
	if ing.HTTP.Retry.Backoff.AsDuration() != 200*time.Millisecond {
		t.Errorf("backoff = %s, want 200ms", ing.HTTP.Retry.Backoff.AsDuration())
	}
	if ing.HTTP.Headers["Authorization"] != "Bearer bearer789" {
		t.Errorf("env not rendered into http header: %q", ing.HTTP.Headers["Authorization"])
	}

	// Route field templates must be left VERBATIM (not env-rendered).
	if got := rc.Routes["alertmanager"].Title; got != "🔥 {{ .payload.commonLabels.alertname }}" {
		t.Errorf("route title was altered at load: %q", got)
	}
	// target scalar vs list both decode.
	if d := rc.Routes["deploy"].Target; len(d) != 2 || d[0].Name != "pushover" || d[1].Name != "ingest" {
		t.Errorf("deploy target = %v, want [pushover ingest]", d)
	}
}

func TestLoadDirAdditiveUnion(t *testing.T) {
	setSecrets(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "10-targets.yaml"), targetsYAML)
	write(t, filepath.Join(dir, "50-routes.yaml"), routesYAML)

	rc, err := LoadRouteConfig(dir)
	if err != nil {
		t.Fatalf("LoadRouteConfig: %v", err)
	}
	// Cross-file reference (routes in 50- reference targets in 10-) resolves
	// post-merge.
	if len(rc.Routes) != 2 || len(rc.Targets) != 2 {
		t.Fatalf("routes=%d targets=%d, want 2 and 2", len(rc.Routes), len(rc.Targets))
	}
	if rc.Routes["alertmanager"].Source != "50-routes.yaml" {
		t.Errorf("source = %q, want 50-routes.yaml", rc.Routes["alertmanager"].Source)
	}
}

func TestDuplicateNamesAreFatal(t *testing.T) {
	setSecrets(t)
	for _, tc := range []struct{ name, a, b, want string }{
		{"route", "routes:\n  dup: {target: x}\n", "routes:\n  dup: {target: x}\n", `duplicate route "dup"`},
		{"target", "targets:\n  dup: {apprise: {url: 'pover://u@t/'}}\n", "targets:\n  dup: {apprise: {url: 'pover://u@t/'}}\n", `duplicate target "dup"`},
		{"template", "templates:\n  dup: 'a'\n", "templates:\n  dup: 'b'\n", `duplicate template "dup"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "10-a.yaml"), tc.a)
			write(t, filepath.Join(dir, "20-b.yaml"), tc.b)
			_, err := LoadRouteConfig(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			// The error (guaranteed non-nil above) must name both source files.
			if !strings.Contains(err.Error(), "10-a.yaml") || !strings.Contains(err.Error(), "20-b.yaml") {
				t.Errorf("error should name both files: %v", err)
			}
		})
	}
}

func TestDuplicateTargetInRouteRejected(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "c.yaml"),
		"targets:\n  x: {apprise: {url: 'pover://u@t/'}}\nroutes:\n  r:\n    message: m\n    target:\n      - { name: x, whenExpr: 'payload.a' }\n      - { name: x, whenExpr: 'payload.b' }\n")
	_, err := LoadRouteConfig(dir)
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("err = %v, want a duplicate-target error guiding to one whenExpr", err)
	}
}

func TestDescriptionRoundTrips(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "c.yaml"),
		"targets:\n  a: { description: a sink, apprise: { url: 'pover://u@t/' } }\nroutes:\n  r: { description: a route, target: a, message: m }\n")
	rc, err := LoadRouteConfig(dir)
	if err != nil {
		t.Fatalf("LoadRouteConfig: %v", err)
	}
	if got := rc.Routes["r"].Description; got != "a route" {
		t.Errorf("route description = %q, want %q", got, "a route")
	}
	if got := rc.Targets["a"].Description; got != "a sink" {
		t.Errorf("target description = %q, want %q", got, "a sink")
	}
}

func TestSinkValidation(t *testing.T) {
	tests := map[string]string{
		"both sinks":     "targets:\n  x: {apprise: {url: 'pover://u@t/'}, http: {url: 'https://h'}}\n",
		"neither sink":   "targets:\n  x: {}\n",
		"unknown key":    "targets:\n  x: {apprise: {url: 'pover://u@t/'}, bogus: 1}\n",
		"bad method":     "targets:\n  x:\n    http: {url: 'https://h', method: FETCH}\nroutes:\n  r: {target: x}\n",
		"missing target": "routes:\n  r: {target: nope}\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "c.yaml"), body)
			if _, err := LoadRouteConfig(dir); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestMissingEnvVarIsFatal(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "c.yaml"), "targets:\n  x:\n    apprise: {url: 'pover://{{ env \"NOPE_MISSING\" }}@t/'}\n")
	_, err := LoadRouteConfig(dir)
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestMultiDocumentRejected(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "c.yaml"), "targets:\n  a: {apprise: {url: 'pover://u@t/'}}\n---\ntargets:\n  b: {apprise: {url: 'pover://u@t/'}}\n")
	_, err := LoadRouteConfig(dir)
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("err = %v, want a multi-document error", err)
	}
}

// TestParseErrorHasNoMultiDocSuffix: a plain syntax error must not be tagged
// with the (multi-document) hint that only applies to multi-document files.
func TestParseErrorHasNoMultiDocSuffix(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "c.yaml"), "targets:\n  a: {apprise: {url: 'pover://u@t/'}}\n bad: indentation\n")
	_, err := LoadRouteConfig(dir)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), "multi") || strings.Contains(err.Error(), "split") {
		t.Errorf("plain parse error carries the multi-document hint: %v", err)
	}
}

func TestEmptyAndCommentOnlyFragmentsSkipped(t *testing.T) {
	setSecrets(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "00-empty.yaml"), "")
	write(t, filepath.Join(dir, "05-comment.yaml"), "# reserved for later\n\n")
	write(t, filepath.Join(dir, "10-targets.yaml"), targetsYAML)
	write(t, filepath.Join(dir, "50-routes.yaml"), routesYAML)

	rc, err := LoadRouteConfig(dir)
	if err != nil {
		t.Fatalf("empty/comment fragments should be skipped, got: %v", err)
	}
	if len(rc.Routes) != 2 {
		t.Errorf("routes = %d, want 2", len(rc.Routes))
	}
}

func TestZeroFilesDirIsEmptyNotError(t *testing.T) {
	rc, err := LoadRouteConfig(t.TempDir())
	if err != nil {
		t.Fatalf("empty dir should not error: %v", err)
	}
	if len(rc.Routes) != 0 || len(rc.Targets) != 0 {
		t.Errorf("want empty config, got routes=%d targets=%d", len(rc.Routes), len(rc.Targets))
	}
}

// TestProjectedConfigMapLayout simulates how Kubernetes mounts a ConfigMap as a
// directory: a ..data symlink to a ..timestamp dir, with each key a symlink
// into it. The loader must read each key exactly once and ignore the dotfiles
// and non-YAML junk.
func TestProjectedConfigMapLayout(t *testing.T) {
	setSecrets(t)
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "..2026_06_20_12_00_00.123")
	write(t, filepath.Join(dataDir, "10-targets.yaml"), targetsYAML)
	write(t, filepath.Join(dataDir, "50-routes.yaml"), routesYAML)

	symlink := func(oldname, newname string) {
		if err := os.Symlink(oldname, filepath.Join(dir, newname)); err != nil {
			t.Fatal(err)
		}
	}
	symlink("..2026_06_20_12_00_00.123", "..data")
	symlink("..data/10-targets.yaml", "10-targets.yaml")
	symlink("..data/50-routes.yaml", "50-routes.yaml")
	// Junk that must be ignored, not read as config:
	write(t, filepath.Join(dir, "README.md"), "# notes")
	write(t, filepath.Join(dir, "10-targets.yaml.bak"), "garbage: true")
	write(t, filepath.Join(dir, ".hidden.yaml"), "garbage: true")

	rc, err := LoadRouteConfig(dir)
	if err != nil {
		t.Fatalf("projected ConfigMap layout should load cleanly: %v", err)
	}
	if len(rc.Routes) != 2 || len(rc.Targets) != 2 {
		t.Fatalf("routes=%d targets=%d, want 2 and 2 (junk/dotfiles must be ignored)", len(rc.Routes), len(rc.Targets))
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.yaml")
	write(t, secret, "targets:\n  x: {apprise: {url: 'pover://u@t/'}}\n")

	dir := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(dir, "evil.yaml")); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRouteConfig(dir)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("err = %v, want a symlink-escape error", err)
	}
}
