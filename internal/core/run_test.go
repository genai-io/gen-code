package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

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
