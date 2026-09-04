package core

import (
	"errors"
	"iter"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
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

	// Inbox is where the world sends messages. The caller owns it and closes
	// it when done; sending after Run returns may block forever.
	Inbox() chan<- Inbound

	// Outbox is where the agent reports. The agent owns it and closes it when
	// Run returns. Single-consumer — fan out on top if you need more.
	Outbox() <-chan Event

	// Messages is a shallow copy: do not mutate a Message's slices or
	// pointers.
	Messages() []Message

	// SetMessages replaces the conversation — compaction and session restore.
	// Shallow-copied, with the same caveat as Messages.
	SetMessages(msgs []Message)

	// Append puts a message into the conversation the next exchange opens
	// with, whether it came from the inbox or straight from a caller.
	Append(ctx context.Context, msg Message)

	// ThinkAct runs one exchange and returns what it produced. Run loops on
	// it; a subagent calls it directly after Append.
	ThinkAct(ctx context.Context) (*Result, error)

	// Run is the mailbox loop: wait on the inbox, drain what accumulated, hand
	// the batch to one exchange, repeat. Returns on ctx cancellation or
	// SigStop, which are checked at every boundary.
	Run(ctx context.Context) error

	// InterruptCurrentTurn cancels the in-flight ThinkAct without ending Run,
	// which is user-initiated mid-stream cancellation; SigStop or ctx cancel
	// is full shutdown.
	//
	// The returned channel closes once that ThinkAct has actually unwound, so
	// a caller can serialize against the agent goroutine. Called between
	// turns, it is already closed and the interrupt is latched, so the next
	// iteration bails instead of starting a turn nobody wanted.
	InterruptCurrentTurn() <-chan struct{}
}

// Config builds an agent. Client, System and Tools are required; NewAgent
// panics without them. Permission is the tool layer's — wrap Tools with
// tool.WithPermission first. See docs/concepts/permission-model.md.
type Config struct {
	ID string
	// Client is the model for one turn. A function of the conversation because
	// an endpoint's headers can depend on what the turn sends — see
	// llm.TurnHeaders.
	Client func(msgs []Message) (*ai.Client, error)
	// CallOptions are per-call settings — the output cap, the reasoning rung —
	// asked for fresh each inference, so a change mid-session lands on the next
	// call. Nil leaves the model's defaults.
	CallOptions func() []ai.Option
	// InputLimit is the prompt budget auto-compaction measures against. Nil or
	// zero turns it off. Not read off the client: the window is the model's
	// unless a setting overrides it, and the setting is the application's.
	InputLimit              func() int
	System                  System                                                    // required: system prompt layers
	Tools                   Tools                                                     // required: available tools (wrap with tool.WithPermission for permission)
	CompactFunc             func(ctx context.Context, msgs []Message) (string, error) // optional: summarize messages for compaction
	MaxSteps                int                                                       // max LLM inference steps per turn, 0 = unlimited
	MaxContinuations        int                                                       // how many times a model cut off by the output cap is asked to carry on, 0 = use default (3)
	MaxTurnRetries          int                                                       // max retries per inference step on transient stream errors, 0 = use default (2)
	StreamFirstChunkTimeout time.Duration                                             // abort if no first chunk arrives within this long, 0 = use default (5m)
	StreamIdleTimeout       time.Duration                                             // abort a stream that goes silent between chunks for this long, 0 = use default (60s)
	InboxBuf                int                                                       // inbox channel buffer size, default 16
	OutboxBuf               int                                                       // outbox channel buffer size, default 64; -1 = no outbox (subagent path)
	// OnEvent observes lifecycle events synchronously, even when OutboxBuf is -1.
	OnEvent func(Event)
}

