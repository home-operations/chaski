package sink

import (
	"context"
	"strings"
	"testing"
)

// The notifier's error paths must never echo the credential-bearing target URL:
// it would flow through observeRelay into logs.

func TestNotifierParseErrorOmitsURL(t *testing.T) {
	// A control character makes url.Parse fail; the URL carries a credential.
	bad := "pover://user:supersecret@host/\x7f"
	err := appriseNotifier{}.Send(t.Context(), bad, "body", "", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("Send = nil, want parse error")
	}
	if !isPermanent(err) {
		t.Errorf("parse error should be permanent (no retry), got %v", err)
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Errorf("error leaks the credential: %v", err)
	}
}

func TestNotifierAddErrorRedactsURL(t *testing.T) {
	// An unsupported scheme fails apprise's Add; its error text may echo the URL.
	bad := "bogus://user:supersecret@host/x"
	err := appriseNotifier{}.Send(t.Context(), bad, "body", "", nil)
	if err == nil {
		t.Fatal("Send = nil, want invalid-scheme error")
	}
	if !isPermanent(err) {
		t.Errorf("invalid scheme should be permanent (no retry), got %v", err)
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Errorf("error leaks the credential: %v", err)
	}
}

func TestNotifierCancelledContextShortCircuits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (appriseNotifier{}).Send(ctx, "pover://u@t/", "body", "", nil); err == nil {
		t.Error("Send = nil, want the context error before any send")
	}
}
