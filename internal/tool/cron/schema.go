package cron

import "github.com/genai-io/san/internal/core"

// Schema returns the model-facing tool definition for Cron.
func (t *CronTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Cron",
		Description: `Manage prompts scheduled into this session. Uses standard 5-field cron: minute hour day-of-month month day-of-week. Jobs only fire while the REPL is idle.

Actions:
- create: schedule a prompt (requires cron + prompt). Recurring jobs (default) auto-expire after 7 days; one-shot jobs (recurring=false) fire once then auto-delete. Returns a job ID.
- delete: cancel a job by id.
- list: show all jobs with status, next fire time, and prompt.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"create", "delete", "list"},
					"description": "What to do: create, delete, or list scheduled jobs.",
				},
				"cron": map[string]any{
					"type":        "string",
					"description": "For create: 5-field cron expression in local time (e.g., '*/5 * * * *', '0 9 * * 1-5')",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "For create: the prompt to enqueue at each fire time",
				},
				"recurring": map[string]any{
					"type":        "boolean",
					"description": "For create: true (default) = fire repeatedly. false = fire once then auto-delete.",
				},
				"durable": map[string]any{
					"type":        "boolean",
					"description": "For create: if true, job persists across sessions for this project (saved to .san/scheduled_tasks.json). Default: false (session-only).",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "For delete: the job ID returned by create",
				},
			},
			"required": []string{"action"},
		},
	}
}
