package conv

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/mcp"
	coretool "github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// --- Tool state ---

type ToolExecState struct {
	PendingCalls []core.ToolCall
	CurrentIdx   int
	// StartedAt stamps when each call began executing (its PreToolEvent),
	// keyed by tool-call ID. The view reads it to show a live elapsed timer
	// on a running row, so a long command no longer looks stuck. Only
	// in-flight calls appear; it is cleared with every new batch.
	StartedAt map[string]time.Time
	// Progress holds the latest output counter of a running command ("1.2k
	// lines"), keyed by tool-call ID — the replaceable companion to the
	// elapsed timer, showing output is actually flowing. Cleared with StartedAt.
	Progress map[string]string
	// AwaitingApprovalID is the tool call parked on the permission modal. Its
	// PreToolEvent already stamped it as started, so without this the row would
	// spin and tick as if the call had been allowed to run. Empty while no
	// permission request is open.
	AwaitingApprovalID string
	Ctx                context.Context
	Cancel             context.CancelFunc
}

func (t *ToolExecState) Begin() context.Context {
	if t.Cancel != nil {
		t.Cancel()
	}
	t.Ctx, t.Cancel = context.WithCancel(context.Background())
	return t.Ctx
}

func (t *ToolExecState) Context() context.Context {
	if t.Ctx != nil {
		return t.Ctx
	}
	return context.Background()
}

func (t *ToolExecState) Reset() {
	if t.Cancel != nil {
		t.Cancel()
	}
	t.ClearPending()
	t.StartedAt = nil
	t.Progress = nil
	t.AwaitingApprovalID = ""
	t.Ctx = nil
	t.Cancel = nil
}

func (t *ToolExecState) Track(calls []core.ToolCall) {
	t.StartedAt = nil
	t.Progress = nil
	t.AwaitingApprovalID = ""
	if len(calls) == 0 {
		t.ClearPending()
		return
	}
	t.PendingCalls = append([]core.ToolCall(nil), calls...)
	t.CurrentIdx = 0
}

func (t *ToolExecState) MarkCurrent(toolCallID string) {
	for i, tc := range t.PendingCalls {
		if tc.ID == toolCallID {
			t.CurrentIdx = i
			return
		}
	}
}

// MarkStarted stamps the moment a tool call began executing, so the view can
// show how long it has been running. Called on its PreToolEvent, and again
// when a permission prompt is approved — the first stamp then covers only how
// long the user took to answer, which is not what the row is reporting.
func (t *ToolExecState) MarkStarted(toolCallID string) {
	if toolCallID == "" {
		return
	}
	if t.StartedAt == nil {
		t.StartedAt = make(map[string]time.Time)
	}
	t.StartedAt[toolCallID] = time.Now()
}

// MarkAwaitingApproval records the call whose permission request is open, so
// its row reports waiting rather than running. Only one request is on screen
// at a time; a later one replaces it.
func (t *ToolExecState) MarkAwaitingApproval(toolCallID string) {
	t.AwaitingApprovalID = toolCallID
}

// ClearAwaitingApproval drops the waiting state once the request is answered.
func (t *ToolExecState) ClearAwaitingApproval() {
	t.AwaitingApprovalID = ""
}

// SetProgress records the latest output counter for a running tool call. A blank
// text clears it (nothing to show yet).
func (t *ToolExecState) SetProgress(toolCallID, text string) {
	if text == "" {
		delete(t.Progress, toolCallID)
		return
	}
	if t.Progress == nil {
		t.Progress = make(map[string]string)
	}
	t.Progress[toolCallID] = text
}

func (t *ToolExecState) IndexOf(toolCallID string) int {
	for i, tc := range t.PendingCalls {
		if tc.ID == toolCallID {
			return i
		}
	}
	return -1
}

func (t *ToolExecState) MarkComplete(toolCallID string) bool {
	completedIdx := -1
	for i, tc := range t.PendingCalls {
		if tc.ID == toolCallID {
			completedIdx = i
			break
		}
	}
	if completedIdx == -1 {
		return false
	}

	batchComplete := completedIdx >= len(t.PendingCalls)-1
	if batchComplete {
		t.ClearPending()
		return true
	}
	t.CurrentIdx = completedIdx + 1
	return false
}

func (t *ToolExecState) ClearPending() {
	t.PendingCalls = nil
	t.CurrentIdx = 0
}

// DrainPendingCalls cancels the current context and returns any remaining
// pending tool calls (from CurrentIdx onward), then resets state.
func (t *ToolExecState) DrainPendingCalls() []core.ToolCall {
	if t.Cancel != nil {
		t.Cancel()
	}
	if t.PendingCalls == nil || t.CurrentIdx >= len(t.PendingCalls) {
		return nil
	}
	calls := t.PendingCalls[t.CurrentIdx:]
	t.Reset()
	return calls
}

