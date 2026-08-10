package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	streamutil "github.com/genai-io/san/internal/llm/stream"
	"github.com/genai-io/san/internal/log"
)

// validToolIDPattern matches the Claude API requirement for tool_use IDs.
var validToolIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// toolIDSanitizer lazily builds a mapping from invalid tool call IDs to
// Claude-compatible replacements. It is constructed once per Stream call
// and populated on-the-fly during message conversion (single pass).
type toolIDSanitizer struct {
	idMap   map[string]string
	counter int
}

// resolve returns a valid tool ID. If the ID already matches Claude's pattern,
// it is returned as-is. Otherwise a stable replacement is created (or reused).
func (s *toolIDSanitizer) resolve(id string) string {
	if validToolIDPattern.MatchString(id) {
		return id
	}
	if s.idMap == nil {
		s.idMap = make(map[string]string)
	}
	if mapped, ok := s.idMap[id]; ok {
		return mapped
	}
	s.counter++
	mapped := fmt.Sprintf("toolu_compat_%d", s.counter)
	s.idMap[id] = mapped
	return mapped
}

// Client implements the Provider interface using the Anthropic SDK
type Client struct {
	client       anthropic.Client
	name         string
	modelsMu     sync.Mutex
	cachedModels []llm.ModelInfo
}

// NewClient creates a new Anthropic client with the given SDK client
func NewClient(client anthropic.Client, name string) *Client {
	return &Client{
		client: client,
		name:   name,
	}
}

// Name returns the provider name
func (c *Client) Name() string {
	return c.name
}

// CachesToolsAndSystemPrompt implements llm.PromptPrefixCacheProvider. Stream
// marks the system block ephemeral and nothing else, and Anthropic renders a
// request as tools → system → messages, so the cached prefix is exactly the
// tool definitions plus the system prompt. That makes the reported cache tokens
// an exact measurement of those two — which is what /context reads them as.
// Moving or adding a breakpoint in Stream invalidates that reading.
func (c *Client) CachesToolsAndSystemPrompt() bool { return true }

