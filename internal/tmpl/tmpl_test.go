package tmpl

import "testing"

// TestFuncMapExcludesIO is the load-bearing guarantee: a template must not be
// able to reach the filesystem or network. env IS available (a secret can be
// interpolated), but the I/O registries stay out.
func TestFuncMapExcludesIO(t *testing.T) {
	fm := FuncMap()

	for _, banned := range []string{"readFile", "readDir", "getHostByName"} {
		if _, ok := fm[banned]; ok {
			t.Errorf("funcmap must not expose %q", banned)
		}
	}
	// A safe helper from the strings registry should be present.
	if _, ok := fm["trim"]; !ok {
		t.Error("funcmap should include safe helpers (e.g. trim)")
	}
}

// TestFuncMapHasStrictEnv asserts env is available everywhere and errors on an
// unset variable rather than rendering empty.
func TestFuncMapHasStrictEnv(t *testing.T) {
	fm := FuncMap()

	fn, ok := fm["env"].(func(string) (string, error))
	if !ok {
		t.Fatalf("funcmap env = %T, want func(string) (string, error)", fm["env"])
	}

	t.Setenv("CHASKI_TEST_VAR", "present")
	if v, err := fn("CHASKI_TEST_VAR"); err != nil || v != "present" {
		t.Errorf("env(set) = %q, %v; want present, nil", v, err)
	}
	if _, err := fn("CHASKI_TEST_DEFINITELY_UNSET"); err == nil {
		t.Error("env(unset) should error (strict), got nil")
	}
}
