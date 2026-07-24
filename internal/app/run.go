// Package app provides the unified entry point for interactive and non-interactive modes.
package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/app/trigger"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/persona"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool"
)

// Run routes to either print mode or interactive TUI.
func Run(opts setting.RunOptions) error {
	if opts.Persona != "" {
		if err := validatePersona(opts.Persona); err != nil {
			return err
		}
	}

	if opts.Print != "" {
		return runPrint(opts.Print, opts.Persona)
	}

	if userQuit, err := kit.ResolveTheme(setting.LoadTheme(), setting.SaveTheme); userQuit || err != nil {
		return err
	}

	m, err := initModel(opts)
	if err != nil {
		return err
	}

	// Fresh sessions show the splash live above the input from launch and freeze
	// it into scrollback on the first commit, so it stays visible immediately
	// yet reflects the model the user picks after launch instead of freezing
	// "no model selected" into scrollback. Resumed sessions skip it —
	// commitAllMessages will reprint the conversation immediately, so a splash
	// would just be churn.
	if len(m.conv.Messages) == 0 {
		m.welcomePending = true
	}

	finalModel, err := tea.NewProgram(m).Run()
	if err != nil {
		return fmt.Errorf("failed to run TUI: %w", err)
	}

	if fm, ok := finalModel.(*model); ok {
		// Persist any transcript-index mutations staged during the session before
		// exit, so the next launch's picker reflects this session without a full
		// rebuild scan. This is the single seam every quit path converges on
		// (ctrl+c, submit-exit, /exit, /quit all end here once tea.Quit unwinds),
		// so the flush runs exactly once regardless of which handler fired it.
		// Best-effort — the index rebuilds from the transcripts if this fails.
		_ = fm.services.Session.FlushIndex()
		printExitMessage(fm)
	}
	return nil
}

func initModel(opts setting.RunOptions) (*model, error) {
	if err := initInfrastructure(); err != nil {
		return nil, err
	}
	m, err := newModel(opts)
	if err != nil {
		return nil, err
	}
	m.fireStartupHooks()
	return m, nil
}

func (m *model) configureAsyncHookCallback() {
	if m.systemInput.AsyncHookQueue == nil {
		return
	}
	queue := m.systemInput.AsyncHookQueue
	m.services.Hook.SetAsyncHookCallback(func(result hook.AsyncHookResult) {
		reason := result.BlockReason
		if reason == "" {
			reason = "asynchronous hook requested a rewake"
		}
		queue.Push(trigger.AsyncHookRewake{
			Notice:             fmt.Sprintf("Async hook blocked: %s", reason),
			Context:            []string{formatAsyncHookContinuationContext(result, reason)},
			ContinuationPrompt: "A background policy hook reported a blocking condition. Re-evaluate the plan and choose a safer next step.",
		})
	})
}

func (m *model) fireStartupHooks() {
	outcome := m.executeStartupHooks(context.Background())
	m.applyStartupHookOutcome(outcome)
	// Hook-injected context rides on the same harness channel as skills and
	// memory: it gets queued for the first user message as a
	// <system-reminder>, not appended as a standalone user message. This
	// keeps SessionStart context out of the visible chat and lets it
	// re-emerge after PostCompact alongside other harness reminders.
	if outcome.AdditionalContext != "" {
		m.services.Reminder.Enqueue(outcome.AdditionalContext)
	}
}

func printExitMessage(m *model) {
	sessionID := m.services.Session.ID()
	command := resumeCommandForSession(sessionID, m.services.Session.TranscriptPath())
	if command != "" {
		dim := kit.DimStyle()
		fmt.Println()
		fmt.Println(dim.Render("Resume this session with:"))
		fmt.Println(dim.Render(command))
		fmt.Println()
	}
}

func resumeCommandForSession(sessionID, transcriptPath string) string {
	if sessionID == "" || transcriptPath == "" {
		return ""
	}
	if _, err := os.Stat(transcriptPath); err != nil {
		return ""
	}
	return "san -r " + sessionID
}

