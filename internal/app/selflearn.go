// L1 self-learning wire-up: bridges setting.SelfLearnSettings into a
// session-scoped selflearn.Reviewer + ReviewFunc that forks against the
// live LLM/System via selflearn.RunReview.
// See notes/active/l1-background-review.md §9 step 4.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/selflearn"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool/evolve"
)

// selfLearnDisableEnvSuffix is the env kill switch (§3.1) — mirrors Claude
// Code's CLAUDE_CODE_DISABLE_AUTO_MEMORY. Read via setting.Getenv as
// SAN_DISABLE_SELF_LEARN.
const selfLearnDisableEnvSuffix = "DISABLE_SELF_LEARN"

// learnedStoreContext is the live workspace source behind the /evolve panel:
// workspace reloads replace both the cwd and the settings service, so the
// panel and its store accessors read them through here rather than capturing
// startup values. All access happens on the bubbletea update goroutine (panel
// key handling, reloadProjectServices), so no locking is needed.
type learnedStoreContext struct {
	cwd      string
	settings *setting.Settings
}

func newLearnedStoreContext(cwd string, settings *setting.Settings) *learnedStoreContext {
	return &learnedStoreContext{cwd: cwd, settings: settings}
}

func (c *learnedStoreContext) Snapshot() (string, *setting.Settings) {
	return c.cwd, c.settings
}

func (c *learnedStoreContext) Update(cwd string, settings *setting.Settings) {
	c.cwd = cwd
	c.settings = settings
}

// resolveSelfLearnConfig is the shared runtime gate for both reviewer wiring
// and Evolve tool advertisement. The environment override is a hard disable;
// invalid settings likewise expose neither a reviewer nor a trigger tool.
func (m *model) resolveSelfLearnConfig() (selflearn.Config, error) {
	if v := setting.Getenv(selfLearnDisableEnvSuffix); v == "1" || strings.EqualFold(v, "true") {
		return selflearn.Config{}, nil
	}
	// Settings.SelfLearn() reads just the value-typed field — no whole-Data
	// clone — so the per-turn drift check in ensureAgentSession stays cheap.
	return selflearn.ResolveSettings(m.services.Setting.SelfLearn())
}

