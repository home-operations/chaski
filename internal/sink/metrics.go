package sink

import (
	"context"
	"errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Per-target delivery metrics. The route-level chaski_relays_total can only say
// a route's fan-out failed; these attribute the outcome to a specific target.
var (
	targetSends = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chaski_target_sends_total",
		Help: "Per-target send outcomes: success, permanent (4xx, not retried), or retryable (exhausted retries on a 5xx/transport error).",
	}, []string{"target", "kind", "outcome"})

	targetRetries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chaski_target_retries_total",
		Help: "Per-target retried send attempts (every attempt beyond the first).",
	}, []string{"target", "kind"})
)

// deliver runs a target's send with retry and records the per-target outcome and
// retry count. It wraps op to count the attempts that actually ran, so withRetry
// stays metric-free (and its tests unchanged).
func deliver(ctx context.Context, name, kind string, p RetryPolicy, op func(context.Context) error) error {
	attempts := 0
	err := withRetry(ctx, p, func(ctx context.Context) error {
		attempts++
		return op(ctx)
	})
	if attempts > 1 {
		targetRetries.WithLabelValues(name, kind).Add(float64(attempts - 1))
	}
	targetSends.WithLabelValues(name, kind, outcome(err)).Inc()
	return err
}

func outcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case isPermanent(err):
		return "permanent"
	default:
		return "retryable"
	}
}

func isPermanent(err error) bool {
	_, ok := errors.AsType[permanent](err)
	return ok
}