func formatAsyncHookContinuationContext(result hook.AsyncHookResult, reason string) string {
	return fmt.Sprintf(
		"<background-hook-result>\nstatus: blocked\nevent: %s\nhook_type: %s\nhook_source: %s\nhook_name: %s\nreason: %s\ninstruction: Re-evaluate the plan before any further model or tool action.\n</background-hook-result>",
		result.Event,
		result.HookType,
		result.HookSource,
		result.HookName,
		reason,
	)
}

func runPrint(userMessage, personaName string) error {
	ctx := context.Background()

	llmProvider, modelID, err := resolveProvider(ctx)
	if err != nil {
		return err
	}

	sysPrompt := setting.DefaultSystemPrompt
	var disabledTools map[string]bool
	if personaName != "" {
		cwd, _ := os.Getwd()
		p, _ := persona.Default().Get(personaName)

		base, _ := setting.LoadForCwd(cwd)
		var overlay *setting.Data
		if p.Settings != nil {
			overlay = &p.Settings.Data
		}
		merged := setting.ApplyPersonaOverlay(base, overlay)
		disabledTools = setting.WithDefaultDisabledTools(merged.DisabledTools)

		sys := system.Build(core.ScopeMain,
			system.WithPersona(system.Persona{
				Identity: p.Identity,
				Behavior: p.Behavior,
				Rules:    p.Rules,
			}),
		)
		sysPrompt = sys.Prompt()

		if merged.Model != "" {
			if models, err := llmProvider.ListModels(ctx); err == nil {
				for _, m := range models {
					if m.ID == merged.Model {
						modelID = merged.Model
						break
					}
				}
			}
		}
	} else {
		disabledTools = setting.WithDefaultDisabledTools(nil)
	}

	schemas := (&tool.Set{Disabled: disabledTools}).Tools()

	completionOpts := llm.CompletionOptions{
		Model:        modelID,
		MaxTokens:    setting.DefaultMaxTokens,
		SystemPrompt: sysPrompt,
		Messages:     []core.Message{core.UserMessage(userMessage, nil)},
		Tools:        schemas,
	}

	streamChan := llmProvider.Stream(ctx, completionOpts)
	for chunk := range streamChan {
		switch chunk.Type {
		case llm.ChunkTypeText:
			fmt.Print(chunk.Text)
		case llm.ChunkTypeError:
			return chunk.Error
		case llm.ChunkTypeDone:
			fmt.Println()
		}
	}

	return nil
}

// resolveProvider connects to the best available provider for a one-shot,
// non-interactive run (print mode, headless agent) and returns it with a
// concrete model id. It shares llm.ResolveProvider's resolution order with the
// interactive startup path; when resolution falls back to a connection without
// a saved current model, it fills that provider's default model id.
func resolveProvider(ctx context.Context) (llm.Provider, string, error) {
	store, err := llm.NewStore()
	if err != nil {
		return nil, "", fmt.Errorf("failed to load store: %w", err)
	}
	resolved, ok := llm.ResolveProvider(ctx, store)
	if !ok {
		return nil, "", fmt.Errorf("no provider connected. Run 'san' and use /models to connect")
	}
	modelID := resolved.ModelID
	if modelID == "" {
		// Provider.Name() carries the "name:auth" form; DefaultModel keys on the
		// bare provider name.
		name, _, _ := strings.Cut(resolved.Provider.Name(), ":")
		modelID = setting.DefaultModel(name, string(resolved.AuthMethod))
	}
	if modelID == "" {
		// Providers without a built-in default model (e.g. a custom provider)
		// need an explicit selection.
		return nil, "", fmt.Errorf("no model selected for %s. Run 'san' and use /models to select one", resolved.Provider.Name())
	}
	return resolved.Provider, modelID, nil
}

// validatePersona ensures the named persona exists on disk, early in startup,
// before either print or interactive mode proceeds.
func validatePersona(name string) error {
	cwd, _ := os.Getwd()
	persona.Initialize(cwd)
	return persona.Default().Validate(name)
}
