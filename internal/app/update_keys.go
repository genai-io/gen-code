// Keyboard handling: routes a tea.KeyMsg first to the active overlay (modal
// or slash-command picker, via activeOverlay), then to image/suggestion/queue
// overlays, then to the textarea-level shortcuts (Ctrl+C/D/L/E/O, Tab, Enter,
// etc.). Also owns the Ctrl+O double-tap detection and the per-keystroke
// thinking-effort cycle.
package app

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/llm"
)

const ctrlODoubleTapWindow = 300 * time.Millisecond

type ctrlOSingleTickMsg struct{}

// routeKeypress is the priority dispatcher for tea.KeyMsg. A keypress
// flows through these layers in order; the first one that claims it wins:
//
//  1. activeOverlay            — any open modal or slash-command picker
//  2. HandleImageSelectKey     — image picker overlay inside textarea
//  3. HandleSuggestionKey      — prompt-suggestion overlay inside textarea
//  4. HandleQueueSelectKey     — queue-navigation mode inside textarea
//  5. handleTextareaShortcut   — Ctrl-shortcuts + Tab/Enter/history
//
// Returns (cmd, true) if any layer consumed the key. Falling off the end
// means "let the textarea consume it as text" — that's handled in
// updateTextarea, not here.
func (m *model) routeKeypress(msg tea.KeyMsg) (tea.Cmd, bool) {
	if ov, ok := m.activeOverlay(); ok {
		return ov.HandleKeypress(msg), true
	}

	if c, ok := m.userInput.HandleImageSelectKey(msg); ok {
		return c, ok
	}
	if c, ok := m.userInput.HandleSuggestionKey(msg); ok {
		return c, ok
	}
	if c, ok := m.userInput.HandleQueueSelectKey(msg); ok {
		// Leaving queue-edit mode releases a drain that was held while the
		// head item was under edit (see drainTurnQueues): if the agent went
		// idle in the meantime, dispatch the queue now.
		if m.userInput.Queue.SelectIdx < 0 && !m.conv.Stream.Active {
			if drain := m.drainInputQueueWhileIdle(); drain != nil {
				return tea.Batch(c, drain), true
			}
		}
		return c, ok
	}

	return m.handleTextareaShortcut(msg)
}

