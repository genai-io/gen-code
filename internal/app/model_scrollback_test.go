package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/todo"
	"github.com/genai-io/san/internal/tool/perm"
	"github.com/genai-io/san/internal/tool/toolresult"
)

func flushTestModel(msg core.ChatMessage) *model {
	m := &model{env: env{Width: 80}, conv: conv.NewModel(80)}
	m.conv.Messages = []core.ChatMessage{msg}
	return m
}

// applyFlush runs the off-thread render Cmd that FlushStreamingBlocks kicked off
// and lands its result, mirroring the real render → handleFlushResult path
// so tests can assert the committed offsets the landing advances.
func applyFlush(t *testing.T, m *model, cmds []tea.Cmd) {
	t.Helper()
	if len(cmds) == 0 {
		t.Fatal("expected a flush render Cmd, got none")
	}
	msg := cmds[0]()
	br, ok := msg.(flushResultMsg)
	if !ok {
		t.Fatalf("flush Cmd returned %T, want flushResultMsg", msg)
	}
	m.handleFlushResult(br)
}

// trimLinePadding trims the trailing blank cells that ultraviolet's renderer
// now emits as spaces for full-width rows (charmbracelet/ultraviolet#128
// flushes trailing empty cells instead of trimming them). Padding is a
// cosmetic of the full-width buffer, so strip it per row before comparing
// physical-line content.
func trimLinePadding(s string) string {
	rows := []string{}
	for line := range strings.SplitSeq(s, "\n") {
		rows = append(rows, strings.TrimRight(line, " "))
	}
	return strings.Join(rows, "\n")
}

// The live welcome banner is visible from launch and tracks the model the user
// picks after launch — the regression behind #252, where the banner froze "no
// model selected" because it was committed to scrollback before any selection.
func TestLiveWelcomeTracksModelSelection(t *testing.T) {
	m := &model{env: env{Width: 80}, conv: conv.NewModel(80), welcomePending: true}

	// At launch, before a model is picked, the splash is already on screen.
	if got := m.liveWelcome(); !strings.Contains(got, "no model selected") {
		t.Fatalf("liveWelcome before selection = %q, want it to mention %q", got, "no model selected")
	}

	// Picking a model updates the live banner — it is not frozen.
	m.env.CurrentModel = &llm.CurrentModelInfo{ModelID: "claude-opus-4-8"}
	got := m.liveWelcome()
	if strings.Contains(got, "no model selected") {
		t.Fatalf("liveWelcome after selection still shows %q: %q", "no model selected", got)
	}
	if !strings.Contains(got, "claude-opus-4-8") {
		t.Fatalf("liveWelcome after selection = %q, want it to mention the picked model", got)
	}
}

// On the first commit the banner is frozen into scrollback with the selected
// model, and the live view stops drawing it (no duplicate).
func TestTakeWelcomeBannerFreezesAndClears(t *testing.T) {
	m := &model{env: env{Width: 80}, conv: conv.NewModel(80), welcomePending: true}
	m.env.CurrentModel = &llm.CurrentModelInfo{ModelID: "claude-opus-4-8"}

	banner := m.takeWelcomeBanner()
	if !strings.Contains(banner, "claude-opus-4-8") {
		t.Fatalf("frozen banner = %q, want it to mention the selected model", banner)
	}
	if m.welcomePending {
		t.Fatal("welcomePending should be cleared once the banner is frozen")
	}
	if got := m.liveWelcome(); got != "" {
		t.Fatalf("liveWelcome after freeze = %q, want \"\" (no duplicate in live view)", got)
	}
	if again := m.takeWelcomeBanner(); again != "" {
		t.Fatalf("takeWelcomeBanner is once-only, second call = %q", again)
	}
}

