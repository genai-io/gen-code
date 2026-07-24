package openaicompat

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

type streamCaptureTransport struct {
	body []byte
}

func (t *streamCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		t.body = body
	}

	streamBody := strings.Join([]string{
		`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		`data: {"id":"1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(streamBody)),
	}, nil
}

type cachedUsageTransport struct{}

func (t *cachedUsageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	streamBody := strings.Join([]string{
		`data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		`data: {"id":"1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":20,"total_tokens":1020,"prompt_tokens_details":{"cached_tokens":900}}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(streamBody)),
	}, nil
}

type authErrorTransport struct{}

func (t *authErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"message":"Invalid Authentication","type":"invalid_authentication_error"}`)),
		Request:    req,
	}, nil
}

func TestStreamChatCompletionsRequestsAndReadsUsage(t *testing.T) {
	transport := &streamCaptureTransport{}
	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL("https://example.com/v1"),
		option.WithHTTPClient(&http.Client{Transport: transport}),
	)

	ch := StreamChatCompletions(context.Background(), ChatStreamConfig{
		Client:           client,
		ProviderName:     "test",
		ConvertAssistant: DefaultAssistantMessage,
		Options: llm.CompletionOptions{
			Model:    "gpt-test",
			Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
		},
	})

	var done *llm.CompletionResponse
	for chunk := range ch {
		if chunk.Type == llm.ChunkTypeDone {
			done = chunk.Response
		}
	}

	if len(transport.body) == 0 {
		t.Fatal("no request body captured")
	}
	var payload map[string]any
	if err := json.Unmarshal(transport.body, &payload); err != nil {
		t.Fatalf("invalid request body: %v", err)
	}
	streamOptions, ok := payload["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options missing from request: %s", string(transport.body))
	}
	if streamOptions["include_usage"] != true {
		t.Fatalf("include_usage = %#v, want true", streamOptions["include_usage"])
	}

	if done == nil {
		t.Fatal("missing done chunk")
	}
	if done.Usage.InputTokens != 11 || done.Usage.OutputTokens != 7 {
		t.Fatalf("usage = in:%d out:%d, want in:11 out:7", done.Usage.InputTokens, done.Usage.OutputTokens)
	}
}

func TestStreamChatCompletionsSplitsCachedPromptTokens(t *testing.T) {
	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL("https://example.com/v1"),
		option.WithHTTPClient(&http.Client{Transport: &cachedUsageTransport{}}),
	)

	ch := StreamChatCompletions(context.Background(), ChatStreamConfig{
		Client:           client,
		ProviderName:     "deepseek",
		ConvertAssistant: DefaultAssistantMessage,
		Options: llm.CompletionOptions{
			Model:    "deepseek-v4-pro",
			Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
		},
	})

	var done *llm.CompletionResponse
	for chunk := range ch {
		if chunk.Type == llm.ChunkTypeDone {
			done = chunk.Response
		}
	}

	if done == nil {
		t.Fatal("missing done chunk")
	}
	// prompt_tokens (1000) is the full prompt; the cached slice (900) moves to
	// CacheRead so InputTokens holds only the fresh tokens (Anthropic
	// convention). Their sum still equals the full prompt for the ctx readout.
	if done.Usage.InputTokens != 100 {
		t.Fatalf("InputTokens = %d, want 100 (1000 prompt - 900 cached)", done.Usage.InputTokens)
	}
	if done.Usage.CacheReadInputTokens != 900 {
		t.Fatalf("CacheReadInputTokens = %d, want 900", done.Usage.CacheReadInputTokens)
	}
	if done.Usage.OutputTokens != 20 {
		t.Fatalf("OutputTokens = %d, want 20", done.Usage.OutputTokens)
	}
}

func TestStreamChatCompletionsNormalizesAuthError(t *testing.T) {
	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL("https://api.moonshot.cn/v1"),
		option.WithHTTPClient(&http.Client{Transport: &authErrorTransport{}}),
	)

	ch := StreamChatCompletions(context.Background(), ChatStreamConfig{
		Client:           client,
		ProviderName:     "moonshot:api_key",
		ConvertAssistant: DefaultAssistantMessage,
		Options: llm.CompletionOptions{
			Model:    "kimi-k2.6",
			Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
		},
	})

	var got error
	for chunk := range ch {
		if chunk.Type == llm.ChunkTypeError {
			got = chunk.Error
			break
		}
	}

	if got == nil {
		t.Fatal("expected auth error chunk")
	}
	msg := got.Error()
	if !strings.Contains(msg, "Moonshot authentication failed") {
		t.Fatalf("error %q missing provider guidance", msg)
	}
	if !strings.Contains(msg, "MOONSHOT_API_KEY") {
		t.Fatalf("error %q missing env var guidance", msg)
	}
	if !strings.Contains(msg, "/models") {
		t.Fatalf("error %q missing reconnect guidance", msg)
	}
}
