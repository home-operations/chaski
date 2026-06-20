package sink

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithRetryStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the first attempt

	calls := 0
	err := withRetry(ctx, fastRetry, func(context.Context) error {
		calls++
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Errorf("op called %d times, want 0 (ctx cancelled up front)", calls)
	}
}

func TestBackoffDelayBounds(t *testing.T) {
	if d := backoffDelay(0, 5); d != 0 {
		t.Errorf("backoffDelay(0, _) = %v, want 0 (no base)", d)
	}

	base := 100 * time.Millisecond
	// Full jitter keeps every sample within [0, maxBackoff]; a large attempt must
	// not overflow the shift — the shift>30 guard caps it at maxBackoff.
	for _, attempt := range []int{1, 5, 31, 64, 1000} {
		for range 300 {
			d := backoffDelay(base, attempt)
			if d < 0 || d > maxBackoff {
				t.Fatalf("backoffDelay(%v, %d) = %v, outside [0, %v]", base, attempt, d, maxBackoff)
			}
		}
	}

	// Attempt 1 has no shift, so it is bounded by the base.
	for range 300 {
		if d := backoffDelay(base, 1); d > base {
			t.Fatalf("backoffDelay(base, 1) = %v, want <= %v", d, base)
		}
	}
}
