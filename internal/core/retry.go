package core

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// Retry tuning for transient LLM stream failures. These are deliberately
// small: the goal is to ride out a brief blip (provider overload, a rate
// limit, a dropped connection), not to mask a sustained outage.
const (
	defaultMaxTurnRetries = 2
	// defaultFirstChunkTimeout bounds time-to-first-chunk. It is generous
	// because a reasoning model may think for a while (and emit nothing) before
	// the first token; it exists only to catch a connection that hangs at open.
	defaultFirstChunkTimeout = 5 * time.Minute
	// defaultStreamIdleTimeout bounds the gap *between* chunks once a response
	// has started — a much tighter signal that an in-flight stream has stalled.
	defaultStreamIdleTimeout = 60 * time.Second

	retryBaseDelay = 500 * time.Millisecond
	retryMaxDelay  = 30 * time.Second
)

// RetryableError marks a stream error that the turn loop may retry. Classify
// attaches it (429/5xx/network) as a provider failure leaves the stream, and
// core's own stall/truncation sentinels implement it directly.
//
// RetryAfter is a server-provided floor (e.g. a 429 Retry-After header), or 0
// when there is no hint.
type RetryableError interface {
	error
	RetryAfter() time.Duration
}

// ContextExceededError marks a failure caused by the prompt outgrowing the
// model's context window. Like RetryableError, Classify attaches it, so the
// loop can compact and retry without carrying any provider's error vocabulary
// — the phrasings differ per vendor ("prompt is too long", "maximum context
// length", "input token count exceeds…") and the SDK is what holds the table.
//
// Deliberately distinct from RetryableError: retrying an oversized prompt
// unchanged just fails again. The turn loop has to shrink the conversation
// first, which is what makes this its own signal.
type ContextExceededError interface {
	error
	ContextExceeded()
}

// streamIncomplete is a core-originated retryable failure: the stream either
// ended without a terminal Done chunk (truncated) or went silent past the idle
// deadline (stalled). Neither carries a server hint, so RetryAfter is 0.
type streamIncomplete struct{ reason string }

func (e streamIncomplete) Error() string             { return "stream " + e.reason }
func (e streamIncomplete) RetryAfter() time.Duration { return 0 }

var (
	errStreamStalled   = streamIncomplete{"stalled (no data within idle timeout)"}
	errStreamTruncated = streamIncomplete{"closed before completion"}
)

// backoffDelay returns the pre-sleep delay for a 1-based attempt: exponential
// (base·2^(n-1)) capped at retryMaxDelay, full-jittered by frac (in [0,1)),
// then floored at `floor` (a server Retry-After hint). Pure, so the policy is
// unit-testable without a clock.
func backoffDelay(attempt int, floor time.Duration, frac float64) time.Duration {
	d := float64(retryBaseDelay) * math.Pow(2, float64(attempt-1))
	if d > float64(retryMaxDelay) {
		d = float64(retryMaxDelay)
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
