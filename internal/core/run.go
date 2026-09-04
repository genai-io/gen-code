package core

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"

	glog "github.com/genai-io/san/internal/log"
)

// agent drives sdk-go's loop and keeps San's shape around it.
//
// What stays here is the mailbox: Run blocks on an inbox, drains what
// accumulated, and hands the batch to one exchange — a shape the SDK
// deliberately does not have, because how messages are batched into exchanges
// is the application's to decide. What is no longer here is everything inside
// one exchange: the inference and its retries, the tool batch and its
// parallelism, compaction. That is sdkagent.Agent, and this file is the
// translation between its events and San's.
type agent struct {
	id          string
	system      System
	tools       Tools
	compactFunc func(ctx context.Context, msgs []Message) (string, error)
	client      func(msgs []Message) (*ai.Client, error)
	callOptions func() []ai.Option
	inputLimit  func() int
	inbox       chan Inbound
	outbox      chan Event
	onEvent     func(Event)

	// inner holds the conversation and runs one exchange at a time.
	inner *sdkagent.Agent

	// pending is what the inbox took since the last exchange. The SDK's Run
	// takes a turn's input as an argument rather than reading a queue, so an
	// ingested message waits here for the exchange it opens.
	mu      sync.Mutex
	pending []Message

	closed atomic.Bool // guards outbox writes after close

	// turn captures the in-flight ThinkAct so InterruptCurrentTurn can
	// cancel it and the caller can wait for it to actually return. Stored
	// as a pointer so swap-with-nil acts as an atomic claim — concurrent
	// InterruptCurrentTurn calls become no-ops.
	turn atomic.Pointer[turnHandle]

	// interruptPending latches an interrupt that arrived while Run was
	// between iterations (turn pointer was momentarily nil). The next
	// inner-loop iteration checks the latch and bails back to
	// waitForInput rather than starting a new ThinkAct that the user
	// already asked not to run.
	interruptPending atomic.Bool
}

// turnHandle binds the per-turn cancel function to a done channel so an
// outside caller (Task.InterruptTurn) can both cancel the turn and wait
// for ThinkAct to actually unwind before resuming work that depends on
// the agent goroutine being quiescent (e.g. clearing pendingPermRequest
// without racing an in-flight PermissionFunc write).
type turnHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (a *agent) ID() string                 { return a.id }
func (a *agent) System() System             { return a.system }
func (a *agent) Tools() Tools               { return a.tools }
func (a *agent) Inbox() chan<- Inbound      { return a.inbox }
func (a *agent) Outbox() <-chan Event       { return a.outbox }
func (a *agent) Messages() []Message        { return a.inner.Messages() }
func (a *agent) SetMessages(msgs []Message) { a.inner.SetMessages(msgs) }

// Append puts a message into the conversation the next exchange opens with.
// This is the unified entry point for both paths:
//
//	Run path:    inbox → ingest → here
//	Direct path: caller → Append → ThinkAct
func (a *agent) Append(ctx context.Context, msg Message) {
	a.ingest(ctx, Inbound{Msg: msg})
}

