// Package config provides multi-level settings management for San.
// Data are loaded from multiple sources with the following priority (lowest to highest):
//  1. ~/.claude/settings.json (Claude user level - compatibility)
//  2. ~/.san/settings.json (San user level)
//  3. .claude/settings.json (Claude project level - compatibility)
//  4. .san/settings.json (San project level)
//  5. .claude/settings.local.json (Claude local level - compatibility)
//  6. .san/settings.local.json (San local level)
//  7. Environment variables / CLI arguments
//  8. managed-settings.json (system level - cannot be overridden)
package setting

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/confdir"
)

// Data represents the complete San configuration.
type Data struct {
	Permissions    PermissionSettings `json:"permissions,omitempty"`
	Model          string             `json:"model,omitempty"`
	Hooks          map[string][]Hook  `json:"hooks,omitempty"`
	Env            map[string]string  `json:"env,omitempty"`
	EnabledPlugins map[string]bool    `json:"enabledPlugins,omitempty"`
	DisabledTools  map[string]bool    `json:"disabledTools,omitempty"`
	Theme          string             `json:"theme,omitempty"`
	SearchProvider string             `json:"searchProvider,omitempty"`
	// AllowBypass gates Bypass Permissions mode. Opt-out: nil/absent means
	// allowed (bypass is in the Shift+Tab cycle by default); set false to
	// lock it out. Read via Settings.AllowBypass().
	AllowBypass *bool `json:"allowBypass,omitempty"`
	// StreamFirstChunkTimeout overrides the core default (5m) for time-to-first-
	// chunk. A valid time.Duration string (e.g. "5m", "120s"); empty = core default.
	StreamFirstChunkTimeout string `json:"streamFirstChunkTimeout,omitempty"`
	// StreamIdleTimeout overrides the core default (60s) for the gap between
	// chunks once a stream has started. A valid time.Duration string (e.g. "60s",
	// "120s"); empty = core default.
	StreamIdleTimeout string `json:"streamIdleTimeout,omitempty"`
	// HookUITimeout bounds a hook the UI goroutine waits on (UserPromptSubmit,
	// SessionEnd) before it is cut short and the prompt proceeds. Raise it for a
	// legitimately slow prompt gate (e.g. a network-backed secret scanner). A
	// valid time.Duration string (e.g. "5s", "20s"); empty = the 5s default.
	HookUITimeout string `json:"hookUITimeout,omitempty"`
	// ContextBar toggles the visual context-usage bar ([██████░░░░] 71%) in
	// the status line. Pointer so an explicit "off" persists distinctly from
	// "unset"; nil (unset) means off — the bar is opt-in. The numeric
	// "ctx X/Y" label is unaffected and always shows.
	ContextBar *bool `json:"contextBar,omitempty"`
	// Persona selects an active persona directory under ~/.san/personas/<name>/
	// or .san/personas/<name>/. Empty = no persona override. The persona's own
	// settings.json is applied as the highest config overlay (see
	// ApplyPersonaOverlay).
	Persona string `json:"persona,omitempty"`
	// SelfLearn toggles + tunes the self-learning loop (per-turn background
	// review of memory and skills). Both arms are off by default (opt-in).
	SelfLearn SelfLearnSettings `json:"selfLearn,omitempty"`
	// AutoPilot configures the autopilot copilot: which lifecycle points it
	// steers, how it drives (model + system prompt), and the mission it steers
	// toward (JSON key "autoPilot").
	AutoPilot AutoPilotSettings `json:"autoPilot,omitempty"`
	// LastOperationMode is the user-wide mode restored when starting a new session.
	LastOperationMode string `json:"lastOperationMode,omitempty"`
}

