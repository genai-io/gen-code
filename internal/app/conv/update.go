// Handler logic for core.Agent outbox events.
package conv

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/tool"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
)

// Update routes all output-path messages: agent outbox, permission gate,
// compaction results, and activity updates.
func Update(rt Runtime, m *Model, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case AgentOutboxMsg:
		if msg.Closed && len(msg.Batch) == 0 {
			m.Stream.Stop()
			return rt.OnAgentStop(nil), true
		}
		if len(msg.Batch) > 0 {
			return handleAgentEventBatch(rt, m, msg.Batch, msg.Closed), true
		}
		return handleAgentEvent(rt, m, msg.Event), true
	case PermGateMsg:
		return rt.HandlePermGate(msg.Request), true
	case CompactResultMsg:
		return rt.HandleCompactResult(msg), true
	case kit.TokenLimitResultMsg:
		return rt.HandleTokenLimitResult(msg), true
	case AgentActivityMsg:
		if msg.Index < 0 && msg.ToolCallID != "" {
			msg.Index = m.Tool.IndexOf(msg.ToolCallID)
		}
		if msg.Index < 0 {
			return m.HandleActivityTick(rt.HasRunningTasks()), true
		}
		// A non-agent tool (Bash) reports a single live status — its output
		// counter — rather than a feed: store it as the row's replaceable
		// progress instead of appending it to the activity history.
		if msg.Index < len(m.Tool.PendingCalls) && !tool.IsAgentToolName(m.Tool.PendingCalls[msg.Index].Name) {
			m.Tool.SetProgress(msg.ToolCallID, msg.Message)
			return tea.Batch(m.Spinner.Tick, m.AgentToUI.Check()), true
		}
		return m.HandleActivity(msg), true
	case AgentToUITickMsg:
		return m.HandleActivityTick(rt.HasRunningTasks()), true
	default:
		return nil, false
	}
}

// --- Agent event dispatch ---

func handleAgentEvent(rt Runtime, m *Model, ev core.Event) tea.Cmd {
	log.QueueLog("handleAgentEvent: %T", ev)
	switch e := ev.(type) {
	case core.TurnEnded:
		m.Stream.Stop()
		m.Tool.ClearPending()
		return rt.OnTurnEnd(e.Result)
	case core.AgentStopped:
		m.Stream.Stop()
		m.Tool.ClearPending()
		return rt.OnAgentStop(e.Err)
	case core.Compacted:
		return rt.OnCompacted(e)
	default:
		// A single event emits at most one scrollback write (extra), and
		// ContinueOutbox never writes scrollback, so there's nothing to order —
		// tea.Batch is safe here (unlike the multi-effect batch handler, which
		// sequences). See handleAgentEventBatch.
		if extra := applyAgentEvent(rt, m, ev); extra != nil {
			return tea.Batch(extra, rt.ContinueOutbox())
		}
		return rt.ContinueOutbox()
	}
}

func handleAgentEventBatch(rt Runtime, m *Model, events []core.Event, closed bool) tea.Cmd {
	var effects []tea.Cmd
	needsContinue := true

	for _, ev := range events {
		log.QueueLog("handleAgentEventBatch: %T", ev)
		switch e := ev.(type) {
		case core.TurnEnded:
			m.Stream.Stop()
			m.Tool.ClearPending()
			effects = append(effects, rt.OnTurnEnd(e.Result))
			needsContinue = false
		case core.AgentStopped:
			m.Stream.Stop()
			m.Tool.ClearPending()
			effects = append(effects, rt.OnAgentStop(e.Err))
			needsContinue = false
		case core.Compacted:
			effects = append(effects, rt.OnCompacted(e))
			needsContinue = false
		default:
			if extra := applyAgentEvent(rt, m, ev); extra != nil {
				effects = append(effects, extra)
			}
			continue
		}
		break // terminal event — don't process further events in this batch
	}

	if closed {
		m.Stream.Stop()
		m.Tool.ClearPending()
		effects = append(effects, rt.OnAgentStop(nil))
		needsContinue = false
	}

	// Effects that write scrollback (commit/flush Println) must keep their
	// emission order. tea.Batch runs commands in concurrent goroutines with no
	// ordering guarantee, which can interleave progressively-committed blocks;
	// tea.Sequence runs them in order. The outbox drain stays concurrent — it
	// only reads the next event and never touches scrollback.
	var ordered tea.Cmd
	switch {
	case len(effects) == 1:
		ordered = effects[0]
	case len(effects) > 1:
		ordered = tea.Sequence(effects...)
	}
	if !needsContinue {
		return ordered
	}
	if ordered == nil {
		return rt.ContinueOutbox()
	}
	return tea.Batch(ordered, rt.ContinueOutbox())
}

