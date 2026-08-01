// Pure message rendering functions that take explicit parameters instead of model state.
package conv

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
)

const (
	// minWrapWidth is the minimum markdown wrap width.
	minWrapWidth = 40

	// streamWrapReserve is the column budget held back when plain-wrapping the
	// live streaming tail: 2 cols for the "● "/"✦ " gutter plus 2 cols of slack
	// so an ambiguous-width glyph (the spinner star ✶ counted 1 cell but drawn
	// 2, CJK, box-drawing) can't push a line into the last column — that makes
	// the inline-mode insertAbove miscount rows and weld a stale frame into
	// scrollback.
	streamWrapReserve = 4

	// agentContentIndent is the extra indent for agent prompt/response content
	// beyond toolResultExpandedStyle's PaddingLeft(4). Total indent = 4 + 4 = 8 chars.
	agentContentIndent = "    "

	// autoCompactThreshold is the context usage percentage that triggers
	// auto-compaction. pctCritical in status_bar.go derives from this; do
	// not reintroduce a separate literal.
	autoCompactThreshold = core.AutoCompactThresholdPercent
)

// toolResultIcon returns the icon for tool results based on error state.
func toolResultIcon(isError bool) string {
	if isError {
		return "✗"
	}
	return "⎿"
}

// InputPrompt marks the input line, in the live textarea and in scrollback
// alike. Its width doubles as the cursor's horizontal offset into the textarea
// (see app.inputCursor), so the glyph and that offset stay in step by
// construction.
const InputPrompt = "❭ "

var (
	userMsgStyle = lipgloss.NewStyle()

	InputPromptStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Focus).
				Bold(true)

	// The assistant bullet leads with weight, not hue: the strongest neutral
	// (near-black on light, near-white on dark) plus bold. The conversation
	// body stays monochrome — teal is reserved for the user's "❭" marker.
	aiPromptStyle = lipgloss.NewStyle().
			Foreground(kit.CurrentTheme.TextBright).
			Bold(true)

	// Footer rules are a faint hairline so they frame the input without
	// drawing ink — softer than the bluish Separator used between messages.
	SeparatorStyle = lipgloss.NewStyle().
			Foreground(kit.AdaptiveColor{Dark: "#3F3F46", Light: "#E4E4E7"})

	ThinkingStyle = lipgloss.NewStyle().
			Foreground(kit.CurrentTheme.Muted)

	systemMsgStyle = lipgloss.NewStyle().
			Foreground(kit.CurrentTheme.TextDim).
			PaddingLeft(2)

	// A message relayed in from a background agent (subagent completion / interim
	// report). Uses the agent tone (Success, matching agentLabelStyle and the
	// default agentColor) with a "◆" marker so it reads as an inbound agent
	// message, distinct from the dim system notices around it.
	agentNoticeStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Success)

	// The tool call line stays readable — the action and its target (the file
	// being edited, the command being run) matter, and the live spinner rides
	// this line while the call is in flight.
	toolCallStyle = lipgloss.NewStyle().
			Foreground(kit.CurrentTheme.Text)

	// Only the "⎿ … → size" result trailer recedes: it's secondary metadata,
	// dimmed to the same supporting tone as the expanded body so the eye lands
	// on the assistant's prose and the actions, not the bookkeeping.
	toolResultStyle = lipgloss.NewStyle().
			Foreground(kit.CurrentTheme.TextDim)

	toolResultExpandedStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.TextDim).
				PaddingLeft(4)

	agentLabelStyle = lipgloss.NewStyle().
			Foreground(kit.CurrentTheme.Success)

	// Auto-review decision labels: green when the judge auto-approved a
	// gray-zone tool call, amber when it escalated the call back to the user.
	decisionApprovedStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Success)
	decisionEscalatedStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Warning)

	// The autopilot continuation header: green like an auto-approved decision —
	// the copilot drove this turn forward — with a downward arrow that points at
	// the "❭" line right below it.
	autopilotStepStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Success)

	trackerPendingStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Muted)

	trackerInProgressStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Primary).
				Bold(true)

	trackerCompletedStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Success)

	PendingImageStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.Primary)

	SelectedImageStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.TextBright).
				Background(kit.CurrentTheme.Primary).
				Bold(true)
)

