// Scrollback rendering: convert pending conversation messages into ANSI
// terminal output and emit them via tea.Println. The bubbletea alt-screen
// only paints the bottom input area; rendered messages live in the
// terminal's native scrollback above.
package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/core"
)

type scrollbackPrintReadyMsg struct{ id uint64 }

type scrollbackPrintDoneMsg struct{ id uint64 }

type pendingScrollbackPrint struct {
	id        uint64
	remaining string
	current   string
}

func printScrollback(id uint64) tea.Cmd {
	return func() tea.Msg {
		return scrollbackPrintReadyMsg{id: id}
	}
}

func (m *model) CommitMessages() []tea.Cmd {
	return m.renderAndCommit(true)
}

// Streaming-flush pipeline. As an assistant message streams in, each of its
// completed markdown blocks migrates from the live tail (redrawn every frame in
// the alt-screen) into the terminal's native scrollback, one block at a time:
//
//	FlushStreamingBlocks  freezes the newly-completed, not-yet-committed slice of
//	                      the live message into a flushSnapshot.
//	renderSnapshotCmd     renders that snapshot with glamour on a background Cmd,
//	                      so the parse + syntax highlight can't stall repaint.
//	flushResultMsg        carries the rendered ANSI back to the UI goroutine.
//	handleFlushResult     Println's it into scrollback and advances the message's
//	                      commit offsets, so the live tail stops redrawing the
//	                      prefix now committed and flushes the next block.
//
// One render is in flight at a time (flush.rendering) so the Printlns stay
// ordered; the still-streaming trailing block stays live until it too completes.

// flushSnapshot is the immutable snapshot the background render goroutine works
// from, so it never touches live model state.
type flushSnapshot struct {
	msgID            string
	index            int
	thinkingSlice    string
	contentSlice     string
	thinkingEnd      int // commit offsets, advanced once this render lands
	contentEnd       int
	showThinkingIcon bool
	showBullet       bool
	width            int
	md               *conv.MDRenderer
}

// flushState is the streaming-block flush subsystem: it renders each completed
// conversation block (thinking, content) off the UI goroutine and commits the
// result to scrollback. See FlushStreamingBlocks and model_scrollback.go.
type flushState struct {
	rendering        bool                     // one render in flight at a time, so Printlns stay ordered
	renderer         *conv.MDRenderer         // background renderer, off the live-view MDRenderer's mutex
	width            int                      // width the renderer was built for; rebuild when it changes
	nextPrintID      uint64                   // monotonic identity for queued scrollback prints
	pendingPrints    []pendingScrollbackPrint // FIFO queue; only the head may be in flight
	minimizeForPrint bool                     // temporarily shrink a full-height frame before insertAbove
	frameForPrint    *tea.View                // freeze insertAbove geometry until the print completes
}

// flushResultMsg is the result of rendering a flushSnapshot off-thread, carrying
// the rendered blocks back for handleFlushResult to commit to scrollback.
type flushResultMsg struct {
	msgID                string
	index                int
	thinkingCommittedLen int
	contentCommittedLen  int
	thinkingEmitted      bool
	bulletEmitted        bool
	printed              string // "" when the blocks rendered empty (blank-only)
}