// A completed thinking paragraph (terminated by a blank line) commits to
// scrollback mid-stream, before any content arrives — reasoning no longer waits
// for the whole block to finish. The render runs off-thread; the committed
// offset advances only once it lands.
func TestFlushStreamingBlocksCommitsThinkingParagraph(t *testing.T) {
	m := flushTestModel(core.ChatMessage{
		Role:     core.RoleAssistant,
		Thinking: "first paragraph of reasoning\n\n",
	})

	applyFlush(t, m, m.FlushStreamingBlocks())

	msg := m.conv.Messages[0]
	if msg.ThinkingCommittedLen != len(msg.Thinking) {
		t.Fatalf("ThinkingCommittedLen = %d, want %d", msg.ThinkingCommittedLen, len(msg.Thinking))
	}
	if !msg.ThinkingEmitted {
		t.Fatal("ThinkingEmitted should be set after the first thinking block commits")
	}
	if m.flush.rendering {
		t.Fatal("flush.rendering should clear once the render has landed")
	}
	if len(m.flush.pendingPrints) != 1 ||
		!strings.Contains(m.flush.pendingPrints[0].current+m.flush.pendingPrints[0].remaining, "first paragraph of reasoning") {
		t.Fatalf("scrollback queue = %#v, want the committed block queued once", m.flush.pendingPrints)
	}
}

func TestScrollbackPhysicalLinesMatchBubbleTeaAccounting(t *testing.T) {
	tests := []struct {
		name    string
		content string
		width   int
		rows    int
		plain   string
	}{
		{name: "ANSI styling", content: "\x1b[31mred\x1b[0m", width: 4, rows: 1, plain: "red"},
		{name: "soft wrap", content: "abcde", width: 4, rows: 2, plain: "abcd\ne"},
		{name: "exact-width wrap", content: "abcdefgh", width: 4, rows: 3, plain: "abcd\nefgh\n"},
		{name: "wide graphemes", content: "界界界", width: 4, rows: 2, plain: "界界\n界"},
		{name: "emoji graphemes", content: "🏳️‍🌈🏳️‍🌈🏳️‍🌈", width: 4, rows: 2, plain: "🏳️‍🌈🏳️‍🌈\n🏳️‍🌈"},
		{name: "trailing newline", content: "a\n", width: 4, rows: 2, plain: "a\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := scrollbackPhysicalLines(tt.content, tt.width)
			if len(lines) != tt.rows {
				t.Fatalf("physical rows = %d, want %d", len(lines), tt.rows)
			}
			if plain := trimLinePadding(ansi.Strip(renderScrollbackLines(lines))); plain != tt.plain {
				t.Fatalf("rendered physical lines = %q, want %q", plain, tt.plain)
			}
		})
	}
}

func TestScrollbackChunkingPreservesStyledWrappedContent(t *testing.T) {
	var flush flushState
	content := "\x1b[31mabcdefghij\x1b[0m\nlast\n"
	cmd := flush.queueScrollbackPrint(content, 0)
	if cmd == nil {
		t.Fatal("the first chunk must start immediately")
	}

	var chunks []string
	for cmd != nil {
		ready := cmd().(scrollbackPrintReadyMsg)
		content, ok := flush.prepareScrollbackPrint(ready.id, 4, 5, 3)
		if !ok {
			t.Fatal("ready chunk has no payload")
		}
		chunks = append(chunks, content)
		cmd = flush.finishScrollbackPrint(ready.id)
	}
	if len(flush.pendingPrints) != 0 {
		t.Fatalf("finished queue length = %d, want 0", len(flush.pendingPrints))
	}

	plain := trimLinePadding(ansi.Strip(strings.Join(chunks, "\n")))
	if plain != "abcd\nefgh\nij\nlast\n" {
		t.Fatalf("chunked content = %q, want all physical rows exactly once", plain)
	}
}

func TestScrollbackFullHeightFrameMinimizesAndRestores(t *testing.T) {
	m := flushTestModel(core.ChatMessage{})
	m.env.Height = 1
	cmd := m.queueScrollbackPrint("A", 0)
	if cmd == nil {
		t.Fatal("the first chunk must start immediately")
	}
	ready := cmd().(scrollbackPrintReadyMsg)
	if _, ok := m.prepareScrollbackPrint(ready.id); !ok || !m.flush.minimizeForPrint {
		t.Fatal("a full-height frame must be minimized before printing")
	}
	if frame, ok := m.scrollbackFrameForPrint(); !ok || frame.Content != "" {
		t.Fatalf("frame during print = %#v, ok=%v, want an empty frozen frame", frame, ok)
	}
	if next := m.finishScrollbackPrint(ready.id); next != nil {
		t.Fatal("the one-row payload should finish in one minimized print")
	}
	if m.flush.minimizeForPrint || m.flush.frameForPrint != nil {
		t.Fatal("the managed frame must be restored after the print completes")
	}
}