func (a *agent) Run(ctx context.Context) error {
	a.emit(ctx, StartEvent(a.id))

	var runErr error
	defer func() {
		// StopEvent must be delivered even on context cancellation,
		// so use emitFinal which bypasses ctx.Done().
		a.emitFinal(StopEvent(a.id, runErr))
		a.closed.Store(true)

		if a.outbox != nil {
			close(a.outbox)
		}
	}()

	for {
		glog.QueueLog("agent.Run: waitForInput blocking...")
		if err := a.waitForInput(ctx); err != nil {
			if err == errStopped {
				return nil
			}
			runErr = err
			return err
		}
		glog.QueueLog("agent.Run: waitForInput received message")

		// A fresh message supersedes a latched interrupt: an Esc that landed
		// while the agent sat idle here had no turn to stop, and keeping the
		// latch made runOneTurn swallow this message.
		a.interruptPending.Store(false)

		for {
			glog.QueueLog("agent.Run: starting ThinkAct")
			result, err, interrupted := a.runOneTurn(ctx)
			if interrupted {
				glog.QueueLog("agent.Run: interrupt latched, resuming wait")
				break
			}

			// A failed turn (StopError) is an agent stop, not a turn boundary:
			// emitting TurnEvent would fire OnTurnEnd on top of OnAgentStop.
			// Cancellation still emits (OnTurnEnd guards StopCanceled).
			if result != nil && result.StopReason != StopError {
				glog.QueueLog("agent.Run: ThinkAct done, emitting TurnEvent")
				a.emit(ctx, TurnEvent(a.id, *result))
			}
			if err != nil {
				glog.QueueLog("agent.Run: ThinkAct error: %v", err)
				if err == errStopped {
					return nil
				}
				// Turn-only interrupt: parent ctx still alive, the turn's ctx
				// was cancelled by InterruptCurrentTurn. Just bail back to
				// waitForInput — ai.Repair strips any orphaned tool_use blocks
				// left in the conversation, and the UI attaches a "previous
				// turn was interrupted" reminder onto the next user message so
				// the model knows the prior response did not complete.
				if ctx.Err() == nil && errors.Is(err, context.Canceled) {
					glog.QueueLog("agent.Run: turn interrupted by user, resuming wait")
					// Consume the latch that triggered this cancel so the
					// next user message can start a fresh turn. Narrow
					// race: a brand-new Interrupt that arrives between
					// close(h.done) and this Store can be clobbered; the
					// 2nd Esc is treated as a duplicate of the first.
					a.interruptPending.Store(false)
					break
				}
				runErr = err
				return err
			}

			n, drainErr := a.drainInbox(ctx)
			if drainErr != nil {
				if drainErr == errStopped {
					return nil
				}
				runErr = drainErr
				return drainErr
			}
			glog.QueueLog("agent.Run: post-ThinkAct drain n=%d", n)
			if n == 0 {
				break
			}
		}
	}
}

// runOneTurn runs a single ThinkAct under a per-turn cancellable ctx and
// returns whether an interrupt was latched (instead of running the turn).
//
// The latch is checked AFTER publishing turn=h so that any concurrent
// InterruptCurrentTurn is honored exactly once:
//   - If InterruptCurrentTurn ran BEFORE our Store, its Swap saw turn=nil
//     and set interruptPending=true; our post-Store Swap reads it.
//   - If InterruptCurrentTurn ran AFTER our Store, its Swap saw turn=h
//     and cancelled turnCtx; ThinkAct exits via context.Canceled.
//
// Cleanup (detach + close(done)) is deferred so a panic in ThinkAct still
// releases turnCtx and signals waiters in Task.InterruptTurn.
func (a *agent) runOneTurn(ctx context.Context) (*Result, error, bool) {
	turnCtx, turnCancel := context.WithCancel(ctx)
	h := &turnHandle{cancel: turnCancel, done: make(chan struct{})}
	a.turn.Store(h)
	defer func() {
		if a.turn.CompareAndSwap(h, nil) {
			turnCancel()
		}
		close(h.done)
	}()

	if a.interruptPending.Swap(false) {
		return nil, nil, true
	}

	result, err := a.ThinkAct(turnCtx)
	return result, err, false
}

