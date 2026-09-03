package compact_test

import (
	"context"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/tests/integration/testutil"
)

func newFakeClient(responses ...llm.CompletionResponse) (*llm.Client, *testutil.FakeProvider) {
	fake := &testutil.FakeProvider{Responses: responses}
	return testutil.NewTestClient(fake), fake
}

func TestCompact_SummarizesConversation(t *testing.T) {
	c, _ := newFakeClient(
		llm.CompletionResponse{Content: "Summary: discussed file reading", StopReason: "end_turn"},
	)

	msgs := []core.Message{
		core.UserMessage("read the file", nil),
		core.AssistantMessage("I'll read the file for you", "", nil),
		core.UserMessage("thanks", nil),
		core.AssistantMessage("you're welcome", "", nil),
	}

	summary, count, err := conv.CompactConversation(context.Background(), c, msgs, "")
	if err != nil {
		t.Fatalf("CompactConversation() error: %v", err)
	}
	if count != 4 {
		t.Errorf("expected count 4, got %d", count)
	}
	if summary != "Summary: discussed file reading" {
		t.Errorf("unexpected summary: %q", summary)
	}
}

func TestCompact_WithFocus(t *testing.T) {
	c, fake := newFakeClient(
		llm.CompletionResponse{Content: "Focused summary on testing", StopReason: "end_turn"},
	)

	msgs := []core.Message{
		core.UserMessage("write tests", nil),
		core.AssistantMessage("ok", "", nil),
	}

	_, _, err := conv.CompactConversation(context.Background(), c, msgs, "testing")
	if err != nil {
		t.Fatalf("CompactConversation() error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if !strings.Contains(fake.Calls[0].Messages[0].Text(), "testing") {
		t.Error("expected focus string 'testing' in sent messages")
	}
}

func TestCompact_EmptyConversation(t *testing.T) {
	c, _ := newFakeClient(
		llm.CompletionResponse{Content: "Empty summary", StopReason: "end_turn"},
	)

	summary, count, err := conv.CompactConversation(context.Background(), c, nil, "")
	if err != nil {
		t.Fatalf("CompactConversation() error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
	if summary == "" {
		t.Error("expected non-empty summary even for empty conversation")
	}
}

func TestCompact_WithoutOptionalSections_LeavesPromptPlain(t *testing.T) {
	c, fake := newFakeClient(
		llm.CompletionResponse{Content: "Plain summary", StopReason: "end_turn"},
	)

	msgs := []core.Message{
		core.UserMessage("inspect session state", nil),
		core.AssistantMessage("checking now", "", nil),
	}

	_, _, err := conv.CompactConversation(context.Background(), c, msgs, "")
	if err != nil {
		t.Fatalf("CompactConversation() error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}

	sent := fake.Calls[0].Messages[0].Content.Text()
	if strings.Contains(sent, "**Important**: Focus the summary on:") {
		t.Fatal("did not expect focus directive without focus override")
	}
	if !strings.Contains(sent, "User: inspect session state") {
		t.Fatal("expected user conversation content in compact prompt")
	}
	if !strings.Contains(sent, "Assistant: checking now") {
		t.Fatal("expected raw conversation content in compact prompt")
	}
}

func TestNeedsCompaction(t *testing.T) {
	tests := []struct {
		name   string
		input  int
		limit  int
		expect bool
	}{
		{"zero limit", 100, 0, false},
		{"zero tokens", 0, 1000, false},
		{"well below", 500, 1000, false},
		{"at 89%", 890, 1000, false},
		{"at 90%", 900, 1000, true},
		{"at 95%", 950, 1000, true},
		{"at 100%", 1000, 1000, true},
		{"over limit", 1100, 1000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := core.NeedsCompaction(tt.input, tt.limit)
			if got != tt.expect {
				t.Errorf("NeedsCompaction(%d, %d) = %v, want %v",
					tt.input, tt.limit, got, tt.expect)
			}
		})
	}
}
