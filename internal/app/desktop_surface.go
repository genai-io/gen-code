// Desktop surface wiring: the seam between the root model and the alt-screen
// reader in internal/app/desktop. The desktop shows the same transcript and the
// same input strip as the inline view — reusing the very same render functions —
// but full-screen and scrollable, so the whole history stays inside the managed
// frame instead of being handed to the terminal's native scrollback.
//
// Everything the surface needs lives here: the Surface enum, its model state,
// the toggle, the repaint heartbeat, the per-frame content snapshot, and key
// routing. The inline path is left alone; the only thing the desktop asks of it
// is that scrollback writes wait until the screen is handed back (see
// scrollbackSuspended).
package app

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/desktop"
	"github.com/genai-io/san/internal/core"
)

// Surface selects which presentation the root model renders.
type Surface int

const (
	// SurfaceInline is the default: the live tail renders inline and finished
	// output is committed to the terminal's native scrollback.
	SurfaceInline Surface = iota
	// SurfaceDesktop is the opt-in full-screen reader: the same content, but San
	// owns the whole screen and the full history scrolls within it.
	SurfaceDesktop
)

// desktopSurface is all the state the full-screen surface needs, in one struct
// so the root model carries a single field and every desktop concern stays in
// this file.
type desktopSurface struct {
	manager desktop.Manager

	// ticking guards the repaint heartbeat to a single chain, so repeated
	// toggles don't stack heartbeats.
	ticking bool

	// md is the surface's own markdown renderer, kept off m.conv.MDRenderer so a
	// full-transcript render never contends with the live view. Rebuilt on width
	// change.
	md      *conv.MDRenderer
	mdWidth int

	// history caches the rendered committed prefix, keyed by everything that can
	// change it. Without it every streamed chunk would re-run glamour over the
	// entire conversation, since the desktop — unlike the inline view — has to
	// draw the history itself.
	history    string
	historyKey string

	// inputRow is where renderFooter placed the composer within the footer strip,
	// carried over so the view can put the terminal cursor there.
	inputRow int
}

func newDesktopSurface() desktopSurface {
	return desktopSurface{manager: desktop.New()}
}

// desktopTickInterval paces the repaint heartbeat: the surface redraws on its
// own so spinners and elapsed timers keep moving between agent events.
const desktopTickInterval = 80 * time.Millisecond

type desktopTickMsg struct{}

// desktopFlushMsg is posted by exitDesktop and handled one loop iteration later,
// after the inline view has repainted and the renderer has left the alt-screen —
// so the held scrollback writes land in the normal buffer, not on the alt-screen
// that is about to be discarded.
type desktopFlushMsg struct{}

func desktopTick() tea.Cmd {
	return tea.Tick(desktopTickInterval, func(time.Time) tea.Msg { return desktopTickMsg{} })
}

// desktopActive reports whether the desktop is both selected and owns the frame.
// An overlay (slash-command picker, approval/question modal) is an inline-surface
// panel, so while one is up the app falls back to the inline view and the desktop
// resumes when it closes. View and the key router share this predicate, keeping
// the app's rule that the panel owning the keyboard is the one drawn on screen.
func (m *model) desktopActive() bool {
	if m.env.Surface != SurfaceDesktop {
		return false
	}
	_, hasOverlay := m.activeOverlay()
	return !hasOverlay
}

// scrollbackSuspended reports whether writes to the terminal's native scrollback
// must wait. Bubble Tea's insertAbove scrolls and inserts rows into whichever
// buffer is current, so a tea.Println issued while the alt-screen is up would
// land in the desktop's frame and be lost from the history below it.
//
// Only the terminal write waits — the commit pipeline itself runs untouched, so
// commit offsets, CommittedCount, and the rendered blocks are identical on both
// surfaces. The queue replays in order once the inline view is back, which is
// why this stays true for the whole desktop session, including the moments an
// overlay borrows the frame.
func (m *model) scrollbackSuspended() bool {
	return m.env.Surface == SurfaceDesktop
}

// enterDesktop switches to the desktop and starts the repaint heartbeat — one
// chain only; it lapses in Update once we go back inline. The first frame needs
// no priming here: View syncs the surface on the paint that follows this Update.
func (m *model) enterDesktop() tea.Cmd {
	m.env.Surface = SurfaceDesktop
	if m.desktop.ticking {
		return nil
	}
	m.desktop.ticking = true
	return desktopTick()
}

// exitDesktop returns to the inline surface and schedules the backlog flush.
func (m *model) exitDesktop() tea.Cmd {
	m.env.Surface = SurfaceInline
	return func() tea.Msg { return desktopFlushMsg{} }
}

// onDesktopTick keeps the heartbeat alive while the surface is selected. Each
// tick is just a repaint trigger — View does the work.
func (m *model) onDesktopTick() tea.Cmd {
	if m.env.Surface == SurfaceDesktop {
		return desktopTick()
	}
	m.desktop.ticking = false
	return nil
}

// onDesktopFlush releases everything scrollbackSuspended held back, now that the
// inline view has repainted: the print queue restarts where it left off, and any
// message that finished during the desktop session is committed behind it.
func (m *model) onDesktopFlush() tea.Cmd {
	return tea.Sequence(
		m.resumeScrollbackPrints(),
		tea.Batch(m.CommitMessages()...),
	)
}

// desktopView renders the full-screen frame.
func (m *model) desktopView() tea.View {
	m.syncDesktop()
	v := tea.NewView(m.desktop.manager.Render())
	// San owns the whole screen here; the native scrollback below is left intact
	// and reappears untouched on exit. No mouse tracking: capturing the mouse
	// would disable the terminal's own text selection and copy.
	v.AltScreen = true
	v.Cursor = m.inputCursor(m.desktop.manager.ContentHeight() + m.desktop.inputRow)
	return v
}