// Deliberately small: ride out a brief blip — overload, a rate limit, a
// dropped connection — not a sustained outage.
const (
	defaultMaxTurnRetries = 2
	// Three, because a fourth continuation of the same answer is a prompt
	// problem rather than a budget one.
	defaultMaxContinuations = 3
	// Generous, because a reasoning model may think a long time before its
	// first token. It only catches a connection that hangs at open.
	defaultFirstChunkTimeout = 5 * time.Minute
	// The gap *between* chunks once a response has started — a much tighter
	// signal that an in-flight stream has stalled.
	defaultStreamIdleTimeout = 60 * time.Second
)

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
	if cfg.MaxContinuations <= 0 {
		cfg.MaxContinuations = defaultMaxContinuations
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
		id:          cfg.ID,
		system:      cfg.System,
		tools:       cfg.Tools,
		compactFunc: cfg.CompactFunc,
		client:      cfg.Client,
		callOptions: cfg.CallOptions,
		inputLimit:  cfg.InputLimit,
		inbox:       make(chan Inbound, cfg.InboxBuf),
		outbox:      outbox,
		onEvent:     cfg.OnEvent,
	}

	// The SDK agent needs a client and San has none yet: which one answers a
	// turn depends on what the turn sends, so PreInfer resolves it per call.
	// The placeholder is replaced before it is ever used, and if the
	// application cannot produce one the turn fails with its reason rather
	// than construction panicking.
	inner, err := sdkagent.New(clientFromPreInfer,
		sdkagent.WithMaxSteps(cfg.MaxSteps),
		// Two budgets that used to be one. WithRetry replays a call the loop
		// knows how to replay; WithContinuation asks a model cut off by the
		// output cap to carry on, in San's own words.
		//
		// The +1 is the difference between the two words: San's setting counts
		// retries, the SDK's counts attempts, and two retries is three goes.
		sdkagent.WithRetry(cfg.MaxTurnRetries+1, backoffBase),
		sdkagent.WithContinuation(cfg.MaxContinuations, TruncatedResumePrompt),
		sdkagent.WithStreamTimeout(cfg.StreamFirstChunkTimeout, cfg.StreamIdleTimeout),
		// Every message in the conversation gets a name, which is what the
		// session's append-only writer dedupes by.
		sdkagent.WithMessageIDs(NewMessageID),
		sdkagent.WithHooks(a.hooks()),
	)
	if err != nil {
		panic("core.NewAgent: " + err.Error())
	}
	a.inner = inner
	// Mirror system + tools mutations onto the event bus. Attach after
	// construction so each registry replays its initial members back to the
	// observer — the recorder sees a complete event chain from t0.
	cfg.System.SetObserver(func(c SystemChange) {
		a.emitTelemetry(c)
	})
	cfg.Tools.SetObserver(func(c ToolsChange) {
		a.emitTelemetry(c)
	})
	return a
}

// Result represents the outcome of one completed turn (end_turn).
// Emitted to Outbox as Event{Type: OnTurn, Data: result}.
type Result struct {
	Content    string     // final text output of this turn
	Messages   []Message  // full conversation history
	Steps      int        // LLM inference steps in this turn
	ToolUses   int        // tool calls in this turn
	Usage      Usage      // what every inference in this turn spent, summed
	StopReason StopReason // why the loop stopped
	StopDetail string     // human-readable detail (e.g. hook block reason)
}

// EventType identifies an agent lifecycle event.
// Event is one thing that happened: the SDK's twelve, verbatim, plus the six
// below. San adds only what the loop has no concept of, because it is about
// the mailbox and the registries rather than one exchange.
//
//	sdkagent.MessageAdded   MessagesReplaced                 the conversation changing
//	sdkagent.MessageStart   MessageUpdate     MessageEnd     one inference
//	sdkagent.ToolStart      ToolUpdate        ToolEnd        one tool call
//	sdkagent.CompactionStart                  CompactionEnd  the shortening span
//	sdkagent.TurnStart                        TurnEnd        one exchange
type Event any

// AgentStarted and AgentStopped bracket Run — the agent's life, however many
// turns wide. The loop is handed one exchange and never learns there was a
// mailbox.
type AgentStarted struct{}

type AgentStopped struct{ Err error }

// MessageReceived is a message reaching the inbox, before an exchange opened
// for it.
type MessageReceived struct{ Message Message }

// TurnEnded closes a San turn: however many exchanges the mailbox drained into
// it, not sdkagent.TurnEnd, which closes one.
type TurnEnded struct{ Result Result }

// Compacted is what a compaction collapsed to. The loop reports the span; the
// summary and its ID are the application's.
type Compacted struct {
	Summary       string
	OriginalCount int
	// SummaryMessageID names the summary that replaced the chain. The recorder
	// writes it as the transcript boundary, so replay truncates there instead
	// of resurrecting the summarized-away messages.
	SummaryMessageID string
	// Trigger is "auto" or "manual" (/compact).
	Trigger string
}

// SystemChange is one mutation to the system prompt's section map, emitted on
// Use/Drop. The recorder writes these as system.section.added / .removed.
type SystemChange struct {
	Name    string // stable across mutations
	Slot    int
	Content string // empty when Removed
	Removed bool
	Caller  string // e.g. "system:init", "command:/identity"
}

// ToolsChange is one mutation to the tool registry.
type ToolsChange struct {
	Schema  ToolSchema // set on Add
	Name    string     // set on Remove
	Removed bool
	Caller  string
}

// clientFromPreInfer is the client the agent is built on, named for where the
// real one comes from: San cannot supply it here, because which client answers
// a turn depends on what that turn sends, so PreInfer resolves it per call with
// the conversation in hand and replaces this every time.
//
// Reaching it therefore means San handed the loop an agent it never configured,
// and it says exactly that rather than failing as though a model had been asked
// and had not answered.
var clientFromPreInfer = ai.NewClientWithDriver(unconfiguredDriver{}, ai.Model{ID: "unconfigured", API: "stub"})

type unconfiguredDriver struct{}

func (unconfiguredDriver) Name() string { return "unconfigured" }

func (unconfiguredDriver) Stream(context.Context, *ai.Request) iter.Seq2[ai.Delta, error] {
	return func(yield func(ai.Delta, error) bool) {
		yield(ai.Delta{}, errors.New("core: no model was configured for this turn"))
	}
}