// AutoPilotSettings tunes the autopilot copilot. Every field is optional;
// empty keeps the built-in defaults (session model + built-in system prompt, no
// mission, permission steer on).
type AutoPilotSettings struct {
	// Model overrides the model used for steer decisions. A bare id (e.g.
	// "claude-haiku-4-5") stays on the session provider; a "vendor/model" ref
	// (e.g. "anthropic/claude-haiku-4-5") routes to that connected provider.
	// Empty uses the session model.
	Model string `json:"model,omitempty"`
	// SystemPrompt replaces the built-in steering prompt inline (edited in the
	// /autopilot panel). Per-session, like Mission: it rides the transcript and
	// restores on /resume, but the panel does not write it as the new-session
	// default — that stays the built-in prompt, and a custom one is carried to
	// another session only via export/import. Takes precedence over
	// SystemPromptFile.
	SystemPrompt string `json:"systemPrompt,omitempty"`
	// SystemPromptFile loads the system prompt from a file — the settings.json
	// hook for a persistent custom default (the panel never sets it). Used only
	// when SystemPrompt is empty; empty falls back to the built-in system prompt.
	SystemPromptFile string `json:"systemPromptFile,omitempty"`
	// Mission is the per-session directive the copilot steers toward — the
	// briefing composed in the /autopilot Mission dialog.
	Mission string `json:"mission,omitempty"`
	// Steers selects which lifecycle points the copilot takes the helm at.
	Steers SteerSettings `json:"steers,omitempty"`
	// MaxContinuations caps how many times the TurnEnd steer may auto-continue
	// a finished turn before yielding to the human. 0 = the default cap,
	// negative = no cap (see AutoPilotUnlimitedContinuations).
	MaxContinuations int `json:"maxContinuations,omitempty"`
}

// SteerSettings toggles each point where the copilot steers the session.
// Field names track the trigger: turnEnd caps the turn; the middle three name
// the agent event that hands control over. (The mission kick-off is an explicit
// action — the panel's Start button — not a persisted steer.)
type SteerSettings struct {
	// Suggest is the feature switch for the input hint (on by default, in and
	// out of AutoPilot mode): while a mission is driven it proposes the next
	// step toward it, otherwise it predicts the human's next input. Tri-state
	// keeps an explicit off distinct from the default-on zero value.
	Suggest *bool `json:"suggest,omitempty"`
	// Permission auto-approves gray-zone tool calls. Tri-state so the baseline
	// can default on while an explicit off still persists: nil = on (autopilot's
	// whole point), false = escalate every gray-zone prompt to the human.
	Permission *bool `json:"permission,omitempty"`
	// BashPrompt answers a running command's interactive prompts.
	BashPrompt bool `json:"bashPrompt,omitempty"`
	// Skill approves the copilot's skill loads outright, without the judge — a
	// deliberate "trust skills" toggle separate from the Permission steer, since
	// the judge tends to escalate a skill load (it can run scripts).
	Skill bool `json:"skill,omitempty"`
	// Question answers an AskUserQuestion on the human's behalf.
	Question bool `json:"question,omitempty"`
	// TurnEnd auto-continues a finished turn toward the mission.
	TurnEnd bool `json:"turnEnd,omitempty"`
}

// SuggestOn reports whether automatic input hints are enabled. Unset defaults
// on; an explicit false from the AutoPilot panel disables them persistently.
func (s SteerSettings) SuggestOn() bool { return s.Suggest == nil || *s.Suggest }

// PermissionOn reports whether the permission steer is active. It defaults on
// because gray-zone approval is the baseline of autopilot; an explicit false
// makes the copilot escalate every gray-zone prompt to the human instead.
func (s SteerSettings) PermissionOn() bool { return s.Permission == nil || *s.Permission }

// AutoPilotDefaultMaxContinuations bounds TurnEnd auto-continuation when the
// config leaves maxContinuations unset — the single source of truth shared by
// the runtime driver and the /autopilot panel's default display.
const AutoPilotDefaultMaxContinuations = 20

// AutoPilotUnlimitedContinuations is the maxContinuations value that lifts the
// cap entirely: the copilot drives until the mission is done or it decides the
// run needs a human, rather than until it runs out of turns. For a long
// unattended run the cap is the wrong stopping rule — it fires on a step count
// that has nothing to do with whether the work is finished.
const AutoPilotUnlimitedContinuations = -1

