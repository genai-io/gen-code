// Handler logic for core.Agent outbox events.
package conv

import (
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/tool"
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
	log.QueueLog("handleAgentEvent: %s", ev.Type)
	switch ev.Type {
	case core.OnTurn:
		result, _ := ev.Result()
		m.Stream.Stop()
		m.Tool.ClearPending()
		return rt.OnTurnEnd(result)
	case core.OnStop:
		err, _ := ev.Error()
		m.Stream.Stop()
		m.Tool.ClearPending()
		return rt.OnAgentStop(err)
	case core.OnCompact:
		info, _ := ev.CompactInfo()
		return rt.OnCompacted(info)
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
		log.QueueLog("handleAgentEventBatch: %s", ev.Type)
		switch ev.Type {
		case core.OnTurn:
			result, _ := ev.Result()
			m.Stream.Stop()
			m.Tool.ClearPending()
			effects = append(effects, rt.OnTurnEnd(result))
			needsContinue = false
		case core.OnStop:
			err, _ := ev.Error()
			m.Stream.Stop()
			m.Tool.ClearPending()
			effects = append(effects, rt.OnAgentStop(err))
			needsContinue = false
		case core.OnCompact:
			info, _ := ev.CompactInfo()
			effects = append(effects, rt.OnCompacted(info))
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
	switch ev.Type {
	case core.OnStart:
		return nil
	case core.OnCompactStart:
		cs, _ := ev.CompactStart()
		return rt.OnCompactStart(cs.Count)
	case core.OnMessage:
		// Nothing to do: every path that hands a user message to the agent —
		// idle submit, queue release, cron prompt, async hook, a notice
		// delivered mid-turn — appends it to the conversation at the call site,
		// so acting on the agent's echo here would double-display it.
		return nil
	case core.PreInfer:
		return applyPreInfer(rt, m)
	case core.OnChunk:
		return applyChunk(rt, m, ev)
	case core.OnStreamReset:
		// Transient failure about to be retried: drop the partial assistant
		// row and stop streaming so the retry's PreInfer starts a clean one.
		m.Stream.Stop()
		m.DropStreamingAssistant()
		return nil
	case core.PostInfer:
		return applyPostInfer(rt, m, ev)
	case core.PreTool:
		applyPreTool(m, ev)
		return nil
	case core.PostTool:
		return applyPostTool(rt, m, ev)
	default:
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
	m.Append(core.ChatMessage{Role: core.RoleAssistant, Content: ""})
	cmds := append(commitCmds, m.Spinner.Tick)
	return tea.Batch(cmds...)
}

func applyChunk(rt Runtime, m *Model, ev core.Event) tea.Cmd {
	chunk, ok := ev.Chunk()
	if !ok {
		return nil
	}
	// Late chunks after handleStreamCancel has flipped Stream off and
	// appended the [Interrupted] marker would otherwise call AppendToLast
	// and bleed text past the marker. RenderAssistantMessage's suffix
	// strip then fails and a literal "[Interrupted]" renders inline.
	if !m.Stream.Active {
		return nil
	}
	if chunk.Text != "" || chunk.Thinking != "" {
		m.AppendToLast(chunk.Text, chunk.Thinking)
	}
	// Final chunk of a text-only turn: commit the streaming message's remaining
	// tail (its completed blocks are already in scrollback) in a single Println.
	if chunk.Done && chunk.Response != nil && len(chunk.Response.ToolCalls) == 0 {
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

func applyPostInfer(rt Runtime, m *Model, ev core.Event) tea.Cmd {
	resp, ok := ev.Response()
	if !ok {
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
	if resp.ThinkingSignature != "" {
		m.SetLastThinkingSignature(resp.ThinkingSignature)
	}
	if len(resp.ToolCalls) > 0 {
		m.SetLastToolCalls(resp.ToolCalls)
		m.Tool.Track(resp.ToolCalls)
	}
	m.Stream.BuildingTool = ""
	return nil
}

func applyPreTool(m *Model, ev core.Event) {
	if tc, ok := ev.ToolCall(); ok {
		m.Stream.BuildingTool = tc.Name
		m.Tool.MarkCurrent(tc.ID)
		m.Tool.MarkStarted(tc.ID)
	}
}

func applyPostTool(rt Runtime, m *Model, ev core.Event) tea.Cmd {
	tr, ok := ev.ToolResult()
	if !ok {
		return nil
	}
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
	result := rt.OnToolResult(tr)
	m.Append(core.ChatMessage{
		Role:       core.RoleUser,
		ToolResult: result,
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
