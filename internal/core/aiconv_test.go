package core

import (
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
)

// Moved here with the function it tests: replayableThinking decides whether a
// model's own thinking may go back to the endpoint it came from, and that
// decision belongs with the conversion.

func TestThinkingIsReplayedOnlyWhereItCanBe(t *testing.T) {
	claudeModel, err := catalog.Model("anthropic/claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	deepseekModel, err := catalog.Model("deepseek/deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	sensenovaModel, err := catalog.Model("sensenova/deepseek-v4-flash")
	if err != nil {
		t.Fatal(err)
	}
	codexModel, err := catalog.Model("openai/gpt-5.6-luna")
	if err != nil {
		t.Fatal(err)
	}

	signed := Message{Role: ai.RoleAssistant, Content: "done", Thinking: "weighing it", ThinkingSignature: "sig-1"}
	unsigned := Message{Role: ai.RoleAssistant, Content: "done", Thinking: "weighing it"}

	cases := []struct {
		name      string
		msg       Message
		model     ai.Model
		want      bool
		signature string
	}{
		{"anthropic keeps its own signed thinking", signed, claudeModel, true, "sig-1"},
		// A session that switched models: the thinking came from somewhere
		// with no signature, and Anthropic rejects one without.
		{"anthropic drops unsigned thinking", unsigned, claudeModel, false, ""},
		// The signature belongs to another protocol and must not travel with it.
		{"chat completions strips a foreign signature", signed, deepseekModel, true, ""},
		{"an endpoint that cannot take it back drops it", signed, sensenovaModel, false, ""},
		// Responses replays reasoning as opaque items instead.
		{"responses drops the readable summary", signed, codexModel, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block, ok := replayableThinking(tc.msg, tc.model)
			if ok != tc.want {
				t.Fatalf("replayed = %v, want %v", ok, tc.want)
			}
			if ok && block.Signature != tc.signature {
				t.Errorf("signature = %q, want %q", block.Signature, tc.signature)
			}
		})
	}

	// The whole conversion agrees with the rule.
	messages := ToAIMessages([]Message{unsigned}, claudeModel)
	if len(messages) != 1 || messages[0].Content.Has(ai.BlockThinking) {
		t.Errorf("unsigned thinking reached an Anthropic request: %+v", messages)
	}
}