// --- Event side-effect handlers (no ContinueOutbox) ---

func applyAgentEvent(rt Runtime, m *Model, ev core.Event) tea.Cmd {
	switch e := ev.(type) {
	case sdkagent.CompactionStart:
		return rt.OnCompactStart(len(e.Messages))
	case sdkagent.CompactionEnd:
		return rt.OnCompactEnd(e)
	case sdkagent.MessageStart:
		// A retry announces itself by the attempt number, not by an event of
		// its own: what was drawn for the previous attempt is void, and the
		// next one re-streams from scratch.
		if e.Attempt > 1 {
			m.Stream.Stop()
			m.DropStreamingAssistant()
		}
		return applyPreInfer(rt, m)
	case sdkagent.MessageUpdate:
		return applyChunk(rt, m, e.Delta)
	case sdkagent.MessageEnd:
		return applyPostInfer(rt, m, e.Response)
	case sdkagent.ToolStart:
		applyPreTool(m, core.ToolCall{ID: e.ID, Name: e.Name, Input: e.Args})
		return nil
	case sdkagent.ToolEnd:
		return applyPostTool(rt, m, core.ToolResult{
			ToolCallID: e.ID,
			ToolName:   e.Name,
			Content:    sdkagent.ResultContent(e.Result, e.Err),
			IsError:    e.Err != nil,
		})
	default:
		// AgentStarted, MessageReceived, and the loop's events the interface
		// has nothing to draw for. A message entering the inbox is already on
		// screen: every path that hands one to the agent appends it at the call
		// site, so acting on the echo would draw it twice.
		return nil
	}
}

func applyPreInfer(rt Runtime, m *Model) tea.Cmd {
	// A new inference means any auto-compaction is over: a successful one already
	// cleared this in OnCompacted, but a failed compaction falls straight through
	// to here, so clear the in-progress indicator either way.
	m.Compact.Clear()
	m.Stream.Active = true
	m.Stream.BuildingTool = ""
	commitCmds := rt.CommitMessages()
	m.Append(core.ChatMessage{Role: core.ChatAssistant, Content: ""})
	cmds := append(commitCmds, m.Spinner.Tick)
	return tea.Batch(cmds...)
}

func applyChunk(rt Runtime, m *Model, chunk ai.Event) tea.Cmd {
	// Late chunks after handleStreamCancel has flipped Stream off and
	// appended the [Interrupted] marker would otherwise call AppendToLast
	// and bleed text past the marker. RenderAssistantMessage's suffix
	// strip then fails and a literal "[Interrupted]" renders inline.
	if !m.Stream.Active {
		return nil
	}
	// The wire says which block a fragment belongs to, where San's own chunk
	// had two named fields and could carry nothing else.
	if chunk.Type == ai.EventBlockDelta && chunk.Block.Text != "" {
		switch chunk.Block.Type {
		case ai.BlockText:
			m.AppendToLast(chunk.Block.Text, "")
		case ai.BlockThinking:
			m.AppendToLast("", chunk.Block.Text)
		}
	}
	// Final chunk of a text-only turn: commit the streaming message's remaining
	// tail (its completed blocks are already in scrollback) in a single Println.
	if chunk.Type == ai.EventDone && chunk.Response != nil && len(chunk.Response.ToolCalls()) == 0 {
		m.Stream.Active = false
		if commitCmds := rt.CommitMessages(); len(commitCmds) > 0 {
			return tea.Batch(commitCmds...)
		}
		return nil
	}
	// Mid-stream: flush any blocks that just completed (thinking once text
	// starts, content at each fence-close / blank-line boundary) to scrollback.
	if flushCmds := rt.FlushStreamingBlocks(); len(flushCmds) > 0 {
		return tea.Batch(flushCmds...)
	}
	return nil
}

