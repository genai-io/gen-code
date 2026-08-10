// Package conv: the /context breakdown — which parts of the prompt occupy
// the model's context window, as a stacked bar plus a legend. The status bar
// answers "how full is it"; this answers "what filled it".
package conv

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
)

// contextUsageBarWidth is the cell count of the stacked bar. Wider than the
// status bar's contextBarWidth because this bar carries six distinguishable
// segments rather than one fill level.
const contextUsageBarWidth = 40

// ContextUsage is one /context snapshot: the window being filled, the size
// the provider measured for the last turn, and the estimated split by
// category.
//
// The categories are named fields rather than a slice so a caller cannot
// reorder or relabel them by accident — render order, labels, and colors are
// decided once, in categories() below.
type ContextUsage struct {
	ModelName string

	// Limit is the model's context window, or 0 when San cannot size the
	// model. A zero limit drops the bar and the percentages; the token
	// counts still render.
	Limit int

	// Measured is the prompt size the provider reported for the last turn —
	// the same number the status bar shows as `ctx X/…`. It is 0 until a turn
	// completes, which falls the total back to the estimate.
	Measured int

	// CachedPrefix is the exact token count of the system prompt and the tool
	// definitions together, read from the provider's cache accounting. When it
	// is set, those categories and the conversation are each scaled to their
	// own exact total instead of sharing one — which stops an estimation error
	// in the tool schemas from being pushed onto Messages. Zero means no exact
	// prefix was available and the whole split scales to Measured.
	CachedPrefix int

	SystemPrompt int
	Tools        int
	MCPTools     int
	Skills       int
	MemoryFiles  int
	Messages     int
}

// contextCategory is one legend row and its slice of the stacked bar.
type contextCategory struct {
	label  string
	tokens int
	color  kit.AdaptiveColor
	// cached marks a category that sits inside the provider's cached prompt
	// prefix, and so is covered by ContextUsage.CachedPrefix. These come first
	// in render order, matching the order the provider renders the request in
	// (tools and system ahead of the conversation).
	cached bool
}

// categories lists the breakdown in render order, darkest to brightest. The
// ramp is the point: the leading segments are fixed session overhead the user
// configures once, and it brightens toward Messages — the one segment that
// grows every turn and the one /compact acts on — which takes the theme's
// single vivid accent.
func (u ContextUsage) categories() []contextCategory {
	t := kit.CurrentTheme
	return []contextCategory{
		{label: "System prompt", tokens: u.SystemPrompt, color: t.Separator, cached: true},
		{label: "Tools", tokens: u.Tools, color: t.Accent, cached: true},
		{label: "MCP tools", tokens: u.MCPTools, color: t.TextDim, cached: true},
		{label: "Skills", tokens: u.Skills, color: t.Primary},
		{label: "Memory files", tokens: u.MemoryFiles, color: t.Text},
		{label: "Messages", tokens: u.Messages, color: t.Focus},
	}
}

// cachedPrefixEnd returns how many leading categories the provider's cached
// prefix covers. Derived from the cached flag rather than a fixed index so
// reordering or adding a category can't silently shift the boundary.
func cachedPrefixEnd(cats []contextCategory) int {
	for i, c := range cats {
		if !c.cached {
			return i
		}
	}
	return len(cats)
}

// RenderContextUsage renders the /context body: a header naming the model and
// the fill, the stacked bar, one row per category, and a footer stating which
// numbers are measured and which are estimated.
func RenderContextUsage(u ContextUsage) string {
	cats := u.categories()

	used := 0
	for _, c := range cats {
		used += c.tokens
	}
	if u.Measured > 0 {
		cats = scaleToProvider(cats, u.Measured, u.CachedPrefix)
		used = u.Measured
	}

	free := max(u.Limit-used, 0)
	muted := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)

	var b strings.Builder
	b.WriteString(muted.Render(contextUsageHeader(u.ModelName, used, u.Limit)))
	b.WriteString("\n\n")
	if u.Limit > 0 {
		b.WriteString(renderStackedBar(cats, u.Limit))
		b.WriteString("\n\n")
	}
	b.WriteString(renderContextLegend(cats, free, u.Limit))
	b.WriteString("\n")
	b.WriteString(muted.Render(u.footer()))
	return b.String()
}

// contextUsageHeader is the "Context · <model> · 41.2k / 200k (21%)" line. An
// unknown window renders as "--" rather than a percentage of a guess, matching
// RenderContextLabel.
func contextUsageHeader(modelName string, used, limit int) string {
	parts := []string{"Context"}
	if modelName != "" {
		parts = append(parts, modelName)
	}
	if limit <= 0 {
		parts = append(parts, kit.FormatTokenCount(used)+" / --")
	} else {
		parts = append(parts, fmt.Sprintf("%s / %s (%d%%)",
			kit.FormatTokenCount(used), kit.FormatTokenCount(limit), percentOf(used, limit)))
	}
	return strings.Join(parts, " · ")
}

// footer names which numbers above it are measured and which are estimated, so
// the reading is never taken for more than it is.
func (u ContextUsage) footer() string {
	switch {
	case u.CachedPrefix > 0:
		return "prompt and tools measured last turn · conversation split estimated"
	case u.Measured > 0:
		return "total measured last turn · split estimated"
	default:
		return "estimated · no turn sent yet this session"
	}
}

