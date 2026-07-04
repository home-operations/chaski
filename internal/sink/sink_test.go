package sink

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/home-operations/chaski/internal/config"
)

var fastRetry = RetryPolicy{Attempts: 3, Backoff: time.Millisecond}

// fakeNotifier records calls and fails the first failTimes times (or always,
// when permanent).
type fakeNotifier struct {
	failTimes  int
	permanent  bool
	calls      int
	lastURL    string
	lastTitle  string
	lastParams map[string]string
}

func (f *fakeNotifier) Send(_ context.Context, targetURL, _, title string, params map[string]string) error {
	f.calls++
	f.lastURL, f.lastTitle, f.lastParams = targetURL, title, params
	if f.permanent {
		return Permanent(errors.New("bad target"))
	}
	if f.calls <= f.failTimes {
		return errors.New("transient")
	}
	return nil
}

func TestAppriseSinkRetriesThenSucceeds(t *testing.T) {
	fn := &fakeNotifier{failTimes: 2}
	s := &appriseSink{name: "pushover", url: "pover://u@t/", notifier: fn, retry: fastRetry}

	err := s.Send(context.Background(), Message{Title: "hi", Body: "b", Params: map[string]string{"priority": "2"}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fn.calls != 3 {
		t.Errorf("calls = %d, want 3 (2 failures + success)", fn.calls)
	}
	if fn.lastTitle != "hi" || fn.lastParams["priority"] != "2" {
		t.Errorf("notifier got title=%q params=%v", fn.lastTitle, fn.lastParams)
	}
}

func TestAppriseSinkPermanentNotRetried(t *testing.T) {
	fn := &fakeNotifier{permanent: true}
	s := &appriseSink{name: "x", url: "pover://u@t/", notifier: fn, retry: fastRetry}
	if err := s.Send(context.Background(), Message{Body: "b"}); err == nil {
		t.Fatal("want error")
	}
	if fn.calls != 1 {
		t.Errorf("calls = %d, want 1 (permanent errors are not retried)", fn.calls)
	}
}

func TestMergeQuery(t *testing.T) {
	got, err := mergeQuery("pover://u@t/?sound=pushover", map[string]string{"priority": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "priority=2") || !strings.Contains(got, "sound=pushover") {
		t.Errorf("merged url = %q, want both priority and sound", got)
	}
	// No params → unchanged.
	if got, _ := mergeQuery("pover://u@t/", nil); got != "pover://u@t/" {
		t.Errorf("empty params changed url: %q", got)
	}
}

func httpTarget(url, method string) *config.Target {
	return &config.Target{HTTP: &config.HTTPSink{
		URL:     url,
		Method:  method,
		Headers: map[string]string{"Authorization": "Bearer secret", "Content-Type": "application/json"},
	}}
}

func TestHTTPSinkSuccessAndHeaderPrecedence(t *testing.T) {
	var gotAuth, gotEvent, gotBody, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotEvent = r.Header.Get("X-Event-Id")
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := New("ingest", httpTarget(srv.URL, "POST"), Options{DefaultRetry: fastRetry})
	if err != nil {
		t.Fatal(err)
	}
	// Route tries to override Authorization (must lose to the target) and adds X-Event-Id.
	msg := Message{Body: `{"x":1}`, Headers: map[string]string{"Authorization": "route-attempt", "X-Event-Id": "42"}}
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotMethod != "POST" || gotBody != `{"x":1}` {
		t.Errorf("method=%q body=%q", gotMethod, gotBody)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want the target's (route must not override)", gotAuth)
	}
	if gotEvent != "42" {
		t.Errorf("X-Event-Id = %q, want 42", gotEvent)
	}
}

func TestHTTPSink5xxRetriedThen4xxNot(t *testing.T) {
	t.Run("5xx exhausts retries", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		s, _ := New("x", httpTarget(srv.URL, "POST"), Options{DefaultRetry: fastRetry})
		if err := s.Send(context.Background(), Message{Body: "b"}); err == nil {
			t.Fatal("want error after exhausting retries")
		}
		if got := n.Load(); got != 3 {
			t.Errorf("requests = %d, want 3 (5xx is retried)", got)
		}
	})

	t.Run("4xx is permanent", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer srv.Close()
		s, _ := New("x", httpTarget(srv.URL, "POST"), Options{DefaultRetry: fastRetry})
		if err := s.Send(context.Background(), Message{Body: "b"}); err == nil {
			t.Fatal("want error")
		}
		if got := n.Load(); got != 1 {
			t.Errorf("requests = %d, want 1 (4xx is not retried)", got)
		}
	})
}

// TestHTTPSinkErrorsOmitURL pins that a delivery error never contains the target
// URL — which may carry credentials in its query — so it can't leak to logs.
func TestHTTPSinkErrorsOmitURL(t *testing.T) {
	const secretURL = "http://127.0.0.1:1/ingest?apikey=s3cr3t"
	t.Run("transport error", func(t *testing.T) {
		s, _ := New("bridge", httpTarget(secretURL, "POST"), Options{DefaultRetry: fastRetry})
		err := s.Send(context.Background(), Message{Body: "b"})
		if err == nil || strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "apikey") {
			t.Fatalf("error leaks the URL: %v", err)
		}
	})
	t.Run("5xx status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		s, _ := New("bridge", httpTarget(srv.URL+"?apikey=s3cr3t", "POST"), Options{DefaultRetry: fastRetry})
		err := s.Send(context.Background(), Message{Body: "b"})
		if err == nil || strings.Contains(err.Error(), "s3cr3t") {
			t.Fatalf("status error leaks the URL query: %v", err)
		}
	})
}

func TestRedactURLs(t *testing.T) {
	got := redactURLs("send to discord://id/token failed: 401", "discord://id/token")
	if strings.Contains(got, "token") {
		t.Errorf("redactURLs left the URL: %q", got)
	}
}

func TestNewBuildsSinks(t *testing.T) {
	a, err := New("po", &config.Target{Apprise: &config.AppriseSink{URL: "pover://u@t/"}}, Options{})
	if err != nil || a.Kind() != "apprise" {
		t.Fatalf("apprise sink: %v kind=%q", err, a.Kind())
	}
	h, err := New(" in", httpTarget("https://h", "PUT"), Options{})
	if err != nil || h.Kind() != "http" {
		t.Fatalf("http sink: %v kind=%q", err, h.Kind())
	}
}

func TestSinkIdentity(t *testing.T) {
	a := &appriseSink{name: "po"}
	if a.Name() != "po" || a.Kind() != "apprise" {
		t.Errorf("apprise identity = %s/%s", a.Name(), a.Kind())
	}
	h := &httpSink{name: "hook"}
	if h.Name() != "hook" || h.Kind() != "http" {
		t.Errorf("http identity = %s/%s", h.Name(), h.Kind())
	}
}
