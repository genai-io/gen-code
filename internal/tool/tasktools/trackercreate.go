package tasktools

import (
	"context"
	"fmt"

	"github.com/genai-io/san/internal/todo"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/toolresult"
)

// TrackerCreateTool creates a new tracked task
type TrackerCreateTool struct{}

func (t *TrackerCreateTool) Name() string        { return "TaskCreate" }
func (t *TrackerCreateTool) Description() string { return "Create a task to track progress" }
func (t *TrackerCreateTool) Icon() string        { return "📋" }

func (t *TrackerCreateTool) Execute(ctx context.Context, params map[string]any, cwd string) toolresult.ToolResult {
	subject := tool.GetString(params, "subject")
	if subject == "" {
		return toolresult.NewErrorResult(t.Name(), "subject is required")
	}

	description := tool.GetString(params, "description")
	if description == "" {
		return toolresult.NewErrorResult(t.Name(), "description is required")
	}

	activeForm := tool.GetString(params, "activeForm")
	metadata, _ := params["metadata"].(map[string]any)

	task := todo.Default().Create(subject, description, activeForm, metadata)

	// Set dependencies if provided
	note := ""
	if ids := parseStringSlice(params["addBlockedBy"]); len(ids) > 0 {
		if err := todo.Default().Update(task.ID, todo.WithAddBlockedBy(ids)); err != nil {
			note = fmt.Sprintf(" (blockers not set: %v)", err)
		}
	}

	return toolresult.ToolResult{
		Success: true,
		Output:  fmt.Sprintf("Task #%s created: %s%s", task.ID, task.Subject, note),
		Metadata: toolresult.ResultMetadata{
			Title:    t.Name(),
			Icon:     t.Icon(),
			Subtitle: task.Subject,
		},
	}
}

func init() {
	tool.Register(&TrackerCreateTool{})
}