// scaleToProvider fits the estimated split onto what the provider actually
// reported, so the headline total matches the status bar exactly and the parts
// still add up to it.
//
// With an exact prefix, the two groups are scaled separately — the cached
// categories to the prefix, the conversation to what remains. That containment
// is the point: scaling everything to one total spreads each category's
// estimation error across all of them, so an underestimated tool schema (JSON
// is punctuation-dense, and estimators read it low) quietly inflates Messages —
// the one category the user acts on. Two groups keep each error inside the
// group that produced it.
func scaleToProvider(cats []contextCategory, measured, cachedPrefix int) []contextCategory {
	scaled := make([]contextCategory, len(cats))
	copy(scaled, cats)

	if boundary := cachedPrefixEnd(scaled); cachedPrefix > 0 && cachedPrefix < measured {
		scaleGroup(scaled[:boundary], cachedPrefix)
		scaleGroup(scaled[boundary:], measured-cachedPrefix)
		return scaled
	}
	scaleGroup(scaled, measured)
	return scaled
}

// scaleGroup rescales one group of categories to sum to total. Rounding drift
// lands on the largest member, where it is proportionally smallest; an empty
// group is left alone rather than inventing tokens for categories the session
// does not have.
func scaleGroup(cats []contextCategory, total int) {
	estimated := 0
	for _, c := range cats {
		estimated += c.tokens
	}
	if estimated <= 0 {
		return
	}

	assigned, largest := 0, 0
	for i, c := range cats {
		cats[i].tokens = int(float64(c.tokens) / float64(estimated) * float64(total))
		assigned += cats[i].tokens
		if cats[i].tokens > cats[largest].tokens {
			largest = i
		}
	}
	cats[largest].tokens = max(cats[largest].tokens+total-assigned, 0)
}

// renderStackedBar draws every category end to end in its own color and fills
// the remainder with the free-space glyph.
func renderStackedBar(cats []contextCategory, limit int) string {
	cells := barCells(cats, limit, contextUsageBarWidth)

	var b strings.Builder
	filled := 0
	for i, c := range cats {
		if cells[i] == 0 {
			continue
		}
		b.WriteString(lipgloss.NewStyle().Foreground(c.color).Render(strings.Repeat("█", cells[i])))
		filled += cells[i]
	}
	if free := contextUsageBarWidth - filled; free > 0 {
		b.WriteString(freeStyle().Render(strings.Repeat("░", free)))
	}
	return b.String()
}

// barCells apportions the filled part of the bar across the categories.
//
// The total fill is pinned to the header's percentage first: a bar that reads
// fuller than the number printed above it is a worse lie than one that hides a
// small category. Those cells are then handed out by largest remainder, so a
// category earns a cell only against the competing claims — a 263-token memory
// file appears in the legend but not in the bar, which is the honest picture
// of what it costs.
func barCells(cats []contextCategory, limit, width int) []int {
	cells := make([]int, len(cats))

	used := 0
	for _, c := range cats {
		used += c.tokens
	}
	target := min(int(float64(used)/float64(limit)*float64(width)+0.5), width)
	if used <= 0 || target <= 0 {
		return cells
	}

	remainders := make([]float64, len(cats))
	assigned := 0
	for i, c := range cats {
		exact := float64(c.tokens) / float64(used) * float64(target)
		cells[i] = int(exact)
		remainders[i] = exact - float64(cells[i])
		assigned += cells[i]
	}
	// Flooring above loses less than one cell per category, so there are
	// strictly fewer leftovers than categories and each can claim at most one.
	for ; assigned < target; assigned++ {
		claimant := 0
		for i := range remainders {
			if remainders[i] > remainders[claimant] {
				claimant = i
			}
		}
		cells[claimant]++
		remainders[claimant] = -1
	}
	return cells
}

// renderContextLegend renders one row per category, then the free-space row,
// with the token and percentage columns right-aligned so the numbers stack.
//
// Empty categories are left out: a session with no MCP server and no memory
// file should read as a short list, not as a fixed form with zeros in it.
// Free space is only meaningful against a known window, so an unknown limit
// drops that row and the whole percentage column with it.
func renderContextLegend(cats []contextCategory, free, limit int) string {
	rows := make([]contextCategory, 0, len(cats)+1)
	for _, c := range cats {
		if c.tokens > 0 {
			rows = append(rows, c)
		}
	}
	if limit > 0 {
		rows = append(rows, contextCategory{label: "Free space", tokens: free, color: kit.CurrentTheme.TextDisabled})
	}

	labelWidth := 0
	for _, r := range rows {
		labelWidth = max(labelWidth, len(r.label))
	}

	var b strings.Builder
	for i, r := range rows {
		glyph := "█"
		if limit > 0 && i == len(rows)-1 {
			glyph = "░" // the free-space row, matching the bar's tail
		}
		b.WriteString(contextLegendRow(glyph, r.color, r.label, labelWidth, r.tokens, limit))
	}
	return b.String()
}

func contextLegendRow(glyph string, color kit.AdaptiveColor, label string, labelWidth, tokens, limit int) string {
	row := fmt.Sprintf("%s  %-*s %8s",
		lipgloss.NewStyle().Foreground(color).Render(glyph),
		labelWidth, label,
		kit.FormatTokenCount(tokens))
	if limit > 0 {
		// One decimal, unlike the header's whole percent: a memory file worth
		// 0.1% of the window should not round away to nothing.
		row += fmt.Sprintf(" %6.1f%%", float64(tokens)/float64(limit)*100)
	}
	return row + "\n"
}

func freeStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDisabled)
}

// percentOf rounds a share of the window to a whole percent for display.
func percentOf(part, whole int) int {
	if whole <= 0 {
		return 0
	}
	return int(float64(part)/float64(whole)*100 + 0.5)
}
