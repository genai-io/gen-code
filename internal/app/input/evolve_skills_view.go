// Rendering for the /evolve panel: the config zone, the LEARNED inventory
// (skills + memory), and the full scrollable preview in modeView. Kept beside
// the panel's state/navigation (evolve_skills.go) so that file stays focused
// on the mode machine.
package input

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
)

const (
	viewPageStep   = 10 // lines per pgup/pgdown in the SKILL.md preview
	recentZoneRows = 4  // max activity rows shown in the RECENT zone
)

var errPreviewUnavailable = errors.New("preview unavailable")

// Render dispatches by mode: the base config form, or one of the drill-in
// sub-views (Learning editor, learned-skills inventory, scrollable preview).
// height is the content-row budget handed down by the popup shell.
func (p *evolvePanel) Render(width, height int) string {
	switch p.mode {
	case modeStrategy:
		return p.strategy.render(width, height)
	case modeView:
		return p.renderView(width, height)
	case modeList, modeConfirm:
		return p.renderLearned(width, height)
	default:
		return p.form.Render(width)
	}
}

// renderLearned is the drill-in inventory sub-view: the agent-created skills
// (browse / view / delete) with the recent-activity recap below.
func (p *evolvePanel) renderLearned(width, height int) string {
	recent := p.renderRecent(width)
	invBudget := max(3, height-strings.Count(recent, "\n")-1)
	return p.renderInventory(width, invBudget) + recent
}

// renderRecent draws the read-only RECENT zone: the last few skill-arm writes
// the reviewer made. Empty (no zone at all) when there's no activity yet, so an
// untouched panel isn't padded with a bare header.
func (p *evolvePanel) renderRecent(width int) string {
	if len(p.events) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(sectionRule("RECENT", width))
	b.WriteString("\n")
	shown := min(len(p.events), recentZoneRows)
	for _, e := range p.events[:shown] {
		b.WriteString(renderRecentRow(e, width))
		b.WriteString("\n")
	}
	return b.String()
}

// renderRecentRow formats one activity line: "· patched go-table-tests — note   2m ago".
func renderRecentRow(e LearnEvent, width int) string {
	head := "  " + recentVerbStyle.Render("· "+e.Verb+" ") + skillNameStyle.Render(e.Target)
	ago := selflearnMutedStyle.Render(e.Ago)
	note := ""
	if e.Note != "" {
		// Leave room for the right-aligned age plus a gap.
		noteWidth := width - lipgloss.Width(head) - lipgloss.Width(e.Ago) - 6
		if noteWidth > 6 {
			note = selflearnMutedStyle.Render(" — " + kit.TruncateText(e.Note, noteWidth))
		}
	}
	left := head + note
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(e.Ago), 1)
	return left + strings.Repeat(" ", gap) + ago
}

