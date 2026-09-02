package app

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/todo"
	"github.com/genai-io/san/internal/tool/perm"
)

func desktopTestModel() *model {
	m := &model{
		env:       env{Width: 100, Height: 24, Ready: true},
		conv:      conv.NewModel(100),
		desktop:   newDesktopSurface(),
		userInput: input.New("", 100, nil, input.SelectorDeps{}),
		services: services{
			Subagent: subagent.NewRegistry(),
			Tracker:  todo.NewStore(),
			Hook:     hook.DefaultEngine(), // the footer's status line reads it
		},
	}
	return m
}

// The desktop covers the terminal's native scrollback, so a message whose
// completed blocks were already flushed there has to be drawn whole — honouring
// the inline commit offsets would leave a hole in the middle of the reply the
// user is watching. The inline path keeps skipping the flushed prefix.
func TestDesktopRendersStreamingMessageWhole(t *testing.T) {
	m := desktopTestModel()
	m.conv.Stream.Active = true
	m.conv.Messages = []core.ChatMessage{{
		Role:    core.RoleAssistant,
		Content: "FLUSHED_PREFIX\n\nLIVE_TAIL",
		// The prefix is already in native scrollback (see FlushStreamingBlocks).
		ContentCommittedLen: len("FLUSHED_PREFIX\n\n"),
		BulletEmitted:       true,
	}}

	transcript := m.renderTranscriptAt(m.env.Width)
	if !strings.Contains(transcript, "FLUSHED_PREFIX") {
		t.Error("desktop transcript dropped the already-flushed prefix")
	}
	if !strings.Contains(transcript, "LIVE_TAIL") {
		t.Error("desktop transcript dropped the live tail")
	}

	// The inline live tail still skips the prefix — the desktop's copy must not
	// have rewritten the model's own commit bookkeeping.
	if got := m.conv.Messages[0].ContentCommittedLen; got != len("FLUSHED_PREFIX\n\n") {
		t.Errorf("inline commit offset = %d, want %d", got, len("FLUSHED_PREFIX\n\n"))
	}
	inline := conv.RenderActiveContent(m.messageRenderParams())
	if strings.Contains(inline, "FLUSHED_PREFIX") {
		t.Error("inline live tail redrew the prefix that is already in scrollback")
	}
}

// While the desktop owns the screen the commit pipeline runs untouched — only
// the terminal write waits, so nothing diverges between the two surfaces and
// nothing is lost. Leaving replays the queue.
func TestDesktopHoldsScrollbackWritesAndReplaysOnExit(t *testing.T) {
	m := desktopTestModel()
	m.enterDesktop()
	if !m.scrollbackSuspended() {
		t.Fatal("entering the desktop did not suspend scrollback writes")
	}

	m.conv.Messages = []core.ChatMessage{{Role: core.RoleAssistant, Content: "committed while full-screen"}}
	if cmds := m.CommitMessages(); len(cmds) != 1 {
		t.Fatalf("commit commands = %d, want 1 — the pipeline must keep running", len(cmds))
	}
	if m.conv.CommittedCount != 1 {
		t.Fatalf("CommittedCount = %d, want 1 — commit bookkeeping must not diverge", m.conv.CommittedCount)
	}
	if len(m.flush.pendingPrints) != 1 {
		t.Fatalf("queued prints = %d, want 1 — the write should be held, not dropped", len(m.flush.pendingPrints))
	}

	// The ready message is a no-op while the alt-screen is up: nothing is
	// prepared for insertAbove and the chunk stays queued.
	if _, cmd := m.Update(scrollbackPrintReadyMsg{id: m.flush.pendingPrints[0].id}); cmd != nil {
		t.Fatal("a scrollback print was dispatched while the desktop owned the screen")
	}
	if m.flush.pendingPrints[0].current != "" {
		t.Fatal("the held chunk was prepared for insertAbove anyway")
	}

	m.exitDesktop()
	if m.scrollbackSuspended() {
		t.Fatal("leaving the desktop did not resume scrollback writes")
	}
	if cmd := m.resumeScrollbackPrints(); cmd == nil {
		t.Fatal("the held queue head was not re-posted on exit")
	}
}

// An overlay is an inline-surface panel: while one is up the app falls back to
// the inline view so the panel that owns the keyboard is the one on screen.
func TestOverlayTakesTheFrameBackFromTheDesktop(t *testing.T) {
	m := desktopTestModel()
	m.enterDesktop()
	if !m.desktopActive() {
		t.Fatal("desktop should own the frame with no overlay up")
	}

	m.userInput.Approval.Show(&perm.PermissionRequest{ToolName: "Bash"}, m.env.Width, m.env.Height)

	if _, ok := m.activeOverlay(); !ok {
		t.Fatal("test setup: expected an active overlay")
	}
	if m.desktopActive() {
		t.Error("desktop kept the frame while a modal owned the keyboard")
	}
	// The surface is still selected — it resumes once the modal closes.
	if m.env.Surface != SurfaceDesktop {
		t.Error("the overlay deselected the desktop surface instead of borrowing the frame")
	}
	if !m.scrollbackSuspended() {
		t.Error("scrollback writes resumed mid-session while the desktop was still selected")
	}
}
