package bigmodel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
)

type captureTransport struct {
	body []byte
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		t.body = b
	}

	streamBody := "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(streamBody)),
	}
	return resp, nil
}

type modelsTransport struct {
	body string
}

func (t *modelsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Request:    req,
	}, nil
}

type modelsErrorTransport struct{}

func (t *modelsErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"message":"Invalid Authentication","type":"invalid_authentication_error"}`)),
		Request:    req,
	}, nil
}

func newTestClient(transport http.RoundTripper) *Client {
	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL("https://example.com/v1"),
		option.WithHTTPClient(&http.Client{Transport: transport}),
	)
	return NewClient(client, "bigmodel:test")
}

func streamRequestBody(t *testing.T, c *Client, opts llm.CompletionOptions, transport *captureTransport) map[string]any {
	t.Helper()
	for range c.Stream(context.Background(), opts) {
	}
	if len(transport.body) == 0 {
		t.Fatal("no request body captured")
	}
	var payload map[string]any
	if err := json.Unmarshal(transport.body, &payload); err != nil {
		t.Fatalf("invalid json body: %v", err)
	}
	return payload
}

func TestBigModelThinkingExtraBody(t *testing.T) {
	transport := &captureTransport{}
	c := newTestClient(transport)

	payload := streamRequestBody(t, c, llm.CompletionOptions{
		Model:          "glm-5.1",
		Messages:       []core.Message{{Role: core.RoleUser, Content: "hi"}},
		ThinkingEffort: "medium",
	}, transport)

	thinking, ok := payload["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking field missing or wrong shape: %v", payload["thinking"])
	}
	if got := thinking["type"]; got != "enabled" {
		t.Fatalf("expected thinking.type=enabled, got %v", got)
	}
}

func TestBigModelNoThinkingWhenEffortNone(t *testing.T) {
	for _, effort := range []string{"", "none", "off"} {
		t.Run("effort="+effort, func(t *testing.T) {
			transport := &captureTransport{}
			c := newTestClient(transport)

			payload := streamRequestBody(t, c, llm.CompletionOptions{
				Model:          "glm-5.1",
				Messages:       []core.Message{{Role: core.RoleUser, Content: "hi"}},
				ThinkingEffort: effort,
			}, transport)

			if _, present := payload["thinking"]; present {
				t.Fatalf("thinking field should not be set for effort=%q", effort)
			}
		})
	}
}

func TestBigModelNoThinkingForNonThinkingModel(t *testing.T) {
	transport := &captureTransport{}
	c := newTestClient(transport)

	payload := streamRequestBody(t, c, llm.CompletionOptions{
		Model:          "glm-4-long",
		Messages:       []core.Message{{Role: core.RoleUser, Content: "hi"}},
		ThinkingEffort: "medium",
	}, transport)

	if _, present := payload["thinking"]; present {
		t.Fatal("thinking field should not be set for non-thinking model glm-4-long")
	}
}

func TestBigModelAssistantMessagesIncludeReasoningContent(t *testing.T) {
	transport := &captureTransport{}
	c := newTestClient(transport)

	messages := []core.Message{
		{Role: core.RoleUser, Content: "hi"},
		{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{ID: "tc1", Name: "WebSearch", Input: "{}"}}},
		{Role: core.RoleUser, ToolResult: &core.ToolResult{ToolCallID: "tc1", Content: "ok"}},
		{Role: core.RoleAssistant, Content: "done"},
	}

	payload := streamRequestBody(t, c, llm.CompletionOptions{
		Model:        "glm-5.1",
		Messages:     messages,
		SystemPrompt: "sys",
	}, transport)

	rawMsgs, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("messages not found in payload")
	}
	for i, raw := range rawMsgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role != "assistant" {
			continue
		}
		if _, ok := msg["reasoning_content"]; !ok {
			t.Fatalf("assistant message missing reasoning_content at index %d", i)
		}
	}
}

func TestBigModelListModelsReturnsAPIResults(t *testing.T) {
	transport := &modelsTransport{
		body: `{
			"object": "list",
			"data": [
				{"id": "glm-5.1", "object": "model", "context_length": 204800},
				{"id": "glm-4.6", "object": "model", "context_length": 204800}
			]
		}`,
	}
	c := newTestClient(transport)

	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "glm-4.6" {
		t.Fatalf("expected first model glm-4.6, got %s", models[0].ID)
	}
	if models[1].ID != "glm-5.1" {
		t.Fatalf("expected second model glm-5.1, got %s", models[1].ID)
	}
	if models[1].InputTokenLimit != 204800 {
		t.Fatalf("expected glm-5.1 input limit 204800, got %d", models[1].InputTokenLimit)
	}
}

func TestBigModelListModelsFallsBackToStaticLimit(t *testing.T) {
	// Real BigModel API omits context_length — verify the static fallback
	// kicks in so the status bar can render.
	transport := &modelsTransport{
		body: `{
			"object": "list",
			"data": [
				{"id": "glm-4.7", "object": "model"},
				{"id": "glm-5.2", "object": "model"}
			]
		}`,
	}
	c := newTestClient(transport)

	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	byID := map[string]int{}
	for _, m := range models {
		byID[m.ID] = m.InputTokenLimit
	}
	if byID["glm-4.7"] != 200_000 {
		t.Errorf("glm-4.7 limit = %d, want 200000 (static fallback)", byID["glm-4.7"])
	}
	if byID["glm-5.2"] != 200_000 {
		t.Errorf("glm-5.2 limit = %d, want 200000 (static fallback)", byID["glm-5.2"])
	}
}

func TestStaticLimits(t *testing.T) {
	cases := []struct {
		model      string
		wantInput  int
		wantOutput int
	}{
		{"glm-5.2", 200_000, 128_000},
		{"glm-5.2-flash", 200_000, 128_000},
		{"glm-5", 200_000, 128_000},
		{"glm-4.7", 200_000, 128_000},
		{"glm-4.7-flashx", 200_000, 128_000},
		{"glm-4.6", 200_000, 128_000},
		// GLM-4-Long is the outlier: a 1M window with a 4K output cap.
		{"glm-4-long", 1_000_000, 4_096},
		// An unknown GLM reports "unknown" rather than a blanket default, so
		// San skips proactive compaction instead of compacting against a
		// figure nobody checked.
		{"glm-4-flash-250414", 0, 0},
		{"glm-3-turbo", 0, 0},
	}
	for _, c := range cases {
		if got := staticInputLimit(c.model); got != c.wantInput {
			t.Errorf("staticInputLimit(%q) = %d, want %d", c.model, got, c.wantInput)
		}
		if got := staticOutputLimit(c.model); got != c.wantOutput {
			t.Errorf("staticOutputLimit(%q) = %d, want %d", c.model, got, c.wantOutput)
		}
	}
}

func TestBigModelListModelsReturnsErrorOnAPIFailure(t *testing.T) {
	c := newTestClient(&modelsErrorTransport{})

	models, err := c.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected ListModels to fail")
	}
	if len(models) != 0 {
		t.Fatalf("expected no fallback models, got %d", len(models))
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestBigModelSupportsThinking(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// Known thinking-capable.
		{"glm-5.1", true},
		{"glm-5", true},
		{"glm-5-turbo", true},
		{"glm-4.7", true},
		{"glm-4.6", true},
		{"glm-4.5-air", true},
		{"glm-4.5-airx", true},
		{"glm-4.5-flash", true},
		// Default-open: hypothetical future models inherit thinking.
		{"glm-6.0", true},
		{"glm-99-future", true},
		// Known non-thinking.
		{"glm-4-long", false},
		{"glm-4-flash-250414", false},
		{"glm-4-flashx-250414", false},
		{"glm-4.7-flash", false},
		{"glm-4.7-flashx", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := supportsThinking(tc.model); got != tc.want {
				t.Fatalf("supportsThinking(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestBigModelThinkingEffortsForNonThinkingModel(t *testing.T) {
	c := newTestClient(&modelsErrorTransport{})
	if efforts := c.ThinkingEfforts("glm-4-long"); efforts != nil {
		t.Fatalf("expected no thinking efforts for glm-4-long, got %v", efforts)
	}
	if def := c.DefaultThinkingEffort("glm-4-long"); def != "" {
		t.Fatalf("expected empty default thinking effort for glm-4-long, got %q", def)
	}
}

func TestBigModelThinkingEffortsForThinkingModel(t *testing.T) {
	c := newTestClient(&modelsErrorTransport{})
	efforts := c.ThinkingEfforts("glm-5.1")
	if len(efforts) == 0 {
		t.Fatal("expected thinking efforts for glm-5.1")
	}
	if c.DefaultThinkingEffort("glm-5.1") != "medium" {
		t.Fatalf("expected default medium, got %q", c.DefaultThinkingEffort("glm-5.1"))
	}
}