// renderInventory draws the learned list: agent-created skills then agent-written
// memory files, grouped by a SKILLS / MEMORY sub-header, windowed and
// cursor-aware, or an empty-state line when there's nothing. The "learned"
// header comes from the sub-view breadcrumb, so it isn't repeated here.
func (p *evolvePanel) renderInventory(width, budget int) string {
	var b strings.Builder

	if len(p.items) == 0 {
		b.WriteString(selflearnMutedStyle.Render("  Nothing learned yet."))
		b.WriteString("\n")
		b.WriteString(p.inventoryError())
		return b.String()
	}

	// Window the list around the cursor so a long inventory scrolls in place.
	// Reserve rows for everything that renders besides the items themselves:
	// one group sub-header per kind present, a possible error line, and the
	// ↑/↓ overflow markers once the list scrolls.
	reserved := 1 + countKindHeaders(p.items)
	if len(p.items)+reserved > budget {
		reserved += 2
	}
	rows := max(1, budget-reserved)
	start := 0
	if len(p.items) > rows && p.invCursor >= rows {
		start = p.invCursor - rows + 1
	}
	end := min(start+rows, len(p.items))
	if start > 0 {
		b.WriteString(selflearnMutedStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		// A group sub-header opens each kind's block (skills, then memory).
		if i == 0 || p.items[i].kind != p.items[i-1].kind {
			b.WriteString("  " + selflearnSubHeaderStyle.Render(kindHeader(p.items[i].kind)))
			b.WriteString("\n")
		}
		b.WriteString(p.renderItemRow(i, width))
		b.WriteString("\n")
	}
	if end < len(p.items) {
		b.WriteString(selflearnMutedStyle.Render(fmt.Sprintf("  ↓ %d more", len(p.items)-end)))
		b.WriteString("\n")
	}
	if p.mode == modeConfirm && p.invCursor < len(p.items) {
		b.WriteString(skillConfirmStyle.Render(
			fmt.Sprintf("  ⚠ Delete %q? ", p.items[p.invCursor].name)) +
			selflearnMutedStyle.Render("y confirm · n cancel"))
		b.WriteString("\n")
	}
	b.WriteString(p.inventoryError())
	return b.String()
}

// kindHeader maps an item kind to its group sub-header label.
func kindHeader(kind string) string {
	if kind == "memory" {
		return "MEMORY"
	}
	return "SKILLS"
}

// countKindHeaders returns how many group sub-headers the inventory renders —
// one per run of same-kind items (skills first, then memory, so at most two).
func countKindHeaders(items []learnedItem) int {
	n := 0
	for i, it := range items {
		if i == 0 || it.kind != items[i-1].kind {
			n++
		}
	}
	return n
}

func (p *evolvePanel) inventoryError() string {
	if p.actionErr == nil {
		return ""
	}
	return selflearnErrorStyle.Render("  ⚠ "+p.actionErr.Error()) + "\n"
}

// renderItemRow formats one inventory row (skill or memory): cursor caret, name,
// a subtitle column (skill scope / memory summary), and — for skills — a truncated
// description, with an inline action hint on the focused row.
func (p *evolvePanel) renderItemRow(i, width int) string {
	it := p.items[i]
	// Reachable only from the modeList/modeConfirm render path.
	focused := i == p.invCursor

	caret := strings.Repeat(" ", cursorWidth)
	nameStyle := skillNameStyle
	if focused {
		caret = selflearnCursorStyle.Render("▸ ")
		nameStyle = skillNameFocusStyle
	}

	name := padRight(it.name, 22)
	subtitle := padRight(it.subtitle, 9)
	line := caret + nameStyle.Render(name) + selflearnMutedStyle.Render(subtitle)

	// Skills carry a description; it fills the rest of the row, leaving room for
	// the focused-row action hint on the right.
	if it.desc != "" {
		used := cursorWidth + 22 + 9
		descWidth := width - used - 20
		if descWidth > 3 {
			line += selflearnMutedStyle.Render(kit.TruncateText(it.desc, descWidth))
		}
	}
	// The action hint rides only the focused list row — during a delete confirm
	// the prompt below the list carries the keys instead.
	if focused && p.mode == modeList {
		line += "  " + skillActionHintStyle.Render("enter view · d del")
	}
	return line
}

// renderView draws the scrollable, read-only SKILL.md preview: a title, a
// windowed slice of the file at viewOffset, and a line-position footer.
func (p *evolvePanel) renderView(width, height int) string {
	var b strings.Builder
	title := "SKILL.md · " + p.viewName
	if p.viewKind == "memory" {
		title = "MEMORY · " + p.viewName
	}
	b.WriteString(skillViewTitleStyle.Render(kit.TruncateText(title, width)))
	b.WriteString("\n\n")

	win := max(3, height-3)
	maxOffset := max(0, len(p.viewLines)-win)
	if p.viewOffset > maxOffset {
		p.viewOffset = maxOffset // clamp (idempotent — only ever lowers)
	}
	end := min(p.viewOffset+win, len(p.viewLines))
	for i := p.viewOffset; i < end; i++ {
		b.WriteString("  " + kit.TruncateText(p.viewLines[i], width-2))
		b.WriteString("\n")
	}

	if len(p.viewLines) > win {
		b.WriteString("\n")
		b.WriteString(selflearnMutedStyle.Render(
			fmt.Sprintf("  line %d–%d of %d", p.viewOffset+1, end, len(p.viewLines))))
	}
	return b.String()
}

// ── small helpers ────────────────────────────────────────────────────────

// sectionRule renders "LABEL ─────…" — a sub-header-styled label with a faint
// rule filling the rest of the row.
func sectionRule(label string, width int) string {
	up := strings.ToUpper(label)
	ruleLen := max(width-len(up)-1, 1)
	return selflearnSubHeaderStyle.Render(up) + " " +
		popupRuleStyle.Render(strings.Repeat("─", ruleLen))
}

func splitLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// padRight pads s with spaces to n columns, truncating (with an ellipsis) when
// it is already wider. padLeft is the same cell, right-aligned — for figures,
// which line up on their units rather than on their first digit.
//
// Both measure display width rather than counting runes, so a CJK name lands
// where it should instead of overhanging its column by one cell per character.
func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return kit.TruncateText(s, n)
}

func padLeft(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return kit.TruncateText(s, n)
}

var (
	skillNameStyle       = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text)
	skillNameFocusStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text).Bold(true)
	skillActionHintStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
	skillViewTitleStyle  = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text).Bold(true)
	// Delete confirm keeps a warning hue — a rare, destructive-action moment
	// worth one amber touch.
	skillConfirmStyle = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning).Bold(true)
	recentVerbStyle   = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
)
