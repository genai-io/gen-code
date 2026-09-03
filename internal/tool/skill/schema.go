package skill

import "github.com/genai-io/san/internal/core"

// Schema returns the model-facing tool definition for Skill.
func (t *SkillTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Skill",
		Description: `Invoke a skill by exact name (no leading slash; plugin skills as "plugin:skill").

- Available skills are listed in <system-reminder> messages; only invoke one that appears there. A "/name" (slash command) in a user message refers to a skill — but not built-in CLI commands like /help or /clear.
- When a skill matches the task, invoke it BEFORE generating any other response about the task.
- Don't invoke a skill that is already running. If the user message carries a <command-name> tag, the skill body is already inlined — follow it instead of calling this tool again.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill": map[string]any{
					"type":        "string",
					"description": "The skill name (e.g., 'commit', 'git:pr', 'pdf')",
				},
				"args": map[string]any{
					"type":        "string",
					"description": "Optional arguments for the skill",
				},
			},
			"required": []string{"skill"},
		},
	}
}
