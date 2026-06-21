package sink

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestDeliverRecordsOutcomeAndRetries(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		s := &appriseSink{name: "ok", url: "pover://u@t/", notifier: &fakeNotifier{}, retry: fastRetry}
		before := testutil.ToFloat64(targetSends.WithLabelValues("ok", "apprise", "success"))
		if err := s.Send(ctx, Message{Body: "b"}); err != nil {
			t.Fatalf("send: %v", err)
		}
		if got := testutil.ToFloat64(targetSends.WithLabelValues("ok", "apprise", "success")) - before; got != 1 {
			t.Errorf("success delta = %v, want 1", got)
		}
	})

	t.Run("retryable then success counts the retries", func(t *testing.T) {
		s := &appriseSink{name: "retry", url: "pover://u@t/", notifier: &fakeNotifier{failTimes: 2}, retry: fastRetry}
		rb := testutil.ToFloat64(targetRetries.WithLabelValues("retry", "apprise"))
		sb := testutil.ToFloat64(targetSends.WithLabelValues("retry", "apprise", "success"))
		if err := s.Send(ctx, Message{Body: "b"}); err != nil {
			t.Fatalf("send: %v", err)
		}
		if got := testutil.ToFloat64(targetRetries.WithLabelValues("retry", "apprise")) - rb; got != 2 {
			t.Errorf("retries delta = %v, want 2", got)
		}
		if got := testutil.ToFloat64(targetSends.WithLabelValues("retry", "apprise", "success")) - sb; got != 1 {
			t.Errorf("success delta = %v, want 1", got)
		}
	})

	t.Run("permanent is recorded and never retried", func(t *testing.T) {
		s := &appriseSink{name: "perm", url: "pover://u@t/", notifier: &fakeNotifier{permanent: true}, retry: fastRetry}
		pb := testutil.ToFloat64(targetSends.WithLabelValues("perm", "apprise", "permanent"))
		rb := testutil.ToFloat64(targetRetries.WithLabelValues("perm", "apprise"))
		if err := s.Send(ctx, Message{Body: "b"}); err == nil {
			t.Fatal("want a permanent error")
		}
		if got := testutil.ToFloat64(targetSends.WithLabelValues("perm", "apprise", "permanent")) - pb; got != 1 {
			t.Errorf("permanent delta = %v, want 1", got)
		}
		if got := testutil.ToFloat64(targetRetries.WithLabelValues("perm", "apprise")) - rb; got != 0 {
			t.Errorf("retries delta = %v, want 0 (permanent is never retried)", got)
		}
	})
}
