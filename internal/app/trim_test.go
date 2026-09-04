package app

import (
	"context"
	"strings"
	"testing"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// A result the window can hold is passed through untouched: nil means "leave
// it alone", and returning a copy instead would rewrite every tool call for
// nothing.
func TestAResultThatFitsIsLeftAlone(t *testing.T) {
	m := &model{}
	out, err := m.filterOversizedResult(context.Background(), sdkagent.PostToolContext{
		Result: sdkagent.Result{Content: ai.TextContent(strings.Repeat("x", 1000))},
	})
	if err != nil || out != nil {
		t.Fatalf("trimOverflow = (%v, %v), want (nil, nil)", out, err)
	}
}

// One too large is replaced, and the replacement is what the model is told —
// the guard used to edit the copy the interface draws, which stopped reaching
// the model once the agent held its own conversation.
func TestAResultTooLargeIsReplacedForTheModel(t *testing.T) {
	m := &model{}
	full := strings.Repeat("y", 200_000)
	out, err := m.filterOversizedResult(context.Background(), sdkagent.PostToolContext{
		Call:   ai.ToolCall{ID: "tc-1"},
		Result: sdkagent.Result{Content: ai.TextContent(full)},
	})
	if err != nil {
		t.Fatalf("trimOverflow: %v", err)
	}
	if out == nil {
		t.Fatal("a 200KB result was sent to the model whole")
	}
	got := out.Content.Text()
	if len(got) >= len(full) {
		t.Errorf("replacement is %d bytes, no smaller than the %d it replaced", len(got), len(full))
	}
	if !strings.Contains(got, "truncated") && !strings.Contains(got, "persisted") {
		t.Errorf("the model was not told the rest exists: %q", got[max(0, len(got)-120):])
	}
}