// ContinuationsUnlimited reports whether auto-continuation is uncapped.
func (a AutoPilotSettings) ContinuationsUnlimited() bool { return a.MaxContinuations < 0 }

// EngageDriving switches on every steer that lets the copilot act without the
// human and lifts the continuation cap — the configuration behind /goal, where
// the run should end when the goal is met rather than when a counter expires.
// Permission is left alone: it defaults on, and an explicit off is a safety
// choice no stated goal should overrule.
func (a *AutoPilotSettings) EngageDriving() {
	a.Steers.BashPrompt = true
	a.Steers.Skill = true
	a.Steers.Question = true
	a.Steers.TurnEnd = true
	a.MaxContinuations = AutoPilotUnlimitedContinuations
}

// StopDriving turns off the steers that make the copilot act on its own,
// leaving the passive safety steers exactly as configured — the wind-down for a
// mission that ended without a goal to rewind to.
func (a *AutoPilotSettings) StopDriving() {
	off := false
	a.Steers.Suggest = &off
	a.Steers.Question = false
	a.Steers.TurnEnd = false
}

// ResolvedMaxContinuations returns the configured continuation cap, or the
// default when unset. Meaningless when ContinuationsUnlimited — check that
// first; it returns the default so a caller that forgets still gets a sane
// number rather than a negative one.
func (a AutoPilotSettings) ResolvedMaxContinuations() int {
	if a.MaxContinuations <= 0 {
		return AutoPilotDefaultMaxContinuations
	}
	return a.MaxContinuations
}

// Clone returns a deep copy, duplicating the tri-state permission pointer so
// callers can mutate the copy without touching the original.
func (a AutoPilotSettings) Clone() AutoPilotSettings {
	if p := a.Steers.Suggest; p != nil {
		v := *p
		a.Steers.Suggest = &v
	}
	if p := a.Steers.Permission; p != nil {
		v := *p
		a.Steers.Permission = &v
	}
	return a
}

// IsZero reports whether the config is entirely unset — used to keep the value
// out of persisted session state and settings files when nothing was configured.
func (a AutoPilotSettings) IsZero() bool {
	return a.Model == "" && a.SystemPrompt == "" && a.SystemPromptFile == "" &&
		a.Mission == "" && a.MaxContinuations == 0 &&
		a.Steers.Suggest == nil && a.Steers.Permission == nil &&
		!a.Steers.BashPrompt && !a.Steers.Skill && !a.Steers.Question && !a.Steers.TurnEnd
}

// Equal compares two configs by value, normalizing the tri-state permission
// steer via PermissionOn (nil and &true are equal). Used for the /autopilot
// panel's unsaved-edits check.
func (a AutoPilotSettings) Equal(b AutoPilotSettings) bool {
	return a.Model == b.Model &&
		a.SystemPrompt == b.SystemPrompt &&
		a.SystemPromptFile == b.SystemPromptFile &&
		a.Mission == b.Mission &&
		a.MaxContinuations == b.MaxContinuations &&
		a.Steers.SuggestOn() == b.Steers.SuggestOn() &&
		a.Steers.PermissionOn() == b.Steers.PermissionOn() &&
		a.Steers.BashPrompt == b.Steers.BashPrompt &&
		a.Steers.Skill == b.Steers.Skill &&
		a.Steers.Question == b.Steers.Question &&
		a.Steers.TurnEnd == b.Steers.TurnEnd
}

// SelfLearnSettings configures self-learning. Both arms are triggered together
// by the model (the Evolve tool); the per-arm fields are permission / store
// settings only. See notes/active/l1-background-review.md §3.1.
type SelfLearnSettings struct {
	Memory SelfLearnMemory `json:"memory,omitempty"`
	Skills SelfLearnSkills `json:"skills,omitempty"`

	// Strategy, when non-empty, replaces the built-in learning strategy in the
	// reviewer prompt (edited via the /evolve Strategy entry) — it steers both
	// the memory and skill sections. Empty ⇒ built-in.
	Strategy string `json:"strategy,omitempty"`
}

