package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	sdkprovider "github.com/genai-io/sdk-go/pkg/ai/provider"

	"github.com/genai-io/san/internal/core"
)

// The tests drive the adapter the way San does — a CompletionOptions in,
// StreamChunks out — against a stub endpoint. What they assert on is the two
// things a translation layer can get wrong: the body that reached the wire,
// and the value that came back.

// endpoint is a stub server that records the last request it was given and
// replays a scripted server-sent-event stream.
type endpoint struct {
	server  *httptest.Server
	body    map[string]any
	headers http.Header
	status  int
	error   string
}

func serve(t *testing.T, events ...[2]string) *endpoint {
	t.Helper()
	e := &endpoint{}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		e.headers = r.Header.Clone()
		e.body = map[string]any{}
		_ = json.Unmarshal(raw, &e.body)

		if e.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			if e.status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "7")
			}
			w.WriteHeader(e.status)
			fmt.Fprint(w, e.error)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event[0], event[1])
		}
	}))
	t.Cleanup(e.server.Close)
	return e
}

// claude builds an adapter pointed at the stub, speaking Anthropic Messages —
// the protocol that carries every content kind San uses.
func claude(t *testing.T, e *endpoint) *vendorProvider {
	t.Helper()
	vendor, ok := catalog.Find("anthropic")
	if !ok {
		t.Fatal("the catalog has no anthropic vendor")
	}
	return newVendorProvider("anthropic:api_key", vendor,
		sdkprovider.Config{APIKey: "test-key", BaseURL: e.server.URL})
}

// collect drains a turn the way the agent loop now does: over the SDK's own
// stream, since the chunk bridge San used to keep in between is gone.
func collect(t *testing.T, p *vendorProvider, opts CompletionOptions) (text, thinking string, resp *CompletionResponse, err error) {
	t.Helper()
	for chunk := range legacyStream(context.Background(), p, opts) {
		switch chunk.Type {
		case ChunkTypeText:
			text += chunk.Text
		case ChunkTypeThinking:
			thinking += chunk.Text
		case ChunkTypeDone:
			resp = chunk.Response
		case ChunkTypeError:
			err = chunk.Error
		}
	}
	return text, thinking, resp, err
}

// legacyStream reproduces, for tests only, the chunk stream San used to keep
// between the SDK and its agent loop. Production ranges the SDK's own stream;
// these tests are about what a vendor sends, not the shape it arrives in.
//
// It reaches the endpoint the way production does — the same turn headers, the
// same call options — so a setting that stops being sent fails a test here
// rather than going quiet on the wire.
func legacyStream(ctx context.Context, p Provider, opts CompletionOptions) <-chan StreamChunk {
	ch := make(chan StreamChunk)
	go func() {
		defer close(ch)
		client, err := p.Client(opts.Model, TurnHeaders(p, opts.Messages))
		if err != nil {
			ch <- StreamChunk{Type: ChunkTypeError, Error: err}
			return
		}
		callOpts := []ai.Option{ai.WithSystem(opts.SystemPrompt)}
		if len(opts.Tools) > 0 {
			callOpts = append(callOpts, ai.WithTools(core.ToAITools(opts.Tools)...))
		}
		callOpts = append(callOpts, callOptions(opts.MaxTokens, opts.ThinkingEffort, opts.Temperature)...)
		for event, err := range client.Stream(ctx, core.ToAIMessages(opts.Messages), callOpts...) {
			if err != nil {
				ch <- StreamChunk{Type: ChunkTypeError, Error: err}
				return
			}
			switch {
			case event.Type == ai.EventBlockDelta && event.Block.Type == ai.BlockText:
				ch <- StreamChunk{Type: ChunkTypeText, Text: event.Block.Text}
			case event.Type == ai.EventBlockDelta && event.Block.Type == ai.BlockThinking:
				ch <- StreamChunk{Type: ChunkTypeThinking, Text: event.Block.Text}
			case event.Type == ai.EventDone:
				ch <- StreamChunk{Type: ChunkTypeDone, Response: core.FromAIResponse(event.Response)}
			}
		}
	}()
	return ch
}