// syncDesktop reconciles the surface's size, transcript, and input strip with the
// live model. Cheap to call every frame: the transcript sits behind a signature
// inside the manager, so the heavy render runs only on real change.
func (m *model) syncDesktop() {
	separator := conv.SeparatorStyle.Render(strings.Repeat("─", m.env.Width))
	footer, inputRow := m.renderFooter(separator)
	m.desktop.inputRow = inputRow

	m.desktop.manager.Resize(m.env.Width, m.env.Height)
	m.desktop.manager.SetFooter(footer)
	m.desktop.manager.SetContent(desktop.Pane{
		ID:     "conversation",
		Sig:    m.desktopSig(),
		Render: func(w, _ int) string { return m.renderTranscriptAt(w) },
	})
}

// desktopSig is the manager's rebuild key: it has to move whenever the rendered
// transcript would differ. Width is tracked by the manager itself.
func (m *model) desktopSig() string {
	lastContent, lastThinking := 0, 0
	if n := len(m.conv.Messages); n > 0 {
		lastContent = len(m.conv.Messages[n-1].Content)
		lastThinking = len(m.conv.Messages[n-1].Thinking)
	}
	// The spinner frame is part of the key: between chunks — while a tool runs —
	// nothing else moves, and without it the live tail's spinner and elapsed
	// timers would freeze on screen.
	blink := 0
	if m.needsSpinner() {
		blink = m.conv.Spinner.Frame()
	}
	return fmt.Sprintf("%d|%d|%d|%d|%d|%d",
		len(m.conv.Messages), m.conv.CommittedCount,
		lastContent, lastThinking, m.expandedCommittedCount(), blink)
}

// renderTranscriptAt renders the whole conversation at the given width, reusing
// the inline renderers so both surfaces read identically. The split mirrors the
// inline view: the committed prefix is rendered statically (it is what inline
// hands to native scrollback), while the active tail goes through
// RenderActiveContent + renderChatSection — the same path that drives the live
// spinner and running-tool rows above the inline input.
func (m *model) renderTranscriptAt(width int) string {
	params := m.desktopParams(width)

	parts := make([]string, 0, 2)
	if history := m.desktopHistory(params, width); strings.TrimSpace(history) != "" {
		parts = append(parts, history)
	}
	live := m.renderChatSection(conv.RenderActiveContent(params), m.renderTrackerList())
	if strings.TrimSpace(live) != "" {
		parts = append(parts, live)
	}
	return strings.Join(parts, "\n")
}

// desktopParams builds the render context for the surface: the inline width and
// renderer are swapped for the surface's own, and the streaming message's commit
// offsets are cleared.
//
// Those offsets exist so the inline view skips the prefix already flushed to
// native scrollback (see FlushStreamingBlocks). The desktop covers that
// scrollback, so honouring them would open a hole in the middle of the message
// the user is watching. Only the uncommitted tail can carry offsets, so the copy
// is a couple of messages, and it is a copy precisely so the inline surface's own
// bookkeeping stays untouched.
func (m *model) desktopParams(width int) conv.RenderContext {
	if m.desktop.md == nil || m.desktop.mdWidth != width {
		m.desktop.md = conv.NewMDRenderer(width)
		m.desktop.mdWidth = width
		m.desktop.historyKey = "" // rebuild the cache through the new renderer
	}

	params := m.messageRenderParams()
	params.Width = width
	params.MDRenderer = m.desktop.md

	if n := len(params.Messages); n > params.CommittedCount {
		whole := make([]core.ChatMessage, n)
		copy(whole, params.Messages)
		for i := params.CommittedCount; i < n; i++ {
			whole[i].ResetStreamCommit()
		}
		params.Messages = whole
	}
	return params
}

// desktopHistory renders messages [0, CommittedCount) — the part the inline
// surface freezes into native scrollback — and caches it, since only a new
// commit, a resize, or an expand/collapse toggle can change it.
func (m *model) desktopHistory(params conv.RenderContext, width int) string {
	key := fmt.Sprintf("%d|%d|%d", width, params.CommittedCount, m.expandedCommittedCount())
	if m.desktop.historyKey == key {
		return m.desktop.history
	}
	m.desktop.historyKey = key
	m.desktop.history = conv.RenderMessageRange(params, 0, params.CommittedCount, false)
	return m.desktop.history
}

// expandedCommittedCount fingerprints the expand/collapse state of the committed
// range. Ctrl-O is the one thing that re-renders an already-committed message,
// and every toggle it offers — one row, or all of them — moves this count.
func (m *model) expandedCommittedCount() int {
	n := 0
	for i := 0; i < m.conv.CommittedCount && i < len(m.conv.Messages); i++ {
		if m.conv.Messages[i].ToolCallsExpanded {
			n++
		}
		if m.conv.Messages[i].Expanded {
			n++
		}
	}
	return n
}

// handleDesktopKey claims the keys the reader needs and nothing else: the exit
// toggle and scrolling. Everything else — typing, Enter, Ctrl-C, Ctrl-O, history
// — falls through to the normal chain, so the app behaves the same on both
// surfaces. It is routed after the suggestion/queue overlays, which already own
// pgup/pgdown/home/end while they are visible.
func (m *model) handleDesktopKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+g":
		return m.exitDesktop(), true
	case "pgup":
		m.desktop.manager.PageUp()
	case "pgdown":
		m.desktop.manager.PageDown()
	case "home":
		m.desktop.manager.GotoTop()
	case "end":
		m.desktop.manager.GotoBottom()
	default:
		return nil, false
	}
	return nil, true
}