// RenderAutopilotMark is the "⎿ autopilot · <note>" annotation a copilot-produced
// turn wears — a continuation ("2/5") or a rewrite ("refined") — drawn tight
// below its "❭" line: the trailer corner hangs the note under the instruction to
// say the copilot, not the human, typed it (the same "⎿" trailer a tool result
// hangs under the call it came from).
func RenderAutopilotMark(note string) string {
	return autopilotStepStyle.Render("  ⎿  autopilot") +
		toolResultStyle.Render(" · "+note) + "\n"
}

// RenderUserMessage renders a user message with prompt and optional images.
func RenderUserMessage(content, displayContent string, images []core.Image, mdRenderer *MDRenderer, width int) string {
	var sb strings.Builder
	prompt := InputPromptStyle.Render(InputPrompt)
	if displayContent == "" {
		displayContent = content
	}

	if len(images) > 0 && core.InlineImageTokenRe.MatchString(displayContent) {
		sb.WriteString(lipgloss.JoinHorizontal(
			lipgloss.Top,
			prompt,
			userMsgStyle.Render(styleInlineImageTokens(displayContent)),
		) + "\n")
		return sb.String()
	}

	if len(images) > 0 {
		imgParts := make([]string, 0, len(images))
		for i := range images {
			imgParts = append(imgParts, PendingImageStyle.Render(fmt.Sprintf("[Image #%d]", i+1)))
		}
		imageLabel := strings.Join(imgParts, " ")
		if displayContent != "" {
			sb.WriteString(prompt + imageLabel + " " + userMsgStyle.Render(displayContent) + "\n")
		} else {
			sb.WriteString(prompt + imageLabel + "\n")
		}
	} else if displayContent != "" {
		sb.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, prompt, userMsgStyle.Render(displayContent)) + "\n")
	}

	return sb.String()
}

func styleInlineImageTokens(content string) string {
	return core.InlineImageTokenRe.ReplaceAllStringFunc(content, func(token string) string {
		return PendingImageStyle.Render(token)
	})
}

// AssistantParams holds the parameters for rendering an assistant core.
type AssistantParams struct {
	Content           string
	Thinking          string
	ToolCalls         []core.ToolCall
	ToolCallsExpanded bool
	StreamActive      bool
	IsLast            bool
	// DockedModalActive freezes the bullet: the stream stays "active" across a
	// permission gate (it clears only on a text-only Done chunk), so without
	// this the live tail spins while the turn is parked on the user's answer.
	DockedModalActive bool
	SpinnerView       string
	MDRenderer        *MDRenderer
	Width             int
	ExecutingTool     string

	// Streaming-commit offsets: how much of Content/Thinking is already in
	// scrollback (see FlushStreamingBlocks). Only the remainder is rendered
	// here. BulletEmitted swaps the "● " marker for a continuation gutter once
	// the turn's first content block has been committed; ThinkingEmitted does the
	// same for the "✦ " marker once the turn's first thinking block is committed.
	ContentCommittedLen  int
	ThinkingCommittedLen int
	BulletEmitted        bool
	ThinkingEmitted      bool
}

// InterruptedMarker is the literal suffix MarkLastInterrupted appends to an
// assistant message's Content when the user cancels mid-stream. It lives on
// the conv-side ChatMessage only — handleStreamCancel no longer pushes conv
// state back into the agent, so the marker reaches the LLM only via session
// save+reload. Stripped at render time so the UI shows a styled badge
// instead of inline text.
const InterruptedMarker = "[Interrupted]"

// continuationGutter is the 2-column blank that aligns continuation lines, and
// content blocks committed after the first, under the "● " assistant marker.
const continuationGutter = "  "

// contentGutter returns the 2-column lead for an assistant content block: the
// "● " marker for the turn's first content, or a blank gutter for blocks that
// continue a turn whose marker was already emitted.
func contentGutter(showBullet bool) string {
	if showBullet {
		return aiPromptStyle.Render("● ")
	}
	return continuationGutter
}

// thinkingGutter returns the 2-column lead for a reasoning block: the muted "✦"
// marker for the turn's first thinking block, or a blank continuation gutter for
// blocks committed after it, so progressively-committed reasoning aligns under
// the single leading glyph.
func thinkingGutter(showIcon bool) string {
	if showIcon {
		return ThinkingStyle.Render("✦ ")
	}
	return continuationGutter
}

