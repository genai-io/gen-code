// Shared mutable app state: provider, permissions, and cache.
// Pure state holder — no singleton service dependencies.
package app

import (
	"strings"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/filecache"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/setting"
)

type env struct {
	// ── App-level state ─────────────────────────────────────────
	CWD           string
	IsGit         bool
	Width         int
	Height        int
	Ready         bool
	InitialPrompt string
	// Surface selects the active presentation: the default inline scrollback
	// view, or the opt-in full-screen desktop. See desktop_surface.go.
	Surface Surface

	// ── Provider (mutable — changes via SwitchProvider) ─────────
	LLMProvider  llm.Provider
	CurrentModel *llm.CurrentModelInfo
	// InputTokens / OutputTokens track the latest infer call only.
	// They back the bottom-right context display, so they reflect the most
	// recent prompt/output size rather than a turn or session aggregate.
	// InputTokens is the FULL prompt size (fresh + cached tokens) so the
	// context readout matches window occupancy even when prompt caching is
	// active — see InferResponse.TotalInputTokens.
	InputTokens  int
	OutputTokens int
	// CachedPrefixTokens is the latest infer call's cached prompt prefix
	// (creation + read). On a provider whose breakpoint sits at the end of the
	// system prompt, that prefix is exactly the tool definitions plus the
	// system prompt — an exact measurement /context uses in place of its
	// estimate. Zero when nothing cached: no turn yet, a provider that caches
	// on its own boundary, or a prefix below the model's cacheable minimum
	// (512–4096 tokens depending on the model), which is why the reading is
	// always corroborated rather than trusted outright.
	CachedPrefixTokens int
	// ConversationCost is the session-cumulative spend shown in the status
	// bar. It survives ResetContextDisplay (per-compaction) so compaction
	// doesn't erase prior spend; only ResetTokens (/clear, /new) zeroes it.
	ConversationCost llm.CostTotal
	ThinkingEffort   string
	// Compressions counts auto + manual compacts this session. Survives
	// ResetContextDisplay (called per-compact); zeroed only by ResetTokens
	// (called on /reset, /new).
	Compressions int
	// ShowContextBar mirrors the persisted appearance setting (off by
	// default): when true the status line renders the visual [██████░░░░] 71%
	// bar. Cached here so the hot render path never snapshots settings.
	// Set at startup and whenever the /config Appearance panel saves.
	ShowContextBar bool

	// ── Permission (mutable — changes per mode cycle) ───────────
	// Two views of one posture, kept in step by ApplyModePermissions.
	// OperationMode belongs to the UI goroutine: the status bar renders it and
	// Shift+Tab advances it. SessionPermissions carries the same mode behind a
	// mutex, and that is the copy the agent goroutine must read (CurrentMode or
	// a Snapshot) — the user can cycle modes while a turn is running.
	OperationMode      setting.OperationMode
	SessionPermissions *setting.SessionPermissions

	// AutoPilot is the live autopilot config for this session: seeded from
	// settings at startup, edited via the /autopilot panel, persisted with the
	// session, and restored on resume. The reviewer and mission responder read
	// it from here (not from a settings snapshot) so mid-session edits and
	// resumed configs take effect.
	AutoPilot setting.AutoPilotSettings

	// SessionName holds the custom session name set via /name. When non-empty
	// it overrides the auto-generated title on save. Restored from persisted
	// session metadata on resume.
	SessionName string

	// ── Cache (session-scoped) ──────────────────────────────────
	FileCache                 *filecache.Cache
	CachedUserInstructions    string
	CachedProjectInstructions string

	// ── Persistence handle (per-model thinking effort, etc.) ────
	// Held as a field so env-level setters can write through without
	// reaching for package globals; nil-safe in tests that bypass newEnv.
	store *llm.Store
}

