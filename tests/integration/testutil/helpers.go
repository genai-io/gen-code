// Package testutil provides shared test helpers for integration tests.
package testutil

import (
	"context"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// ---------------------------------------------------------------------------
// Client helpers
// ---------------------------------------------------------------------------

// NewTestClient fixes a model on a FakeProvider, ready for use in loops or
// compact calls.
func NewTestClient(fake *FakeProvider) *llm.Client {
	return llm.NewClient(fake, "fake-model", 8192)
}

// ---------------------------------------------------------------------------
// Response builders
// ---------------------------------------------------------------------------

// ToolCallResponse builds a CompletionResponse that triggers a single tool_use.
func ToolCallResponse(toolName, toolID, input string) llm.CompletionResponse {
	return llm.CompletionResponse{
		StopReason: "tool_use",
		ToolCalls:  []core.ToolCall{{ID: toolID, Name: toolName, Input: input}},
		Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5},
	}
}

// MultiToolCallResponse builds a CompletionResponse with multiple tool calls.
func MultiToolCallResponse(calls ...core.ToolCall) llm.CompletionResponse {
	return llm.CompletionResponse{
		StopReason: "tool_use",
		ToolCalls:  calls,
		Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5},
	}
}

// EndTurnResponse builds a simple end_turn response with default usage.
func EndTurnResponse(content string) llm.CompletionResponse {
	return llm.CompletionResponse{
		Content:    content,
		StopReason: "end_turn",
		Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5},
	}
}

// EndTurnResponseWithUsage builds an end_turn response with custom token counts.
func EndTurnResponseWithUsage(content string, input, output int) llm.CompletionResponse {
	return llm.CompletionResponse{
		Content:    content,
		StopReason: "end_turn",
		Usage:      llm.Usage{InputTokens: input, OutputTokens: output},
	}
}

// ---------------------------------------------------------------------------
// Fake tool registration
// ---------------------------------------------------------------------------

// RegisterFakeTool registers a named tool in the global registry that returns
// a fixed result. The global registry is reset via t.Cleanup.
func RegisterFakeTool(t *testing.T, name, result string) {
	t.Helper()
	tool.Register(&fakeTool{name: name, result: result})
	t.Cleanup(func() {
		tool.Unregister(name)
		tool.ResetDefaultRegistry()
	})
}

type fakeTool struct {
	name   string
	result string
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return "fake tool for testing" }
func (f *fakeTool) Icon() string        { return "T" }
func (f *fakeTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name:        f.name,
		Description: "fake tool for testing",
		Definition:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}
func (f *fakeTool) Execute(_ context.Context, _ map[string]any, _ string) toolresult.ToolResult {
	return toolresult.ToolResult{
		Success:  true,
		Output:   f.result,
		Metadata: toolresult.ResultMetadata{Title: f.name},
	}
}

// ---------------------------------------------------------------------------
// Fake / mock providers
// ---------------------------------------------------------------------------
