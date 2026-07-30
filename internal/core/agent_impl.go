package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	glog "github.com/genai-io/san/internal/log"
)

// agent is the default Agent implementation.
type agent struct {
	id                string
	system            System
	tools             Tools
	compactFunc       func(ctx context.Context, msgs []Message) (string, error)
	llm               LLM
	cwd               string
	maxSteps          int
	maxOutputRecovery int
	maxTurnRetries    int
	firstChunkTimeout time.Duration
	idleTimeout       time.Duration
	inbox             chan Message
	outbox            chan Event
	onEvent           func(Event)

	// lastTotalInputTokens is InferResponse.TotalInputTokens from the previous
	// inference — the whole prompt the provider billed for, cached prefix
	// included, not the uncached delta InputTokens carries. The name says
	// "last" because the lag is deliberate: the figure is always one step
	// behind the prompt being assembled now.
	//
	// It lives on the agent rather than in ThinkAct so it survives a turn
	// boundary: a turn's first step is exactly where the context accumulated by
	// previous turns lands, and a single-step turn (a plain answer, no tool
	// calls) would otherwise never be checked at all. Deliberately outside the
	// mu block — written and read only on the agent's own goroutine (ThinkAct,
	// ingest).
	lastTotalInputTokens int

	mu       sync.RWMutex
	messages []Message // conversation history

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

type agentToolTask struct {
	call ToolCall
	tool Tool
}

type toolTaskOutput struct {
	content string
	err     error
}

func (a *agent) ID() string            { return a.id }
func (a *agent) System() System        { return a.system }
func (a *agent) Tools() Tools          { return a.tools }
func (a *agent) Inbox() chan<- Message { return a.inbox }
func (a *agent) Outbox() <-chan Event  { return a.outbox }
func (a *agent) Messages() []Message   { return a.snapshot() }

func (a *agent) SetMessages(msgs []Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = make([]Message, len(msgs))
	copy(a.messages, msgs)
}

// Append adds a message to the conversation and fires the OnMessage hook.
func (a *agent) Append(ctx context.Context, msg Message) {
	a.ingest(ctx, msg)
}

// Run is the agent's main loop: wait for input → think+act → repeat.
// Outbox is closed when Run returns. Inbox is NOT closed (caller owns it).
//
// Each ThinkAct call runs under a per-turn ctx derived from Run's ctx so
// InterruptCurrentTurn can cancel the in-flight turn without ending the
// loop. Parent-ctx cancellation still ends Run.
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
			// Cancellation still emits (OnTurnEnd guards StopCancelled).
			if result != nil && result.StopReason != StopError {
				glog.QueueLog("agent.Run: ThinkAct done, emitting TurnEvent")
				a.emit(ctx, TurnEvent(a.id, *result))
			}
			if err != nil {
				glog.QueueLog("agent.Run: ThinkAct error: %v", err)
				if err == errStopped {
					return nil
				}
				// Turn-only interrupt: parent ctx still alive, the turn's
				// ctx was cancelled by InterruptCurrentTurn. Just bail
				// back to waitForInput — provider-side convert layers
				// already strip any orphaned tool_use blocks left in
				// a.messages, and the UI attaches a "previous turn was
				// interrupted" reminder onto the next user message so
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
		// Block until a message arrives (idle state).
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-a.inbox:
			startsTurn, err := a.ingestBatch(ctx, msg, ok)
			if err != nil {
				return err
			}
			if startsTurn {
				return nil
			}
			// Only control signals arrived — keep waiting.
		}
	}
}

// ingestBatch processes the just-received message plus any others already
// queued (non-blocking), and reports whether any of them starts a turn. A
// closed inbox or SigStop yields errStopped.
func (a *agent) ingestBatch(ctx context.Context, msg Message, ok bool) (startsTurn bool, err error) {
	for {
		if !ok || msg.Signal == SigStop {
			return false, errStopped
		}
		if a.ingest(ctx, msg) {
			startsTurn = true
		}
		select {
		case msg, ok = <-a.inbox:
			// another message was already queued — loop to process it
		default:
			return startsTurn, nil
		}
	}
}

