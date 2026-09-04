package core

import (
	"strings"
	"testing"
)

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
