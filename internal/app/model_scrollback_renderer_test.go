package app

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ActiveState/vt10x"
)

const (
	rendererBarrierOldFrame = "STALE-LIVE-FRAME\nSECOND-STALE-ROW"
	rendererBarrierNewFrame = "CURRENT-MANAGED-FRAME"
	rendererBarrierCommit   = "COMMITTED-SCROLLBACK-BLOCK"
)

type synchronizedOutput struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	writes chan struct{}
}

func (o *synchronizedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	n, err := o.buf.Write(p)
	o.mu.Unlock()
	select {
	case o.writes <- struct{}{}:
	default:
	}
	return n, err
}

func (o *synchronizedOutput) Reset() {
	o.mu.Lock()
	o.buf.Reset()
	o.mu.Unlock()
}

func (o *synchronizedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (o *synchronizedOutput) WaitFor(marker string, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if strings.Contains(o.String(), marker) {
			return true
		}
		select {
		case <-o.writes:
		case <-timer.C:
			return false
		}
	}
}

type beginRendererBarrierHandoffMsg struct{}

type rendererBarrierModel struct {
	output    *synchronizedOutput
	committed bool
}

func (m *rendererBarrierModel) Init() tea.Cmd {
	return nil
}

func (m *rendererBarrierModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(beginRendererBarrierHandoffMsg); !ok {
		return m, nil
	}

	// Discard startup rendering. From here on the assertion only observes the
	// handoff: CURRENT must be flushed before COMMITTED is inserted.
	m.output.Reset()
	m.committed = true
	return m, tea.Sequence(
		tea.Println(rendererBarrierCommit),
		tea.Quit,
	)
}

func (m *rendererBarrierModel) View() tea.View {
	if m.committed {
		return tea.NewView(rendererBarrierNewFrame)
	}
	return tea.NewView(rendererBarrierOldFrame)
}

// A Println must flush the newest queued View before insertAbove scrolls the
// managed frame. Without the downstream Bubble Tea barrier, COMMITTED appears
// first and CURRENT is only flushed during shutdown, reproducing the stale-frame
// race behind #314 without depending on a real terminal.
func TestScrollbackPrintFlushesCurrentFrameBeforeInsert(t *testing.T) {
	output := &synchronizedOutput{writes: make(chan struct{}, 1)}
	m := &rendererBarrierModel{output: output}
	program := tea.NewProgram(
		m,
		tea.WithInput(nil),
		tea.WithOutput(output),
		// Waiting until the 1 FPS renderer writes the old frame gives the test
		// almost a full second to send Println before another ticker flush.
		tea.WithFPS(1),
		tea.WithWindowSize(80, 24),
	)
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()

	if !output.WaitFor(rendererBarrierOldFrame, 3*time.Second) {
		program.Kill()
		<-done
		t.Fatal("timed out waiting for the stale frame to reach the renderer")
	}

	// The old frame is now physical and the next automatic flush is one second
	// away. Ask Update to queue the shorter current frame and Println at once.
	output.Reset()
	program.Send(beginRendererBarrierHandoffMsg{})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run renderer handoff model: %v", err)
		}
	case <-time.After(3 * time.Second):
		program.Kill()
		<-done
		t.Fatal("timed out waiting for the renderer handoff")
	}

	rendered := output.String()
	currentAt := strings.Index(rendered, rendererBarrierNewFrame)
	committedAt := strings.Index(rendered, rendererBarrierCommit)
	if currentAt < 0 || committedAt < 0 {
		t.Fatalf(
			"handoff output missing markers (current=%d, committed=%d): %q",
			currentAt,
			committedAt,
			rendered,
		)
	}
	if currentAt > committedAt {
		t.Fatalf(
			"scrollback inserted before the current managed frame was flushed "+
				"(current=%d, committed=%d): %q",
			currentAt,
			committedAt,
			rendered,
		)
	}
}

const (
	nativeHistoryWidth  = 20
	nativeHistoryHeight = 10
	nativeFrameHeight   = 6

	nativeThinking = "THINKING-LIVE"
	nativeInput    = "INPUT-LIVE"
	nativeFooter   = "FOOTER-LIVE"
	nativePadding  = "PADDING-LIVE-"
	nativeBash     = "BASH-ROW-"
	nativeEdit     = "EDIT-ROW"
)

type beginNativeHistoryCommitsMsg struct{}

type nativeHistoryModel struct {
	model
	started   bool
	committed chan struct{}
}

func newNativeHistoryModel() *nativeHistoryModel {
	return &nativeHistoryModel{
		model:     model{env: env{Width: nativeHistoryWidth, Height: nativeHistoryHeight}},
		committed: make(chan struct{}, 1),
	}
}

func (m *nativeHistoryModel) Init() tea.Cmd {
	return nil
}