// ingest processes one inbox message and reports whether it starts a turn
// (i.e. a real message was appended to the conversation). SigCompact applies
// an in-place compaction with the precomputed summary it carries; signals
// never start a turn.
func (a *agent) ingest(ctx context.Context, msg Message) bool {
	if msg.Signal == SigCompact {
		a.applyCompaction(ctx, msg.Content, len(a.snapshot()), "manual")
		return false
	}
	a.emit(ctx, MessageEvent(a.id, msg))
	if msg.Signal == "" {
		a.append(msg)
		return true
	}
	return false
}

// ThinkAct runs one full inference-action cycle until end_turn.
// Returns the result directly — the caller decides whether to emit TurnEvent.
//
// A Result accompanies every outcome, including the errors: a turn that ends
// on cancellation or an inference failure still carries the messages, steps
// and token usage it accumulated, which is what lets the caller persist the
// run. The sole exception is a deliberate shutdown (errStopped), where no turn
// outcome exists to report.
func (a *agent) ThinkAct(ctx context.Context) (*Result, error) {
	var steps, toolUses, tokensIn, tokensOut int
	var maxOutputRecoveryCount int
	var turnRetries int
	// lastContent tracks the newest assistant text this turn, so every exit —
	// including cancel and the step cap — reports what the model actually said
	// rather than an empty string or a status placeholder.
	var lastContent string

	makeResult := func(stop StopReason) *Result {
		return &Result{
			Content: lastContent, Messages: a.snapshot(),
			Steps: steps, ToolUses: toolUses, InputTokens: tokensIn, OutputTokens: tokensOut,
			StopReason: stop,
		}
	}

	for {
		if ctx.Err() != nil {
			return makeResult(StopCancelled), ctx.Err()
		}

		// Max steps guard. The cap itself is reported through StopReason —
		// interpretStopReason turns it into "reached maximum steps (N)" — so
		// Content stays the model's own last output rather than a status string.
		if a.maxSteps > 0 && steps >= a.maxSteps {
			return makeResult(StopMaxSteps), nil
		}

		// Between steps: drain any new inbox messages (non-blocking).
		// errStopped is drainInbox's only error, and it means a deliberate
		// shutdown (closed inbox or SigStop) rather than a failed turn — the
		// one exit with no outcome to report, hence no Result.
		if steps > 0 {
			if _, err := a.drainInbox(ctx); err != nil {
				return nil, err
			}
		}

		// Proactive compaction. Checked against what the provider measured on
		// the previous step, so it lags by one step's worth of appended output —
		// absorbed by the threshold's headroom, and a step large enough to
		// overshoot the limit outright still lands on the reactive
		// prompt-too-long path below.
		if a.compactFunc != nil {
			// InputLimit resolves through the provider on first use, so it stays
			// behind the compactFunc guard — without a compactFunc the result
			// could not be acted on anyway.
			if limit := a.llm.InputLimit(); limit > 0 &&
				NeedsCompaction(a.promptTokensOrEstimate(), limit) && a.compact(ctx) {
				continue
			}
		}

		resp, err := a.streamInfer(ctx)
		if err != nil {
			// Reactive compaction: if prompt too long, compact and retry
			if a.compactFunc != nil && isPromptTooLong(err) && a.compact(ctx) {
				continue
			}
			// On turn cancellation, return a Result so observers see a
			// turn boundary with StopCancelled. The error is still
			// propagated so Run's loop can branch on it.
			if errors.Is(err, context.Canceled) {
				return makeResult(StopCancelled), err
			}
			// Transient stream failure (network blip, 5xx/529 overload,
			// 429, idle stall, truncation): discard the partial assistant
			// output and retry this inference step after a backoff. Bounded
			// by maxTurnRetries so a sustained outage still surfaces.
			var re RetryableError
			if errors.As(err, &re) {
				if turnRetries < a.maxTurnRetries {
					turnRetries++
					a.emit(ctx, StreamResetEvent(a.id))
					if werr := BackoffSleep(ctx, turnRetries, re.RetryAfter()); werr != nil {
						return makeResult(StopCancelled), werr
					}
					continue
				}
			}
			// Retry budget exhausted, or the failure was not retryable. Report
			// the turn rather than dropping it: the steps already taken are
			// billed, and a caller with no Result cannot persist what the run
			// produced — a subagent loses its entire transcript that way.
			failed := makeResult(StopError)
			failed.StopDetail = err.Error()
			return failed, err
		}
		turnRetries = 0 // reset budget once a step completes

		steps++
		// Record the FULL prompt the model processed, not resp.InputTokens: with
		// prompt caching on, a provider reports the cached prefix under
		// CacheRead/CacheCreation and InputTokens holds only the uncached delta —
		// a few hundred tokens against a 200k window. Testing that against the
		// limit read ~1% while the context was nearly full, so auto-compaction
		// never fired (issue #338). TotalInputTokens is also what the status bar
		// displays, so the two now agree.
		a.lastTotalInputTokens = resp.TotalInputTokens()
		tokensIn += resp.InputTokens
		tokensOut += resp.OutputTokens

		a.emit(ctx, PostInferEvent(a.id, resp))
		a.append(Message{
			Role:    RoleAssistant,
			Content: resp.Content, Thinking: resp.Thinking,
			ThinkingSignature: resp.ThinkingSignature,
			Reasoning:         resp.Reasoning,
			ToolCalls:         resp.ToolCalls,
		})
		// Unconditional: a tool-only step carries no text, and reporting that
		// honestly beats resurrecting an earlier step's narration as the outcome.
		lastContent = resp.Content

		// Max tokens recovery — output truncated, ask LLM to continue
		if resp.StopReason == StopMaxTokens && len(resp.ToolCalls) == 0 {
			maxRecovery := a.maxOutputRecovery
			if maxRecovery <= 0 {
				maxRecovery = 3
			}
			if maxOutputRecoveryCount >= maxRecovery {
				return makeResult(StopMaxOutputRecoveryExhausted), nil
			}
			maxOutputRecoveryCount++
			a.append(Message{Role: RoleUser, Content: TruncatedResumePrompt})
			continue
		}

		// No tool calls → end turn
		if len(resp.ToolCalls) == 0 {
			return makeResult(StopEndTurn), nil
		}

		// Execute tool calls
		toolUses += a.execTools(ctx, resp.ToolCalls)
	}
}

