// Methods on *model that exist for sub-features (input overlay, prompt
// suggestion, trigger) to consume. Most build the Deps struct each
// sub-feature declares; a few expose model state (spinner tick, cron
// queue reset) or actions (external editor) the sub-features need.
// Centralized here so update.go / model.go stay focused on the main loop.
package app

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"strings"

	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/app/trigger"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
)

// suggestTask is the Suggest steer's per-call instruction — the mission-driven
// input hint. Like every LLM steer it rides on the shared autopilotSystemPrompt
// persona, so only the task and its output format live here.
const suggestTask = `Suggest the SINGLE next instruction to give the agent toward the mission — a short, direct imperative the user can accept and send as-is.
Reply with ONLY that instruction (a few words to one sentence): no quotes, no explanation. Reply with nothing if the mission looks complete or the next step needs a human decision.`

// startPromptSuggestion fires the input hint. AutoPilot owns the whole decision
// here, reading its live config directly so the hint holds no autopilot state:
// the Suggest steer is the feature switch — on by default. With it off, the
// hint never runs, in or out of AutoPilot mode. With it on, a mission driven
// under AutoPilot proposes the next step toward it; otherwise it falls back to
// predicting the human's next input.
func (m *model) startPromptSuggestion() tea.Cmd {
	if m.env.LLMProvider == nil {
		return nil
	}
	if !m.env.AutoPilot.Steers.SuggestOn() {
		return nil
	}
	if mission := m.autopilotSuggestMission(); mission != "" {
		return m.userInput.PromptSuggestion.Start(m.missionSuggestionRequest(mission))
	}
	return input.StartPromptSuggestion(m.promptSuggestionDeps())
}

// missionSuggestionRequest builds the Suggest-steer hint: the recent session
// plus the mission, asking for the single next instruction to give the agent.
func (m *model) missionSuggestionRequest(mission string) input.PromptSuggestionRequest {
	msgs := input.RecentSuggestionMessages(&m.conv.ConversationModel)
	msgs = append(msgs, core.Message{Role: ai.RoleUser, Content: suggestTask + "\n\nMission:\n" + mission})
	return input.PromptSuggestionRequest{
		Client:       m.buildLLMClient(),
		Messages:     msgs,
		SystemPrompt: m.autopilotSystemPrompt(),
		MaxTokens:    60,
	}
}

func (m *model) promptSuggestionDeps() input.PromptSuggestionDeps {
	return input.PromptSuggestionDeps{
		Input:        &m.userInput,
		Conversation: &m.conv.ConversationModel,
		HasProvider:  m.env.LLMProvider != nil,
		BuildClient:  m.buildLLMClient,
	}
}

// autopilotSuggestMission returns the mission the Suggest steer should propose
// the next step toward — the live autopilot mission when the steer is on, else
// "". Read fresh from env every call, so clearing the mission (End completion /
// ctrl+r / panel) takes effect immediately.
func (m *model) autopilotSuggestMission() string {
	if !m.autopilotEngaged() || !m.env.AutoPilot.Steers.SuggestOn() {
		return ""
	}
	return strings.TrimSpace(m.env.AutoPilot.Mission)
}

func (m *model) overlayDeps() input.OverlayDeps {
	reloadModelStore := func() {
		if store := m.services.LLM.Store(); store != nil {
			if err := store.Reload(); err != nil {
				log.Logger().Warn("reload provider store after model catalog update", zap.Error(err))
			}
		}
	}
	return input.OverlayDeps{
		State:             &m.userInput,
		Conv:              &m.conv.ConversationModel,
		Cwd:               m.env.CWD,
		CommitMessages:    m.CommitMessages,
		CommitAllMessages: m.commitAllMessages,
		SwitchProvider: func(p llm.Provider) {
			m.StopAgentSession()
			m.switchProvider(p)
			m.ReconfigureAgentTool()
		},
		SetCurrentModel: func(info *llm.CurrentModelInfo) {
			m.env.CurrentModel = info
			llm.Default().SetCurrentModel(info)
			// The selector cached the model's metadata (display name, token
			// limits) through its own Store; reload the shared store so the
			// status bar reflects the new model's name and context-window
			// limit instead of the raw ID and "--".
			reloadModelStore()
			m.env.LoadThinkingEffortFromStore()
		},
		ReloadModelStore: reloadModelStore,
		// No welcome reprint on model switch: the live status line already
		// shows the current model, and the startup brand line is a one-time
		// splash (re-emitting it duplicated the banner / leaked blank lines).
		ClearCachedInstructions: m.env.ClearCachedInstructions,
		RefreshMemoryContext:    m.refreshMemoryContext,
		FireFileChanged:         m.fireFileChanged,
		ReloadAfterPluginChange: m.ReloadAfterPluginChange,
		LoadSession:             m.loadSessionByID,
		SetActivePersona:        m.setActivePersona,
		OpenPersona:             m.openPersona,
		DeletePersona:           m.deletePersona,
	}
}

func (m *model) triggerDeps() trigger.Deps {
	return trigger.Deps{
		StreamActive: m.conv.Stream.Active,
		Cron:         m.services.Cron,
		InjectCron:   m.injectCronPrompt,
		InjectHook:   m.injectAsyncHookContinuation,
		AppendNotice: func(text string) {
			if text != "" {
				m.conv.Append(core.ChatMessage{Role: core.ChatNotice, Content: text})
			}
		},
	}
}

func (m *model) StartExternalEditor(path string) tea.Cmd {
	return kit.StartExternalEditor(path, func(err error) tea.Msg {
		return input.MemoryEditorFinishedMsg{Err: err}
	})
}

func (m *model) SpinnerTickCmd() tea.Cmd { return m.conv.Spinner.Tick }
func (m *model) ResetCronQueue()         { m.systemInput.CronQueue = nil }
