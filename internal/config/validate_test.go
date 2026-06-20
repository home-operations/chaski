package config

import "testing"

// TestValidateStatus covers the override-status check, especially that 0 (the
// "use the default" sentinel) is accepted, not rejected.
func TestValidateStatus(t *testing.T) {
	cases := []struct {
		code int
		ok   bool
	}{
		{0, true}, // 0 = leave the default (200 relay / 204 skip), not an error
		{100, true}, {200, true}, {204, true}, {599, true},
		{99, false}, {600, false}, {-1, false},
	}
	for _, tc := range cases {
		err := validateStatus(tc.code, "r", "c.yaml", "status")
		if (err == nil) != tc.ok {
			t.Errorf("validateStatus(%d) err=%v, want ok=%v", tc.code, err, tc.ok)
		}
	}
}
