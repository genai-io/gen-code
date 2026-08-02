package conv

import (
	"strings"
	"time"

	"github.com/genai-io/san/internal/core"
)

// RenderContext bundles everything per-message rendering needs to read.
// One instance is built once per render pass (app.messageRenderParams)
// and threaded down through RenderActiveContent → RenderMessageRange
// → RenderMessageAt → renderAssistantWithTools.
type RenderContext struct {
	// ── Conversation state ──────────────────────────────────────
	Messages       []core.ChatMessage
	CommittedCount int
	// InlinedResults precomputes which ToolResult messages will be
	// drawn inline with their owning assistant (not as standalone
	// messages). Built once in one pass over Messages; lets every
	// render helper read the relationship without re-scanning.
	InlinedResults inlinedToolResults

	// ── Streaming + in-flight tool execution ────────────────────
	StreamActive bool
	BuildingTool string
	PendingCalls []core.ToolCall
	CurrentIdx   int
	// ToolStartedAt maps a running tool call's ID to when it began executing,
	// so its row can show a live elapsed timer. Only in-flight calls appear.
	ToolStartedAt map[string]time.Time
	// ToolProgress maps a running command's ID to its latest output counter
	// ("1.2k lines"), shown next to the timer. Empty for calls with no output.
	ToolProgress map[string]string
	// AwaitingApprovalID is the in-flight call parked on a permission prompt.
	// It was stamped as started before the prompt went out, so its row has to
	// be told apart from the siblings that really are executing.
	AwaitingApprovalID string

	// ── Renderer / terminal env ─────────────────────────────────
	Width      int
	MDRenderer *MDRenderer

	// ── Per-tick UI state ───────────────────────────────────────
	SpinnerView  string
	Blink        int
	ModelName    string
	InputTokens  int
	OutputTokens int

	// ── Decorations (color / activity maps) ─────────────────────
	AgentColors  map[string]string
	TaskActivity map[int][]string
	TaskOwnerMap map[string]string

	// ── Modal interlock — suppress chrome under a tool prompt ───
	// DockedModalActive marks the frames where a Question / Approval / secret
	// modal owns the screen. The chat tail still renders above it, so anything
	// that animates has to hold still: the turn is parked on the user's answer,
	// not making progress.
	DockedModalActive bool

	// Interactive marks the live render pass (RenderActiveContent): the
	// uncommitted tail the user can still act on. "(ctrl+o to expand)" hints
	// show only here — once a message is frozen into native scrollback the
	// key no longer reaches it.
	Interactive bool
}

// inlinedToolResults precomputes which ToolResult messages should be
// drawn inline with their owning assistant rather than as standalone
// messages, in one linear pass over the message slice. Three render
// helpers used to re-derive this independently with subtly different
// scans; centralising it here eliminates the drift risk.
type inlinedToolResults struct {
	// resultOwner: ToolResult message index → owning assistant index.
	// Used to know which results to SKIP when rendering a range (they
	// will be drawn inline with their owning assistant instead).
	resultOwner map[int]int
	// resultsForAssistant: assistant index → (toolCallID → ToolResultData)
	// Used by renderAssistantWithTools to draw the paired-results block.
	resultsForAssistant map[int]map[string]ToolResultData
}

// PrecomputeInlinedResults walks `messages[from:]` once and returns the
// inlining map. `from` is the caller's CommittedCount: the render only ever
// draws [from:] (committed messages live in native scrollback), so scanning the
// whole conversation every frame is pure waste that grows with session length.
// Absolute indices are preserved — the maps key by position in the full slice,
// and a result at index ≥ from is always owned by an assistant at index ≥ from
// (an assistant commits together with its tool results), so nothing is missed.
// Exported because the model builds it inside messageRenderParams; everything
// else in this package reads RenderContext.InlinedResults.
func PrecomputeInlinedResults(messages []core.ChatMessage, from int) inlinedToolResults {
	p := inlinedToolResults{
		resultOwner:         make(map[int]int),
		resultsForAssistant: make(map[int]map[string]ToolResultData),
	}
	if from < 0 {
		from = 0
	}
	for i := from; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != core.RoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		owned := make(map[string]struct{}, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			owned[tc.ID] = struct{}{}
		}
		for j := i + 1; j < len(messages); j++ {
			next := messages[j]
			if next.Role == core.RoleNotice {
				continue
			}
			if next.ToolResult == nil {
				break
			}
			callID := next.ToolResult.ToolCallID
			if _, ok := owned[callID]; !ok {
				continue
			}
			p.resultOwner[j] = i
			results := p.resultsForAssistant[i]
			if results == nil {
				results = make(map[string]ToolResultData)
				p.resultsForAssistant[i] = results
			}
			results[callID] = ToolResultData{
				ToolName: next.ToolResult.ToolName,
				Content:  next.ToolResult.Content,
				IsError:  next.ToolResult.IsError,
				Expanded: next.Expanded,
				Decision: next.Decision,
				Details:  next.ToolResult.Details,
			}
		}
	}
	return p
}

