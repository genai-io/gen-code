// Generic settings-popup shell. PanelPopup frames one or more sub-panels in a
// centered rounded card with a title lockup ("✦ Self-Learning"), a tab strip
// when multiple panels are registered, a faint hairline, and the active
// panel's body + hint. Each sub-panel implements Panel and owns its body.
//
// Two popups are built on this shell today:
//   - /config  → Appearance + Permissions panels (Provider planned).
//   - /evolve  → one self-learning panel covering both arms (skills + memory).
//
// To add a panel to either, implement Panel and append it in the popup's
// constructor (NewConfigSelector / NewEvolveSelector).
package input

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/setting"
)

// Panel is one popup sub-panel.
//
// Lifecycle:
//   - Enter(): reset working state on (re)activation.
//   - HandleKey(): handle one keypress; (cmd, done) — done=true asks the
//     shell to dismiss the popup (e.g. after Save).
//   - Render(width, height): the panel body, framed by the shell. height is
//     the content rows available so a panel can window a scrollable region.
//   - HintLine(): the muted bottom hint (e.g. "↑↓ navigate · …"). Shown
//     under the body; the shell appends its own "· esc close".
type Panel interface {
	Title() string
	Enter()
	HandleKey(msg tea.KeyMsg) (tea.Cmd, bool)
	Render(width, height int) string
	HintLine() string
	// Dirty reports whether the panel has unsaved edits. The shell uses
	// this to pin a "● unsaved" tag to the top-right of the header.
	Dirty() bool
}

// modalPanel is an optional Panel capability. While a panel is showing a
// sub-view (a scrollable preview, a text editor, a confirm prompt), it returns
// Modal()=true so the shell routes esc and ←→ to the panel via HandleKey —
// driving the sub-view — instead of dismissing the popup or switching tabs.
type modalPanel interface {
	Modal() bool
}

// breadcrumbPanel is an optional Panel capability: a panel inside a sub-view
// returns the sub-view's name so the shell can show "‹title› › ‹panel› › ‹sub›"
// in the header lockup (and hide the tab strip while you're deep in it).
type breadcrumbPanel interface {
	Breadcrumb() string
}

// PanelPopup is a centered rounded-card settings popup hosting one or more
// Panels behind a title lockup + tab strip. Adding a sibling panel is a
// one-liner in the popup's constructor.
type PanelPopup struct {
	glyph   string // title-lockup glyph, e.g. "✦"
	title   string // wordmark, e.g. "Self-Learning"
	tagline string // muted tagline shown when not inside a sub-view

	panels []Panel
	index  int

	active bool
	width  int
	height int
}

// newPanelPopup wires a popup with its title lockup and set of panels. Order is
// preserved by the tab strip.
func newPanelPopup(glyph, title, tagline string, panels ...Panel) PanelPopup {
	return PanelPopup{glyph: glyph, title: title, tagline: tagline, panels: panels}
}

// NewConfigSelector builds the /config popup: Appearance and Permissions,
// with Provider planned as a sibling panel.
func NewConfigSelector(settings *setting.Settings) PanelPopup {
	return newPanelPopup("⚙", "Config", "appearance & settings",
		newAppearancePanel(settings), newPermissionsPanel(settings))
}

// Enter activates the popup, re-focusing whichever panel was last open (the
// index survives between openings, so /config reopens where you left it).
func (c *PanelPopup) Enter(width, height int) {
	c.width = width
	c.height = height
	c.active = true
	if c.index >= len(c.panels) {
		c.index = 0
	}
	if p := c.activePanel(); p != nil {
		p.Enter()
	}
}

// IsActive implements the popup interface.
func (c *PanelPopup) IsActive() bool { return c.active }