func applyPostInfer(rt Runtime, m *Model, resp *ai.Response) tea.Cmd {
	if resp == nil {
		return nil
	}
	rt.OnInference(resp)
	m.Compact.WarningSuppressed = false
	// No Stream.Active guard: SetLastThinkingSignature / SetLastToolCalls
	// already bail on non-assistant tails, which is the only way a late
	// PostInfer could corrupt conv state (after cancelPendingToolCalls
	// appended user-role rows). A guard on Stream.Active would also
	// suppress these setters for normal text-only completions, since
	// applyChunk flips Stream.Active=false on the Done chunk that arrives
	// just before this PostInfer — silently dropping ThinkingSignature.
	if sig := resp.Content.ThinkingSignature(); sig != "" {
		m.SetLastThinkingSignature(sig)
	}
	if calls := resp.Content.ToolCalls(); len(calls) > 0 {
		m.SetLastToolCalls(calls)
		m.Tool.Track(calls)
	}
	m.Stream.BuildingTool = ""
	return nil
}

func applyPreTool(m *Model, tc core.ToolCall) {
	m.Stream.BuildingTool = tc.Name
	m.Tool.MarkCurrent(tc.ID)
	m.Tool.MarkStarted(tc.ID)
}

func applyPostTool(rt Runtime, m *Model, tr core.ToolResult) tea.Cmd {
	m.Stream.BuildingTool = ""
	if tool.IsAgentToolName(tr.ToolName) {
		m.TaskActivity = nil
	}
	batchComplete := m.Tool.MarkComplete(tr.ToolCallID)
	// A tool that completed just before the user pressed Esc may have its
	// PostToolEvent still buffered in the outbox when handleStreamCancel
	// runs — cancelPendingToolCalls then writes a cancelled-result row for
	// the same ToolCallID. When the buffered event finally drains we'd
	// double-append. Skip if conv already carries a result for this call.
	if tr.ToolCallID != "" {
		for i := range m.Messages {
			if existing := m.Messages[i].ToolResult; existing != nil && existing.ToolCallID == tr.ToolCallID {
				return nil
			}
		}
	}
	result, details := rt.OnToolResult(tr)
	m.Append(core.ChatMessage{
		Role:        core.ChatUser,
		ToolResult:  result,
		ToolDetails: details,
		// Stamp the auto-review decision (if this call was judged) onto the
		// result message so it renders inline under the tool call. Consumed
		// here — the handoff map keeps only in-flight calls.
		Decision: rt.TakeReviewDecision(tr.ToolCallID),
	})
	// Release queued input only after every tool call in this response has
	// completed. Releasing after the first result inserts the user's pending
	// message between sibling tool rows; waiting preserves the visible order:
	// complete tool output first, then the pending message.
	if batchComplete {
		return rt.OnStepEnd()
	}
	return nil
}

// --- Activity handling (operates on output Model directly) ---

func (m *OutputModel) drainActivity() {
	if m.AgentToUI == nil {
		return
	}
	m.TaskActivity = m.AgentToUI.Drain(m.TaskActivity)
}

func (m *OutputModel) HandleActivity(msg AgentActivityMsg) tea.Cmd {
	if m.TaskActivity == nil {
		m.TaskActivity = make(map[int][]string)
	}
	m.TaskActivity[msg.Index] = append(m.TaskActivity[msg.Index], msg.Message)
	// Cap activity entries per agent to prevent unbounded growth
	if len(m.TaskActivity[msg.Index]) > maxAgentActivityHistory {
		m.TaskActivity[msg.Index] = m.TaskActivity[msg.Index][len(m.TaskActivity[msg.Index])-maxAgentActivityHistory:]
	}

	if m.AgentToUI == nil {
		return m.Spinner.Tick
	}
	return tea.Batch(m.Spinner.Tick, m.AgentToUI.Check())
}

func (m *OutputModel) HandleActivityTick(hasRunningTasks bool) tea.Cmd {
	if m.AgentToUI != nil {
		if hasRunningTasks {
			return tea.Batch(m.Spinner.Tick, m.AgentToUI.Check())
		}
		return m.AgentToUI.Check()
	}
	if hasRunningTasks {
		return m.Spinner.Tick
	}
	return nil
}