// promptTokensOrEstimate returns the prompt size to test against the input
// limit. The name is a disclosure, not a hedge: the caller cannot tell from
// the return value whether it got the provider's measurement or San's guess,
// and the two are not equally trustworthy.
//
// The measurement is used whenever it exists. It is missing only before this
// agent has inferred once, which is not always a small conversation:
// ensureAgentSession seeds a rebuilt agent with the full existing history
// (session resume, model switch, toolset drift), so the very first prompt can
// already be near the limit.
//
// The estimate is the repo's usual 4-bytes-per-token approximation. That is
// right for English prose and low for CJK (nearer 2–3), and it ignores the tool
// schemas the real prompt also carries — so it errs low by design. Erring low
// just defers to the reactive prompt-too-long retry, whereas erring high would
// compact a conversation that still had room.
func (a *agent) promptTokensOrEstimate() int {
	if a.lastTotalInputTokens > 0 {
		return a.lastTotalInputTokens
	}
	return (len(a.system.Prompt()) + a.conversationTextLen()) / 4
}

// conversationTextLen reads the live history without making a snapshot copy.
func (a *agent) conversationTextLen() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return conversationTextLen(a.messages)
}

// execTools runs tool calls in three phases:
//  1. Resolve — emit PreTool event, look up tool
//  2. Execute — parallel for read-only batches, sequential when side effects are possible
//  3. Record results — sequential, in original call order
//
// Permission checking is handled by the tool decorator (tool.WithPermission),
// not by the agent. See docs/concepts/permission-model.md.
func (a *agent) execTools(ctx context.Context, calls []ToolCall) int {
	var tasks []agentToolTask
	for _, tc := range calls {
		if ctx.Err() != nil {
			break
		}
		a.emit(ctx, PreToolEvent(tc))
		t := a.tools.Get(tc.Name)
		if t == nil {
			a.appendResult(tc, fmt.Sprintf("unknown tool: %s", tc.Name), true)
			continue
		}
		tasks = append(tasks, agentToolTask{tc, t})
	}
	if len(tasks) == 0 {
		return 0
	}

	// Phase 2: Execute (parallel only for read-only batches). The sequential
	// path re-checks ctx between calls — nothing obliges a tool to consult ctx
	// before doing its work, so without it an Esc during the first of three
	// Edits still applied the other two.
	results := make([]toolTaskOutput, len(tasks))
	if len(tasks) == 1 || !canExecuteToolBatchInParallel(tasks) {
		for i, t := range tasks {
			if ctx.Err() != nil {
				results[i] = toolTaskOutput{"", ctx.Err()}
				continue
			}
			results[i] = executeToolTask(ctx, t)
		}
	} else {
		var wg sync.WaitGroup
		for i, t := range tasks {
			wg.Add(1)
			go func(i int, t agentToolTask) {
				defer wg.Done()
				results[i] = executeToolTask(ctx, t)
			}(i, t)
		}
		wg.Wait()
	}

	// Phase 3: Record results in order + PostTool hooks. Bail on ctx cancel
	// so an InterruptCurrentTurn that lands mid-batch does not keep
	// appending tool results into a.messages after the UI's cancel handler
	// has already written its own cancelled-tool-result entries.
	var toolUses int
	for i, t := range tasks {
		if ctx.Err() != nil {
			break
		}
		r := results[i]
		if r.err != nil {
			a.appendResult(t.call, r.err.Error(), true)
			a.emit(ctx, PostToolEvent(ToolResult{
				ToolCallID: t.call.ID, ToolName: t.call.Name, Content: r.err.Error(), IsError: true,
			}))
			continue
		}
		toolUses++
		a.appendResult(t.call, r.content, false)
		a.emit(ctx, PostToolEvent(ToolResult{
			ToolCallID: t.call.ID, ToolName: t.call.Name, Content: r.content,
		}))
	}
	return toolUses
}