// SelfLearnMaxMemoryKB is the upper bound on memory.maxKB and the default
// when the config field is zero. It matches the injection cap
// (autoMemoryByteCap = 25 KB) so the on-disk per-file cap can never exceed
// what the loader would have to truncate — see §4.2 invariant.
const SelfLearnMaxMemoryKB = 25

// SelfLearnMemory controls memory-evolving. MaxKB is the on-disk cap per memory
// file; lower values force more aggressive pruning. May not exceed
// SelfLearnMaxMemoryKB.
type SelfLearnMemory struct {
	Enabled bool `json:"enabled,omitempty"`
	MaxKB   int  `json:"maxKB,omitempty"` // 0 = SelfLearnMaxMemoryKB

	// Path, when non-empty, overrides where the auto-memory store lives (the
	// /evolve Memory storage-path editor). "~" expands to home; a relative path
	// resolves against cwd. Empty ⇒ the project-partitioned default.
	Path string `json:"path,omitempty"`
}

// SelfLearnSkills controls skill-evolving. Triggering is model-decided (the
// Evolve tool); these fields only bound what a triggered review may do. The
// review is scoped by skill use: a turn that used a skill weighs UPDATE /
// DELETE of that skill, a skill-free turn weighs CREATE.
//
// There is no separate on/off toggle: skill-evolving is Active when at least
// one action is allowed. Action gates are encoded as Deny* booleans so the
// zero value is "allow" — the Go idiom of "zero value should be a sensible
// default" — and an omitted field gets the permissive default. (The JSON
// codecs below still read/write an explicit `enabled` marker for
// compatibility with the pre-permission schema.)
type SelfLearnSkills struct {
	// DenyCreate / DenyUpdate / DenyDelete gate the corresponding action on
	// agent-created skills. Zero ⇒ allowed; set to true to disable that action.
	// They bound what a model-triggered review may do to the skill set.
	DenyCreate bool `json:"denyCreate,omitempty"`
	DenyUpdate bool `json:"denyUpdate,omitempty"`
	DenyDelete bool `json:"denyDelete,omitempty"`
}

// wireSkills is the JSON shape of SelfLearnSkills: the Deny* gates plus an
// explicit `enabled` marker retained for compatibility with the legacy
// explicit-toggle schema (see the codec methods below).
type wireSkills struct {
	Enabled    bool `json:"enabled"`
	DenyCreate bool `json:"denyCreate,omitempty"`
	DenyUpdate bool `json:"denyUpdate,omitempty"`
	DenyDelete bool `json:"denyDelete,omitempty"`
}

// MarshalJSON always writes the `enabled` marker. Before skill-evolving became
// permission-driven, enabled:false was omitted by encoding/json and therefore
// commonly persisted as an empty skills object; the explicit marker lets
// UnmarshalJSON distinguish new permissive configurations from those legacy
// opt-outs.
func (s SelfLearnSkills) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireSkills{
		Enabled:    s.Active(),
		DenyCreate: s.DenyCreate,
		DenyUpdate: s.DenyUpdate,
		DenyDelete: s.DenyDelete,
	})
}

// UnmarshalJSON migrates the legacy explicit-toggle schema: a missing or false
// `enabled` (both decode to false) is the old disabled state, so all
// autonomous actions are denied. Files emitted by MarshalJSON always carry
// enabled:true when any action is allowed, so the permission gates round-trip
// exactly.
func (s *SelfLearnSkills) UnmarshalJSON(data []byte) error {
	var wire wireSkills
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*s = SelfLearnSkills{
		DenyCreate: wire.DenyCreate,
		DenyUpdate: wire.DenyUpdate,
		DenyDelete: wire.DenyDelete,
	}
	if !wire.Enabled {
		s.DenyCreate = true
		s.DenyUpdate = true
		s.DenyDelete = true
	}
	return nil
}

// AllowCreate / AllowUpdate / AllowDelete report whether the named action
// is permitted under the current configuration. These are the read paths
// the runtime takes — settings.json stores Deny*.
func (s SelfLearnSkills) AllowCreate() bool { return !s.DenyCreate }
func (s SelfLearnSkills) AllowUpdate() bool { return !s.DenyUpdate }
func (s SelfLearnSkills) AllowDelete() bool { return !s.DenyDelete }

