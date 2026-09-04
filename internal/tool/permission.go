package tool

import (
	"context"
	"fmt"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool/perm"
	"github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// WithPermission wraps core.Tools with permission checking.
// nil check returns inner unchanged (permit-all). The checker itself decides
// whether safe tools are allowed so deny/ask rules still apply consistently.
func WithPermission(inner core.Tools, check perm.PermissionFunc) core.Tools {
	if check == nil {
		return inner
	}
	return &permissionTools{inner: inner, check: check}
}

// permissionTools wraps a core.Tools and injects permission checking on Get().
type permissionTools struct {
	inner core.Tools
	check perm.PermissionFunc
}

func (pt *permissionTools) Get(name string) core.Tool {
	t := pt.inner.Get(name)
	if t == nil {
		return nil
	}
	return &permissionTool{inner: t, check: pt.check}
}

func (pt *permissionTools) All() []core.Tool                      { return pt.inner.All() }
func (pt *permissionTools) Add(tool core.Tool, caller string)     { pt.inner.Add(tool, caller) }
func (pt *permissionTools) Remove(name, caller string)            { pt.inner.Remove(name, caller) }
func (pt *permissionTools) Schemas() []core.ToolSchema            { return pt.inner.Schemas() }
func (pt *permissionTools) SetObserver(fn func(core.ToolsChange)) { pt.inner.SetObserver(fn) }

// permissionTool wraps a single core.Tool with permission checking.
type permissionTool struct {
	inner core.Tool
	check perm.PermissionFunc
}

func (pt *permissionTool) Schema() core.ToolSchema { return pt.inner.Schema() }

func (pt *permissionTool) Run(ctx context.Context, call ai.ToolCall) (agent.Result, error) {
	input, _ := core.ParseToolInput(call.Input)
	if allow, reason := pt.check(ctx, call.Name, input); !allow {
		return agent.Result{}, fmt.Errorf("blocked: %s", reason)
	}
	return pt.inner.Run(ctx, call)
}
