package session

import (
	"testing"
	"time"

	"github.com/genai-io/san/internal/core"
)

// P1 regression, now structural: a saved session holds the ChatMessages
// themselves, so an ID cannot be lost on the way to the transcript. This pins
// the path that still converts — the nodes the append-only writer dedupes by.
func Test_messagesToNodes_preservesChatMessageID(t *testing.T) {
	msgs := []core.ChatMessage{
		{ID: "fixed-1", Role: core.ChatUser, Content: "hello"},
		{ID: "fixed-2", Role: core.ChatAssistant, Content: "hi"},
	}

	first := messagesToNodes(msgs, "/cwd", time.Time{}, "main")
	second := messagesToNodes(msgs, "/cwd", time.Time{}, "main")

	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("expected 2 nodes each call, got first=%d second=%d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != msgs[i].ID || second[i].ID != msgs[i].ID {
			t.Errorf("node[%d] IDs = %q / %q, want %q", i, first[i].ID, second[i].ID, msgs[i].ID)
		}
	}
}

// A message without an ID still gets a fresh one when projected onto a
// transcript node (back-compat for any path that builds a message without
// going through conv.Append).
func Test_messagesToNodes_fallsBackWhenIDMissing(t *testing.T) {
	msgs := []core.ChatMessage{{Role: core.ChatUser, Content: "hello"}}
	nodes := messagesToNodes(msgs, "/cwd", time.Time{}, "main")
	if len(nodes) != 1 || nodes[0].ID == "" {
		t.Fatalf("expected fallback node ID, got %+v", nodes)
	}
}
