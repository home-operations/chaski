package gate

import (
	"context"
	"testing"
	"time"
)

func mustCompile(t *testing.T, expr string) *Gate {
	t.Helper()
	g, err := Compile(expr)
	if err != nil {
		t.Fatalf("Compile(%q): %v", expr, err)
	}
	return g
}

func sampleInput() Input {
	return Input{
		Payload: map[string]any{
			"status":    "firing",
			"count":     float64(10), // JSON numbers decode as float64 (CEL double)
			"msg":       "abcdef",
			"name":      "world",
			"client_ip": "10.1.2.3",
		},
		Headers: map[string]string{"x-source": "ci"},
		Query:   map[string]string{"dry": "1"},
		Method:  "POST",
		Route:   "alertmanager",
		Now:     time.Unix(1_700_000_000, 0),
	}
}

func eval(t *testing.T, expr string) bool {
	t.Helper()
	out, err := mustCompile(t, expr).Eval(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Eval(%q): %v", expr, err)
	}
	return out
}

func TestCompileErrors(t *testing.T) {
	for _, expr := range []string{
		`payload.status ==`,  // syntax error
		`bogus.field == "x"`, // unknown variable
		`42`,                 // non-boolean literal
		`"a string"`,         // non-boolean literal
		`payload.count + 1`,  // non-boolean (dyn arithmetic is still int/double)
	} {
		if _, err := Compile(expr); err == nil {
			t.Errorf("Compile(%q) = nil error, want error", expr)
		}
	}
}

func TestEmptyExprAlwaysFires(t *testing.T) {
	g := mustCompile(t, "")
	got, err := g.Eval(context.Background(), Input{})
	if err != nil || !got {
		t.Fatalf("empty gate = %v, %v; want true, nil", got, err)
	}
}

func TestEvalTrueFalse(t *testing.T) {
	tests := map[string]bool{
		`payload.status == "firing"`:                  true,
		`payload.status == "resolved"`:                false,
		`headers["x-source"] == "ci"`:                 true,
		`method == "POST" && route == "alertmanager"`: true,
		`payload.count > 5.0`:                         true, // double compare
		`has(payload.nope)`:                           false,
		`has(payload.status) && payload.status != ""`: true,
		`query["dry"] == "1"`:                         true,
	}
	for expr, want := range tests {
		if got := eval(t, expr); got != want {
			t.Errorf("eval(%q) = %v, want %v", expr, got, want)
		}
	}
}

func TestHelpers(t *testing.T) {
	// toJSON + the strings extension compose.
	if !eval(t, `toJSON(payload).contains("firing")`) {
		t.Error(`toJSON(payload).contains("firing") = false, want true`)
	}
	// truncate is rune-safe prefix.
	if !eval(t, `truncate(payload.msg, 3) == "abc"`) {
		t.Error(`truncate(payload.msg, 3) != "abc"`)
	}
	if !eval(t, `truncate(payload.msg, 100) == "abcdef"`) {
		t.Error(`truncate beyond length should return the whole string`)
	}
}

func TestNetworkExtension(t *testing.T) {
	tests := map[string]bool{
		`isIP(string(payload.client_ip))`:                        true,
		`isIP("not-an-ip")`:                                      false,
		`cidr("10.0.0.0/8").containsIP(ip(payload.client_ip))`:   true,
		`cidr("10.0.0.0/8").containsIP(payload.client_ip)`:       true, // string overload
		`cidr("192.168.0.0/16").containsIP(payload.client_ip)`:   false,
		`isCIDR("10.0.0.0/8") && !isCIDR(payload.client_ip)`:     true,
		`ip(payload.client_ip).family() == 4`:                    true,
		`!ip(payload.client_ip).isLoopback()`:                    true,
		`cidr("10.0.0.0/8").containsCIDR(cidr("10.1.0.0/16"))`:   true,
		`cidr("10.0.0.0/8").containsCIDR(cidr("172.16.0.0/12"))`: false,
	}
	for expr, want := range tests {
		if got := eval(t, expr); got != want {
			t.Errorf("eval(%q) = %v, want %v", expr, got, want)
		}
	}
}

func TestEncodersExtension(t *testing.T) {
	if !eval(t, `base64.encode(bytes(payload.msg)) == "YWJjZGVm"`) {
		t.Error(`base64.encode(bytes("abcdef")) != "YWJjZGVm"`)
	}
	if !eval(t, `string(base64.decode("YWJjZGVm")) == payload.msg`) {
		t.Error(`base64.decode round-trip failed`)
	}
	// EncodersVersion(0) pins base64 only: json.encode must not exist (toJSON
	// remains the single JSON spelling).
	if _, err := Compile(`json.encode(payload) != ""`); err == nil {
		t.Error(`json.encode compiled; want unknown-function error (EncodersVersion(0))`)
	}
}

func TestNonBooleanResultIsRuntimeError(t *testing.T) {
	// Compiles (payload.name is dyn) but yields a string at runtime.
	g := mustCompile(t, `payload.name`)
	if _, err := g.Eval(context.Background(), sampleInput()); err == nil {
		t.Fatal("expected a runtime error for a non-boolean result")
	}
}
