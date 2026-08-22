package llm

import (
	"context"
	"sync"
)

// FakeLLM is a Provider that answers from a queue instead of a network.
//
// It lives in the package rather than in a _test.go file because the packages
// above this one build their agents on it — the integration suites hand one to
// NewClient exactly the way the app hands it a real vendor, which is what makes
// a test of the loop a test of the loop and not of a stub in the middle.
//
// It implements Provider and nothing more. A double that mirrors the contract
// it stands in for cannot drift from it; one that grows its own convenience
// methods starts testing itself.
//
//	fake := &llm.FakeLLM{Responses: []llm.CompletionResponse{
//	    {Content: "hello", StopReason: "end_turn"},
//	}}
//	client := llm.NewClient(fake, "fake-model", 8192)
type FakeLLM struct {
	// Responses is the queue to answer from, consumed in order. Each call pops
	// the first entry; an exhausted queue answers "no more responses" rather
	// than blocking, so a test that under-primes it fails on the assertion
	// rather than on a timeout.
	Responses []CompletionResponse

	// ProviderName is what Name reports. Defaults to "fake".
	ProviderName string

	// Calls records every request received, in order, for tests that assert on
	// what was actually sent.
	Calls []CompletionOptions

	// ErrorAt injects ErrorValue on the Nth call (1-based); 0 disables it.
	// This is how a test drives the failure branch of a turn.
	ErrorAt    int
	ErrorValue error

	mu        sync.Mutex
	callCount int
}

// Stream answers the next queued response as a single done chunk.
func (f *FakeLLM) Stream(_ context.Context, opts CompletionOptions) <-chan StreamChunk {
	f.mu.Lock()
	f.Calls = append(f.Calls, opts)

	var chunk StreamChunk
	if f.injectError() {
		chunk = StreamChunk{Type: ChunkTypeError, Error: f.ErrorValue}
	} else {
		resp := f.next()
		chunk = StreamChunk{Type: ChunkTypeDone, Response: &resp}
	}
	f.mu.Unlock()

	ch := make(chan StreamChunk, 1)
	ch <- chunk
	close(ch)
	return ch
}

// ListModels reports nothing: a queue serves whatever model it is asked for.
func (f *FakeLLM) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }

// Name returns the provider name.
func (f *FakeLLM) Name() string {
	if f.ProviderName != "" {
		return f.ProviderName
	}
	return "fake"
}

// injectError reports whether this call is the one to fail. Callers hold f.mu.
func (f *FakeLLM) injectError() bool {
	f.callCount++
	return f.ErrorAt > 0 && f.callCount == f.ErrorAt
}

// next pops the head of the queue. Callers hold f.mu.
func (f *FakeLLM) next() CompletionResponse {
	if len(f.Responses) == 0 {
		return CompletionResponse{Content: "no more responses", StopReason: "end_turn"}
	}
	resp := f.Responses[0]
	f.Responses = f.Responses[1:]
	return resp
}

var _ Provider = (*FakeLLM)(nil)