// wireSelfLearn builds the L1 Reviewer for the running session when ≥1
// arm is enabled. params is captured so the fork rebuilds an LLM client
// with the same provider/model/max-tokens for prefix-cache parity
// (§6 invariant #2).
func (m *model) wireSelfLearn(params agent.BuildParams) {
	// Tear down first — ensureAgentSession can re-enter after a stopped agent
	// and would otherwise overwrite reviewCancel un-called, leaking the context
	// and pinning the old fork for up to forkDeadline.
	m.teardownSelfLearn()

	// The shared resolver applies the env kill switch and validates settings so
	// reviewer wiring and Evolve advertisement cannot disagree.
	cfg, err := m.resolveSelfLearnConfig()
	if err != nil {
		log.Logger().Warn("self-learning config rejected at startup", zap.Error(err))
		return
	}
	if !cfg.Enabled() {
		return
	}

	// Session-scoped review context; teardownSelfLearn cancels it so an
	// in-flight fork unblocks immediately on /clear / quit instead of waiting
	// up to forkDeadline for its independent timeout. reviewCancel + live are
	// stored in the session struct once the reviewer is built (below).
	reviewCtx, reviewCancel := context.WithCancel(context.Background())

	// live gates the fork-goroutine write observers below. They capture this
	// local so a write landing after teardown drops silently instead of racing
	// on UI state; teardownSelfLearn flips it false through the session struct
	// (the same *atomic.Bool).
	live := &atomic.Bool{}
	live.Store(true)

	memStore := selflearn.NewMemoryStore(m.env.CWD, cfg.MemoryMaxChars, cfg.MemoryPath)

	// Write observers feed the live spinner-tail and the post-pass recap. They
	// run on the fork goroutine and check `live` so a write landing after
	// teardown drops silently instead of racing on UI state. recordAction is
	// the shared gate + indicator hop; the memory and skill paths each adapt it
	// with their own verb/target naming.
	recordAction := func(kind, verb, target, note string) {
		if !live.Load() {
			return
		}
		act := ReviewAction{Verb: verb, Kind: kind, Target: target, Note: note}
		m.services.SelfLearn.Indicator.RecordAction(act)
		// Also feed the persistent recent-activity log behind the /evolve
		// RECENT zones (the indicator's copy is drained after each pass).
		m.services.SelfLearn.Recent.Add(act, time.Now())
	}
	memStore.SetWriteObserver(func(action, topic, note string) {
		recordAction("memory", memoryVerb(action), memoryTopicName(topic), note)
	})

	review := func(kinds selflearn.ReviewKind, skillPerms selflearn.SkillPermissions, snapshot []core.Message) {
		// Liveness checks before any UI mutation — a teardown race must
		// not flash "evolving → evolved" on a session the user just killed.
		// Active() guards the macro window; the per-phase live.Load() checks
		// below catch a teardown that lands AFTER Active() returned true.
		if !m.services.Agent.Active() {
			return
		}
		sys := m.services.Agent.System()
		if sys == nil {
			return
		}

		if !live.Load() {
			return
		}

		// The skill manager is scoped to exactly the actions the trigger allowed
		// this pass (create on a skill-free turn, update/delete on a skill-use
		// turn), so the reviewer can't take an action that wasn't triggered. All
		// actions remain agent-created only.
		skillMgr := selflearn.NewSkillManager(m.env.CWD, skillPerms)
		skillMgr.SetWriteObserver(func(action, skillName, note string) {
			recordAction("skill", skillVerb(action), skillName, note)
		})

		m.services.SelfLearn.Indicator.BeginReview()
		m.publishSelfLearnStarted()

		client := llm.NewClient(params.Provider, params.ModelID, params.MaxTokens)
		client.SetThinkingEffort(params.ThinkingEffort)
		// Sidechain recorder: each L1 fork gets its OWN session ID
		// (formatted "<parent>.selflearn-review.<unix>") so
		// `san --resume <fork-id>` replays exactly that review's LLM
		// calls in isolation. The recap row surfaces this fork ID.
		var forkOnEvent func(core.Event)
		var forkSessionID string
		if rec := m.services.Session.NewSidechainRecorder("selflearn-review", params.Provider.Name(), params.ModelID, params.MaxTokens); rec != nil {
			forkOnEvent = rec.OnAgentEvent
			forkSessionID = rec.SessionID()
		}
		aiClient, err := client.AI(nil)
		if err != nil {
			return
		}
		fc := selflearn.ForkConfig{
			Client:   aiClient,
			System:   sys,
			CWD:      m.env.CWD,
			Memory:   memStore,
			Skills:   skillMgr,
			Strategy: cfg.Strategy,
			OnEvent:  forkOnEvent,
		}
		llmSummary, runErr := selflearn.RunReview(reviewCtx, fc, kinds, snapshot)
		// Re-check live AFTER the RunReview return. This is the macro
		// window — RunReview can sit on the LLM for up to forkDeadline
		// and teardown is most likely to have landed by now.
		if !live.Load() {
			return
		}
		if runErr != nil {
			m.services.SelfLearn.Indicator.Fail()
			// Drain even on failure so a partial pass (e.g. two memory
			// writes that succeeded before the LLM timed out) still
			// reaches the recap — otherwise the user sees "review
			// failed" with no record of what was actually persisted.
			actions := m.services.SelfLearn.Indicator.DrainActions()
			log.Logger().Warn("self-learning review failed",
				zap.String("kinds", kinds.String()),
				zap.Int("partial-changes", len(actions)),
				zap.Error(runErr),
			)
			m.publishSelfLearnFailure(kinds, runErr)
			if len(actions) > 0 {
				m.publishSelfLearnSummary(actions, forkSessionID)
			}
			return
		}
		// Complete BEFORE Drain so doneCount snapshots len(s.actions);
		// zero-write pass collapses to idle inside Complete (§6 #7).
		// The reviewer's last line ("trimmed go-testing SKILL.md by
		// 1.8KB") becomes the done-phase status tag; the action-log
		// fallback covers a misbehaving / silent reviewer.
		m.services.SelfLearn.Indicator.Complete(llmSummary)
		actions := m.services.SelfLearn.Indicator.DrainActions()
		if len(actions) == 0 {
			return
		}
		log.Logger().Info("self-learning review",
			zap.String("kinds", kinds.String()),
			zap.Int("changes", len(actions)),
			zap.String("fork-session", forkSessionID),
		)
		m.publishSelfLearnSummary(actions, forkSessionID)
	}

	r := selflearn.New(cfg, review)
	m.services.SelfLearn.session = &selfLearnSession{
		reviewer: r,
		cancel:   reviewCancel,
		live:     live,
	}
}