func newEnv(llmConn *llm.Conn, cwd string, isGit bool) env {
	e := env{
		CWD:   cwd,
		IsGit: isGit,

		OperationMode:      setting.ModeNormal,
		SessionPermissions: setting.NewSessionPermissions(),

		LLMProvider:  llmConn.Provider(),
		CurrentModel: llmConn.CurrentModel(),

		FileCache: filecache.New(),
		store:     llmConn.Store(),
	}
	// Restore the user's prior per-model thinking-effort choice. Empty
	// means "use provider default" — EffectiveThinkingEffort handles that.
	if e.store != nil && e.CurrentModel != nil {
		e.ThinkingEffort = e.store.GetThinkingEffort(e.CurrentModel.ModelID)
	}
	return e
}

// SetThinkingEffort updates the in-memory thinking-effort selection and
// persists it for the current model. Call this for explicit user choices
// (Ctrl+T, /think); keyword-driven auto-bumps stay in-memory only, so a
// stray "ultrathink" in a prompt doesn't lock the model into the top tier.
func (m *env) SetThinkingEffort(effort string) {
	m.ThinkingEffort = effort
	if m.store == nil || m.CurrentModel == nil {
		return
	}
	if err := m.store.SetThinkingEffort(m.CurrentModel.ModelID, effort); err != nil {
		log.Logger().Warn("persist thinking effort",
			zap.String("model", m.CurrentModel.ModelID),
			zap.String("effort", effort),
			zap.Error(err))
	}
}

// LoadThinkingEffortFromStore refreshes ThinkingEffort from the persisted
// per-model preference. Called after switching models so each model recalls
// its own last-chosen effort.
func (m *env) LoadThinkingEffortFromStore() {
	if m.store == nil || m.CurrentModel == nil {
		m.ThinkingEffort = ""
		return
	}
	m.ThinkingEffort = m.store.GetThinkingEffort(m.CurrentModel.ModelID)
}

func (m *env) GetModelID() string {
	if m.CurrentModel != nil {
		return m.CurrentModel.ModelID
	}
	return ""
}

// GetModelDisplayName returns a human-readable display name for the current
// model by looking it up in the store's cached model list. Falls back to the
// raw model ID if no display name is found.
func (m *env) GetModelDisplayName() string {
	id := m.GetModelID()
	if id == "" {
		return "no model selected"
	}
	if m.store == nil {
		return id
	}
	if name := m.store.CachedModelDisplayName(id); name != "" {
		return name
	}
	return id
}

func (m *env) EffectiveThinkingEffort() string {
	return llm.ResolveThinkingEffortForModel(m.LLMProvider, m.store, m.CurrentModel, m.ThinkingEffort)
}

func (m *env) ThinkingEfforts() []string {
	return llm.ThinkingEffortsForModel(m.LLMProvider, m.store, m.CurrentModel)
}

func (m *env) OperationModeName() string {
	switch m.OperationMode {
	case setting.ModeAutoAccept:
		return "auto"
	case setting.ModeAutoPilot:
		return "autoPilot"
	case setting.ModeBypassPermissions:
		return "bypassPermissions"
	default:
		return "default"
	}
}

func (m *env) ResetSessionPermissions() {
	m.SessionPermissions.ResetPosture()
}

// applyEditPosture grants the accept-edits posture — edits/writes auto-approved,
// working dir trusted. Shared by accept-edits and auto-pilot (which additionally
// routes non-edit prompts to the review agent). ApplyModePermissions owns
// SessionPermissions.Mode; this only layers on the extra allowances.
func (m *env) applyEditPosture(cwd string) {
	m.SessionPermissions.GrantEditPosture(cwd)
}