// ownerOf returns the index of the assistant message whose tool call
// produced the ToolResult at resultIdx, or -1 if the result doesn't
// belong to any assistant (orphan). Callers use this to know which
// ToolResult messages to skip during a range render — those are
// already being drawn by their owning assistant's block.
func (p inlinedToolResults) ownerOf(resultIdx int) int {
	if owner, ok := p.resultOwner[resultIdx]; ok {
		return owner
	}
	return -1
}

// resultsFor returns the toolCallID → ToolResultData map for an
// assistant's inlined results, or nil if none. renderAssistantWithTools
// uses this to assemble the result block that hangs underneath the
// assistant's text.
func (p inlinedToolResults) resultsFor(assistantIdx int) map[string]ToolResultData {
	return p.resultsForAssistant[assistantIdx]
}

// IsResultInlined reports whether the ToolResult at idx is going to be
// drawn inline with its owning assistant. RenderSingleMessage uses this
// to short-circuit standalone rendering for results that are already
// being shown via their assistant.
func (p inlinedToolResults) IsResultInlined(idx int) bool {
	_, ok := p.resultOwner[idx]
	return ok
}

func RenderMessageAt(p RenderContext, idx int, isStreaming bool) string {
	msg := p.Messages[idx]
	var sb strings.Builder

	// A copilot-produced turn (continuation or rewrite) wears a "⎿ autopilot ·
	// <note>" annotation hugging its "❭" line from below (no blank between) so
	// the trailer corner hangs it under the instruction; the normal blank line
	// above still separates it from the turn before.
	autopilotDriven := msg.Role == core.RoleUser && msg.ToolResult == nil && msg.AutopilotNote != ""

	if msg.ToolResult == nil {
		sb.WriteString("\n")
	}

	switch msg.Role {
	case core.RoleUser:
		switch {
		case msg.ToolResult != nil:
			sb.WriteString(RenderToolResultInline(ToolResultData{
				ToolName:    msg.ToolResult.ToolName,
				Content:     msg.ToolResult.Content,
				IsError:     msg.ToolResult.IsError,
				Expanded:    msg.Expanded,
				Interactive: p.Interactive,
				Width:       p.Width,
				Details:     msg.ToolResult.Details,
			}, p.MDRenderer))
		case core.IsCompactSummary(msg.Content):
			// The post-compaction summary is injected as a user message (the
			// model reads it, and it persists/seeds resume), but it's shown as a
			// system notice — not a "❭" user turn — so the transcript stays clean.
			sb.WriteString(RenderSystemMessage(msg.DisplayContent))
		case isAgentEnvelope(msg.Content):
			// A background-agent envelope (task completion / relayed message) is
			// submitted to the model as a user turn and persisted as raw XML.
			// Live it shows as a green notice; on resume the turn is rebuilt from
			// the transcript, so collapse it to the same one-line notice here
			// instead of dumping the raw <task-notification>/<agent-message> XML.
			sb.WriteString(RenderAgentNotice(agentEnvelopeSummary(msg.Content)))
		default:
			sb.WriteString(RenderUserMessage(msg.Content, msg.DisplayContent, msg.Images, p.MDRenderer, p.Width))
			if autopilotDriven {
				sb.WriteString(RenderAutopilotMark(msg.AutopilotNote))
			}
		}
	case core.RoleNotice:
		if msg.AgentNotice {
			sb.WriteString(RenderAgentNotice(msg.Content))
		} else {
			sb.WriteString(RenderSystemMessage(msg.Content))
		}
	case core.RoleAssistant:
		sb.WriteString(renderAssistantWithTools(p, msg, idx, isStreaming))
	}

	return sb.String()
}

