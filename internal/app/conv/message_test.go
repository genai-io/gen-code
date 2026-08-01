package conv

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// The turn's first committed thinking block leads with the "✦" marker;
// progressively-committed blocks after it align under a blank continuation
// gutter so the glyph appears once, not on every block.
func TestRenderCommittedThinkingBlockGutter(t *testing.T) {
	first := RenderCommittedThinkingBlock("first reasoning block", true, 80, nil)
	if !strings.Contains(first, "✦") {
		t.Fatalf("first thinking block should lead with the ✦ marker, got %q", first)
	}
	cont := RenderCommittedThinkingBlock("continuation reasoning block", false, 80, nil)
	if strings.Contains(cont, "✦") {
		t.Fatalf("continuation thinking block should not repeat the ✦ marker, got %q", cont)
	}
}

// Committed reasoning lays out as markdown — headings and emphasis render rather
// than showing raw "###" / "**" — but flattened to the single muted tone so it
// reads as de-emphasized reasoning.
func TestRenderCommittedThinkingBlockRendersMarkdown(t *testing.T) {
	out := stripANSI(RenderCommittedThinkingBlock("### CoreDNS\n\nDNS service with **bold** text.", true, 80, NewMDRenderer(80)))
	if strings.Contains(out, "###") || strings.Contains(out, "**") {
		t.Errorf("reasoning should render markdown, not raw markers:\n%s", out)
	}
	for _, want := range []string{"CoreDNS", "bold"} {
		if !strings.Contains(out, want) {
			t.Errorf("reasoning text %q should survive:\n%s", want, out)
		}
	}
}

func Test_extractIntField(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		prefix   string
		expected int
	}{
		{
			name:     "valid turns",
			content:  "Agent: Explore\nStatus: completed\nTurns: 12\nTokens: 1500",
			prefix:   "Turns: ",
			expected: 12,
		},
		{
			name:     "turns at start",
			content:  "Turns: 5\nOther info",
			prefix:   "Turns: ",
			expected: 5,
		},
		{
			name:     "large turns number",
			content:  "Some text\nTurns: 999\nMore text",
			prefix:   "Turns: ",
			expected: 999,
		},
		{
			name:     "no turns field",
			content:  "Agent: Explore\nStatus: completed",
			prefix:   "Turns: ",
			expected: 0,
		},
		{
			name:     "empty content",
			content:  "",
			prefix:   "Turns: ",
			expected: 0,
		},
		{
			name:     "turns with zero",
			content:  "Turns: 0\n",
			prefix:   "Turns: ",
			expected: 0,
		},
		{
			name:     "single digit",
			content:  "Turns: 1",
			prefix:   "Turns: ",
			expected: 1,
		},
		{
			name:     "turns followed by text",
			content:  "Turns: 42abc",
			prefix:   "Turns: ",
			expected: 42,
		},
		{
			name:     "extract tokens",
			content:  "Turns: 10\nTokens: 1500",
			prefix:   "Tokens: ",
			expected: 1500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractIntField(tt.content, tt.prefix)
			if result != tt.expected {
				t.Errorf("extractIntField(%q, %q) = %d, want %d", tt.content, tt.prefix, result, tt.expected)
			}
		})
	}
}

func Test_extractToolArgsPreservesFullCommand(t *testing.T) {
	input := `{"command":"cd /Users/myan/Workspace/ideas/san && git describe --tags --abbrev=0 2>/dev/null"}`
	got := extractToolArgs(input)
	if !strings.Contains(got, "git describe --tags --abbrev=0") {
		t.Fatalf("extractToolArgs() = %q, want full command", got)
	}
}

func Test_renderBashToolCallSingleLineStylesDescriptionAsDimmed(t *testing.T) {
	raw := renderBashToolCall(`{"command":"git status","description":"inspect\nrepository"}`, 100, "●", "")
	out := stripANSI(raw)
	if out != "● Bash(git status) - inspect repository\n" {
		t.Fatalf("single-line command should render as one preview row, got %q", out)
	}
	if raw != lipgloss.JoinHorizontal(lipgloss.Top,
		toolCallStyle.Width(2).Render("●"),
		toolCallStyle.Render("Bash(git status)"),
		toolResultStyle.Render(" - inspect repository"),
	)+"\n" {
		t.Fatalf("single-line preview should dim only its description, got %q", raw)
	}
}

func Test_renderBashToolCallEmptyCommandUsesCommandBlock(t *testing.T) {
	out := stripANSI(renderBashToolCall(`{}`, 100, "●", ""))
	if strings.Contains(out, "Bash(") || !strings.Contains(out, "Bash\n  $ (no command)\n") {
		t.Fatalf("empty command should retain the Bash command block, got %q", out)
	}
}

func Test_renderBashToolCallMultiLineShowsEveryLine(t *testing.T) {
	input := `{"command":"for f in a b; do\n  echo \"$f\"\ndone","description":"loop over files"}`
	out := renderBashToolCall(input, 100, "●", "")

	// Every command line renders in the block, not folded away behind ctrl+o.
	for _, want := range []string{"for f in a b; do", `echo "$f"`, "done"} {
		if !strings.Contains(out, want) {
			t.Fatalf("multi-line command should show %q in place, got %q", want, out)
		}
	}
	plain := stripANSI(out)
	// Later physical command lines use the result connector, while the first
	// line retains the shell prompt.
	if !strings.Contains(plain, "  $ for f in a b; do\n  ┊   echo \"$f\"\n  ┊ done\n") {
		t.Fatalf("multi-line command should connect its continuation lines, got %q", plain)
	}
	// The description stays on the header in normal text, separated by a dash.
	if !strings.Contains(stripANSI(out), "Bash - loop over files") {
		t.Fatalf("multi-line command should show its description on the header, got %q", out)
	}
	// The raw command is never crammed into the Bash(...) one-line label.
	if strings.Contains(out, "Bash(for f") {
		t.Fatalf("multi-line command should not be crammed into a one-line label, got %q", out)
	}
}

func Test_renderBashToolCallShowsShellPrompt(t *testing.T) {
	// A long line in a multi-line command wraps within the existing terminal
	// block: the first row is led by a "$" prompt and later rows use connectors.
	// No gutter bar or background fill is added.
	long := `{"command":"git log --oneline --graph --all --decorate --abbrev-commit --since='2 weeks ago' | head -50\necho done"}`
	raw := renderBashToolCall(long, 70, "●", "")
	if strings.ContainsAny(raw, "│") || strings.Contains(raw, "48;2;") {
		t.Fatalf("command block should have no bar and no background, got %q", raw)
	}

	rows := strings.Split(strings.TrimRight(stripANSI(raw), "\n"), "\n")[1:]
	if len(rows) < 2 {
		t.Fatalf("expected the long command to wrap onto multiple rows, got %q", raw)
	}
	if !strings.HasPrefix(rows[0], bashPrompt) {
		t.Fatalf("first command row should carry the %q prompt: %q", bashPrompt, rows[0])
	}
	// The command text starts immediately after the single space following "$".
	if got := strings.TrimPrefix(rows[0], bashPrompt); !strings.HasPrefix(got, "git log") {
		t.Fatalf("command should follow the prompt without extra spacing: %q", rows[0])
	}

	// Wrapped rows use the connector so the command block stays connected to its
	// result summary. The prompt appears only on the first row.
	for i, row := range rows[1:] {
		if !strings.HasPrefix(row, nestedBodyPrefix) || strings.HasPrefix(row, bashPrompt) {
			t.Fatalf("continuation row %d should retain the connector without repeating the prompt: %q", i+1, row)
		}
	}

	// The prompt and nested result markers occupy the same column.
	if strings.IndexByte(bashPrompt, '$') != strings.Index(nestedTrailerPrefix, "└") {
		t.Fatalf("prompt $ column must equal the result └ column")
	}
}