// --- Tool execution dispatching ---

type DefaultMCPExecutor struct {
	caller *mcp.Caller
}

func NewMCPExecutor(caller *mcp.Caller) DefaultMCPExecutor {
	return DefaultMCPExecutor{caller: caller}
}

func (e DefaultMCPExecutor) IsMCPTool(name string) bool {
	return mcp.IsMCPTool(name)
}

func (e DefaultMCPExecutor) ExecuteMCP(ctx context.Context, name string, params map[string]any) (toolresult.ToolResult, error) {
	if e.caller == nil {
		return toolresult.NewErrorResult(name, "MCP not initialized"), nil
	}
	output, isError, err := e.caller.CallTool(ctx, name, params)
	if err != nil {
		return toolresult.NewErrorResult(name, err.Error()), nil
	}
	return toolresult.ToolResult{
		Success:  !isError,
		Output:   output,
		Metadata: toolresult.ResultMetadata{Title: name, Icon: "plugin"},
	}, nil
}

type ExecResultMsg struct {
	Index    int
	Result   core.ToolResult
	ToolName string
}

func newExecResult(tc core.ToolCall, index int, content string, isError bool) ExecResultMsg {
	return ExecResultMsg{
		Index:    index,
		Result:   core.ToolResult{ToolCallID: tc.ID, Content: content, IsError: isError},
		ToolName: tc.Name,
	}
}

func newExecResultFromOutput(tc core.ToolCall, index int, output toolresult.ToolResult) ExecResultMsg {
	return ExecResultMsg{
		Index:    index,
		Result:   core.ToolResult{ToolCallID: tc.ID, Content: output.FormatForLLM(), IsError: !output.Success, HookResponse: output.HookResponse, Details: output.Details},
		ToolName: tc.Name,
	}
}

func ExecuteApproved(ctx context.Context, agentUI *AgentToUI, toolCalls []core.ToolCall, idx int, cwd string, mcpExec ...coretool.MCPExecutor) tea.Cmd {
	if idx >= len(toolCalls) {
		return nil
	}

	tc := toolCalls[idx]
	var executor coretool.MCPExecutor
	if len(mcpExec) > 0 && mcpExec[0] != nil {
		executor = mcpExec[0]
	} else {
		executor = NewMCPExecutor(nil)
	}

	return func() tea.Msg {
		if ctx == nil {
			ctx = context.Background()
		}

		prepared, err := coretool.PrepareToolCall(tc, executor)
		if err != nil {
			errMsg := "Error parsing tool input: " + err.Error()
			if strings.HasPrefix(err.Error(), "unknown tool: ") {
				errMsg = "Unknown tool: " + strings.TrimPrefix(err.Error(), "unknown tool: ")
			}
			return newExecResult(tc, idx, errMsg, true)
		}

		attachExecAgentCallbacks(ctx, agentUI, idx, prepared)

		start := time.Now()
		result, err := prepared.Execute(ctx, cwd, true, executor)
		if err != nil {
			if executor != nil && executor.IsMCPTool(tc.Name) {
				return newExecResult(tc, idx, "Internal error: "+err.Error(), true)
			}
			return newExecResult(tc, idx, "Internal error: unknown tool: "+tc.Name, true)
		}
		log.LogTool(tc.Name, tc.ID, time.Since(start).Milliseconds(), result.Success)
		return newExecResultFromOutput(tc, idx, result)
	}
}

func attachExecAgentCallbacks(ctx context.Context, agentUI *AgentToUI, idx int, prepared *coretool.PreparedToolCall) {
	if !coretool.IsAgentToolName(prepared.Call.Name) {
		return
	}

	prepared.Params["_onActivity"] = coretool.ActivityFunc(func(msg string) {
		if agentUI != nil {
			agentUI.SendForAgent(idx, msg)
		}
	})
	prepared.Params["_onQuestion"] = coretool.AskQuestionFunc(func(qctx context.Context, req *coretool.QuestionRequest) (*coretool.QuestionResponse, error) {
		if qctx == nil {
			qctx = ctx
		}
		return askExecAgentQuestion(qctx, agentUI, idx, req)
	})

	if getter := coretool.GetMessagesGetter(ctx); getter != nil {
		prepared.Params["_messagesGetter"] = getter
	}
}

func askExecAgentQuestion(ctx context.Context, agentUI *AgentToUI, idx int, req *coretool.QuestionRequest) (*coretool.QuestionResponse, error) {
	if agentUI == nil {
		return nil, context.Canceled
	}
	return agentUI.Ask(ctx, idx, req)
}
