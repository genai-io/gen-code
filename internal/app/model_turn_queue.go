// Inbox drain and prompt injection. After every agent turn ends we drain (in
// priority order) queued user messages, cron-fired prompts, async-hook
// continuations, and the main-loop notice buffer (broker messages routed to
// "main"). Each drained item is converted to a notice + optional re-send to the
// agent. Notice delivery is not limited to the turn boundary — see notify.go
// for when each seam applies. Also handles the Stop hook result that gates
// session persistence.
package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/input"
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
// it a step or a turn later, and the agent's echo of it is ignored (see conv.applyAgentEvent).
// Shared by the step-boundary (OnStepEnd) and turn-boundary
// (drainTurnQueues) drains.
func (m *model) releaseQueuedMessage() (tea.Cmd, bool) {
	if m.userInput.Queue.SelectIdx == 0 {
		return nil, false
	}
	item, ok := m.userInput.Queue.Dequeue()
	if !ok {
		return nil, false
	}
	// Process image references (leading image paths, @-references, bare paths)
	// just like the normal submit path does — the raw queued content hasn't been
	// through ProcessImageRefs yet. On failure this message still goes out: it
	// runs mid-stream, so there is no textarea to hand it back to (the user is
	// typing the next one there). Send what resolved.
	content, fileImages, err := input.ProcessImageRefs(m.env.CWD, item.Content)
	if err != nil {
		m.conv.AddNotice("Image error: " + err.Error())
	}
	// Images the user attached travel on the item — they were moved off the
	// textarea when it was queued. Anything pending in the textarea now belongs
	// to the next message, so it must not be picked up here.
	images := make([]core.Image, 0, len(item.Images)+len(fileImages))
	images = append(images, item.Images...)
	images = append(images, fileImages...)
	// Split display from content the way buildUserMessage does: the queued text
	// still carries the [Image #N] markers the textarea used to position its
	// attachments, which the reader wants to see and the model doesn't.
	displayContent := content
	content = strings.TrimSpace(core.InlineImageTokenRe.ReplaceAllString(content, ""))
	// Text-only models can't receive image parts; inline each image's path so
	// the model can decide how to use it (e.g. via an MCP tool).
	content, providerImages := m.adaptTurnForProvider(content, images)
	m.conv.Append(core.ChatMessage{
		Role:           core.RoleUser,
		Content:        content,
		DisplayContent: displayContent,
		Images:         images,
	})
	svc := m.services.Agent
	send := func() tea.Msg {
		svc.Send(content, providerImages)
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

	// Whatever releaseParkedNotices did not get to: a task that finished during
	// the turn's last step, or during a turn that never ran a tool.
	if notices := m.takeParkedNotices(); len(notices) > 0 {
		return m.injectAsNewTurn(mergeNotices(notices)), true
	}

	return nil, false
}

// takeParkedNotices removes everything parked during the running turn, topped
// up from the channel so notices that landed since the last read ride along in
// the same injection rather than trickling in one step apart.
func (m *model) takeParkedNotices() []mainNotice {
	if len(m.pendingNotices) == 0 {
		return nil
	}
	notices := m.pendingNotices
	m.pendingNotices = nil
	if extra := drainNotices(m.mainNotices, maxNoticesPerDrain-len(notices)); len(extra) > 0 {
		notices = append(notices, extra...)
	}
	return notices
}

// releaseParkedNotices injects what LastMessageIsStreaming held back, at the step
// boundary where the tail is free again.
func (m *model) releaseParkedNotices() tea.Cmd {
	notices := m.takeParkedNotices()
	if len(notices) == 0 {
		return nil
	}
	log.QueueLog("releaseParkedNotices: releasing %d notice(s) mid-turn", len(notices))
	return m.injectIntoRunningTurn(mergeNotices(notices))
}

// injectIntoRunningTurn hands a notice to the agent partway through a turn it is
// already running: sendToAgent reaches its inbox, which it drains between steps,
// so the content is in the conversation the next inference reads. Its pair is
// injectAsNewTurn, whose SubmitToAgent must not be used here — that path can
// rebuild the agent session, which would tear down the running turn.
func (m *model) injectIntoRunningTurn(n mainNotice) tea.Cmd {
	m.showNoticeLine(n)
	cmds := m.CommitMessages()
	if n.Content != "" { // display-only notices have nothing for the model to read
		cmds = append(cmds, m.sendToAgent(n.Content, nil))
	}
	return tea.Batch(cmds...)
}

// injectAsNewTurn delivers a notice with no turn running: the line is shown and the
// body, if any, starts a fresh turn.
func (m *model) injectAsNewTurn(n mainNotice) tea.Cmd {
	m.showNoticeLine(n)
	if n.Content == "" {
		return tea.Batch(m.CommitMessages()...)
	}
	return m.SubmitToAgent(n.Content, nil)
}

func (m *model) showNoticeLine(n mainNotice) {
	if n.Display == "" {
		return
	}
	if n.FromAgent {
		m.conv.AddAgentNotice(n.Display)
	} else {
		m.conv.AddNotice(n.Display)
	}
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

// onMainNotice routes an arriving notice to the earliest injection the
// conversation can take: into a running turn, as a fresh turn when idle, or
// parked while the stream owns the tail (see notify.go). Re-arming is
// unconditional: after the read the chan is empty, so the next firing waits for
// the next message.
func (m *model) onMainNotice(n mainNotice) tea.Cmd {
	next := awaitMainNotice(m.mainNotices)
	switch {
	case m.conv.LastMessageIsStreaming():
		m.pendingNotices = append(m.pendingNotices, n)
		return next
	case m.conv.Stream.Active:
		return tea.Batch(m.injectIntoRunningTurn(n), next)
	default:
		return tea.Batch(m.injectAsNewTurn(n), next)
	}
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