// renderThinkingBlock renders reasoning text as the muted "✦" block shared by
// the live view and the scrollback commit path. The glyph and text both stay
// muted, matching the status-bar thinking indicator — no hue. showIcon leads the
// block with the "✦ " marker (the turn's first thinking) or a blank continuation
// gutter for blocks committed after it. With md set the reasoning is laid out as
// markdown (then re-toned muted); without it, a plain muted wrap — the live
// streaming tail passes nil to stay cheap, matching the content tail.
func renderThinkingBlock(thinking string, showIcon bool, width int, md *MDRenderer) string {
	body := mutedThinkingBody(thinking, width, md)
	if strings.TrimSpace(xansi.Strip(body)) == "" {
		return ""
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, thinkingGutter(showIcon), body)
}

// mutedThinkingBody lays reasoning out as markdown when a renderer is available,
// then flattens it to the single muted thinking tone: headings, lists and tables
// keep their structure but read as de-emphasized reasoning instead of a raw
// "###" / "| |" dump. Without a renderer it falls back to a plain muted wrap.
func mutedThinkingBody(thinking string, width int, md *MDRenderer) string {
	var text string
	if md != nil {
		if rendered, err := md.Render(thinking); err == nil {
			text = xansi.Strip(rendered) // drop markdown's hues; re-tone muted below
		}
	}
	if text == "" {
		wrapWidth := max(width-streamWrapReserve, minWrapWidth)
		text = lipgloss.NewStyle().Width(wrapWidth).Render(thinking)
	}
	var lines []string
	for line := range strings.SplitSeq(strings.Trim(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			lines = append(lines, "")
		} else {
			lines = append(lines, ThinkingStyle.Render(line))
		}
	}
	return strings.Join(lines, "\n")
}

// sliceFrom returns the portion of s past the first n bytes — the not-yet-
// committed remainder of a streaming field. n out of range collapses to the
// natural answer (whole string for n<=0, empty once n has caught up to len).
func sliceFrom(s string, n int) string {
	switch {
	case n <= 0:
		return s
	case n >= len(s):
		return ""
	default:
		return s[n:]
	}
}

// RenderCommittedThinkingBlock renders a completed reasoning block for commit to
// native scrollback — the muted gutter plus the wrapped thinking text. showIcon
// leads with the "✦ " marker (the turn's first thinking block) or a blank
// continuation gutter for blocks committed after it. Returns "" when the slice
// renders empty (e.g. only blank lines).
func RenderCommittedThinkingBlock(thinking string, showIcon bool, width int, md *MDRenderer) string {
	if strings.TrimSpace(thinking) == "" {
		return ""
	}
	return renderThinkingBlock(thinking, showIcon, width, md)
}

// RenderCommittedContentBlock renders one or more completed markdown blocks of
// assistant content for commit to native scrollback. showBullet leads the block
// with the "● " marker (the turn's first content) or a blank continuation
// gutter. Returns "" when the slice renders empty (e.g. only blank lines).
func RenderCommittedContentBlock(content string, showBullet bool, md *MDRenderer) string {
	body := content
	if md != nil {
		body = renderMarkdownContent(md, content)
	}
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, contentGutter(showBullet), body)
}

// RenderAssistantMessage renders an assistant message with thinking, content, and tool calls.
func RenderAssistantMessage(params AssistantParams) string {
	var sb strings.Builder

	// Render only the not-yet-committed remainder: completed blocks of a
	// streaming message are already in scrollback (see FlushStreamingBlocks).
	params.Thinking = sliceFrom(params.Thinking, params.ThinkingCommittedLen)
	params.Content = sliceFrom(params.Content, params.ContentCommittedLen)

	// The first content of a turn leads with "● "; once a block has been
	// committed the bullet is spent and the remainder aligns under a gutter.
	// While streaming, the active tail shows the spinner in the bullet slot.
	aiIcon := contentGutter(!params.BulletEmitted)
	if params.StreamActive && params.IsLast && !params.DockedModalActive {
		aiIcon = aiPromptStyle.Render(params.SpinnerView + " ")
	}

	interrupted := false
	switch {
	case strings.HasSuffix(params.Content, " "+InterruptedMarker):
		params.Content = strings.TrimSuffix(params.Content, " "+InterruptedMarker)
		interrupted = true
	case params.Content == InterruptedMarker:
		params.Content = ""
		interrupted = true
	}

	if params.Thinking != "" {
		// The live streaming tail stays plain (nil renderer), matching the content
		// tail; a settled block lays out as muted markdown.
		thinkMD := params.MDRenderer
		if params.StreamActive && params.IsLast {
			thinkMD = nil
		}
		sb.WriteString(renderThinkingBlock(params.Thinking, !params.ThinkingEmitted, params.Width, thinkMD) + "\n\n")
	}

	content := formatAssistantContent(params)
	if content != "" {
		sb.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, aiIcon, content) + "\n")
	}

	if interrupted {
		sb.WriteString("  " + ThinkingStyle.Render("↩ cancelled") + "\n")
	}

	return sb.String()
}