// InterruptCurrentTurn cancels the ctx of the currently-running ThinkAct
// without ending Run. Returns a channel that closes when the in-flight
// ThinkAct has fully unwound — callers that need to observe a quiescent
// agent (e.g. before mutating shared state that the agent goroutine
// might also touch) should wait on the channel.
//
// When called between turns (turn pointer is nil), latches the
// interrupt so the next inner-loop iteration bails before starting a
// fresh ThinkAct, and returns an already-closed channel.
func (a *agent) InterruptCurrentTurn() <-chan struct{} {
	a.interruptPending.Store(true)
	if h := a.turn.Swap(nil); h != nil {
		h.cancel()
		return h.done
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}

var errStopped = errors.New("stopped")

// TruncatedResumePrompt is injected when generation stops at the output limit
// and the caller wants the model to continue in the next turn.
const TruncatedResumePrompt = "Your response was truncated due to output token limits. Resume directly from where you left off. Do not repeat any content."

// waitForInput blocks until a real (turn-starting) message arrives, then drains
// remaining. Control-only signals such as SigCompact are processed but do not
// start a turn: if a wake-up delivered nothing but signals, we loop back to
// blocking rather than returning, so e.g. a manual /compact while idle compacts
// the chain without triggering a spurious inference on the lone summary.
func (a *agent) waitForInput(ctx context.Context) error {
	for {
		select {
		case in, ok := <-a.inbox:
			startsTurn, err := a.ingestBatch(ctx, in, ok)
			if err != nil {
				return err
			}
			if startsTurn {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ingestBatch processes the just-received message plus any others already
// queued (non-blocking), and reports whether any of them starts a turn. A
// closed inbox or SigStop yields errStopped.
func (a *agent) ingestBatch(ctx context.Context, in Inbound, ok bool) (startsTurn bool, err error) {
	for {
		if !ok || in.Signal == SigStop {
			return false, errStopped
		}
		if a.ingest(ctx, in) {
			startsTurn = true
		}
		select {
		case in, ok = <-a.inbox:
			// another message was already queued — loop to process it
		default:
			return startsTurn, nil
		}
	}
}

// ingest processes one inbox item and reports whether it starts a turn (i.e. a
// real message arrived). SigCompact applies an in-place compaction with the
// precomputed summary it carries; signals never start a turn.
func (a *agent) ingest(ctx context.Context, in Inbound) bool {
	if in.Signal == SigCompact {
		a.applyCompaction(ctx, in.Summary, len(a.Messages()), "manual")
		return false
	}
	if in.Signal != "" {
		return false
	}
	a.emit(ctx, MessageEvent(a.id, in.Msg))

	a.mu.Lock()
	a.pending = append(a.pending, in.Msg)
	a.mu.Unlock()
	return true
}

// takePending empties the queue ingest fills, for the exchange it opens.
func (a *agent) takePending() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()

	msgs := a.pending
	a.pending = nil
	return msgs
}

// emit sends an event to the outbox for external observation.
// No-op when outbox is nil (subagent direct path).
// Blocks if outbox is full (backpressure). Skips if outbox is closed or ctx is cancelled.
func (a *agent) emit(ctx context.Context, event Event) {
	if a.onEvent != nil {
		a.onEvent(event)
	}
	if a.outbox == nil || a.closed.Load() {
		return
	}
	select {
	case a.outbox <- event:
	case <-ctx.Done():
	}
}

// emitTelemetry delivers a fire-and-forget event: synchronously to onEvent,
// non-blocking to the outbox (dropped if full). Used for events whose
// consumers tolerate misses (system changes, hot-path tracing) and which can
// fire from goroutines without a useful ctx (e.g. system observer callbacks).
func (a *agent) emitTelemetry(event Event) {
	if a.onEvent != nil {
		a.onEvent(event)
	}
	if a.outbox == nil || a.closed.Load() {
		return
	}
	select {
	case a.outbox <- event:
	default:
	}
}

// emitFinal sends a critical event that must be delivered even on ctx cancellation.
// Used for StopEvent — consumers rely on it for cleanup/session saving.
// No-op when outbox is nil. Blocks up to 5 seconds; logs a warning if delivery fails.
func (a *agent) emitFinal(event Event) {
	if a.onEvent != nil {
		a.onEvent(event)
	}
	if a.outbox == nil || a.closed.Load() {
		return
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case a.outbox <- event:
	case <-timer.C:
		log.Printf("core/agent: failed to deliver %s event (outbox full for 5s)", event.Type)
	}
}

// drainInbox non-blocking reads ONE pending inbox message.
// Returns 1 if a turn-starting message was consumed, 0 otherwise (nothing
// pending, or a control-only signal such as SigCompact that was applied but
// does not warrant a new ThinkAct). Each turn-starting message gets its own
// ThinkAct cycle so the TUI can pair each user message with its response.
func (a *agent) drainInbox(ctx context.Context) (int, error) {
	select {
	case in, ok := <-a.inbox:
		if !ok || in.Signal == SigStop {
			return 0, errStopped
		}
		if a.ingest(ctx, in) {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, nil
	}
}
