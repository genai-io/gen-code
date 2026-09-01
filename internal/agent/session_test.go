package agent

import (
	"github.com/genai-io/sdk-go/pkg/ai"
	"iter"

	"context"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
)

// stubProvider is a minimal llm.Provider that ends every turn immediately, so a
// Session can start without a real backend.
type stubProvider struct{}

func (p stubProvider) Client(string, map[string]string) (*ai.Client, error) {
	return ai.NewClientWithDriver(p, ai.Model{ID: "stub", API: "stub"}), nil
}

func (stubProvider) Stream(context.Context, *ai.Request) iter.Seq2[ai.Delta, error] {
	return func(yield func(ai.Delta, error) bool) {
		yield(ai.Delta{StopReason: ai.StopEndTurn}, nil)
	}
}
func (stubProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (stubProvider) Name() string                                        { return "stub" }

// A mid-conversation rebuild reseeds the replacement agent from the outgoing
// one's chain (see ensureAgentSession), so Session must surface that chain with
// message IDs intact — otherwise the rebuilt agent loses the conversation.
func TestSessionMessagesReturnsLiveChainWithIDs(t *testing.T) {
	sess := &Session{}
	seed := []core.Message{
		{ID: "u1", Role: core.RoleUser, Content: "survey internal/broker"},
		{ID: "a1", Role: core.RoleAssistant, Content: "here is what I found"},
	}
	if err := sess.Start(BuildParams{Provider: stubProvider{}, ModelID: "m"}, seed); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Stop()

	got := sess.Messages()
	if len(got) != 2 || got[0].ID != "u1" || got[1].ID != "a1" {
		t.Fatalf("Messages() = %+v, want the seeded chain with ids preserved", got)
	}
}

// A cold start has no live agent, so Messages() reports nothing and
// ensureAgentSession falls back to the UI conversation for its seed.
func TestSessionMessagesNilWhenInactive(t *testing.T) {
	if got := (&Session{}).Messages(); got != nil {
		t.Fatalf("Messages() on an inactive session = %+v, want nil", got)
	}
}