// A queued Println must not run while a docked approval prompt owns the
// managed frame. insertAbove otherwise promotes that temporary prompt into
// terminal history together with the actual conversation output.
func TestScrollbackPrintWaitsForApprovalModalToClose(t *testing.T) {
	m := dockedModalModel(t, "about to inspect the repository")
	cmd := m.queueScrollbackPrint("COMMITTED_MARKDOWN_BLOCK", 0)
	if cmd == nil {
		t.Fatal("the first queued print must start immediately")
	}
	ready := cmd().(scrollbackPrintReadyMsg)

	if _, next := m.Update(ready); next != nil {
		t.Fatal("a print must be deferred while the approval modal is visible")
	}
	if len(m.flush.pendingPrints) != 1 || m.flush.pendingPrints[0].current != "" {
		t.Fatalf("deferred queue = %#v, want one untouched payload", m.flush.pendingPrints)
	}
	if m.flush.frameForPrint != nil {
		t.Fatal("a deferred print must not freeze the approval frame")
	}

	m.userInput.Approval.Hide()
	resume := m.resumeDeferredScrollbackPrint()
	if resume == nil {
		t.Fatal("closing the modal must restart the deferred print")
	}
	resumed := resume().(scrollbackPrintReadyMsg)
	if resumed.id != ready.id {
		t.Fatalf("resumed print id = %d, want %d", resumed.id, ready.id)
	}
	content, ok := m.flush.prepareScrollbackPrint(resumed.id, m.env.Width, m.env.Height, 0)
	if !ok || !strings.Contains(content, "COMMITTED_MARKDOWN_BLOCK") {
		t.Fatalf("resumed content = %q, ok=%v", content, ok)
	}
}

// A permission prompt opened after a print has been queued has the inverse race:
// the prompt's frame could be captured by insertAbove after it appears. Hold the
// prompt until the final Println completes, then open it on the clean live frame.
func TestDeferredApprovalWaitsForScrollbackHandoff(t *testing.T) {
	m := dockedModalModel(t, "about to inspect the repository")
	m.userInput.Approval.Hide()
	m.deferredApproval = &perm.PermissionRequest{
		ToolName: "Bash",
		BashMeta: &perm.BashMetadata{Command: "git status"},
	}
	cmd := m.queueScrollbackPrint("COMMITTED_BEFORE_APPROVAL", 0)
	if cmd == nil {
		t.Fatal("the queued print must start")
	}
	ready := cmd().(scrollbackPrintReadyMsg)
	if _, ok := m.flush.prepareScrollbackPrint(ready.id, m.env.Width, m.env.Height, 0); !ok {
		t.Fatal("the print should prepare before the approval opens")
	}
	if m.userInput.Approval.IsActive() {
		t.Fatal("approval opened before the scrollback handoff completed")
	}
	if next := m.finishScrollbackPrint(ready.id); next != nil {
		t.Fatal("the one-line print should finish in one chunk")
	}
	if !m.userInput.Approval.IsActive() {
		t.Fatal("approval did not open after the scrollback handoff completed")
	}
}

func TestScrollbackPrintQueueIsSingleFlightFIFO(t *testing.T) {
	m := flushTestModel(core.ChatMessage{})
	firstCmd := m.queueScrollbackPrint("A", 0)
	if firstCmd == nil {
		t.Fatal("the first queued print must start immediately")
	}
	if secondCmd := m.queueScrollbackPrint("B", 0); secondCmd != nil {
		t.Fatal("a second print must wait until the in-flight head completes")
	}

	first := firstCmd().(scrollbackPrintReadyMsg)
	firstContent, ok := m.prepareScrollbackPrint(first.id)
	if !ok || trimLinePadding(firstContent) != "A" {
		t.Fatalf("first print content = %q, ok=%v, want A", firstContent, ok)
	}
	if next := m.finishScrollbackPrint(first.id + 1); next != nil {
		t.Fatal("an out-of-order done message must not advance the queue")
	}
	if len(m.flush.pendingPrints) != 2 {
		t.Fatalf("out-of-order done changed queue length to %d, want 2", len(m.flush.pendingPrints))
	}

	secondCmd := m.finishScrollbackPrint(first.id)
	if secondCmd == nil {
		t.Fatal("finishing the queue head must start the next print")
	}
	second := secondCmd().(scrollbackPrintReadyMsg)
	secondContent, ok := m.prepareScrollbackPrint(second.id)
	if !ok || trimLinePadding(secondContent) != "B" {
		t.Fatalf("second print content = %q, ok=%v, want B", secondContent, ok)
	}
	if next := m.finishScrollbackPrint(second.id); next != nil {
		t.Fatal("finishing the final print must leave no command")
	}
	if len(m.flush.pendingPrints) != 0 {
		t.Fatalf("finished queue length = %d, want 0", len(m.flush.pendingPrints))
	}
}