func executeToolTask(ctx context.Context, t agentToolTask) (result toolTaskOutput) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("core/agent: tool %s panicked: %v\n%s", t.call.Name, r, debug.Stack())
			result = toolTaskOutput{"", fmt.Errorf("tool %s panicked: %v", t.call.Name, r)}
		}
	}()
	params, _ := ParseToolInput(t.call.Input)
	execCtx := WithToolCallID(ctx, t.call.ID)
	content, err := t.tool.Execute(execCtx, params)
	return toolTaskOutput{content, err}
}

// canExecuteToolBatchInParallel permits two batch shapes: all read-only
// calls, or all agent-spawning calls. Agent calls are independent workers
// with their own permission gates — running them concurrently is the point
// of launching several at once; mixed batches stay sequential because
// side-effect ordering against other tools is unknown.
func canExecuteToolBatchInParallel(tasks []agentToolTask) bool {
	readOnly, agents := true, true
	for _, t := range tasks {
		if !isReadOnlyToolCall(t.call.Name) {
			readOnly = false
		}
		if !isAgentSpawnToolCall(t.call.Name) {
			agents = false
		}
	}
	return readOnly || agents
}

func isReadOnlyToolCall(name string) bool {
	switch name {
	case "Read", "WebFetch", "WebSearch", "LSP":
		return true
	default:
		return false
	}
}

func isAgentSpawnToolCall(name string) bool {
	return name == "Agent" || name == "SendMessage"
}