// formatAssistantContent formats the assistant message content based on streaming state.
func formatAssistantContent(params AssistantParams) string {
	if params.Content == "" && len(params.ToolCalls) == 0 && params.StreamActive && params.Thinking == "" {
		if params.ExecutingTool != "" {
			return ThinkingStyle.Render(getToolExecutionDesc(params.ExecutingTool))
		}
		if !params.BulletEmitted {
			return ThinkingStyle.Render("Thinking...")
		}
		// BulletEmitted: content is already streaming to scrollback block by
		// block — fall through so the momentary gap between committed blocks
		// still shows the live spinner, not the "Thinking…" filler.
	}

	if params.StreamActive && params.IsLast && len(params.ToolCalls) == 0 {
		if params.Content == "" {
			// Nothing new to draw (turn start, or the gap right after a block
			// commits): a lone space keeps the leading spinner on screen so the
			// tail reads as live without trailing any caret glyph after the text.
			return " "
		}
		// Plain-wrap the streaming tail so its \n-line count matches the height
		// calc; reserve streamWrapReserve cols (gutter + last-column slack).
		wrapWidth := max(params.Width-streamWrapReserve, minWrapWidth)
		return lipgloss.NewStyle().Width(wrapWidth).Render(params.Content)
	}

	if params.Content == "" {
		return ""
	}

	if params.MDRenderer != nil {
		return renderMarkdownContent(params.MDRenderer, params.Content)
	}

	return params.Content
}

// renderMarkdownContent renders content through the markdown renderer, dropping
// glamour's full-width blank margin lines so blocks don't accrue extra vertical
// gaps (especially when committed to scrollback block-by-block while streaming).
func renderMarkdownContent(mdRenderer *MDRenderer, content string) string {
	rendered, err := mdRenderer.Render(content)
	if err != nil {
		return content
	}
	return trimStyledBlankLines(rendered)
}

// getToolExecutionDesc returns a human-readable description for a tool being executed.
func getToolExecutionDesc(toolName string) string {
	switch toolName {
	case "Read":
		return "Reading file..."
	case "Write":
		return "Writing file..."
	case "Edit":
		return "Editing file..."
	case "Bash":
		return "Executing command..."
	case "WebFetch":
		return "Fetching web content..."
	case "WebSearch":
		return "Searching the web..."
	case tool.ToolAskUserQuestion:
		return "Preparing question..."
	case tool.ToolSkill:
		return "Loading skill..."
	default:
		return "Executing..."
	}
}

// RenderSystemMessage renders a system/notice core.
func RenderSystemMessage(content string) string {
	return systemMsgStyle.Render(content) + "\n"
}

// RenderAgentNotice renders a background-agent notice (a subagent completion or
// interim report) — a "◆" marker plus the "<description> <status>" line in the
// accent tone, so it stands out from the dim system notices as an agent message.
func RenderAgentNotice(content string) string {
	return agentNoticeStyle.Render("◆ "+content) + "\n"
}

// renderDecision renders the auto-review outcome as a one-line annotation
// between a tool call and its result: a "↳" arrow plus the decision — green
// "auto-approved" when the judge let it through, amber "escalated" when it
// handed the call back to the user — and the judge's (few-word) reason dimmed
// after it. Returns "" when the call was not judged. The "  ↳" indent aligns the
// arrow with the "⎿" result trailer on the line below.
func renderDecision(v *core.ReviewDecision) string {
	if v == nil {
		return ""
	}
	label, style := "escalated", decisionEscalatedStyle
	if v.Approved {
		label, style = "auto-approved", decisionApprovedStyle
	}
	line := style.Render("  ↳ " + label)
	if v.Reason != "" {
		line += toolResultStyle.Render(" · " + v.Reason)
	}
	return line + "\n"
}

