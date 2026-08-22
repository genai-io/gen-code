package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/genai-io/san/internal/core"
)

// --- mock provider for LLM tests ---

type mockLLMProvider struct {
	responses []CompletionResponse
	callIdx   int
	models    []ModelInfo
	listErr   error
	lastOpts  CompletionOptions
	listCalls int
}

func (m *mockLLMProvider) Stream(_ context.Context, opts CompletionOptions) <-chan StreamChunk {
	m.lastOpts = opts
	ch := make(chan StreamChunk, 1)
	go func() {
		defer close(ch)
		if m.callIdx >= len(m.responses) {
			ch <- StreamChunk{Type: ChunkTypeDone, Response: &CompletionResponse{
				Content:    "no more responses",
				StopReason: "end_turn",
			}}
			return
		}
		resp := m.responses[m.callIdx]
		m.callIdx++
		ch <- StreamChunk{Type: ChunkTypeDone, Response: &resp}
	}()
	return ch
}

func (m *mockLLMProvider) ListModels(_ context.Context) ([]ModelInfo, error) {
	m.listCalls++
	return m.models, m.listErr
}

func (m *mockLLMProvider) Name() string { return "mock" }

type mockLimitFetcherProvider struct {
	mockLLMProvider
	inputLimit  int
	outputLimit int
	fetchErr    error
}

func (m *mockLimitFetcherProvider) FetchModelLimits(_ context.Context, _ string) (int, int, error) {
	return m.inputLimit, m.outputLimit, m.fetchErr
}

// --- LLM tests ---

