// Root bubbletea model. Holds the four event sources (user input, system
// triggers, agent outbox, main-loop notices), the env state, and the
// services struct. Init batches the initial commands (MCP autoconnect, cron +
// async-hook tickers, optional initial prompt).
//
// All the model's *behavior* lives in sibling files:
//
//	model_lifecycle.go     construction + run-option application + task
//	                       lifecycle wiring + SessionEnd shutdown
//	model_session.go       session save/load + per-session task storage
//	model_scrollback.go    rendering committed messages to terminal output
//	model_agent_events.go  conv.Runtime callbacks invoked by the agent
//	                       outbox pump
//	model_compact.go       conversation compaction (auto + /compact)
//	model_tool_effects.go  side effects from tool calls (cwd, files, agent
//	                       launches, oversized-output persistence)
//	model_workspace.go     cwd/file change reactions + FileWatcher setup
//	model_turn_queue.go    inbox drain + prompt injection at turn end +
//	                       stop-hook gate before persistence
//	model_subfeatures.go   deps builders for sub-features
//	model_actions.go       persona switch (hot-patch prompt parts + skills)
package app

import (
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/app/trigger"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool/evolve"
)

const defaultWidth = 80

type model struct {
	// ── Sub-models (one per event source / concern) ─────────────
	userInput input.Model // Source 1: user keyboard input
	// mainNotices is the TUI's notice-staging channel (Source 2) — NOT the main
	// agent's inbox. Broker messages routed to "main" (subagent completions,
	// interim messages) and self-learn notices land here; they show as a notice
	// and then reach the main agent's real core.Agent inbox. See notify.go.
	mainNotices chan mainNotice
	// pendingNotices holds notices that arrived mid-stream, where appending to
	// the conversation is unsafe. Released at the next completed tool batch
	// (OnStepEnd), or at OnTurnEnd if the turn ends first.
	pendingNotices []mainNotice
	// drainedThisStep caps OnStepEnd to one queued message per step
	// (PostTool fires once per tool). Reset each step in OnInference (PostInfer).
	drainedThisStep bool
	selfLearnStarts chan struct{}        // fork goroutine → Update loop: a review started (start the spinner)
	systemInput     trigger.Model        // Source 3: system events (cron/hooks/watcher)
	conv            conv.Model           // Agent Outbox: conversation + output rendering
	desktop         desktopSurface       // Opt-in full-screen surface (ctrl+g); see desktop_surface.go
	env             env                  // Shared app state: provider, session, permission, plan, config
	services        services             // Domain service singletons, injected at construction
	learnedStores   *learnedStoreContext // live cwd/settings source for /evolve inventories

	// welcomePending marks the startup splash as not yet frozen into scrollback.
	// While set, the splash renders live above the input (visible from launch
	// and tracking the model the user picks); the first scrollback commit then
	// freezes that same banner — with the now-selected model — and clears this.
	// Set in Run for fresh sessions. See view.go (liveWelcome) and
	// model_scrollback.go (takeWelcomeBanner).
	welcomePending bool

	// reviewerApprovals / reviewerEscalations count auto-review outcomes this
	// session for the status bar: gray-zone tool calls the judge auto-approved
	// vs. handed back to the user. Pointers so value-receiver copies of the
	// model share one counter across the agent and UI goroutines.
	reviewerApprovals   *atomic.Int64
	reviewerEscalations *atomic.Int64

	// pendingDecisions maps a tool call ID to the auto-review judge's decision,
	// so the renderer can draw it inline under that tool call. Written on the
	// agent goroutine (in PermissionReview, before the tool runs) and read on
	// the UI goroutine at render time — a sync.Map, shared by pointer across
	// value-receiver copies of the model.
	pendingDecisions *sync.Map // tool call ID → core.ReviewDecision

	// autopilot holds the live autopilot snapshot (judge + resolved config),
	// hot-swapped when the /autopilot panel saves so model/prompt/steer changes
	// take effect without an agent restart. m.env.AutoPilot is UI-goroutine-owned
	// and can be reassigned by a mid-turn Save, so the agent side never reads it
	// directly — it Loads this snapshot instead (single-word swap is race-free).
	autopilot *atomic.Pointer[autopilotRuntime]

	// autopilotContinuations counts TurnEnd auto-continuations since the last
	// human turn (reset in dispatchSubmission); autopilotContinuing tags the
	// in-flight submit as copilot-driven so that reset skips it.
	autopilotContinuations int
	autopilotContinuing    bool

	// autopilotDeciding is true while a turn-end/kick decision is in flight, so
	// the mode indicator shows "thinking…" instead of a transcript notice.
	autopilotDeciding bool

	// autopilotRecoveries counts consecutive attempts to revive a run after a
	// turn died on an error, bounding a retry loop into a sustained outage. Any
	// turn that reaches OnTurnEnd resets it.
	autopilotRecoveries int

	// beforeGoal is the autopilot config as it stood before /goal took the wheel,
	// restored when the goal is met or cleared so the steers it switched on don't
	// outlive it. Nil when no goal is driving.
	beforeGoal *setting.AutoPilotSettings

	// skillUsedThisTurn records whether the current turn invoked the Skill tool.
	// It scopes the self-learning skills review: a skill-use turn weighs
	// update/delete of that skill, a skill-free turn weighs create. Set in
	// OnToolResult, read + cleared at OnTurnEnd.
	skillUsedThisTurn bool

	// evolveRequestedThisTurn records whether the current turn called the
	// Evolve tool — the model-decided self-learning trigger. Set in
	// OnToolResult, read + cleared at OnTurnEnd.
	evolveRequestedThisTurn bool

	// agentEvolveCaps records the self-learning capabilities the live agent's
	// toolset was built with. ensureAgentSession compares them against the
	// current settings on every turn start and rebuilds on drift — covering
	// /evolve saves and external settings edits with one mechanism.
	agentEvolveCaps evolve.Capabilities

	// agentDisabledToolsSignature is the disabledToolsSignature() the live
	// agent's toolset was built with; ensureAgentSession rebuilds when it no
	// longer matches the current one, so a /tool enable/disable takes effect on
	// the next turn.
	agentDisabledToolsSignature string

	// agentRestartMessages is the last live main-agent chain captured before a
	// deliberate stop. The UI conversation is a rendering model and can be
	// empty or temporarily divergent, so it is not authoritative for rebuilding
	// a stopped agent. The snapshot is cleared only after a replacement starts,
	// or explicitly by ResetAgentSession when the conversation itself changes.
	agentRestartMessages []core.Message

	// Streaming blocks render their markdown off the UI goroutine so a completed
	// block never stalls repaint. See flushState and model_scrollback.go.
	flush flushState

	// tempImageFiles holds the files adaptTurnForProvider materialized for
	// clipboard images, removed at exit — see removeTempImageFiles.
	tempImageFiles []string
}

var _ conv.Runtime = (*model)(nil)

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.userInput.MCP.Selector.AutoConnect(),
		trigger.TriggerCronTickNow(),
		trigger.StartCronTicker(),
		trigger.StartAsyncHookTicker(),
		awaitMainNotice(m.mainNotices),
		awaitSelfLearnStart(m.selfLearnStarts),
	}
	if m.env.InitialPrompt != "" {
		prompt := m.env.InitialPrompt
		cmds = append(cmds, func() tea.Msg { return initialPromptMsg(prompt) })
	}
	return tea.Batch(cmds...)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