// CompactMaxTokens is the max output tokens for compaction LLM calls.
const CompactMaxTokens = 4096

// CompactSummaryPrefix marks a user message as the post-compaction summary.
// The UI uses it to render that message as a system notice rather than a normal
// user turn, while the model and session store keep the full text.
const CompactSummaryPrefix = "Previous context:\n"

// FormatCompactSummary formats a compaction summary for injection as a user message.
func FormatCompactSummary(summary string) string {
	return CompactSummaryPrefix + summary
}

// IsCompactSummary reports whether content is a post-compaction summary message
// (produced by FormatCompactSummary).
func IsCompactSummary(content string) bool {
	return strings.HasPrefix(content, CompactSummaryPrefix)
}

// compact calls CompactFunc and replaces messages with the summary.
// Returns true if compaction succeeded.
func (a *agent) compact(ctx context.Context) bool {
	msgs := a.snapshot()
	if len(msgs) < 3 {
		return false
	}
	// Announce the start so the UI can show a progress line during the blocking
	// summarization call. On failure the loop falls through to streamInfer,
	// whose PreInfer clears the indicator.
	a.emit(ctx, CompactStartEvent(a.id, CompactStart{Count: len(msgs)}))
	summary, err := a.compactFunc(ctx, msgs)
	if err != nil || summary == "" {
		return false
	}
	a.applyCompaction(ctx, summary, len(msgs), "auto")
	return true
}

// applyCompaction replaces the conversation chain with a single summary
// message in place. The summary becomes the sole message of the new chain;
// it gets a stable ID and is recorded as a normal message.appended so
// transcript replay can resolve the ID the next inference references, then a
// CompactEvent is emitted carrying that ID as the boundary so replay truncates
// the summarized-away history. Both auto-compaction (compact) and manual
// /compact (SigCompact) funnel through here so they record identically and
// neither rebuilds the agent.
func (a *agent) applyCompaction(ctx context.Context, summary string, originalCount int, trigger string) {
	summaryMsg := UserMessage(FormatCompactSummary(summary), nil)
	summaryMsg.ID = NewMessageID()
	a.SetMessages([]Message{summaryMsg})
	// The chain just collapsed to one message, so the last measurement is stale
	// and clearing it is what stops the check from immediately re-firing:
	// keeping the pre-compaction figure would still read "full" against the
	// now-tiny chain and compact again on the very next step, forever. Zero
	// suppresses the check until the next inference measures the new prompt.
	a.lastTotalInputTokens = 0
	a.emitAppend(summaryMsg)
	a.emit(ctx, CompactEvent(a.id, CompactInfo{
		Summary:          summary,
		OriginalCount:    originalCount,
		SummaryMessageID: summaryMsg.ID,
		Trigger:          trigger,
	}))
}

// isPromptTooLong reports whether the prompt overflowed the model's context
// window. The llm layer tags that case during classification (see
// llmerr.Wrap), so this stays free of provider error vocabulary.
func isPromptTooLong(err error) bool {
	var exceeded ContextExceededError
	return errors.As(err, &exceeded)
}

// --- context keys ---

type contextKey string

const toolCallIDKey contextKey = "tool_call_id"

// WithToolCallID returns a context carrying the given tool call ID.
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey, id)
}

// ToolCallIDFromContext extracts the tool call ID from the context.
func ToolCallIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(toolCallIDKey).(string); ok {
		return id
	}
	return ""
}

// --- internals ---

