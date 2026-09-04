package session

import (
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/session/transcript"
)

// messageIDs is the active chain in send order — what the next inference
// referenced, so replay can check it referenced what the transcript holds.
func messageIDs(msgs []core.Message) []string {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

// toolViews is the toolset as this transcript stores it, which is also what it
// is digested as.
func toolViews(schemas []core.ToolSchema) []transcript.ToolSchemaView {
	if len(schemas) == 0 {
		return nil
	}
	out := make([]transcript.ToolSchemaView, 0, len(schemas))
	for _, s := range schemas {
		out = append(out, *toolSchemaView(s))
	}
	return out
}
