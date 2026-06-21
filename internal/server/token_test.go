package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInboundToken(t *testing.T) {
	t.Run("bearer header", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/hooks/x", nil)
		r.Header.Set("Authorization", "Bearer abc")
		if got := inboundToken(r); got != "abc" {
			t.Errorf("token = %q, want abc", got)
		}
	})

	t.Run("query fallback when no bearer header", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/hooks/x?token=q123", nil)
		if got := inboundToken(r); got != "q123" {
			t.Errorf("token = %q, want q123", got)
		}
	})

	t.Run("none present", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/hooks/x", nil)
		if got := inboundToken(r); got != "" {
			t.Errorf("token = %q, want empty", got)
		}
	})
}