// Stream sends a completion request and returns a channel of streaming chunks
func (c *Client) Stream(ctx context.Context, opts llm.CompletionOptions) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk)

	go func() {
		defer close(ch)

		// Sanitizer for cross-provider tool ID compatibility (lazy, single-pass)
		var ids toolIDSanitizer
		thinkingBudget := int64(anthropicThinkingBudget(opts.Model, opts.ThinkingEffort))

		// Remove orphaned tool_result blocks whose tool_use_id doesn't match
		// any tool_use in the nearest preceding assistant core. This guards
		// against stale results from cancelled tool executions.
		sanitized := sanitizeToolResults(opts.Messages)

		// Convert messages to Anthropic format
		anthropicMsgs := make([]anthropic.MessageParam, 0, len(sanitized))
		for _, msg := range sanitized {
			if msg.ToolResult != nil {
				// Tool result message
				anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(
					anthropic.NewToolResultBlock(
						ids.resolve(msg.ToolResult.ToolCallID),
						msg.ToolResult.Content,
						msg.ToolResult.IsError,
					),
				))
				continue
			}
			switch msg.Role {
			case core.RoleUser:
				if len(msg.Images) > 0 {
					if parts := core.InterleavedContentParts(msg); parts != nil {
						blocks := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
						for _, p := range parts {
							switch p.Type {
							case core.ContentPartText:
								blocks = append(blocks, anthropic.NewTextBlock(p.Text))
							case core.ContentPartImage:
								blocks = append(blocks, anthropic.NewImageBlockBase64(p.Image.MediaType, p.Image.Data))
							}
						}
						anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(blocks...))
					} else {
						blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Images)+1)
						for _, img := range msg.Images {
							blocks = append(blocks, anthropic.NewImageBlockBase64(
								img.MediaType,
								img.Data,
							))
						}
						if msg.Content != "" {
							blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
						}
						anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(blocks...))
					}
				} else if msg.Content != "" {
					anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(
						anthropic.NewTextBlock(msg.Content),
					))
				}
			case core.RoleAssistant:
				blocks := assistantContentBlocks(msg, thinkingBudget)
				if len(msg.ToolCalls) > 0 {
					for _, tc := range msg.ToolCalls {
						var input any
						if tc.Input != "" {
							if err := json.Unmarshal([]byte(tc.Input), &input); err != nil {
								input = tc.Input
							}
						} else {
							input = map[string]any{}
						}
						blocks = append(blocks, anthropic.NewToolUseBlock(ids.resolve(tc.ID), input, tc.Name))
					}
					anthropicMsgs = append(anthropicMsgs, anthropic.NewAssistantMessage(blocks...))
				} else if len(blocks) > 0 {
					anthropicMsgs = append(anthropicMsgs, anthropic.NewAssistantMessage(blocks...))
				}
			}
		}

		// Merge consecutive same-role messages into single messages.
		// This is required because multiple tool results are stored as separate
		// user messages internally, but the Claude API requires all tool_result
		// blocks to be in a single user message following the assistant's tool_use.
		anthropicMsgs = mergeConsecutiveMessages(anthropicMsgs)

		// Build request params
		maxTokens := int64(opts.MaxTokens)

		// When extended thinking is enabled, budget_tokens must be < max_tokens.
		// Ensure max_tokens is large enough to accommodate the thinking budget + response.
		if thinkingBudget > 0 && maxTokens <= thinkingBudget {
			maxTokens = thinkingBudget + 8192 // leave room for the actual response
		}

		modelID, reqOpts := resolveModelOpts(opts.Model)

		params := anthropic.MessageNewParams{
			Model:     anthropic.Model(modelID),
			MaxTokens: maxTokens,
			Messages:  anthropicMsgs,
		}

		// Configure extended thinking
		if thinkingBudget > 0 {
			params.Thinking = anthropic.ThinkingConfigParamOfEnabled(thinkingBudget)
		}

		if opts.SystemPrompt != "" {
			// Mark the last system block as ephemeral to enable prompt caching.
			// This lets Anthropic cache the system prompt across requests, reducing
			// cost and latency for long system prompts (tools, memory, CLAUDE.md).
			params.System = []anthropic.TextBlockParam{
				{Text: opts.SystemPrompt, CacheControl: anthropic.NewCacheControlEphemeralParam()},
			}
		}

		// Add tools if provided
		if len(opts.Tools) > 0 {
			params.Tools = convertAnthropicTools(opts.Tools)
		}

		// Log request
		log.LogRequestCtx(ctx, c.name, opts.Model, opts)

		// Create streaming request
		stream := c.client.Messages.NewStreaming(ctx, params, reqOpts...)

		state := streamutil.NewState(c.name)

		// Track tool calls
		var currentToolID string
		var currentToolName string
		var currentToolInput strings.Builder

		// Read stream events
		for stream.Next() {
			event := stream.Current()
			state.Count()

			switch event.Type {
			case "content_block_start":
				block := event.AsContentBlockStart()
				if block.ContentBlock.Type == "tool_use" {
					currentToolID = block.ContentBlock.ID
					currentToolName = block.ContentBlock.Name
					currentToolInput.Reset()
				}

			case "content_block_delta":
				delta := event.AsContentBlockDelta()
				switch delta.Delta.Type {
				case "text_delta":
					state.EmitText(ctx, ch, delta.Delta.Text)
				case "thinking_delta":
					state.EmitThinking(ctx, ch, delta.Delta.Thinking)
				case "signature_delta":
					if delta.Delta.Signature != "" {
						state.Response.ThinkingSignature += delta.Delta.Signature
					}
				case "input_json_delta":
					currentToolInput.WriteString(delta.Delta.PartialJSON)
				}

			case "content_block_stop":
				// When a tool block ends, add the accumulated tool call
				if currentToolID != "" && currentToolName != "" {
					state.Response.ToolCalls = append(state.Response.ToolCalls, core.ToolCall{
						ID:    currentToolID,
						Name:  currentToolName,
						Input: currentToolInput.String(),
					})
					currentToolID = ""
					currentToolName = ""
					currentToolInput.Reset()
				}

			case "message_delta":
				msgDelta := event.AsMessageDelta()
				state.Response.StopReason = core.StopReason(msgDelta.Delta.StopReason)
				// Anthropic populates only OutputTokens here; some Anthropic-compatible
				// providers (e.g. SenseNova) emit InputTokens in message_delta rather
				// than message_start. UpdateUsage is a no-op when the value is 0, so
				// passing both is safe for either shape.
				state.UpdateUsage(int(msgDelta.Usage.InputTokens), int(msgDelta.Usage.OutputTokens))
				state.UpdateCacheUsage(int(msgDelta.Usage.CacheCreationInputTokens), int(msgDelta.Usage.CacheReadInputTokens))

			case "message_start":
				msgStart := event.AsMessageStart()
				state.UpdateUsage(int(msgStart.Message.Usage.InputTokens), 0)
				state.UpdateCacheUsage(
					int(msgStart.Message.Usage.CacheCreationInputTokens),
					int(msgStart.Message.Usage.CacheReadInputTokens),
				)
			}
		}

		if err := stream.Err(); err != nil {
			state.Fail(ctx, ch, err)
			return
		}

		state.EnsureToolUseStopReason()
		state.Finish(ctx, ch)
	}()

	return ch
}

