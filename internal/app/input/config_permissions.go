// /config Permissions panel: one radio group gating YOLO mode.
//
//   - YOLO MODE — allowed / locked. Allowed (the default) puts the mode in
//     the shift+tab cycle, one step past autopilot; locked removes it and
//     downgrades a "bypassPermissions" defaultMode to normal at startup.
//
// The setting is opt-out (`allowBypass`, nil = allowed), so this panel is
// how you take it away without hand-editing settings.json. It persists to
// the user settings file, matching the appearance panel's user-level scope.
//
// Scope, precisely: `allowBypass` is read in exactly two places — the
// shift+tab cycle (OperationMode.NextWithBypass) and the startup default
// (env.ApplyDefaultPermissionMode). A subagent that declares
// `mode: bypassPermissions` resolves through subagent.NormalizePermissionMode,
// which never consults it, and still runs unrestricted. So this is a lock on
// how *you* reach the mode, not a session-wide guarantee that nothing runs
// unprompted — don't describe it as one.
package input

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/setting"
)

// AllowBypassSavedMsg is emitted after the permissions panel persists the
// YOLO-mode gate so the app can refresh its settings handle, drop a session
// that is already in YOLO mode when the gate closes, and show a
// confirmation. The value is already written to disk by the time this fires.
type AllowBypassSavedMsg struct {
	Allowed bool
}

// yoloOption is one selectable row; allowed carries the value it applies.
type yoloOption struct {
	label   string
	desc    string
	allowed bool
}

func yoloOptions() []yoloOption {
	return []yoloOption{
		{label: "Allowed", desc: "shift+tab reaches YOLO mode — no permission prompts", allowed: true},
		{label: "Locked", desc: "shift+tab stops at autopilot", allowed: false},
	}
}

type permissionsPanel struct {
	settings *setting.Settings

	options []yoloOption
	cursor  int

	// baseline is the effective value on disk, marked "● current".
	baseline bool

	// saveErr holds the last failed persist so Render can surface it inline
	// instead of silently swallowing it. Cleared on navigation / re-entry.
	saveErr error
}

func newPermissionsPanel(settings *setting.Settings) *permissionsPanel {
	return &permissionsPanel{settings: settings}
}

func (p *permissionsPanel) Title() string { return "permissions" }

func (p *permissionsPanel) Enter() {
	p.options = yoloOptions()
	// Unset means allowed — mirror Settings.AllowBypass's opt-out default so a
	// fresh install shows "Allowed ● current" instead of a wrong "Locked".
	p.baseline = p.settings == nil || p.settings.AllowBypass()
	p.cursor = indexOfYolo(p.baseline)
	p.saveErr = nil
}

func (p *permissionsPanel) Dirty() bool {
	// Enter() populates options; guard so a Dirty() consulted before it (e.g.
	// a shell that polls every tab for an unsaved marker) cannot panic.
	if len(p.options) == 0 {
		return false
	}
	return p.options[p.cursor].allowed != p.baseline
}

func (p *permissionsPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
		p.saveErr = nil
	case "down", "j":
		if p.cursor < len(p.options)-1 {
			p.cursor++
		}
		p.saveErr = nil
	case "enter", " ":
		return p.apply(p.options[p.cursor])
	}
	return nil, false
}

// apply persists the hovered choice, advances the baseline, and returns the
// confirmation command. On failure it sets saveErr, keeps the popup open
// (done=false), and leaves the baseline untouched.
func (p *permissionsPanel) apply(opt yoloOption) (tea.Cmd, bool) {
	if err := setting.SaveAllowBypass(opt.allowed); err != nil {
		p.saveErr = err
		return nil, false
	}
	p.baseline = opt.allowed
	return func() tea.Msg { return AllowBypassSavedMsg{Allowed: opt.allowed} }, true
}

func (p *permissionsPanel) HintLine() string {
	return keycap("↑↓") + " navigate  " + keycap("enter") + " apply"
}

func (p *permissionsPanel) Render(width, _ int) string {
	var b strings.Builder

	b.WriteString(renderAppearanceSection("YOLO MODE", width))
	b.WriteString("\n\n")
	for i, opt := range p.options {
		b.WriteString(p.renderOption(i, opt))
		b.WriteString("\n")
	}

	// Two things the labels above would otherwise overpromise: what survives
	// the mode (deny rules and the / · ~ circuit breaker do; the confirmation
	// tiers don't), and how far "Locked" reaches — it gates the shift+tab
	// cycle, not a subagent that declares bypassPermissions for itself.
	b.WriteString("\n")
	b.WriteString(appearanceDescStyle.Render(
		"Deny rules and the / · ~ circuit breaker still hold. Other confirmations do not."))
	b.WriteString("\n")
	b.WriteString(appearanceDescStyle.Render(
		"Locking gates the shift+tab cycle only — subagents set to bypass are unaffected."))
	b.WriteString("\n")

	if p.saveErr != nil {
		b.WriteString("\n")
		b.WriteString(appearanceErrorStyle.Render("⚠ couldn't save: " + p.saveErr.Error()))
		b.WriteString("\n")
	}
	return b.String()
}

func (p *permissionsPanel) renderOption(i int, opt yoloOption) string {
	caret := "  "
	label := appearanceLabelStyle.Render(opt.label)
	if i == p.cursor {
		caret = appearanceCursorStyle.Render("▸ ")
		label = appearanceCursorStyle.Render(opt.label)
	}

	radio := appearanceRadioOffStyle.Render("○")
	current := ""
	if opt.allowed == p.baseline {
		radio = appearanceRadioOnStyle.Render("●")
		current = "  " + appearanceCurrentStyle.Render("current")
	}

	labelCell := label + strings.Repeat(" ", max(8-len(opt.label), 1))
	return caret + radio + " " + labelCell + appearanceDescStyle.Render(opt.desc) + current
}

// indexOfYolo returns the row index for an effective value.
func indexOfYolo(allowed bool) int {
	for i, opt := range yoloOptions() {
		if opt.allowed == allowed {
			return i
		}
	}
	return 0
}
