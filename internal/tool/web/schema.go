package web

import "github.com/genai-io/san/internal/core"

// Schema returns the model-facing tool definition for WebFetch.
func (t *WebFetchTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "WebFetch",
		Description: `Fetches a URL and returns its content as markdown.

- HTTP is upgraded to HTTPS; very large content may be truncated.
- For GitHub URLs prefer the gh CLI via Bash (gh pr view, gh issue view, gh api).`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to fetch content from",
				},
				"format": map[string]any{
					"type":        "string",
					"description": "Output format: 'markdown' (default) or 'raw'",
				},
			},
			"required": []string{"url"},
		},
	}
}

// Schema returns the model-facing tool definition for WebSearch.
func (t *WebSearchTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "WebSearch",
		Description: `Search the web for up-to-date information. Returns a list of relevant results with titles, URLs, and snippets.
When searching for current information, always use the present year rather than previous years.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query",
				},
				"num_results": map[string]any{
					"type":        "integer",
					"description": "Number of results to return (default: 10)",
				},
				"allowed_domains": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Only include results from these domains",
				},
				"blocked_domains": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Exclude results from these domains",
				},
			},
			"required": []string{"query"},
		},
	}
}