// ToolCallsParams holds the parameters for rendering tool calls.
type ToolCallsParams struct {
	ToolCalls         []core.ToolCall
	ToolCallsExpanded bool
	ResultMap         map[string]ToolResultData
	ParallelMode      bool
	// DockedModalActive marks the frames where a Question / Approval modal
	// owns the screen; the call it is asking about holds still until answered.
	DockedModalActive bool
	TaskActivity      map[int][]string
	PendingCalls      []core.ToolCall
	CurrentIdx        int
	// ToolStartedAt maps a running tool call's ID to when it began executing,
	// for the live elapsed timer on its row. Only in-flight calls appear.
	ToolStartedAt map[string]time.Time
	// ToolProgress maps a running command's ID to its latest output counter,
	// shown next to the timer. Empty for calls that have produced no output.
	ToolProgress map[string]string
	ModelName    string
	InputTokens  int
	OutputTokens int
	Blink        int
	AgentColors  map[string]string
	SpinnerView  string
	TaskOwnerMap map[string]string
	MDRenderer   *MDRenderer
	Width        int
	Interactive  bool
}

// expandHints reports whether "(ctrl+o to expand)" affordances belong on this
// pass. They ride the live tail only, and not while a docked modal owns the
// keyboard: routeKeypress hands every key to the active overlay, so ctrl+o
// reaches the modal (where it toggles a preview, or nothing) and never the row
// offering the hint.
func (p ToolCallsParams) expandHints() bool {
	return p.Interactive && !p.DockedModalActive
}

// ToolResultData holds the data needed to render a tool result inline.
type ToolResultData struct {
	ToolName    string
	Content     string
	IsError     bool
	Interactive bool
	Expanded    bool
	ToolInput   string
	Width       int
	Details     any
	// Nested indicates this result is rendering immediately below its tool call.
	// Nested results show a terminal state without repeating the tool name.
	Nested bool
	// Decision is the auto-review decision for this call (nil if it was not
	// judged), drawn as a colored line between the call and its result.
	Decision *core.ReviewDecision
}

// RenderToolCalls renders the tool calls section of an assistant core.
func RenderToolCalls(params ToolCallsParams) string {
	var sb strings.Builder

	for _, tc := range params.ToolCalls {
		switch tc.Name {
		case tool.ToolTaskCreate, tool.ToolTaskUpdate:
			continue
		case tool.ToolTaskGet:
			// A bare TaskGet (no taskId) lists every task; that overview is
			// already shown in the tracker panel, so skip the row as the old
			// silent TaskList did. A single-task lookup still renders below.
			if taskGetIsList(tc.Input) {
				continue
			}
		}
		if tool.IsAgentToolName(tc.Name) {
			agent := parseAgentInput(tc.Input)
			label := formatAgentLabel(agent)
			color := configuredAgentColor(agent, params.AgentColors)
			_, hasResult := params.ResultMap[tc.ID]
			if hasResult {
				sb.WriteString(renderAgentToolLine(label, params.Width, "●", color) + "\n")
			} else {
				// agentIcon blinks ●/○ off the frame counter; under a docked
				// modal it would advertise a subagent as working while the
				// turn waits on the user, same as a spinning tool row.
				icon := agentIcon(params.Blink)
				if params.DockedModalActive {
					icon = "●"
				}
				sb.WriteString(renderAgentToolLine(label, params.Width, icon, color))
				if !params.ToolCallsExpanded && params.expandHints() {
					sb.WriteString(ThinkingStyle.Render("  (ctrl+o to expand)"))
				}
				sb.WriteString("\n")
			}
			if params.ToolCallsExpanded && !hasResult {
				sb.WriteString(formatAgentDefinition(agent, params.Width))
			}
		} else if params.ToolCallsExpanded {
			toolLine := renderToolLine(tc.Name, params.Width)
			sb.WriteString(toolLine + "\n")
			var p map[string]any
			if err := json.Unmarshal([]byte(tc.Input), &p); err == nil {
				for k, v := range p {
					if s, ok := v.(string); ok {
						if len(s) > 80 {
							sb.WriteString(toolResultExpandedStyle.Render(fmt.Sprintf("%s:", k)) + "\n")
							sb.WriteString(toolResultExpandedStyle.Render(s) + "\n")
						} else {
							sb.WriteString(toolResultExpandedStyle.Render(fmt.Sprintf("%s: %s", k, s)) + "\n")
						}
					}
				}
			}
		} else {
			icon := toolCallIcon(tc, params)
			detail := runningRowDetail(tc, params)
			var row string
			if tc.Name == tool.ToolTaskGet && params.TaskOwnerMap != nil {
				args := extractTaskGetDisplay(tc.Input, params.TaskOwnerMap)
				row = renderToolLineWithIcon(fmt.Sprintf("%s(%s)", tc.Name, args), params.Width, icon) + "\n"
			} else if tc.Name == tool.ToolBash {
				row = renderBashToolCall(tc.Input, params.Width, icon, detail)
				detail = ""
			} else {
				args := extractToolArgs(tc.Input)
				row = renderToolLineWithIcon(fmt.Sprintf("%s(%s)", tc.Name, args), params.Width, icon) + "\n"
			}
			sb.WriteString(appendRowDetail(row, detail))
		}

		if resultData, ok := params.ResultMap[tc.ID]; ok {
			resultData.ToolInput = tc.Input
			resultData.Interactive = params.expandHints()
			resultData.Width = params.Width
			resultData.Nested = true
			// Decision sits between the call and its result, mirroring the
			// order things happened: judged → ran → produced this output.
			sb.WriteString(renderDecision(resultData.Decision))
			sb.WriteString(RenderToolResultInline(resultData, params.MDRenderer))
		} else if tool.IsAgentToolName(tc.Name) {
			limit := maxCompactAgentToolLines
			if params.ParallelMode {
				limit = maxParallelAgentToolLines
			}
			sb.WriteString(renderAgentActivityInline(tc, params.PendingCalls, params.TaskActivity, params.ToolCallsExpanded, limit, AgentStats{
				Model:        params.ModelName,
				InputTokens:  params.InputTokens,
				OutputTokens: params.OutputTokens,
			}))
		}
	}

	return sb.String()
}