// HandleKeypress implements the popup interface. Esc dismisses the popup;
// Tab / Shift+Tab cycle the tab strip (skills ↔ memory); everything else —
// including ←/→, which the panels use for the user/project scope control —
// delegates to the active panel.
func (c *PanelPopup) HandleKeypress(msg tea.KeyMsg) tea.Cmd {
	if !c.active {
		return nil
	}
	p := c.activePanel()
	if p == nil {
		return nil
	}
	// A panel showing a sub-view (Modal) captures esc / Tab itself so those keys
	// drive the sub-view rather than dismissing the popup or switching tabs.
	// Otherwise the shell owns them.
	if !panelIsModal(p) {
		switch msg.String() {
		case "esc":
			c.active = false
			return nil
		case "tab", "shift+tab":
			// Only intercept when there's more than one panel; otherwise fall
			// through so the active panel can handle it.
			if len(c.panels) > 1 {
				step := 1
				if msg.String() == "shift+tab" {
					step = -1
				}
				c.index = (c.index + step + len(c.panels)) % len(c.panels)
				c.panels[c.index].Enter()
				return nil
			}
		}
	}
	cmd, done := p.HandleKey(msg)
	if done {
		c.active = false
	}
	return cmd
}

// panelIsModal reports whether the panel currently owns a sub-view (see
// modalPanel). Panels that don't implement modalPanel are never modal.
func panelIsModal(p Panel) bool {
	mp, ok := p.(modalPanel)
	return ok && mp.Modal()
}

// pasteSink is an optional Panel capability: a panel with an open text editor
// (the Strategy editor) accepts bracketed paste. Bracketed paste arrives as
// tea.PasteMsg, not a tea.KeyMsg, so it never reaches HandleKey.
type pasteSink interface {
	HandlePaste(content string)
}

// HandlePaste implements the app's pasteHandler: it routes bracketed paste to
// the active panel's text editor when one is open, so pasted strategy text
// lands in the textarea instead of leaking into the prompt behind the popup.
func (c *PanelPopup) HandlePaste(content string) tea.Cmd {
	if !c.active || content == "" {
		return nil
	}
	if ps, ok := c.activePanel().(pasteSink); ok {
		ps.HandlePaste(content)
	}
	return nil
}

// Render frames the active panel in a centered rounded card: the title lockup +
// tab strip, a faint hairline, the panel body, and the hint line. The column is
// built at exactly innerWidth and the card adds border+padding around it
// without re-setting a width, so nothing re-wraps.
func (c *PanelPopup) Render() string {
	if !c.active || len(c.panels) == 0 {
		return ""
	}
	p := c.activePanel()
	w := c.innerWidth()

	header := c.renderHeader(w)
	headerLines := strings.Count(header, "\n") + 1
	// Rows left for the body after the header, the hairline, the blank lines,
	// the hint, and the card's border + padding + a screen margin.
	bodyHeight := max(4, c.height-headerLines-12)

	rule := popupRuleStyle.Render(strings.Repeat("─", w))

	body := p.Render(w, bodyHeight)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(rule)
	b.WriteString("\n\n")
	b.WriteString(body)
	b.WriteString("\n\n")
	b.WriteString(c.renderHint(p.HintLine()))

	col := lipgloss.NewStyle().Width(w).Render(b.String())
	card := popupCardStyle.Render(col)
	// Center on both axes so the card sits balanced in the terminal.
	return lipgloss.Place(c.width, c.height-2, lipgloss.Center, lipgloss.Center, card)
}

// innerWidth is the card's content column — a generous fill of the terminal so
// the panel reads as a confident, roomy card, capped so rows don't sprawl on an
// ultra-wide screen. The -10 leaves room for the card's border + padding and a
// screen margin.
func (c *PanelPopup) innerWidth() int { return min(max(c.width-10, 40), 120) }

// ActivePanel returns the currently focused panel; nil when none are
// registered. Exported for tests.
func (c *PanelPopup) ActivePanel() Panel { return c.activePanel() }

func (c *PanelPopup) activePanel() Panel {
	if len(c.panels) == 0 {
		return nil
	}
	return c.panels[c.index]
}