func Test_renderBashToolCallKeepsConnectorsContinuousAcrossBlankLines(t *testing.T) {
	for _, input := range []string{
		`{"command":"first\n\nthird"}`,
		`{"command":"first\n   \nthird"}`,
	} {
		out := stripANSI(renderBashToolCall(input, 100, "●", ""))
		if !strings.Contains(out, bashPrompt+"first\n"+nestedBodyPrefix+"\n"+nestedBodyPrefix+"third\n") {
			t.Fatalf("blank command lines should retain the connector, got %q", out)
		}
	}

	if out := stripANSI(renderBashToolCall(`{"command":"first\n"}`, 100, "●", "")); !strings.Contains(out, bashPrompt+"first\n"+nestedBodyPrefix+"\n") {
		t.Fatalf("trailing blank command line should retain the connector, got %q", out)
	}
	if out := stripANSI(renderBashToolCall(`{"command":"\nfirst"}`, 100, "●", "")); strings.Contains(out, nestedBodyPrefix+"\n") || !strings.Contains(out, bashPrompt+"first\n") {
		t.Fatalf("leading blank lines should be skipped and first visible command should retain the shell prompt, got %q", out)
	}
	if out := stripANSI(renderBashToolCall(`{"command":"   "}`, 100, "●", "")); !strings.Contains(out, bashPrompt+"(no command)\n") {
		t.Fatalf("whitespace-only command should use the empty-command fallback, got %q", out)
	}
}

func Test_renderBashToolCallMovesOversizedSingleLineCommandToFullBlock(t *testing.T) {
	command := "gh pr view 414 --repo genai-io/san --json number,title,state,url,author,baseRefName,headRefName,headRepositoryOwner,body,comments,reviews,reviewRequests,statusCheckRollup"
	input := `{"command":` + strconv.Quote(command) + `,"description":"Inspect pull request details and review status"}`
	const width = 80

	rendered := stripANSI(renderBashToolCall(input, width, "●", ""))
	if !strings.HasPrefix(rendered, "● Bash - Inspect pull request details and review status\n") {
		t.Fatalf("oversized command should use a Bash header, got %q", rendered)
	}
	if strings.Contains(rendered, "...") || strings.Contains(rendered, "…") {
		t.Fatalf("oversized command must not be abbreviated, got %q", rendered)
	}
	var reconstructed strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")[1:] {
		switch {
		case strings.HasPrefix(line, bashPrompt):
			reconstructed.WriteString(strings.TrimPrefix(line, bashPrompt))
		case strings.HasPrefix(line, nestedBodyPrefix):
			reconstructed.WriteString(strings.TrimPrefix(line, nestedBodyPrefix))
		default:
			t.Fatalf("unexpected command block row %q", line)
		}
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("command row width %d exceeds terminal width %d: %q", w, width, line)
		}
	}
	if got := strings.ReplaceAll(reconstructed.String(), " ", ""); got != strings.ReplaceAll(command, " ", "") {
		t.Fatalf("wrapped command lost content: got %q, want %q", reconstructed.String(), command)
	}
}

func TestRenderModeStatusShowsTokenUsageWithModel(t *testing.T) {
	rendered := RenderModeStatus(OperationModeParams{
		ModelName:        "claude-sonnet-4-6",
		InputTokens:      142000,
		InputLimit:       200000,
		ConversationCost: llm.NewCostTotal(llm.Money{Amount: 0.04, Currency: llm.CurrencyUSD}),
		ShowContextBar:   true,
		Width:            120,
	})
	visible := stripANSI(rendered)
	if !strings.Contains(visible, "claude-sonnet-4-6") {
		t.Fatalf("RenderModeStatus() = %q, want model name", visible)
	}
	if !strings.Contains(visible, "[") || !strings.Contains(visible, "] 71%") {
		t.Fatalf("RenderModeStatus() = %q, want bar with percent", visible)
	}
	// The numeric label rides alongside the bar.
	if !strings.Contains(visible, "ctx 142.0k/200.0k") {
		t.Fatalf("RenderModeStatus() = %q, want numeric ctx label", visible)
	}
	// Cost segment must still render.
	if !strings.Contains(visible, "$0.04") {
		t.Fatalf("RenderModeStatus() = %q, want cost segment", visible)
	}
}

// TestRenderModeStatusHidesBarByDefault confirms the visual bar is opt-in:
// with ShowContextBar unset the [██░░] bar and its percent are gone, but the
// numeric "ctx X/Y" label still shows.
func TestRenderModeStatusHidesBarByDefault(t *testing.T) {
	rendered := RenderModeStatus(OperationModeParams{
		ModelName:   "claude-sonnet-4-6",
		InputTokens: 142000,
		InputLimit:  200000,
		Width:       120,
	})
	visible := stripANSI(rendered)
	if !strings.Contains(visible, "ctx 142.0k/200.0k") {
		t.Fatalf("numeric ctx label should still show when bar is off; got %q", visible)
	}
	if strings.Contains(visible, "█") || strings.Contains(visible, "71%") {
		t.Fatalf("visual bar should be hidden by default; got %q", visible)
	}
}

func TestRenderModeStatusShowsBarWhenEnabled(t *testing.T) {
	rendered := RenderModeStatus(OperationModeParams{
		ModelName:      "claude-sonnet-4-6",
		InputTokens:    190000,
		InputLimit:     200000,
		ShowContextBar: true,
		Width:          120,
	})
	visible := stripANSI(rendered)
	if !strings.Contains(visible, "claude-sonnet-4-6") {
		t.Fatalf("want model name in %q", visible)
	}
	// Bar should appear exactly once: fail if EITHER count is wrong.
	if strings.Count(visible, "] ") != 1 || strings.Count(visible, "%") != 1 {
		t.Fatalf("want unified context display (single bar + percent) in %q", visible)
	}
	if !strings.Contains(visible, "compact at") && !strings.Contains(visible, "auto-compact") {
		t.Fatalf("want auto-compact hint in %q", visible)
	}
}

func TestRenderModeStatusShowsCompressionsBadgeWhenNonZero(t *testing.T) {
	rendered := RenderModeStatus(OperationModeParams{
		ModelName:    "claude-sonnet-4-6",
		InputTokens:  1000,
		InputLimit:   200000,
		Compressions: 3,
		Width:        120,
	})
	visible := stripANSI(rendered)
	if !strings.Contains(visible, "compacted ×3") {
		t.Fatalf("want 'compacted ×3' badge in %q", visible)
	}
}

func TestRenderModeStatusHidesBadgeWhenZero(t *testing.T) {
	rendered := RenderModeStatus(OperationModeParams{
		ModelName:    "claude-sonnet-4-6",
		InputTokens:  1000,
		InputLimit:   200000,
		Compressions: 0,
		Width:        120,
	})
	visible := stripANSI(rendered)
	if strings.Contains(visible, "compacted") {
		t.Fatalf("badge should be hidden when zero; got %q", visible)
	}
}

