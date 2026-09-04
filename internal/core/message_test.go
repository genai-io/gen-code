package core

import (
	"strings"
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai"
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

// What this conversion owes the SDK is everything the model left behind: the
// per-endpoint decision about which of it can go back moved to ai.Model, which
// drops or trims it on the way out. So what is checked here is that nothing is
// lost on the way in — the test that used to sit here, a table of protocols
// and their answers, now lives once in the SDK instead of once per application.
func TestAModelsOwnStateSurvivesTheConversion(t *testing.T) {
	chat := ChatMessage{
		Role: ChatAssistant, Content: "done",
		Thinking: "weighing it", ThinkingSignature: "sig-1",
		Reasoning: []ReasoningItem{{ID: "r1", EncryptedContent: "opaque"}},
		ToolCalls: []ToolCall{{ID: "c1", Name: "Read", Input: "{}"}},
	}

	msg, ok := chat.ToMessage()
	if !ok {
		t.Fatal("an assistant row converted to nothing")
	}
	out := []Message{msg}
	if len(out) != 1 {
		t.Fatalf("converted to %d messages, want 1", len(out))
	}

	// Replay order: reasoning first — Anthropic rejects a thinking block that
	// does not lead, and a Responses call whose reasoning item does not
	// precede it — then the answer, then the calls.
	var kinds []ai.BlockType
	for _, b := range out[0].Content {
		kinds = append(kinds, b.Type)
	}
	want := []ai.BlockType{ai.BlockThinking, ai.BlockReasoning, ai.BlockText, ai.BlockToolCall}
	if len(kinds) != len(want) {
		t.Fatalf("blocks = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("blocks = %v, want %v", kinds, want)
		}
	}

	// And the signature travels with the thinking it proves. Whether the
	// endpoint being asked can take it is ai.Model's to answer, not this.
	if got := out[0].Content[0].Signature; got != "sig-1" {
		t.Errorf("signature = %q, want it carried through", got)
	}
}
