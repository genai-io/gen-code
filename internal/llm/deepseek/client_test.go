package deepseek

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
		Body:       io.NopCloser(io.Reader(strings.NewReader(streamBody))),
	}
	return resp, nil
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

func TestDeepSeekListModelsPropagatesError(t *testing.T) {
	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL("https://example.com/v1"),
		option.WithHTTPClient(&http.Client{Transport: &modelsErrorTransport{}}),
	)

	c := NewClient(client, "deepseek:test")
	_, err := c.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error from unauthorized models list request")
	}
}

func TestDeepSeekStreamSendsRequest(t *testing.T) {
	transport := &captureTransport{}
	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL("https://example.com/v1"),
		option.WithHTTPClient(&http.Client{Transport: transport}),
	)

	c := NewClient(client, "deepseek:test")

	messages := []core.Message{
		{Role: core.RoleUser, Content: "hi"},
	}
	ch := c.Stream(context.Background(), llm.CompletionOptions{
		Model:        "deepseek-v4-flash",
		Messages:     messages,
		SystemPrompt: "sys",
	})
	for range ch {
	}

	if len(transport.body) == 0 {
		t.Fatal("no request body captured")
	}

	var payload map[string]any
	if err := json.Unmarshal(transport.body, &payload); err != nil {
		t.Fatalf("invalid json body: %v", err)
	}

	if payload["model"] != "deepseek-v4-flash" {
		t.Fatalf("expected model deepseek-v4-flash, got %v", payload["model"])
	}
}

func TestDeepSeekIsTextOnly(t *testing.T) {
	c := NewClient(openai.Client{}, "deepseek:test")
	if c.SupportsImages("deepseek-v4-pro") {
		t.Error("DeepSeek is text-only; SupportsImages should be false")
	}
}

func TestDeepSeekEstimateCost(t *testing.T) {
	cost, ok := EstimateCost("deepseek-v4-flash", llm.Usage{
		InputTokens:  1000000,
		OutputTokens: 1000000,
	})
	if !ok {
		t.Fatal("expected pricing lookup to succeed")
	}
	if cost.Amount < 0.419 || cost.Amount > 0.421 {
		t.Fatalf("expected ~0.42, got %.6f", cost.Amount)
	}
	if cost.Currency != llm.CurrencyUSD {
		t.Fatalf("expected USD, got %s", cost.Currency)
	}
}

func TestDeepSeekV4StreamIncludesReasoningEffort(t *testing.T) {
	transport := &captureTransport{}
	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL("https://example.com/v1"),
		option.WithHTTPClient(&http.Client{Transport: transport}),
	)

	c := NewClient(client, "deepseek:test")

	ch := c.Stream(context.Background(), llm.CompletionOptions{
		Model:          "deepseek-v4-flash",
		Messages:       []core.Message{{Role: core.RoleUser, Content: "hi"}},
		ThinkingEffort: "high",
	})
	for range ch {
	}

	var payload map[string]any
	if err := json.Unmarshal(transport.body, &payload); err != nil {
		t.Fatalf("invalid json body: %v", err)
	}

	effort, _ := payload["reasoning_effort"].(string)
	if effort != "high" {
		t.Fatalf("expected reasoning_effort=high, got %q", effort)
	}
}

// "off" is a san-side value with no API equivalent. Omitting reasoning_effort
// is not enough — thinking is on by default, so the turn would still reason.
func TestDeepSeekV4StreamDisablesThinkingWhenOff(t *testing.T) {
	transport := &captureTransport{}
	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL("https://example.com/v1"),
		option.WithHTTPClient(&http.Client{Transport: transport}),
	)

	c := NewClient(client, "deepseek:test")

	ch := c.Stream(context.Background(), llm.CompletionOptions{
		Model:          "deepseek-v4-flash",
		Messages:       []core.Message{{Role: core.RoleUser, Content: "hi"}},
		ThinkingEffort: "off",
	})
	for range ch {
	}

	var payload map[string]any
	if err := json.Unmarshal(transport.body, &payload); err != nil {
		t.Fatalf("invalid json body: %v", err)
	}

	if got, ok := payload["reasoning_effort"]; ok {
		t.Errorf("sent reasoning_effort=%v, want the field left out", got)
	}
	toggle, _ := payload["thinking"].(map[string]any)
	if toggle["type"] != "disabled" {
		t.Fatalf("thinking = %v, want type=disabled", payload["thinking"])
	}
}

func TestDeepSeekSupportsThinking(t *testing.T) {
	tests := []string{"deepseek-v4-flash", "deepseek-v4-pro"}
	for _, model := range tests {
		if !supportsThinking(model) {
			t.Errorf("supportsThinking(%q) = false, want true", model)
		}
	}
}

func TestDeepSeekThinkingEfforts(t *testing.T) {
	c := NewClient(openai.NewClient(), "deepseek:test")

	tests := []struct {
		model   string
		efforts []string
	}{
		{"deepseek-v4-flash", []string{"off", "low", "high", "xhigh", "max"}},
		{"deepseek-v4-pro", []string{"off", "low", "high", "xhigh", "max"}},
	}
	for _, tt := range tests {
		got := c.ThinkingEfforts(tt.model)
		if len(got) != len(tt.efforts) {
			t.Errorf("ThinkingEfforts(%q) = %v, want %v", tt.model, got, tt.efforts)
			continue
		}
		for i, v := range got {
			if v != tt.efforts[i] {
				t.Errorf("ThinkingEfforts(%q)[%d] = %s, want %s", tt.model, i, v, tt.efforts[i])
			}
		}
	}
}