func TestRenderModeStatusShowsPlaceholderWhenLimitUnknown(t *testing.T) {
	// When InputLimit == 0 (limit unknown), the bar must still render with
	// a placeholder so the gap stays visible and actionable, instead of
	// silently hiding the entire context segment.
	rendered := RenderModeStatus(OperationModeParams{
		ModelName:      "some-model",
		InputTokens:    5000,
		InputLimit:     0,
		ShowContextBar: true,
		Width:          120,
	})
	visible := stripANSI(rendered)
	if !strings.Contains(visible, "[----------]") {
		t.Errorf("want placeholder bar [----------] when limit unknown; got %q", visible)
	}
	if !strings.Contains(visible, "--") {
		t.Errorf("want '--' percent label when limit unknown; got %q", visible)
	}
}

func TestRenderModeStatusShowsTemporaryStatusMessage(t *testing.T) {
	rendered := RenderModeStatus(OperationModeParams{
		ModelName:     "kimi-k2.6",
		StatusMessage: "compacted",
		Width:         80,
	})
	for _, want := range []string{"kimi-k2.6", "compacted"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("RenderModeStatus() = %q, want %q", rendered, want)
		}
	}
}

// The bottom status line must carry the ctx readout but never the old
// per-turn "↑… ↓…" usage summary (removed to avoid a second, confusingly
// scoped token figure above the input area).
func TestRenderModeStatusShowsCtxWithoutTurnUsageArrows(t *testing.T) {
	visible := stripANSI(RenderModeStatus(OperationModeParams{
		ModelName:   "gpt-test",
		InputTokens: 164600,
		InputLimit:  272000,
		Width:       120,
	}))
	if !strings.Contains(visible, "ctx") {
		t.Fatalf("RenderModeStatus() = %q, want the ctx label", visible)
	}
	if strings.ContainsAny(visible, "↑↓") {
		t.Fatalf("RenderModeStatus() = %q, must not render per-turn usage arrows", visible)
	}
}