// runSelfLearnDemo drives the indicator through one scripted lifecycle
// (reviewing → 3 actions → done) so a developer can eyeball the spinner /
// target / done-summary in a real terminal without firing a live LLM
// review. Returns immediately; the script runs on a background goroutine.
func (m *model) runSelfLearnDemo() {
	ind := m.services.SelfLearn.Indicator
	if ind == nil {
		return
	}
	const kinds = selflearn.KindMemory | selflearn.KindSkills
	go func() {
		ind.BeginReview()
		m.publishSelfLearnStarted()

		steps := []struct {
			wait   time.Duration
			action ReviewAction
		}{
			{800 * time.Millisecond, ReviewAction{
				Verb: "saved", Kind: "memory", Target: "",
				Note: "noted that lint runs via make ci, not go vet",
			}},
			{1200 * time.Millisecond, ReviewAction{
				Verb: "saved", Kind: "memory", Target: "debugging",
				Note: "added 3 race-condition repro tips",
			}},
			{1200 * time.Millisecond, ReviewAction{
				Verb: "updated", Kind: "skill", Target: "go-testing",
				Note: "trimmed verbose examples, kept the table-test snippet",
			}},
			{1200 * time.Millisecond, ReviewAction{
				Verb: "created", Kind: "skill", Target: "python-typing",
				Note: "new skill, typing-hints and Protocol patterns",
			}},
		}
		for _, s := range steps {
			time.Sleep(s.wait)
			ind.RecordAction(s.action)
		}
		time.Sleep(800 * time.Millisecond)
		ind.Complete("trimmed go-testing SKILL.md by 1.8KB · saved 2 notes")
		actions := ind.DrainActions()
		// Demo: fabricate a plausible-looking fork session ID so the
		// recap footer is identical in shape to the real path.
		demoSessionID := fmt.Sprintf("demo-session.selflearn-review.%d", time.Now().Unix())
		m.publishSelfLearnSummary(actions, demoSessionID)
	}()
}

// notifySelfLearnOverride detects when a /evolve save was overridden by the
// other settings level. Memory enablement and each skill action are compared
// separately because the merger combines their safety gates independently.
// Surfaces a notice instead of leaving the user with only a misleading saved
// confirmation while the requested behavior stays unchanged.
func (m *model) notifySelfLearnOverride(msg input.ConfigSavedMsg) {
	snap := m.services.Setting.Snapshot()
	if snap == nil {
		return
	}
	var arms []string
	if msg.SavedSelfLearn.Memory.Enabled != snap.SelfLearn.Memory.Enabled {
		arms = append(arms, "Memory")
	}
	if msg.SavedSelfLearn.Skills.AllowCreate() != snap.SelfLearn.Skills.AllowCreate() {
		arms = append(arms, "Skill create")
	}
	if msg.SavedSelfLearn.Skills.AllowUpdate() != snap.SelfLearn.Skills.AllowUpdate() {
		arms = append(arms, "Skill update")
	}
	if msg.SavedSelfLearn.Skills.AllowDelete() != snap.SelfLearn.Skills.AllowDelete() {
		arms = append(arms, "Skill delete")
	}
	if len(arms) == 0 {
		return
	}
	other := "project"
	if msg.Scope == "project" {
		other = "user"
	}
	m.conv.AddNotice("Note: " + strings.Join(arms, " and ") +
		" setting is overridden by " + other + "-level settings")
}

// newLearnedSkillStore builds the /evolve inventory accessors over the live
// workspace source. Each call constructs a short-lived SkillManager doing a
// fresh disk scan (the reviewer mutates the skill dirs mid-session, so a
// cached view would go stale). This is a human-initiated surface, so it uses
// full action permissions — the reviewer's Deny* gates govern the autonomous
// loop, not the user's own deletions — and only ever surfaces agent-created
// skills.
func newLearnedSkillStore(source func() (string, *setting.Settings)) input.LearnedSkillStore {
	mgr := func() *selflearn.SkillManager {
		cwd, _ := source()
		return selflearn.NewSkillManager(cwd, selflearn.AllowAllSkillActions())
	}
	return input.LearnedSkillStore{
		List: func() []selflearn.SkillInfo {
			var out []selflearn.SkillInfo
			for _, s := range mgr().Inventory() {
				if !s.Editable() {
					continue // agent-created only; user-authored skills live in /skills
				}
				out = append(out, s)
			}
			return out
		},
		Read: func(name string) (string, error) {
			return mgr().Read(name)
		},
		Delete: func(name string) error {
			_, err := mgr().Delete(name, "removed via /evolve panel")
			return err
		},
	}
}