// renderHeader renders the title lockup ("✦ Self-Learning" + tagline, or a
// "› panel › sub-view" breadcrumb when inside a sub-view), an "● unsaved" tag
// pinned right, and the tab strip below when more than one panel is registered.
func (c *PanelPopup) renderHeader(width int) string {
	left := popupGlyphStyle.Render(c.glyph+" ") + popupTitleStyle.Render(c.title)
	crumb := c.activeBreadcrumb()
	switch {
	case crumb != "":
		// Multi-panel popups show "title › panel › sub-view"; a single-panel
		// popup drops the redundant panel name ("title › sub-view").
		if len(c.panels) > 1 {
			left += popupCrumbDimStyle.Render("  ›  ") + popupCrumbStyle.Render(c.activePanel().Title())
		}
		left += popupCrumbDimStyle.Render("  ›  ") + popupCrumbStyle.Render(crumb)
	case c.tagline != "":
		left += popupTaglineStyle.Render("   " + c.tagline)
	}

	line1 := left
	if p := c.activePanel(); p != nil && p.Dirty() {
		right := popupUnsavedDotStyle.Render("●") + " " + popupUnsavedTextStyle.Render("unsaved")
		gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 1)
		line1 = left + strings.Repeat(" ", gap) + right
	}

	// Tab strip only at the top level (not while a sub-view breadcrumb shows).
	if len(c.panels) > 1 && crumb == "" {
		return line1 + "\n\n" + c.renderTabs()
	}
	return line1
}

// renderTabs draws the skills/memory tab strip as prominent uppercase pills —
// the active one filled in the accent, the rest a dim label. Roomier than the
// shared kit strip so the tabs read as the panel's primary navigation.
func (c *PanelPopup) renderTabs() string {
	parts := make([]string, len(c.panels))
	for i, p := range c.panels {
		label := strings.ToUpper(p.Title())
		if i == c.index {
			parts[i] = popupTabActiveStyle.Render(label)
		} else {
			parts[i] = popupTabIdleStyle.Render(label)
		}
	}
	return strings.Join(parts, "  ")
}

func (c *PanelPopup) activeBreadcrumb() string {
	if bp, ok := c.activePanel().(breadcrumbPanel); ok {
		return bp.Breadcrumb()
	}
	return ""
}

// renderHint builds the bottom hint. A modal sub-view states its own keys, so
// the shell adds nothing; otherwise it appends "esc close" and, for a
// multi-panel popup, the tab-switch hint.
func (c *PanelPopup) renderHint(panelHint string) string {
	if panelIsModal(c.activePanel()) {
		return kit.HintLine(panelHint)
	}
	parts := []string{}
	if panelHint != "" {
		parts = append(parts, panelHint)
	}
	parts = append(parts, "esc close")
	if len(c.panels) > 1 {
		parts = append(parts, "tab switch panel")
	}
	return kit.HintLine(parts...)
}

var (
	// Rounded card that frames the whole popup.
	popupCardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(kit.CurrentTheme.Border).
			Padding(1, 2)

	// Title lockup: the teal glyph is the one accent touch; the wordmark is
	// plain bold text and the tagline a muted italic.
	popupGlyphStyle    = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Focus)
	popupTitleStyle    = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text).Bold(true)
	popupTaglineStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Italic(true)
	popupCrumbDimStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
	popupCrumbStyle    = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text).Bold(true)

	// Barely-there hairline under the header — a soft divider, not a heading.
	popupRuleStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim).Faint(true)

	// "● unsaved" tag — muted, so it's noticeable without shouting in a hue.
	popupUnsavedDotStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Bold(true)
	popupUnsavedTextStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)

	// Tab strip: the active tab is a filled teal pill (the one accent),
	// uppercased and padded so the tabs read as the panel's primary nav; the
	// rest are dim labels.
	popupTabActiveStyle = lipgloss.NewStyle().
				Background(kit.CurrentTheme.Focus).
				Foreground(kit.CurrentTheme.Background).
				Bold(true).
				Padding(0, 2)
	popupTabIdleStyle = lipgloss.NewStyle().
				Foreground(kit.CurrentTheme.TextDim).
				Bold(true).
				Padding(0, 2)
)
