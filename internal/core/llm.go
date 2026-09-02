package core

import (
	"fmt"
	"strings"
)

// StopReason describes why the LLM stopped generating.
type StopReason string

const (
	StopEndTurn                    StopReason = "end_turn"
	StopMaxTokens                  StopReason = "max_tokens"
	StopToolUse                    StopReason = "tool_use"
	StopMaxSteps                   StopReason = "max_steps"
	StopCancelled                  StopReason = "cancelled"
	StopHook                       StopReason = "stop_hook"
	StopMaxOutputRecoveryExhausted StopReason = "max_output_recovery_exhausted"
	// StopError marks a turn that died on an inference failure — the retry
	// budget ran out, or the error was not retryable. The turn still produced
	// messages and token usage, so it reports an outcome like any other stop
	// reason rather than vanishing. StopDetail carries the underlying error.
	StopError StopReason = "error"
)

// Usage is the token accounting for one LLM call. Field names use the project's
// domain vocabulary; the json tags preserve each provider's wire format (e.g.
// Anthropic's cache_creation_input_tokens). It lives in core, the foundation
// layer, so both core.InferResponse and the llm provider response share one
// definition (llm.Usage aliases this).
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// InferResponse is the final aggregated response from one LLM call.
type InferResponse struct {
	Content           string          // text output
	Thinking          string          // chain-of-thought (extended thinking)
	ThinkingSignature string          // signature for replaying thinking blocks
	Reasoning         []ReasoningItem // reasoning blocks to echo back (OpenAI stateless backend)
	ToolCalls         []ToolCall      // tool execution requests
	StopReason        StopReason
	Usage
}

// TotalInputTokens is the full prompt size the model processed: fresh input
// plus the cached portion (e.g. Anthropic reports the cache-marked system
// prompt under CacheRead/CacheCreation, not InputTokens). This is the figure
// that reflects context-window occupancy; InputTokens alone undercounts it
// whenever prompt caching is active.
func (r InferResponse) TotalInputTokens() int {
	return r.InputTokens + r.CacheCreationInputTokens + r.CacheReadInputTokens
}

// Logging accessors — satisfy the duck-typed responseLoggable interface in the
// log package so log depends on neither core nor llm (foundation-layer
// contract). Defined here because llm.CompletionResponse aliases this type.
func (r InferResponse) LogStopReason() string { return string(r.StopReason) }
func (r InferResponse) LogContent() string    { return r.Content }
func (r InferResponse) LogThinking() string   { return r.Thinking }
func (r InferResponse) LogInputTokens() int   { return r.InputTokens }
func (r InferResponse) LogOutputTokens() int  { return r.OutputTokens }
func (r InferResponse) LogRawToolCalls() any  { return r.ToolCalls }
func (r InferResponse) LogRawUsage() any      { return r.Usage }

func (r InferResponse) LogToolCallSummary(escaper func(string) string) string {
	if len(r.ToolCalls) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "    ToolCalls(%d):\n", len(r.ToolCalls))
	for _, tc := range r.ToolCalls {
		fmt.Fprintf(&sb, "      [%s] %s(%s)\n", tc.ID, tc.Name, escaper(tc.Input))
	}
	return sb.String()
}
