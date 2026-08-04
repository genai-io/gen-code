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
			LLM:      &llm.Conn{},
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

// brokenImagePath writes a broken.png the loader rejects, points the model's
// cwd at it, and returns its absolute path.
func brokenImagePath(t *testing.T, m *model) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "broken.png")
	if err := os.WriteFile(path, []byte("not an image"), 0o644); err != nil {
		t.Fatalf("write broken.png: %v", err)
	}
	m.env.CWD = dir
	return path
}

// A text-only model gets the image's path as text and no attachment: the path
// is something it can act on (an MCP tool can read the file), the attachment is
// what the provider rejects.
func TestAdaptTurnForProviderWithholdsImagesFromTextOnlyModel(t *testing.T) {
	m, _ := textOnlyModel(t)

	content, providerImages := m.adaptTurnForProvider("what does this show", []core.Image{chartImage()})

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

func TestAdaptTurnForProviderLeavesVisionModelsAlone(t *testing.T) {
	m, _ := textOnlyModel(t)
	m.env.LLMProvider = &restartStubProvider{} // no ImageSupportProvider — images supported

	content, providerImages := m.adaptTurnForProvider("what does this show", []core.Image{chartImage()})

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
		sent := false
		for _, msg := range chain {
			if len(msg.Images) > 0 {
				t.Fatalf("provider received %d image part(s) it rejects: %+v", len(msg.Images), msg)
			}
			if strings.Contains(msg.Content, "/tmp/chart.png") {
				sent = true
			}
		}
		if !sent {
			t.Fatalf("provider never saw the image path, so the no-images check proves nothing: %+v", chain)
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

// The queued text keeps the [Image #N] markers the textarea used to position
// its attachments: they belong in the display, not in what the model reads —
// the same split buildUserMessage makes on the direct path. Run on a vision
// model so the only thing separating the two fields is the marker.
func TestReleasedQueuedMessageSplitsDisplayFromContent(t *testing.T) {
	m, _ := textOnlyModel(t)
	m.env.LLMProvider = &restartStubProvider{}
	m.userInput.Queue.Enqueue("look at [Image #1] please", []core.Image{chartImage()})

	if _, released := m.releaseQueuedMessage(); !released {
		t.Fatal("queued message was not released")
	}

	last := m.conv.Messages[len(m.conv.Messages)-1]
	if strings.Contains(last.Content, "[Image #1]") {
		t.Errorf("content = %q, want the marker stripped for the model", last.Content)
	}
	if !strings.Contains(last.DisplayContent, "[Image #1]") {
		t.Errorf("display content = %q, want the marker kept for rendering", last.DisplayContent)
	}
}

// Compaction summarizes on the active model, so it is a third way the
// conversation's images reach the provider — and the conversation now keeps
// images even when the model can't read them.
func TestCompactRequestCarriesNoImagesForTextOnlyModel(t *testing.T) {
	m, _ := textOnlyModel(t)
	m.conv.Append(core.ChatMessage{
		Role:    core.RoleUser,
		Content: "what does this show",
		Images:  []core.Image{chartImage()},
	})

	req := m.BuildCompactRequest("", "manual")

	if len(req.Messages) == 0 {
		t.Fatal("compact request has no messages, so the check below proves nothing")
	}
	for _, msg := range req.Messages {
		if len(msg.Images) > 0 {
			t.Fatalf("compact request carries %d image part(s) the provider rejects: %+v", len(msg.Images), msg)
		}
	}
	// The conversation keeps its own copy — only the request is stripped.
	if len(m.conv.Messages[0].Images) != 1 {
		t.Error("building the request mutated the conversation's images")
	}
}

// Releasing a queued message runs mid-stream, when the textarea holds the next
// message being typed. A queued image that fails to load costs the user that
// image — not both messages.
func TestQueuedImageErrorLeavesTheNextMessageAlone(t *testing.T) {
	m, _ := textOnlyModel(t)
	brokenImagePath(t, m)
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

// A queued message that leads with an image path the loader rejects still has
// to lose the path: the release path can't hand the turn back to the textarea,
// so whatever it sends is final, and a bare path left in the text is the
// unknown-command-looking string this whole change exists to remove.
func TestQueuedLeadingImageFailureStillConsumesThePath(t *testing.T) {
	m, _ := textOnlyModel(t)
	broken := brokenImagePath(t, m)
	m.userInput.Queue.Enqueue(broken+" what is this", nil)

	if _, released := m.releaseQueuedMessage(); !released {
		t.Fatal("queued message was not released")
	}

	last := m.conv.Messages[len(m.conv.Messages)-1]
	if strings.Contains(last.Content, broken) {
		t.Fatalf("last message = %q, want the failed leading path consumed, not sent as text", last.Content)
	}
	if !strings.Contains(last.Content, "what is this") {
		t.Fatalf("last message = %q, want the rest of the prompt to survive", last.Content)
	}
}

// The inlined path is written into content that conv persists and replays, so
// the file behind it has to outlive the turn: a follow-up question reaches a
// model reading that same path back out of its own history. It goes at quit
// instead.
func TestTempImageFileOutlivesTheTurnThatWroteIt(t *testing.T) {
	m, _ := textOnlyModel(t)
	pasted := core.Image{MediaType: "image/png", Data: "ZmFrZQ==", FileName: "clipboard_120000.png"}

	content, providerImages := m.adaptTurnForProvider("what does this show", []core.Image{pasted})
	if len(providerImages) != 0 {
		t.Fatalf("provider images = %+v, want none", providerImages)
	}
	if len(m.tempImageFiles) != 1 {
		t.Fatalf("temp files = %+v, want the pasted image materialized", m.tempImageFiles)
	}
	path := m.tempImageFiles[0]
	if !strings.Contains(content, path) {
		t.Fatalf("content = %q, want the temp path inlined", content)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v — the path is in the persisted content already", path, err)
	}

	m.removeTempImageFiles()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat %s after quit = %v, want it removed", path, err)
	}
}
