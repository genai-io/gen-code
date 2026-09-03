package tasktools

import "github.com/genai-io/san/internal/core"

// Schema returns the model-facing tool definition for TaskCreate.
func (t *TrackerCreateTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "TaskCreate",
		Description: `Create a task to track progress on multi-step work. The task list is shown to the user above the input area.

When to use:
- Complex tasks requiring 3+ distinct steps
- User provides multiple tasks at once

When NOT to use:
- Single straightforward task or trivial fix
- Purely conversational or informational exchange

Granularity: one task per logical deliverable (a file, a feature, a test suite).
Don't create tasks for sub-steps within a single file or for "planning"/"summarizing".

Tips:
- Prefer sending ALL TaskCreate calls in a single message (parallel tool calls) for speed
- Use imperative subjects ("Fix bug", "Add tests")
- Provide activeForm for spinner display ("Fixing bug", "Adding tests")
- Check TaskGet first to avoid duplicates
- Task IDs are sequential integers starting from 1. Use addBlockedBy to set dependencies (e.g. addBlockedBy=["1"])`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subject": map[string]any{
					"type":        "string",
					"description": "A brief, actionable title in imperative form (e.g., 'Fix authentication bug')",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Detailed description of what needs to be done, including context and acceptance criteria",
				},
				"activeForm": map[string]any{
					"type":        "string",
					"description": "Present continuous form shown in spinner when in_progress (e.g., 'Fixing authentication bug'). If omitted, the spinner shows the subject instead.",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Arbitrary metadata to attach to the task",
				},
				"addBlockedBy": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Task IDs that must complete before this task can start",
				},
			},
			"required": []string{"subject", "description"},
		},
	}
}

// Schema returns the model-facing tool definition for TaskGet.
func (t *TrackerGetTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "TaskGet",
		Description: `Read tasks. Omit taskId to list every task; pass a taskId for one task's full details.

List mode (no taskId):
- Check overall progress, or find the next available task after finishing one
- Returns one summary line per task (id, status, owner); work in ID order, lowest first
- Find blocked tasks whose dependencies still need resolving

Detail mode (taskId):
- Before starting a task — read its full requirements, status, and dependencies
- Verify "Blocked by (open)" is empty before beginning work`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"taskId": map[string]any{
					"type":        "string",
					"description": "The ID of the task to retrieve full details for. Omit to list all tasks instead.",
				},
			},
		},
	}
}

// Schema returns the model-facing tool definition for TaskUpdate.
func (t *TrackerUpdateTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "TaskUpdate",
		Description: `Update a task's status, details, or dependencies.

Status: pending → in_progress → completed. Use "deleted" to remove.
- Set in_progress BEFORE starting work
- ONLY mark completed when FULLY done (not if tests fail or partial)
- After completing, call TaskGet for the next task
- If blocked, keep as in_progress and create a new task for the blocker
- Close out every in_progress task before ending your turn; one left open is
  shown as stalled, since nothing is executing it any more
- When a <task-reminder> block appears in the conversation, review and update
  stale tasks immediately`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"taskId": map[string]any{
					"type":        "string",
					"description": "The ID of the task to update",
				},
				"status": map[string]any{
					"type":        "string",
					"description": "New status: pending, in_progress, completed, or deleted",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "New subject for the task",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "New description for the task",
				},
				"activeForm": map[string]any{
					"type":        "string",
					"description": "Present continuous form shown in spinner when in_progress (e.g., 'Fixing authentication bug')",
				},
				"owner": map[string]any{
					"type":        "string",
					"description": "New owner for the task (agent name)",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Metadata keys to merge into the task (set a key to null to delete it)",
				},
				"addBlocks": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Task IDs that this task blocks",
				},
				"addBlockedBy": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Task IDs that block this task",
				},
			},
			"required": []string{"taskId"},
		},
	}
}
