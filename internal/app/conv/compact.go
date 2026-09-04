// Compact state, message types, commands, and helper functions for conversation
// compaction and token-limit management.
package conv

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
)

// --- Message types ---

// CompactResultMsg is sent when a compaction operation completes.
type CompactResultMsg struct {
	Summary       string
	OriginalCount int
	Trigger       string // "manual" or "auto"
	Err           error
}

// --- Compact state ---

const PhaseSummarizing = "Summarizing conversation history"

type CompactState struct {
	Active            bool
	SummaryFocus      string
	LastResult        string
	LastError         bool
	Phase             string
	Count             int // messages being compacted, shown in the in-progress line
	WarningSuppressed bool
}

func (c *CompactState) ClearResult() {
	c.LastResult = ""
	c.LastError = false
}

// Clear stops the in-progress compaction indicator (Active + Phase) without
// touching the completed-summary result fields. Used on both the success
// boundary and the failure fall-through into the next inference.
func (c *CompactState) Clear() {
	c.Active = false
	c.Phase = ""
}

func (c *CompactState) Complete(result string, isError bool) {
	c.Active = false
	c.SummaryFocus = ""
	c.LastResult = result
	c.LastError = isError
	c.Phase = ""
	if !isError {
		c.WarningSuppressed = true
	}
}

// ErrNothingToCompact says the conversation is already as short as summarising
// can make it. The automatic paths refuse below the same floor; this one is
// asked for by a person, so it says so rather than spending a model call to
// summarise a summary — which loses a little more of the conversation each
// time it is pressed.
var ErrNothingToCompact = errors.New("the conversation is already too short to shorten")

func CompactConversation(ctx context.Context, c *llm.Client, msgs []core.Message, focus string) (summary string, count int, err error) {
	count = len(msgs)
	if count < core.MinMessagesToCompact {
		return "", count, ErrNothingToCompact
	}

	conversationText := core.BuildCompactionText(msgs)

	if focus != "" {
		conversationText += fmt.Sprintf("\n\n**Important**: Focus the summary on: %s", focus)
	}

	response, err := c.Complete(ctx,
		system.CompactPrompt(),
		[]core.Message{core.UserMessage(conversationText, nil)},
		core.CompactMaxTokens,
	)
	if err != nil {
		return "", count, fmt.Errorf("failed to generate summary: %w", err)
	}

	summary = strings.TrimSpace(response.Content.Text())
	if summary == "" {
		return "", count, fmt.Errorf("compaction produced empty summary")
	}

	return summary, count, nil
}

// RenderCompactStatus renders a single dim line while a manual /compact is in
// flight (a spinner + how many messages are being compacted), and a one-line
// error if it failed. A successful compaction shows nothing here — the boundary
// line + the collapsed summary message communicate it. No box, no multi-line.
func RenderCompactStatus(_ int, spinnerView string, state CompactState) string {
	switch {
	case state.Active:
		msg := "Compacting conversation…"
		if state.Count > 0 {
			msg = fmt.Sprintf("Compacting %d messages…", state.Count)
		}
		return lipgloss.NewStyle().
			Foreground(kit.CurrentTheme.TextDim).
			PaddingLeft(1).
			Render(spinnerView + " " + msg)
	case state.LastResult != "" && state.LastError:
		return lipgloss.NewStyle().
			Foreground(kit.CurrentTheme.Error).
			PaddingLeft(1).
			Render("✗ " + state.LastResult)
	default:
		return ""
	}
}

// --- Compact command ---

// CompactRequest holds all parameters needed to perform a conversation compaction.
type CompactRequest struct {
	Ctx          context.Context
	Client       *llm.Client
	Messages     []core.Message
	SummaryFocus string
	HookEngine   hook.Handler
	Trigger      string
}

func CompactCmd(req CompactRequest) tea.Cmd {
	return func() tea.Msg {
		ctx := req.Ctx
		focus := req.SummaryFocus
		if req.HookEngine != nil {
			outcome := req.HookEngine.Execute(ctx, hook.PreCompact, hook.HookInput{
				Trigger:            req.Trigger,
				CustomInstructions: req.SummaryFocus,
			})
			if outcome.AdditionalContext != "" {
				if focus != "" {
					focus += "\n" + outcome.AdditionalContext
				} else {
					focus = outcome.AdditionalContext
				}
			}
		}
		summary, count, err := CompactConversation(ctx, req.Client, req.Messages, focus)
		return CompactResultMsg{Summary: summary, OriginalCount: count, Trigger: req.Trigger, Err: err}
	}
}
