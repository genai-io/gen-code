package core

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// Exponential backoff with full jitter, for the retry loops that are not the
// agent's — llm's one-shot Complete and autopilot's steering call. The agent's
// own is the SDK's, and shares none of this.
const (
	backoffBase = 500 * time.Millisecond
	backoffMax  = 30 * time.Second
)

// backoffDelay returns the pre-sleep delay for a 1-based attempt: exponential
// (base·2^(n-1)) capped at backoffMax, full-jittered by frac (in [0,1)),
// then floored at `floor` (a server Retry-After hint). Pure, so the policy is
// unit-testable without a clock.
func backoffDelay(attempt int, floor time.Duration, frac float64) time.Duration {
	d := float64(backoffBase) * math.Pow(2, float64(attempt-1))
	if d > float64(backoffMax) {
		d = float64(backoffMax)
	}
	delay := time.Duration(d * frac) // full jitter
	return max(delay, floor)
}

// BackoffSleep waits out the backoff for `attempt`, returning ctx.Err() if the
// caller cancels mid-wait so a retry never blocks past an interrupt. Exported
// so the llm layer's utility-call retry (Complete) shares one policy.
func BackoffSleep(ctx context.Context, attempt int, floor time.Duration) error {
	d := backoffDelay(attempt, floor, rand.Float64())
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