// defaultModels is the fallback static model list.
var defaultModels = StaticModels()

// ListModels returns available models using the Anthropic Models API,
// falling back to a static list if the API call fails.
// Unlike sync.Once, a failed fetch (e.g. due to a cancelled context) does not
// permanently cache the fallback — subsequent calls will retry the API.
func (c *Client) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	c.modelsMu.Lock()
	defer c.modelsMu.Unlock()

	if c.cachedModels != nil {
		return c.cachedModels, nil
	}

	models, err := c.fetchModels(ctx)
	if err != nil {
		// Return static fallback but don't cache it so we retry next time
		return defaultModels, nil
	}
	c.cachedModels = models
	return c.cachedModels, nil
}

// fetchModels fetches available models from the Anthropic Models API
func (c *Client) fetchModels(ctx context.Context) ([]llm.ModelInfo, error) {
	pager := c.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})

	var models []llm.ModelInfo
	for pager.Next() {
		m := pager.Current()
		if info, ok := CatalogModel(m.ID); ok {
			if m.DisplayName != "" {
				info.Name = m.DisplayName
				info.DisplayName = m.DisplayName
			}
			models = append(models, info)
			continue
		}
		models = append(models, llm.ModelInfo{
			ID:          m.ID,
			Name:        m.DisplayName,
			DisplayName: m.DisplayName,
		})
	}
	if err := pager.Err(); err != nil {
		return nil, err
	}

	if len(models) == 0 {
		return nil, fmt.Errorf("no models returned from API")
	}
	return models, nil
}

// sanitizeToolResults ensures tool_use/tool_result consistency:
//  1. Removes orphaned tool_result messages whose tool_use_id doesn't match
//     any tool_use in the nearest preceding assistant core.
//  2. Strips tool_use blocks from assistant messages when no corresponding
//     tool_result exists in the immediately following messages.
//
// This prevents API errors ("unexpected tool_use_id" and "tool_use ids were
// found without tool_result blocks") caused by stale results from cancelled
// tool executions, session restore artifacts, or interrupted tool dispatch.
func sanitizeToolResults(msgs []core.Message) []core.Message {
	// First pass: collect all tool_result IDs for forward-reference checking.
	allResultIDs := make(map[string]bool, len(msgs))
	for _, msg := range msgs {
		if msg.ToolResult != nil {
			allResultIDs[msg.ToolResult.ToolCallID] = true
		}
	}

	// Second pass: filter orphaned tool_results and strip orphaned tool_uses.
	var currentToolIDs map[string]bool
	result := make([]core.Message, 0, len(msgs))

	for _, msg := range msgs {
		switch {
		case msg.Role == core.RoleAssistant:
			// Strip tool_use blocks that have no matching tool_result anywhere.
			if len(msg.ToolCalls) > 0 {
				filtered := make([]core.ToolCall, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					if allResultIDs[tc.ID] {
						filtered = append(filtered, tc)
					}
				}
				if len(filtered) != len(msg.ToolCalls) {
					msg.ToolCalls = filtered
				}
			}

			currentToolIDs = make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				currentToolIDs[tc.ID] = true
			}
			result = append(result, msg)

		case msg.ToolResult != nil:
			if currentToolIDs != nil && currentToolIDs[msg.ToolResult.ToolCallID] {
				result = append(result, msg)
			}
			// else: orphaned tool_result — skip it

		default:
			result = append(result, msg)
		}
	}

	return result
}