func TestConsecutiveToolCommitsStayOutOfManagedFrameAndPrintOnceInOrder(t *testing.T) {
	m := &model{
		env:       env{Width: 100, Height: 24},
		conv:      conv.NewModel(100),
		userInput: input.New("", 100, nil, input.SelectorDeps{}),
		services: services{
			Subagent: subagent.NewRegistry(),
			Tracker:  todo.NewStore(),
		},
	}

	bashCall := core.ToolCall{ID: "bash-1", Name: "Bash", Input: `{"command":"git status"}`}
	m.conv.Messages = append(m.conv.Messages,
		core.ChatMessage{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{bashCall}},
		core.ChatMessage{Role: core.RoleUser, Expanded: true, ToolResult: &core.ToolResult{
			ToolCallID: bashCall.ID,
			ToolName:   "Bash",
			Content:    "BASH_RESULT_SENTINEL",
		}},
	)
	firstCmds := m.CommitMessages()
	if len(firstCmds) != 1 || firstCmds[0] == nil {
		t.Fatalf("first commit commands = %#v, want one active print", firstCmds)
	}

	editCall := core.ToolCall{ID: "edit-1", Name: "Edit", Input: `{"file_path":"main.go","old_string":"old","new_string":"EDIT_RESULT_SENTINEL"}`}
	m.conv.Messages = append(m.conv.Messages,
		core.ChatMessage{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{editCall}},
		core.ChatMessage{Role: core.RoleUser, ToolResult: &core.ToolResult{
			ToolCallID: editCall.ID,
			ToolName:   "Edit",
			Content:    "Edited main.go",
			Details: toolresult.FileChangeDetails{
				Path:         "main.go",
				EditCount:    1,
				AddedLines:   1,
				RemovedLines: 1,
				UnifiedDiff:  "@@ -1 +1 @@\n-old\n+EDIT_RESULT_SENTINEL",
			},
		}},
	)
	secondCmds := m.CommitMessages()
	if len(secondCmds) != 1 || secondCmds[0] != nil {
		t.Fatalf("second commit commands = %#v, want one queued nil command", secondCmds)
	}
	if len(m.flush.pendingPrints) != 2 {
		t.Fatalf("queued prints = %d, want 2", len(m.flush.pendingPrints))
	}

	// A live repaint carries the handoff copy of the print in flight — the head,
	// and only the head. Holding it is what keeps the frame from shrinking the
	// moment CommittedCount advances, which is what strands a copy of the live
	// tail in native history (see pendingScrollbackView). A queued print that
	// has not started is not drawn: it would be a second copy of content that is
	// still waiting for its own Println.
	liveFrame := "LIVE_EDIT_ACTIVITY\n" + strings.Repeat("\n", 12) + "INPUT_SENTINEL\nFOOTER_SENTINEL"
	managed := m.renderChatSection(liveFrame, "")
	if !strings.Contains(managed, "BASH_RESULT_SENTINEL") {
		t.Fatalf("managed frame lost the in-flight handoff copy: %q", managed)
	}
	if strings.Contains(managed, "EDIT_RESULT_SENTINEL") {
		t.Fatalf("managed frame drew a print that has not started: %q", managed)
	}
	for _, live := range []string{"INPUT_SENTINEL", "FOOTER_SENTINEL"} {
		if !strings.Contains(managed, live) {
			t.Fatalf("managed frame should retain live marker %q: %q", live, managed)
		}
	}

	first := firstCmds[0]().(scrollbackPrintReadyMsg)
	firstContent, ok := m.prepareScrollbackPrint(first.id)
	if !ok {
		t.Fatal("Bash print command has no current payload")
	}
	secondCmd := m.finishScrollbackPrint(first.id)
	if secondCmd == nil {
		t.Fatal("finishing Bash must start the queued Edit print")
	}
	second := secondCmd().(scrollbackPrintReadyMsg)
	secondContent, ok := m.prepareScrollbackPrint(second.id)
	if !ok {
		t.Fatal("Edit print command has no current payload")
	}
	m.finishScrollbackPrint(second.id)

	nativePayloads := firstContent + "\n" + secondContent
	for _, result := range []string{"BASH_RESULT_SENTINEL", "EDIT_RESULT_SENTINEL"} {
		if count := strings.Count(nativePayloads, result); count != 1 {
			t.Fatalf("native payload count for %q = %d, want 1: %q", result, count, nativePayloads)
		}
	}
	if strings.Index(nativePayloads, "BASH_RESULT_SENTINEL") > strings.Index(nativePayloads, "EDIT_RESULT_SENTINEL") {
		t.Fatalf("native payload order is not Bash then Edit: %q", nativePayloads)
	}
	for _, live := range []string{"LIVE_EDIT_ACTIVITY", "INPUT_SENTINEL", "FOOTER_SENTINEL"} {
		if strings.Contains(nativePayloads, live) {
			t.Fatalf("native payload contains live-frame marker %q: %q", live, nativePayloads)
		}
	}
	if strings.Contains(nativePayloads, strings.Repeat("\n", 8)) {
		t.Fatalf("native payload contains live blank repaint space: %q", nativePayloads)
	}
}

