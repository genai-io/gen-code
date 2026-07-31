// Turn-boundary inbox drain and prompt injection. After every agent turn
// ends we drain (in priority order) queued user messages, cron-fired
// prompts, async-hook continuations, and the main-loop notice buffer (broker
// messages routed to "main"). Each drained item is converted to a notice +
// optional re-send to the agent. Also handles the Stop hook result that gates
// session persistence.
package app

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/trigger"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/log"
)

const maxNoticesPerDrain = 8

func (m *model) handleStopHookResult(msg stopHookResultMsg) tea.Cmd {
	if msg.Blocked {
		log.QueueLog("handleStopHookResult: hooks BLOCKED reason=%q", msg.Reason)
		blockMsg := "Stop hook blocked: " + msg.Reason
		m.conv.Append(core.ChatMessage{Role: core.RoleUser, Content: blockMsg})
		return m.sendToAgent(blockMsg, nil)
	}
	log.QueueLog("handleStopHookResult: hooks done, persisting")
	var cmds []tea.Cmd
	if cmd := m.persistAfterTurn(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.startPromptSuggestion(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if msg.Result.StopReason != "" && msg.Result.StopReason != core.StopEndTurn {
		m.conv.AddNotice(fmt.Sprintf("Agent stopped: %s", msg.Result.StopReason))
		if msg.Result.StopDetail != "" {
			m.conv.AddNotice(msg.Result.StopDetail)
		}
	}
	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

// releaseQueuedMessage hands the next queued user message to the running agent
// and returns (cmd, true) when one was released, or (nil, false) when the queue
// is empty or its head is under edit (SelectIdx 0 — dispatching would send
// pre-edit text and orphan the user's changes). A text-only model receives the
// queued images' paths inlined into the content instead of the attachments.
//
// The message is shown in the conversation now, at release time, so it never
// vanishes between the queue and the flow; the agent addresses it once it ingests
// it a step or a turn later, and its ingest echo is a no-op (see OnAgentMessage).
// Shared by the step-boundary (DrainQueuedAtStep) and turn-boundary
// (drainTurnQueues) drains.
func (m *model) releaseQueuedMessage() (tea.Cmd, bool) {
	if m.userInput.Queue.SelectIdx == 0 {
		return nil, false
	}
	item, ok := m.userInput.Queue.Dequeue()
	if !ok {
		return nil, false
	}
	// Text-only models can't receive image parts; inline each image's path so
	// the model can decide how to use it (e.g. via an MCP tool).
	content, images := m.adaptImagesForModel(item.Content, item.Images)
	m.conv.Append(core.ChatMessage{Role: core.RoleUser, Content: content, Images: images})
	svc := m.services.Agent
	send := func() tea.Msg {
		svc.Send(content, images)
		return nil
	}
	return tea.Batch(append(m.CommitMessages(), send)...), true
}

func (m *model) drainTurnQueues() (tea.Cmd, bool) {
	// Drain ONE user message per call so each gets its own agent response.
	// The agent's inner loop also drains one inbox message at a time,
	// producing one TurnEvent per queued message. Leaving edit mode re-kicks a
	// drain held by a head item under edit (see routeKeypress).
	if cmd, released := m.releaseQueuedMessage(); released {
		log.QueueLog("drainTurnQueues: released queued message, remaining=%d", m.userInput.Queue.Len())
		return cmd, true
	} else if m.userInput.Queue.SelectIdx == 0 {
		log.QueueLog("drainTurnQueues: head item under edit, holding %d queued", m.userInput.Queue.Len())
	}

	if len(m.systemInput.CronQueue) > 0 {
		prompt := m.systemInput.CronQueue[0]
		m.systemInput.CronQueue = m.systemInput.CronQueue[1:]
		return m.injectCronPrompt(prompt), true
	}

	if m.systemInput.AsyncHookQueue != nil {
		if item, ok := m.systemInput.AsyncHookQueue.Pop(); ok {
			return m.injectAsyncHookContinuation(item), true
		}
	}

	if len(m.pendingNotices) > 0 {
		events := m.pendingNotices
		m.pendingNotices = nil
		// Listener may have just re-armed during OnTurnEnd processing;
		// catch any chan events that landed in that window too.
		if extra := drainNotices(m.mainNotices, maxNoticesPerDrain-len(events)); len(extra) > 0 {
			events = append(events, extra...)
		}
		return m.injectNotices(events), true
	}

	return nil, false
}

// injectNotice surfaces merged main-loop notices (subagent completions,
// interim messages) into the live conversation: the notice line is shown, the
// body (if any) is submitted to the main agent as a fresh turn.
func (m *model) injectNotice(n mainNotice) tea.Cmd {
	if n.Display != "" {
		if n.FromAgent {
			m.conv.AddAgentNotice(n.Display)
		} else {
			m.conv.AddNotice(n.Display)
		}
	}
	if n.Content == "" {
		return tea.Batch(m.CommitMessages()...)
	}
	return m.SubmitToAgent(n.Content, nil)
}

func drainNotices(ch <-chan mainNotice, max int) []mainNotice {
	var out []mainNotice
	for range max {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
	return out
}

// mainNoticeMsg wraps one Source-2 notice for the Update loop.
// Counterpart to AgentOutboxMsg for the agent outbox chan.
type mainNoticeMsg struct{ notice mainNotice }

// awaitMainNotice blocks until one notice arrives on the chan, then yields a
// mainNoticeMsg. onMainNotice re-arms after handling.
func awaitMainNotice(ch <-chan mainNotice) tea.Cmd {
	return func() tea.Msg {
		return mainNoticeMsg{notice: <-ch}
	}
}

// onMainNotice injects a message now when idle, or parks it in pendingNotices
// for the next turn boundary when a stream is in flight. Re-arming is
// unconditional: after the read the chan is empty, so the next firing waits for
// the next message.
func (m *model) onMainNotice(n mainNotice) tea.Cmd {
	next := awaitMainNotice(m.mainNotices)
	if m.conv.Stream.Active {
		m.pendingNotices = append(m.pendingNotices, n)
		return next
	}
	return tea.Batch(m.injectNotices([]mainNotice{n}), next)
}

func (m *model) injectNotices(notices []mainNotice) tea.Cmd {
	return m.injectNotice(mergeNotices(notices))
}

// injectCronPrompt fires a scheduled cron prompt as if the user had just
// typed it. The notice + user message show what triggered; SubmitToAgent
// handles provider/agent state.
func (m *model) injectCronPrompt(prompt string) tea.Cmd {
	m.conv.AddNotice("Scheduled task fired")
	m.conv.Append(core.ChatMessage{Role: core.RoleUser, Content: prompt})
	return m.SubmitToAgent(prompt, nil)
}

// injectAsyncHookContinuation surfaces an async hook's follow-up: the hook
// pushed one or more context lines + a continuation prompt; we display the
// context as user messages and submit the continuation to the agent.
func (m *model) injectAsyncHookContinuation(item trigger.AsyncHookRewake) tea.Cmd {
	if item.Notice != "" {
		m.conv.AddNotice(item.Notice)
	}
	if len(item.Context) == 0 {
		return tea.Batch(m.CommitMessages()...)
	}
	for _, ctx := range item.Context {
		m.conv.Append(core.ChatMessage{Role: core.RoleUser, Content: ctx})
	}
	return m.SubmitToAgent(item.ContinuationPrompt, nil)
}
