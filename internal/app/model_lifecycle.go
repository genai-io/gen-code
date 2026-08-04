// Model lifecycle: construction (newModel/newBaseModel), startup-time
// option application (--continue / --resume / --plugin-dir), plugin-change
// state reload, memory-context priming, task lifecycle wiring, and
// SessionEnd shutdown.
package app

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/app/trigger"
	"github.com/genai-io/san/internal/broker"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/task"
	"github.com/genai-io/san/internal/todo"
)

func newModel(opts setting.RunOptions) (*model, error) {
	base := newBaseModel()
	m := &base

	// The main conversation registers the "main" address with the broker;
	// messages routed to it (subagent completions, interim messages) land on
	// mainNotices and are injected at the next safe seam — see notify.go.
	broker.Register(broker.Main, func(msg broker.Message) bool {
		return m.notifyMain(fromBrokerMessage(msg))
	})

	// Wire task completion: closure captures hooks + tracker.
	m.wireTaskLifecycle(m.services.Hook)

	m.configureAsyncHookCallback()
	m.ensureMemoryContextLoaded()
	m.ReconfigureAgentTool()
	m.applyPersonaSkills()
	m.applyPersonaAgents()
	m.wireReminderProviders()
	m.InitTaskStorage()
	m.userInput.Autopilot.SetMissionRefiner(m.missionRefine)
	m.userInput.Autopilot.SetConfigSource(func() setting.AutoPilotSettings { return m.env.AutoPilot })
	if err := m.applyRunOptions(opts); err != nil {
		return nil, err
	}
	return m, nil
}

func newBaseModel() model {
	svc := newServices()
	learnedStores := newLearnedStoreContext(appCwd, svc.Setting)
	environment := newEnv(svc.LLM, appCwd, svc.Setting.IsGitRepo(appCwd))
	applyStartupSettings(&environment, svc.Setting.Snapshot(), appCwd, svc.Setting.AllowBypass(), svc.Hook)
	return model{
		userInput: input.New(appCwd, defaultWidth, commandSuggestionMatcher(svc.Command), input.SelectorDeps{
			AgentRegistry:   &agentRegistryAdapter{svc.Subagent},
			PersonaRegistry: svc.Persona,
			SkillRegistry:   svc.Skill,
			MCPRegistry:     svc.MCP,
			PluginRegistry:  svc.Plugin,
			Setting:         svc.Setting,
			LoadDisabled:    svc.Setting.GetDisabledToolsAt,
			UpdateDisabled:  svc.Setting.UpdateDisabledToolsAt,
			Evolve: input.EvolveDeps{
				Workspace: learnedStores.Snapshot,
				Learned:   newLearnedSkillStore(learnedStores.Snapshot),
				Memory:    newLearnedMemoryStore(learnedStores.Snapshot),
				Recent:    newRecentLearnAccessor(svc.SelfLearn.Recent),
			},
		}),
		conv:                conv.NewModel(defaultWidth),
		mainNotices:         make(chan mainNotice, 64),
		selfLearnStarts:     make(chan struct{}, 8),
		systemInput:         trigger.New(),
		env:                 environment,
		services:            svc,
		learnedStores:       learnedStores,
		reviewerApprovals:   new(atomic.Int64),
		reviewerEscalations: new(atomic.Int64),
		pendingDecisions:    new(sync.Map),
		autopilot:           new(atomic.Pointer[autopilotRuntime]),
	}
}

func applyStartupSettings(environment *env, settings *setting.Data, cwd string, allowBypass bool, hookEngine *hook.Engine) {
	if settings == nil {
		return
	}
	environment.ApplyDefaultPermissionMode(settings.StartupMode(), cwd, allowBypass)
	hookEngine.SetPermissionMode(environment.OperationModeName())
	environment.ShowContextBar = settings.ShowContextBar()
	environment.AutoPilot = settings.AutoPilot.Clone()
}

func (m *model) applyRunOptions(opts setting.RunOptions) error {
	if opts.PluginDir != "" {
		ctx := context.Background()
		if err := m.services.Plugin.LoadFromPath(ctx, opts.PluginDir); err != nil {
			return fmt.Errorf("failed to load plugins from %s: %w", opts.PluginDir, err)
		}
		if err := m.ReloadAfterPluginChange(); err != nil {
			return err
		}
	}

	if opts.Prompt != "" {
		m.env.InitialPrompt = opts.Prompt
	}

	if opts.Persona != "" {
		if err := m.services.Persona.Validate(opts.Persona); err != nil {
			return err
		}
		if err := m.setActivePersona(opts.Persona); err != nil {
			return err
		}
	}

	if opts.Continue {
		if err := m.applyContinueOption(); err != nil {
			return err
		}
	}

	if opts.Resume {
		if err := m.applyResumeOption(opts.ResumeID); err != nil {
			return err
		}
	}

	return nil
}

// ReloadAfterPluginChange rebuilds the state that plugins contribute to after
// the active plugin set changes — a --plugin-dir load at startup, or a /plugin
// install / uninstall mid-session. It reloads the project's feature services,
// re-merges plugin hooks, and re-wires the agent tool, persona, and reminders
// so the running session reflects the new set.
func (m *model) ReloadAfterPluginChange() error {
	// Plugins were just loaded by the caller; rebuild the project's feature
	// services (not the plugins themselves) and re-point at them.
	m.reloadProjectServices(m.env.CWD)

	m.syncSettingsToHookEngine()
	m.ReconfigureAgentTool()
	m.applyPersonaSkills()
	m.applyPersonaAgents()

	// Refresh skills/memory reminders so the LLM sees the updated skill set
	// in the next user message instead of waiting for SessionStart/PostCompact.
	m.services.Reminder.RequeueSystemReminders()

	return nil
}