// The still-streaming trailing paragraph (no terminating blank line) stays in
// the live view until it completes — exactly like content's trailing block.
func TestFlushStreamingBlocksHoldsIncompleteThinking(t *testing.T) {
	m := flushTestModel(core.ChatMessage{
		Role:     core.RoleAssistant,
		Thinking: "still streaming this paragraph",
	})

	if cmds := m.FlushStreamingBlocks(); cmds != nil {
		t.Fatal("an incomplete thinking paragraph must stay in the live view")
	}
	if got := m.conv.Messages[0].ThinkingCommittedLen; got != 0 {
		t.Fatalf("ThinkingCommittedLen = %d, want 0 (nothing committed)", got)
	}
}

// When content starts — the reliable "reasoning done" signal — thinking's
// trailing paragraph is flushed too, so nothing reasoning-side lingers.
func TestFlushStreamingBlocksFlushesTrailingThinkingOnContent(t *testing.T) {
	m := flushTestModel(core.ChatMessage{
		Role:     core.RoleAssistant,
		Thinking: "reasoning with no trailing blank line",
		Content:  "Here",
	})

	applyFlush(t, m, m.FlushStreamingBlocks())

	msg := m.conv.Messages[0]
	if msg.ThinkingCommittedLen != len(msg.Thinking) {
		t.Fatalf("thinking should be fully committed once content starts, got %d/%d",
			msg.ThinkingCommittedLen, len(msg.Thinking))
	}
}

// Only one block render is in flight at a time: while one is rendering off-
// thread, a second flush is suppressed so the scrollback Printlns stay ordered.
func TestFlushStreamingBlocksGatesWhileRendering(t *testing.T) {
	m := flushTestModel(core.ChatMessage{
		Role:    core.RoleAssistant,
		Content: "first block\n\nsecond block\n\n",
	})

	if cmds := m.FlushStreamingBlocks(); len(cmds) == 0 {
		t.Fatal("the first completed block should start a render")
	}
	if !m.flush.rendering {
		t.Fatal("flush.rendering should latch while a render is in flight")
	}
	if cmds := m.FlushStreamingBlocks(); cmds != nil {
		t.Fatal("a second flush must be suppressed while one render is in flight")
	}
}