// handleTextareaShortcut handles keys that target the textarea itself:
// Ctrl-shortcuts (C/D/L/E/O/U/V/Y/T), Tab, Shift+Tab, Enter, Esc, and
// arrow-key history navigation. Returns (cmd, true) if the key was a
// recognized shortcut, (nil, false) to let the rune fall through to
// updateTextarea as plain text input.
func (m *model) handleTextareaShortcut(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "tab", "right":
		if m.userInput.PromptSuggestion.Text != "" && m.userInput.Textarea.Value() == "" {
			m.userInput.Textarea.SetValue(m.userInput.PromptSuggestion.Text)
			m.userInput.Textarea.CursorEnd()
			m.userInput.PromptSuggestion.Clear()
			return nil, true
		}

	case "shift+tab":
		// Cycling stays available while a turn is running: being asked to approve
		// every command is exactly when the user reaches for accept-edits or
		// autopilot, and the permission gate reads the posture per call, so the
		// switch lands on the turn's next tool. Overlays that bind Shift+Tab
		// themselves (the approval prompt's "allow all this session", the
		// pickers) already took it in routeKeypress; the command popup is the one
		// caller that reaches this far.
		if !m.userInput.Suggestions.IsVisible() {
			return m.cycleOperationMode(), true
		}

	case "ctrl+t":
		return m.cycleThinkingEffort(), true

	case "alt+t", "alt+T":
		m.conv.ShowTasks = !m.conv.ShowTasks
		return nil, true

	case "ctrl+o":
		return m.handleCtrlO(), true

	case "ctrl+e":
		return m.expandCollapseAll(), true

	case "ctrl+x":
		return nil, false

	case "ctrl+u":
		if m.userInput.Queue.Len() > 0 {
			if m.userInput.Queue.SelectIdx >= 0 {
				// Mid-edit: return the pre-edit stash to the textarea before
				// wiping the queue out from under the edit session.
				m.userInput.ExitQueueSelection()
			}
			m.userInput.Queue.Clear()
			return nil, true
		}
		return nil, false

	case "ctrl+v", "ctrl+y":
		return m.pasteImageFromClipboard()

	case "ctrl+c":
		if m.userInput.Textarea.Value() != "" {
			m.userInput.Reset()
			m.userInput.History.Index = -1
			m.userInput.LastCtrlC = time.Time{}
			return nil, true
		}
		if m.conv.Stream.Active {
			m.userInput.LastCtrlC = time.Time{}
			return m.handleStreamCancel(), true
		}
		now := time.Now()
		if !m.userInput.LastCtrlC.IsZero() && now.Sub(m.userInput.LastCtrlC) < 1*time.Second {
			return m.QuitWithCancel()
		}
		m.userInput.LastCtrlC = now
		_, cmd, _ := m.executeCommand(context.Background(), "/clear")
		return cmd, true

	case "ctrl+d":
		if m.userInput.Textarea.Value() != "" {
			return nil, false
		}
		return m.QuitWithCancel()

	case "ctrl+l":
		_, cmd, _ := m.executeCommand(context.Background(), "/clear")
		return cmd, true

	case "esc":
		if m.userInput.PromptSuggestion.Text != "" {
			m.userInput.PromptSuggestion.Clear()
			return nil, true
		}
		if m.userInput.Suggestions.IsVisible() {
			m.userInput.Suggestions.Hide()
			return nil, true
		}
		if m.conv.Stream.Active {
			return m.handleStreamCancel(), true
		}
		return nil, true

	case "up":
		if m.userInput.Textarea.Line() == 0 {
			if m.userInput.Queue.Len() > 0 {
				m.userInput.EnterQueueSelection()
				return nil, true
			}
			m.userInput.HistoryUp()
			return nil, true
		}

	case "down":
		lines := strings.Count(m.userInput.Textarea.Value(), "\n")
		if m.userInput.Textarea.Line() == lines {
			if m.userInput.Queue.Len() > 0 {
				m.userInput.EnterQueueSelection()
				return nil, true
			}
			m.userInput.HistoryDown()
			return nil, true
		}

	case "enter":
		return m.handleSubmit(), true
	}
	// Newline keys deliberately fall through to the textarea, which owns them
	// via KeyMap.InsertNewline — see newTextarea.

	return nil, false
}

func (m *model) cycleThinkingEffort() tea.Cmd {
	current := m.env.EffectiveThinkingEffort()
	next, ok := llm.NextThinkingEffortForModel(m.env.LLMProvider, m.env.store, m.env.CurrentModel, current)
	if !ok {
		token := m.userInput.Provider.SetStatusMessage("reasoning is not supported by this provider")
		return kit.StatusTimer(3*time.Second, token)
	}

	m.env.SetThinkingEffort(next)
	return nil
}

func (m *model) handleCtrlO() tea.Cmd {
	if m.userInput.Approval.IsActive() {
		m.userInput.Approval.TogglePreview()
		return nil
	}

	now := time.Now()
	if !m.userInput.LastCtrlO.IsZero() && now.Sub(m.userInput.LastCtrlO) < ctrlODoubleTapWindow {
		m.userInput.LastCtrlO = time.Time{}
		return m.expandCollapseAll()
	}

	m.userInput.LastCtrlO = now
	return tea.Tick(ctrlODoubleTapWindow, func(time.Time) tea.Msg {
		return ctrlOSingleTickMsg{}
	})
}

func (m *model) handleCtrlOSingleTick() tea.Cmd {
	if m.userInput.LastCtrlO.IsZero() {
		return nil
	}
	m.userInput.LastCtrlO = time.Time{}
	m.conv.ToggleMostRecentExpandable()
	return nil
}

func (m *model) expandCollapseAll() tea.Cmd {
	m.conv.ToggleAllExpandable()
	return nil
}
