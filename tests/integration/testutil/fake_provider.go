package testutil

import (
	"iter"

	"github.com/genai-io/san/internal/core"

	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"sync"

	"github.com/genai-io/san/internal/llm"
)

// FakeProvider is an llm.Provider that answers from a queue instead of a
// network. The suites hand one to llm.NewClient exactly the way the app hands
// it a real vendor, which is what makes a test of the loop a test of the loop
// and not of a stub in the middle.
//
// It implements llm.Provider and nothing more. A double that mirrors the
// contract it stands in for cannot drift from it; one that grows its own
// convenience methods starts testing itself.
//
//	fake := &testutil.FakeProvider{Responses: []llm.CompletionResponse{
//	    {Content: "hello", StopReason: "end_turn"},
//	}}
//	client := llm.NewClient(fake, "fake-model", 8192)
type FakeProvider struct {
	// Responses is the queue to answer from, consumed in order. Each call pops
	// the first entry; an exhausted queue answers "no more responses" rather
	// than blocking, so a test that under-primes it fails on its assertion
	// rather than on a timeout.
	Responses []llm.CompletionResponse

	// ProviderName is what Name reports. Defaults to "fake".
	ProviderName string

	// Calls records every request received, in order, for tests that assert on
	// what was actually sent.
	Calls []llm.CompletionOptions

	// ErrorAt injects ErrorValue on the Nth call (1-based); 0 disables it.
	// This is how a test drives the failure branch of a turn.
	ErrorAt    int
	ErrorValue error

	mu        sync.Mutex
	callCount int
}

// Stream answers the next queued response as a single done chunk.
func (f *FakeProvider) Client(string, map[string]string) (*ai.Client, error) {
	return ai.NewClientWithDriver(f, ai.Model{ID: "stub", API: "stub"}), nil
}

// Stream is the ai.Driver method: this double fakes the protocol now, which is
// the seam pkg/ai already has and the one five real drivers sit behind.
func (f *FakeProvider) Stream(_ context.Context, req *ai.Request) iter.Seq2[ai.Delta, error] {
	f.mu.Lock()
	msgs := make([]core.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, core.Message{Role: m.Role, Content: m.Content})
	}
	f.Calls = append(f.Calls, llm.CompletionOptions{SystemPrompt: req.System, Messages: msgs})
	fail := f.injectError()
	errValue := f.ErrorValue
	// An injected failure leaves the queue where it was: the response it would
	// have answered with is still owed to the next call.
	var resp llm.CompletionResponse
	if !fail {
		resp = f.next()
	}
	f.mu.Unlock()

	return func(yield func(ai.Delta, error) bool) {
		if fail {
			yield(ai.Delta{}, errValue)
			return
		}
		for _, d := range FakeDeltas(resp) {
			if !yield(d, nil) {
				return
			}
		}
	}
}

// ListModels reports nothing: a queue serves whatever model it is asked for.
func (f *FakeProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

// Name returns the provider name.
func (f *FakeProvider) Name() string {
	if f.ProviderName != "" {
		return f.ProviderName
	}
	return "fake"
}

// injectError reports whether this call is the one to fail. Callers hold f.mu.
func (f *FakeProvider) injectError() bool {
	f.callCount++
	return f.ErrorAt > 0 && f.callCount == f.ErrorAt
}

// next pops the head of the queue. Callers hold f.mu.
func (f *FakeProvider) next() llm.CompletionResponse {
	if len(f.Responses) == 0 {
		return llm.CompletionResponse{Content: ai.TextContent("no more responses"), StopReason: ai.StopEndTurn}
	}
	resp := f.Responses[0]
	f.Responses = f.Responses[1:]
	return resp
}

var _ llm.Provider = (*FakeProvider)(nil)
