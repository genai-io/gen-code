package openaicompat

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	streamutil "github.com/genai-io/san/internal/llm/stream"
	"github.com/genai-io/san/internal/log"
)

// ChatStreamConfig contains provider-specific knobs for OpenAI-compatible
// Chat Completions streaming.
type ChatStreamConfig struct {
	Client           openai.Client
	ProviderName     string
	Options          llm.CompletionOptions
	ConvertAssistant func(core.Message) openai.ChatCompletionMessageParamUnion
	ConfigureParams  func(*openai.ChatCompletionNewParams)
	ExtractReasoning bool

	// RequestOptions apply to this request only — for headers a provider has to
	// vary per turn rather than fix on the client (e.g. Copilot's X-Initiator).
	RequestOptions []option.RequestOption
}

// StreamChatCompletions streams an OpenAI-compatible Chat Completions request.
func StreamChatCompletions(ctx context.Context, cfg ChatStreamConfig) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk)

	go func() {
		defer close(ch)

		opts := cfg.Options
		messages := ConvertMessages(opts.Messages, opts.SystemPrompt, cfg.ConvertAssistant)

		params := openai.ChatCompletionNewParams{
			Model:    opts.Model,
			Messages: messages,
			StreamOptions: openai.ChatCompletionStreamOptionsParam{
				IncludeUsage: openai.Bool(true),
			},
		}
		if opts.MaxTokens > 0 {
			params.MaxCompletionTokens = openai.Int(int64(opts.MaxTokens))
		}
		if opts.Temperature > 0 {
			params.Temperature = openai.Float(opts.Temperature)
		}
		if len(opts.Tools) > 0 {
			params.Tools = ConvertTools(opts.Tools)
		}
		if cfg.ConfigureParams != nil {
			cfg.ConfigureParams(&params)
		}

		log.LogRequestCtx(ctx, cfg.ProviderName, opts.Model, opts)

		stream := cfg.Client.Chat.Completions.NewStreaming(ctx, params, cfg.RequestOptions...)
		state := streamutil.NewState(cfg.ProviderName)
		toolCalls := make(map[int]*core.ToolCall)

		for stream.Next() {
			chunk := stream.Current()
			state.Count()

			for _, choice := range chunk.Choices {
				if cfg.ExtractReasoning {
					if content := ExtractReasoningContent(choice.Delta.RawJSON()); content != "" {
						state.EmitThinking(ctx, ch, content)
					}
				}
				if choice.Delta.Content != "" {
					state.EmitText(ctx, ch, choice.Delta.Content)
				}

				for _, tc := range choice.Delta.ToolCalls {
					idx := int(tc.Index)
					if _, exists := toolCalls[idx]; !exists {
						toolCalls[idx] = &core.ToolCall{ID: tc.ID, Name: tc.Function.Name}
					}
					if tc.Function.Arguments != "" {
						toolCalls[idx].Input += tc.Function.Arguments
					}
				}

				if choice.FinishReason != "" {
					state.Response.StopReason = MapFinishReason(choice.FinishReason)
				}
			}

			// prompt_tokens is the full prompt; the cached slice lives under
			// prompt_tokens_details. Split into the Anthropic fresh/cache-read
			// convention the app assumes — see SplitInputTokens.
			fresh, cached := SplitInputTokens(int(chunk.Usage.PromptTokens), int(chunk.Usage.PromptTokensDetails.CachedTokens))
			state.UpdateUsage(fresh, int(chunk.Usage.CompletionTokens))
			state.UpdateCacheUsage(0, cached)
		}

		if err := stream.Err(); err != nil {
			state.Fail(ctx, ch, NormalizeAPIError(cfg.ProviderName, err))
			return
		}

		state.AddToolCallsSorted(toolCalls)
		state.EnsureToolUseStopReason()
		state.Finish(ctx, ch)
	}()

	return ch
}