func TestRenderQueuePreviewShowsShadowPromptAndEditHint(t *testing.T) {
	rendered := stripANSI(RenderQueuePreview([]QueuePreviewItem{{
		Content: "Codex review 建议如何运行?",
	}}, -1, 80))

	for _, want := range []string{"❭ Codex review", "↑ edit"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("RenderQueuePreview() = %q, want %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "1.") {
		t.Fatalf("RenderQueuePreview() = %q, should not number items", rendered)
	}
}

func TestRenderQueuePreviewEditingShowsFocusBarAndKeys(t *testing.T) {
	rendered := stripANSI(RenderQueuePreview([]QueuePreviewItem{
		{Content: "first queued"},
		{Content: "second queued"},
	}, 0, 80))

	for _, want := range []string{"▎", "enter done · ctrl+c delete"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("RenderQueuePreview() = %q, want %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "↑ edit") {
		t.Fatalf("RenderQueuePreview() = %q, idle hint should be replaced while editing", rendered)
	}
}

func TestRenderToolCallsWrapsLongSingleLineBashCommandWithoutTruncating(t *testing.T) {
	const width = 100
	params := ToolCallsParams{
		ToolCalls: []core.ToolCall{{
			ID:    "tc-1",
			Name:  "Bash",
			Input: `{"command":"cd /Users/myan/Workspace/ideas/san && git describe --tags --abbrev=0 2>/dev/null","description":"inspect the latest repository tag and ignore command errors"}`,
		}},
		ResultMap: map[string]ToolResultData{},
		Width:     width,
	}

	rendered := stripANSI(RenderToolCalls(params))
	fullCommand := "Bash(cd /Users/myan/Workspace/ideas/san && git describe --tags --abbrev=0 2>/dev/null)"
	if !strings.HasPrefix(rendered, "● "+fullCommand) {
		t.Fatalf("RenderToolCalls() = %q, want the complete Bash command", rendered)
	}
	if strings.Count(strings.TrimSuffix(rendered, "\n"), "\n") != 0 {
		t.Fatalf("a command that fits should remain on one row, got %q", rendered)
	}
	if !strings.Contains(rendered, "...") {
		t.Fatalf("RenderToolCalls() = %q, long description may be abbreviated", rendered)
	}
	if strings.Contains(rendered, "abbrev=0...") || strings.Contains(rendered, "abbrev=0…") {
		t.Fatalf("RenderToolCalls() = %q, command itself was abbreviated", rendered)
	}
	if lineWidth := lipgloss.Width(strings.TrimSuffix(rendered, "\n")); lineWidth > width {
		t.Fatalf("rendered line width %d exceeds terminal width %d", lineWidth, width)
	}
}

func Test_renderBashToolCallKeepsFullCommandWhenRunningDetailNeedsSpace(t *testing.T) {
	const width = 70
	detail := toolResultStyle.Render(" · 12s · 1.2k lines")
	rendered := stripANSI(renderBashToolCall(
		`{"command":"git log --oneline --graph --all --decorate --abbrev-commit","description":"inspect repository history"}`,
		width, "⋯", detail,
	))

	if !strings.HasPrefix(rendered, "⋯ Bash - inspect repository history · 12s · 1.2k lines\n") {
		t.Fatalf("running command should move to a block when the full label does not fit, got %q", rendered)
	}
	if !strings.Contains(rendered, bashPrompt+"git log --oneline --graph --all --decorate --abbrev-commit\n") {
		t.Fatalf("running command was truncated instead of rendered in full, got %q", rendered)
	}
	if strings.Contains(rendered, "...") || strings.Contains(rendered, "…") {
		t.Fatalf("running command must not be abbreviated, got %q", rendered)
	}
	for _, line := range strings.Split(strings.TrimSuffix(rendered, "\n"), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("running command line width %d exceeds terminal width %d: %q", w, width, line)
		}
	}
}

func TestRenderToolCallsShowsRunningStateForPendingBash(t *testing.T) {
	params := ToolCallsParams{
		ToolCalls: []core.ToolCall{{
			ID:    "tc-1",
			Name:  "Bash",
			Input: `{"command":"find /Users/myan -name test"}`,
		}},
		ResultMap: map[string]ToolResultData{},
		PendingCalls: []core.ToolCall{{
			ID:    "tc-1",
			Name:  "Bash",
			Input: `{"command":"find /Users/myan -name test"}`,
		}},
		CurrentIdx:  0,
		SpinnerView: "⋯",
		Width:       100,
	}

	rendered := stripANSI(RenderToolCalls(params))
	if !strings.Contains(rendered, "⋯ Bash(find /Users/myan -name test)\n") {
		t.Fatalf("RenderToolCalls() = %q, want spinner on the Bash preview", rendered)
	}
	if strings.Contains(rendered, "running...") {
		t.Fatalf("RenderToolCalls() = %q, should not add extra running text", rendered)
	}
}

func TestRenderToolCallsShowsElapsedTimerOnRunningBash(t *testing.T) {
	call := core.ToolCall{ID: "tc-1", Name: "Bash", Input: `{"command":"npm test"}`}
	params := ToolCallsParams{
		ToolCalls:     []core.ToolCall{call},
		ResultMap:     map[string]ToolResultData{},
		PendingCalls:  []core.ToolCall{call},
		CurrentIdx:    0,
		SpinnerView:   "⋯",
		Width:         100,
		ToolStartedAt: map[string]time.Time{"tc-1": time.Now().Add(-12 * time.Second)},
	}

	rendered := stripANSI(RenderToolCalls(params))
	if !strings.Contains(rendered, "⋯ Bash(npm test) · 12s\n") {
		t.Fatalf("RenderToolCalls() = %q, want spinner and timer on the Bash preview", rendered)
	}
	if !strings.Contains(rendered, "· 12s") {
		t.Fatalf("RenderToolCalls() = %q, want an elapsed timer on the running row", rendered)
	}
}

func TestRenderToolCallsShowsOutputCounterNextToTimer(t *testing.T) {
	call := core.ToolCall{ID: "tc-1", Name: "Bash", Input: `{"command":"npm test"}`}
	params := ToolCallsParams{
		ToolCalls:     []core.ToolCall{call},
		ResultMap:     map[string]ToolResultData{},
		PendingCalls:  []core.ToolCall{call},
		CurrentIdx:    0,
		SpinnerView:   "⋯",
		Width:         100,
		ToolStartedAt: map[string]time.Time{"tc-1": time.Now().Add(-8 * time.Second)},
		ToolProgress:  map[string]string{"tc-1": "1.2k lines"},
	}

	rendered := stripANSI(RenderToolCalls(params))
	if !strings.Contains(rendered, "· 8s · 1.2k lines") {
		t.Fatalf("RenderToolCalls() = %q, want the timer and output counter together", rendered)
	}
}

func TestRenderToolCallsHidesCounterWithoutTimer(t *testing.T) {
	call := core.ToolCall{ID: "tc-1", Name: "Bash", Input: `{"command":"npm test"}`}
	// Output has arrived but the call is under a second old: no timer, so no
	// counter either — a sub-second row stays clean.
	params := ToolCallsParams{
		ToolCalls:     []core.ToolCall{call},
		ResultMap:     map[string]ToolResultData{},
		PendingCalls:  []core.ToolCall{call},
		CurrentIdx:    0,
		SpinnerView:   "⋯",
		Width:         100,
		ToolStartedAt: map[string]time.Time{"tc-1": time.Now()},
		ToolProgress:  map[string]string{"tc-1": "42 lines"},
	}

	if got := stripANSI(RenderToolCalls(params)); strings.Contains(got, "42 lines") {
		t.Fatalf("RenderToolCalls() = %q, want no counter before the one-second timer appears", got)
	}
}

func TestRenderToolCallsHidesTimerForSubSecondAndCompletedCalls(t *testing.T) {
	call := core.ToolCall{ID: "tc-1", Name: "Bash", Input: `{"command":"npm test"}`}
	base := ToolCallsParams{
		ToolCalls:    []core.ToolCall{call},
		PendingCalls: []core.ToolCall{call},
		CurrentIdx:   0,
		SpinnerView:  "⋯",
		Width:        100,
	}

	// Under a second old: too brief to be worth a timer.
	fresh := base
	fresh.ResultMap = map[string]ToolResultData{}
	fresh.ToolStartedAt = map[string]time.Time{"tc-1": time.Now()}
	if got := stripANSI(RenderToolCalls(fresh)); strings.Contains(got, "·") {
		t.Fatalf("RenderToolCalls() = %q, want no timer under a second", got)
	}

	// Finished: the result is in, so the row is static with no timer.
	done := base
	done.ResultMap = map[string]ToolResultData{"tc-1": {ToolName: "Bash", Content: "ok"}}
	done.ToolStartedAt = map[string]time.Time{"tc-1": time.Now().Add(-time.Minute)}
	if got := stripANSI(RenderToolCalls(done)); strings.Contains(got, "·") {
		t.Fatalf("RenderToolCalls() = %q, want no timer once the call has completed", got)
	}
}

func TestRenderToolCallsPutsTimerOnBashHeaderForMultilineCommand(t *testing.T) {
	call := core.ToolCall{ID: "tc-1", Name: "Bash", Input: `{"command":"line one\nline two\nline three"}`}
	params := ToolCallsParams{
		ToolCalls:     []core.ToolCall{call},
		ResultMap:     map[string]ToolResultData{},
		PendingCalls:  []core.ToolCall{call},
		CurrentIdx:    0,
		SpinnerView:   "⋯",
		Width:         100,
		ToolStartedAt: map[string]time.Time{"tc-1": time.Now().Add(-90 * time.Second)},
	}

	rendered := stripANSI(RenderToolCalls(params))
	header := strings.SplitN(rendered, "\n", 2)[0]
	if !strings.Contains(header, "· 1m 30s") {
		t.Fatalf("RenderToolCalls() header = %q, want the timer on the Bash header line", header)
	}
	if strings.Contains(rendered, "$  line one · 1m 30s") {
		t.Fatalf("RenderToolCalls() = %q, timer must not land on the command line", rendered)
	}
}

func TestRenderActiveContentShowsRunningStateForPendingWebFetch(t *testing.T) {
	call := core.ToolCall{
		ID:    "tc-1",
		Name:  "WebFetch",
		Input: `{"url":"https://github.com/features/copilot/plans"}`,
	}
	params := RenderContext{
		Messages: []core.ChatMessage{{
			Role:      core.RoleAssistant,
			ToolCalls: []core.ToolCall{call},
		}},
		PendingCalls: []core.ToolCall{call},
		CurrentIdx:   0,
		SpinnerView:  "⋯",
		Width:        100,
	}
	params.InlinedResults = PrecomputeInlinedResults(params.Messages, 0)

	rendered := stripANSI(RenderActiveContent(params))
	if !strings.Contains(rendered, "⋯ WebFetch(https://github.com/features/copilot/plans)") {
		t.Fatalf("RenderActiveContent() = %q, want pending WebFetch spinner", rendered)
	}
}

func TestPrecomputeInlinedResultsFromScopesToActiveRange(t *testing.T) {
	// assistant(0) with a tool call, then its result(1), then a fresh
	// assistant(2) with a call and its result(3).
	msgs := []core.ChatMessage{
		{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{ID: "a", Name: "Bash"}}},
		{Role: core.RoleUser, ToolResult: &core.ToolResult{ToolCallID: "a", ToolName: "Bash"}},
		{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{ID: "b", Name: "Bash"}}},
		{Role: core.RoleUser, ToolResult: &core.ToolResult{ToolCallID: "b", ToolName: "Bash"}},
	}

	// from=0 inlines both results with their owners.
	all := PrecomputeInlinedResults(msgs, 0)
	if !all.IsResultInlined(1) || !all.IsResultInlined(3) {
		t.Fatalf("from=0 should inline both results, got %+v", all.resultOwner)
	}

	// from=2 (first turn committed) scans only the active tail: result(3)
	// still inlines under assistant(2); the committed result(1) is skipped.
	active := PrecomputeInlinedResults(msgs, 2)
	if active.IsResultInlined(1) {
		t.Errorf("from=2 should not scan the committed result at index 1")
	}
	if !active.IsResultInlined(3) || active.ownerOf(3) != 2 {
		t.Errorf("from=2 should still inline the active result at index 3 under assistant 2")
	}
}

func TestRenderToolCallsShowsCompletedIconForResultEvenWhenPending(t *testing.T) {
	call := core.ToolCall{
		ID:    "tc-1",
		Name:  "WebFetch",
		Input: `{"url":"https://github.com/features/copilot/plans"}`,
	}
	params := ToolCallsParams{
		ToolCalls:    []core.ToolCall{call},
		PendingCalls: []core.ToolCall{call},
		CurrentIdx:   0,
		SpinnerView:  "⋯",
		Width:        100,
		ResultMap: map[string]ToolResultData{
			"tc-1": {ToolName: "WebFetch", Content: "done"},
		},
	}

	rendered := stripANSI(RenderToolCalls(params))
	if !strings.Contains(rendered, "● WebFetch(https://github.com/features/copilot/plans)") {
		t.Fatalf("RenderToolCalls() = %q, want completed WebFetch icon", rendered)
	}
	if strings.Contains(rendered, "⋯ WebFetch") {
		t.Fatalf("RenderToolCalls() = %q, should not show spinner for completed result", rendered)
	}
	if strings.Count(rendered, "WebFetch") != 1 || !strings.Contains(rendered, "  └ 4 B\n") {
		t.Fatalf("nested WebFetch should not repeat its tool name, got %q", rendered)
	}
}

