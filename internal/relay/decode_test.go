package relay_test

import (
	"testing"

	"github.com/home-operations/chaski/internal/relay"
)

func TestDecodeBody(t *testing.T) {
	t.Run("empty JSON body becomes an empty object", func(t *testing.T) {
		p, _, err := relay.DecodeBody("application/json", nil)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if m, ok := p.(map[string]any); !ok || len(m) != 0 {
			t.Errorf("payload = %#v, want empty map", p)
		}
	})

	t.Run("absent content-type defaults to JSON", func(t *testing.T) {
		p, _, err := relay.DecodeBody("", []byte(`{"a":1}`))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if m, _ := p.(map[string]any); m["a"] != float64(1) {
			t.Errorf("payload = %#v, want a=1", p)
		}
	})

	t.Run("a content-type parameter is tolerated", func(t *testing.T) {
		if _, _, err := relay.DecodeBody("application/json; charset=utf-8", []byte(`{}`)); err != nil {
			t.Errorf("charset parameter should be ignored: %v", err)
		}
	})

	t.Run("form repeats become a list, singles stay strings", func(t *testing.T) {
		p, _, err := relay.DecodeBody("application/x-www-form-urlencoded", []byte("k=1&k=2&x=y"))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		m, _ := p.(map[string]any)
		if m["x"] != "y" {
			t.Errorf("x = %#v, want \"y\"", m["x"])
		}
		ks, ok := m["k"].([]any)
		if !ok || len(ks) != 2 || ks[0] != "1" || ks[1] != "2" {
			t.Errorf("k = %#v, want []any{\"1\", \"2\"}", m["k"])
		}
	})

	t.Run("unsupported content-type errors", func(t *testing.T) {
		if _, _, err := relay.DecodeBody("text/plain", []byte("hi")); err == nil {
			t.Error("want an error for an unsupported content-type")
		}
	})

	t.Run("invalid JSON errors", func(t *testing.T) {
		if _, _, err := relay.DecodeBody("application/json", []byte("{bad")); err == nil {
			t.Error("want an error for invalid JSON")
		}
	})
}
