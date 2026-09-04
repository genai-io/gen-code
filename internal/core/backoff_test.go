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
	if a1 != backoffBase {
		t.Fatalf("attempt1 = %v, want %v", a1, backoffBase)
	}
	if a2 != 2*backoffBase {
		t.Fatalf("attempt2 = %v, want %v", a2, 2*backoffBase)
	}
	if capped := backoffDelay(20, 0, 1); capped != backoffMax {
		t.Fatalf("attempt20 = %v, want cap %v", capped, backoffMax)
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

// stalled is what a driver reports when a stream goes quiet: the loop replays
// what ai.IsRetryable admits, and a network failure is one. San's own
// RetryableError is still the llm layer's — its one-shot Complete has a retry
// of its own — but the agent loop reads the SDK's classification now.
var stalled = &ai.Error{Kind: ai.KindNetwork, Message: "stream stalled"}

func TestThinkActRetriesTransientStreamError(t *testing.T) {
	llm := &scriptedLLM{failErr: stalled, failures: 2}
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
	llm := &scriptedLLM{failErr: stalled, failures: 99}
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
	// A 400 as the driver hands it over — typed, so ai.IsRetryable leaves it
	// fatal.
	llm := &scriptedLLM{failErr: &ai.Error{Kind: ai.KindInvalidRequest, Status: 400,
		Message: "bad request"}, failures: 99}
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

// The loop's retry budget is spent on the failures the SDK typed as transient.
// Nothing between the driver and the loop tags them, so streamInfer classifies
// as the failure leaves the stream — without that, a provider's 429 read as
// fatal and the turn died on a blip it was built to ride out.
func TestThinkActRetriesAProviderRateLimit(t *testing.T) {
	llm := &scriptedLLM{failErr: &ai.Error{Kind: ai.KindRateLimit, Status: 429,
		Message: "slow down"}, failures: 2}
	a := newRetryAgent(t, llm, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := a.ThinkAct(ctx)
	if err != nil {
		t.Fatalf("ThinkAct returned error after retries: %v", err)
	}
	if result.Content != "ok" {
		t.Fatalf("content = %q, want ok", result.Content)
	}
	if llm.calls != 3 {
		t.Fatalf("Infer calls = %d, want 3 (2 rate limits + 1 success)", llm.calls)
	}
}

// A terminal failure the SDK could not type is a transport failure, which is
// the one place the stream rule differs from the completed-call rule.
func TestThinkActRetriesAnOpaqueStreamFailure(t *testing.T) {
	llm := &scriptedLLM{failErr: &ai.Error{Kind: ai.KindNetwork, Message: "unexpected EOF"}, failures: 1}
	a := newRetryAgent(t, llm, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := a.ThinkAct(ctx); err != nil {
		t.Fatalf("ThinkAct returned error after retries: %v", err)
	}
	if llm.calls != 2 {
		t.Fatalf("Infer calls = %d, want 2 (1 failure + 1 success)", llm.calls)
	}
}

// The reactive half: an overflow the provider reported has to reach
// isPromptTooLong, or the loop retries a prompt that cannot fit instead of
// shrinking it. Deliberately not also retryable — one compaction, then the
// call goes out again.
func TestThinkActCompactsOnProviderContextOverflow(t *testing.T) {
	llm := &scriptedLLM{failErr: &ai.Error{Kind: ai.KindContextExceeded, Status: 400,
		Message: "prompt is too long"}, failures: 1}
	compacted := 0
	ag := NewAgent(Config{
		ID:     "test",
		Client: testClient(llm),
		System: NewSystem(),
		Tools:  NewTools(),
		CompactFunc: func(context.Context, []Message) (string, error) {
			compacted++
			return "the story so far", nil
		},
	}).(*agent)
	go func() {
		for range ag.Outbox() {
		}
	}()
	ag.SetMessages([]Message{
		UserMessage("one", nil), AssistantMessage("two", "", nil), UserMessage("three", nil),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := ag.ThinkAct(ctx); err != nil {
		t.Fatalf("ThinkAct returned error: %v", err)
	}
	if compacted != 1 {
		t.Fatalf("compactions = %d, want 1", compacted)
	}
	if llm.calls != 2 {
		t.Fatalf("Infer calls = %d, want 2 (overflow, compact, retry)", llm.calls)
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
	llm := &scriptedLLM{failErr: stalled, failures: 99}
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
