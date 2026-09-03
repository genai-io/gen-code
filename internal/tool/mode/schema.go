package mode

import "github.com/genai-io/san/internal/core"

// Schema returns the model-facing tool definition for AskUserQuestion.
func (t *AskUserQuestionTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "AskUserQuestion",
		Description: `Ask the user a question with predefined choices; an 'Other' free-text option is appended automatically.

Use this instead of plain text when you need a decision or preference (ambiguous request, multiple valid approaches); skip it when a reasonable default exists or the answer is in the code.

Single question (most common):
  {"question": "Which version?", "options": ["v1.0", "v2.0", "v3.0"]}

Multiple questions (rare):
  {"questions": [{"question": "Q1?", "options": ["A","B"]}, {"question": "Q2?", "options": ["X","Y"]}]}`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question text (for single question)",
				},
				"options": map[string]any{
					"type":        "array",
					"description": "2-8 short choice labels (for single question)",
					"minItems":    2,
					"maxItems":    8,
					"items":       map[string]any{"type": "string"},
				},
				"questions": map[string]any{
					"type":        "array",
					"description": "For multiple questions. Array of {question, options} objects (max 8).",
					"minItems":    1,
					"maxItems":    8,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"question": map[string]any{"type": "string"},
							"options": map[string]any{
								"type":     "array",
								"minItems": 2,
								"maxItems": 8,
								"items":    map[string]any{"type": "string"},
							},
						},
						"required": []string{"question", "options"},
					},
				},
			},
			"minProperties": 1,
			"required":      []string{},
		},
	}
}