// teardownSelfLearn unwires the current L1 reviewer: cancels the
// session-scoped fork context, flips the liveness gate false, and drops the
// session. Idempotent. Called from StopAgentSession and the top of
// wireSelfLearn so a rebuild never leaks the prior context.
func (m *model) teardownSelfLearn() {
	s := m.services.SelfLearn.session
	if s == nil {
		return
	}
	s.cancel()
	s.live.Store(false)
	m.services.SelfLearn.session = nil
}

// newLearnedMemoryStore builds the /evolve "Learned" memory accessors over the
// resolved auto-memory dir (honoring a configured memory path, read live from
// settings): list the agent-written .md files, read one, delete one. Read/Delete
// are guarded against path traversal.
func newLearnedMemoryStore(source func() (string, *setting.Settings)) input.LearnedMemoryStore {
	dir := func() string {
		cwd, settings := source()
		var path string
		if settings != nil {
			if snap := settings.Snapshot(); snap != nil {
				path = snap.SelfLearn.Memory.Path
			}
		}
		return system.ResolveAutoMemoryDir(cwd, path)
	}
	return input.LearnedMemoryStore{
		List: func() []input.LearnedMemory {
			d := dir()
			entries, err := os.ReadDir(d)
			if err != nil {
				return nil
			}
			var out []input.LearnedMemory
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".md") {
					continue
				}
				topic := name
				if name != system.AutoMemoryIndexName {
					topic = strings.TrimSuffix(name, ".md")
				}
				out = append(out, input.LearnedMemory{
					Topic:   topic,
					File:    name,
					Summary: memoryFileSummary(filepath.Join(d, name)),
				})
			}
			return out
		},
		Read: func(file string) (string, error) {
			if !safeMemoryFile(file) {
				return "", fmt.Errorf("invalid memory file: %q", file)
			}
			b, err := os.ReadFile(filepath.Join(dir(), file))
			return string(b), err
		},
		Delete: func(file string) error {
			if !safeMemoryFile(file) {
				return fmt.Errorf("invalid memory file: %q", file)
			}
			return os.Remove(filepath.Join(dir(), file))
		},
	}
}

// safeMemoryFile guards Read/Delete against path traversal: the file must be a
// plain .md base name inside the memory dir.
func safeMemoryFile(file string) bool {
	return file != "" && filepath.Base(file) == file && strings.HasSuffix(file, ".md")
}

// memoryFileSummary returns a short "N lines" summary for an inventory row.
func memoryFileSummary(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return "empty"
	}
	n := strings.Count(string(b), "\n")
	if !strings.HasSuffix(string(b), "\n") {
		n++
	}
	return fmt.Sprintf("%d lines", n)
}

// selfLearnCapabilities returns the enabled self-learning capabilities, used
// to build (and gate) the Evolve trigger tool. The zero value — every skill
// action denied and memory off — means "self-learning off", so no tool is
// injected. Shares resolveSelfLearnConfig with wireSelfLearn so the advertised
// tool and the wired reviewer can never disagree (env kill switch, invalid
// settings).
func (m *model) selfLearnCapabilities() evolve.Capabilities {
	cfg, err := m.resolveSelfLearnConfig()
	if err != nil {
		return evolve.Capabilities{}
	}
	return evolve.Capabilities{
		CreateSkills: cfg.Skills.AllowCreate,
		UpdateSkills: cfg.Skills.AllowUpdate,
		DeleteSkills: cfg.Skills.AllowDelete,
		WriteMemory:  cfg.MemoryEnabled,
	}
}

// selfLearnExtraTools returns the Evolve trigger tool for the main agent's
// toolset — the only conditional extra tool today. Nil when self-learning is
// off, so the tool is absent entirely.
func (m *model) selfLearnExtraTools() []core.ToolSchema {
	caps := m.selfLearnCapabilities()
	if !caps.Active() {
		return nil
	}
	return []core.ToolSchema{evolve.Schema(caps)}
}