func TestRenderToolCallsNestsGenericFailureWithoutRepeatingToolName(t *testing.T) {
	call := core.ToolCall{ID: "tc-1", Name: "WebSearch", Input: `{"query":"test"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "WebSearch", Content: "Error: request failed\n   \nretry later\n\n", IsError: true},
		},
		Width: 100,
	}))

	if strings.Count(rendered, "WebSearch") != 1 {
		t.Fatalf("WebSearch should be named only in its call header, got %q", rendered)
	}
	if !strings.Contains(rendered, "  ┊ request failed\n    \n  ┊ retry later\n  └ failed\n") {
		t.Fatalf("generic failure should use the nested result layout without connector-only rows, got %q", rendered)
	}
	if strings.Contains(rendered, "  ┊ \n") {
		t.Fatalf("blank output lines should not render connector-only rows, got %q", rendered)
	}
}

func TestRenderToolCallsShowsGapForPendingAgent(t *testing.T) {
	params := ToolCallsParams{
		ToolCalls: []core.ToolCall{{
			ID:    "tc-1",
			Name:  "Agent",
			Input: `{"name":"Explore","description":"HA code structure","prompt":"Inspect the codebase"}`,
		}},
		ResultMap: map[string]ToolResultData{},
		PendingCalls: []core.ToolCall{{
			ID:    "tc-1",
			Name:  "Agent",
			Input: `{"name":"Explore","description":"HA code structure","prompt":"Inspect the codebase"}`,
		}},
		CurrentIdx:  0,
		Blink:       agentBlinkTicks,
		SpinnerView: "◓",
		Width:       100,
	}

	rendered := stripANSI(RenderToolCalls(params))
	want := agentIcon(params.Blink) + " Agent - Explore: HA code structure"
	if !strings.Contains(rendered, want) {
		t.Fatalf("RenderToolCalls() = %q, want a single visible gap before explicit agent label", rendered)
	}
}

func TestRenderToolCallsPreservesAgentName(t *testing.T) {
	call := core.ToolCall{
		ID:    "tc-1",
		Name:  "Agent",
		Input: `{"name":"general-purpose","description":"audit git changes","mode":"explore"}`,
	}
	params := ToolCallsParams{
		ToolCalls:    []core.ToolCall{call},
		ResultMap:    map[string]ToolResultData{},
		PendingCalls: []core.ToolCall{call},
		CurrentIdx:   0,
		Blink:        agentBlinkTicks,
		SpinnerView:  "◓",
		Width:        100,
	}

	rendered := stripANSI(RenderToolCalls(params))
	want := agentIcon(params.Blink) + " Agent - general-purpose: audit git changes"
	if !strings.Contains(rendered, want) {
		t.Fatalf("RenderToolCalls() = %q, want named agent label", rendered)
	}
}

func TestRenderToolCallsShowsSingleAgentRuntimeActivity(t *testing.T) {
	call := core.ToolCall{
		ID:    "tc-1",
		Name:  "Agent",
		Input: `{"name":"Explore","description":"audit git changes before review","prompt":"Inspect the codebase","mode":"explore"}`,
	}
	params := ToolCallsParams{
		ToolCalls:    []core.ToolCall{call},
		ResultMap:    map[string]ToolResultData{},
		PendingCalls: []core.ToolCall{call},
		CurrentIdx:   0,
		TaskActivity: map[int][]string{
			0: {
				"Mode: explore · max 100 turns",
				"Thinking...",
				"Read(internal/tool/schema_agent.go)",
				"Grep(ContinueAgent)",
				"Read(internal/app/conv/message.go)",
				"Grep(renderAgentActivityInline)",
			},
		},
		ModelName:    "gpt-5.4-mini",
		InputTokens:  18500,
		OutputTokens: 467,
		Blink:        agentBlinkTicks,
		SpinnerView:  "◓",
		Width:        120,
	}

	rendered := stripANSI(RenderToolCalls(params))
	want := agentIcon(params.Blink) + " Agent - Explore: audit git changes before review"
	if !strings.Contains(rendered, want) {
		t.Fatalf("RenderToolCalls() = %q, want agent header", rendered)
	}
	if !strings.Contains(rendered, "model: gpt-5.4-mini") || !strings.Contains(rendered, "mode: explore") || !strings.Contains(rendered, "tools: 4") {
		t.Fatalf("RenderToolCalls() = %q, want runtime summary", rendered)
	}
	if strings.Contains(rendered, "Read(internal/tool/schema_agent.go)") {
		t.Fatalf("RenderToolCalls() = %q, want only the latest three tool calls", rendered)
	}
	for _, want := range []string{"Grep(ContinueAgent)", "Read(internal/app/conv/message.go)", "Grep(renderAgentActivityInline)"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("RenderToolCalls() = %q, missing recent tool call %q", rendered, want)
		}
	}
}

func TestRenderToolCallsShowsAgentStatusBeforeToolCalls(t *testing.T) {
	call := core.ToolCall{
		ID:    "tc-1",
		Name:  "Agent",
		Input: `{"name":"Explore","description":"audit git changes","prompt":"Inspect the codebase","mode":"explore"}`,
	}
	params := ToolCallsParams{
		ToolCalls:    []core.ToolCall{call},
		ResultMap:    map[string]ToolResultData{},
		PendingCalls: []core.ToolCall{call},
		CurrentIdx:   0,
		TaskActivity: map[int][]string{
			0: {
				"Mode: explore · max 100 turns",
				"Thinking...",
			},
		},
		ModelName:   "gpt-5.4-mini",
		SpinnerView: "◓",
		Width:       120,
	}

	rendered := stripANSI(RenderToolCalls(params))
	if !strings.Contains(rendered, "model: gpt-5.4-mini") || !strings.Contains(rendered, "mode: explore") {
		t.Fatalf("RenderToolCalls() = %q, want runtime summary before tool calls", rendered)
	}
	if !strings.Contains(rendered, "Thinking...") {
		t.Fatalf("RenderToolCalls() = %q, want status before tool calls", rendered)
	}
}

func TestRenderToolCallsUsesActivityModelForAgentSummary(t *testing.T) {
	call := core.ToolCall{
		ID:    "tc-1",
		Name:  "Agent",
		Input: `{"name":"Explore","description":"audit git changes","prompt":"Inspect the codebase","mode":"explore","model":"sonnet"}`,
	}
	params := ToolCallsParams{
		ToolCalls:    []core.ToolCall{call},
		ResultMap:    map[string]ToolResultData{},
		PendingCalls: []core.ToolCall{call},
		CurrentIdx:   0,
		TaskActivity: map[int][]string{
			0: {
				"Model: gpt-5.5",
				"Mode: explore · max 100 turns",
				"Thinking...",
			},
		},
		ModelName:   "gpt-5.4-mini",
		SpinnerView: "◓",
		Width:       120,
	}

	rendered := stripANSI(RenderToolCalls(params))
	if !strings.Contains(rendered, "model: gpt-5.5") {
		t.Fatalf("RenderToolCalls() = %q, want activity model", rendered)
	}
	if strings.Contains(rendered, "model: sonnet") {
		t.Fatalf("RenderToolCalls() = %q, should not use raw tool input model", rendered)
	}
}

func TestRenderToolCallsUsesActivityUsageForAgentTokens(t *testing.T) {
	call := core.ToolCall{
		ID:    "tc-1",
		Name:  "Agent",
		Input: `{"name":"general-purpose","description":"audit git changes","mode":"explore"}`,
	}
	params := ToolCallsParams{
		ToolCalls:    []core.ToolCall{call},
		ResultMap:    map[string]ToolResultData{},
		PendingCalls: []core.ToolCall{call},
		CurrentIdx:   0,
		TaskActivity: map[int][]string{
			0: {
				"Model: kimi-k2.6",
				"Mode: explore · max 100 turns",
				"Usage: input=8300 output=272",
				"Read(README.md)",
				"Usage: input=9200 output=410",
			},
		},
		ModelName:    "gpt-5.4-mini",
		InputTokens:  100,
		OutputTokens: 10,
		SpinnerView:  "◓",
		Width:        120,
	}

	rendered := stripANSI(RenderToolCalls(params))
	if !strings.Contains(rendered, "tokens: ↑9.2k ↓410") {
		t.Fatalf("RenderToolCalls() = %q, want latest activity token usage", rendered)
	}
	if strings.Contains(rendered, "tools: 3") {
		t.Fatalf("RenderToolCalls() = %q, usage lines should not count as tools", rendered)
	}
}

func Test_formatToolResultSizeUsesNoOutputForEmptyContent(t *testing.T) {
	if got := formatToolResultSize("Bash", ""); got != "no output" {
		t.Fatalf("formatToolResultSize() = %q, want %q", got, "no output")
	}
}

func TestRenderToolResultInlineShowsEditSummary(t *testing.T) {
	details := toolresult.FileChangeDetails{
		Path:         "internal/app/view.go",
		EditCount:    2,
		AddedLines:   3,
		RemovedLines: 1,
		UnifiedDiff:  "@@ -1 +1 @@\n-old\n+new",
	}
	result := stripANSI(RenderToolResultInline(ToolResultData{ToolName: "Edit", Details: details}, nil))
	if !strings.Contains(result, "+3 -1") {
		t.Fatalf("successful Edit summary = %q", result)
	}
	// The diff now shows by default, in gutter-and-marker form.
	if !strings.Contains(result, "1 - old") || !strings.Contains(result, "1 + new") {
		t.Fatalf("Edit result should show the diff rows, got %q", result)
	}

	errResult := stripANSI(RenderToolResultInline(ToolResultData{ToolName: "Edit", Content: "Error: edits[0]: oldText was not found", IsError: true}, nil))
	if !strings.Contains(errResult, "Edit → failed") {
		t.Fatalf("Edit error summary should show failure, got %q", errResult)
	}
	if strings.Contains(errResult, "Edit → Error:") || strings.Contains(errResult, "Error: edits") {
		t.Fatalf("Edit error should not repeat the error prefix, got %q", errResult)
	}
	if !strings.Contains(errResult, "\n     edits[0]: oldText was not found\n") {
		t.Fatalf("Edit error reason should align with Edit, got %q", errResult)
	}

	bashErrResult := stripANSI(RenderToolResultInline(ToolResultData{ToolName: "Bash", Content: "command failed", IsError: true}, nil))
	if !strings.Contains(bashErrResult, "Bash → failed") {
		t.Fatalf("standalone Bash error should retain its tool label, got %q", bashErrResult)
	}
}

func TestRenderToolCallsNestsBashResultUnderCommand(t *testing.T) {
	call := core.ToolCall{ID: "bash-1", Name: "Bash", Input: `{"command":"git status"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Bash", Content: "on main\nnothing to commit", Expanded: true},
		},
		Width: 100,
	}))

	if strings.Count(rendered, "Bash") != 1 {
		t.Fatalf("Bash should be named only in its call header, got %q", rendered)
	}
	if !strings.Contains(rendered, "Bash(git status)\n  ┊ on main\n  ┊ nothing to commit\n  └ 2 lines\n") {
		t.Fatalf("Bash result should be a dotted, nested line summary followed by expanded output, got %q", rendered)
	}
}

