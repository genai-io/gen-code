package app

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/todo"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
)

// The composer prints the "❭ " prompt once, on the first row, while inputCursor
// offsets the cursor by that prompt's width on every row. hangComposerRows is
// what keeps the two in step: without the gutter, wrapped and newline-split rows
// start at column 0 and the cursor floats two columns right of the text.
func TestComposerCursorAlignsWithEveryRow(t *testing.T) {
	const width = 80

	tests := []struct {
		name  string
		value string
	}{
		{"hard newline", "first line\nsecond"},
		{"soft wrap", strings.TrimSpace(strings.Repeat("ab/cd-ef ", 12))},
		{"cjk soft wrap", strings.Repeat("看看当前分支对 readme 的更改，", 6)},
		{"single row", "short"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &model{
				env:       env{Width: width, Height: 24, Ready: true},
				conv:      conv.NewModel(width),
				userInput: input.New("", width, nil, input.SelectorDeps{}),
			}
			m.userInput.Textarea.SetWidth(width - 4 - 2)
			m.userInput.Textarea.SetValue(tt.value)
			m.userInput.Textarea.CursorEnd()

			lines := strings.Split(m.renderInputView(), "\n")
			cursor := m.inputCursor(0)
			if cursor == nil {
				t.Fatal("composer reported no cursor")
			}
			if cursor.Position.Y != len(lines)-1 {
				t.Fatalf("cursor on row %d, but the value ends on row %d", cursor.Position.Y, len(lines)-1)
			}

			// The cursor sits at the end of the value, so it must land exactly
			// where the drawn row's text stops.
			row := strings.TrimRight(ansi.Strip(lines[cursor.Position.Y]), " ")
			if got := lipgloss.Width(row); got != cursor.Position.X {
				t.Fatalf("cursor at column %d, but row %q ends at column %d", cursor.Position.X, row, got)
			}

			// Every row has to fit the terminal, gutter included, or the
			// terminal rewraps the composer under the cursor.
			for i, line := range lines {
				if w := lipgloss.Width(line); w > width {
					t.Fatalf("row %d is %d columns wide, exceeds terminal width %d", i, w, width)
				}
			}
		})
	}
}

// The terminal the docked-modal tests render into.
const (
	modalTestWidth  = 80
	modalTestHeight = 24
)

// dockedModalModel builds a model whose approval modal is asking about the
// first of two parallel Bash calls the assistant has just explained, with that
// explanation still live (not yet committed to scrollback). The batch is what
// makes the fixture realistic: both calls get a PreTool event, so the "current"
// call is the *last* one tracked, not the one the modal is asking about.
func dockedModalModel(t *testing.T, rationale string) *model {
	t.Helper()

	return dockedModalModelWithCalls(t, rationale, []core.ToolCall{
		{ID: "bash-1", Name: "Bash", Input: `{"command":"df -h","description":"check disk"}`},
		{ID: "bash-2", Name: "Bash", Input: `{"command":"date","description":"check clock"}`},
	})
}

func dockedModalModelWithCalls(t *testing.T, rationale string, batch []core.ToolCall) *model {
	t.Helper()

	const width, height = modalTestWidth, modalTestHeight
	m := &model{
		env:       env{Width: width, Height: height, Ready: true},
		conv:      conv.NewModel(width),
		userInput: input.New("", width, nil, input.SelectorDeps{}),
		services:  services{Tracker: todo.NewStore(), Subagent: subagent.NewRegistry()},
	}
	// The stream is still open across a permission gate — it clears only on a
	// text-only final chunk — so the live tail believes it is mid-turn.
	m.conv.Stream.Active = true
	m.conv.Messages = append(m.conv.Messages, core.ChatMessage{
		Role:      core.RoleAssistant,
		Content:   rationale,
		ToolCalls: batch,
	})
	// PreTool fires before the permission request, so both calls are already
	// tracked as in flight by the time the modal goes up.
	m.conv.Tool.Track(batch)
	for _, call := range batch {
		m.conv.Tool.MarkCurrent(call.ID)
		m.conv.Tool.MarkStarted(call.ID)
	}
	// What HandlePermGate records when the request lands: the modal is asking
	// about the first call, not the batch.
	m.conv.Tool.MarkAwaitingApproval(batch[0].ID)

	m.userInput.Approval.Show(&perm.PermissionRequest{
		ID:          "req-1",
		ToolName:    "Bash",
		Description: "check disk",
		BashMeta:    &perm.BashMetadata{Command: "df -h", Description: "check disk"},
	}, width, height)

	return m
}

