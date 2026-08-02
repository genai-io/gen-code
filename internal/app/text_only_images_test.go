package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/subagent"
	"github.com/genai-io/san/internal/todo"
)

// textOnlyModel returns a model whose session is live on a text-only provider,
// with a queue ready to accept items.
func textOnlyModel(t *testing.T) (*model, *textOnlyStubProvider) {
	t.Helper()

	provider := &textOnlyStubProvider{restartStubProvider{requests: make(chan []core.Message, 2)}}
	sess := &agent.Session{}
	if err := sess.Start(agent.BuildParams{Provider: provider, ModelID: "deepseek-chat"}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(sess.Stop)

	m := &model{
		services: services{
			Agent:    sess,
			Tracker:  todo.NewStore(),
			Subagent: subagent.NewRegistry(),
			Reminder: reminder.NewService(),
		},
		conv: conv.NewModel(80),
	}
	m.env.LLMProvider = provider
	m.env.CurrentModel = &llm.CurrentModelInfo{ModelID: "deepseek-chat"}
	m.userInput.Queue = input.NewQueue()
	return m, provider
}

func chartImage() core.Image {
	return core.Image{
		MediaType: "image/png",
		Data:      "ZmFrZQ==",
		FileName:  "chart.png",
		Path:      "/tmp/chart.png",
	}
}

// A text-only model gets the image's path as text and no attachment: the path
// is something it can act on (an MCP tool can read the file), the attachment is
// what the provider rejects.
func TestAdaptImagesForModelWithholdsImagesFromTextOnlyModel(t *testing.T) {
	m, _ := textOnlyModel(t)

	content, providerImages := m.adaptImagesForModel("what does this show", []core.Image{chartImage()})

	if len(providerImages) != 0 {
		t.Fatalf("provider images = %+v, want none — a text-only provider rejects image parts", providerImages)
	}
	if !strings.Contains(content, "[Image #1: /tmp/chart.png]") {
		t.Fatalf("content = %q, want the image path inlined", content)
	}
	if !strings.Contains(content, "what does this show") {
		t.Fatalf("content = %q, want the user's own text kept", content)
	}
}

func TestAdaptImagesForModelLeavesVisionModelsAlone(t *testing.T) {
	m, _ := textOnlyModel(t)
	m.env.LLMProvider = &restartStubProvider{} // no ImageSupportProvider — images supported

	content, providerImages := m.adaptImagesForModel("what does this show", []core.Image{chartImage()})

	if len(providerImages) != 1 {
		t.Fatalf("provider images = %d, want the attachment passed through", len(providerImages))
	}
	if content != "what does this show" {
		t.Fatalf("content = %q, want it untouched", content)
	}
}

// The turn-boundary release is a second send path, and it reaches the provider
// without passing through seedAgentMessages — the session is already active by
// then, so nothing downstream strips the images.
func TestReleasedQueuedImageNeverReachesATextOnlyProvider(t *testing.T) {
	m, provider := textOnlyModel(t)
	m.userInput.Queue.Enqueue("what does this show", []core.Image{chartImage()})

	cmd, released := m.releaseQueuedMessage()
	if !released {
		t.Fatal("queued message was not released")
	}
	runCmd(cmd)

	select {
	case chain := <-provider.requests:
		for _, msg := range chain {
			if len(msg.Images) > 0 {
				t.Fatalf("provider received %d image part(s) it rejects: %+v", len(msg.Images), msg)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued message never reached the provider")
	}

	// The conversation keeps the image: it still renders, and a later switch to
	// a vision-capable model can use it.
	last := m.conv.Messages[len(m.conv.Messages)-1]
	if len(last.Images) != 1 {
		t.Fatalf("conversation message carries %d image(s), want 1: %+v", len(last.Images), last)
	}
}

// Releasing a queued message runs mid-stream, when the textarea holds the next
// message being typed. A queued image that fails to load costs the user that
// image — not both messages.
func TestQueuedImageErrorLeavesTheNextMessageAlone(t *testing.T) {
	m, _ := textOnlyModel(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.png"), []byte("not an image"), 0o644); err != nil {
		t.Fatalf("write broken.png: %v", err)
	}
	m.env.ProjectRoot = dir
	m.userInput.Queue.Enqueue("look at @broken.png", nil)
	// What the user is typing right now, while the turn streams.
	m.userInput.Images.Pending = []input.PendingImage{{ID: 1, Data: core.Image{FileName: "next.png"}}}

	if _, released := m.releaseQueuedMessage(); !released {
		t.Fatal("queued message was not released")
	}

	if len(m.userInput.Images.Pending) != 1 {
		t.Fatalf("pending images = %+v, want the one being typed to survive", m.userInput.Images.Pending)
	}
	last := m.conv.Messages[len(m.conv.Messages)-1]
	if !strings.Contains(last.Content, "look at") {
		t.Fatalf("last message = %q, want the queued message sent anyway", last.Content)
	}
}
