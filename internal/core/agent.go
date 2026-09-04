package core

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"time"
)

// Agent is the core abstraction — an autonomous entity that reasons and acts.
//
// Three capabilities, nothing more:
//  1. System  — WHO it is (composable, mutable identity)
//  2. Tools   — WHAT it can do (the single action primitive)
//  3. Inbox/Outbox — HOW it communicates (Go channels)
//
// Hooks are app-layer only (hook.Engine), not part of the agent core.
//
// Lifecycle control:
//   - Graceful stop: send Message{Signal: SigStop} to Inbox
//   - Immediate stop: cancel the context passed to Run
type Agent interface {
	ID() string
	System() System
	Tools() Tools

	// Inbox is the write channel — external world sends messages to the agent.
	// Messages are integrated into the conversation at turn boundaries.
	//
	// Ownership: caller owns the channel and must close it when done sending.
	// Sending to Inbox after Run() returns may block indefinitely.
	Inbox() chan<- Inbound

	// Outbox is the read channel — agent emits events to the external world.
	// Events include streaming chunks, tool execution status, and turn results.
	//
	// Ownership: agent owns the channel and closes it when Run() returns.
	// Outbox is single-consumer; for multiple consumers, build a fan-out on top.
	Outbox() <-chan Event

	// Messages returns a snapshot of the conversation history.
	// The returned slice is a shallow copy — do not mutate Message fields
	// that contain slices or pointers (Images, ToolCalls, ToolResult).
	Messages() []Message

	// SetMessages replaces the conversation history.
	// Used by compaction (shrink context) and session restore (load saved state).
	// The provided slice is shallow-copied; same mutation caveats as Messages().
	SetMessages(msgs []Message)

	// Append adds a message to the conversation and fires the OnMessage hook.
	// This is the unified entry point for both paths:
	//   Run path:   inbox → ingest (Append internally)
	//   Direct path: caller → Append → ThinkAct
	Append(ctx context.Context, msg Message)

	// ThinkAct runs one full inference-action cycle: PreInfer → LLM stream →
	// tool execution → repeat until end_turn. Returns the result directly.
	//
	// This is the agent's atomic operation. Two callers drive it differently:
	//   Run():   loop { waitForInput → ThinkAct }, emits TurnEvent to Outbox
	//   Direct:  Append(msg) → ThinkAct(ctx), returns *Result synchronously
	ThinkAct(ctx context.Context) (*Result, error)

	// Run starts the agent's main loop. Blocks until context cancellation or SigStop.
	//
	// The run loop has three phases per cycle:
	//
	//   Phase 1 — WAIT (blocking):
	//     Block on Inbox until a message arrives. This is the idle state.
	//     On SigStop or ctx.Done(): fire OnStop hooks and return.
	//
	//   Phase 2 — DRAIN (non-blocking):
	//     Drain any additional messages that accumulated in Inbox.
	//     All drained messages are appended to the conversation.
	//
	//   Phase 3 — THINK + ACT (inference loop):
	//     Loop: LLM inference → tool execution → LLM inference → ...
	//     Between each step, non-blocking drain of Inbox for new messages.
	//     Emit streaming chunks and tool results to Outbox.
	//     Loop until LLM returns end_turn.
	//     Then go back to Phase 1 (wait for next message).
	//
	// Signals (SigStop) are checked at every phase boundary.
	Run(ctx context.Context) error

	// InterruptCurrentTurn cancels the in-flight ThinkAct without ending
	// Run. After interruption the run loop returns to waitForInput and
	// resumes normally on the next inbox message. Use this for
	// user-initiated mid-stream cancellation; use SigStop / ctx cancel
	// for full shutdown.
	//
	// Returns a channel that closes when the in-flight ThinkAct has
	// actually returned, so callers that need to mutate shared state
	// right after the interrupt can serialize against the agent
	// goroutine. When called between turns the channel is already
	// closed and the interrupt is latched so the next iteration of the
	// inner loop bails before starting an unwanted ThinkAct.
	InterruptCurrentTurn() <-chan struct{}
}

