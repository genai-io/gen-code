package fs

import (
	"fmt"

	"github.com/genai-io/san/internal/core"
)

// Schema returns the model-facing tool definition for Read. The limits and
// the truncation marker are formatted from the same constants read.go
// enforces, so the model's instructions can't drift from the behavior.
func (t *ReadTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Read",
		Description: fmt.Sprintf(`Reads a file from the local filesystem.

- Prefer relative paths for files inside the session working directory; absolute for targets outside it
- Reads up to %d lines from the start by default; when you already know which part of a large file you need, read just that part with offset/limit
- Read output has a line-number and tab prefix; strip it for Edit and preserve the rest exactly
- Lines over %d characters end with “%s” and cannot be copied into an Edit
- Do not re-read a file to verify your own Edit/Write — a failed change errors, and successful results keep your view current
- Image files are recognized but cannot be displayed yet; ask the user to attach the image to a message instead`, maxReadLines, maxLineLength, lineTruncationMarker),
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file to read. Relative paths are resolved from the current session working directory.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "The line number to start reading from (1-based). Only provide if the file is too large to read at once.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "The number of lines to read. Only provide if the file is too large to read at once.",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

// Schema returns the model-facing tool definition for Edit.
func (t *EditTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Edit",
		Description: `Performs exact string replacement in a file.

- Requires a current view: Read first, unless successful Write/Edit already observed the file this session. Re-read after external changes.
- old_string must match the file exactly after stripping Read's line-number prefix (preserve indentation) and must be unique — add surrounding context if not, or set replace_all to change every occurrence. Trailing-whitespace-only mismatches apply automatically; other whitespace slips fail with the actual lines echoed.
- Apply several changes to one file with multiple Edit calls in one message; they run in order.
- Do not Read and Edit the same file in one message.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file to modify. Relative paths are resolved from the current session working directory.",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "The exact text to replace",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "The replacement text (must differ from old_string)",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "Replace every occurrence of old_string (default false)",
				},
			},
			"required": []string{"file_path", "old_string", "new_string"},
		},
	}
}

// Schema returns the model-facing tool definition for Write.
func (t *WriteTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Write",
		Description: `Writes a file to the local filesystem, overwriting any existing content.

- To overwrite, Read the file first unless successful Write/Edit already observed it this session; re-read after external changes.
- Use Edit for every existing-file change; reserve Write for new files or wholesale regeneration.
- Never create documentation or README files unless explicitly requested.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file to write. Relative paths are resolved from the current session working directory.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The content to write to the file",
				},
			},
			"required": []string{"file_path", "content"},
		},
	}
}

// Schema returns the model-facing tool definition for Bash.
func (t *BashTool) Schema() core.ToolSchema {
	return core.ToolSchema{
		Name: "Bash",
		Description: `Executes a bash command and returns its output.

- Commands already run in the session working directory — NEVER prefix with "cd <cwd> &&"; use relative paths inside it. A successful cd updates the session working directory; other shell state (variables, aliases) does not persist between calls.
- Search and discovery run through this tool (rg, find/fd, ls); pipe large output through head/wc. Provably read-only commands run without approval prompts.
- For file contents use the dedicated tools: Read (not cat), Edit (not sed), Write (not echo/redirection).
- No TTY and no stdin — anything awaiting interactive input hangs until timeout. Use non-interactive flags ("git commit -m", "npm init -y", "apt-get -y") or feed input via heredoc.
- Optional timeout in ms (default 120000, max 600000). On Unix, run_in_background detaches the command and its result includes the process-group ID plus exact Bash commands for graceful or forced termination.`,
		Definition: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The command to execute",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Clear, concise description of what this command does in active voice",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Optional timeout in milliseconds (max 600000)",
				},
				"run_in_background": map[string]any{
					"type":        "boolean",
					"description": "Set to true to run this command in the background. You will be notified when it completes.",
				},
			},
			"required": []string{"command"},
		},
	}
}