// A render that lands after its row was already committed whole (turn-end or
// cancel commits the remainder, in-flight block included) is dropped — no
// duplicate Println, and flush.rendering still clears.
func TestHandleFlushResultDiscardsCommittedRow(t *testing.T) {
	m := flushTestModel(core.ChatMessage{
		Role:    core.RoleAssistant,
		Content: "a block\n\n",
	})

	cmds := m.FlushStreamingBlocks()
	if len(cmds) == 0 {
		t.Fatal("expected a render Cmd")
	}
	br, ok := cmds[0]().(flushResultMsg)
	if !ok {
		t.Fatal("flush Cmd did not return a flushResultMsg")
	}

	// The row got committed to scrollback before the render landed.
	m.conv.CommittedCount = 1
	if cmd := m.handleFlushResult(br); cmd != nil {
		t.Fatal("a render for an already-committed row must be discarded (no Println)")
	}
	if m.flush.rendering {
		t.Fatal("flush.rendering must clear even when the render is discarded")
	}
}

// A render that lands after its row was dropped and replaced by a retry's fresh
// row (new message ID) is dropped rather than corrupting the new row's offsets.
func TestHandleFlushResultDiscardsReplacedRow(t *testing.T) {
	m := flushTestModel(core.ChatMessage{
		Role:    core.RoleAssistant,
		ID:      "old",
		Content: "a block\n\n",
	})

	cmds := m.FlushStreamingBlocks()
	if len(cmds) == 0 {
		t.Fatal("expected a render Cmd")
	}
	br := cmds[0]().(flushResultMsg)

	// Retry dropped the streaming row and appended a fresh one (new ID).
	m.conv.Messages[0] = core.ChatMessage{Role: core.RoleAssistant, ID: "new", Content: "retried"}
	if cmd := m.handleFlushResult(br); cmd != nil {
		t.Fatal("a render for a replaced row must be discarded")
	}
	if got := m.conv.Messages[0].ContentCommittedLen; got != 0 {
		t.Fatalf("the fresh row's ContentCommittedLen = %d, want 0 (stale render must not advance it)", got)
	}
}

// A fullscreen picker holds the queue exactly like a docked prompt does, and
// closing it has to restart the queue. Nothing in the picker's own dismissal
// says "approval answered", so a resume wired to the three prompt replies never
// fired: the head stalled, every block queued behind it, and renderAndCommit
// had already advanced CommittedCount — so the live tail had stopped drawing
// what the queue was still holding. The output was on neither.
func TestScrollbackPrintResumesWhenAnyOverlayCloses(t *testing.T) {
	m := dockedModalModel(t, "about to inspect the repository")
	m.userInput.Approval.Hide()
	m.userInput.Config.Enter(m.env.Width, m.env.Height)
	if _, active := m.activeOverlay(); !active {
		t.Fatal("the config picker did not become the active overlay")
	}

	cmd := m.queueScrollbackPrint("COMMITTED_MARKDOWN_BLOCK", 0)
	if cmd == nil {
		t.Fatal("the first queued print must start immediately")
	}
	ready := cmd().(scrollbackPrintReadyMsg)
	if _, next := m.Update(ready); next != nil {
		t.Fatal("a print must be deferred while the picker is up")
	}

	// Esc, routed the way a keypress reaches the panel.
	if _, resume := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); resume == nil {
		t.Fatal("closing the picker must restart the deferred print")
	}
	if _, active := m.activeOverlay(); active {
		t.Fatal("the picker is still up after Esc")
	}
	if len(m.flush.pendingPrints) != 1 || m.flush.pendingPrints[0].current != "" {
		t.Fatalf("deferred queue = %#v, want one untouched payload", m.flush.pendingPrints)
	}
	content, ok := m.flush.prepareScrollbackPrint(
		m.flush.pendingPrints[0].id, m.env.Width, m.env.Height, 0)
	if !ok || !strings.Contains(content, "COMMITTED_MARKDOWN_BLOCK") {
		t.Fatalf("resumed content = %q, ok=%v", content, ok)
	}
}

// The room above the frame is what caps a chunk, so the frame has to be counted
// the way the chunk is: in physical rows. A frame line wider than the terminal
// occupies more rows than it has newlines, and counting newlines instead
// overstates the room — insertAbove then scrolls live rows into native history.
func TestScrollbackFrameHeightCountsWrappedRows(t *testing.T) {
	wide := strings.Repeat("x", 100)
	logical := strings.Count(wide, "\n") + 1
	physical := len(scrollbackPhysicalLines(wide, 40))
	if physical <= logical {
		t.Fatalf("physical rows = %d, newline count = %d — the case this guards cannot occur",
			physical, logical)
	}
}