// FlushStreamingBlocks starts one turn of the streaming-flush pipeline above:
// it freezes the last message's newly-completed blocks into a flushSnapshot and
// kicks off their background render. Returns nil when a render is already in
// flight or nothing new is ready to flush.
func (m *model) FlushStreamingBlocks() []tea.Cmd {
	if m.flush.rendering {
		return nil // a block is already rendering off-thread; wait for it to land
	}
	idx := len(m.conv.Messages) - 1
	if idx < 0 {
		return nil
	}
	msg := &m.conv.Messages[idx]
	if msg.Role != core.RoleAssistant {
		return nil
	}

	// Once content starts, flush thinking's trailing paragraph too (it has no
	// terminating blank line, but reasoning is done).
	thinkingEnd := conv.CompletedBlockBoundary(msg.Thinking)
	if len(msg.Content) > 0 {
		thinkingEnd = len(msg.Thinking)
	}
	contentEnd := conv.CompletedBlockBoundary(msg.Content)

	var thinkingSlice, contentSlice string
	if thinkingEnd > msg.ThinkingCommittedLen {
		thinkingSlice = msg.Thinking[msg.ThinkingCommittedLen:thinkingEnd]
	}
	if contentEnd > msg.ContentCommittedLen {
		contentSlice = msg.Content[msg.ContentCommittedLen:contentEnd]
	}
	if strings.TrimSpace(thinkingSlice) == "" && strings.TrimSpace(contentSlice) == "" {
		return nil // no completed block yet (or blank-only — nothing to render)
	}

	m.flush.rendering = true
	return []tea.Cmd{renderSnapshotCmd(flushSnapshot{
		msgID:            msg.ID,
		index:            idx,
		thinkingSlice:    thinkingSlice,
		contentSlice:     contentSlice,
		thinkingEnd:      thinkingEnd,
		contentEnd:       contentEnd,
		showThinkingIcon: !msg.ThinkingEmitted,
		showBullet:       !msg.BulletEmitted,
		width:            m.env.Width,
		md:               m.flush.mdRenderer(m.env.Width),
	})}
}

// renderSnapshotCmd renders the snapshot's completed blocks (glamour, off the UI
// goroutine) and returns them as a flushResultMsg.
func renderSnapshotCmd(snap flushSnapshot) tea.Cmd {
	return func() tea.Msg {
		// != "" just skips a slice absent this flush; the render helpers
		// blank-check their input and we gate on a non-empty result.
		var blocks []string
		thinkingEmitted := false
		if snap.thinkingSlice != "" {
			if b := conv.RenderCommittedThinkingBlock(snap.thinkingSlice, snap.showThinkingIcon, snap.width, snap.md); b != "" {
				blocks = append(blocks, b)
				thinkingEmitted = true
			}
		}
		bulletEmitted := false
		if snap.contentSlice != "" {
			if b := conv.RenderCommittedContentBlock(snap.contentSlice, snap.showBullet, snap.md); b != "" {
				blocks = append(blocks, b)
				bulletEmitted = true
			}
		}
		printed := ""
		if len(blocks) > 0 {
			// Match RenderMessageAt's leading newline + blank-line separation.
			printed = "\n" + strings.Join(blocks, "\n\n")
		}
		return flushResultMsg{
			msgID:                snap.msgID,
			index:                snap.index,
			thinkingCommittedLen: snap.thinkingEnd,
			contentCommittedLen:  snap.contentEnd,
			thinkingEmitted:      thinkingEmitted,
			bulletEmitted:        bulletEmitted,
			printed:              printed,
		}
	}
}

// handleFlushResult lands a background render: advance the row's offsets,
// print to scrollback, then flush the next completed block.
func (m *model) handleFlushResult(msg flushResultMsg) tea.Cmd {
	m.flush.rendering = false

	// Drop the render if its row was committed whole (turn-end/cancel) or
	// replaced by a retry (new ID) meanwhile — its content is already handled, so
	// printing it now would duplicate or reorder scrollback.
	if msg.index >= len(m.conv.Messages) ||
		msg.index < m.conv.CommittedCount ||
		m.conv.Messages[msg.index].ID != msg.msgID ||
		m.conv.Messages[msg.index].Role != core.RoleAssistant {
		return nil
	}

	row := &m.conv.Messages[msg.index]
	row.ThinkingCommittedLen = msg.thinkingCommittedLen
	row.ContentCommittedLen = msg.contentCommittedLen
	if msg.thinkingEmitted {
		row.ThinkingEmitted = true
	}
	if msg.bulletEmitted {
		row.BulletEmitted = true
	}

	var cmds []tea.Cmd
	if msg.printed != "" {
		cmds = append(cmds, m.queueScrollbackPrint(msg.printed))
	}
	// Catch a block that completed while this one rendered — Stream.Active means
	// the row is still uncommitted, so it's safe.
	if m.conv.Stream.Active {
		cmds = append(cmds, m.FlushStreamingBlocks()...)
	}
	// Sequence, not Batch: this block's print must be queued before the next
	// render's result, or concurrent Batch could reorder scrollback blocks.
	return tea.Sequence(cmds...)
}