// The text streamed right before a tool call is the rationale for the
// permission being requested, so the approval modal has to render on top of it
// rather than in place of it — otherwise the reasoning disappears at exactly
// the moment the user has to act on it (issue #436).
func TestDockedModalKeepsPrecedingAssistantText(t *testing.T) {
	const rationale = "RATIONALE_SENTINEL: this is not a timeout, it is a kernel-level failure"

	m := dockedModalModel(t, rationale)
	frame, _ := m.viewString()

	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "RATIONALE_SENTINEL") {
		t.Fatalf("modal frame dropped the assistant text preceding the tool call:\n%s", frame)
	}
	if !strings.Contains(plain, "df -h") {
		t.Fatalf("modal frame is missing the approval prompt itself:\n%s", frame)
	}
}

// The chat tail above a docked modal is capped: rendering it in full would push
// the modal's answer options off the bottom of the screen, which is worse than
// showing no text at all.
func TestDockedModalStaysWithinTerminalHeight(t *testing.T) {
	rationale := strings.TrimSpace(strings.Repeat("wall of reasoning text that wraps and wraps ", 60))
	m := dockedModalModel(t, rationale)
	frame, _ := m.viewString()

	if rows := strings.Count(frame, "\n") + 1; rows > modalTestHeight {
		t.Fatalf("modal frame is %d rows tall, exceeds terminal height %d", rows, modalTestHeight)
	}
	// The options are the last thing in the modal: if they survived the cap,
	// nothing below the chat tail was pushed off screen.
	if !strings.Contains(ansi.Strip(frame), "Do you want to proceed?") {
		t.Fatalf("modal frame lost its question to the chat tail:\n%s", frame)
	}
}

// PreTool stamps a call before the permission request, so the in-flight state
// is live while the user decides. The call the modal is asking about has not
// been allowed to start: its row must say so rather than spin and tick an
// elapsed timer over the user's deliberation (issue #440).
func TestDockedModalDoesNotShowTheGatedCallAsRunning(t *testing.T) {
	m := dockedModalModel(t, "about to check the disk")
	spinnerGlyph := ansi.Strip(m.conv.Spinner.View())

	frame, _ := m.viewString()

	var row string
	for line := range strings.SplitSeq(ansi.Strip(frame), "\n") {
		if strings.Contains(line, "Bash(df -h)") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("modal frame has no row for the gated call:\n%s", frame)
	}
	if strings.Contains(row, spinnerGlyph) {
		t.Fatalf("tool row %q spins while it waits on the approval modal", row)
	}
	if !strings.Contains(row, "waiting for approval") {
		t.Fatalf("tool row %q does not say what it is waiting for", row)
	}
}

// The awaiting state is keyed by tool call, so the request has to carry the ID
// of the call it gates — the whole point of routing it through the modal.
func TestHandlePermGateMarksTheCallItAsksAbout(t *testing.T) {
	m := dockedModalModel(t, "about to check the disk")
	m.services.Agent = &agent.Session{}
	m.services.Session = &session.Setup{}
	m.conv.Tool.ClearAwaitingApproval()

	m.HandlePermGate(&conv.PermGateRequest{
		RequestID:   "req-1",
		ToolCallID:  "bash-1",
		ToolName:    "Bash",
		Description: "check disk",
		Input:       map[string]any{"command": "df -h"},
	})

	if got := m.conv.Tool.AwaitingApprovalID; got != "bash-1" {
		t.Fatalf("awaiting call = %q, want the call the request gates", got)
	}
}

