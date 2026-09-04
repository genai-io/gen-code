// Scrollback rendering: convert pending conversation messages into ANSI
// terminal output and emit them via tea.Println. The bubbletea alt-screen
// only paints the bottom input area; rendered messages live in the
// terminal's native scrollback above.
//
// The frame rule everything here obeys: insertAbove prints above the managed
// frame, and the inline renderer only redraws the frame's current extent — so
// what the frame holds then stays on screen, and any row it stops holding is
// left behind. Both are permanent; scrollback cannot be rewritten.
package app

import (
	"github.com/genai-io/san/internal/core"

	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/conv"
)

type scrollbackPrintReadyMsg struct{ id uint64 }

type scrollbackPrintDoneMsg struct{ id uint64 }

type pendingScrollbackPrint struct {
	id        uint64
	remaining string
	current   string
	frameRows int  // chat rows this print's content occupied in the live tail
	started   bool // first chunk prepared; the rest is no longer the frame's job
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
	rendering     bool                     // one render in flight at a time, so Printlns stay ordered
	renderer      *conv.MDRenderer         // background renderer, off the live-view MDRenderer's mutex
	width         int                      // width the renderer was built for; rebuild when it changes
	nextPrintID   uint64                   // monotonic identity for queued scrollback prints
	pendingPrints []pendingScrollbackPrint // FIFO queue; only the head may be in flight
	frameForPrint *tea.View                // freeze insertAbove geometry until the print completes
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
	if msg.Role != core.ChatAssistant {
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
		m.conv.Messages[msg.index].Role != core.ChatAssistant {
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
		// No budget: a streamed block leaves the tail as its rendered self.
		cmds = append(cmds, m.queueScrollbackPrint(msg.printed, 0))
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
	// What the loop below is about to take out of the frame.
	preCommitRows := rowCount(conv.RenderActiveContent(params))

	for i := m.conv.CommittedCount; i < len(m.conv.Messages); i++ {
		msg := m.conv.Messages[i]

		if checkReady {
			if i == lastIdx && msg.Role == core.ChatAssistant && m.conv.Stream.Active {
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
	return []tea.Cmd{m.queueScrollbackPrint(strings.Join(parts, "\n"), preCommitRows)}
}

// queueScrollbackPrint appends content to a single-flight FIFO. Only an empty
// queue starts a print; finishScrollbackPrint starts the next chunk after Bubble
// Tea has processed the current Println. Each chunk is no taller than the rows
// above the managed frame, so insertAbove cannot scroll live UI rows into native
// history.
func (m *model) queueScrollbackPrint(content string, frameRows int) tea.Cmd {
	return m.flush.queueScrollbackPrint(content, frameRows)
}

// resumeDeferredScrollbackPrint restarts the queue once no panel owns the frame.
//
// Update calls it on the overlay-closed edge rather than on the three prompt
// replies: the queue is single-flight, so a head nobody restarts stalls every
// block behind it for the session, and renderAndCommit advances CommittedCount
// before the print runs — the live tail has already stopped drawing what the
// queue is still holding, which makes a stall a disappearance.
func (m *model) resumeDeferredScrollbackPrint() tea.Cmd {
	if len(m.flush.pendingPrints) == 0 || m.flush.pendingPrints[0].current != "" {
		return nil
	}
	return printScrollback(m.flush.pendingPrints[0].id)
}

func (f *flushState) queueScrollbackPrint(content string, frameRows int) tea.Cmd {
	if content == "" {
		return nil
	}
	f.nextPrintID++
	pending := pendingScrollbackPrint{
		id:        f.nextPrintID,
		remaining: content,
		frameRows: frameRows,
	}
	f.pendingPrints = append(f.pendingPrints, pending)
	if len(f.pendingPrints) > 1 {
		return nil
	}
	return printScrollback(pending.id)
}

// finishScrollbackPrint completes only the in-flight queue head. A stale or
// out-of-order done message is ignored; the next chunk or queued print cannot
// start until the current Println has been processed.
func (m *model) finishScrollbackPrint(id uint64) tea.Cmd {
	next := m.flush.finishScrollbackPrint(id)
	if len(m.flush.pendingPrints) == 0 {
		m.showDeferredApproval()
	}
	return next
}

func (f *flushState) finishScrollbackPrint(id uint64) tea.Cmd {
	if len(f.pendingPrints) == 0 || f.pendingPrints[0].id != id {
		return nil
	}
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
	// Physical rows, like the chunk this is subtracted from: a frame line wider
	// than the terminal occupies more rows than it has newlines, and counting
	// the two differently overstates the room above the frame.
	frameHeight := len(scrollbackPhysicalLines(frame.Content, m.env.Width))
	content, ok := m.flush.prepareScrollbackPrint(
		id,
		m.env.Width,
		m.env.Height,
		frameHeight,
	)
	if !ok {
		return "", false
	}
	if frameFillsScreen(frameHeight, m.env.Height) {
		frame = tea.NewView("")
	}
	m.flush.frameForPrint = &frame
	return content, true
}

// frameFillsScreen reports that the frame leaves no room above it. The print
// then has nowhere to insert, so it shrinks the frame to nothing for the
// duration — both halves of prepareScrollbackPrint read the answer here rather
// than one telling the other.
func frameFillsScreen(frameHeight, height int) bool {
	return height > 0 && frameHeight >= height
}

func (f *flushState) prepareScrollbackPrint(id uint64, width, height, frameHeight int) (string, bool) {
	if len(f.pendingPrints) == 0 || f.pendingPrints[0].id != id || f.pendingPrints[0].current != "" {
		return "", false
	}
	lines := scrollbackPhysicalLines(f.pendingPrints[0].remaining, width)
	if len(lines) == 0 {
		return "", false
	}
	f.pendingPrints[0].started = true

	capacity := len(lines)
	if height > 0 {
		capacity = height - min(max(frameHeight, 0), height)
		if frameFillsScreen(frameHeight, height) {
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
}

// trimPadding drops the padding a full-width buffer leaves on a row.
// Free at the width it was printed at, but scrollback is immutable: narrow the
// window and text-plus-padding rewraps into two rows, the second all spaces.
//
// A trailing space counts as padding whatever foreground or bold it inherited —
// none of that shows on a space. Only a background or an underline does, and
// those are content.
func trimPadding(line uv.Line) uv.Line {
	end := len(line)
	for end > 0 && isPadding(&line[end-1]) {
		end--
	}
	return line[:end]
}

func isPadding(c *uv.Cell) bool {
	if c.IsZero() || c.Equal(&uv.EmptyCell) {
		return true
	}
	return c.Content == " " && c.Link.IsZero() &&
		c.Style.Bg == nil && c.Style.UnderlineColor == nil && c.Style.Underline == uv.UnderlineNone
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
	for i, line := range lines {
		lines[i] = trimPadding(line)
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

// pendingScrollbackView is the handoff copy: the queued chunk stays drawn in the
// frame until its Println lands, so committing cannot shrink the frame mid-print.
//
// Restored from e02ed422, which 74ac1647 dropped as redundant beside the FIFO
// and the renderer's flush barrier. It is not: the barrier fixes *when*
// insertAbove sees the frame, this fixes *what* the frame is.
func (m model) pendingScrollbackView() string {
	if len(m.flush.pendingPrints) == 0 {
		return ""
	}
	// Until the first chunk goes out the whole payload stands in for the tail;
	// after it, only the chunk in flight. Holding the remainder would swell the
	// frame, and the next chunk's insertAbove welds that into its own output.
	head := m.flush.pendingPrints[0]
	handoff := head.current
	if !head.started {
		handoff = head.remaining
	}
	if handoff == "" {
		return ""
	}
	// The rendered block is shorter than the plain tail it replaces, and every
	// row of the difference is one the frame would vacate.
	return handoff + strings.Repeat("\n", max(head.frameRows-rowCount(handoff), 0))
}

// rowCount is how many terminal rows a rendered section occupies.
func rowCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