// mdRenderer returns the background goroutine's own markdown renderer, kept off
// m.conv.MDRenderer so a slow render can't block the live View on its mutex.
// Rebuilt on width change; needs no lock since flush.rendering means one render
// uses it at a time.
func (f *flushState) mdRenderer(width int) *conv.MDRenderer {
	if f.renderer == nil || f.width != width {
		f.renderer = conv.NewMDRenderer(width)
		f.width = width
	}
	return f.renderer
}

func (m *model) commitAllMessages() []tea.Cmd {
	return m.renderAndCommit(false)
}

func (m *model) renderAndCommit(checkReady bool) []tea.Cmd {
	var parts []string
	lastIdx := len(m.conv.Messages) - 1
	params := m.messageRenderParams()

	for i := m.conv.CommittedCount; i < len(m.conv.Messages); i++ {
		msg := m.conv.Messages[i]

		if checkReady {
			if i == lastIdx && msg.Role == core.RoleAssistant && m.conv.Stream.Active {
				break
			}
		}

		if rendered := conv.RenderSingleMessage(params, i); rendered != "" {
			parts = append(parts, rendered)
		}
		// Fully in scrollback now (any progressively-flushed prefix plus this
		// remainder). Clear the commit offsets so a later full rebuild (resize
		// reflow, compact reprint) renders the message whole, not just its tail.
		m.conv.Messages[i].ResetStreamCommit()
		m.conv.CommittedCount = i + 1
	}

	if len(parts) == 0 {
		return nil
	}
	if banner := m.takeWelcomeBanner(); banner != "" {
		parts = append([]string{banner}, parts...)
	}
	return []tea.Cmd{m.queueScrollbackPrint(strings.Join(parts, "\n"))}
}

// queueScrollbackPrint appends content to a single-flight FIFO. Only an empty
// queue starts a print; finishScrollbackPrint starts the next chunk after Bubble
// Tea has processed the current Println. Each chunk is no taller than the rows
// above the managed frame, so insertAbove cannot scroll live UI rows into native
// history.
func (m *model) queueScrollbackPrint(content string) tea.Cmd {
	return m.flush.queueScrollbackPrint(content)
}

func (f *flushState) queueScrollbackPrint(content string) tea.Cmd {
	if content == "" {
		return nil
	}
	f.nextPrintID++
	pending := pendingScrollbackPrint{
		id:        f.nextPrintID,
		remaining: content,
	}
	f.pendingPrints = append(f.pendingPrints, pending)
	if len(f.pendingPrints) > 1 {
		return nil
	}
	return printScrollback(pending.id)
}

// resumeScrollbackPrints restarts the queue after writes were held back (see
// scrollbackSuspended): the ready message for the head was dropped while the
// alt-screen was up, so it needs re-posting. Nothing pending, nothing to do.
func (m *model) resumeScrollbackPrints() tea.Cmd {
	if len(m.flush.pendingPrints) == 0 {
		return nil
	}
	return printScrollback(m.flush.pendingPrints[0].id)
}

// finishScrollbackPrint completes only the in-flight queue head. A stale or
// out-of-order done message is ignored; the next chunk or queued print cannot
// start until the current Println has been processed.
func (m *model) finishScrollbackPrint(id uint64) tea.Cmd {
	return m.flush.finishScrollbackPrint(id)
}

func (f *flushState) finishScrollbackPrint(id uint64) tea.Cmd {
	if len(f.pendingPrints) == 0 || f.pendingPrints[0].id != id {
		return nil
	}
	f.minimizeForPrint = false
	f.frameForPrint = nil
	f.pendingPrints[0].current = ""
	if f.pendingPrints[0].remaining != "" {
		return printScrollback(id)
	}
	f.pendingPrints = f.pendingPrints[1:]
	if len(f.pendingPrints) == 0 {
		return nil
	}
	return printScrollback(f.pendingPrints[0].id)
}

