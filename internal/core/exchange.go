package core

import (
	"context"
	"fmt"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// One exchange: the toolset it offers, the hooks it installs, driving the SDK's
// loop through it, and turning every event the loop reports into San's.
//
// Shortening the conversation is not here, though two of these hooks ask for
// it: the person can ask for it too, through the inbox, so it belongs to
// neither the exchange nor the mailbox. See compact.go.

// ThinkAct runs one exchange and reports what it produced.
//
// The exchange is the SDK's: it calls the model, runs the tools it asked for,
// replays what can be replayed and compacts where the hooks say so. What
// happens here is translation — every event becomes San's, and the Result is
// folded out of that same stream rather than tracked alongside it, so what an
// observer sees and what this returns cannot come to disagree about one turn.
func (a *agent) ThinkAct(ctx context.Context) (*Result, error) {
	// The prompt and the toolset are read afresh every exchange: both are
	// registries the application mutates while the agent runs.
	a.inner.SetSystem(a.system.Prompt())
	a.inner.SetTools(a.offered()...)

	out := &Result{}
	var turnErr error

	for event, err := range a.inner.Run(ctx, a.takePending()...) {
		if err != nil {
			// Outside a turn — ErrBusy today, which means two callers drove
			// one conversation and the second must not silently do nothing.
			return nil, err
		}
		a.translate(ctx, event, out)
		if end, ok := event.(sdkagent.TurnEnd); ok {
			turnErr = end.Err
		}
	}

	out.Messages = a.Messages()
	return out, turnErr
}

// translate turns one SDK event into San's and folds it into the outcome.
//
// Three of the SDK's twelve are deliberately not forwarded, and saying so is
// the point — an unhandled case in a closed set is otherwise indistinguishable
// from one nobody thought about:
//
//	MessagesReplaced  the only hooks that replace a conversation are San's own
//	                  two, and both announce the one message they collapse to
//	                  as an append — which is what a transcript replays from.
//	ToolUpdate        a tool reporting mid-run. No San tool calls agent.Report;
//	                  the first one that does wants a case here.
//	TurnStart         San's turn boundary is the mailbox's, not the exchange's:
//	                  Run emits it around however many exchanges a turn took.
func (a *agent) translate(ctx context.Context, event sdkagent.Event, out *Result) {
	switch e := event.(type) {
	case sdkagent.MessageStart:
		// A retry announces itself by the absence of an appended message, not
		// by an event of its own: what a consumer drew for the previous
		// attempt is void, and this is what tells it so.
		if e.Attempt > 1 {
			a.emit(ctx, StreamResetEvent(a.id))
		}
		a.emit(ctx, PreInferEvent(a.id, inferenceContextOf(e.Inference)))

	case sdkagent.MessageUpdate:
		a.emit(ctx, ChunkEvent(a.id, e.Delta))

	case sdkagent.MessageEnd:
		if e.Err != nil || e.Response == nil {
			return
		}
		out.Steps++
		out.InputTokens += e.Response.Usage.Input
		out.OutputTokens += e.Response.Usage.Output
		a.emit(ctx, PostInferEvent(a.id, e.Response))

	case sdkagent.MessageAdded:
		a.emit(ctx, AppendEvent(a.id, e.Message))

	case sdkagent.ToolStart:
		out.ToolUses++
		a.emit(ctx, PreToolEvent(ToolCall{ID: e.ID, Name: e.Name, Input: e.Args}))

	case sdkagent.ToolEnd:
		a.emit(ctx, PostToolEvent(ToolResult{
			ToolCallID: e.ID,
			ToolName:   e.Name,
			Content:    sdkagent.ResultContent(e.Result, e.Err),
			IsError:    e.Err != nil,
		}))

	case sdkagent.CompactionStart:
		a.emit(ctx, CompactStartEvent(a.id, CompactStart{Count: len(e.Messages)}))

	case sdkagent.CompactionEnd:
		// The loop closes this span whichever way the hook went, which is the
		// only reason a consumer that drew a progress line on the start is
		// guaranteed to be told to stop.
		a.emit(ctx, CompactEndEvent(a.id, CompactEnd{Count: len(e.Messages), Err: e.Err}))

	case sdkagent.TurnEnd:
		out.Content = e.Message.Text()
		out.StopReason = stopReasonOf(e.StopReason)
		if e.Err != nil {
			out.StopDetail = e.Err.Error()
		}
	}
}

// inferenceContextOf describes the call about to go out as digests rather than
// content, so an observer can reference the inputs without copying them.
func inferenceContextOf(inf *sdkagent.Inference) InferenceContext {
	if inf == nil {
		return InferenceContext{}
	}
	schemas := make([]ToolSchema, 0, len(inf.Tools))
	for _, t := range inf.Tools {
		schemas = append(schemas, t.Schema)
	}
	return InferenceContext{
		SystemDigest: sha256Hex([]byte(inf.System)),
		ToolsDigest:  toolsDigest(schemas),
		MessageIDs:   messageIDs(inf.Messages),
	}
}

// stopReasonOf maps the SDK's reasons onto San's.
//
// max_tokens becomes MaxOutputRecoveryExhausted because by the time the loop
// reports it, WithContinuation has already asked the model to carry on as often
// as it was allowed to: the answer is still cut off, and that is what San's
// name says. Terminated is a tool voting to end the turn, which in San is only
// ever a hook doing it.
func stopReasonOf(r sdkagent.StopReason) StopReason {
	switch r {
	case sdkagent.StopMaxTokens:
		return StopMaxOutputRecoveryExhausted
	case sdkagent.StopMaxSteps:
		return StopMaxSteps
	case sdkagent.StopCanceled:
		return StopCancelled
	case sdkagent.StopTerminated:
		return StopHook
	case sdkagent.StopError:
		return StopError
	default:
		// StopEndTurn, and the two reasons the SDK names that San does not:
		// a refusal and a stop sequence both end a turn the model was allowed
		// to finish. Only a failed inference is StopError, and only because
		// Run reads that one to mean the agent died — anything else landing
		// there would swallow the turn boundary the interface waits on.
		return StopEndTurn
	}
}

// sequenced marks the tools that must not run beside others, which is how the
// SDK is told what may go in parallel.
//
// San's rule used to be a property of the batch — all read-only, or all
// agent-spawning — checked in the loop against a list of names. It is the same
// rule said where it belongs: a tool that may touch shared state declares it,
// and one such tool makes its whole batch sequential, because a batch is only
// safe to parallelize when every member is.
func (a *agent) offered() []Tool {
	all := a.tools.All()
	out := make([]Tool, 0, len(all))
	for _, t := range all {
		named := withCallID{inner: t}
		if name := t.Schema().Name; isReadOnlyToolCall(name) || isAgentSpawnToolCall(name) {
			out = append(out, named)
			continue
		}
		// Sequential must be outermost: the mark is on the value the loop
		// holds, and a wrapper outside it would hide the mark.
		out = append(out, sdkagent.Sequential(named))
	}
	return out
}

// withCallID tells a tool which call it is running: how one that reports
// progress says whose row it is reporting about, and how a side effect finds
// its way back to the result it belongs to.
//
// The loop used to do this before dispatching. The SDK hands a tool the call
// itself, so the ID is right there — this only puts it where San's tools have
// always looked for it.
type withCallID struct{ inner Tool }

func (i withCallID) Schema() ToolSchema { return i.inner.Schema() }

func (i withCallID) Run(ctx context.Context, call ai.ToolCall) (sdkagent.Result, error) {
	return i.inner.Run(WithToolCallID(ctx, call.ID), call)
}

// hooks is where San gets between the SDK's loop and the model: which client
// answers this turn and what options ride with it, and — in compact.go, since
// the answer is the same one /compact gives — when the conversation has grown
// past what can be sent.
func (a *agent) hooks() sdkagent.Hook {
	return sdkagent.Hook{
		PreInfer:     a.preInfer,
		PreStep:      a.preStep,
		OnInferError: a.onInferError,
	}
}

// preInfer points the call at the client this turn's messages ask for, and
// layers on whatever options the application wants with it.
//
// The client is asked for per call rather than held on the agent because one
// vendor's headers depend on what the turn sends — an interactive endpoint
// bills a user-initiated turn differently from an agent-initiated one.
func (a *agent) preInfer(_ context.Context, inf *sdkagent.Inference) error {
	if a.client != nil {
		client, err := a.client(inf.Messages)
		if err != nil {
			return fmt.Errorf("reaching the model: %w", err)
		}
		inf.Client = client
	}
	if a.callOptions != nil {
		inf.Options = append(inf.Options, a.callOptions()...)
	}
	return nil
}

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