func TestRenderToolCallsNestsBashFailureUnderCommand(t *testing.T) {
	call := core.ToolCall{ID: "bash-1", Name: "Bash", Input: `{"command":"false"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {
				ToolName: "Bash",
				Content:  "test output\nstderr detail\nError: exit code 1",
				IsError:  true,
				Details:  toolresult.BashDetails{Error: "exit code 1", LineCount: 2},
			},
		},
		Width: 100,
	}))

	if strings.Count(rendered, "Bash") != 1 {
		t.Fatalf("Bash should be named only in its call header, got %q", rendered)
	}
	if !strings.Contains(rendered, "  ┊ test output\n  ┊ stderr detail\n  └ failed · exit code 1 · 2 lines\n") {
		t.Fatalf("Bash failure should move its error into the summary without counting it as output, got %q", rendered)
	}
	if strings.Contains(rendered, "Error: exit code 1") {
		t.Fatalf("Bash failure should not repeat its model-facing error line in the output body, got %q", rendered)
	}
}

func TestRenderToolCallsKeepsBashOutputConnectorAcrossBlankLines(t *testing.T) {
	call := core.ToolCall{ID: "bash-1", Name: "Bash", Input: `{"command":"printf output; exit 1"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {
				ToolName: "Bash",
				Content:  "first\n\nthird\nError: exit code 1",
				IsError:  true,
				Details:  toolresult.BashDetails{Error: "exit code 1", LineCount: 3},
			},
		},
		Width: 100,
	}))

	if !strings.Contains(rendered, "  ┊ first\n  ┊ \n  ┊ third\n  └ failed · exit code 1 · 3 lines\n") {
		t.Fatalf("blank Bash output lines should retain the connector, got %q", rendered)
	}
}

func TestRenderToolCallsRestoresLegacyBashFailureSummary(t *testing.T) {
	call := core.ToolCall{ID: "bash-1", Name: "Bash", Input: `{"command":"false"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {
				ToolName: "Bash",
				Content:  "first\n\nthird\nError: exit code 1",
				IsError:  true,
			},
		},
		Width: 100,
	}))

	if !strings.Contains(rendered, "  ┊ first\n  ┊ \n  ┊ third\n  └ failed · exit code 1 · 3 lines\n") {
		t.Fatalf("legacy Bash failures without details should retain the concise summary, got %q", rendered)
	}
	if strings.Contains(rendered, "Error: exit code 1") {
		t.Fatalf("legacy Bash failure should not repeat its model-facing error line, got %q", rendered)
	}
}

func TestRenderToolCallsNestsBashFailureWithoutOutput(t *testing.T) {
	call := core.ToolCall{ID: "bash-1", Name: "Bash", Input: `{"command":"false"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {
				ToolName: "Bash",
				Content:  "Error: exit code 1",
				IsError:  true,
				Details:  toolresult.BashDetails{Error: "exit code 1"},
			},
		},
		Width: 100,
	}))

	if !strings.Contains(rendered, "Bash(false)\n  └ failed · exit code 1\n") {
		t.Fatalf("Bash failure without output should show only its reason in the summary, got %q", rendered)
	}
	if strings.Contains(rendered, "Error:") || strings.Contains(rendered, "no output") {
		t.Fatalf("Bash failure without output should not add a body or no-output suffix, got %q", rendered)
	}
}

