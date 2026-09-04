package core

import (
	"context"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// What the model may call, and how a tool learns which call it is running.
// The exchange asks once, through offered, and never looks inside.

// offered is what the model may call this exchange, with the ones that must
// not run beside others marked Sequential.
//
// The rule moved off the batch and onto the tool: each one carries the answer
// into the loop, so a batch is parallel exactly when every member says it may
// be. Which tools those are is still this list — nothing in a schema
// distinguishes reading a file from writing one.
func (a *agent) offered() []Tool {
	all := a.tools.All()
	out := make([]Tool, 0, len(all))
	for _, t := range all {
		tagged := withCallID{inner: t}
		if name := t.Schema().Name; isReadOnlyTool(name) || spawnsAnAgent(name) {
			out = append(out, tagged)
			continue
		}
		// Sequential must be outermost: the mark is on the value the loop
		// holds, and a wrapper outside it would hide the mark.
		out = append(out, sdkagent.Sequential(tagged))
	}
	return out
}

// withCallID tells a tool which call it is running, which is how one that
// reports progress says whose row it is about. The SDK hands the tool the call;
// this puts the ID where San's tools have always looked for it.
type withCallID struct{ inner Tool }

func (i withCallID) Schema() ToolSchema { return i.inner.Schema() }

func (i withCallID) Run(ctx context.Context, call ai.ToolCall) (sdkagent.Result, error) {
	return i.inner.Run(WithToolCallID(ctx, call.ID), call)
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

func isReadOnlyTool(name string) bool {
	switch name {
	case "Read", "WebFetch", "WebSearch", "LSP":
		return true
	default:
		return false
	}
}

func spawnsAnAgent(name string) bool {
	return name == "Agent" || name == "SendMessage"
}
