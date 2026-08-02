package conv

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/core"
)

type postToolRuntime struct {
	Runtime
	drainCalls int
}

func (r *postToolRuntime) OnToolResult(tr core.ToolResult) *core.ToolResult { return &tr }
func (r *postToolRuntime) TakeReviewDecision(string) *core.ReviewDecision   { return nil }
func (r *postToolRuntime) OnStepEnd() tea.Cmd {
	r.drainCalls++
	return nil
}

func TestPostToolDrainsQueuedInputAfterEntireToolBatch(t *testing.T) {
	m := NewModel(80)
	m.Tool.Track([]core.ToolCall{{ID: "tc-1", Name: "Read"}, {ID: "tc-2", Name: "Bash"}})
	rt := &postToolRuntime{}

	applyPostTool(rt, &m, core.PostToolEvent(core.ToolResult{ToolCallID: "tc-1", ToolName: "Read"}))
	if rt.drainCalls != 0 {
		t.Fatalf("drained pending input after first tool result; calls = %d", rt.drainCalls)
	}
	applyPostTool(rt, &m, core.PostToolEvent(core.ToolResult{ToolCallID: "tc-2", ToolName: "Bash"}))
	if rt.drainCalls != 1 {
		t.Fatalf("drain calls after complete tool batch = %d, want 1", rt.drainCalls)
	}
}

func TestHandleActivityWithoutAgentToUIDoesNotPanic(t *testing.T) {
	m := OutputModel{Spinner: newFrameClock(), MDRenderer: NewMDRenderer(80)}

	cmd := m.HandleActivity(AgentActivityMsg{
		Index:   1,
		Message: "step",
	})
	if cmd == nil {
		t.Fatal("expected spinner cmd even without an agent-to-UI channel")
	}
	if len(m.TaskActivity[1]) != 1 || m.TaskActivity[1][0] != "step" {
		t.Fatalf("unexpected activity state: %#v", m.TaskActivity)
	}
}

func Test_drainActivityWithoutHubIsNoop(t *testing.T) {
	m := OutputModel{Spinner: newFrameClock(), MDRenderer: NewMDRenderer(80)}
	m.TaskActivity = map[int][]string{2: {"existing"}}

	m.drainActivity()

	if len(m.TaskActivity[2]) != 1 || m.TaskActivity[2][0] != "existing" {
		t.Fatalf("unexpected activity state after drain: %#v", m.TaskActivity)
	}
}

func TestMarkToolCallCompleteAdvancesAndClearsPendingState(t *testing.T) {
	state := ToolExecState{}
	state.Track([]core.ToolCall{
		{ID: "tc-1", Name: "WebFetch"},
		{ID: "tc-2", Name: "Grep"},
	})

	state.MarkCurrent("tc-1")
	if state.CurrentIdx != 0 {
		t.Fatalf("CurrentIdx = %d, want 0", state.CurrentIdx)
	}

	if complete := state.MarkComplete("tc-1"); complete {
		t.Fatal("first tool must not complete the batch")
	}
	if state.CurrentIdx != 1 {
		t.Fatalf("CurrentIdx = %d, want 1", state.CurrentIdx)
	}
	if len(state.PendingCalls) != 2 {
		t.Fatalf("PendingCalls length = %d, want 2", len(state.PendingCalls))
	}

	if complete := state.MarkComplete("tc-2"); !complete {
		t.Fatal("last tool must complete the batch")
	}
	if state.PendingCalls != nil {
		t.Fatalf("PendingCalls = %#v, want nil", state.PendingCalls)
	}
	if state.CurrentIdx != 0 {
		t.Fatalf("CurrentIdx = %d, want 0", state.CurrentIdx)
	}
}
