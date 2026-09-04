// Side effects triggered by tool calls: cwd-changing tools (Bash, worktree),
// file-touching tools (Write/Edit/Read), and large-output persistence
// (oversized ToolResult.Content gets paged out to a blob and replaced with a
// preview + reference). Background tasks are not tracked from here — they are
// tracked from the task manager's lifecycle notifications (wireTaskLifecycle),
// which fire early enough that a task cannot finish before its entry exists.
package app

import (
	"context"
	"fmt"
	"unicode/utf8"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"

	"github.com/genai-io/san/internal/app/kit"
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

// filterOversizedResult keeps a result too large for the window out of the conversation:
// the full text goes to the session store and the model is told a preview plus
// where the rest is.
//
// It is core.ResultFilter — the loop's PostTool — because that is the only seam that
// changes what the model is told. It used to edit the copy the interface draws
// instead, which was the same object back when the chat rows were the
// conversation; once the agent held its own, the guard went on protecting the
// screen from an output that was still reaching the model in full.
func (m *model) filterOversizedResult(_ context.Context, c sdkagent.PostToolContext) (*sdkagent.Result, error) {
	const overflowThreshold = 100_000
	const previewSize = 10_000

	// Paging applies to what a tool wrote, which is text. A picture a tool
	// returned is bounded by what the protocol accepts and is not what makes a
	// result outgrow the window.
	full := c.Result.Content.Text()
	if len(full) <= overflowThreshold {
		return nil, nil
	}
	cutoff := min(previewSize, len(full))
	for cutoff > 0 && !utf8.RuneStart(full[cutoff]) {
		cutoff--
	}
	preview := full[:cutoff]

	// A panic here is not recovered — the SDK says so of every hook — and a
	// session that is not up yet is an ordinary state, not a failure. Without
	// one the model still gets the preview; it just cannot be pointed at the
	// rest.
	out := c.Result
	if sess := m.services.Session; sess != nil && sess.EnsureStore(m.env.CWD) == nil && sess.ID() != "" {
		if err := sess.GetStore().PersistToolResult(sess.ID(), c.Call.ID, full); err == nil {
			out.Content = ai.TextContent(fmt.Sprintf("%s\n\n[Full output persisted to blobs/tool-result/%s/%s]",
				preview, sess.ID(), c.Call.ID))
			return &out, nil
		}
	}
	out.Content = ai.TextContent(fmt.Sprintf("%s\n\n[Output truncated from %d bytes — full content not persisted]",
		preview, len(full)))
	return &out, nil
}
