package subagent

import (
	"context"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// activityTools wraps core.Tools to call onExec before each tool execution.
type activityTools struct {
	inner  core.Tools
	onExec func(name string, params map[string]any)
}

func (a *activityTools) Get(name string) core.Tool {
	t := a.inner.Get(name)
	if t == nil {
		return nil
	}
	return &activityTool{inner: t, onExec: a.onExec}
}

// All wraps like Get does. A set whose members differ depending on how you
// ask for them is a set that will be read the wrong way by somebody.
func (a *activityTools) All() []core.Tool {
	inner := a.inner.All()
	out := make([]core.Tool, 0, len(inner))
	for _, t := range inner {
		out = append(out, &activityTool{inner: t, onExec: a.onExec})
	}
	return out
}

func (a *activityTools) Add(t core.Tool, caller string)        { a.inner.Add(t, caller) }
func (a *activityTools) Remove(name, caller string)            { a.inner.Remove(name, caller) }
func (a *activityTools) Schemas() []core.ToolSchema            { return a.inner.Schemas() }
func (a *activityTools) SetObserver(fn func(core.ToolsChange)) { a.inner.SetObserver(fn) }

type activityTool struct {
	inner  core.Tool
	onExec func(name string, params map[string]any)
}

func (t *activityTool) Schema() core.ToolSchema { return t.inner.Schema() }
func (t *activityTool) Run(ctx context.Context, call ai.ToolCall) (agent.Result, error) {
	params, _ := core.ParseToolInput(call.Input)
	t.onExec(call.Name, params)
	return t.inner.Run(ctx, call)
}
