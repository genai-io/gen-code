// Package kit provides utility functions and styles used across UI packages.
package kit

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"github.com/genai-io/san/internal/secret"
)

func FuzzyMatch(str, pattern string) bool {
	pi := 0
	for si := 0; si < len(str) && pi < len(pattern); si++ {
		if str[si] == pattern[pi] {
			pi++
		}
	}
	return pi == len(pattern)
}

func CalculateBoxWidth(screenWidth int) int {
	boxWidth := screenWidth - 8
	return max(40, min(boxWidth, 60))
}

func CalculateToolBoxWidth(screenWidth int) int {
	boxWidth := screenWidth * 80 / 100
	return max(60, boxWidth)
}

// TruncateText shortens text to at most maxLen *display columns* (not runes),
// appending a single-glyph ellipsis (…) when it has to cut. Width-aware: CJK
// and other 2-cell glyphs are budgeted at their real column count, so the
// result never overflows a maxLen-wide slot (a rune count would undercount
// wide glyphs and let the row wrap). Returns the original text if maxLen <= 0
// or it already fits.
func TruncateText(text string, maxLen int) string {
	if maxLen <= 0 || lipgloss.Width(text) <= maxLen {
		return text
	}
	if maxLen == 1 {
		return "…"
	}
	// Accumulate width rune by rune, reserving one column for the ellipsis, and
	// stop once the next rune would overflow. O(n), unlike a shrink-from-the-end
	// loop that re-measures the whole string each step.
	//
	// Measured with runewidth rather than lipgloss.Width: one rune cannot
	// contain an escape sequence, so the ANSI-stripping state machine has
	// nothing to do, and this runs per rune per row on every frame.
	budget := maxLen - 1
	width := 0
	var b strings.Builder
	for _, r := range text {
		rw := runewidth.RuneWidth(r)
		if width+rw > budget {
			break
		}
		width += rw
		b.WriteRune(r)
	}
	return b.String() + "…"
}

// TruncateKeepEnd trims text to maxLen display columns keeping its tail, with
// a leading ellipsis — the mirror of TruncateText, for values whose end
// identifies them (a path's filename, a long id). Measured in columns and cut
// on rune boundaries, so CJK neither overflows the slot nor splits.
func TruncateKeepEnd(text string, maxLen int) string {
	if maxLen <= 0 || lipgloss.Width(text) <= maxLen {
		return text
	}
	if maxLen == 1 {
		return "…"
	}
	budget := maxLen - 1
	runes := []rune(text)
	width := 0
	i := len(runes)
	for i > 0 {
		rw := lipgloss.Width(string(runes[i-1]))
		if width+rw > budget {
			break
		}
		width += rw
		i--
	}
	return "…" + string(runes[i:])
}

func ShortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

func ShortenPathForProject(path, cwd string) string {
	if after, ok := strings.CutPrefix(path, cwd); ok {
		rel := after
		rel = strings.TrimPrefix(rel, "/")
		if rel != "" {
			return rel
		}
	}
	return ShortenPath(path)
}

// RenderSelectableRow renders a list row, prefixing the focused one with the
// teal FocusBar (the label aligns at the same column as unselected rows).
func RenderSelectableRow(line string, isSelected bool) string {
	if isSelected {
		return "  " + FocusBarStyle().Render(FocusBar) + " " + SelectorSelectedLabelStyle().Render(line)
	}
	return SelectorItemStyle().Render("  " + line)
}

// RenderPanelRow is RenderSelectableRow for the expansive full-screen panels
// (skills, agents): both variants right-pad to width so the row edges line up
// with the panel separators, and the focused row gets the teal FocusBar.
func RenderPanelRow(line string, isSelected bool, width int) string {
	if isSelected {
		bar := FocusBarStyle().Render(FocusBar)
		label := SelectorSelectedLabelStyle().Width(max(1, width-1)).Render(" " + line)
		return bar + label
	}
	return SelectorItemLabelStyle().Width(width).Render("  " + line)
}

// AlignedRowMinGap is the minimum spacing kept between the name and info
// columns, so names longer than colWidth never collide with the info column.
const AlignedRowMinGap = 2

// FormatAlignedRow formats "icon  name<padding>info" with name padded to
// colWidth and always separated from info by at least AlignedRowMinGap spaces.
func FormatAlignedRow(icon, name string, colWidth int, info string) string {
	gap := max(
		// display width, ANSI/Unicode safe
		colWidth-lipgloss.Width(name), AlignedRowMinGap)
	return fmt.Sprintf("%s  %s%s%s", icon, name, strings.Repeat(" ", gap), info)
}

// MapString extracts a string value from a generic map.
func MapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// RenderEnvVarStatus returns a styled "ENVVAR ✓" or "ENVVAR ✗" indicator.
func RenderEnvVarStatus(envVar string) string {
	if envVar == "" {
		return ""
	}
	// The env-var name is secondary reference info (kept dim); the check mark
	// carries the signal — green ✓ when configured, dim ✗ when not.
	name := DimStyle().Render(envVar)
	if secret.Resolve(envVar) != "" {
		return name + " " + SelectorStatusConnected().Render("✓")
	}
	return name + " " + SelectorStatusNone().Render("✗")
}