// The handoff copy keeps a committed block drawn in the managed view until its
// Println has landed. Without it the frame shrinks the moment CommittedCount
// advances, and Bubble Tea's inline renderer leaves the vacated rows on screen —
// a plain copy of the streaming tail welded into native scrollback, with the
// rendered version printed underneath it.
func TestCommittedBlockStaysInTheFrameUntilItsPrintLands(t *testing.T) {
	m := flushTestModel(core.ChatMessage{})
	cmd := m.queueScrollbackPrint("RENDERED_BLOCK", 0)
	if cmd == nil {
		t.Fatal("the first queued print must start")
	}
	if view := m.pendingScrollbackView(); !strings.Contains(view, "RENDERED_BLOCK") {
		t.Fatalf("handoff copy = %q, want the queued block", view)
	}

	ready := cmd().(scrollbackPrintReadyMsg)
	if _, ok := m.flush.prepareScrollbackPrint(ready.id, m.env.Width, m.env.Height, 0); !ok {
		t.Fatal("the print did not prepare")
	}
	if view := m.pendingScrollbackView(); !strings.Contains(view, "RENDERED_BLOCK") {
		t.Fatalf("handoff copy during the print = %q, want the block still drawn", view)
	}

	m.finishScrollbackPrint(ready.id)
	if view := m.pendingScrollbackView(); view != "" {
		t.Fatalf("handoff copy after the print = %q, want it dropped", view)
	}
}

// The rendered block is shorter than the plain wrapped tail it replaces, and
// every row of that difference is a row the frame vacates — so the copy is
// padded to what the live tail occupied.
func TestHandoffCopyKeepsTheFrameAtItsPreCommitHeight(t *testing.T) {
	m := flushTestModel(core.ChatMessage{})
	m.queueScrollbackPrint("ONE\nTWO", 6)
	cmd := printScrollback(m.flush.pendingPrints[0].id)
	ready := cmd().(scrollbackPrintReadyMsg)
	m.flush.prepareScrollbackPrint(ready.id, m.env.Width, m.env.Height, 0)

	if rows := viewRows(m.pendingScrollbackView()); rows != 6 {
		t.Fatalf("handoff rows = %d, want the pre-commit height 6", rows)
	}

	// Never trimmed: a copy taller than the tail it replaces is the frame
	// growing, which the renderer handles.
	m.flush.pendingPrints[0].frameRows = 1
	if rows := viewRows(m.pendingScrollbackView()); rows != 2 {
		t.Fatalf("handoff rows = %d, want the block's own 2", rows)
	}
}

// A payload taller than the room above the frame is printed in several chunks.
// The handoff copy stands in for the tail only until the first chunk goes out;
// after that part of the payload is already in scrollback, and holding the rest
// in the frame is what the next chunk's insertAbove welds into the middle of
// the output it belongs to.
func TestHandoffCopyDropsTheRemainderOncePrintingStarts(t *testing.T) {
	m := flushTestModel(core.ChatMessage{})
	m.env.Height = 6
	tall := strings.Join([]string{"L1", "L2", "L3", "L4", "L5", "L6", "L7", "L8"}, "\n")
	cmd := m.queueScrollbackPrint(tall, 0)
	if cmd == nil {
		t.Fatal("the first queued print must start")
	}

	// Before the first chunk: the whole payload stands in for the emptied tail.
	if view := m.pendingScrollbackView(); !strings.Contains(view, "L8") {
		t.Fatalf("handoff copy before printing = %q, want the whole payload", view)
	}

	ready := cmd().(scrollbackPrintReadyMsg)
	chunk, ok := m.flush.prepareScrollbackPrint(ready.id, m.env.Width, m.env.Height, 2)
	if !ok {
		t.Fatal("the print did not prepare")
	}
	if m.flush.pendingPrints[0].remaining == "" {
		t.Fatal("the payload fit in one chunk; this test needs it split")
	}

	// Once printing has started, only the chunk in flight.
	view := m.pendingScrollbackView()
	if view != chunk {
		t.Fatalf("handoff copy = %q, want only the chunk in flight %q", view, chunk)
	}

	// And nothing at all between chunks.
	m.finishScrollbackPrint(ready.id)
	if view := m.pendingScrollbackView(); view != "" {
		t.Fatalf("handoff copy between chunks = %q, want none", view)
	}
}
