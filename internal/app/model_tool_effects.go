// Side effects triggered by tool calls: cwd-changing tools (Bash, worktree),
// file-touching tools (Write/Edit/Read), and large-output persistence
// (oversized ToolResult.Content gets paged out to a blob and replaced with a
// preview + reference). Background tasks are not tracked from here — they are
// tracked from the task manager's lifecycle notifications (wireTaskLifecycle),
// which fire early enough that a task cannot finish before its entry exists.
package app

import (
	"fmt"
	"unicode/utf8"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/sdk-go/pkg/ai"
)

func (m *model) applyToolSideEffects(toolName string, sideEffect any) {
	resp, ok := sideEffect.(map[string]any)
	if !ok {
		return
	}
	switch toolName {
	case "Bash":
		if newCwd := kit.MapString(resp, "cwd"); newCwd != "" {
			m.changeCwd(newCwd)
		}
	case "Write", "Edit":
		if filePath := kit.MapString(resp, "filePath"); filePath != "" {
			m.fireFileChanged(filePath, toolName)
			m.reloadPersonasIfChanged(filePath)
			if m.env.FileCache != nil {
				m.env.FileCache.Touch(filePath)
			}
		}
	case "Read":
		if fileData, ok := resp["file"].(map[string]any); ok {
			if filePath := kit.MapString(fileData, "filePath"); filePath != "" && m.env.FileCache != nil {
				m.env.FileCache.Touch(filePath)
			}
		}
	}
}

func (m *model) persistOverflow(result *core.ToolResult) {
	const overflowThreshold = 100_000
	const previewSize = 10_000

	// Paging applies to what a tool wrote, which is text. A picture a tool
	// returned is bounded by what the protocol accepts and is not what makes a
	// result outgrow the window.
	full := result.Content.Text()
	if len(full) <= overflowThreshold {
		return
	}
	cutoff := min(previewSize, len(full))
	for cutoff > 0 && !utf8.RuneStart(full[cutoff]) {
		cutoff--
	}
	preview := full[:cutoff]
	persisted := false
	if err := m.services.Session.EnsureStore(m.env.CWD); err == nil && m.services.Session.ID() != "" {
		if err := m.services.Session.GetStore().PersistToolResult(m.services.Session.ID(), result.ToolCallID, full); err == nil {
			persisted = true
		}
	}
	if persisted {
		result.Content = ai.TextContent(fmt.Sprintf("%s\n\n[Full output persisted to blobs/tool-result/%s/%s]",
			preview, m.services.Session.ID(), result.ToolCallID))
	} else {
		result.Content = ai.TextContent(fmt.Sprintf("%s\n\n[Output truncated from %d bytes — full content not persisted]",
			preview, len(full)))
	}
}