func TestRenderToolCallsSummarizesBashTimeout(t *testing.T) {
	const reason = "command timed out after 2s — if it was waiting for interactive input, re-run with a non-interactive flag"
	call := core.ToolCall{ID: "bash-1", Name: "Bash", Input: `{"command":"sleep 10"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {
				ToolName: "Bash",
				Content:  "started\nError: " + reason,
				IsError:  true,
				Details:  toolresult.BashDetails{Error: reason, LineCount: 1},
			},
		},
		Width: 100,
	}))

	if !strings.Contains(rendered, "  ┊ started\n  └ failed · timed out after 2s · 1 line\n") {
		t.Fatalf("Bash timeout should use a concise reason and preserve output count, got %q", rendered)
	}
	if strings.Contains(rendered, "Error:") || strings.Contains(rendered, "non-interactive") {
		t.Fatalf("Bash timeout should not repeat its long model-facing diagnostic, got %q", rendered)
	}
}

func TestRenderToolCallsKeepsBashConnectorWhenOutputIsCollapsed(t *testing.T) {
	call := core.ToolCall{ID: "bash-1", Name: "Bash", Input: `{"command":"git status"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Bash", Content: "on main\nnothing to commit"},
		},
		Width: 100,
	}))
	if !strings.Contains(rendered, "Bash(git status)\n  └ 2 lines\n") {
		t.Fatalf("collapsed Bash output should retain its connector, got %q", rendered)
	}
}

func TestRenderToolCallsDoesNotAddBashBodyForEmptyOutput(t *testing.T) {
	call := core.ToolCall{ID: "bash-1", Name: "Bash", Input: `{"command":"true"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Bash", Expanded: true},
		},
		Width: 100,
	}))
	if strings.Contains(rendered, "└ no output\n  \n") {
		t.Fatalf("empty Bash output should not add a blank body row, got %q", rendered)
	}
}

func TestRenderToolCallsNestsEditResultUnderPath(t *testing.T) {
	call := core.ToolCall{
		ID:    "edit-1",
		Name:  "Edit",
		Input: `{"path":"internal/app/view.go","edits":[{"oldText":"old","newText":"new"}]}`,
	}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Edit", Content: "Edited internal/app/view.go", Details: toolresult.FileChangeDetails{
				Path: "internal/app/view.go", EditCount: 1, AddedLines: 1, RemovedLines: 1,
				UnifiedDiff: "@@ -1 +1 @@\n-old\n+new",
			}},
		},
		Width: 100,
	}))

	if strings.Count(rendered, "Edit") != 1 {
		t.Fatalf("Edit should be named only in its call header, got %q", rendered)
	}
	if !strings.Contains(rendered, "  ┊    1 - old") || !strings.Contains(rendered, "  ┊    1 + new") {
		t.Fatalf("Edit diff rows should align with the nested result trailer, got %q", rendered)
	}
	if !strings.Contains(rendered, "1 - old") || !strings.Contains(rendered, "1 + new") {
		t.Fatalf("Edit result should retain its rendered diff, got %q", rendered)
	}
	if !strings.Contains(rendered, "  └ +1 -1\n") || strings.Index(rendered, "  └ +1 -1") < strings.Index(rendered, "1 + new") {
		t.Fatalf("Edit summary should follow the actual diff, got %q", rendered)
	}
	if strings.Contains(rendered, "Edited internal/app/view.go") {
		t.Fatalf("Edit result should not duplicate its call path, got %q", rendered)
	}
}

func TestRenderToolCallsNestsWriteResultUnderPath(t *testing.T) {
	call := core.ToolCall{ID: "write-1", Name: "Write", Input: `{"path":"internal/app/new.go","content":"package app"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Write", Content: "Wrote internal/app/new.go", Details: toolresult.FileChangeDetails{
				Path: "internal/app/new.go", IsNewFile: true, AddedLines: 1,
				UnifiedDiff: "@@ -0,0 +1 @@\n+package app",
			}},
		},
		Width: 100,
	}))

	if strings.Count(rendered, "Write") != 1 {
		t.Fatalf("Write should be named only in its call header, got %q", rendered)
	}
	if !strings.Contains(rendered, "  ┊    1 + package app") {
		t.Fatalf("Write diff rows should align with the nested result trailer, got %q", rendered)
	}
	if !strings.Contains(rendered, "+ package app") {
		t.Fatalf("Write result should retain its rendered diff, got %q", rendered)
	}
	if !strings.Contains(rendered, "  └ +1 -0\n") || strings.Index(rendered, "  └ +1 -0") < strings.Index(rendered, "+ package app") {
		t.Fatalf("Write summary should follow its diff, got %q", rendered)
	}
}

func TestRenderToolCallsNestsFileChangeResultWithoutDetails(t *testing.T) {
	call := core.ToolCall{ID: "edit-1", Name: "Edit", Input: `{"path":"internal/app/view.go","edits":[]}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Edit", Content: "Edited internal/app/view.go (1 replacement(s), +1 -1)"},
		},
		Width: 100,
	}))

	if strings.Count(rendered, "Edit") != 1 {
		t.Fatalf("Edit should be named only in its call header, got %q", rendered)
	}
	if !strings.Contains(rendered, "Edit(internal/app/view.go)\n  └ 1 replacement(s), +1 -1\n") {
		t.Fatalf("Edit fallback result should retain its summary, got %q", rendered)
	}
}

func TestRenderToolCallsExtractsFallbackAfterParenthesizedPath(t *testing.T) {
	call := core.ToolCall{ID: "edit-1", Name: "Edit", Input: `{"path":"dir/foo(bar).go","edits":[]}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Edit", Content: "Edited dir/foo(bar).go (1 replacement(s), +1 -1)"},
		},
		Width: 100,
	}))

	if !strings.Contains(rendered, "  └ 1 replacement(s), +1 -1\n") {
		t.Fatalf("Edit fallback should use the trailing summary, got %q", rendered)
	}
	if strings.Contains(rendered, "  └ bar\n") {
		t.Fatalf("Edit fallback should not mistake path parentheses for its summary, got %q", rendered)
	}
	if got := extractTrailingParenContent("Edited dir/foo.go (1 replacement(s), +1 -1); current", "completed"); got != "1 replacement(s), +1 -1" {
		t.Fatalf("extractTrailingParenContent() = %q, want the recognized summary", got)
	}
	if got := extractTrailingParenContent("Wrote dir/foo(bar).go", "completed"); got != "completed" {
		t.Fatalf("extractTrailingParenContent() = %q, want fallback for parenthesized path", got)
	}
}

