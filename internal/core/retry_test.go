package core

import (
	"iter"

	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"errors"
	"testing"
	"time"
)

func TestBackoffDelayFloorWins(t *testing.T) {
	// frac=0 zeroes the exponential term, so the Retry-After floor stands.
	if got := backoffDelay(1, 5*time.Second, 0); got != 5*time.Second {
		t.Fatalf("backoffDelay floor = %v, want 5s", got)
	}
}

func TestBackoffDelayGrowsAndCaps(t *testing.T) {
	// frac=1 (upper edge of full jitter): attempt n ≈ base·2^(n-1), capped.
	a1 := backoffDelay(1, 0, 1)
	a2 := backoffDelay(2, 0, 1)
	if a1 != retryBaseDelay {
		t.Fatalf("attempt1 = %v, want %v", a1, retryBaseDelay)
	}
	if a2 != 2*retryBaseDelay {
		t.Fatalf("attempt2 = %v, want %v", a2, 2*retryBaseDelay)
	}
	if capped := backoffDelay(20, 0, 1); capped != retryMaxDelay {
		t.Fatalf("attempt20 = %v, want cap %v", capped, retryMaxDelay)
	}
}

func TestBackoffSleepHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Use a floor large enough that, without the cancel check, the call would
	// block well past the test. A canceled ctx must return immediately.
	if err := BackoffSleep(ctx, 1, 10*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("BackoffSleep on canceled ctx = %v, want context.Canceled", err)
	}
}

func TestStreamSentinelsAreRetryable(t *testing.T) {
	for _, e := range []error{errStreamStalled, errStreamTruncated} {
		var re RetryableError
		if !errors.As(e, &re) {
			t.Fatalf("%v should satisfy RetryableError", e)
		}
		if re.RetryAfter() != 0 {
			t.Fatalf("%v RetryAfter = %v, want 0", e, re.RetryAfter())
		}
	}
}

// scriptedLLM fails the first `failures` Infer calls with failErr, then
// completes with a text-only end_turn response.
type scriptedLLM struct {
	failErr  error
	failures int
	calls    int
}

func (s *scriptedLLM) Name() string { return "scripted" }

func (s *scriptedLLM) Stream(context.Context, *ai.Request) iter.Seq2[ai.Delta, error] {
	s.calls++
	fail := s.calls <= s.failures
	return func(yield func(ai.Delta, error) bool) {
		if fail {
			yield(ai.Delta{}, s.failErr)
			return
		}
		for _, d := range deltas(InferResponse{Content: "ok", StopReason: StopEndTurn}) {
			if !yield(d, nil) {
				return
			}
		}
	}
}

// hangLLM never produces a chunk; it unblocks only when the per-inference ctx
// is canceled (by the idle-timeout watchdog).
type hangLLM struct{ calls int }

func (h *hangLLM) Name() string { return "hang" }

func (h *hangLLM) Stream(ctx context.Context, _ *ai.Request) iter.Seq2[ai.Delta, error] {
	h.calls++
	return func(yield func(ai.Delta, error) bool) {
		<-ctx.Done()
		yield(ai.Delta{}, ctx.Err())
	}
}

func newRetryAgent(t *testing.T, d ai.Driver, maxRetries int, timeout time.Duration) *agent {
	t.Helper()
	ag := NewAgent(Config{
		ID:                      "test",
		Client:                  testClient(d),
		System:                  NewSystem(),
		Tools:                   NewTools(),
		MaxTurnRetries:          maxRetries,
		StreamFirstChunkTimeout: timeout,
		StreamIdleTimeout:       timeout,
	})
	go func() {
		for range ag.Outbox() {
		}
	}()
	a := ag.(*agent)
	a.SetMessages([]Message{UserMessage("hi", nil)})
	return a
}

func TestThinkActRetriesTransientStreamError(t *testing.T) {
	llm := &scriptedLLM{failErr: errStreamStalled, failures: 2}
	a := newRetryAgent(t, llm, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := a.ThinkAct(ctx)
	if err != nil {
		t.Fatalf("ThinkAct returned error after retries: %v", err)
	}
	if result.Content != "ok" {
		t.Fatalf("content = %q, want ok", result.Content)
	}
	if llm.calls != 3 {
		t.Fatalf("Infer calls = %d, want 3 (2 failures + 1 success)", llm.calls)
	}
}

func TestThinkActSurfacesErrorAfterMaxRetries(t *testing.T) {
	llm := &scriptedLLM{failErr: errStreamStalled, failures: 99}
	a := newRetryAgent(t, llm, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := a.ThinkAct(ctx); err == nil {
		t.Fatal("ThinkAct should surface the error after exhausting retries")
	}
	if llm.calls != 3 { // 1 initial + 2 retries
		t.Fatalf("Infer calls = %d, want 3", llm.calls)
	}
}

func TestThinkActDoesNotRetryFatalError(t *testing.T) {
	llm := &scriptedLLM{failErr: errors.New("400 bad request"), failures: 99}
	a := newRetryAgent(t, llm, 3, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := a.ThinkAct(ctx); err == nil {
		t.Fatal("ThinkAct should surface a fatal error")
	}
	if llm.calls != 1 {
		t.Fatalf("Infer calls = %d, want 1 (no retry on fatal)", llm.calls)
	}
}

func TestStreamInferIdleTimeoutRetries(t *testing.T) {
	llm := &hangLLM{}
	a := newRetryAgent(t, llm, 1, 40*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := a.ThinkAct(ctx); err == nil {
		t.Fatal("ThinkAct should fail once a stalled stream exhausts retries")
	}
	if llm.calls != 2 { // 1 initial + 1 retry, each stalls
		t.Fatalf("Infer calls = %d, want 2 (idle-timeout retry)", llm.calls)
	}
}

// TestThinkActReturnsResultWhenRetriesAreExhausted pins the Result contract on
// the failure path. A turn that dies on a provider outage has still appended
// messages and burned tokens, and the caller persists a run from its Result —
// so returning only an error silently discards the work. subagent's
// finalizeResult is the concrete casualty: no Result means the transcript is
// never written to disk.
func TestThinkActReturnsResultWhenRetriesAreExhausted(t *testing.T) {
	llm := &scriptedLLM{failErr: errStreamStalled, failures: 99}
	a := newRetryAgent(t, llm, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := a.ThinkAct(ctx)
	if err == nil {
		t.Fatal("ThinkAct should surface the error after exhausting retries")
	}
	if result == nil {
		t.Fatal("ThinkAct returned nil Result on a failed turn; the caller cannot persist the run")
	}
	if result.StopReason != StopError {
		t.Errorf("StopReason = %q, want %q", result.StopReason, StopError)
	}
	if result.StopDetail == "" {
		t.Error("StopDetail is empty; it should carry the underlying failure")
	}
	if len(result.Messages) == 0 {
		t.Error("Messages is empty; the conversation is what the caller persists")
	}
}
