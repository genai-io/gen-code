// Package bigmodel implements the Provider interface for BigModel (Zhipu GLM).
// BigModel's API is OpenAI-compatible, so we reuse the openai-go SDK with a
// custom base URL and the shared openaicompat helpers.
package bigmodel

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/openai/openai-go/v3"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/llm/openaicompat"
)

// Client implements the Provider interface for BigModel using the OpenAI SDK.
type Client struct {
	client openai.Client
	name   string
}

// NewClient creates a new BigModel client with the given OpenAI SDK client.
func NewClient(client openai.Client, name string) *Client {
	return &Client{
		client: client,
		name:   name,
	}
}

// Name returns the provider name.
func (c *Client) Name() string {
	return c.name
}

func (c *Client) ThinkingEfforts(model string) []string {
	if !supportsThinking(model) {
		return nil
	}
	return []string{"none", "low", "medium", "high"}
}

func (c *Client) DefaultThinkingEffort(model string) string {
	if !supportsThinking(model) {
		return ""
	}
	return "medium"
}

// supportsThinking reports whether the given model exposes the
// extra_body.thinking toggle. Defaults to true so new GLM models automatically
// inherit the thinking-effort UI affordance; only the small set of known
// non-thinking GLM models is excluded. If BigModel ships a new non-thinking
// model, add its ID here.
func supportsThinking(modelID string) bool {
	switch modelID {
	case "glm-4-long",
		"glm-4-flash-250414",
		"glm-4-flashx-250414",
		"glm-4.7-flash",
		"glm-4.7-flashx":
		return false
	}
	return true
}

// convertAssistant converts an assistant message for BigModel. Like Moonshot,
// BigModel requires reasoning_content on assistant messages when thinking is
// enabled — we always include the field (empty string if no thinking content).
func convertAssistant(msg core.Message) openai.ChatCompletionMessageParamUnion {
	return openaicompat.AssistantMessageWithReasoning(msg, msg.Thinking)
}

// Stream sends a completion request and returns a channel of streaming chunks.
func (c *Client) Stream(ctx context.Context, opts llm.CompletionOptions) <-chan llm.StreamChunk {
	return openaicompat.StreamChatCompletions(ctx, openaicompat.ChatStreamConfig{
		Client:           c.client,
		ProviderName:     c.name,
		Options:          opts,
		ConvertAssistant: convertAssistant,
		ConfigureParams: func(params *openai.ChatCompletionNewParams) {
			if !supportsThinking(opts.Model) {
				return
			}
			if opts.ThinkingEffort != "" && opts.ThinkingEffort != "off" && opts.ThinkingEffort != "none" {
				params.SetExtraFields(map[string]any{
					"thinking": map[string]any{"type": "enabled"},
				})
			}
		},
		ExtractReasoning: true,
	})
}

// ListModels returns the available models for BigModel using the /models API.
// The list is fully dynamic — no hardcoded catalog or fallback. If the API
// errors, the error propagates so users see the real failure rather than a
// stale offline list.
func (c *Client) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	page, err := c.client.Models.List(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]llm.ModelInfo, 0, len(page.Data))
	for _, m := range page.Data {
		id := m.ID
		info := llm.ModelInfo{ID: id, Name: id, DisplayName: id}
		if raw := m.RawJSON(); raw != "" {
			var extra struct {
				ContextLength int `json:"context_length"`
			}
			if err := json.Unmarshal([]byte(raw), &extra); err == nil && extra.ContextLength > 0 {
				info.InputTokenLimit = extra.ContextLength
			}
		}
		if info.InputTokenLimit == 0 {
			info.InputTokenLimit = staticInputLimit(id)
		}
		if info.OutputTokenLimit == 0 {
			info.OutputTokenLimit = staticOutputLimit(id)
		}
		models = append(models, info)
	}

	if len(models) == 0 {
		return nil, fmt.Errorf("bigmodel returned no models")
	}

	slices.SortFunc(models, func(a, b llm.ModelInfo) int { return cmp.Compare(a.ID, b.ID) })
	return models, nil
}

// Ensure Client implements Provider.
var _ llm.Provider = (*Client)(nil)
