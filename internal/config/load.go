package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

// LoadRouteConfig loads the routes + targets from path, which may be a single
// YAML file or a directory of *.yaml/*.yml fragments merged additively
// (config.d). It env-renders the plain config values and validates the merged
// result. An empty file or a directory with no fragments yields an empty config
// (the server boots idle); a malformed or contradictory config is an error.
func LoadRouteConfig(path string) (*RouteConfig, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	var rc *RouteConfig
	if info.IsDir() {
		rc, err = loadDir(path)
	} else {
		rc, err = loadFile(path)
	}
	if err != nil {
		return nil, err
	}

	if err := renderEnv(rc); err != nil {
		return nil, err
	}
	if err := rc.Validate(); err != nil {
		return nil, err
	}
	return rc, nil
}

func emptyConfig() *RouteConfig {
	return &RouteConfig{
		Routes:          map[string]*Route{},
		Targets:         map[string]*Target{},
		Templates:       map[string]string{},
		templateSources: map[string]string{},
	}
}

// readFragment reads and strict-decodes one fragment file. An empty or
// comment-only file decodes to (nil, nil) — the caller decides what that means
// (an idle config for a single file, a skipped fragment in a directory).
func readFragment(path string) (*RouteConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator config path; dir-contained for config.d
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return decodeFragment(data, filepath.Base(path))
}

func loadFile(path string) (*RouteConfig, error) {
	frag, err := readFragment(path)
	if err != nil {
		return nil, err
	}
	if frag == nil {
		return emptyConfig(), nil
	}
	return frag, nil
}

// loadDir reads every fragment in one directory level and merges them. Files
// are selected and ordered by selectFiles; fragments union additively with a
// duplicate name being fatal (order therefore never changes the outcome).
func loadDir(dir string) (*RouteConfig, error) {
	files, err := selectFiles(dir)
	if err != nil {
		return nil, err
	}

	acc := emptyConfig()
	for _, file := range files {
		frag, err := readFragment(file)
		if err != nil {
			return nil, err
		}
		if frag == nil {
			continue // empty / comment-only fragment: a no-op
		}
		if err := union(acc, frag); err != nil {
			return nil, err
		}
	}
	return acc, nil
}

// selectFiles returns the fragment files in dir, lexicographically sorted by
// name. It skips dot-prefixed entries (so a Kubernetes-projected ConfigMap's
// ..data symlink and ..timestamp dir are ignored and each key is read once),
// non-YAML files, kustomization.yaml, and directories; it follows a symlink
// only when it resolves to a regular file inside dir, and rejects a config dir
// holding two names that differ only in case.
func selectFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	dirReal, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("config: resolving config dir %q: %w", dir, err)
	}

	var files []string
	seenLower := make(map[string]string)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // ., .., ..data, ..2026_*-timestamp, editor dotfiles
		}
		lower := strings.ToLower(name)
		if lower == "kustomization.yaml" || lower == "kustomization.yml" {
			continue // a Kustomize/Flux build file co-mounted by mistake
		}
		if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") {
			continue
		}

		full := filepath.Join(dir, name)
		info, err := os.Stat(full) // follows symlinks
		if err != nil {
			return nil, fmt.Errorf("config: stat %q: %w", name, err)
		}
		if info.IsDir() {
			continue // non-recursive
		}
		if !info.Mode().IsRegular() {
			continue // sockets, devices, …
		}

		real, err := filepath.EvalSymlinks(full)
		if err != nil {
			return nil, fmt.Errorf("config: resolving %q: %w", name, err)
		}
		if !within(dirReal, real) {
			return nil, fmt.Errorf("config: %q resolves outside the config directory (symlink escape)", name)
		}

		if prev, ok := seenLower[lower]; ok {
			return nil, fmt.Errorf("config: %q and %q differ only in case; the config directory must be case-stable", prev, name)
		}
		seenLower[lower] = name
		files = append(files, full)
	}

	slices.Sort(files) // shared dir prefix ⇒ sorts by file name
	return files, nil
}

// within reports whether target is dir itself or lies beneath it, after both
// have been symlink-resolved. The legitimate Kubernetes ..data indirection
// resolves back under dir and passes.
func within(dir, target string) bool {
	if target == dir {
		return true
	}
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// decodeFragment strict-decodes one YAML fragment. An empty or comment-only
// file is a no-op (nil, nil). Source provenance is stamped on every decoded
// route/target for error attribution.
func decodeFragment(data []byte, name string) (*RouteConfig, error) {
	if isEffectivelyEmpty(data) {
		return nil, nil
	}

	var rc RouteConfig
	// Strict decoding: an unknown/typo'd key is an error, not a silent no-op.
	if err := yaml.Load(data, &rc, yaml.WithKnownFields()); err != nil {
		// The split-files hint only applies to the multi-document error; don't
		// append it to unrelated parse errors (a typo, bad indentation, …).
		if strings.Contains(err.Error(), "expected single document") {
			return nil, fmt.Errorf("config: %q has multiple YAML documents; split them into separate files", name)
		}
		return nil, fmt.Errorf("config: parsing %q: %w", name, err)
	}

	// A valueless key ("routes:\n  r:\n") decodes to a nil entry; reject it with
	// a clear, provenance-bearing error rather than panicking downstream.
	for rn, r := range rc.Routes {
		if r == nil {
			return nil, fmt.Errorf("config: %q: route %q has no configuration", name, rn)
		}
		r.Source = name
	}
	for tn, t := range rc.Targets {
		if t == nil {
			return nil, fmt.Errorf("config: %q: target %q has no configuration", name, tn)
		}
		t.Source = name
	}
	if len(rc.Templates) > 0 {
		rc.templateSources = make(map[string]string, len(rc.Templates))
		for tpl := range rc.Templates {
			rc.templateSources[tpl] = name
		}
	}
	return &rc, nil
}

// isEffectivelyEmpty reports whether data has no YAML content — only blank
// lines, comments, or a bare document marker. The decoder errors on such input
// ("no documents in stream"), so the loader treats it as a skippable no-op
// rather than a fatal parse error.
func isEffectivelyEmpty(data []byte) bool {
	for line := range strings.SplitSeq(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t == "---" || t == "..." || strings.HasPrefix(t, "#") {
			continue
		}
		return false
	}
	return true
}

// union merges frag into acc, erroring on a duplicate route or target name and
// naming both source files. Routes and targets are independent namespaces.
func union(acc, frag *RouteConfig) error {
	for name, r := range frag.Routes {
		if prev, ok := acc.Routes[name]; ok {
			return fmt.Errorf("config: duplicate route %q defined in %s and %s", name, prev.Source, r.Source)
		}
		acc.Routes[name] = r
	}
	for name, t := range frag.Targets {
		if prev, ok := acc.Targets[name]; ok {
			return fmt.Errorf("config: duplicate target %q defined in %s and %s", name, prev.Source, t.Source)
		}
		acc.Targets[name] = t
	}
	for name, body := range frag.Templates {
		if _, ok := acc.Templates[name]; ok {
			return fmt.Errorf("config: duplicate template %q defined in %s and %s", name, acc.templateSources[name], frag.templateSources[name])
		}
		acc.Templates[name] = body
		acc.templateSources[name] = frag.templateSources[name]
	}
	return nil
}