// Config holds construction parameters for an agent.
//
// Required fields: LLM, System, Tools. NewAgent panics if any is nil.
// Optional fields: ID, CWD, MaxSteps, InboxBuf, OutboxBuf, CompactFunc.
//
// Permission is a tool-layer concern — use tool.WithPermission to wrap Tools
// before passing them to NewAgent. See docs/concepts/permission-model.md.
type Config struct {
	ID string
	// Client hands over the model to talk to for one turn. Required. A function
	// of the conversation because an endpoint's headers can depend on what the
	// turn sends (see llm.TurnHeaders), which only the application knows.
	Client func(msgs []Message) (*ai.Client, error)
	// CallOptions are the per-call settings — the output cap, the reasoning
	// rung — asked for fresh on every inference, so a person changing the rung
	// mid-session takes effect on the next call. Nil leaves the model's own
	// defaults.
	CallOptions func() []ai.Option
	// InputLimit is the prompt budget auto-compaction measures against, as a
	// function because the application may let a person change it. Nil, or a
	// zero return, turns auto-compaction off. It is not read off the client:
	// the figure San uses is the model's window unless a setting overrides it,
	// and that setting is the application's.
	InputLimit              func() int
	System                  System                                                    // required: system prompt layers
	Tools                   Tools                                                     // required: available tools (wrap with tool.WithPermission for permission)
	CompactFunc             func(ctx context.Context, msgs []Message) (string, error) // optional: summarize messages for compaction
	CWD                     string
	MaxSteps                int           // max LLM inference steps per turn, 0 = unlimited
	MaxOutputRecovery       int           // max retries on truncated output, 0 = use default (3)
	MaxTurnRetries          int           // max retries per inference step on transient stream errors, 0 = use default (2)
	StreamFirstChunkTimeout time.Duration // abort if no first chunk arrives within this long, 0 = use default (5m)
	StreamIdleTimeout       time.Duration // abort a stream that goes silent between chunks for this long, 0 = use default (60s)
	InboxBuf                int           // inbox channel buffer size, default 16
	OutboxBuf               int           // outbox channel buffer size, default 64; -1 = no outbox (subagent path)
	// OnEvent observes lifecycle events synchronously, even when OutboxBuf is -1.
	OnEvent func(Event)
}

// NewAgent creates an agent from config.
//
// Panics if LLM, System, or Tools is nil — these are required capabilities.
// Inbox is owned by the caller (caller closes when done sending).
// Outbox is owned by the agent (closed when Run returns).
func NewAgent(cfg Config) Agent {
	if cfg.Client == nil {
		panic("core.NewAgent: Client is required")
	}
	if cfg.System == nil {
		panic("core.NewAgent: System is required")
	}
	if cfg.Tools == nil {
		panic("core.NewAgent: Tools is required")
	}
	if cfg.InboxBuf <= 0 {
		cfg.InboxBuf = 16
	}
	if cfg.OutboxBuf == 0 {
		cfg.OutboxBuf = 64
	}
	if cfg.MaxTurnRetries <= 0 {
		cfg.MaxTurnRetries = defaultMaxTurnRetries
	}
	if cfg.StreamFirstChunkTimeout <= 0 {
		cfg.StreamFirstChunkTimeout = defaultFirstChunkTimeout
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = defaultStreamIdleTimeout
	}

	var outbox chan Event
	if cfg.OutboxBuf > 0 {
		outbox = make(chan Event, cfg.OutboxBuf)
	}

	a := &agent{
		id:                cfg.ID,
		system:            cfg.System,
		tools:             cfg.Tools,
		compactFunc:       cfg.CompactFunc,
		client:            cfg.Client,
		callOptions:       cfg.CallOptions,
		inputLimit:        cfg.InputLimit,
		cwd:               cfg.CWD,
		maxSteps:          cfg.MaxSteps,
		maxOutputRecovery: cfg.MaxOutputRecovery,
		maxTurnRetries:    cfg.MaxTurnRetries,
		firstChunkTimeout: cfg.StreamFirstChunkTimeout,
		idleTimeout:       cfg.StreamIdleTimeout,
		inbox:             make(chan Inbound, cfg.InboxBuf),
		outbox:            outbox,
		onEvent:           cfg.OnEvent,
	}
	// Mirror system + tools mutations onto the event bus. Attach after
	// construction so each registry replays its initial members back to the
	// observer — the recorder sees a complete event chain from t0.
	cfg.System.SetObserver(func(c SystemChange) {
		a.emitTelemetry(SystemChangeEvent(a.id, c))
	})
	cfg.Tools.SetObserver(func(c ToolsChange) {
		a.emitTelemetry(ToolsChangeEvent(a.id, c))
	})
	return a
}

