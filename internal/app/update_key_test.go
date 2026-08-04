package app

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool/perm"
)

type testThinkingProvider struct {
	efforts []string
	def     string
}

func (p *testThinkingProvider) Stream(context.Context, llm.CompletionOptions) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch
}

func (p *testThinkingProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

func (p *testThinkingProvider) Name() string { return "test" }

func (p *testThinkingProvider) ThinkingEfforts(string) []string {
	out := make([]string, len(p.efforts))
	copy(out, p.efforts)
	return out
}

func (p *testThinkingProvider) DefaultThinkingEffort(string) string { return p.def }

func TestCtrlTCyclesThinkingEffort(t *testing.T) {
	m := &model{}
	m.env.LLMProvider = &testThinkingProvider{
		efforts: []string{"none", "low", "medium", "high"},
		def:     "none",
	}
	m.env.CurrentModel = &llm.CurrentModelInfo{ModelID: "test-model", Provider: llm.OpenAI}

	cmd, handled := m.handleTextareaShortcut(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("Ctrl+T was not handled")
	}
	if cmd != nil {
		t.Fatal("Ctrl+T should update the model label without a status message")
	}
	if m.env.ThinkingEffort != "low" {
		t.Fatalf("ThinkingEffort = %q, want low", m.env.ThinkingEffort)
	}
	if m.userInput.Provider.StatusMessage != "" {
		t.Fatalf("StatusMessage = %q, want empty", m.userInput.Provider.StatusMessage)
	}
	if m.conv.ShowTasks {
		t.Fatal("Ctrl+T should not toggle the task panel")
	}
}

func TestCtrlTUsesCachedModelReasoningMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.CacheModels(llm.OpenAI, llm.AuthSubscription, []llm.ModelInfo{{
		ID:        "dynamic-model",
		Reasoning: llm.NewReasoningCapability([]string{"low", "ultra"}, "low"),
	}}); err != nil {
		t.Fatalf("CacheModels() error = %v", err)
	}

	m := &model{}
	m.env.store = store
	m.env.LLMProvider = &testThinkingProvider{
		efforts: []string{"high"},
		def:     "high",
	}
	m.env.CurrentModel = &llm.CurrentModelInfo{
		ModelID:    "dynamic-model",
		Provider:   llm.OpenAI,
		AuthMethod: llm.AuthSubscription,
	}

	cmd, handled := m.handleTextareaShortcut(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !handled || cmd != nil {
		t.Fatal("Ctrl+T should be handled without a status timer")
	}
	if m.env.ThinkingEffort != "ultra" {
		t.Fatalf("ThinkingEffort = %q, want dynamic next effort ultra", m.env.ThinkingEffort)
	}
}

// A running turn is exactly when the user reaches for a laxer mode — every
// command is stopping for approval — so Shift+Tab must keep cycling mid-stream.
func TestShiftTabCyclesModeDuringStream(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()

	m := &model{}
	m.env.CWD = cwd
	m.env.SessionPermissions = setting.NewSessionPermissions()
	m.services.Setting = setting.New(setting.NewData())
	m.services.Hook = hook.NewEngine(setting.NewData(), "", cwd, "")
	m.conv.Stream.Active = true

	shiftTab := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	if _, handled := m.handleTextareaShortcut(shiftTab); !handled {
		t.Fatal("shift+tab was dropped while a turn was streaming")
	}
	if m.env.OperationMode != setting.ModeAutoAccept {
		t.Fatalf("OperationMode = %v, want %v", m.env.OperationMode, setting.ModeAutoAccept)
	}
	if got := m.env.SessionPermissions.CurrentMode(); got != setting.ModeAutoAccept {
		t.Fatalf("posture mode = %v, want %v (this is what the permission gate reads)", got, setting.ModeAutoAccept)
	}
}

// The approval prompt keeps Shift+Tab as its "allow all this session"
// accelerator: it is an overlay, so routeKeypress hands it the key before the
// mode cycle ever sees it.
func TestShiftTabAtApprovalPromptDoesNotCycleMode(t *testing.T) {
	m := &model{conv: conv.NewModel(80)}
	m.env.SessionPermissions = setting.NewSessionPermissions()
	m.conv.Stream.Active = true
	m.userInput.Approval.Show(&perm.PermissionRequest{ToolName: "Bash"}, 80, 24)

	if _, ok := m.routeKeypress(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}); !ok {
		t.Fatal("shift+tab was not routed to the approval overlay")
	}
	if m.env.OperationMode != setting.ModeNormal {
		t.Fatalf("OperationMode = %v, want %v (the approval prompt owns this key)", m.env.OperationMode, setting.ModeNormal)
	}
}

func TestAltTTogglesTaskPanel(t *testing.T) {
	m := &model{}

	_, handled := m.handleTextareaShortcut(tea.KeyPressMsg{Code: 't', Mod: tea.ModAlt})
	if !handled {
		t.Fatal("Alt+T was not handled")
	}
	if !m.conv.ShowTasks {
		t.Fatal("Alt+T should toggle the task panel")
	}
}
