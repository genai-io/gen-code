// Input-driven side effects that don't belong to a single key handler:
// streaming cancel (Ctrl+C / Esc mid-stream), in-flight tool-call
// cancellation, clipboard image paste, and the quit-with-cancel path that
// gracefully stops the agent before tea.Quit.
package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/image"
)

// InterruptReminder is enqueued on the reminder service when the user
// cancels a streaming turn. It piggybacks onto the next user message
// via attachPendingReminders, so the model gets an explicit "the
// previous response did not complete" signal without us having to
// inject a synthetic assistant or user message into the chain.
const InterruptReminder = "The previous response was interrupted by the user. Acknowledge the interruption and proceed with their next instruction."

func (m *model) handleStreamCancel() tea.Cmd {
	// The whole cancel path is just two things now: stop the agent's
	// in-flight turn, and arrange for the next user message to carry a
	// reminder explaining what happened. Convert-layer sanitization
	// already strips any orphaned tool_use blocks the cancel left in
	// the agent's history, and Anthropic's converter merges consecutive
	// same-role messages — so the agent does not need to synthesize
	// any marker of its own, and the conv layer does not need to push
	// state back into the agent.
	m.services.Agent.InterruptTurn()
	// EnqueueOnce so mashed Esc keys don't attach N identical
	// <system-reminder> blocks to the next user message.
	m.services.Reminder.EnqueueOnce(InterruptReminder)
	m.conv.Stream.Stop()
	m.conv.AgentToUI.DrainPendingQuestions()
	m.conv.Modal.Question.Hide()
	m.cancelPendingToolCalls()
	m.conv.MarkLastInterrupted()

	cmds := m.CommitMessages()
	if cmd := m.drainInputQueueWhileIdle(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (m *model) cancelPendingToolCalls() {
	toolCalls := m.conv.Tool.DrainPendingCalls()
	if toolCalls == nil && len(m.conv.Messages) > 0 {
		lastMsg := m.conv.Messages[len(m.conv.Messages)-1]
		if lastMsg.Role == core.RoleAssistant {
			toolCalls = lastMsg.ToolCalls
		}
	}
	m.conv.AppendCancelledToolResults(toolCalls, func(tc core.ToolCall) string {
		return "Tool execution interrupted because the user sent a new message."
	}, m.TakeReviewDecision)
}

func (m *model) pasteImageFromClipboard() (tea.Cmd, bool) {
	imgData, err := image.ReadClipboard()
	if err != nil {
		m.conv.Append(core.ChatMessage{Role: core.RoleNotice, Content: "Image paste error: " + err.Error()})
		return tea.Batch(m.CommitMessages()...), true
	}
	if imgData == nil {
		return nil, false
	}
	label := m.userInput.AddPendingImage(*imgData)
	m.userInput.Images.Selection = input.ImageSelection{}
	m.userInput.Textarea.InsertString(label)
	return nil, true
}

func (m *model) QuitWithCancel() (tea.Cmd, bool) {
	m.services.Agent.Stop()
	m.conv.Stream.Stop()
	if m.conv.Tool.Cancel != nil {
		m.conv.Tool.Cancel()
	}
	m.FireSessionEnd("prompt_input_exit")
	// Index flush happens once at the post-Run teardown seam (see run.go), the
	// single point every quit path converges on — including /exit and /quit,
	// which run this same shutdown sequence but never reach here.
	return tea.Quit, true
}