func TestRenderToolCallsNestsFileChangeFailureUnderPath(t *testing.T) {
	call := core.ToolCall{ID: "edit-1", Name: "Edit", Input: `{"path":"internal/app/view.go","edits":[{"oldText":"old","newText":"new"}]}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Edit", Content: "Error: oldText was not found\nmatching lines:\n     1\tactual", IsError: true},
		},
		Width: 100,
	}))

	if strings.Count(rendered, "Edit") != 1 {
		t.Fatalf("Edit should be named only in its call header, got %q", rendered)
	}
	if !strings.Contains(rendered, "  ┊ - old") || !strings.Contains(rendered, "  └ failed\n") || !strings.Contains(rendered, "  ┊ oldText was not found") {
		t.Fatalf("Edit failure should show requested input, terminal failure, and diagnostic, got %q", rendered)
	}
}

func TestRenderToolCallsCollapsesReadResultByDefault(t *testing.T) {
	call := core.ToolCall{ID: "read-1", Name: "Read", Input: `{"file_path":"internal/app/conv/message.go","offset":510,"limit":2}`}
	content := "   510\ttype ToolResultData struct {\n   511\t\tToolName string"
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Read", Content: content},
		},
		Width: 100,
	}))

	if !strings.Contains(rendered, "Read(internal/app/conv/message.go)") || strings.Contains(rendered, "· lines") {
		t.Fatalf("Read call should show only its path, got %q", rendered)
	}
	if strings.Contains(rendered, "type ToolResultData") {
		t.Fatalf("collapsed Read should not render file content, got %q", rendered)
	}
	if !strings.Contains(rendered, "  └ 2 lines\n") {
		t.Fatalf("collapsed Read should show a compact line summary, got %q", rendered)
	}
}

func TestRenderToolCallsExpandsReadResultAlignedWithSummary(t *testing.T) {
	call := core.ToolCall{ID: "read-1", Name: "Read", Input: `{"file_path":"internal/app/conv/message.go","offset":510,"limit":2}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{
			call.ID: {ToolName: "Read", Content: "   510\ttype ToolResultData struct {\n   511\t\tToolName string", Expanded: true},
		},
		Width: 100,
	}))

	if !strings.Contains(rendered, "  ┊    510    type ToolResultData struct {\n  ┊    511        ToolName string\n  └ 2 lines\n") {
		t.Fatalf("expanded Read rows should align with the nested result summary, got %q", rendered)
	}
}

func TestFormatReadResultSummaryCountsNumberedRows(t *testing.T) {
	content := "     1\tpackage app\n     2\t\n(output truncated at line 2; continue with offset=3)"
	if got := formatReadResultSummary(content); got != "2 lines" {
		t.Fatalf("formatReadResultSummary() = %q, want %q", got, "2 lines")
	}
}

func TestFormatReadResultSummaryDescribesAdvisoryOutput(t *testing.T) {
	tests := map[string]string{
		"Binary file detected: fixture.bin":                                "binary file",
		"image file: fixture.png (1.2 KB). Read cannot display images yet": "image file",
	}
	for content, want := range tests {
		if got := formatReadResultSummary(content); got != want {
			t.Fatalf("formatReadResultSummary(%q) = %q, want %q", content, got, want)
		}
	}
}

func TestFormatReadResultSummaryUsesSingularLine(t *testing.T) {
	if got := formatReadResultSummary("     1\tpackage app"); got != "1 line" {
		t.Fatalf("formatReadResultSummary() = %q, want %q", got, "1 line")
	}
}

func TestRenderFileChangeInputPreviewUsesFailedLegacyEdit(t *testing.T) {
	input := `{"edits":[{"oldText":"first old","newText":"first new"},{"oldText":"second old","newText":"second new"}]}`
	preview := stripANSI(renderFileChangeInputPreview(input, "Error: edits[1]: oldText was not found", 80))
	if !strings.Contains(preview, "- second old") || !strings.Contains(preview, "+ second new") {
		t.Fatalf("preview should show the failed edit, got %q", preview)
	}
	if strings.Contains(preview, "first old") || strings.Contains(preview, "first new") {
		t.Fatalf("preview should not show an unrelated edit, got %q", preview)
	}
}

func TestRenderFileChangeInputPreviewWrapsLongContent(t *testing.T) {
	preview := stripANSI(renderFileChangeInputPreview(`{"content":"`+strings.Repeat("x", 100)+`"}`, "", 40))
	for line := range strings.SplitSeq(strings.TrimSuffix(preview, "\n"), "\n") {
		if width := lipgloss.Width(line); width > 40 {
			t.Fatalf("preview line width = %d, want at most 40: %q", width, line)
		}
	}
}

func TestRenderFileChangeInputPreviewHonorsNarrowWidth(t *testing.T) {
	preview := stripANSI(renderFileChangeInputPreview(`{"content":"long"}`, "", 6))
	if preview != "" {
		t.Fatalf("narrow preview = %q, want no overflowing preview", preview)
	}
}

func TestRenderToolCallsNestsReadFailure(t *testing.T) {
	call := core.ToolCall{ID: "read-1", Name: "Read", Input: `{"file_path":"missing"}`}
	rendered := stripANSI(RenderToolCalls(ToolCallsParams{
		ToolCalls: []core.ToolCall{call},
		ResultMap: map[string]ToolResultData{call.ID: {ToolName: "Read", Content: "Error: file not found: missing", IsError: true}},
		Width:     100,
	}))
	if !strings.Contains(rendered, "  ┊ file not found: missing\n  └ failed\n") {
		t.Fatalf("Read failure should end in nested failed state, got %q", rendered)
	}
}

func TestRenderDecision(t *testing.T) {
	// No decision → no annotation line at all.
	if got := renderDecision(nil); got != "" {
		t.Errorf("nil decision should render nothing, got %q", got)
	}

	approved := stripANSI(renderDecision(&core.ReviewDecision{Approved: true, Reason: "read-only ls, no side effects"}))
	for _, want := range []string{"↳", "auto-approved", "read-only ls, no side effects"} {
		if !strings.Contains(approved, want) {
			t.Errorf("approved decision missing %q: %q", want, approved)
		}
	}

	escalated := stripANSI(renderDecision(&core.ReviewDecision{Approved: false, Reason: "writes outside the project"}))
	if !strings.Contains(escalated, "escalated") {
		t.Errorf("escalated decision should say so: %q", escalated)
	}

	// A blank reason drops the separator rather than leaving a dangling "·".
	bare := stripANSI(renderDecision(&core.ReviewDecision{Approved: true}))
	if strings.Contains(bare, "·") {
		t.Errorf("blank reason should not render a separator: %q", bare)
	}
}

// A docked modal parks the turn on the user's answer, so an Agent row must
// hold its glyph instead of blinking ●/○ off the frame counter — a blink reads
// as a subagent working while the user decides whether to let it start.
func TestRenderToolCallsFreezesAgentRowUnderDockedModal(t *testing.T) {
	call := core.ToolCall{ID: "tc-1", Name: tool.ToolAgent, Input: `{"agent":"reviewer","prompt":"review it"}`}
	params := ToolCallsParams{
		ToolCalls:    []core.ToolCall{call},
		ResultMap:    map[string]ToolResultData{},
		PendingCalls: []core.ToolCall{call},
		CurrentIdx:   0,
		SpinnerView:  "⋯",
		Width:        100,
		Interactive:  true,
	}

	// agentIcon flips every agentBlinkTicks frames; both phases must render
	// the same static bullet once the modal is up.
	for _, blink := range []int{0, agentBlinkTicks} {
		params.Blink = blink
		live := stripANSI(RenderToolCalls(params))
		if !strings.Contains(live, agentIcon(blink)) {
			t.Fatalf("blink %d: RenderToolCalls() = %q, want the blinking icon while no modal is up", blink, live)
		}

		params.DockedModalActive = true
		frozen := stripANSI(RenderToolCalls(params))
		params.DockedModalActive = false

		if !strings.Contains(frozen, "●") {
			t.Fatalf("blink %d: RenderToolCalls() = %q, want a static bullet under the modal", blink, frozen)
		}
		if strings.Contains(frozen, "○") {
			t.Fatalf("blink %d: RenderToolCalls() = %q, agent row still blinks under the modal", blink, frozen)
		}
		if strings.Contains(frozen, "ctrl+o") {
			t.Fatalf("blink %d: RenderToolCalls() = %q, ctrl+o hint is dead while the modal owns the keyboard", blink, frozen)
		}
	}
}