func (m *env) DetectThinkingKeywords(input string) {
	lower := strings.ToLower(input)
	efforts := m.ThinkingEfforts()
	if len(efforts) == 0 {
		return
	}

	if strings.Contains(lower, "ultrathink") ||
		strings.Contains(lower, "think really hard") ||
		strings.Contains(lower, "think super hard") ||
		strings.Contains(lower, "maximum thinking") {
		m.ThinkingEffort = efforts[len(efforts)-1]
		return
	}

	if strings.Contains(lower, "think harder") ||
		strings.Contains(lower, "think hard") ||
		strings.Contains(lower, "think deeply") ||
		strings.Contains(lower, "think carefully") {
		if len(efforts) >= 2 {
			m.ThinkingEffort = efforts[len(efforts)-2]
		}
		return
	}
}

func (m *env) ApplyModePermissions(cwd string) {
	m.ResetSessionPermissions()
	// SessionPermissions.Mode always mirrors OperationMode; the switch below only
	// layers on the extra allowances for the edit modes. Bypass needs no extra
	// allowances — mirroring the mode is enough.
	m.SessionPermissions.SetMode(m.OperationMode)

	switch m.OperationMode {
	case setting.ModeAutoAccept, setting.ModeAutoPilot:
		m.applyEditPosture(cwd)
	}
}

func (m *env) ApplyDefaultPermissionMode(mode string, cwd string, allowBypass bool) {
	opMode := setting.OperationModeFromString(mode)
	if opMode == setting.ModeBypassPermissions && !allowBypass {
		opMode = setting.ModeNormal
	}
	m.OperationMode = opMode
	m.ApplyModePermissions(cwd)
}

func (m *env) ClearCachedInstructions() {
	m.CachedUserInstructions = ""
	m.CachedProjectInstructions = ""
}

// SessionMode is the mode label persisted with the session and stamped on each
// permission record. Called from the agent goroutine, so it reads the posture.
func (m *env) SessionMode() string {
	switch m.SessionPermissions.CurrentMode() {
	case setting.ModeAutoAccept:
		return "auto-accept"
	case setting.ModeAutoPilot:
		return "auto-pilot"
	default:
		return "normal"
	}
}

// resumeOperationMode is the mode a resumed session restores into: the one it
// recorded, or — for a session that recorded none, which is every session saved
// before modes were persisted — the mode the launch already established. Only a
// recorded mode may move the user; an absent one must not demote a startup
// accept-edits/autopilot posture to Normal behind their back.
func resumeOperationMode(recorded string, startup setting.OperationMode) setting.OperationMode {
	if recorded == "" {
		return startup
	}
	return parseSessionMode(recorded)
}

// parseSessionMode maps a persisted session mode back to the OperationMode a
// resumed session restores into, reusing the canonical string parser but folding
// everything except the two elevated cycle modes to Normal — so a resumed (or
// hand-edited) session can never silently regain bypass / dont-ask.
func parseSessionMode(mode string) setting.OperationMode {
	switch setting.OperationModeFromString(mode) {
	case setting.ModeAutoAccept:
		return setting.ModeAutoAccept
	case setting.ModeAutoPilot:
		return setting.ModeAutoPilot
	default:
		return setting.ModeNormal
	}
}

// ResetContextDisplay zeroes the bottom-right context-window readout (latest
// input/output tokens). Called per-compaction: the live context shrinks to the
// summary, so the bar/label restart from empty until the next infer. The
// cumulative ConversationCost is deliberately NOT reset here — compaction does
// not refund spend, so the session cost must survive it (see ResetTokens for
// the full reset on /clear, /new).
func (m *env) ResetContextDisplay() {
	m.InputTokens = 0
	m.OutputTokens = 0
	// The prefix measurement belongs to the same infer call as InputTokens;
	// keeping it past the reset would pair an exact prefix with an estimated
	// total and report a split neither number supports.
	m.CachedPrefixTokens = 0
}

// ResetTokens clears all token/cost accounting for a fresh session (/clear,
// /new). Unlike ResetContextDisplay this also zeroes the session-cumulative
// ConversationCost and the compaction counter.
func (m *env) ResetTokens() {
	m.ResetContextDisplay()
	m.ConversationCost = llm.CostTotal{}
	m.Compressions = 0
}