// streamInfer calls the LLM, streams chunks to outbox, returns the final response.
//
// Emits PreInferEvent (with input digests) before the LLM call so observers
// can record exactly what was sent without copying the bytes on every turn.
func (a *agent) streamInfer(ctx context.Context) (*InferResponse, error) {
	sys := a.system.Prompt()
	msgs := a.snapshot()
	tools := a.tools.Schemas()

	a.emit(ctx, PreInferEvent(a.id, InferenceContext{
		SystemDigest: sha256Hex([]byte(sys)),
		ToolsDigest:  toolsDigest(tools),
		MessageIDs:   messageIDs(msgs),
	}))

	// Per-inference child ctx so an idle stall can be torn down without
	// cancelling the whole turn. The provider and bridge goroutines are
	// ctx-aware, so cancelling inferCtx unwinds them cleanly.
	inferCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	chunks, err := a.llm.Infer(inferCtx, InferRequest{
		System:   sys,
		Messages: msgs,
		Tools:    tools,
	})
	if err != nil {
		return nil, fmt.Errorf("infer: %w", err)
	}

	// First chunk gets the generous TTFT budget (a reasoning model may emit
	// nothing while it thinks); every chunk thereafter rearms the timer with
	// the tighter inter-chunk idle budget.
	idle := time.NewTimer(a.firstChunkTimeout)
	defer idle.Stop()

	var resp *InferResponse
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				// ctx-cancellation racing the bridge's defer close(ch)
				// produces both ok==false and ctx.Done() ready at the
				// same time — select picks randomly. Check the caller
				// ctx first so a user interrupt is always reported as
				// context.Canceled, never as a (retryable) truncation.
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if resp == nil {
					// Closed without a Done chunk: the provider always
					// emits Done on success, so this is an abnormal
					// truncation — retryable.
					return nil, errStreamTruncated
				}
				return resp, nil
			}
			idle.Reset(a.idleTimeout)
			if chunk.Err != nil {
				return nil, fmt.Errorf("infer: %w", chunk.Err)
			}
			if chunk.Text != "" || chunk.Thinking != "" || chunk.Done {
				a.emit(ctx, ChunkEvent(a.id, chunk))
			}
			if chunk.Done {
				resp = chunk.Response
			}
		case <-idle.C:
			// The stream went silent (connection alive but no bytes —
			// half-open socket, stalled provider). Tear it down and let
			// the turn loop retry. Caller ctx is still live, so this is
			// classified retryable, not a cancel.
			cancel()
			return nil, errStreamStalled
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
	case msg, ok := <-a.inbox:
		if !ok || msg.Signal == SigStop {
			return 0, errStopped
		}
		if a.ingest(ctx, msg) {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, nil
	}
}

// append adds msg to the conversation chain and emits OnAppend so observers
// (notably the session recorder) can persist it in causal order — every
// message.appended record must precede any inference.requested that consumes
// it.
//
// Stamps an ID if msg.ID is empty so the OnAppend payload always carries a
// stable identifier; downstream persistence dedupes on this ID.
func (a *agent) append(msg Message) {
	if msg.ID == "" {
		msg.ID = NewMessageID()
	}
	a.mu.Lock()
	a.messages = append(a.messages, msg)
	a.mu.Unlock()
	// emit outside the lock — onEvent handlers may do I/O (transcript writes).
	a.emitAppend(msg)
}

// emitAppend pushes an OnAppend event without a ctx (callers of append() may
// not have one) and without blocking the outbox. The recorder listens via
// onEvent which is invoked synchronously.
func (a *agent) emitAppend(msg Message) {
	if a.onEvent != nil {
		a.onEvent(AppendEvent(a.id, msg))
	}
	if a.outbox == nil || a.closed.Load() {
		return
	}
	select {
	case a.outbox <- AppendEvent(a.id, msg):
	default:
	}
}

func (a *agent) snapshot() []Message {
	a.mu.RLock()
	defer a.mu.RUnlock()
	cp := make([]Message, len(a.messages))
	copy(cp, a.messages)
	return cp
}

func (a *agent) appendResult(tc ToolCall, content string, isError bool) {
	// A tool result rides on a RoleUser message — its content lives on
	// ToolResult.Content, not on Message.Content. See the Role doc.
	a.append(Message{
		Role:       RoleUser,
		ToolResult: &ToolResult{ToolCallID: tc.ID, ToolName: tc.Name, Content: content, IsError: isError},
	})
}
