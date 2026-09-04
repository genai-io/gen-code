package mcp

import (
	"context"
	"fmt"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// mcpCoreTool wraps an MCP tool as a core.Tool for use with core.Agent.
type mcpCoreTool struct {
	schema core.ToolSchema
	caller *Caller
}

func (t *mcpCoreTool) Schema() core.ToolSchema { return t.schema }

func (t *mcpCoreTool) Run(ctx context.Context, call ai.ToolCall) (agent.Result, error) {
	input, _ := core.ParseToolInput(call.Input)
	content, isError, err := t.caller.CallTool(ctx, t.schema.Name, input)
	if err != nil {
		return agent.Result{}, err
	}
	if isError {
		return agent.TextResult(content), fmt.Errorf("%s", content)
	}
	return agent.TextResult(content), nil
}

// AsCoreTools converts MCP tool schemas into core.Tool implementations
// that route execution through the provided Caller.
func AsCoreTools(schemas []core.ToolSchema, caller *Caller) []core.Tool {
	if caller == nil || len(schemas) == 0 {
		return nil
	}
	tools := make([]core.Tool, 0, len(schemas))
	for _, schema := range schemas {
		if !IsMCPTool(schema.Name) {
			continue
		}
		tools = append(tools, &mcpCoreTool{schema: schema, caller: caller})
	}
	return tools
}
