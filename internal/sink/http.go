package sink

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/home-operations/chaski/internal/config"
)

const (
	kindApprise = "apprise"
	kindHTTP    = "http"
)

// httpSink relays to a generic HTTP target.
type httpSink struct {
	name    string
	method  string
	url     string
	headers map[string]string // target's static headers (env-rendered at load)
	client  *http.Client
	retry   RetryPolicy
}

func newHTTPSink(name string, h *config.HTTPSink, opts Options) *httpSink {
	method := strings.ToUpper(h.Method)
	if method == "" {
		method = http.MethodPost
	}
	timeout := h.Timeout.AsDuration()
	if timeout <= 0 {
		timeout = opts.DefaultTimeout
	}
	retry := opts.DefaultRetry
	if h.Retry != nil {
		retry = RetryPolicy{Attempts: h.Retry.Attempts, Backoff: h.Retry.Backoff.AsDuration()}
	}
	return &httpSink{
		name:    name,
		method:  method,
		url:     h.URL,
		headers: h.Headers,
		client:  &http.Client{Timeout: timeout},
		retry:   retry,
	}
}

func (s *httpSink) Name() string { return s.name }
func (s *httpSink) Kind() string { return kindHTTP }

func (s *httpSink) Send(ctx context.Context, msg Message) error {
	return deliver(ctx, s.name, s.Kind(), s.retry, func(ctx context.Context) error {
		return s.do(ctx, msg)
	})
}

func (s *httpSink) do(ctx context.Context, msg Message) error {
	var body io.Reader
	if s.method != http.MethodGet && msg.Body != "" {
		body = strings.NewReader(msg.Body)
	}
	req, err := http.NewRequestWithContext(ctx, s.method, s.url, body)
	if err != nil {
		return Permanent(fmt.Errorf("http: build request: %w", err))
	}
	// Route headers first, then the target's static headers — the target wins on
	// any conflict, so a route header can't override the target's Authorization
	// or Host. Content-Type is the lowest priority of the three.
	if msg.ContentType != "" {
		req.Header.Set("Content-Type", msg.ContentType)
	}
	for k, v := range msg.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("http: %s %s: %w", s.method, s.url, err) // transport/timeout — retryable
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return Permanent(fmt.Errorf("http: %s %s: status %d", s.method, s.url, resp.StatusCode))
	default:
		return fmt.Errorf("http: %s %s: status %d", s.method, s.url, resp.StatusCode) // 5xx — retryable
	}
}
