package conv

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
)

func TestAgentEnvelopeSummary(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		isEnv   bool
	}{
		{
			"task notification uses description and status",
			`<task-notification task-id="t1" status="completed" agent-id="a1" description="README复核: 交叉核查 README" output-file="/tmp/t1.log">` + "\n## report\n</task-notification>",
			"README复核: 交叉核查 README completed",
			true,
		},
		{
			"relayed agent message uses sender",
			"<agent-message from=\"Backend\">\nan interim note\n</agent-message>",
			"Message from Backend",
			true,
		},
		{
			"merged batch gets a generic label",
			"<agent-messages>\n<task-notification status=\"completed\"></task-notification>\n</agent-messages>",
			"Messages from background agents",
			true,
		},
		{
			"task notification without attributes falls back",
			"<task-notification>\nbody\n</task-notification>",
			"Background agent message",
			true,
		},
		{
			"ordinary user text is not an envelope",
			"please review the <task-notification> format in the docs",
			"",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAgentEnvelope(tt.content); got != tt.isEnv {
				t.Fatalf("isAgentEnvelope = %v, want %v", got, tt.isEnv)
			}
			if !tt.isEnv {
				return
			}
			if got := agentEnvelopeSummary(tt.content); got != tt.want {
				t.Fatalf("agentEnvelopeSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

// A resumed session rebuilds the injected notification as a user turn; the
// renderer must collapse it to the one-line notice, never dump the raw XML.
func TestRenderMessageCollapsesResumedAgentEnvelope(t *testing.T) {
	content := `<task-notification task-id="t1" status="completed" description="README review">` + "\n## full report body\n</task-notification>"
	out := stripANSI(RenderMessageAt(RenderContext{
		Width:    80,
		Messages: []core.ChatMessage{{Role: core.ChatUser, Content: content}},
	}, 0, false))

	if strings.Contains(out, "<task-notification") || strings.Contains(out, "full report body") {
		t.Fatalf("resumed envelope must not render raw XML, got %q", out)
	}
	if !strings.Contains(out, "README review completed") {
		t.Fatalf("collapsed envelope should show description + status, got %q", out)
	}
}
