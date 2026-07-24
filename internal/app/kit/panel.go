package kit

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Panel is a sizing helper for the expansive full-screen selector layout
// shared by the model, skills, and agents overlays.
//
// All dimensions derive from the terminal size; callers feed in (width, height)
// from the TUI window-size message.
type Panel struct {
	Width  int // terminal width
	Height int // terminal height
}

func (p Panel) ContentWidth() int {
	return max(60, p.Width-6)
}

func (p Panel) BoxHeight() int {
	return max(18, p.Height-4)
}

// BodyHeight is BoxHeight minus space for tabs, search box, separators, and hints.
func (p Panel) BodyHeight() int {
	return max(6, p.BoxHeight()-10)
}

// SeparatorLine returns a horizontal rule that spans the full inner width
// of the panel (content width minus the box's horizontal Padding(_, 2)).
func (p Panel) SeparatorLine() string {
	style := lipgloss.NewStyle().Foreground(CurrentTheme.TextDim)
	return style.Render(strings.Repeat("─", max(1, p.ContentWidth()-4)))
}

// PadViewport pads the body content with blank lines so the panel keeps a
// stable height regardless of how many list items render.
func (p Panel) PadViewport(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	visible := p.BodyHeight()
	if visible <= 0 {
		return ""
	}
	for len(lines) < visible {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n") + "\n"
}

// Wrap renders the assembled content inside a fixed-size box and centers it
// near the top of the terminal — matching the /models overlay placement.
func (p Panel) Wrap(content string) string {
	box := lipgloss.NewStyle().
		Width(p.ContentWidth()).
		Height(p.BoxHeight()).
		Padding(1, 2).
		Render(content)
	return lipgloss.Place(p.Width, p.Height-2, lipgloss.Center, lipgloss.Top, box)
}

// PanelTab is one entry in the panel's tab header.
type PanelTab struct {
	Name    string
	Count   int  // appended after the name when > 0; count-less tabs show just the name
	Show    bool // when false the tab is hidden
	Disable bool // dim/grey the tab and skip it in navigation
}

// RenderPanelTabs renders a row of tab pills with the given active index.
// Disabled tabs are dimmed; hidden tabs are skipped.
func RenderPanelTabs(tabs []PanelTab, active int) string {
	activeStyle := lipgloss.NewStyle().
		Foreground(TabActiveFg).
		Background(TabActiveBg).
		Bold(true).
		Padding(0, 2)
	inactiveStyle := lipgloss.NewStyle().
		Foreground(CurrentTheme.TextDim).
		Padding(0, 2)
	disabledStyle := lipgloss.NewStyle().
		Foreground(CurrentTheme.TextDim).
		Faint(true).
		Padding(0, 2)

	var parts []string
	for i, t := range tabs {
		if !t.Show {
			continue
		}
		label := t.Name
		if t.Count > 0 {
			label = fmt.Sprintf("%s %d", t.Name, t.Count)
		}
		switch {
		case t.Disable:
			parts = append(parts, disabledStyle.Render(label))
		case i == active:
			parts = append(parts, activeStyle.Render(label))
		default:
			parts = append(parts, inactiveStyle.Render(label))
		}
	}
	return strings.Join(parts, "  ")
}

// SearchBoxOpts configures RenderSearchBox.
type SearchBoxOpts struct {
	Query       string // current search text
	Placeholder string // shown when Query is empty
	Filtered    int    // number of items after filter (shown only when Query != "")
	Total       int    // total items
	Width       int    // target inner width of the search box
}

// RenderSearchBox renders the magnifier-prefixed search input with optional
// "(filtered/total)" count, matching the /models overlay style.
func RenderSearchBox(opts SearchBoxOpts) string {
	// Fill the panel's inner content area; the panel adds Padding(_, 2) so
	// callers pass ContentWidth() and we subtract that 4 here.
	innerWidth := max(20, opts.Width-4)

	var text string
	switch {
	case opts.Query == "":
		text = " " + opts.Placeholder
	case opts.Total > 0:
		text = fmt.Sprintf(" %s▏ (%d/%d)", opts.Query, opts.Filtered, opts.Total)
	default:
		text = " " + opts.Query + "▏"
	}

	textFg := CurrentTheme.TextDim
	if opts.Query != "" {
		textFg = CurrentTheme.Text
	}
	return lipgloss.NewStyle().
		Foreground(textFg).
		Background(SearchBg).
		Padding(0, 1).
		Width(innerWidth).
		Render(text)
}

// MoreAbove returns the "↑ more above" pagination hint.
func MoreAbove() string {
	return DimStyle().PaddingLeft(2).Render("↑ more above")
}

// MoreBelow returns the "↓ more below" pagination hint.
func MoreBelow() string {
	return DimStyle().PaddingLeft(2).Render("↓ more below")
}

// HintLine renders a footer hint line in dim style.
func HintLine(parts ...string) string {
	return DimStyle().Render(strings.Join(parts, " · "))
}

// BadgeStyle returns the style used for inline source/scope badges
// (e.g. "[Plugin: foo]" or "[Project]") shown next to list items.
func BadgeStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(CurrentTheme.TextDim).
		Faint(true)
}
