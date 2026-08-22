package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/genai-io/san/internal/core"
)

func TestFakeLLMAnswersInOrder(t *testing.T) {
	fake := &FakeLLM{
		Responses: []CompletionResponse{
			{Content: "response 1", StopReason: "end_turn"},
			{Content: "response 2", StopReason: "end_turn"},
		},
	}

	for _, want := range []string{"response 1", "response 2"} {
		resp, err := Complete(context.Background(), fake, CompletionOptions{})
		if err != nil {
			t.Fatalf("Complete() error: %v", err)
		}
		if resp.Content != want {
			t.Errorf("Content = %q, want %q", resp.Content, want)
		}
	}

	// An exhausted queue answers rather than blocking, so a test that
	// under-primes it fails on its assertion instead of on a timeout.
	resp, err := Complete(context.Background(), fake, CompletionOptions{})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Content != "no more responses" {
		t.Errorf("exhausted queue answered %q", resp.Content)
	}
}

func TestFakeLLMCarriesToolCalls(t *testing.T) {
	fake := &FakeLLM{
		Responses: []CompletionResponse{{
			StopReason: "tool_use",
			ToolCalls:  []core.ToolCall{{ID: "call_1", Name: "Read", Input: `{"path":"/tmp/x"}`}},
		}},
	}

	resp, err := Complete(context.Background(), fake, CompletionOptions{})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "Read" || resp.StopReason != "tool_use" {
		t.Errorf("tool call did not survive: %+v", resp)
	}
}

// Calls is what a test asserts the request on, so it has to record the request
// as the client actually built it.
func TestFakeLLMRecordsWhatItWasSent(t *testing.T) {
	fake := &FakeLLM{Responses: []CompletionResponse{{Content: "ok"}}}
	opts := CompletionOptions{
		Model:        "fake-model",
		Messages:     []core.Message{{Role: core.RoleUser, Content: "hello"}},
		Tools:        []ToolSchema{{Name: "Read", Description: "read files"}},
		SystemPrompt: "sys prompt",
	}

	if _, err := Complete(context.Background(), fake, opts); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("Calls = %d, want 1", len(fake.Calls))
	}
	got := fake.Calls[0]
	if got.SystemPrompt != "sys prompt" || got.Model != "fake-model" {
		t.Errorf("request not recorded: %+v", got)
	}
	if len(got.Messages) != 1 || len(got.Tools) != 1 {
		t.Errorf("messages/tools not recorded: %+v", got)
	}
}

// Error injection is how a test drives the failure branch of a turn.
func TestFakeLLMInjectsAnErrorOnTheNthCall(t *testing.T) {
	boom := errors.New("boom")
	fake := &FakeLLM{
		Responses:  []CompletionResponse{{Content: "first"}, {Content: "third"}},
		ErrorAt:    2,
		ErrorValue: boom,
	}

	if _, err := Complete(context.Background(), fake, CompletionOptions{}); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if _, err := Complete(context.Background(), fake, CompletionOptions{}); !errors.Is(err, boom) {
		t.Fatalf("second call error = %v, want boom", err)
	}
	if resp, err := Complete(context.Background(), fake, CompletionOptions{}); err != nil || resp.Content != "third" {
		t.Fatalf("third call = (%q, %v), want (third, nil)", resp.Content, err)
	}
}

func TestFakeLLMName(t *testing.T) {
	if name := (&FakeLLM{}).Name(); name != "fake" {
		t.Errorf("Name() = %q, want fake", name)
	}
	if name := (&FakeLLM{ProviderName: "custom"}).Name(); name != "custom" {
		t.Errorf("Name() = %q, want custom", name)
	}
}