func renderAssistantWithTools(p RenderContext, msg core.ChatMessage, idx int, isLast bool) string {
	base := RenderAssistantMessage(AssistantParams{
		Content:              msg.Content,
		Thinking:             msg.Thinking,
		ToolCalls:            msg.ToolCalls,
		StreamActive:         p.StreamActive,
		IsLast:               isLast,
		DockedModalActive:    p.DockedModalActive,
		SpinnerView:          p.SpinnerView,
		MDRenderer:           p.MDRenderer,
		Width:                p.Width,
		ExecutingTool:        p.BuildingTool,
		ContentCommittedLen:  msg.ContentCommittedLen,
		ThinkingCommittedLen: msg.ThinkingCommittedLen,
		BulletEmitted:        msg.BulletEmitted,
		ThinkingEmitted:      msg.ThinkingEmitted,
	})

	if len(msg.ToolCalls) == 0 {
		return base
	}

	var sb strings.Builder
	sb.WriteString(base)

	if msg.Content != "" {
		sb.WriteString("\n")
	}

	resultMap := p.InlinedResults.resultsFor(idx)
	if resultMap == nil {
		resultMap = map[string]ToolResultData{}
	}

	sb.WriteString(RenderToolCalls(ToolCallsParams{
		ToolCalls:          msg.ToolCalls,
		ToolCallsExpanded:  msg.ToolCallsExpanded,
		ResultMap:          resultMap,
		ParallelMode:       len(p.PendingCalls) > 1,
		DockedModalActive:  p.DockedModalActive,
		TaskActivity:       p.TaskActivity,
		PendingCalls:       p.PendingCalls,
		CurrentIdx:         p.CurrentIdx,
		ToolStartedAt:      p.ToolStartedAt,
		ToolProgress:       p.ToolProgress,
		AwaitingApprovalID: p.AwaitingApprovalID,
		ModelName:          p.ModelName,
		InputTokens:        p.InputTokens,
		OutputTokens:       p.OutputTokens,
		Blink:              p.Blink,
		AgentColors:        p.AgentColors,
		SpinnerView:        p.SpinnerView,
		TaskOwnerMap:       p.TaskOwnerMap,
		MDRenderer:         p.MDRenderer,
		Width:              p.Width,
		Interactive:        p.Interactive,
	}))

	return sb.String()
}

func RenderMessageRange(p RenderContext, startIdx, endIdx int, includeSpinner bool) string {
	var sb strings.Builder

	lastIdx := endIdx - 1
	isLastStreaming := p.StreamActive && lastIdx >= 0 && p.Messages[lastIdx].Role == core.RoleAssistant

	for i := startIdx; i < endIdx; i++ {
		// Skip a ToolResult that will be drawn inline with its owning
		// assistant — but only if the owner is also being rendered in
		// this range. Orphan results render standalone.
		if owner := p.InlinedResults.ownerOf(i); owner >= startIdx {
			continue
		}
		isStreaming := i == lastIdx && isLastStreaming
		sb.WriteString(RenderMessageAt(p, i, isStreaming))
	}

	if includeSpinner {
		sb.WriteString(renderPendingToolSpinnerFromParams(p, startIdx < endIdx))
	}

	return sb.String()
}

func RenderSingleMessage(p RenderContext, idx int) string {
	if idx < 0 || idx >= len(p.Messages) {
		return ""
	}

	if p.Messages[idx].ToolResult != nil && p.InlinedResults.IsResultInlined(idx) {
		return ""
	}

	return strings.TrimRight(RenderMessageAt(p, idx, false), "\n")
}

func RenderActiveContent(p RenderContext) string {
	p.Interactive = true
	if p.CommittedCount >= len(p.Messages) {
		return renderPendingToolSpinnerFromParams(p, false)
	}
	return RenderMessageRange(p, p.CommittedCount, len(p.Messages), true)
}

func renderPendingToolSpinnerFromParams(p RenderContext, suppressAgentLabel bool) string {
	return RenderPendingToolSpinner(PendingToolSpinnerParams{
		DockedModalActive:  p.DockedModalActive,
		BuildingTool:       p.BuildingTool,
		PendingCalls:       p.PendingCalls,
		CurrentIdx:         p.CurrentIdx,
		TaskActivity:       p.TaskActivity,
		SpinnerView:        p.SpinnerView,
		Blink:              p.Blink,
		AgentColors:        p.AgentColors,
		Width:              p.Width,
		SuppressAgentLabel: suppressAgentLabel,
	})
}
