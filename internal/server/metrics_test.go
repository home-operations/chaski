package server

import (
	"net/http"
	"runtime"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestWebhookRejectedMetric(t *testing.T) {
	srv := newServer(engineWithRoute(t, &fakeNotifier{}), "tok")
	authed := map[string]string{"Content-Type": jsonCT, "Authorization": "Bearer tok"}

	cases := []struct {
		reason  string
		headers map[string]string
		target  string
		body    string
	}{
		{"unauthorized", map[string]string{"Content-Type": jsonCT}, "/notify/alertmanager", `{}`},
		{"not_found", authed, "/notify/ghost", `{}`},
		{"decode", map[string]string{"Content-Type": "text/plain", "Authorization": "Bearer tok"}, "/notify/alertmanager", "hi"},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			before := testutil.ToFloat64(webhookRejected.WithLabelValues(c.reason))
			do(srv, http.MethodPost, c.target, c.body, c.headers)
			if got := testutil.ToFloat64(webhookRejected.WithLabelValues(c.reason)) - before; got != 1 {
				t.Errorf("%s delta = %v, want 1", c.reason, got)
			}
		})
	}
}

func TestBuildInfoMetric(t *testing.T) {
	RecordBuildInfo("v1.2.3", "abc123")
	if got := testutil.ToFloat64(buildInfo.WithLabelValues("v1.2.3", "abc123", runtime.Version())); got != 1 {
		t.Errorf("build_info = %v, want 1", got)
	}
}
