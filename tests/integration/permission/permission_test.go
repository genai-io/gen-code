package permission_test

import (
	"context"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/tests/integration/testutil"
)

func TestPermission_PermitAll_AllowsWrite(t *testing.T) {
	testutil.RegisterFakeTool(t, "Write", "written successfully")

	ag, _ := testutil.NewTestAgentWithPermission(t, testutil.PermitAllPermission(),
		testutil.ToolCallResponse("Write", "tc1", `{"file_path": "/tmp/test"}`),
		testutil.EndTurnResponse("done"),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "write a file")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	for _, m := range result.Messages {
		if len(m.ToolResults()) > 0 && m.ToolResults()[0].IsError {
			t.Errorf("unexpected error result: %s", m.ToolResults()[0].Content.Text())
		}
	}
	if result.StopReason != core.StopEndTurn {
		t.Errorf("expected 'end_turn', got %q", result.StopReason)
	}
}

func TestPermission_ReadOnly_BlocksWrite(t *testing.T) {
	testutil.RegisterFakeTool(t, "Write", "should not execute")

	ag, _ := testutil.NewTestAgentWithPermission(t, testutil.ReadOnlyPermission(),
		testutil.ToolCallResponse("Write", "tc1", `{"file_path": "/tmp/test"}`),
		testutil.EndTurnResponse("ok"),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "write")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	hasError := false
	for _, m := range result.Messages {
		if m.ToolResults() != nil && m.ToolResults()[0].IsError {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("expected error result for Write tool in ReadOnly mode")
	}
}

func TestPermission_ReadOnly_AllowsRead(t *testing.T) {
	testutil.RegisterFakeTool(t, "Read", "file contents")

	ag, _ := testutil.NewTestAgentWithPermission(t, testutil.ReadOnlyPermission(),
		testutil.ToolCallResponse("Read", "tc1", `{"file_path": "/tmp/test"}`),
		testutil.EndTurnResponse("done"),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "read")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	for _, m := range result.Messages {
		if m.ToolResults() != nil && m.ToolResults()[0].IsError {
			t.Errorf("unexpected error for Read tool: %s", m.ToolResults()[0].Content.Text())
		}
	}
}

func TestPermission_DenyAll_BlocksNonSafeTools(t *testing.T) {
	testutil.RegisterFakeTool(t, "Bash", "should not execute")

	ag, _ := testutil.NewTestAgentWithPermission(t, testutil.DenyAllPermission(),
		testutil.ToolCallResponse("Bash", "tc1", `{"command":"echo hi"}`),
		testutil.EndTurnResponse("done"),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "run a command")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	hasError := false
	for _, m := range result.Messages {
		if m.ToolResults() != nil && m.ToolResults()[0].IsError {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("expected error result for Bash tool in DenyAll mode")
	}
}

func TestPermission_SafeToolGoesThroughPermission(t *testing.T) {
	testutil.RegisterFakeTool(t, "Read", "file contents")

	ag, _ := testutil.NewTestAgentWithPermission(t, testutil.DenyAllPermission(),
		testutil.ToolCallResponse("Read", "tc1", `{}`),
		testutil.EndTurnResponse("done"),
	)

	result, err := testutil.RunAgent(context.Background(), ag, "read")
	if err != nil {
		t.Fatalf("RunAgent() error: %v", err)
	}

	hasError := false
	for _, m := range result.Messages {
		if m.ToolResults() != nil && m.ToolResults()[0].IsError {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("expected safe tool Read to be denied by DenyAll")
	}
}
