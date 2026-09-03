package agent

import (
	"strings"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
)

// AgentTool describes itself with the available-agents directory embedded when
// one is supplied at build time.
var _ tool.AgentDirectoryAwareTool = (*AgentTool)(nil)

// Schema returns the model-facing tool definition for Agent, without an
// available-agents directory. GetToolSchemasWith injects the directory via
// SchemaWithAgentDirectory when one is available.
func (t *AgentTool) Schema() core.ToolSchema {
	return agentSchema("")
}

// SchemaWithAgentDirectory returns the Agent schema with the available-agents
// directory embedded in the description. It satisfies tool.AgentDirectoryAwareTool
// so the schema follows the live agent catalog on each rebuild. An empty
// directory yields the same result as Schema.
func (t *AgentTool) SchemaWithAgentDirectory(agentDirectory string) core.ToolSchema {
	return agentSchema(agentDirectory)
}

// agentSchema builds the Agent tool schema with the given agent-directory body
// embedded directly in the description. The directory is rendered before the
// usage notes so the LLM sees the available agent names right after the
// opening line.
func agentSchema(agentDirectory string) core.ToolSchema {
	agentDirectory = strings.TrimSpace(agentDirectory)

	var sb strings.Builder
	sb.WriteString("Launch a subagent only when separate context or parallel execution materially helps. Handle clear, bounded work directly, even when it requires multiple tool calls.\n\n")
	if agentDirectory != "" {
		sb.WriteString("Available agent definitions:\n\n")
		sb.WriteString(agentDirectory)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Brief the agent with all context it needs: the goal, relevant paths, constraints, and what is already known. Use explore for read-only investigation and edit for file changes.\n\n")
	sb.WriteString("Launch independent agents concurrently. Use background mode only for work that does not block your next step. Verify the result before reporting it.")

	return core.ToolSchema{
		Name:        "Agent",
		Description: sb.String(),
		Definition:  agentToolParameters,
	}
}

var agentToolParameters = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"prompt": map[string]any{
			"type":        "string",
			"description": "The task for the agent to perform",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "A short (3-5 word) description of the task",
		},
		"name": map[string]any{
			"type":        "string",
			"description": "Choose an available agent, or name a new general-purpose agent for this task. New names are for display only.",
		},
		"run_in_background": map[string]any{
			"type":        "boolean",
			"description": "Set to true to run this agent in the background. You will be notified when it completes.",
		},
		"max_steps": map[string]any{
			"type":        "number",
			"description": "Maximum number of LLM inference steps. Defaults to 500; lower values are raised to 500.",
		},
		"mode": map[string]any{
			"type":        "string",
			"description": "Permission mode for the spawned agent: explore = read-only; edit = can modify files; default = use the named definition's configured mode, or inherit the parent session when name is empty.",
			"enum":        []string{"explore", "edit", "default"},
		},
	},
	"required": []string{"description", "prompt"},
}

// Schema returns the model-facing tool definition for AgentStop.
func (t *AgentStopTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: t.Name(),
		Description: `Stops a running background agent.

Only use the exact task_id returned when that agent was started. This tool cannot stop background Bash commands; use the process-group command reported by Bash instead.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The exact Task ID of the running background agent to stop.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional concise reason for stopping the agent.",
				},
			},
			"required": []string{"task_id"},
		},
	}
}

// Schema returns the model-facing tool definition for SendMessage.
func (t *SendMessageTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "SendMessage",
		Description: `Send a message to another agent, routed by the broker. The message lands in the recipient's inbox and is read at its next step (a running subagent) or turn boundary (the main conversation).

Recipients (to):
- a running subagent's task id — steer or add information to a subagent that is still working.
- "main" — from inside a subagent, send an interim note to the main conversation without ending your run.

Notes:
- Delivery is best-effort: a subagent that has finished (or never takes another step) will not see the message. A subagent's final result comes back on its own when it completes — do not use SendMessage for it.
- The recipient sees the message as a user turn — make it self-contained.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type":        "string",
					"description": "Recipient address: a running subagent's task id, or \"main\".",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "The message to deliver. Self-contained — the recipient reads it as a user turn.",
				},
			},
			"required": []string{"to", "message"},
		},
	}
}