// Active reports whether skill-evolving is on — i.e. at least one action is
// allowed. This replaces the old explicit Enabled toggle (implicit enable).
func (s SelfLearnSkills) Active() bool {
	return s.AllowCreate() || s.AllowUpdate() || s.AllowDelete()
}

// ResolvedMaxKB returns the resolved MaxKB (default SelfLearnMaxMemoryKB if zero).
func (m SelfLearnMemory) ResolvedMaxKB() int {
	if m.MaxKB <= 0 {
		return SelfLearnMaxMemoryKB
	}
	return m.MaxKB
}

// Validate enforces the cross-field invariants of §3.1: two illegal skill
// boolean combinations are rejected, and memory.maxKB must lie in [0, 25].
// Returns nil when the configuration is acceptable (including the all-zero
// "feature off" case).
//
// denyDelete is intentionally NOT constrained: "let the reviewer create and
// refine its own skills but never auto-delete them" is a legitimate
// conservative config. Delete is already restricted to agent-created skills,
// so opting out of it removes no safety.
func (s SelfLearnSettings) Validate() error {
	if s.Memory.MaxKB < 0 || s.Memory.MaxKB > SelfLearnMaxMemoryKB {
		return fmt.Errorf(
			"memory size must be between 1 and %d KB (got %d)",
			SelfLearnMaxMemoryKB, s.Memory.MaxKB,
		)
	}
	if s.Skills.AllowCreate() && !s.Skills.AllowUpdate() {
		return fmt.Errorf(
			"\"Create new skills\" needs \"Update a skill\" — otherwise created skills could never be refined",
		)
	}
	return nil
}

// ShowContextBar reports whether the visual context-usage bar is enabled.
// Nil (unset) resolves to off — the bar is opt-in.
func (s *Data) ShowContextBar() bool {
	return s != nil && s.ContextBar != nil && *s.ContextBar
}

// StartupMode is the operation mode a new session starts in: the mode the user
// last left, falling back to the configured default permission mode.
func (s *Data) StartupMode() string {
	if s == nil {
		return ""
	}
	return coalesce(s.LastOperationMode, s.Permissions.DefaultMode)
}

// PermissionSettings defines permission rules for tool execution.
// Rule format: "Tool(pattern)" — e.g. "Bash(npm:*)", "Read(**/.env)".
type PermissionSettings struct {
	DefaultMode string   `json:"defaultMode,omitempty"`
	Allow       []string `json:"allow,omitempty"`
	Deny        []string `json:"deny,omitempty"`
	Ask         []string `json:"ask,omitempty"`
}

// Hook defines an event hook configuration.
type Hook struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []HookCmd `json:"hooks,omitempty"`
}