// A request with no call ID names nothing, so the per-call state has to stay
// empty rather than hold a stale ID — that is what puts the rows back on the
// docked-modal freeze instead of leaving the gated call spinning over the
// user's deliberation. Unreachable on the main agent path today, but it is the
// seam that would degrade first.
func TestHandlePermGateWithoutCallIDFallsBackToTheModalFreeze(t *testing.T) {
	m := dockedModalModel(t, "about to check the disk")
	m.services.Agent = &agent.Session{}
	m.services.Session = &session.Setup{}
	spinnerGlyph := ansi.Strip(m.conv.Spinner.View())

	m.HandlePermGate(&conv.PermGateRequest{
		RequestID:   "req-1",
		ToolName:    "Bash",
		Description: "check disk",
		Input:       map[string]any{"command": "df -h"},
	})

	if got := m.conv.Tool.AwaitingApprovalID; got != "" {
		t.Fatalf("awaiting call = %q, want no call named when the request carries no ID", got)
	}

	frame, _ := m.viewString()
	for line := range strings.SplitSeq(ansi.Strip(frame), "\n") {
		if !strings.Contains(line, "Bash(") {
			continue
		}
		if strings.Contains(line, spinnerGlyph) {
			t.Fatalf("tool row %q spins while the modal owns the screen", line)
		}
	}
}

// The stream stays open across a permission gate, so the assistant message
// carrying the rationale still counts as the live tail. Its bullet must not
// spin either — the turn is parked on the modal, not producing text.
func TestDockedModalFreezesAssistantBullet(t *testing.T) {
	m := dockedModalModel(t, "RATIONALE_SENTINEL about to check the disk")
	spinnerGlyph := ansi.Strip(m.conv.Spinner.View())

	frame, _ := m.viewString()

	for line := range strings.SplitSeq(ansi.Strip(frame), "\n") {
		if strings.Contains(line, "RATIONALE_SENTINEL") && strings.Contains(line, spinnerGlyph) {
			t.Fatalf("assistant row %q spins while the turn waits on the modal", line)
		}
	}
}

// An Agent row is drawn by its own branch, which blinks its icon off the frame
// counter instead of using the shared spinner and offers "(ctrl+o to expand)".
// Both are wrong under a modal — the row is not working, and the modal owns
// ctrl+o. (The glyph freeze itself is pinned in conv; this covers the wiring.)
func TestDockedModalDropsDeadExpandHint(t *testing.T) {
	m := dockedModalModelWithCalls(t, "spawning a reviewer", []core.ToolCall{
		{ID: "agent-1", Name: tool.ToolAgent, Input: `{"agent":"reviewer","prompt":"review the diff"}`},
	})

	frame, _ := m.viewString()

	if strings.Contains(ansi.Strip(frame), "ctrl+o") {
		t.Fatalf("modal frame offers ctrl+o while the modal owns the keyboard:\n%s", frame)
	}
}

func TestTailLines(t *testing.T) {
	five := "L0\nL1\nL2\nL3\nL4"

	tests := []struct {
		name     string
		in       string
		maxLines int
		want     string
	}{
		{"no room drops content", five, 0, ""},
		{"negative room drops content", five, -3, ""},
		{"fewer lines than max returns input", "a\nb", 5, "a\nb"},
		{"exact fit returns input", five, 5, five},
		{"truncates to last N (latest)", five, 2, "L3\nL4"},
		{"single line cap keeps latest", five, 1, "L4"},
		{"empty string", "", 3, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tailLines(tt.in, tt.maxLines)
			if got != tt.want {
				t.Fatalf("tailLines(%q, %d) = %q, want %q", tt.in, tt.maxLines, got, tt.want)
			}
			// The result must never exceed maxLines rows when capping applies.
			if tt.maxLines > 0 {
				if n := strings.Count(got, "\n") + 1; n > tt.maxLines && got != tt.in {
					t.Fatalf("tailLines(%q, %d) returned %d lines, exceeds cap", tt.in, tt.maxLines, n)
				}
			}
		})
	}
}
