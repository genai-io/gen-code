package core

import (
	"iter"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"

	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Compaction must record the synthetic summary as a normal message.appended
// (so replay can resolve the ID the next inference references) and emit a
// CompactEvent whose SummaryMessageID equals that summary's ID (so replay truncates
// the summarized-away history at the summary).
func TestCompactRecordsSummaryAppendAndBoundary(t *testing.T) {
	var captured []Event
	ag := NewAgent(Config{
		ID:     "test",
		Client: testClient(newBlockingLLM(1)),
		System: NewSystem(),
		Tools:  NewTools(),
		CompactFunc: func(_ context.Context, _ []Message) (string, error) {
			return "the summary", nil
		},
		OnEvent: func(e Event) { captured = append(captured, e) },
	})
	a := ag.(*agent)

	// Drain the outbox so the blocking CompactEvent emit doesn't stall.
	go func() {
		for range ag.Outbox() {
		}
	}()

	a.SetMessages([]Message{
		UserMessage("hi", nil),
		AssistantMessage("hello", "", nil),
		UserMessage("tell me more", nil),
	})

	shorter, err := a.summarise(context.Background(), a.Messages(), "auto")
	if err != nil || shorter == nil {
		t.Fatalf("summarise() gave nothing: %v", err)
	}
	a.SetMessages(shorter)

	var summaryAppend *Message
	var info *CompactInfo
	for _, e := range captured {
		switch e.Type {
		case OnAppend:
			if m, ok := e.Data.(Message); ok {
				mm := m
				summaryAppend = &mm
			}
		case OnCompact:
			if ci, ok := e.CompactInfo(); ok {
				c := ci
				info = &c
			}
		}
	}

	if summaryAppend == nil {
		t.Fatal("compact did not emit OnAppend for the summary message")
	}
	if summaryAppend.ID == "" {
		t.Fatal("summary message must carry a stable ID")
	}
	if info == nil {
		t.Fatal("compact did not emit OnCompact")
	}
	if info.SummaryMessageID != summaryAppend.ID {
		t.Fatalf("SummaryMessageID %q must equal the appended summary ID %q", info.SummaryMessageID, summaryAppend.ID)
	}

	msgs := a.Messages()
	if len(msgs) != 1 || msgs[0].ID != summaryAppend.ID {
		t.Fatalf("post-compact chain must be the single summary, got %d messages", len(msgs))
	}
	if !strings.Contains(msgs[0].Text(), "the summary") {
		t.Fatalf("summary content missing from chain: %q", msgs[0].Text())
	}
}

// Auto-compaction must announce its start (with the pre-compaction message
// count) before the blocking summarization call, so the UI can show progress
// instead of appearing frozen, and that start must precede the OnCompact.
func TestCompactEmitsStartBeforeBoundary(t *testing.T) {
	var captured []Event
	ag := NewAgent(Config{
		ID:       "test",
		Client:   testClient(&talkingLLM{text: "done"}),
		System:   NewSystem(),
		Tools:    NewTools(),
		MaxSteps: 1,
		// One token of room, so the first boundary is already over budget and
		// the hook fires on the exchange below.
		InputLimit: func() int { return 1 },
		CompactFunc: func(_ context.Context, _ []Message) (string, error) {
			return "the summary", nil
		},
		OnEvent: func(e Event) { captured = append(captured, e) },
	})
	a := ag.(*agent)

	go func() {
		for range ag.Outbox() {
		}
	}()

	a.SetMessages([]Message{
		UserMessage("hi", nil),
		AssistantMessage("hello", "", nil),
		UserMessage("tell me more", nil),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := a.ThinkAct(ctx); err != nil {
		t.Fatalf("ThinkAct: %v", err)
	}

	startIdx, compactIdx := -1, -1
	for i, e := range captured {
		switch e.Type {
		case OnCompactStart:
			if _, ok := e.CompactStart(); !ok {
				t.Fatalf("OnCompactStart carried %T, want CompactStart", e.Data)
			}
			if startIdx < 0 {
				startIdx = i
			}
		case OnCompact:
			if compactIdx < 0 {
				compactIdx = i
			}
		}
	}

	if startIdx < 0 {
		t.Fatal("compaction did not announce its start")
	}
	if compactIdx < 0 {
		t.Fatal("compaction did not record its boundary")
	}
	if startIdx > compactIdx {
		t.Fatalf("OnCompactStart (idx %d) must precede OnCompact (idx %d)", startIdx, compactIdx)
	}
}

// The #338 regression — measuring the uncached delta instead of the whole
// prompt, so auto-compaction never fired — is gone by construction:
// PreStepContext.Tokens is the whole prompt, measured fresh at every
// boundary, tool schemas included. There is no remembered figure to get
// wrong and nothing here left to assert.

func newAgentForPromptSizing(t *testing.T) *agent {
	t.Helper()
	ag := NewAgent(Config{
		ID:     "test",
		Client: testClient(newBlockingLLM(1)),
		System: NewSystem(),
		Tools:  NewTools(),
	})
	go func() {
		for range ag.Outbox() {
		}
	}()
	return ag.(*agent)
}

// Nothing to clear any more: the size is measured fresh at each boundary,
// so a just-shortened conversation cannot still read as full.

// A SigCompact applies an in-place compaction (replacing the chain with the
// precomputed summary, recording the manual boundary) and must NOT start a
// turn — otherwise the lone summary would trigger a spurious inference.
func TestIngestSigCompactAppliesInPlaceWithoutStartingTurn(t *testing.T) {
	var captured []Event
	ag := NewAgent(Config{
		ID:      "test",
		Client:  testClient(newBlockingLLM(1)),
		System:  NewSystem(),
		Tools:   NewTools(),
		OnEvent: func(e Event) { captured = append(captured, e) },
	})
	a := ag.(*agent)
	go func() {
		for range ag.Outbox() {
		}
	}()

	a.SetMessages([]Message{
		UserMessage("hi", nil),
		AssistantMessage("hello", "", nil),
		UserMessage("more", nil),
	})

	if a.ingest(context.Background(), Inbound{Signal: SigCompact, Summary: "the summary"}) {
		t.Fatal("SigCompact must not start a turn")
	}

	msgs := a.Messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text(), "the summary") {
		t.Fatalf("SigCompact should compact in place to the single summary, got %d messages", len(msgs))
	}

	var info *CompactInfo
	for _, e := range captured {
		if e.Type == OnCompact {
			if ci, ok := e.CompactInfo(); ok {
				c := ci
				info = &c
			}
		}
	}
	if info == nil {
		t.Fatal("SigCompact should emit a CompactEvent")
	}
	if info.Trigger != "manual" {
		t.Fatalf("manual compaction trigger = %q, want manual", info.Trigger)
	}
	if info.SummaryMessageID == "" || info.SummaryMessageID != msgs[0].ID {
		t.Fatalf("boundary %q must equal the summary message ID %q", info.SummaryMessageID, msgs[0].ID)
	}

	if !a.ingest(context.Background(), Inbound{Msg: UserMessage("next", nil)}) {
		t.Fatal("a normal user message must start a turn")
	}
}

// blockingLLM blocks Infer until the caller pushes a release signal. The
// release channel is buffered so the test can enqueue signals without
// racing the agent goroutine's read of the field.
type blockingLLM struct {
	release chan struct{}
}

func newBlockingLLM(capacity int) *blockingLLM {
	return &blockingLLM{release: make(chan struct{}, capacity)}
}

func (b *blockingLLM) Name() string { return "blocking" }

func (b *blockingLLM) Stream(ctx context.Context, _ *ai.Request) iter.Seq2[ai.Delta, error] {
	return func(yield func(ai.Delta, error) bool) {
		select {
		case <-ctx.Done():
			yield(ai.Delta{}, ctx.Err())
		case <-b.release:
			for _, d := range deltas(InferResponse{Content: "released", StopReason: StopEndTurn}) {
				if !yield(d, nil) {
					return
				}
			}
		}
	}
}

func TestInterruptCurrentTurnReturnsToWaitInsteadOfEndingRun(t *testing.T) {
	llm := newBlockingLLM(4)
	ag := NewAgent(Config{
		ID:     "test",
		Client: testClient(llm),
		System: NewSystem(),
		Tools:  NewTools(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- ag.Run(ctx) }()

	// Drain outbox in the background so emit calls don't block.
	go func() {
		for range ag.Outbox() {
		}
	}()

	// Kick off the first turn, then interrupt while Infer is blocked.
	ag.Inbox() <- Inbound{Msg: UserMessage("first", nil)}
	// turn is stored at the top of each inner-loop iteration, right
	// before ThinkAct is called — wait until that pointer is published.
	waitFor(t, "agent turn to be stored", func() bool {
		return ag.(*agent).turn.Load() != nil
	})

	done := ag.InterruptCurrentTurn()

	// InterruptCurrentTurn's done channel should close once ThinkAct
	// has fully unwound — i.e. before any racing caller-side mutation
	// of agent state can collide with the agent goroutine.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("InterruptCurrentTurn done channel did not close")
	}

	// Resume by sending a second message and releasing the LLM. The
	// release channel is buffered so the test never races the agent's
	// read of it. Waiting on turn.Load() instead of sleeping proves the
	// second turn actually entered Infer.
	ag.Inbox() <- Inbound{Msg: UserMessage("second", nil)}
	waitFor(t, "second turn to enter Infer", func() bool {
		return ag.(*agent).turn.Load() != nil
	})
	llm.release <- struct{}{}

	// Wait for the second turn to drain fully before sending SigStop so
	// the test asserts the resume path actually executed, rather than
	// passing because SigStop preempted a never-started second turn.
	waitFor(t, "second turn to unwind", func() bool {
		return ag.(*agent).turn.Load() == nil
	})

	ag.Inbox() <- Inbound{Signal: SigStop}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after SigStop")
	}
}

// TestInterruptBetweenTurnsIsLatched verifies that an interrupt fired between
// two of Run's inner-loop iterations (turn pointer nil) is not dropped — the
// next runOneTurn must see the latch and bail. Driven through runOneTurn
// directly because that window is only a few instructions wide.
func TestInterruptBetweenTurnsIsLatched(t *testing.T) {
	llm := newBlockingLLM(4)
	ag := NewAgent(Config{
		ID:     "test",
		Client: testClient(llm),
		System: NewSystem(),
		Tools:  NewTools(),
	}).(*agent)
	go func() {
		for range ag.Outbox() {
		}
	}()

	// No turn in flight, so only the latch can carry this interrupt.
	done := ag.InterruptCurrentTurn()
	select {
	case <-done:
	default:
		t.Fatal("between-turn interrupt should return an already-closed done channel")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err, interrupted := ag.runOneTurn(ctx)
	if !interrupted {
		t.Fatal("runOneTurn ran a turn the latch should have stopped")
	}
	if result != nil || err != nil {
		t.Fatalf("latched turn returned result=%v err=%v, want both nil", result, err)
	}
	if ag.interruptPending.Load() {
		t.Error("latch should be consumed by the runOneTurn that acted on it")
	}
}

// TestIdleInterruptDoesNotEatTheNextMessage pins the other half of the latch
// rule. InterruptCurrentTurn latches unconditionally, so an Esc landing while
// the agent sits idle in waitForInput left the latch set with no turn to
// consume it — and the next user message was dropped without inferring.
func TestIdleInterruptDoesNotEatTheNextMessage(t *testing.T) {
	llm := newBlockingLLM(4)
	llm.release <- struct{}{}
	ag := NewAgent(Config{
		ID:     "test",
		Client: testClient(llm),
		System: NewSystem(),
		Tools:  NewTools(),
	})
	go func() {
		for range ag.Outbox() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- ag.Run(ctx) }()

	// Interrupt while the agent is parked in waitForInput.
	<-ag.InterruptCurrentTurn()

	ag.Inbox() <- Inbound{Msg: UserMessage("answer me", nil)}

	// blockingLLM only replies once its release token is read, so a consumed
	// token proves Infer was reached.
	waitFor(t, "the message after an idle interrupt to reach inference", func() bool {
		return len(llm.release) == 0
	})

	ag.Inbox() <- Inbound{Signal: SigStop}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after SigStop")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", what)
}

// The rule that used to live in the loop — a batch runs in parallel only if
// every call in it is read-only, or every one spawns an agent — is now a
// property each tool declares. What is checked here is the translation: a tool
// that may touch shared state comes back marked, and one that only looks comes
// back as it went in.
func TestOnlyTheToolsThatMayTouchAnythingAreMarkedSequential(t *testing.T) {
	tools := []Tool{
		namedTool("Read"), namedTool("WebFetch"), namedTool("WebSearch"), namedTool("LSP"),
		namedTool("Agent"), namedTool("SendMessage"),
		namedTool("Edit"), namedTool("Bash"), namedTool("Write"),
	}
	ag := NewAgent(Config{
		ID: "test", Client: testClient(newBlockingLLM(1)),
		System: NewSystem(), Tools: NewTools(tools...),
	}).(*agent)

	unmarked := map[string]bool{}
	for _, t := range ag.offered() {
		// Every tool is wrapped once to carry the call's ID; a marked one is
		// wrapped again by Sequential, so it is no longer that inner wrapper.
		_, plain := t.(withCallID)
		unmarked[t.Schema().Name] = plain
	}

	for _, name := range []string{"Read", "WebFetch", "WebSearch", "LSP", "Agent", "SendMessage"} {
		if !unmarked[name] {
			t.Errorf("%s was marked sequential; it only looks at things", name)
		}
	}
	for _, name := range []string{"Edit", "Bash", "Write"} {
		if unmarked[name] {
			t.Errorf("%s was not marked sequential; it may touch shared state", name)
		}
	}
}

type stubTool struct{ name string }

func namedTool(name string) Tool { return stubTool{name: name} }

func (s stubTool) Schema() ToolSchema { return ToolSchema{Name: s.name} }
func (s stubTool) Run(context.Context, ai.ToolCall) (sdkagent.Result, error) {
	return sdkagent.TextResult("ok"), nil
}

// ai.IsContextExceeded is the SDK's, and the table of provider phrasings
// with it.

type stubContextExceeded struct{}

func (stubContextExceeded) Error() string    { return "prompt too long" }
func (stubContextExceeded) ContextExceeded() {}

// cancelOnRunTool counts its executions and how many ran on an already-dead
// ctx — a tool that works before consulting ctx, which nothing forbids.
type cancelOnRunTool struct {
	name      string
	ran       *atomic.Int32
	ranOnDead *atomic.Int32
	onRun     func()
}

func (c cancelOnRunTool) Schema() ToolSchema {
	return ToolSchema{Name: c.name, Description: "records its execution"}
}

func (c cancelOnRunTool) Run(ctx context.Context, _ ai.ToolCall) (sdkagent.Result, error) {
	c.ran.Add(1)
	if ctx.Err() != nil {
		c.ranOnDead.Add(1)
	}
	if c.onRun != nil {
		c.onRun()
	}
	return sdkagent.TextResult("ok"), nil
}

// batchLLM answers with three side-effecting calls, so execTools runs the
// batch sequentially.
type batchLLM struct{}

func (batchLLM) Name() string { return "batch" }

func (batchLLM) Stream(context.Context, *ai.Request) iter.Seq2[ai.Delta, error] {
	return yieldAll(deltas(InferResponse{
		StopReason: StopToolUse,
		ToolCalls: []ToolCall{
			{ID: "c1", Name: "first", Input: "{}"},
			{ID: "c2", Name: "second", Input: "{}"},
			{ID: "c3", Name: "third", Input: "{}"},
		},
	}))
}

// A cancel landing mid-batch must stop the rest of the batch, not just the
// call it interrupted.
func TestCancelDuringToolBatchStopsTheRemainingCalls(t *testing.T) {
	var ran, ranOnDead atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := NewAgent(Config{
		ID: "test", Client: testClient(batchLLM{}), System: NewSystem(),
		Tools: NewTools(
			cancelOnRunTool{name: "first", ran: &ran, ranOnDead: &ranOnDead, onRun: cancel},
			cancelOnRunTool{name: "second", ran: &ran, ranOnDead: &ranOnDead},
			cancelOnRunTool{name: "third", ran: &ran, ranOnDead: &ranOnDead},
		),
	}).(*agent)
	go func() {
		for range ag.Outbox() {
		}
	}()
	ag.Append(context.Background(), Message{Role: ai.RoleUser, Content: ai.TextContent("go")})

	result, err := ag.ThinkAct(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ThinkAct err = %v, want context.Canceled", err)
	}
	if result.StopReason != StopCancelled {
		t.Fatalf("StopReason = %q, want %q", result.StopReason, StopCancelled)
	}
	if n := ranOnDead.Load(); n != 0 {
		t.Errorf("%d of %d tool calls executed after the turn was cancelled", n, ran.Load())
	}
}

// --- what the application keeps saying about every call ---

// requestRecordingDriver keeps the request it was handed, which is the only
// place the settings above it can be observed.
type requestRecordingDriver struct{ req *ai.Request }

func (requestRecordingDriver) Name() string { return "recording" }

func (d *requestRecordingDriver) Stream(_ context.Context, req *ai.Request) iter.Seq2[ai.Delta, error] {
	d.req = req
	return yieldAll(deltas(InferResponse{Content: "ok", StopReason: StopEndTurn}))
}

// The reasoning rung and the output cap are the application's, not the
// client's: a person changes the rung mid-session and it has to be on the next
// call. Without this the /model picker, its shortcut and its stored value were
// all decoration — nothing put them on the wire.
func TestStreamInferSendsTheApplicationsCallSettings(t *testing.T) {
	driver := &requestRecordingDriver{}
	effort := ai.EffortLow
	ag := NewAgent(Config{
		ID:          "test",
		Client:      testClient(driver),
		CallOptions: func() []ai.Option { return []ai.Option{ai.WithMaxTokens(1234), ai.WithEffort(effort)} },
		System:      NewSystem(),
		Tools:       NewTools(),
	}).(*agent)
	go func() {
		for range ag.Outbox() {
		}
	}()
	ag.Append(context.Background(), Message{Role: ai.RoleUser, Content: ai.TextContent("go")})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if _, err := ag.ThinkAct(ctx); err != nil {
		t.Fatalf("ThinkAct: %v", err)
	}
	if driver.req.MaxTokens != 1234 {
		t.Errorf("MaxTokens = %d, want 1234", driver.req.MaxTokens)
	}
	if driver.req.Effort != ai.EffortLow {
		t.Errorf("Effort = %q, want low", driver.req.Effort)
	}

	// Asked for again on the next call, so a mid-session change lands.
	effort = ai.EffortHigh
	ag.Append(context.Background(), Message{Role: ai.RoleUser, Content: ai.TextContent("again")})
	if _, err := ag.ThinkAct(ctx); err != nil {
		t.Fatalf("ThinkAct: %v", err)
	}
	if driver.req.Effort != ai.EffortHigh {
		t.Errorf("Effort after the change = %q, want high", driver.req.Effort)
	}
}

// Which client answers is a per-turn question — Copilot's headers depend on
// what the turn sends — so the loop asks with the conversation in hand.
func TestStreamInferAsksForAClientPerTurn(t *testing.T) {
	driver := &requestRecordingDriver{}
	client := ai.NewClientWithDriver(driver, ai.Model{ID: "stub", API: "stub"})
	var asked [][]Message
	ag := NewAgent(Config{
		ID: "test",
		Client: func(msgs []Message) (*ai.Client, error) {
			asked = append(asked, msgs)
			return client, nil
		},
		System: NewSystem(),
		Tools:  NewTools(),
	}).(*agent)
	go func() {
		for range ag.Outbox() {
		}
	}()
	ag.Append(context.Background(), Message{Role: ai.RoleUser, Content: ai.TextContent("go")})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if _, err := ag.ThinkAct(ctx); err != nil {
		t.Fatalf("ThinkAct: %v", err)
	}
	if len(asked) != 1 || len(asked[0]) != 1 || asked[0][0].Text() != "go" {
		t.Fatalf("client asked with %v, want this turn's conversation", asked)
	}
}

// A turn that cannot reach the endpoint at all is the turn's failure, not the
// build's: it is reported like any other inference failure.
func TestStreamInferReportsAnUnreachableModel(t *testing.T) {
	ag := NewAgent(Config{
		ID:     "test",
		Client: func([]Message) (*ai.Client, error) { return nil, errors.New("no provider") },
		System: NewSystem(),
		Tools:  NewTools(),
	}).(*agent)
	go func() {
		for range ag.Outbox() {
		}
	}()
	ag.Append(context.Background(), Message{Role: ai.RoleUser, Content: ai.TextContent("go")})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := ag.ThinkAct(ctx)
	if err == nil {
		t.Fatal("ThinkAct returned nil error for an unconfigured model")
	}
	if result == nil || result.StopReason != StopError {
		t.Fatalf("result = %+v, want a turn reported as StopError", result)
	}
}

// A refusal and a stop sequence end a turn. Only a failed inference is
// StopError, because Run reads that one to mean the agent died and skips the
// TurnEvent — so anything else landing there would leave the interface waiting
// on a boundary that never comes.
func TestOnlyAFailedInferenceIsAnError(t *testing.T) {
	for _, tc := range []struct {
		sdk  sdkagent.StopReason
		want StopReason
	}{
		{sdkagent.StopEndTurn, StopEndTurn},
		{sdkagent.StopRefusal, StopEndTurn},
		{sdkagent.StopSequence, StopEndTurn},
		{sdkagent.StopMaxTokens, StopMaxOutputRecoveryExhausted},
		{sdkagent.StopMaxSteps, StopMaxSteps},
		{sdkagent.StopCanceled, StopCancelled},
		{sdkagent.StopTerminated, StopHook},
		{sdkagent.StopError, StopError},
	} {
		if got := stopReasonOf(tc.sdk); got != tc.want {
			t.Errorf("stopReasonOf(%q) = %q, want %q", tc.sdk, got, tc.want)
		}
	}
}

// A summarizer that produces nothing still closes the span it opened. This is
// the case that matters: OnCompact never fires, so if the close came from
// there, a consumer that drew a progress line on the start would be left
// holding it — and on the OnInferError path there is no next inference to fall
// through to, because a prompt that is still too long ends the turn.
func TestAFailedShorteningStillClosesTheSpan(t *testing.T) {
	var captured []Event
	ag := NewAgent(Config{
		ID:         "test",
		Client:     testClient(&talkingLLM{text: "done"}),
		System:     NewSystem(),
		Tools:      NewTools(),
		MaxSteps:   1,
		InputLimit: func() int { return 1 },
		CompactFunc: func(_ context.Context, _ []Message) (string, error) {
			return "", errors.New("the summarizer is down")
		},
		OnEvent: func(e Event) { captured = append(captured, e) },
	})
	a := ag.(*agent)

	go func() {
		for range ag.Outbox() {
		}
	}()

	a.SetMessages([]Message{
		UserMessage("hi", nil),
		AssistantMessage("hello", "", nil),
		UserMessage("tell me more", nil),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := a.ThinkAct(ctx); err != nil {
		t.Fatalf("ThinkAct: %v", err)
	}

	start, end := -1, -1
	for i, e := range captured {
		switch e.Type {
		case OnCompactStart:
			if start < 0 {
				start = i
			}
		case OnCompactEnd:
			if _, ok := e.CompactEnd(); !ok {
				t.Fatalf("OnCompactEnd carried %T, want CompactEnd", e.Data)
			}
			end = i
		case OnCompact:
			t.Error("a summarizer that produced nothing announced a compaction")
		}
	}
	if start < 0 {
		t.Fatal("the hook announced nothing, so there is no span to test")
	}
	if end < start {
		t.Error("the span opened and was never closed")
	}
}