type HookCmd struct {
	Type           string            `json:"type"`
	Command        string            `json:"command,omitempty"`
	Prompt         string            `json:"prompt,omitempty"`
	URL            string            `json:"url,omitempty"`
	If             string            `json:"if,omitempty"`
	Shell          string            `json:"shell,omitempty"`
	Model          string            `json:"model,omitempty"`
	Async          bool              `json:"async,omitempty"`
	AsyncRewake    bool              `json:"asyncRewake,omitempty"`
	Timeout        int               `json:"timeout,omitempty"`
	StatusMessage  string            `json:"statusMessage,omitempty"`
	Once           bool              `json:"once,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	AllowedEnvVars []string          `json:"allowedEnvVars,omitempty"`
}

// SessionPermissions tracks runtime permission state for the current session.
//
// It is shared across goroutines: the UI goroutine rewrites the posture when
// the user cycles the operation mode, while the agent goroutine reads it on
// every tool call. Mutations go through the methods below, which hold mu;
// readers take a Snapshot rather than reading fields directly, so one decision
// never straddles a posture change.
type SessionPermissions struct {
	mu sync.RWMutex

	Mode            OperationMode // Active permission mode (Normal, BypassPermissions, DontAsk, etc.)
	AllowAllEdits   bool
	AllowAllWrites  bool
	AllowAllBash    bool
	AllowAllSkills  bool
	AllowAllTasks   bool
	AllowedTools    map[string]bool
	AllowedPatterns map[string]bool
	Denials         DenialTracking // Tracks denial frequency for fallback

	// WorkingDirectories restricts Edit/Write operations to these directories.
	// When non-empty, file edits outside these dirs prompt for confirmation
	// (skipped in bypass mode). Set automatically when entering AutoAccept mode.
	WorkingDirectories []string

	// ShouldAvoidPrompts is set for headless/async subagents that cannot
	// show interactive dialogs. When true, ask → deny automatically.
	ShouldAvoidPrompts bool
}

func NewSessionPermissions() *SessionPermissions {
	return &SessionPermissions{
		AllowedTools:    make(map[string]bool),
		AllowedPatterns: make(map[string]bool),
	}
}

// Snapshot returns a detached copy safe to read without holding the lock.
// A permission decision reads the mode, the blanket allowances, the granted
// patterns and the working directories in sequence; taking them from one
// snapshot keeps the decision coherent even if the user cycles the operation
// mode mid-turn. Returns nil for a nil receiver, since callers pass an
// optional session through.
func (sp *SessionPermissions) Snapshot() *SessionPermissions {
	if sp == nil {
		return nil
	}
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	// Field-by-field rather than *sp: the copy gets its own zero mutex, and a
	// snapshot is read-only anyway.
	return &SessionPermissions{
		Mode:               sp.Mode,
		AllowAllEdits:      sp.AllowAllEdits,
		AllowAllWrites:     sp.AllowAllWrites,
		AllowAllBash:       sp.AllowAllBash,
		AllowAllSkills:     sp.AllowAllSkills,
		AllowAllTasks:      sp.AllowAllTasks,
		AllowedTools:       maps.Clone(sp.AllowedTools),
		AllowedPatterns:    maps.Clone(sp.AllowedPatterns),
		Denials:            sp.Denials,
		WorkingDirectories: slices.Clone(sp.WorkingDirectories),
		ShouldAvoidPrompts: sp.ShouldAvoidPrompts,
	}
}

// ResetPosture clears the blanket allowances and returns to Normal mode. One
// critical section, so a reader never observes a half-cleared posture.
func (sp *SessionPermissions) ResetPosture() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.AllowAllEdits = false
	sp.AllowAllWrites = false
	sp.AllowAllBash = false
	sp.AllowAllSkills = false
	sp.Mode = ModeNormal
}

// SetMode points the session at an operation mode without touching the
// allowances layered on top of it.
func (sp *SessionPermissions) SetMode(mode OperationMode) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.Mode = mode
}

// CurrentMode reads the active mode under the lock — Snapshot for callers that
// only need the mode, without cloning the allowances.
func (sp *SessionPermissions) CurrentMode() OperationMode {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.Mode
}

// GrantEditPosture auto-approves edits and writes and trusts cwd, as one
// atomic change — the accept-edits and auto-pilot posture.
func (sp *SessionPermissions) GrantEditPosture(cwd string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.AllowAllEdits = true
	sp.AllowAllWrites = true
	sp.addWorkingDirectoryLocked(cwd)
}

func (sp *SessionPermissions) AllowTool(toolName string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.AllowedTools == nil {
		sp.AllowedTools = make(map[string]bool)
	}
	sp.AllowedTools[toolName] = true
}

func (sp *SessionPermissions) AllowPattern(pattern string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.AllowedPatterns == nil {
		sp.AllowedPatterns = make(map[string]bool)
	}
	sp.AllowedPatterns[pattern] = true
}

func (sp *SessionPermissions) IsToolAllowed(toolName string) bool {
	if sp.AllowedTools[toolName] {
		return true
	}
	switch toolName {
	case "Edit":
		return sp.AllowAllEdits
	case "Write":
		return sp.AllowAllWrites
	case "Bash":
		return sp.AllowAllBash
	case "Skill":
		return sp.AllowAllSkills
	case "Agent":
		return sp.AllowAllTasks
	}
	return false
}

// AddWorkingDirectory adds a directory to the allowed working directories list.
func (sp *SessionPermissions) AddWorkingDirectory(dir string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.addWorkingDirectoryLocked(dir)
}

// addWorkingDirectoryLocked is the body of AddWorkingDirectory for callers
// that already hold mu (GrantEditPosture, which grants the directory and the
// edit allowances together).
func (sp *SessionPermissions) addWorkingDirectoryLocked(dir string) {
	// Avoid duplicates
	for _, d := range sp.WorkingDirectories {
		if d == dir {
			return
		}
	}
	sp.WorkingDirectories = append(sp.WorkingDirectories, dir)
}

// OperationMode defines the current operation mode.
type OperationMode int

const (
	ModeNormal            OperationMode = iota
	ModeAutoAccept                      // auto-approve edits/writes
	ModeBypassPermissions               // "YOLO mode": allow all except deny rules and the root/home-removal circuit breaker
	ModeDontAsk                         // convert ask → deny (never prompt)
	ModeReadOnly                        // safe tools only; everything else denied (subagent explore)
	ModeAutoPilot                       // auto-approve edits; delegate the rest to the review agent
)

// allModes lists the modes that the user can cycle through with the mode toggle.
// BypassPermissions is only reachable when explicitly enabled; DontAsk and
// ReadOnly are entered programmatically (headless subagents), not via cycling.
var cycleModes = []OperationMode{ModeNormal, ModeAutoAccept, ModeAutoPilot}
var cycleModesWithBypass = []OperationMode{ModeNormal, ModeAutoAccept, ModeAutoPilot, ModeBypassPermissions}

func (m OperationMode) String() string {
	switch m {
	case ModeAutoAccept:
		return "accept edits"
	case ModeAutoPilot:
		return "autopilot"
	case ModeBypassPermissions:
		return "yolo"
	case ModeDontAsk:
		return "don't ask"
	case ModeReadOnly:
		return "read-only"
	default:
		return "normal"
	}
}

func (m OperationMode) PersistenceName() string {
	switch m {
	case ModeAutoAccept:
		return "auto-accept"
	case ModeAutoPilot:
		return "auto-pilot"
	case ModeBypassPermissions:
		return "bypass"
	case ModeDontAsk:
		return "dont-ask"
	case ModeReadOnly:
		return "read-only"
	default:
		return "normal"
	}
}

func OperationModeFromString(mode string) OperationMode {
	mode = strings.TrimSpace(mode)
	switch mode {
	case "acceptEdits", "accept-edits", "autoAccept", "auto-accept":
		return ModeAutoAccept
	case "autoPilot", "auto-pilot", "autopilot", "pilot":
		return ModeAutoPilot
	// bypassPermissions is the wire name (Claude Code compatible); yolo is
	// what the UI calls it, accepted here so hand-written settings match.
	case "bypassPermissions", "bypass-permissions", "bypass", "yolo":
		return ModeBypassPermissions
	case "dontAsk", "dont-ask":
		return ModeDontAsk
	case "readOnly", "read-only":
		return ModeReadOnly
	default:
		return ModeNormal
	}
}

func (m OperationMode) Next() OperationMode {
	for i, mode := range cycleModes {
		if mode == m {
			return cycleModes[(i+1)%len(cycleModes)]
		}
	}
	// If current mode is not in the cycle list (e.g. BypassPermissions),
	// return to normal.
	return ModeNormal
}

// NextWithBypass cycles to the next operation mode.
// When enabled is true, BypassPermissions is included in the cycle.
func (m OperationMode) NextWithBypass(enabled bool) OperationMode {
	modes := cycleModes
	if enabled {
		modes = cycleModesWithBypass
	}
	for i, mode := range modes {
		if mode == m {
			return modes[(i+1)%len(modes)]
		}
	}
	return ModeNormal
}

func NewData() *Data {
	return &Data{
		Hooks:          make(map[string][]Hook),
		Env:            make(map[string]string),
		EnabledPlugins: make(map[string]bool),
		DisabledTools:  make(map[string]bool),
	}
}

// InitForApp loads settings for cwd, deep-clones them, and returns
// an isolated copy safe for mutation by the app layer.
// It also merges external provider preferences (e.g., search provider
// from providers.json) into the unified Data struct.
func InitForApp(cwd string) *Data {
	var (
		settings *Data
		err      error
	)
	if cwd != "" {
		settings, err = LoadForCwd(cwd)
	} else {
		settings, err = Load()
	}
	_ = err
	if settings == nil {
		settings = defaultData()
	}
	mergeProviderPreferences(settings)
	return settings.Clone()
}

// mergeProviderPreferences reads external provider config files and merges
// relevant preferences into Data. Currently reads searchProvider from
// ~/.san/providers.json (owned by the llm package) so that search config
// is accessible via the unified Data struct.
func mergeProviderPreferences(s *Data) {
	if s.SearchProvider != "" {
		return
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(confdir.Dir(homeDir), "providers.json"))
	if err != nil {
		return
	}
	var raw struct {
		SearchProvider *string `json:"searchProvider"`
	}
	if json.Unmarshal(data, &raw) == nil && raw.SearchProvider != nil {
		s.SearchProvider = *raw.SearchProvider
	}
}

// Clone returns a deep copy of the Data.
func (s *Data) Clone() *Data {
	if s == nil {
		return defaultData()
	}
	dst := NewData()
	dst.Permissions.DefaultMode = s.Permissions.DefaultMode
	dst.Permissions.Allow = append([]string(nil), s.Permissions.Allow...)
	dst.Permissions.Deny = append([]string(nil), s.Permissions.Deny...)
	dst.Permissions.Ask = append([]string(nil), s.Permissions.Ask...)
	dst.Model = s.Model
	dst.Theme = s.Theme
	dst.SearchProvider = s.SearchProvider
	dst.StreamFirstChunkTimeout = s.StreamFirstChunkTimeout
	dst.StreamIdleTimeout = s.StreamIdleTimeout
	dst.HookUITimeout = s.HookUITimeout
	dst.Persona = s.Persona
	dst.SelfLearn = s.SelfLearn // value-typed; shallow copy is correct
	dst.AutoPilot = s.AutoPilot.Clone()
	dst.LastOperationMode = s.LastOperationMode
	if s.AllowBypass != nil {
		v := *s.AllowBypass
		dst.AllowBypass = &v
	}
	if s.ContextBar != nil {
		v := *s.ContextBar
		dst.ContextBar = &v
	}
	for k, v := range s.Env {
		dst.Env[k] = v
	}
	for k, v := range s.EnabledPlugins {
		dst.EnabledPlugins[k] = v
	}
	for k, v := range s.DisabledTools {
		dst.DisabledTools[k] = v
	}
	for event, hooks := range s.Hooks {
		clonedHooks := make([]Hook, len(hooks))
		for i, hook := range hooks {
			clonedHooks[i].Matcher = hook.Matcher
			clonedHooks[i].Hooks = make([]HookCmd, len(hook.Hooks))
			for j, cmd := range hook.Hooks {
				clonedHooks[i].Hooks[j] = HookCmd{
					Type:           cmd.Type,
					Command:        cmd.Command,
					Prompt:         cmd.Prompt,
					URL:            cmd.URL,
					If:             cmd.If,
					Shell:          cmd.Shell,
					Model:          cmd.Model,
					Async:          cmd.Async,
					AsyncRewake:    cmd.AsyncRewake,
					Timeout:        cmd.Timeout,
					StatusMessage:  cmd.StatusMessage,
					Once:           cmd.Once,
					Headers:        maps.Clone(cmd.Headers),
					AllowedEnvVars: append([]string(nil), cmd.AllowedEnvVars...),
				}
			}
		}
		dst.Hooks[event] = clonedHooks
	}
	return dst
}
