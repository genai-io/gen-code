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
// Scope: `allowBypass` is read by OperationMode.NextWithBypass and
// env.ApplyDefaultPermissionMode, and nowhere else — subagent.
// NormalizePermissionMode never consults it, so an agent declaring
// `mode: bypassPermissions` runs unrestricted with the gate locked. This
// gates how you reach the mode, not what the session as a whole may do.
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

// yoloOptions is the row list, in display order. Allowed comes first so the
// cursor parks on the default at Enter.
var yoloOptions = []yoloOption{
	{label: "Allowed", desc: "shift+tab reaches YOLO mode — no permission prompts", allowed: true},
	{label: "Locked", desc: "shift+tab stops at autopilot", allowed: false},
}

type permissionsPanel struct {
	settings *setting.Settings

	cursor int

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
	// Unset means allowed — mirror Settings.AllowBypass's opt-out default so a
	// fresh install shows "Allowed ● current" instead of a wrong "Locked".
	p.baseline = p.settings == nil || p.settings.AllowBypass()
	p.cursor = 0
	for i, opt := range yoloOptions {
		if opt.allowed == p.baseline {
			p.cursor = i
			break
		}
	}
	p.saveErr = nil
}

func (p *permissionsPanel) Dirty() bool {
	return yoloOptions[p.cursor].allowed != p.baseline
}

func (p *permissionsPanel) HandleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
		p.saveErr = nil
	case "down", "j":
		if p.cursor < len(yoloOptions)-1 {
			p.cursor++
		}
		p.saveErr = nil
	case "enter", " ":
		return p.apply(yoloOptions[p.cursor])
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

func (p *permissionsPanel) HintLine() string { return radioHintLine() }

func (p *permissionsPanel) Render(width, _ int) string {
	var b strings.Builder

	b.WriteString(renderAppearanceSection("YOLO MODE", width))
	b.WriteString("\n\n")
	for i, opt := range yoloOptions {
		b.WriteString(renderRadioRow(opt.label, opt.desc, i == p.cursor, opt.allowed == p.baseline))
		b.WriteString("\n")
	}

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
