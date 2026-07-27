package app

import (
	"context"
	"testing"
	"time"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
)

// restartStubProvider lets the foreground Session run without contacting a
// backend and exposes the exact message chain received by its next inference.
type restartStubProvider struct {
	requests chan []core.Message
}

func (p *restartStubProvider) Stream(_ context.Context, opts llm.CompletionOptions) <-chan llm.StreamChunk {
	p.requests <- append([]core.Message(nil), opts.Messages...)
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{
		Type: llm.ChunkTypeDone,
		Response: &llm.CompletionResponse{
			Content:    "ok",
			StopReason: core.StopEndTurn,
		},
	}
	close(ch)
	return ch
}

func (*restartStubProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (*restartStubProvider) Name() string                                        { return "restart-stub" }

// A stop may happen before the next call to ensureAgentSession (for example an
// agent toggle or a terminal outbox event). The live core chain must survive
// that gap and win over the UI rendering model when the replacement is seeded.
func TestStopAgentSessionPreservesLiveChainForRestart(t *testing.T) {
	sess := &agent.Session{}
	provider := &restartStubProvider{requests: make(chan []core.Message, 1)}
	inferences := make(chan core.InferenceContext, 1)
	live := []core.Message{
		{ID: "u1", Role: core.RoleUser, Content: "survey internal/broker"},
		{ID: "a1", Role: core.RoleAssistant, Content: "foreground result"},
	}
	if err := sess.Start(agent.BuildParams{Provider: provider, ModelID: "m"}, live); err != nil {
		t.Fatalf("Start: %v", err)
	}

	m := model{
		services: services{Agent: sess},
		conv:     conv.NewModel(80),
	}
	m.StopAgentSession()
	if sess.Active() {
		t.Fatal("Session remained active after StopAgentSession")
	}

	// A background completion is already represented in the UI when
	// SubmitToAgent asks for a replacement. It is sent through the inbox after
	// Start, so it must not displace the preserved seed.
	m.conv.Append(core.ChatMessage{ID: "ui-completion", Role: core.RoleUser, Content: "background result"})
	got := m.seedAgentMessages("background result")
	if len(got) != len(live) {
		t.Fatalf("seedAgentMessages() len = %d, want %d: %+v", len(got), len(live), got)
	}
	for i := range live {
		if got[i].ID != live[i].ID {
			t.Fatalf("seedAgentMessages()[%d].ID = %q, want %q", i, got[i].ID, live[i].ID)
		}
	}

	if err := sess.Start(agent.BuildParams{
		Provider: provider,
		ModelID:  "m",
		OnEvent: func(event core.Event) {
			if inference, ok := event.InferenceContext(); ok {
				inferences <- inference
			}
		},
	}, got); err != nil {
		t.Fatalf("restart Session: %v", err)
	}
	defer sess.Stop()
	restarted := sess.Messages()
	if len(restarted) != len(live) {
		t.Fatalf("restarted Messages() len = %d, want %d: %+v", len(restarted), len(live), restarted)
	}
	for i := range live {
		if restarted[i].ID != live[i].ID {
			t.Fatalf("restarted Messages()[%d].ID = %q, want %q", i, restarted[i].ID, live[i].ID)
		}
	}

	sess.Send("background result", nil)
	select {
	case request := <-provider.requests:
		if len(request) != 3 {
			t.Fatalf("inference message count = %d, want 3: %+v", len(request), request)
		}
		if request[0].Content != live[0].Content || request[1].Content != live[1].Content {
			t.Fatalf("inference lost preserved content: %+v", request)
		}
		if request[2].Role != core.RoleUser || request[2].Content != "background result" {
			t.Fatalf("inference trailing message = %+v, want background result", request[2])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for restarted inference")
	}

	select {
	case inference := <-inferences:
		if len(inference.MessageIDs) != 3 {
			t.Fatalf("PreInfer message IDs = %v, want three IDs", inference.MessageIDs)
		}
		if inference.MessageIDs[0] != "u1" || inference.MessageIDs[1] != "a1" {
			t.Fatalf("PreInfer lost preserved IDs: %v", inference.MessageIDs)
		}
		if inference.MessageIDs[2] == "" {
			t.Fatal("PreInfer did not assign an ID to the background result")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for PreInfer event")
	}
}

// Clearing or replacing a conversation must explicitly discard a retained
// chain; otherwise the next user message would resurrect the old session.
func TestResetAgentSessionDiscardsRestartChain(t *testing.T) {
	m := model{
		services: services{Agent: &agent.Session{}},
		conv:     conv.NewModel(80),
		agentRestartMessages: []core.Message{
			{ID: "old-u1", Role: core.RoleUser, Content: "old session"},
		},
	}
	m.ResetAgentSession()
	m.conv.Append(core.ChatMessage{ID: "new-u1", Role: core.RoleUser, Content: "new session"})

	if got := m.seedAgentMessages("new session"); len(got) != 0 {
		t.Fatalf("seedAgentMessages() after reset = %+v, want no old seed", got)
	}
}

// textOnlyStubProvider declares itself unable to accept image input, the way
// DeepSeek does. Seeding never streams, so Stream is left unimplemented.
type textOnlyStubProvider struct{ restartStubProvider }

func (*textOnlyStubProvider) Name() string               { return "text-only-stub" }
func (*textOnlyStubProvider) SupportsImages(string) bool { return false }

func chainWithImage() []core.Message {
	return []core.Message{
		{
			ID:      "u1",
			Role:    core.RoleUser,
			Content: "what is in this screenshot?",
			Images:  []core.Image{{MediaType: "image/png", Data: "aW1n", FileName: "shot.png", Size: 3}},
		},
		{ID: "a1", Role: core.RoleAssistant, Content: "a terminal"},
	}
}

// A conversation that began on a vision-capable model carries its images in the
// chain. Handing them to a text-only model replays image parts the provider
// rejects on every later turn, so the seed has to drop them — keeping the text.
func TestSeedAgentMessagesDropsImagesForTextOnlyModel(t *testing.T) {
	original := chainWithImage()
	m := model{conv: conv.NewModel(80), agentRestartMessages: chainWithImage()}
	m.env.LLMProvider = &textOnlyStubProvider{}

	seeded := m.seedAgentMessages("")
	if len(seeded) != len(original) {
		t.Fatalf("seedAgentMessages() length = %d, want %d", len(seeded), len(original))
	}
	if len(seeded[0].Images) != 0 {
		t.Errorf("seeded user message kept %d image(s), want none", len(seeded[0].Images))
	}
	if seeded[0].Content != original[0].Content {
		t.Errorf("seeded user message content = %q, want %q", seeded[0].Content, original[0].Content)
	}
	// The retained snapshot must survive intact: switching back to a
	// vision-capable model has to restore the images.
	if len(m.agentRestartMessages[0].Images) != 1 {
		t.Error("stripping mutated the retained restart chain")
	}
}

// Vision-capable providers are the common case and must see the chain untouched.
func TestSeedAgentMessagesKeepsImagesForVisionModel(t *testing.T) {
	m := model{conv: conv.NewModel(80), agentRestartMessages: chainWithImage()}
	m.env.LLMProvider = &restartStubProvider{}

	seeded := m.seedAgentMessages("")
	if len(seeded[0].Images) != 1 {
		t.Errorf("seeded user message has %d image(s), want the original 1", len(seeded[0].Images))
	}
}
