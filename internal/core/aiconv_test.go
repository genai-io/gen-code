package core

import (
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai"
)

// What this conversion owes the SDK is everything the model left behind: the
// per-endpoint decision about which of it can go back moved to ai.Model, which
// drops or trims it on the way out. So what is checked here is that nothing is
// lost on the way in — the test that used to sit here, a table of protocols
// and their answers, now lives once in the SDK instead of once per application.
func TestAModelsOwnStateSurvivesTheConversion(t *testing.T) {
	msgs := []Message{
		{
			Role: ai.RoleAssistant, Content: "done",
			Thinking: "weighing it", ThinkingSignature: "sig-1",
			Reasoning: []ReasoningItem{{ID: "r1", EncryptedContent: "opaque"}},
			ToolCalls: []ToolCall{{ID: "c1", Name: "Read", Input: "{}"}},
		},
	}

	out := ToAIMessages(msgs)
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