func TestStreamCarriesEveryContentKind(t *testing.T) {
	e := serve(t,
		[2]string{"message_start", `{"type":"message_start","message":{"id":"m1","model":"claude-opus-5",` +
			`"usage":{"input_tokens":11,"cache_creation_input_tokens":3,"cache_read_input_tokens":5}}}`},
		[2]string{"content_block_start", `{"type":"content_block_start","index":0,` +
			`"content_block":{"type":"thinking","thinking":""}}`},
		[2]string{"content_block_delta", `{"type":"content_block_delta","index":0,` +
			`"delta":{"type":"thinking_delta","thinking":"weighing it"}}`},
		[2]string{"content_block_delta", `{"type":"content_block_delta","index":0,` +
			`"delta":{"type":"signature_delta","signature":"sig-1"}}`},
		[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		[2]string{"content_block_start", `{"type":"content_block_start","index":1,` +
			`"content_block":{"type":"text","text":""}}`},
		[2]string{"content_block_delta", `{"type":"content_block_delta","index":1,` +
			`"delta":{"type":"text_delta","text":"reading it now"}}`},
		[2]string{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		[2]string{"content_block_start", `{"type":"content_block_start","index":2,` +
			`"content_block":{"type":"tool_use","id":"call_1","name":"Read","input":{}}}`},
		[2]string{"content_block_delta", `{"type":"content_block_delta","index":2,` +
			`"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"main.go\"}"}}`},
		[2]string{"content_block_stop", `{"type":"content_block_stop","index":2}`},
		[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`},
	)

	text, thinking, resp, err := collect(t, claude(t, e), CompletionOptions{
		Model:        "claude-opus-5",
		SystemPrompt: "be brief",
		Messages:     []core.Message{{Role: ai.RoleUser, Content: "read main.go"}},
		Tools:        []ToolSchema{{Name: "Read", Description: "Read a file", Parameters: map[string]any{"type": "object"}}},
		MaxTokens:    2048,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if text != "reading it now" {
		t.Errorf("streamed text = %q", text)
	}
	if thinking != "weighing it" {
		t.Errorf("streamed thinking = %q", thinking)
	}
	if resp == nil {
		t.Fatal("no final response")
	}
	if resp.Content != "reading it now" || resp.Thinking != "weighing it" {
		t.Errorf("response = %+v", resp)
	}
	if resp.ThinkingSignature != "sig-1" {
		t.Errorf("ThinkingSignature = %q, want the token that replays the block", resp.ThinkingSignature)
	}
	if resp.StopReason != core.StopToolUse {
		t.Errorf("StopReason = %q", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "Read" ||
		resp.ToolCalls[0].Input != `{"path":"main.go"}` {
		t.Errorf("ToolCalls = %+v", resp.ToolCalls)
	}
	want := Usage{InputTokens: 11, OutputTokens: 9, CacheCreationInputTokens: 3, CacheReadInputTokens: 5}
	if resp.Usage != want {
		t.Errorf("Usage = %+v, want %+v", resp.Usage, want)
	}

	// The request carried what San asked for.
	if e.body["system"] == nil {
		t.Error("the system prompt did not reach the wire")
	}
	if tools, _ := e.body["tools"].([]any); len(tools) != 1 {
		t.Errorf("tools on the wire = %v", e.body["tools"])
	}
	if e.body["max_tokens"] != float64(2048) {
		t.Errorf("max_tokens = %v", e.body["max_tokens"])
	}
}

func TestAssistantTurnReplaysInOrder(t *testing.T) {
	e := serve(t,
		[2]string{"message_start", `{"type":"message_start","message":{"id":"m2","model":"claude-opus-5","usage":{"input_tokens":4}}}`},
		[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
	)

	history := []core.Message{
		{Role: ai.RoleUser, Content: "read main.go"},
		{
			Role:              ai.RoleAssistant,
			Content:           "reading it",
			Thinking:          "weighing it",
			ThinkingSignature: "sig-1",
			ToolCalls:         []core.ToolCall{{ID: "call_1", Name: "Read", Input: `{"path":"main.go"}`}},
		},
		{Role: ai.RoleUser, ToolResult: &core.ToolResult{ToolCallID: "call_1", ToolName: "Read", Content: "package main"}},
	}
	if _, _, _, err := collect(t, claude(t, e), CompletionOptions{
		Model: "claude-opus-5", Messages: history,
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}

	messages, _ := e.body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages on the wire = %d, want the whole history", len(messages))
	}
	assistant, _ := messages[1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	var kinds []string
	for _, block := range blocks {
		b, _ := block.(map[string]any)
		kinds = append(kinds, fmt.Sprint(b["type"]))
	}
	// Anthropic rejects a turn whose thinking does not lead, and a tool call
	// with no answer.
	if strings.Join(kinds, ",") != "thinking,text,tool_use" {
		t.Errorf("assistant blocks = %v, want thinking first and the call last", kinds)
	}
	result, _ := messages[2].(map[string]any)
	resultBlocks, _ := result["content"].([]any)
	if len(resultBlocks) != 1 {
		t.Fatalf("tool result blocks = %v", resultBlocks)
	}
	if got, _ := resultBlocks[0].(map[string]any); fmt.Sprint(got["tool_use_id"]) != "call_1" {
		t.Errorf("tool result = %v, want it paired with call_1", got)
	}
}

func TestUnansweredToolCallIsRepaired(t *testing.T) {
	e := serve(t,
		[2]string{"message_start", `{"type":"message_start","message":{"id":"m3","model":"claude-opus-5","usage":{"input_tokens":4}}}`},
		[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
	)

	// A turn interrupted between the call and its result: every protocol
	// rejects the history as it stands.
	history := []core.Message{
		{Role: ai.RoleUser, Content: "read main.go"},
		{Role: ai.RoleAssistant, Content: "reading it", ToolCalls: []core.ToolCall{{ID: "call_1", Name: "Read", Input: "{}"}}},
	}
	if _, _, _, err := collect(t, claude(t, e), CompletionOptions{
		Model: "claude-opus-5", Messages: history,
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}

	if strings.Contains(fmt.Sprint(e.body["messages"]), "tool_use") {
		t.Errorf("an unanswered call reached the wire: %v", e.body["messages"])
	}
}

func TestRateLimitIsRetryableWithTheProvidersHint(t *testing.T) {
	e := serve(t)
	e.status = http.StatusTooManyRequests
	e.error = `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`

	_, _, _, err := collect(t, claude(t, e), CompletionOptions{
		Model: "claude-opus-5", Messages: []core.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	// Asked of pkg/ai now. San used to re-tag this into its own vocabulary,
	// and the partition it produced was this one exactly.
	if !ai.IsRetryable(err) {
		t.Fatalf("a 429 was not retryable: %v", err)
	}
	if got := ai.RetryAfter(err); got != 7*time.Second {
		t.Errorf("RetryAfter = %v, want the provider's own hint", got)
	}
}

func TestOverflowedPromptAsksForCompaction(t *testing.T) {
	e := serve(t)
	e.status = http.StatusBadRequest
	e.error = `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 300000 tokens > 200000 maximum"}}`

	_, _, _, err := collect(t, claude(t, e), CompletionOptions{
		Model: "claude-opus-5", Messages: []core.Message{{Role: ai.RoleUser, Content: "hi"}},
	})
	if !ai.IsContextExceeded(err) {
		t.Fatalf("an overflowed prompt was not marked for compaction: %v", err)
	}
	if ai.IsRetryable(err) {
		t.Error("an overflowed prompt was marked retryable; replaying it cannot help")
	}
}

func TestModelInfoCarriesLimitsAndReasoning(t *testing.T) {
	e := serve(t)
	// The listing endpoint is not what this stub serves, so the catalog is
	// what answers — which is the fallback San relies on for a vendor whose
	// models it already knows.
	models, err := claude(t, e).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	var opus ModelInfo
	for _, m := range models {
		if m.ID == "claude-opus-5" {
			opus = m
		}
		if m.ID == "claude-opus-4-1-20250805" {
			t.Error("a retired model reached the picker")
		}
	}
	if opus.ID == "" {
		t.Fatalf("claude-opus-5 is missing from %d models", len(models))
	}
	if opus.InputTokenLimit == 0 || opus.OutputTokenLimit == 0 {
		t.Errorf("model limits = %d/%d, want the catalog's figures", opus.InputTokenLimit, opus.OutputTokenLimit)
	}
	if opus.Reasoning == nil || len(opus.Reasoning.SupportedEfforts) == 0 {
		t.Fatalf("Reasoning = %+v, want the model's ladder", opus.Reasoning)
	}
	if opus.Reasoning.DefaultEffort == "" {
		t.Error("the ladder states no default rung")
	}
}

func TestThinkingEffortReachesTheWire(t *testing.T) {
	e := serve(t,
		[2]string{"message_start", `{"type":"message_start","message":{"id":"m4","model":"claude-opus-5","usage":{"input_tokens":4}}}`},
		[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
	)

	p := claude(t, e)
	efforts := p.ThinkingEfforts("claude-opus-5")
	if len(efforts) == 0 {
		t.Fatal("claude-opus-5 advertises no reasoning rungs")
	}
	top := efforts[len(efforts)-1]

	if _, _, _, err := collect(t, p, CompletionOptions{
		Model:          "claude-opus-5",
		Messages:       []core.Message{{Role: ai.RoleUser, Content: "hi"}},
		ThinkingEffort: top,
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains(fmt.Sprint(e.body), top) {
		t.Errorf("effort %q did not reach the wire: %v", top, e.body)
	}
}

func TestCopilotOptsIntoVisionOnlyWhenSendingImages(t *testing.T) {
	headers := turnHeadersFor(copilotVendor)
	if headers == nil {
		t.Fatal("Copilot states no turn-dependent headers")
	}

	opening := headers([]core.Message{{Role: ai.RoleUser, Content: "hi"}})
	if opening["X-Initiator"] != "user" {
		t.Errorf("X-Initiator on the opening turn = %q", opening["X-Initiator"])
	}
	if _, ok := opening["Copilot-Vision-Request"]; ok {
		t.Error("a text-only turn asked for vision")
	}

	followUp := headers([]core.Message{
		{Role: ai.RoleUser, Content: "look", Images: []core.Attachment{{Image: ai.Image{MediaType: "image/png", Data: "x"}}}},
		{Role: ai.RoleAssistant, Content: "looking"},
	})
	if followUp["X-Initiator"] != "agent" {
		t.Errorf("X-Initiator once the loop is driving = %q", followUp["X-Initiator"])
	}
	if followUp["Copilot-Vision-Request"] != "true" {
		t.Error("a turn carrying an image did not opt into vision")
	}
}

func TestEveryRegisteredVendorResolves(t *testing.T) {
	for _, e := range vendorEntries {
		if e.vendorID == "" {
			continue // the user-defined endpoint has no catalog row
		}
		vendor, ok := catalog.Find(e.vendorID)
		if !ok {
			t.Errorf("%s: no catalog vendor %q", providerName(e.meta), e.vendorID)
			continue
		}
		if _, registered := driverFor(vendor.API); !registered {
			t.Errorf("%s: no driver linked in for %q", providerName(e.meta), vendor.API)
		}
		if _, shown := vendorDisplays[e.meta.Provider]; !shown {
			t.Errorf("%s: no display row", providerName(e.meta))
		}
	}
}

// driverFor reports whether a protocol has a driver in this binary.
func driverFor(api ai.API) (ai.API, bool) {
	for _, registered := range ai.RegisteredAPIs() {
		if registered == api {
			return api, true
		}
	}
	return "", false
}

// listing serves an OpenAI-compatible model list and nothing else.
func listing(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAListedModelKeepsWhatItsVendorKnows(t *testing.T) {
	// What most OpenAI-compatible endpoints publish: an ID, and nothing that
	// would let a caller size the context window.
	server := listing(t, `{"object":"list","data":[{"id":"glm-4.7","object":"model"},{"id":"glm-9-imaginary","object":"model"}]}`)

	vendor, ok := catalog.Find("bigmodel")
	if !ok {
		t.Fatal("the catalog has no bigmodel vendor")
	}
	p := newVendorProvider("bigmodel:api_key", vendor, sdkprovider.Config{APIKey: "k", BaseURL: server.URL})

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	byID := map[string]ModelInfo{}
	for _, m := range models {
		byID[m.ID] = m
	}

	// One the vendor lists: its window comes from the catalog row.
	listed, present := byID["glm-4.7"]
	if !present {
		t.Fatalf("glm-4.7 is missing from %+v", models)
	}
	if listed.InputTokenLimit == 0 {
		t.Error("a listed model reached the picker with no window; the context " +
			"percentage and auto-compaction both go quiet when that happens")
	}
	if listed.Reasoning == nil {
		t.Error("a listed model reached the picker with no reasoning ladder")
	}

	// One it does not: the vendor still sizes it from the ID.
	unlisted, present := byID["glm-9-imaginary"]
	if !present {
		t.Fatalf("an unlisted model was dropped from the listing: %+v", models)
	}
	if unlisted.InputTokenLimit != 0 {
		t.Errorf("glm-9-imaginary window = %d; the vendor cannot size an ID it "+
			"does not recognise, and a guess is worse than nothing", unlisted.InputTokenLimit)
	}
}

func TestModelStudioAnswersOneModelAtATime(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"qwen3.8-max","extra_info":{"default_envs":{"max_input_tokens":129024,"max_output_tokens":8192}}}`)
	}))
	t.Cleanup(server.Close)

	vendor, ok := catalog.Find("alibaba")
	if !ok {
		t.Fatal("the catalog has no alibaba vendor")
	}
	p := newVendorProvider("alibaba:api_key", vendor, sdkprovider.Config{APIKey: "k", BaseURL: server.URL})

	in, out, err := p.FetchModelLimits(context.Background(), "qwen3.8-max")
	if err != nil {
		t.Fatalf("FetchModelLimits: %v", err)
	}
	if in != 129024 || out != 8192 {
		t.Errorf("limits = %d/%d, want the figures the endpoint reported", in, out)
	}
	if !strings.HasSuffix(path, "/models/qwen3.8-max") {
		t.Errorf("asked %q, want the per-model detail path", path)
	}

	// Every other vendor says so rather than spending a round trip to find out.
	deepseek, _ := catalog.Find("deepseek")
	other := newVendorProvider("deepseek:api_key", deepseek, sdkprovider.Config{APIKey: "k", BaseURL: server.URL})
	if _, _, err := other.FetchModelLimits(context.Background(), "deepseek-v4-pro"); err == nil {
		t.Error("a vendor with no per-model detail endpoint reported limits anyway")
	}
}

// A guardrail, carried over from the per-vendor catalogs this replaces: every
// model San offers in a picker must state a context window.
//
// A zero window is not a cosmetic gap. It is what "cannot size this
// conversation" means: the context percentage cannot render and auto-compaction
// cannot fire, both silently. A model whose window genuinely is not known
// belongs in neither catalog — leave it to the endpoint's own listing, where
// its absence is at least explained by the endpoint saying nothing.
func TestEveryCatalogModelStatesItsWindow(t *testing.T) {
	for _, e := range vendorEntries {
		if e.vendorID == "" || e.vendorID == alibabaVendor {
			// The user-defined endpoint ships no models, and Model Studio
			// answers per model — see TestModelStudioAnswersOneModelAtATime.
			continue
		}
		vendor, ok := catalog.Find(e.vendorID)
		if !ok {
			t.Errorf("%s: no catalog vendor %q", providerName(e.meta), e.vendorID)
			continue
		}
		for _, m := range ai.Available(vendor.ModelList()) {
			if m.ContextWindow == 0 {
				t.Errorf("%s: model %q states no context window", e.vendorID, m.ID)
			}
		}
	}
}

// A vendor's transient failure has to reach the agent loop as one. The SDK
// classifies it from the provider's typed error; San's job is to carry that
// answer across onto core.RetryableError, which is the only thing the loop
// reads. Assert the whole path, because a break anywhere in it turns a
// retryable overload into a failed turn.
func TestAnOverloadedEndpointReachesTheLoopAsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`)
	}))
	t.Cleanup(server.Close)

	vendor, ok := catalog.Find("anthropic")
	if !ok {
		t.Fatal("the catalog has no anthropic vendor")
	}
	p := newVendorProvider("anthropic:api_key", vendor, sdkprovider.Config{APIKey: "k", BaseURL: server.URL})

	var streamErr error
	for chunk := range legacyStream(context.Background(), p, CompletionOptions{
		Model:    "claude-opus-5",
		Messages: []core.Message{{Role: ai.RoleUser, Content: "hi"}},
	}) {
		if chunk.Type == ChunkTypeError {
			streamErr = chunk.Error
		}
	}
	if streamErr == nil {
		t.Fatal("an overloaded endpoint produced no error")
	}

	if !ai.IsRetryable(streamErr) {
		t.Errorf("an overloaded endpoint was not retryable: %T", streamErr)
	}
	if !strings.Contains(streamErr.Error(), "overloaded") {
		t.Errorf("the provider's own message was lost: %v", streamErr)
	}
}

// The picker's labels are only as good as the facts behind them, and every one
// of these was blank before: nothing populated a model's stage or its
// modalities. Assert the projection, not the wording — the wording is the
// picker's (see modelLabels).
func TestToModelInfoCarriesTheCatalogsFacts(t *testing.T) {
	tests := []struct {
		name  string
		model ai.Model
		want  ModelInfo
	}{
		{
			name:  "a plain model states nothing extra",
			model: ai.Model{ID: "plain", Input: []ai.Modality{ai.ModalityImage}},
			want:  ModelInfo{ID: "plain", Name: "plain", DisplayName: "plain"},
		},
		{
			name:  "a text-only model is flagged; taking images is the norm",
			model: ai.Model{ID: "chat", Input: []ai.Modality{ai.ModalityText}},
			want:  ModelInfo{ID: "chat", Name: "chat", DisplayName: "chat", TextOnly: true},
		},
		{
			name:  "a model that takes images says nothing",
			model: ai.Model{ID: "opus", Input: []ai.Modality{ai.ModalityText, ai.ModalityImage}},
			want:  ModelInfo{ID: "opus", Name: "opus", DisplayName: "opus"},
		},
		{
			name:  "a preview model says so",
			model: ai.Model{ID: "prev", Stage: ai.StagePreview, Input: []ai.Modality{ai.ModalityImage}},
			want:  ModelInfo{ID: "prev", Name: "prev", DisplayName: "prev", Lifecycle: ModelPreview},
		},
		{
			name: "a deprecation carries its replacement",
			model: ai.Model{ID: "old", Stage: ai.StageDeprecated, Replacement: "claude-opus-5",
				Input: []ai.Modality{ai.ModalityImage}},
			want: ModelInfo{
				ID: "old", Name: "old", DisplayName: "old",
				Lifecycle: ModelDeprecated, Replacement: "claude-opus-5",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := toModelInfo(tc.model); got != tc.want {
				t.Errorf("toModelInfo() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