func (m *model) prepareScrollbackPrint(id uint64) (string, bool) {
	frame := m.View()
	frameHeight := 0
	if frame.Content != "" {
		frameHeight = strings.Count(frame.Content, "\n") + 1
	}
	content, ok := m.flush.prepareScrollbackPrint(
		id,
		m.env.Width,
		m.env.Height,
		frameHeight,
	)
	if !ok {
		return "", false
	}
	if m.flush.minimizeForPrint {
		frame = tea.NewView("")
	}
	m.flush.frameForPrint = &frame
	return content, true
}

func (f *flushState) prepareScrollbackPrint(id uint64, width, height, frameHeight int) (string, bool) {
	if len(f.pendingPrints) == 0 || f.pendingPrints[0].id != id || f.pendingPrints[0].current != "" {
		return "", false
	}
	lines := scrollbackPhysicalLines(f.pendingPrints[0].remaining, width)
	if len(lines) == 0 {
		return "", false
	}

	capacity := len(lines)
	f.minimizeForPrint = false
	if height > 0 {
		capacity = height - min(max(frameHeight, 0), height)
		if capacity < 1 {
			f.minimizeForPrint = true
			capacity = height
		}
	}
	capacity = min(capacity, len(lines))
	f.pendingPrints[0].current = renderScrollbackLines(lines[:capacity])
	f.pendingPrints[0].remaining = renderScrollbackLines(lines[capacity:])
	return f.pendingPrints[0].current, true
}

func (m *model) scrollbackFrameForPrint() (tea.View, bool) {
	if m.flush.frameForPrint == nil {
		return tea.View{}, false
	}
	return *m.flush.frameForPrint, true
}

func (m *model) useMinimalScrollbackFrame() {
	if m.flush.frameForPrint == nil {
		return
	}
	frame := tea.NewView("")
	m.flush.frameForPrint = &frame
	m.flush.minimizeForPrint = true
}

// scrollbackPhysicalLines decomposes content exactly as Bubble Tea's
// insertAbove accounts for it: ANSI escapes are zero-width, grapheme clusters
// retain their terminal width, soft wraps consume rows, and a trailing newline
// creates a final blank row.
func scrollbackPhysicalLines(content string, width int) []uv.Line {
	if content == "" {
		return nil
	}
	height := len(strings.Split(content, "\n"))
	bufferWidth := width
	if bufferWidth <= 0 {
		bufferWidth = 1
	}
	for line := range strings.SplitSeq(content, "\n") {
		lineWidth := ansi.StringWidth(line)
		if width > 0 && lineWidth > width {
			height += lineWidth / width
		}
		if width <= 0 {
			bufferWidth = max(bufferWidth, lineWidth)
		}
	}

	buffer := uv.NewScreenBuffer(bufferWidth, height)
	buffer.Method = ansi.GraphemeWidth
	styled := uv.NewStyledString(content)
	styled.Wrap = true
	styled.Draw(buffer, buffer.Bounds())
	return buffer.Lines
}

func renderScrollbackLines(lines []uv.Line) string {
	if len(lines) == 0 {
		return ""
	}
	if rendered := uv.Lines(lines).Render(); rendered != "" {
		return rendered
	}
	// insertAbove ignores an empty string. A reset sequence represents one
	// physical blank row without adding visible content.
	return ansi.ResetStyle
}

// takeWelcomeBanner freezes the startup splash into scrollback once, on the
// first commit, then clears the pending flag so the live view (liveWelcome)
// stops drawing it. Freezing it here rather than before the TUI starts lets the
// banner capture the model the user selected after launch instead of freezing
// "no model selected" into scrollback.
func (m *model) takeWelcomeBanner() string {
	if !m.welcomePending {
		return ""
	}
	m.welcomePending = false
	return m.welcomeBannerText()
}

// welcomeBannerText renders the startup splash for the current model and cwd.
// It backs both the live banner (liveWelcome) and the scrollback freeze
// (takeWelcomeBanner) so the two always read identically.
func (m model) welcomeBannerText() string {
	return welcomeBanner(welcomeInfo{
		Model: m.env.GetModelDisplayName(),
		CWD:   m.env.CWD,
	})
}
