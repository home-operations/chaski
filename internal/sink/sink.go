// Package sink delivers a rendered relay to a configured target — an apprise
// notification (through the swappable Notifier seam) or a generic HTTP
// request. Each sink applies a bounded retry policy (transport/timeout/5xx,
// exponential backoff with full jitter, capped by the request deadline; never a
// 4xx). Fan-out across a route's target list is the caller's job.
package sink

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/home-operations/chaski/internal/config"
)

// Message is the rendered content relayed to a target. A given sink uses the
// fields relevant to its kind: apprise uses Title/Body/Params; http uses
// Body/Headers/ContentType.
type Message struct {
	Title       string
	Body        string
	Params      map[string]string // apprise provider params (rendered)
	Headers     map[string]string // http extra headers (rendered route headers)
	ContentType string            // http body content-type
}

// Sink delivers a Message to one target, with retry.
type Sink interface {
	Send(ctx context.Context, msg Message) error
	Name() string
	Kind() string
}

// Notifier is the notification backend seam: apprise-go today, a
// Go-native or sidecar backend tomorrow, as a one-file swap. The route's params
// are merged into the target URL's query by the implementation.
type Notifier interface {
	Send(ctx context.Context, targetURL, body, title string, params map[string]string) error
}

// RetryPolicy bounds a target's send attempts.
type RetryPolicy struct {
	Attempts int           // total tries (>= 1)
	Backoff  time.Duration // base for exponential backoff + jitter
}

// Options carry the cross-cutting defaults and the injected Notifier.
type Options struct {
	Notifier       Notifier // apprise backend; DefaultNotifier() when nil
	DefaultRetry   RetryPolicy
	DefaultTimeout time.Duration // http per-target timeout when the target sets none
}

const defaultHTTPTimeout = 10 * time.Second

// New builds the sink for a target. The target is assumed valid (config.Validate
// ran), i.e. exactly one of apprise/http is set.
func New(name string, t *config.Target, opts Options) (Sink, error) {
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = defaultHTTPTimeout
	}
	switch t.Kind() {
	case kindApprise:
		n := opts.Notifier
		if n == nil {
			n = DefaultNotifier()
		}
		return &appriseSink{name: name, url: t.Apprise.URL, notifier: n, retry: opts.DefaultRetry}, nil
	case kindHTTP:
		return newHTTPSink(name, t.HTTP, opts), nil
	default:
		return nil, fmt.Errorf("sink: target %q has no apprise or http sink", name)
	}
}

// permanent marks an error that must not be retried (e.g. a 4xx response).
type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent wraps err so withRetry stops immediately instead of retrying.
func Permanent(err error) error { return permanent{err: err} }

// withRetry runs op until it succeeds, returns a permanent error, exhausts the
// attempts, or the context is cancelled. Backoff is exponential with full
// jitter, and each wait is bounded by the context deadline.
func withRetry(ctx context.Context, p RetryPolicy, op func(context.Context) error) error {
	attempts := max(p.Attempts, 1)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op(ctx)
		if err == nil {
			return nil
		}
		if _, ok := errors.AsType[permanent](err); ok {
			return err
		}
		lastErr = err
		if attempt == attempts {
			break
		}
		if !sleepCtx(ctx, backoffDelay(p.Backoff, attempt)) {
			return ctx.Err()
		}
	}
	return lastErr
}

const maxBackoff = 30 * time.Second

// backoffDelay returns a full-jitter delay: a random duration in [0, d], where d
// is base*2^(attempt-1) capped at maxBackoff.
func backoffDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	d := base
	if shift := attempt - 1; shift > 0 {
		if shift > 30 { // guard the shift from overflowing
			d = maxBackoff
		} else {
			d = base << shift
		}
	}
	if d <= 0 || d > maxBackoff {
		d = maxBackoff
	}
	return time.Duration(rand.Int64N(int64(d) + 1))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