func (m *model) applyContinueOption() error {
	if err := m.services.Session.EnsureStore(m.env.CWD); err != nil {
		return fmt.Errorf("failed to initialize session store: %w", err)
	}

	sess, err := m.services.Session.LoadLatest()
	if err != nil {
		return fmt.Errorf("no previous session to continue: %w", err)
	}

	m.restoreSessionData(sess)
	return nil
}

func (m *model) applyResumeOption(resumeID string) error {
	if err := m.services.Session.EnsureStore(m.env.CWD); err != nil {
		return fmt.Errorf("failed to initialize session store: %w", err)
	}

	if resumeID != "" {
		sess, err := m.services.Session.Load(resumeID)
		if err != nil {
			return fmt.Errorf("failed to load session %s: %w", resumeID, err)
		}
		m.restoreSessionData(sess)
		return nil
	}

	m.userInput.Session.PendingSelector = true
	return nil
}

func (m *model) ensureMemoryContextLoaded() {
	if m.env.CachedUserInstructions != "" || m.env.CachedProjectInstructions != "" {
		return
	}
	m.refreshMemoryContext(m.env.CWD, "session_start")
}

func (m *model) wireTaskLifecycle(hookEngine hook.Handler) {
	trackerSvc := m.services.Tracker

	fireHook := func(event hook.EventType, info task.TaskInfo) {
		if hookEngine == nil {
			return
		}
		hookEngine.ExecuteAsync(event, hook.HookInput{
			TaskID:          info.ID,
			TaskSubject:     taskSubject(info),
			TaskDescription: info.Description,
		})
	}

	task.SetLifecycleHandler(taskLifecycleFunc{
		onCreated: func(info task.TaskInfo) {
			fireHook(hook.TaskCreated, info)

			// The entry is born here rather than off the launching tool's
			// result, so that it exists before the task can possibly finish:
			// the manager notifies from inside CreateBashTask / RegisterTask,
			// while the goroutine that can complete the task is only started
			// afterwards. Reading it off the tool result instead let a
			// short-lived task complete an entry that did not exist yet.
			todo.TrackWorker(trackerSvc, info)
		},
		onCompleted: func(info task.TaskInfo) {
			fireHook(hook.TaskCompleted, info)
			todo.CompleteWorker(trackerSvc, info)

			// A finished subagent sends its result to the "main" address —
			// the same broker path a running subagent uses for interim
			// messages, so main has one arrival channel for everything.
			if msg, ok := taskCompletionMessage(info); ok {
				broker.Send(msg)
			}
		},
	})
}

// notifyMain posts a notice (a broker-routed subagent message, or a self-learn
// done/failed line) to the main loop's Source-2 channel and always reports it
// delivered. The broker's delivery contract forbids blocking, but a subagent
// completion is main's only signal that a background worker finished, so it
// must not be dropped: on a full channel we hand the notice to a short-lived
// goroutine that blocks on the send instead of discarding it. The Update loop
// drains continuously, so the goroutine settles promptly.
func (m *model) notifyMain(n mainNotice) bool {
	select {
	case m.mainNotices <- n:
	default:
		go func() { m.mainNotices <- n }()
	}
	return true
}

type taskLifecycleFunc struct {
	onCreated   func(task.TaskInfo)
	onCompleted func(task.TaskInfo)
}

func (f taskLifecycleFunc) TaskCreated(info task.TaskInfo)   { f.onCreated(info) }
func (f taskLifecycleFunc) TaskCompleted(info task.TaskInfo) { f.onCompleted(info) }

func (m *model) FireSessionEnd(reason string) {
	// Bounded for the same reason as checkPromptHook: this runs on the
	// bubbletea goroutine while the user is quitting, and a SessionEnd hook
	// that blocks makes the quit itself look hung.
	ctx, cancel := context.WithTimeout(context.Background(), m.uiHookBudget())
	defer cancel()
	m.services.Hook.Execute(ctx, hook.SessionEnd, hook.HookInput{
		Reason: reason,
	})
	m.services.Hook.ClearSessionHooks()
	if m.systemInput.FileWatcher != nil {
		m.systemInput.FileWatcher.Stop()
	}
	broker.Unregister(broker.Main)
}

// removeTempImageFiles deletes the files adaptTurnForProvider materialized for
// clipboard images this run. Deleting them at the end of the turn that wrote
// them would leave every later turn pointing at a path that no longer resolves:
// the path lives on in the message content conv persists and replays, so it has
// to keep resolving for as long as the model can still be asked about it.
//
// The scope is the process, not the conversation. A --continue or --resume
// starts a fresh one, which replays that same persisted path with nothing
// behind it — see #458 for giving these files a home that survives the way the
// transcript does.
func (m *model) removeTempImageFiles() {
	for _, p := range m.tempImageFiles {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			log.Logger().Warn("remove temp image file", zap.String("path", p), zap.Error(err))
		}
	}
	m.tempImageFiles = nil
}