// handleSelflearnTick advances the indicator and schedules the next tick
// at the cadence Tick returns (spinner interval while reviewing; one
// deadline tick during done/failed hold). Returns nil when idle.
func (m *model) handleSelflearnTick() tea.Cmd {
	if m.services.SelfLearn.Indicator == nil {
		return nil
	}
	delay, stillActive := m.services.SelfLearn.Indicator.Tick(time.Now())
	if !stillActive {
		return nil
	}
	return tea.Tick(delay, func(time.Time) tea.Msg { return selflearnTickMsg{} })
}

// memoryTopicName returns the bare topic name (e.g. "debugging") for a
// memory file, or "" for the index. The indicator renderer adds the
// "memory" / "memory · " prefix at display time.
func memoryTopicName(file string) string {
	if file == "" || strings.EqualFold(file, system.AutoMemoryIndexName) {
		return ""
	}
	return strings.TrimSuffix(file, ".md")
}

// publishSelfLearnSummary posts the post-pass recap into the conversation
// flow. Recap goes in Subject (display-only Notice); routing it through
// Data would re-submit it to the LLM and break the §6 out-of-band promise.
// forkSessionID points at the L1 fork's own session so the recap can
// suggest "san --resume <id>" for replay.
func (m *model) publishSelfLearnSummary(actions []ReviewAction, forkSessionID string) {
	if len(actions) == 0 {
		return
	}
	m.notifyMain(mainNotice{Display: formatRecapBlock(actions, forkSessionID)})
}

// formatRecapBlock renders the post-review recap as a lipgloss-bordered
// card with the "san --resume" hint as a separate line below — no more
// hand-built width math, no footer crammed into the bottom border.
// Layout:
//
//	╭┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄╮
//	┊  memory                                       ┊
//	┊    · index — noted that lint runs via make ci ┊
//	┊    · debugging — added 3 race-condition tips  ┊
//	┊                                               ┊
//	┊  skill                                        ┊
//	┊    · go-testing — trimmed verbose examples    ┊
//	┊    · python-typing — new skill, typing-hints  ┊
//	╰┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄╯
//	  ↪ san --resume demo-session.selflearn-review.123
//
// Empty input ⇒ "" so the publish is skipped on no-write passes.
func formatRecapBlock(actions []ReviewAction, sessionID string) string {
	if len(actions) == 0 {
		return ""
	}
	type group struct {
		kind string
		rows []ReviewAction
	}
	var groups []group
	idx := map[string]int{}
	for _, a := range actions {
		if i, ok := idx[a.Kind]; ok {
			groups[i].rows = append(groups[i].rows, a)
		} else {
			idx[a.Kind] = len(groups)
			groups = append(groups, group{kind: a.Kind, rows: []ReviewAction{a}})
		}
	}

	var inner strings.Builder
	for gi, g := range groups {
		if gi > 0 {
			inner.WriteString("\n\n") // blank line between groups
		}
		inner.WriteString(recapKindStyle(g.kind).Render(g.kind))
		for _, a := range g.rows {
			inner.WriteString("\n")
			inner.WriteString(recapRowLine(a))
		}
	}

	out := selflearnRecapBoxStyle.Render(inner.String())
	if sessionID != "" {
		// "↪ " prefix flips the line from passive label to affordance.
		// Indented to match the box's left padding so the arrow lines
		// up under the first content column.
		out += "\n  " + selflearnRecapFooterStyle.Render("↪ san --resume "+sessionID)
	}
	return out
}

// recapRowLine formats one action row: " · <target>" optionally
// followed by " — <note>". Single-space indent so the bullet sits
// directly under the kind sub-header without dragging the column
// further right.
func recapRowLine(a ReviewAction) string {
	target := a.Target
	if target == "" && a.Kind == "memory" {
		target = "index"
	}
	row := " · " + target
	if note := strings.TrimSpace(a.Note); note != "" {
		row += " — " + note
	}
	return selflearnRecapRowStyle.Render(row)
}

// recapKindStyle returns the per-kind sub-header style: blue for
// memory, purple for skill, dim for anything else.
func recapKindStyle(kind string) lipgloss.Style {
	switch kind {
	case "memory":
		return selflearnRecapMemoryStyle
	case "skill":
		return selflearnRecapSkillStyle
	default:
		return selflearnRecapKindStyle
	}
}

