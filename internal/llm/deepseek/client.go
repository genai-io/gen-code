// Package deepseek implements the Provider interface using the DeepSeek API.
// DeepSeek's API is OpenAI-compatible, so we reuse the openai-go SDK with a custom base URL.
package deepseek

import (
	"context"
	"encoding/json"

	"github.com/openai/openai-go/v3"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/llm/openaicompat"
)

// Client implements the Provider interface for DeepSeek using the OpenAI SDK.
type Client struct {
	client openai.Client
	name   string
}

// NewClient creates a new DeepSeek client with the given OpenAI SDK client.
func NewClient(client openai.Client, name string) *Client {
	return &Client{client: client, name: name}
}

// Name returns the provider name.
func (c *Client) Name() string { return c.name }

// supportsThinking returns true — all DeepSeek V4 models support reasoning_effort.
func supportsThinking(model string) bool {
	return true
}

// makeAssistantConverter returns a provider-specific assistant message converter.
func makeAssistantConverter(thinking bool) func(core.Message) openai.ChatCompletionMessageParamUnion {
	if !thinking {
		return openaicompat.DefaultAssistantMessage
	}
	return func(msg core.Message) openai.ChatCompletionMessageParamUnion {
		return openaicompat.AssistantMessageWithReasoning(msg, msg.Thinking)
	}
}

func (c *Client) ThinkingEfforts(model string) []string {
	if !supportsThinking(model) {
		return nil
	}
	// https://api-docs.deepseek.com/guides/thinking_mode/
	return []string{"off", "low", "high", "xhigh", "max"}
}

func (c *Client) DefaultThinkingEffort(model string) string {
	if !supportsThinking(model) {
		return ""
	}
	return "off"
}

// Stream sends a completion request and returns a channel of streaming chunks.
func (c *Client) Stream(ctx context.Context, opts llm.CompletionOptions) <-chan llm.StreamChunk {
	thinking := supportsThinking(opts.Model)
	return openaicompat.StreamChatCompletions(ctx, openaicompat.ChatStreamConfig{
		Client:           c.client,
		ProviderName:     c.name,
		Options:          opts,
		ConvertAssistant: makeAssistantConverter(thinking),
		ConfigureParams: func(params *openai.ChatCompletionNewParams) {
			if !thinking || opts.ThinkingEffort == "" {
				return
			}
			if opts.ThinkingEffort == "off" {
				// Thinking runs by default, so leaving reasoning_effort out
				// keeps it on — it has to be switched off explicitly.
				params.SetExtraFields(map[string]any{
					"thinking": map[string]any{"type": "disabled"},
				})
				return
			}
			params.SetExtraFields(map[string]any{
				"reasoning_effort": opts.ThinkingEffort,
			})
		},
		ExtractReasoning: thinking,
	})
}

// ListModels returns the available models from the DeepSeek API.
func (c *Client) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	page, err := c.client.Models.List(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]llm.ModelInfo, 0, len(page.Data))
	for _, m := range page.Data {
		if info, ok := CatalogModel(m.ID); ok {
			models = append(models, info)
			continue
		}
		info := llm.ModelInfo{ID: m.ID, Name: m.ID, DisplayName: m.ID}
		if raw := m.RawJSON(); raw != "" {
			var extra struct {
				ContextLength int `json:"context_length"`
			}
			if err := json.Unmarshal([]byte(raw), &extra); err == nil && extra.ContextLength > 0 {
				info.InputTokenLimit = extra.ContextLength
			}
		}
		models = append(models, info)
	}

	if len(models) == 0 {
		return StaticModels(), nil
	}

	return models, nil
}

// SupportsImages reports that DeepSeek models are text-only: the Chat
// Completions API rejects image_url content parts.
func (c *Client) SupportsImages(_ string) bool { return false }

// Ensure Client implements Provider
var _ llm.Provider = (*Client)(nil)
var _ llm.ThinkingEffortProvider = (*Client)(nil)
var _ llm.ImageSupportProvider = (*Client)(nil)
