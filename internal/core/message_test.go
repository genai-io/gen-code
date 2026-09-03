package core

import (
	"strings"
	"testing"
)

// The counting path must match the materialized conversation text.
func TestConversationTextLenMatchesBuild(t *testing.T) {
	long := strings.Repeat("x", 800)
	msgs := []Message{
		UserMessage("hello <system-reminder>keep me</system-reminder>", nil),
		AssistantMessage("reasoned reply", "", []ToolCall{
			{ID: "1", Name: "Bash"},
			{ID: "2", Name: "Bash"},
			{ID: "3", Name: "Read"},
		}),
		ToolResultMessage(ToolResult{ToolCallID: "1", ToolName: "Bash", Content: "ok"}),
		ToolResultMessage(ToolResult{ToolCallID: "2", ToolName: "Bash", Content: long}),
		AssistantMessage("final answer", "", nil),
	}

	if got, want := conversationTextLen(msgs), len(BuildConversationText(msgs)); got != want {
		t.Fatalf("conversationTextLen() = %d, want len(BuildConversationText()) = %d", got, want)
	}
	if got, want := conversationTextLen(nil), len(BuildConversationText(nil)); got != want {
		t.Fatalf("conversationTextLen(nil) = %d, want %d", got, want)
	}
}

func TestBuildConversationTextAggregatesToolCalls(t *testing.T) {
	text := BuildConversationText([]Message{
		AssistantMessage("", "", []ToolCall{
			{ID: "1", Name: "Bash"},
			{ID: "2", Name: "Bash"},
			{ID: "3", Name: "Glob"},
		}),
	})

	if !strings.Contains(text, "[Tool Calls: Bash × 2, Glob]") {
		t.Fatalf("BuildConversationText() = %q, want aggregated tool calls", text)
	}
	if strings.Count(text, "[Tool Call: Bash]") > 0 {
		t.Fatalf("BuildConversationText() = %q, should not emit repeated raw tool-call lines", text)
	}
}

func TestBuildCompactionTextStripsSystemReminders(t *testing.T) {
	content := "Fix the login bug\n\n" +
		`<system-reminder source="memory-project">` + "\nproject memory\n</system-reminder>" +
		"\n\n<system-reminder>\none-time notice\n</system-reminder>"
	text := BuildCompactionText([]Message{UserMessage(content, nil)})

	if !strings.Contains(text, "User: Fix the login bug") {
		t.Fatalf("BuildCompactionText() = %q, want the real user prompt", text)
	}
	if strings.Contains(text, "system-reminder") || strings.Contains(text, "project memory") || strings.Contains(text, "one-time notice") {
		t.Fatalf("BuildCompactionText() = %q, should strip system-reminder blocks", text)
	}
}

func TestBuildCompactionTextDropsReminderOnlyMessage(t *testing.T) {
	content := "<system-reminder source=\"skills-directory\">\nuse the Skill tool\n</system-reminder>"
	text := BuildCompactionText([]Message{UserMessage(content, nil)})

	if strings.Contains(text, "User:") {
		t.Fatalf("BuildCompactionText() = %q, reminder-only message should not emit a User line", text)
	}
}

// BuildConversationText (the estimator's input) must keep reminders so the
// proactive-compaction size estimate reflects what is actually sent.
func TestBuildConversationTextKeepsSystemReminders(t *testing.T) {
	content := "Fix the login bug\n\n<system-reminder source=\"skills-directory\">\nuse the Skill tool\n</system-reminder>"
	text := BuildConversationText([]Message{UserMessage(content, nil)})

	if !strings.Contains(text, "use the Skill tool") {
		t.Fatalf("BuildConversationText() = %q, should retain reminder content for size estimation", text)
	}
}

// A <system-reminder> the user typed/pasted mid-message must survive; only the
// trailing harness-appended run is stripped.
func TestBuildCompactionTextPreservesMidMessageReminderMention(t *testing.T) {
	content := "explain <system-reminder>X</system-reminder> please\n\n" +
		"<system-reminder source=\"skills-directory\">\nreal\n</system-reminder>"
	text := BuildCompactionText([]Message{UserMessage(content, nil)})

	if !strings.Contains(text, "explain <system-reminder>X</system-reminder> please") {
		t.Fatalf("BuildCompactionText() = %q, should preserve a mid-message reminder mention", text)
	}
	if strings.Contains(text, "\nreal\n") {
		t.Fatalf("BuildCompactionText() = %q, should still strip the trailing reminder", text)
	}
}

// A reminder body that itself contains the literal "</system-reminder>" must
// be removed in full, not truncated at the inner close tag.
func TestBuildCompactionTextStripsReminderWithEmbeddedCloseTag(t *testing.T) {
	content := "fix it\n\n<system-reminder source=\"memory-project\">\n" +
		"<memory scope=\"project\">\nnote: the </system-reminder> tag is special\n</memory>\n" +
		"</system-reminder>"
	text := BuildCompactionText([]Message{UserMessage(content, nil)})

	if strings.TrimSpace(text) != "Please summarize this coding conversation:\n\nUser: fix it" {
		t.Fatalf("BuildCompactionText() = %q, should drop the whole reminder despite embedded close tag", text)
	}
}