func TestCompleteCollectsTheStream(t *testing.T) {
	mp := &mockLLMProvider{
		responses: []CompletionResponse{
			{Content: "hello", StopReason: "end_turn", Usage: Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
	l := &Client{provider: mp, model: "test-model", maxTokens: 4096}

	msgs := []core.Message{{Role: core.RoleUser, Content: "hi"}}
	resp, err := Complete(context.Background(), mp, l.completionOpts(msgs, nil, "system prompt"))
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("expected 'hello', got '%s'", resp.Content)
	}
}

func TestLLMStream(t *testing.T) {
	mp := &mockLLMProvider{
		responses: []CompletionResponse{
			{Content: "streamed", StopReason: "end_turn"},
		},
	}
	l := &Client{provider: mp, model: "test-model"}

	msgs := []core.Message{{Role: core.RoleUser, Content: "hi"}}
	ch := l.Stream(context.Background(), msgs, nil, "")

	var resp *CompletionResponse
	for chunk := range ch {
		if chunk.Type == ChunkTypeDone {
			resp = chunk.Response
		}
	}
	if resp == nil {
		t.Fatal("expected response from stream")
	}
	if resp.Content != "streamed" {
		t.Errorf("expected 'streamed', got '%s'", resp.Content)
	}
}

func TestLLMComplete(t *testing.T) {
	mp := &mockLLMProvider{
		responses: []CompletionResponse{
			{Content: "summary", StopReason: "end_turn"},
		},
	}
	l := &Client{provider: mp, model: "test-model"}

	resp, err := l.Complete(context.Background(), "compact", []core.Message{{Role: core.RoleUser, Content: "summarize"}}, 2048)
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Content != "summary" {
		t.Errorf("expected 'summary', got '%s'", resp.Content)
	}
}

func TestLLMNameAndModelID(t *testing.T) {
	l := &Client{provider: &mockLLMProvider{}, model: "claude-3"}
	if l.Name() != "mock" {
		t.Errorf("expected 'mock', got '%s'", l.Name())
	}
	if l.ModelID() != "claude-3" {
		t.Errorf("expected 'claude-3', got '%s'", l.ModelID())
	}
}

func TestResolveMaxTokens_CustomOverride(t *testing.T) {
	l := &Client{provider: &mockLLMProvider{}, model: "m", maxTokens: 16384}
	got := l.ResolveMaxTokens(context.Background())
	if got != 16384 {
		t.Errorf("expected 16384, got %d", got)
	}
}

func TestResolveMaxTokens_FromProvider(t *testing.T) {
	mp := &mockLLMProvider{
		models: []ModelInfo{
			{ID: "claude-opus", OutputTokenLimit: 32000},
			{ID: "claude-sonnet", OutputTokenLimit: 64000},
		},
	}
	l := &Client{provider: mp, model: "claude-sonnet"} // maxTokens = 0

	got := l.ResolveMaxTokens(context.Background())
	if got != 64000 {
		t.Errorf("expected 64000, got %d", got)
	}
}

func TestResolveMaxTokens_Fallback(t *testing.T) {
	mp := &mockLLMProvider{
		models: []ModelInfo{
			{ID: "other-model", OutputTokenLimit: 32000},
		},
	}
	l := &Client{provider: mp, model: "unknown-model"} // no match

	got := l.ResolveMaxTokens(context.Background())
	if got != defaultMaxTokens {
		t.Errorf("expected default %d, got %d", defaultMaxTokens, got)
	}
}

// TestModelLimitsMemoized guards the hot-path fix: the input and output limits
// resolve from ListModels (a live network call for OpenAI-family providers), so
// repeated queries — one per inference step — must reuse a cached result rather
// than call the provider each time.
func TestModelLimitsMemoized(t *testing.T) {
	mp := &mockLLMProvider{
		models: []ModelInfo{{ID: "m", InputTokenLimit: 200000, OutputTokenLimit: 8000}},
	}
	l := &Client{provider: mp, model: "m"}

	for i := range 5 {
		if got := l.InputLimit(); got != 200000 {
			t.Fatalf("InputLimit call %d = %d, want 200000", i, got)
		}
		if got := l.ResolveMaxTokens(context.Background()); got != 8000 {
			t.Fatalf("ResolveMaxTokens call %d = %d, want 8000", i, got)
		}
	}
	// One ListModels for the input limit + one for the output limit = 2 total,
	// regardless of how many times the limits are queried.
	if mp.listCalls != 2 {
		t.Errorf("ListModels called %d times across 5 rounds, want 2 (memoized)", mp.listCalls)
	}
}

// TestModelLimitsRetryAfterFailure ensures a transient resolution failure is not
// cached as 0: the next query retries, and only a successful lookup is memoized.
func TestModelLimitsRetryAfterFailure(t *testing.T) {
	t.Setenv(InputLimitEnvVar, "")
	mp := &mockLLMProvider{listErr: errors.New("network down")}
	l := &Client{provider: mp, model: "m"}

	if got := l.InputLimit(); got != 0 {
		t.Fatalf("InputLimit during outage = %d, want 0", got)
	}
	if got := l.InputLimit(); got != 0 {
		t.Fatalf("InputLimit during outage (2nd) = %d, want 0", got)
	}
	if mp.listCalls != 2 {
		t.Errorf("ListModels called %d times during outage, want 2 (failures retry)", mp.listCalls)
	}

	// Provider recovers: the success is now cached and later calls stop hitting it.
	mp.listErr = nil
	mp.models = []ModelInfo{{ID: "m", InputTokenLimit: 200000}}
	if got := l.InputLimit(); got != 200000 {
		t.Fatalf("InputLimit after recovery = %d, want 200000", got)
	}
	afterRecovery := mp.listCalls
	if got := l.InputLimit(); got != 200000 {
		t.Fatalf("InputLimit cached = %d, want 200000", got)
	}
	if mp.listCalls != afterRecovery {
		t.Errorf("ListModels called again after success (%d→%d), want cached", afterRecovery, mp.listCalls)
	}
}

// The env override outranks the provider's own figure, for a provider that
// under-reports its window.
func TestInputLimitEnvOverrideBeatsProvider(t *testing.T) {
	t.Setenv(InputLimitEnvVar, "272000")
	mp := &mockLLMProvider{models: []ModelInfo{{ID: "m", InputTokenLimit: 200000}}}
	l := &Client{provider: mp, model: "m"}

	if got := l.InputLimit(); got != 272000 {
		t.Fatalf("InputLimit() = %d, want the 272000 override", got)
	}
}

// An undiscoverable window resolves to 0 rather than a guess: proactive
// compaction then stays out of the way and the prompt-too-long retry recovers.
// Acting on an invented number would silently compact a conversation that had
// room, or never fire on one that did not.
func TestInputLimitUnknownStaysZero(t *testing.T) {
	t.Setenv(InputLimitEnvVar, "")
	l := &Client{provider: &mockLLMProvider{models: []ModelInfo{{ID: "m"}}}, model: "m"}

	if got := l.InputLimit(); got != 0 {
		t.Fatalf("InputLimit() = %d, want 0 for an unknown window", got)
	}
}

func TestResolveMaxTokens_FromModelLimitsFetcher(t *testing.T) {
	mp := &mockLimitFetcherProvider{
		mockLLMProvider: mockLLMProvider{
			models: []ModelInfo{{ID: "m"}},
		},
		outputLimit: 128000,
	}
	l := &Client{provider: mp, model: "m"}

	got := l.ResolveMaxTokens(context.Background())
	if got != 128000 {
		t.Errorf("expected 128000, got %d", got)
	}
}

func TestInputLimitFromModelLimitsFetcher(t *testing.T) {
	mp := &mockLimitFetcherProvider{
		mockLLMProvider: mockLLMProvider{
			models: []ModelInfo{{ID: "m"}},
		},
		inputLimit: 400000,
	}

	got := inputLimitFromProvider(mp, "m")
	if got != 400000 {
		t.Errorf("expected 400000, got %d", got)
	}
}

func TestCompletionOptsDefaultMaxTokens(t *testing.T) {
	l := &Client{provider: &mockLLMProvider{}, model: "m"}
	opts := l.completionOpts(nil, nil, "")
	if opts.MaxTokens != defaultMaxTokens {
		t.Errorf("expected default %d, got %d", defaultMaxTokens, opts.MaxTokens)
	}
}

func TestCompletionOptsIncludesThinkingEffort(t *testing.T) {
	l := &Client{
		provider:       &mockLLMProvider{},
		model:          "m",
		thinkingEffort: "high",
	}
	opts := l.completionOpts(nil, nil, "system")
	if opts.ThinkingEffort != "high" {
		t.Fatalf("expected thinking effort high, got %q", opts.ThinkingEffort)
	}
	if opts.SystemPrompt != "system" {
		t.Fatalf("expected system prompt to be preserved, got %q", opts.SystemPrompt)
	}
}

func TestOutputLimitFromProviderNil(t *testing.T) {
	got := outputLimitFromProvider(nil, "m")
	if got != 0 {
		t.Fatalf("expected nil provider to return 0, got %d", got)
	}
}

func TestOutputLimitFromProviderListModelsError(t *testing.T) {
	got := outputLimitFromProvider(&mockLLMProvider{listErr: errors.New("boom")}, "m")
	if got != 0 {
		t.Fatalf("expected ListModels error to return 0, got %d", got)
	}
}

func TestAddUsageIncludesCacheTokens(t *testing.T) {
	l := &Client{}
	l.AddUsage(Usage{
		InputTokens:              10,
		OutputTokens:             5,
		CacheCreationInputTokens: 7,
		CacheReadInputTokens:     3,
	})

	got := l.Tokens()
	if got.InputTokens != 10 || got.OutputTokens != 5 {
		t.Fatalf("unexpected base usage: %+v", got)
	}
	if got.CacheCreationInputTokens != 7 || got.CacheReadInputTokens != 3 {
		t.Fatalf("unexpected cache usage: %+v", got)
	}
}

// --- streaming failure paths ---

type streamErrorProvider struct {
	err error
}

func (p streamErrorProvider) Stream(context.Context, CompletionOptions) <-chan StreamChunk {
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{Type: ChunkTypeError, Error: p.err}
	close(ch)
	return ch
}

func (streamErrorProvider) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }
func (streamErrorProvider) Name() string                                    { return "stream-error" }

type retryThenSuccessProvider struct {
	calls int
}

func (p *retryThenSuccessProvider) Stream(context.Context, CompletionOptions) <-chan StreamChunk {
	p.calls++
	ch := make(chan StreamChunk, 1)
	if p.calls == 1 {
		ch <- StreamChunk{Type: ChunkTypeError, Error: errors.New("opaque terminal stream error")}
	} else {
		ch <- StreamChunk{Type: ChunkTypeDone, Response: &CompletionResponse{Content: "recovered"}}
	}
	close(ch)
	return ch
}

func (*retryThenSuccessProvider) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }
func (*retryThenSuccessProvider) Name() string                                    { return "retry-stream" }

func TestInferWrapsOpaqueStreamErrorAsRetryable(t *testing.T) {
	original := errors.New("opaque terminal stream error")
	client := NewClient(streamErrorProvider{err: original}, "test-model", 1)

	chunks, err := client.Infer(context.Background(), core.InferRequest{})
	if err != nil {
		t.Fatalf("Infer() error = %v", err)
	}
	chunk, ok := <-chunks
	if !ok {
		t.Fatal("Infer() returned no error chunk")
	}
	var retryable core.RetryableError
	if !errors.As(chunk.Err, &retryable) {
		t.Fatalf("chunk error %v is not retryable", chunk.Err)
	}
	if !errors.Is(chunk.Err, original) {
		t.Fatal("chunk error does not preserve the provider error")
	}
}

func TestCompleteRetriesOpaqueStreamError(t *testing.T) {
	provider := &retryThenSuccessProvider{}
	client := NewClient(provider, "test-model", 1)

	resp, err := client.Complete(context.Background(), "", nil, 1)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "recovered" {
		t.Fatalf("Complete() content = %q, want recovered", resp.Content)
	}
	if provider.calls != 2 {
		t.Fatalf("Stream() calls = %d, want 2", provider.calls)
	}
}