func (m *nativeHistoryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case beginNativeHistoryCommitsMsg:
		m.started = true
		first := m.queueScrollbackPrint(strings.Join([]string{
			nativeBash + "1",
			nativeBash + "2",
			nativeBash + "3",
			nativeBash + "4",
			nativeBash + "5",
			nativeBash + "6",
			nativeBash + "7",
			nativeBash + "8",
		}, "\n"), 0)
		m.queueScrollbackPrint(nativeEdit, 0)
		return m, first
	case scrollbackPrintReadyMsg:
		frame := m.View()
		content, ok := m.flush.prepareScrollbackPrint(
			msg.id,
			nativeHistoryWidth,
			nativeHistoryHeight,
			nativeFrameHeight,
		)
		if !ok {
			return m, nil
		}
		if m.flush.minimizeForPrint {
			frame = tea.NewView("")
		}
		m.flush.frameForPrint = &frame
		return m, tea.Sequence(
			tea.Println(content),
			func() tea.Msg { return scrollbackPrintDoneMsg{id: msg.id} },
		)
	case scrollbackPrintDoneMsg:
		next := m.finishScrollbackPrint(msg.id)
		if len(m.flush.pendingPrints) == 0 {
			m.committed <- struct{}{}
			return m, nil
		}
		return m, next
	}
	return m, nil
}

func (m *nativeHistoryModel) View() tea.View {
	if frame, ok := m.scrollbackFrameForPrint(); ok {
		return frame
	}
	if !m.started {
		return tea.NewView(strings.Join([]string{
			"OLD-LIVE-1",
			"OLD-LIVE-2",
			"OLD-LIVE-3",
			"OLD-LIVE-4",
			"OLD-LIVE-5",
			"OLD-LIVE-6",
		}, "\n"))
	}
	return tea.NewView(strings.Join([]string{
		nativeThinking,
		nativePadding + "1",
		nativePadding + "2",
		nativeInput,
		nativePadding + "3",
		nativeFooter,
	}, "\n"))
}

func (m *nativeHistoryModel) queueScrollbackPrint(content string, frameRows int) tea.Cmd {
	return m.flush.queueScrollbackPrint(content, frameRows)
}

func (m *nativeHistoryModel) finishScrollbackPrint(id uint64) tea.Cmd {
	return m.flush.finishScrollbackPrint(id)
}

type terminalHistoryState struct {
	mu sync.Mutex
	*vt10x.VT
	state *vt10x.State
}

func newTerminalHistoryState(width, height int) *terminalHistoryState {
	state := &vt10x.State{RecordHistory: true}
	terminal, err := vt10x.Create(state, nil)
	if err != nil {
		panic(err)
	}
	terminal.Resize(width, height)
	return &terminalHistoryState{VT: terminal, state: state}
}

func (t *terminalHistoryState) snapshot() (history, screen string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, globalY := t.state.GlobalCursor()
	_, cursorY := t.state.Cursor()
	historyRows := globalY - cursorY
	allToCursor := strings.Split(t.state.StringToCursorFrom(0, 0), "\n")
	return strings.Join(allToCursor[:min(historyRows, len(allToCursor))], "\n"), t.state.String()
}

func (t *terminalHistoryState) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := t.VT.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n")))
	return len(p), err
}

// A payload taller than the physical rows above the live managed frame must be
// split before Println. Otherwise insertAbove scrolls the live Thinking/input/
// footer rows through the terminal top and irreversibly adds them to history.
func TestConsecutiveTallScrollbackCommitsPreserveNativeHistory(t *testing.T) {
	terminal := newTerminalHistoryState(nativeHistoryWidth, nativeHistoryHeight)
	m := newNativeHistoryModel()
	program := tea.NewProgram(
		m,
		tea.WithInput(nil),
		tea.WithOutput(terminal),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "TERM_PROGRAM=Apple_Terminal"}),
		tea.WithFPS(60),
		tea.WithWindowSize(nativeHistoryWidth, nativeHistoryHeight),
	)
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	defer func() {
		program.Quit()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("stop native history model: %v", err)
			}
		case <-time.After(3 * time.Second):
			program.Kill()
			<-done
			t.Error("timed out stopping native history model")
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, screen := terminal.snapshot()
		if strings.Contains(screen, "OLD-LIVE-1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the initial managed frame")
		}
		time.Sleep(time.Millisecond)
	}
	program.Send(beginNativeHistoryCommitsMsg{})

	select {
	case <-m.committed:
	case err := <-done:
		t.Fatalf("native history model stopped before the snapshot: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for native history commits")
	}

	// The done message follows the final Println in one tea.Sequence, so this
	// snapshot observes terminal history after insertion and before Bubble Tea's
	// shutdown newlines can scroll the managed frame.
	history, screen := terminal.snapshot()
	all := history + "\n" + screen
	for _, live := range []string{nativeThinking, nativeInput, nativeFooter, nativePadding} {
		if strings.Contains(history, live) {
			t.Fatalf("native history contains managed-frame row %q:\n%s", live, history)
		}
	}
	committedRows := []string{
		nativeBash + "1",
		nativeBash + "2",
		nativeBash + "3",
		nativeBash + "4",
		nativeBash + "5",
		nativeBash + "6",
		nativeBash + "7",
		nativeBash + "8",
		nativeEdit,
	}
	previous := -1
	for _, row := range committedRows {
		if count := strings.Count(all, row); count != 1 {
			t.Fatalf("terminal contains %d copies of %q, want 1:\n%s", count, row, all)
		}
		at := strings.Index(all, row)
		if at <= previous {
			t.Fatalf("terminal rows are not FIFO at %q:\n%s", row, all)
		}
		previous = at
	}
}
