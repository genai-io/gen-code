package session

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
)

// P1 regression: MessagesFromChat must preserve the ChatMessage.ID across
// successive calls. Without this, every save assigns a fresh UUID and the
// append-only persistence path duplicates the entire history each turn.
func Test_MessagesFromChat_preservesChatMessageID(t *testing.T) {
	msgs := []core.ChatMessage{
		{ID: "fixed-1", Role: core.ChatUser, Content: "hello"},
		{ID: "fixed-2", Role: core.ChatAssistant, Content: "hi"},
	}

	first := MessagesFromChat(msgs)
	second := MessagesFromChat(msgs)

	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("expected 2 messages each call, got first=%d second=%d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != msgs[i].ID {
			t.Errorf("message[%d] first call: ID=%q want %q", i, first[i].ID, msgs[i].ID)
		}
		if second[i].ID != msgs[i].ID {
			t.Errorf("message[%d] second call: ID=%q want %q", i, second[i].ID, msgs[i].ID)
		}
	}
}

// A message without an ID still gets a fresh one when projected onto a
// transcript node (back-compat for any path that builds a message without
// going through conv.Append).
func Test_messagesToNodes_fallsBackWhenIDMissing(t *testing.T) {
	msgs := []core.Message{{Role: ai.RoleUser, Content: "hello"}}
	nodes := messagesToNodes(msgs, "/cwd", time.Time{}, "main")
	if len(nodes) != 1 || nodes[0].ID == "" {
		t.Fatalf("expected fallback node ID, got %+v", nodes)
	}
}