// Result represents the outcome of one completed turn (end_turn).
// Emitted to Outbox as Event{Type: OnTurn, Data: result}.
type Result struct {
	Content      string     // final text output of this turn
	Messages     []Message  // full conversation history
	Steps        int        // LLM inference steps in this turn
	ToolUses     int        // tool calls in this turn
	InputTokens  int        // input tokens consumed
	OutputTokens int        // output tokens produced
	StopReason   StopReason // why the loop stopped
	StopDetail   string     // human-readable detail (e.g. hook block reason)
}

// EventType identifies an agent lifecycle event.
type EventType string

// Agent lifecycle events — emitted to the Outbox for TUI rendering.
const (
	OnStart   EventType = "AgentStart" // agent begins
	OnStop    EventType = "AgentStop"  // agent ends (error or nil in Data)
	PreInfer  EventType = "PreInfer"   // before LLM call
	PostInfer EventType = "PostInfer"  // after LLM response (*InferResponse in Data)
	OnChunk   EventType = "Chunk"      // one streamed fragment (ai.Event in Data)

	// OnStreamReset fires when a transient stream failure is about to be
	// retried: the partial assistant output streamed so far must be discarded
	// before the next attempt re-streams from scratch (no payload).
	OnStreamReset EventType = "StreamReset"
	PreTool       EventType = "PreTool"  // before tool execution (ToolCall in Data)
	PostTool      EventType = "PostTool" // after tool execution (ToolResult in Data)
	OnMessage     EventType = "Message"  // message received on inbox (Message in Data)
	OnAppend      EventType = "Append"   // message appended to conversation chain (Message in Data)
	OnTurn        EventType = "Turn"     // think+act cycle completed (Result in Data)
	OnCompact     EventType = "Compact"  // conversation compacted (CompactInfo in Data)

	// OnCompactStart fires when auto-compaction begins, before the (blocking,
	// non-streaming) summarization call. It lets the UI show a "Compacting…"
	// progress line for the several-second window that would otherwise look
	// frozen. Data is CompactStart. Manual /compact drives this indicator from
	// the UI side and does not emit it.
	OnCompactStart EventType = "CompactStart"

	// OnSystemChange fires when a system-prompt section is added, replaced,
	// or removed. Data is SystemChange. Non-critical telemetry — never blocks
	// the outbox on backpressure.
	OnSystemChange EventType = "SystemChange"

	// OnToolsChange fires when a tool is registered or unregistered. Data is
	// ToolsChange. Like OnSystemChange, non-blocking telemetry.
	OnToolsChange EventType = "ToolsChange"
)

// Event carries context for an agent lifecycle point.
// Emitted to Outbox for TUI observation.
type Event struct {
	Type   EventType // which event
	Source string    // who triggered (agent ID, tool name, "user")
	Data   any       // payload — type depends on EventType (see above)
}

// Event.Data type helpers — reduce boilerplate in handlers.

func (e Event) ToolCall() (ToolCall, bool)         { tc, ok := e.Data.(ToolCall); return tc, ok }
func (e Event) ToolResult() (ToolResult, bool)     { tr, ok := e.Data.(ToolResult); return tr, ok }
func (e Event) Message() (Message, bool)           { m, ok := e.Data.(Message); return m, ok }
func (e Event) Result() (Result, bool)             { r, ok := e.Data.(Result); return r, ok }
func (e Event) Response() (*InferResponse, bool)   { r, ok := e.Data.(*InferResponse); return r, ok }
func (e Event) Chunk() (ai.Event, bool)            { c, ok := e.Data.(ai.Event); return c, ok }
func (e Event) Error() (error, bool)               { err, ok := e.Data.(error); return err, ok }
func (e Event) CompactInfo() (CompactInfo, bool)   { ci, ok := e.Data.(CompactInfo); return ci, ok }
func (e Event) CompactStart() (CompactStart, bool) { cs, ok := e.Data.(CompactStart); return cs, ok }

// CompactStart carries the pre-compaction message count for the OnCompactStart
// event so the progress line can read "Compacting N messages…".
type CompactStart struct {
	Count int
}