// toolCallIcon picks a call's row glyph: the spinner while it is in flight, a
// static bullet once it has a result or is not the call being executed.
//
// A docked modal freezes every row. The call it is asking about was marked
// current and stamped as started by its PreToolEvent, which fires *before* the
// permission request, so it would otherwise spin as if it had been allowed to
// run. Nothing here can tell that call apart from a batch sibling that really
// is executing (the permission gate lives inside each tool's Execute, and a
// parallel batch runs one goroutine per call), so the freeze is deliberately
// blunt: a row that stalls for the length of the decision beats a row that
// claims a command is running before it was allowed to start. Distinguishing
// them needs the pending call's identity on the permission request itself —
// see #440.
func toolCallIcon(tc core.ToolCall, params ToolCallsParams) string {
	if _, done := params.ResultMap[tc.ID]; done {
		return "●"
	}
	if params.DockedModalActive {
		return "●"
	}

	idx := -1
	for i, pending := range params.PendingCalls {
		if pending.ID == tc.ID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return "●"
	}

	// In parallel mode every in-flight call spins; sequentially, only the
	// current call does.
	if params.ParallelMode || idx == params.CurrentIdx {
		return params.SpinnerView
	}

	return "●"
}

// runningRowDetail returns a dim " · 12s · 1.2k lines" suffix for a tool call
// that is still executing and has run at least a second — long enough to be
// worth telling the user it is alive, how long it has taken, and (for a command
// producing output) that output is flowing rather than hung. A call that has
// finished (its result is in) or has not started (no start stamp) gets "".
// The stamp map only holds in-flight calls, so membership already gates this:
// a not-yet-current sequential call has no entry and shows nothing.
func runningRowDetail(tc core.ToolCall, params ToolCallsParams) string {
	if _, done := params.ResultMap[tc.ID]; done {
		return ""
	}
	if params.DockedModalActive {
		return ""
	}
	started, ok := params.ToolStartedAt[tc.ID]
	if !ok {
		return ""
	}
	elapsed := formatElapsedTime(started)
	if elapsed == "" {
		return ""
	}
	detail := " · " + elapsed
	if progress := params.ToolProgress[tc.ID]; progress != "" {
		detail += " · " + progress
	}
	return toolResultStyle.Render(detail)
}

// appendRowDetail splices a trailing detail (an already-styled elapsed timer)
// into the first line of an already-rendered tool row, right before its
// newline — so a multi-line Bash block carries the timer on its header, not
// buried beside the command. Newlines never sit inside an ANSI escape, so
// slicing at the first one is safe.
func appendRowDetail(row, detail string) string {
	if detail == "" {
		return row
	}
	if nl := strings.IndexByte(row, '\n'); nl != -1 {
		return row[:nl] + detail + row[nl:]
	}
	return row + detail
}

// stripMarkdownHeading removes leading `#` markers from markdown headings.
func stripMarkdownHeading(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(trimmed, "#") {
		return line
	}
	stripped := strings.TrimLeft(trimmed, "#")
	stripped = strings.TrimPrefix(stripped, " ")
	indent := line[:len(line)-len(trimmed)]
	return indent + stripped
}