const extendedContextSuffix = "[1m]"

func resolveModelOpts(model string) (string, []option.RequestOption) {
	if base, ok := strings.CutSuffix(model, extendedContextSuffix); ok {
		return base, []option.RequestOption{option.WithHeader("anthropic-beta", "context-1m-2025-08-07")}
	}
	return model, nil
}

func anthropicThinkingBudget(model, effort string) int {
	if !supportsThinkingModel(model) {
		return 0
	}
	switch effort {
	case ThinkingNormal:
		return 5000
	case ThinkingHigh:
		return 32000
	case ThinkingUltra:
		return 128000
	default:
		return 0
	}
}

// mergeConsecutiveMessages combines consecutive messages with the same role
// into single messages with merged content blocks. This is required by the
// Claude API when multiple tool results follow a single assistant message
// with multiple tool_use blocks.
func mergeConsecutiveMessages(msgs []anthropic.MessageParam) []anthropic.MessageParam {
	if len(msgs) <= 1 {
		return msgs
	}
	merged := make([]anthropic.MessageParam, 0, len(msgs))
	merged = append(merged, msgs[0])
	for i := 1; i < len(msgs); i++ {
		last := &merged[len(merged)-1]
		if msgs[i].Role == last.Role {
			last.Content = append(last.Content, msgs[i].Content...)
		} else {
			merged = append(merged, msgs[i])
		}
	}
	return merged
}

// convertAnthropicTools converts generic llm.ToolSchema definitions to the Anthropic SDK format.
// The JSON Schema "required" field may arrive as []string or []any (from JSON decoding);
// anyStrings normalises both forms.
func convertAnthropicTools(tools []llm.ToolSchema) []anthropic.ToolUnionParam {
	result := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{}
		if props, ok := t.Parameters.(map[string]any); ok {
			if properties, ok := props["properties"]; ok {
				schema.Properties = properties
			}
			schema.Required = anyStrings(props["required"])
			schema.ExtraFields = toolSchemaExtraFields(props)
		}
		result = append(result, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: schema,
			},
		})
	}
	return result
}

func toolSchemaExtraFields(schema map[string]any) map[string]any {
	extras := make(map[string]any)
	for k, v := range schema {
		switch k {
		case "type", "properties", "required":
			continue
		default:
			extras[k] = v
		}
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}

// anyStrings converts a "required" JSON Schema value to []string.
// It accepts []string (typed) or []any (JSON-decoded) and ignores other types.
func anyStrings(v any) []string {
	switch r := v.(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, item := range r {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// assistantContentBlocks builds the thinking + text content blocks for an assistant core.
func assistantContentBlocks(msg core.Message, thinkingBudget int64) []anthropic.ContentBlockParamUnion {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, 2)
	if msg.Thinking != "" && thinkingBudget > 0 && msg.ThinkingSignature != "" {
		blocks = append(blocks, anthropic.NewThinkingBlock(msg.ThinkingSignature, msg.Thinking))
	}
	if msg.Content != "" {
		blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
	}
	return blocks
}

// Ensure Client implements Provider
var _ llm.Provider = (*Client)(nil)