// CompactInfo carries compaction details for the OnCompact event.
type CompactInfo struct {
	Summary       string
	OriginalCount int
	// SummaryMessageID is the ID of the synthetic summary message that replaces
	// the pre-compaction chain. The session recorder writes it as the transcript
	// compaction boundary so replay truncates history at the summary instead of
	// resurrecting the summarized-away messages.
	SummaryMessageID string
	// Trigger is "auto" (proactive/reactive in-loop compaction) or "manual"
	// (user /compact). Observers use it to vary post-compaction behavior.
	Trigger string
}

// InferenceContext is the PreInfer payload — what was about to be sent to the
// LLM, expressed as content-addressed digests so consumers (trace recorder,
// debug logger) can reference inputs without copying them on every turn.
type InferenceContext struct {
	SystemDigest string   // sha256 of rendered system prompt
	ToolsDigest  string   // sha256 of canonicalized tool schemas
	MessageIDs   []string // active chain at request time, in send order
}

func (e Event) InferenceContext() (InferenceContext, bool) {
	ic, ok := e.Data.(InferenceContext)
	return ic, ok
}

// SystemChange describes one mutation to the system prompt's section map.
// Emitted on Use/Drop. The recorder translates these into
// system.section.added / system.section.removed records.
type SystemChange struct {
	Name    string // section name (stable across mutations)
	Slot    int    // render slot
	Content string // rendered content; empty when Removed
	Removed bool   // true on Drop, false on Use
	Caller  string // who triggered the mutation (e.g. "system:init", "command:/identity")
}

func (e Event) SystemChange() (SystemChange, bool) {
	c, ok := e.Data.(SystemChange)
	return c, ok
}

// ToolsChange describes one mutation to the tool registry. On removal,
// Schema.Name carries the dropped tool's name and other fields are zero.
type ToolsChange struct {
	Schema  ToolSchema // populated on Add (zero on Remove)
	Name    string     // populated on Remove (empty on Add)
	Removed bool       // true on Remove, false on Add
	Caller  string     // who triggered the mutation
}

func (e Event) ToolsChange() (ToolsChange, bool) {
	c, ok := e.Data.(ToolsChange)
	return c, ok
}

// Typed event constructors — enforce correct Data types at construction.

func StartEvent(agentID string) Event { return Event{Type: OnStart, Source: agentID} }
func StopEvent(agentID string, err error) Event {
	return Event{Type: OnStop, Source: agentID, Data: err}
}
func ChunkEvent(agentID string, e ai.Event) Event {
	return Event{Type: OnChunk, Source: agentID, Data: e}
}
func StreamResetEvent(agentID string) Event { return Event{Type: OnStreamReset, Source: agentID} }
func MessageEvent(agentID string, msg Message) Event {
	return Event{Type: OnMessage, Source: agentID, Data: msg}
}
func AppendEvent(agentID string, msg Message) Event {
	return Event{Type: OnAppend, Source: agentID, Data: msg}
}
func TurnEvent(agentID string, r Result) Event { return Event{Type: OnTurn, Source: agentID, Data: r} }
func PreInferEvent(agentID string, ctx InferenceContext) Event {
	return Event{Type: PreInfer, Source: agentID, Data: ctx}
}
func PostInferEvent(agentID string, r *InferResponse) Event {
	return Event{Type: PostInfer, Source: agentID, Data: r}
}
func PreToolEvent(tc ToolCall) Event    { return Event{Type: PreTool, Source: tc.Name, Data: tc} }
func PostToolEvent(tr ToolResult) Event { return Event{Type: PostTool, Source: tr.ToolName, Data: tr} }
func CompactEvent(agentID string, info CompactInfo) Event {
	return Event{Type: OnCompact, Source: agentID, Data: info}
}
func CompactStartEvent(agentID string, cs CompactStart) Event {
	return Event{Type: OnCompactStart, Source: agentID, Data: cs}
}

func SystemChangeEvent(agentID string, c SystemChange) Event {
	return Event{Type: OnSystemChange, Source: agentID, Data: c}
}

func ToolsChangeEvent(agentID string, c ToolsChange) Event {
	return Event{Type: OnToolsChange, Source: agentID, Data: c}
}
