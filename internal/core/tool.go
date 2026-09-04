package core

import (
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// Tool is one thing an agent can do, and it is the SDK's: what the model is
// told, and what answers a call.
//
// It carries no Name or Description of its own. Those are Schema().Name and
// Schema().Description — one fact with one home, where before a tool could
// answer one thing to a caller and declare another to the model.
type Tool = sdkagent.Tool

// ToolSchema is what the model is told a tool takes. It is ai.Schema: the
// same three things — a name, a description, and the JSON Schema itself —
// which is what the SDK sends and what it validates arguments against.
type ToolSchema = ai.Schema

// Tools is a mutable, queryable collection of tools.
//
// Can change dynamically: hooks add/remove tools, agent definitions
// restrict to read-only, parent agents filter child tool sets.
type Tools interface {
	Get(name string) Tool
	All() []Tool
	// Add registers (or replaces) a tool. Caller tags the mutation source
	// (e.g. "mcp:weather", "agent:init") for trace records.
	Add(tool Tool, caller string)
	// Remove unregisters a tool by name. No-op if absent.
	Remove(name, caller string)
	Schemas() []ToolSchema

	// SetObserver installs a callback invoked synchronously on every
	// subsequent Add/Remove. Attaching also replays existing tools as
	// synthetic Add events so the observer sees the full registry from t0.
	SetObserver(fn func(ToolsChange))
}

// ToAITools is what an inference is told it may call. Run stays nil: San
// executes tools itself and hands the results back as history, so the SDK is
// never asked to run one.
func ToAITools(schemas []ToolSchema) []ai.Tool {
	out := make([]ai.Tool, len(schemas))
	for i, schema := range schemas {
		out[i] = ai.Tool{Schema: schema}
	}
	return out
}
