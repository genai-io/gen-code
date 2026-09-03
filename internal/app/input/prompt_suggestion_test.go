package input

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"testing"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/core"
)

// The next-input hint runs an inference on the active model, so it is one more
// way the conversation's images reach the provider — and the conversation keeps
// images even when the model is text-only and rejects them.
func TestRecentSuggestionMessagesCarryNoImages(t *testing.T) {
	c := &conv.ConversationModel{
		Messages: []core.ChatMessage{{
			Role:    core.ChatUser,
			Content: "what does this show",
			Images:  []core.Attachment{{Image: ai.Image{MediaType: "image/png", Data: "ZmFrZQ==", FileName: "chart.png"}}},
		}},
	}

	msgs := RecentSuggestionMessages(c)

	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want the one user turn", len(msgs))
	}
	if imageCount(msgs[0]) != 0 {
		t.Errorf("hint request carries %d image(s), want none", imageCount(msgs[0]))
	}
	if len(c.Messages[0].Images) != 1 {
		t.Error("stripping mutated the conversation's own copy")
	}
}

func imageCount(m core.Message) int {
	n := 0
	for _, b := range m.Content {
		if b.Type == ai.BlockImage {
			n++
		}
	}
	return n
}
