package llm

import (
	"iter"

	"github.com/genai-io/sdk-go/pkg/ai"

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

func (m *mockLLMProvider) Client(string, map[string]string) (*ai.Client, error) {
	return ai.NewClientWithDriver(m, ai.Model{ID: "mock", API: "mock"}), nil
}

func (m *mockLLMProvider) Stream(_ context.Context, req *ai.Request) iter.Seq2[ai.Delta, error] {
	m.lastOpts = CompletionOptions{SystemPrompt: req.System}
	resp := CompletionResponse{Content: "no more responses", StopReason: "end_turn"}
	if m.callIdx < len(m.responses) {
		resp = m.responses[m.callIdx]
		m.callIdx++
	}
	return func(yield func(ai.Delta, error) bool) {
		if resp.Content != "" {
			yield(ai.Delta{Block: ai.TextBlock(resp.Content)}, nil)
			yield(ai.Delta{EndBlock: true}, nil)
		}
		yield(ai.Delta{StopReason: ai.StopEndTurn, Usage: &ai.Usage{
			Input: resp.Usage.InputTokens, Output: resp.Usage.OutputTokens,
		}}, nil)
	}
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
	got := l.effectiveMaxTokens()
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

	got := l.effectiveMaxTokens()
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

	got := l.effectiveMaxTokens()
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
		if got := l.effectiveMaxTokens(); got != 8000 {
			t.Fatalf("ResolveMaxTokens call %d = %d, want 8000", i, got)
		}
	}
	// One listing states both limits, so one call answers everything —
	// regardless of how many times either is queried.
	if mp.listCalls != 1 {
		t.Errorf("ListModels called %d times across 5 rounds, want 1 (memoized)", mp.listCalls)
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

	got := l.effectiveMaxTokens()
	if got != 128000 {
		t.Errorf("expected 128000, got %d", got)
	}
}

// A listing that states no window sends the resolver to the endpoint that
// answers per model — Model Studio publishes limits nowhere else.
func TestModelLimitsFallBackToTheFetcher(t *testing.T) {
	mp := &mockLimitFetcherProvider{
		mockLLMProvider: mockLLMProvider{models: []ModelInfo{{ID: "m"}}},
		inputLimit:      400000,
		outputLimit:     8192,
	}

	in, out, _ := resolveModelLimits(mp, "m")
	if in != 400000 || out != 8192 {
		t.Errorf("resolveModelLimits() = (%d, %d), want (400000, 8192)", in, out)
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

// Unknown has to stay 0 rather than become a guess: a guessed window is acted
// on silently and is wrong in both directions.
func TestModelLimitsReportUnknownAsZero(t *testing.T) {
	if in, out, answered := resolveModelLimits(nil, "m"); in != 0 || out != 0 || answered {
		t.Errorf("no provider = (%d, %d, %v), want (0, 0, false)", in, out, answered)
	}
	if in, out, answered := resolveModelLimits(&mockLLMProvider{listErr: errors.New("boom")}, "m"); in != 0 || out != 0 || answered {
		t.Errorf("failed listing = (%d, %d, %v), want (0, 0, false)", in, out, answered)
	}
}

// A provider that answered "I don't know" has answered. Re-asking re-fetches an
// entire endpoint catalog, and InputLimit runs inside the agent's step loop —
// so this was a network round-trip per step of every turn, for any model whose
// window nobody publishes.
func TestAnUnknownWindowIsStillAnAnswer(t *testing.T) {
	t.Setenv(InputLimitEnvVar, "")
	mp := &mockLLMProvider{models: []ModelInfo{{ID: "m"}}} // listed, no window stated
	l := &Client{provider: mp, model: "m"}

	for range 5 {
		if got := l.InputLimit(); got != 0 {
			t.Fatalf("InputLimit() = %d, want 0 (unknown)", got)
		}
		if got := l.effectiveMaxTokens(); got != defaultMaxTokens {
			t.Fatalf("effectiveMaxTokens() = %d, want the default", got)
		}
	}
	if mp.listCalls != 1 {
		t.Errorf("ListModels called %d times across 5 rounds, want 1", mp.listCalls)
	}
}

// One listing answers both questions; resolving them separately paid for the
// same round-trip twice.
func TestModelLimitsResolveBothFromOneListing(t *testing.T) {
	mp := &mockLLMProvider{models: []ModelInfo{{ID: "m", InputTokenLimit: 200000, OutputTokenLimit: 8192}}}
	l := &Client{provider: mp, model: "m"}

	if got := l.InputLimit(); got != 200000 {
		t.Fatalf("InputLimit() = %d, want 200000", got)
	}
	if got := l.effectiveMaxTokens(); got != 8192 {
		t.Fatalf("effectiveMaxTokens() = %d, want 8192", got)
	}
	if mp.listCalls != 1 {
		t.Errorf("ListModels called %d times, want 1", mp.listCalls)
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

func (p *retryThenSuccessProvider) Client(string, map[string]string) (*ai.Client, error) {
	return ai.NewClientWithDriver(p, ai.Model{ID: "stub", API: "stub"}), nil
}

func (p *retryThenSuccessProvider) Stream(context.Context, *ai.Request) iter.Seq2[ai.Delta, error] {
	p.calls++
	first := p.calls == 1
	return func(yield func(ai.Delta, error) bool) {
		if first {
			// An overloaded endpoint: retryable, so the next attempt runs.
			yield(ai.Delta{}, &ai.Error{Kind: ai.KindOverloaded, Message: "overloaded"})
			return
		}
		yield(ai.Delta{Block: ai.TextBlock("recovered")}, nil)
		yield(ai.Delta{EndBlock: true}, nil)
		yield(ai.Delta{StopReason: ai.StopEndTurn}, nil)
	}
}

func (*retryThenSuccessProvider) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }
func (*retryThenSuccessProvider) Name() string                                    { return "retry-stream" }

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

// --- the seam the agent loop reaches the endpoint through ---

// headerRecordingProvider is an endpoint whose headers depend on the turn, and
// which remembers what it was handed. Copilot is the real one.
type headerRecordingProvider struct {
	mockLLMProvider
	got map[string]string
}

func (p *headerRecordingProvider) TurnHeaders(msgs []core.Message) map[string]string {
	for _, msg := range msgs {
		if len(msg.Images) > 0 {
			return map[string]string{"X-Initiator": "user", "Copilot-Vision-Request": "true"}
		}
	}
	return map[string]string{"X-Initiator": "user"}
}

func (p *headerRecordingProvider) Client(model string, headers map[string]string) (*ai.Client, error) {
	p.got = headers
	return p.mockLLMProvider.Client(model, headers)
}

// A turn's headers have to reach the endpoint that asked for them. Copilot
// rejects image content outright unless the request opts into vision, so a
// client built without them cannot send a picture at all.
func TestTurnClientCarriesTheTurnsOwnHeaders(t *testing.T) {
	p := &headerRecordingProvider{}
	client := NewClient(p, "mock", 0)

	if _, err := client.TurnClient([]core.Message{
		{Role: core.RoleUser, Content: "look", Images: []core.Attachment{{Image: ai.Image{MediaType: "image/png", Data: "x"}}}},
	}); err != nil {
		t.Fatalf("TurnClient: %v", err)
	}
	if p.got["Copilot-Vision-Request"] != "true" {
		t.Fatalf("headers = %v, want the turn's vision opt-in", p.got)
	}

	if _, err := client.TurnClient([]core.Message{{Role: core.RoleUser, Content: "hi"}}); err != nil {
		t.Fatalf("TurnClient: %v", err)
	}
	if _, asked := p.got["Copilot-Vision-Request"]; asked {
		t.Fatalf("headers = %v, want no vision opt-in on a text-only turn", p.got)
	}
}

// A provider whose headers never vary gets none, rather than an empty map that
// would key its client cache differently from a nil one.
func TestTurnClientSendsNoHeadersForAPlainProvider(t *testing.T) {
	if got := TurnHeaders(&mockLLMProvider{}, []core.Message{{Role: core.RoleUser, Content: "hi"}}); got != nil {
		t.Fatalf("TurnHeaders = %v, want nil", got)
	}
}

// The rung a person picked has to be on every call, and it changes mid-session
// — so it is read when the call is made, not when the client was built.
func TestCallOptionsCarryTheCurrentThinkingEffort(t *testing.T) {
	client := NewClient(&mockLLMProvider{}, "mock", 4096)
	client.SetThinkingEffort("high")

	req := &ai.Request{}
	for _, opt := range client.CallOptions() {
		opt(req)
	}
	if req.Effort != ai.EffortHigh {
		t.Errorf("Effort = %q, want high", req.Effort)
	}
	if req.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", req.MaxTokens)
	}

	client.SetThinkingEffort("low")
	req = &ai.Request{}
	for _, opt := range client.CallOptions() {
		opt(req)
	}
	if req.Effort != ai.EffortLow {
		t.Errorf("Effort after the change = %q, want low", req.Effort)
	}
}