// selflearnRecap*Style — the recap sits inside a thin rounded box
// drawn in TextDim so the frame stays soft chrome. Inside, kind
// sub-headers carry the only color (memory blue, skill purple) and
// rows stay italic + TextDim.
var (
	selflearnRecapKindStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.TextDim).
				Italic(true)
	// Memory blue / skill purple — desaturated ~15-20% vs the previous
	// values so they blend with the overall muted/italic aesthetic
	// instead of pulling focus from chat content.
	selflearnRecapMemoryStyle = lipgloss.NewStyle().
					Foreground(kit.AdaptiveColor{Dark: "#82A0BA", Light: "#487192"}).
					Italic(true)
	selflearnRecapSkillStyle = lipgloss.NewStyle().
					Foreground(kit.AdaptiveColor{Dark: "#A89AC4", Light: "#745783"}).
					Italic(true)
	selflearnRecapRowStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.TextDim).
				Italic(true)
	// Box style — lipgloss-managed dashed border, soft TextDim corners.
	// Padding(0, 2) is the standard 2-col gutter inside the frame; the
	// card stays compact because there are no vertical padding rows.
	selflearnRecapBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.Border{
			Top:         "┄",
			Bottom:      "┄",
			Left:        "┊",
			Right:       "┊",
			TopLeft:     "╭",
			TopRight:    "╮",
			BottomLeft:  "╰",
			BottomRight: "╯",
		}).
		BorderForeground(kit.AdaptiveColor{Dark: "#4A4A52", Light: "#C8C8CC"}).
		Padding(0, 2)
	// Footer style for "↪ san --resume <id>" on its own line below the
	// box. TextDim + Faint so the command reads as a quiet hint, kept
	// upright so the shell command stays copy-paste recognisable.
	selflearnRecapFooterStyle = lipgloss.NewStyle().
					Foreground(kit.CurrentTheme.TextDim).
					Faint(true)
	// selflearnLiveStyle dresses the inline indicator row (the spinner
	// + target line that lives above the prompt while a review runs).
	// Italic + TextDim so it sits softly in the chat flow without
	// pulling focus from real messages.
	selflearnLiveStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim).Italic(true)
)

// memoryVerb maps a memory_write action to the recap-line verb.
func memoryVerb(action string) string {
	switch action {
	case "add":
		return "saved"
	case "replace":
		return "replaced"
	case "remove":
		return "removed"
	default:
		return action
	}
}

// skillVerb maps a skill_manage action to its recap verb. patch/edit
// collapse to "updated"; write_file/remove_file are support-file edits.
func skillVerb(action string) string {
	switch action {
	case "create":
		return "created"
	case "patch", "edit":
		return "updated"
	case "write_file":
		return "extended"
	case "remove_file":
		return "trimmed"
	case "delete":
		return "retired"
	default:
		return action
	}
}

// publishSelfLearnStarted signals the main loop that a review began (on the
// fork goroutine), so it can start the spinner from frame one. Non-blocking:
// a full channel drops the signal — the spinner just starts on the next one.
func (m *model) publishSelfLearnStarted() {
	select {
	case m.selfLearnStarts <- struct{}{}:
	default:
	}
}

// selfLearnStartedMsg wakes the Update loop when a review starts, so the
// spinner is scheduled on the main goroutine.
type selfLearnStartedMsg struct{}

// awaitSelfLearnStart yields a selfLearnStartedMsg each time a review starts.
func awaitSelfLearnStart(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return selfLearnStartedMsg{}
	}
}

// onSelfLearnStarted starts the spinner (at most once per active review) and
// re-arms the listener. TryStartTicker keeps back-to-back reviews from
// stacking parallel tick chains.
func (m *model) onSelfLearnStarted() tea.Cmd {
	next := awaitSelfLearnStart(m.selfLearnStarts)
	if m.services.SelfLearn.Indicator != nil && m.services.SelfLearn.Indicator.TryStartTicker() {
		return tea.Batch(scheduleSelflearnTick(), next)
	}
	return next
}

// publishSelfLearnFailure surfaces a terse failure notice; full details
// land in the log. Notice only — never re-submitted to the LLM.
func (m *model) publishSelfLearnFailure(kinds selflearn.ReviewKind, err error) {
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "review failed (see log)"
	}
	m.notifyMain(mainNotice{Display: fmt.Sprintf("Self-improvement review failed (%s): %s", kinds.String(), msg)})
}
