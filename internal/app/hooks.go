// Hook-forwarding model methods and related helpers.
// These methods use m.services.Hook to fire lifecycle events,
// replacing the former env hook methods that used singletons directly.
package app

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/plugin"
)

func (m *model) firePostToolHook(tr core.ToolResult, sideEffect any) {
	eventType := hook.PostToolUse
	if tr.IsError {
		eventType = hook.PostToolUseFailure
	}
	toolResponse := any(tr.Content)
	if sideEffect != nil {
		toolResponse = sideEffect
	}
	input := hook.HookInput{
		ToolName:     tr.ToolName,
		ToolUseID:    tr.ToolCallID,
		ToolResponse: toolResponse,
	}
	if tr.IsError {
		input.Error = tr.Content
	}
	m.services.Hook.ExecuteAsync(eventType, input)
}

func (m *model) fireStopFailureHook(lastAssistantContent string, err error) {
	m.services.Hook.ExecuteAsync(hook.StopFailure, hook.HookInput{
		LastAssistantMessage: lastAssistantContent,
		Error:                err.Error(),
		StopHookActive:       m.services.Hook.StopHookActive(),
	})
}

func (m *model) executeStartupHooks(ctx context.Context) hook.HookOutcome {
	m.services.Hook.ExecuteAsync(hook.Setup, hook.HookInput{
		Trigger: "init",
	})
	source := "startup"
	if m.services.Session.ID() != "" {
		source = "resume"
	}
	// Enqueue session-level reminders (skills directory, memory, etc.) so
	// they ride on the first user message of this session. The system-prompt
	// cache prefix stays untouched.
	m.services.Reminder.RequeueSystemReminders()
	return m.services.Hook.Execute(ctx, hook.SessionStart, hook.HookInput{
		Source: source,
		Model:  m.env.GetModelID(),
	})
}

type stopHookResultMsg struct {
	Blocked bool
	Reason  string
	Result  core.Result
}

func (m *model) fireIdleHooksCmd(result core.Result) tea.Cmd {
	hookEngine := m.services.Hook

	lastContent := core.LastAssistantChatContent(m.conv.Messages)
	hasStopHooks := hookEngine.HasHooks(hook.Stop)
	stopHookActive := hookEngine.StopHookActive()

	return func() tea.Msg {
		var blocked bool
		var reason string
		if hasStopHooks {
			outcome := hookEngine.Execute(context.Background(), hook.Stop, hook.HookInput{
				LastAssistantMessage: lastContent,
				StopHookActive:       stopHookActive,
			})
			if outcome.ShouldBlock {
				blocked = true
				reason = outcome.BlockReason
			}
		}
		hookEngine.ExecuteAsync(hook.Notification, hook.HookInput{
			Message:          "Claude is waiting for your input",
			NotificationType: "idle_prompt",
		})
		return stopHookResultMsg{Blocked: blocked, Reason: reason, Result: result}
	}
}

// defaultUIHookBudget bounds a hook that the bubbletea goroutine waits on.
//
// The engine's own default is 600 seconds (hook.defaultTimeout), which suits a
// detached hook. Applied to a gate sitting between a keypress and the submit it
// means a hook that blocks — a network call, an unreachable host, a stray
// `read` — takes the entire UI with it for ten minutes: nothing repaints, and
// Esc and Ctrl+C queue up behind the very loop that would dispatch them.
//
// The cap applies even to a hook that configured a longer timeout of its own.
// That configuration is reasonable for a hook running off the UI goroutine; on
// this path it amounts to asking for a frozen terminal. A hook cut short fails
// open — the engine logs it, and the user's prompt is not blocked because their
// hook hung. A deployment with a legitimately slow gate can raise the budget
// via the hookUITimeout setting (see uiHookBudget).
const defaultUIHookBudget = 5 * time.Second

// uiHookBudget resolves the deadline for a hook the bubbletea goroutine waits
// on. It honors the hookUITimeout setting so a legitimately slow prompt gate can
// raise it, falling back to defaultUIHookBudget for an empty, unparseable, or
// non-positive value.
func (m *model) uiHookBudget() time.Duration {
	if raw := m.services.Setting.HookUITimeout(); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return defaultUIHookBudget
}

func (m *model) checkPromptHook(ctx context.Context, prompt string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, m.uiHookBudget())
	defer cancel()

	outcome := m.services.Hook.Execute(ctx, hook.UserPromptSubmit, hook.HookInput{Prompt: prompt})
	// UserPromptSubmit hooks may return additionalContext to inject into the
	// next turn. Enqueue as a <system-reminder> so it rides on the user
	// message that's about to go out (which is the same prompt being checked).
	if outcome.AdditionalContext != "" {
		m.services.Reminder.Enqueue(outcome.AdditionalContext)
	}
	return outcome.ShouldBlock, outcome.BlockReason
}

func (m *model) switchProvider(p llm.Provider) {
	m.env.LLMProvider = p
	m.services.LLM.SetProvider(p)
	m.services.Hook.SetLLMCompleter(buildHookCompleter(p), m.env.GetModelID())
}

func (m *model) refreshMemoryContext(cwd, loadReason string) {
	files := system.LoadMemoryFiles(cwd)
	var userParts, projectParts []string
	for _, f := range files {
		switch f.Level {
		case "global":
			userParts = append(userParts, f.Content)
		case "project", "local":
			projectParts = append(projectParts, f.Content)
		}
		m.services.Hook.ExecuteAsync(hook.InstructionsLoaded, hook.HookInput{
			FilePath:   f.Path,
			MemoryType: memoryTypeForLevel(f.Level),
			LoadReason: loadReason,
		})
	}
	m.env.CachedUserInstructions = joinSections(userParts)
	m.env.CachedProjectInstructions = joinSections(projectParts)
}

// syncSettingsToHookEngine hands the engine the current settings with plugin
// hooks folded in.
//
// The merge has to happen on the snapshot that is actually passed on. Settings
// .Snapshot() deep-copies on every call, so merging into a separate one — as
// both reload paths used to — writes the plugin hooks into a clone that is
// immediately discarded, and the engine receives a clean copy with none of
// them. A plugin's hooks then stopped firing after any cwd change or plugin
// install, silently. init.go gets this right by binding the snapshot first;
// doing the merge here means a caller cannot get it wrong.
func (m *model) syncSettingsToHookEngine() {
	settings := m.services.Setting.Snapshot()
	plugin.MergePluginHooksIntoSettings(settings)
	m.services.Hook.SetSettings(settings)
}

func memoryTypeForLevel(level string) string {
	switch level {
	case "global":
		return "User"
	case "local":
		return "Local"
	default:
		return "Project"
	}
}

func joinSections(parts []string) string {
	return strings.Join(parts, "\n\n")
}

func buildHookCompleter(p llm.Provider) hook.LLMCompleter {
	if p == nil {
		return nil
	}
	return func(ctx context.Context, systemPrompt, userMessage, model string) (string, error) {
		c := llm.NewClient(p, model, 0)
		resp, err := c.Complete(ctx, systemPrompt, []core.Message{{
			Role:    ai.RoleUser,
			Content: userMessage,
		}}, 4096)
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
}
