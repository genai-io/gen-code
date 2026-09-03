package loop_test

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/tests/integration/testutil"
)

func TestAgent_SingleTurn_EndTurn(t *testing.T) {
	ag, _ := testutil.NewTestAgent(t,
		testutil.EndTurnResponse("hello world"),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "hi")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	if result.StopReason != core.StopEndTurn {
		t.Errorf("expected stop reason 'end_turn', got %q", result.StopReason)
	}
	if result.Content != "hello world" {
		t.Errorf("expected content 'hello world', got %q", result.Content)
	}
	if result.Steps != 1 {
		t.Errorf("expected 1 step, got %d", result.Steps)
	}
	if result.InputTokens == 0 {
		t.Error("expected non-zero input tokens")
	}
}

func TestAgent_MultiTurn_ToolUse(t *testing.T) {
	testutil.RegisterFakeTool(t, "MyTool", "tool output")

	ag, _ := testutil.NewTestAgent(t,
		testutil.ToolCallResponse("MyTool", "tc1", `{}`),
		testutil.EndTurnResponse("done after tool"),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "use tool")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	if result.Steps != 2 {
		t.Errorf("expected 2 steps, got %d", result.Steps)
	}
	if result.StopReason != core.StopEndTurn {
		t.Errorf("expected 'end_turn', got %q", result.StopReason)
	}

	// Verify messages contain tool call and result
	msgs := result.Messages
	hasToolCall := false
	hasToolResult := false
	for _, m := range msgs {
		if m.Role == ai.RoleAssistant && len(m.ToolCalls) > 0 {
			hasToolCall = true
		}
		if m.ToolResult != nil {
			hasToolResult = true
		}
	}
	if !hasToolCall {
		t.Error("expected tool call in messages")
	}
	if !hasToolResult {
		t.Error("expected tool result in messages")
	}
}

func TestAgent_MaxSteps(t *testing.T) {
	testutil.RegisterFakeTool(t, "AlwaysTool", "ok")

	responses := make([]llm.CompletionResponse, 10)
	for i := range responses {
		responses[i] = testutil.ToolCallResponse("AlwaysTool", "tc", `{}`)
	}

	ag, _ := testutil.NewTestAgentWithMaxSteps(t, 3, responses...)

	result, err := testutil.RunAgent(context.Background(), ag, "go")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	if result.StopReason != core.StopMaxSteps {
		t.Errorf("expected 'max_steps', got %q", result.StopReason)
	}
}

func TestAgent_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	ag, _ := testutil.NewTestAgent(t,
		testutil.EndTurnResponse("should not reach"),
	)

	_, err := testutil.RunAgent(ctx, ag, "hello")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestAgent_UnknownTool(t *testing.T) {
	ag, _ := testutil.NewTestAgent(t,
		testutil.ToolCallResponse("NonExistent", "tc1", `{}`),
		testutil.EndTurnResponse("recovered"),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "call unknown")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	if result.StopReason != core.StopEndTurn {
		t.Errorf("expected 'end_turn', got %q", result.StopReason)
	}

	hasError := false
	for _, m := range result.Messages {
		if m.ToolResult != nil && m.ToolResult.IsError {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("expected error tool result for unknown tool")
	}
}

func TestAgent_MultipleToolCalls(t *testing.T) {
	testutil.RegisterFakeTool(t, "ToolA", "result A")
	testutil.RegisterFakeTool(t, "ToolB", "result B")

	ag, _ := testutil.NewTestAgent(t,
		testutil.MultiToolCallResponse(
			core.ToolCall{ID: "tc1", Name: "ToolA", Input: `{}`},
			core.ToolCall{ID: "tc2", Name: "ToolB", Input: `{}`},
		),
		testutil.EndTurnResponse("both done"),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "use both")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	toolResults := 0
	for _, m := range result.Messages {
		if m.ToolResult != nil && !m.ToolResult.IsError {
			toolResults++
		}
	}
	if toolResults != 2 {
		t.Errorf("expected 2 tool results, got %d", toolResults)
	}
}

func TestAgent_TokenAccumulation(t *testing.T) {
	testutil.RegisterFakeTool(t, "Tick", "ok")

	ag, _ := testutil.NewTestAgent(t,
		testutil.ToolCallResponse("Tick", "tc1", `{}`),
		testutil.ToolCallResponse("Tick", "tc2", `{}`),
		testutil.EndTurnResponseWithUsage("done", 20, 10),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "go")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	if result.Steps != 3 {
		t.Errorf("expected 3 steps, got %d", result.Steps)
	}

	// Each of the first 2 responses has 10+5 usage, third has 20+10
	// Total: 10+10+20=40 input, 5+5+10=20 output
	if result.InputTokens != 40 {
		t.Errorf("expected 40 input tokens, got %d", result.InputTokens)
	}
	if result.OutputTokens != 20 {
		t.Errorf("expected 20 output tokens, got %d", result.OutputTokens)
	}
}
