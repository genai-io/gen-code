package conv

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

// midSession is a representative snapshot: a measured total, every category
// populated, and one category (memory) far too small to earn a bar cell.
func midSession() ContextUsage {
	return ContextUsage{
		ModelName:    "claude-sonnet-5",
		Limit:        200000,
		Measured:     41200,
		SystemPrompt: 5300,
		Tools:        19700,
		MCPTools:     3100,
		Skills:       2200,
		MemoryFiles:  263,
		Messages:     13700,
	}
}

func TestScaleToMeasuredPartsSumToMeasured(t *testing.T) {
	u := midSession()
	cats := u.categories()

	estimated := 0
	for _, c := range cats {
		estimated += c.tokens
	}

	scaled := scaleToMeasured(cats, estimated, u.Measured)
	total := 0
	for _, c := range scaled {
		total += c.tokens
	}
	if total != u.Measured {
		t.Errorf("scaled parts sum to %d, want the measured total %d", total, u.Measured)
	}
}

// The rounding remainder must land somewhere real, not on a category that
// holds nothing — an empty category picking up stray tokens would render a
// row for a thing the session does not have.
func TestScaleToMeasuredKeepsEmptyCategoriesEmpty(t *testing.T) {
	u := ContextUsage{Limit: 200000, Measured: 41201, SystemPrompt: 5300, Messages: 13700}
	cats := u.categories()

	scaled := scaleToMeasured(cats, 19000, u.Measured)
	for i, c := range scaled {
		if cats[i].tokens == 0 && c.tokens != 0 {
			t.Errorf("%s was empty but scaled to %d", c.label, c.tokens)
		}
	}
}

// The bar's fill has to agree with the percentage printed above it. Giving
// every non-empty category a guaranteed cell is what breaks this: five tiny
// categories would show five cells the window is not actually holding.
func TestBarCellsFillMatchesHeaderPercent(t *testing.T) {
	u := midSession()
	cells := barCells(u.categories(), u.Limit, contextUsageBarWidth)

	filled := 0
	for _, n := range cells {
		filled += n
	}
	want := percentOf(filled, contextUsageBarWidth)
	got := percentOf(u.SystemPrompt+u.Tools+u.MCPTools+u.Skills+u.MemoryFiles+u.Messages, u.Limit)
	if diff := want - got; diff > 2 || diff < -2 {
		t.Errorf("bar reads %d%% full but the categories fill %d%%", want, got)
	}
}

func TestBarCellsNeverExceedWidth(t *testing.T) {
	full := ContextUsage{Limit: 100, Measured: 100, SystemPrompt: 40, Tools: 40, Messages: 40}
	cells := barCells(full.categories(), full.Limit, contextUsageBarWidth)

	filled := 0
	for _, n := range cells {
		filled += n
	}
	if filled > contextUsageBarWidth {
		t.Errorf("over-full context allocated %d cells, want at most %d", filled, contextUsageBarWidth)
	}
}

func TestRenderContextUsageShowsPopulatedCategoriesOnly(t *testing.T) {
	out := xansi.Strip(RenderContextUsage(midSession()))

	for _, label := range []string{"System prompt", "Tools", "MCP tools", "Skills", "Memory files", "Messages", "Free space"} {
		if !strings.Contains(out, label) {
			t.Errorf("missing category %q in:\n%s", label, out)
		}
	}
	if !strings.Contains(out, "41.2k") {
		t.Errorf("header should carry the measured total, got:\n%s", out)
	}
	if !strings.Contains(out, "total measured last turn") {
		t.Errorf("footer should name the measured total, got:\n%s", out)
	}
}

func TestRenderContextUsageOmitsEmptyCategories(t *testing.T) {
	out := xansi.Strip(RenderContextUsage(ContextUsage{
		ModelName: "gpt-5.5", Limit: 400000, SystemPrompt: 5300, Tools: 19700,
	}))

	if strings.Contains(out, "MCP tools") {
		t.Errorf("a session with no MCP server should not list MCP tools:\n%s", out)
	}
	if !strings.Contains(out, "no turn sent yet") {
		t.Errorf("an unmeasured session should say so, got:\n%s", out)
	}
}

// An unknown window has nothing to take a percentage of, so the bar, the
// percentage column, and free space all drop out rather than render against
// a guessed denominator.
func TestRenderContextUsageWithoutLimitDropsPercentages(t *testing.T) {
	out := xansi.Strip(RenderContextUsage(ContextUsage{
		ModelName: "some-local-model", Measured: 41200, SystemPrompt: 5300, Messages: 13700,
	}))

	if strings.Contains(out, "%") {
		t.Errorf("no window means no percentages, got:\n%s", out)
	}
	if strings.Contains(out, "Free space") {
		t.Errorf("free space is unknowable without a window, got:\n%s", out)
	}
	// Legend rows keep their single-cell color chip; only the bar goes away.
	if strings.Contains(out, "██") || strings.Contains(out, "░") {
		t.Errorf("the bar needs a window to fill, got:\n%s", out)
	}
	if !strings.Contains(out, "41.2k / --") {
		t.Errorf("unknown window should render as --, got:\n%s", out)
	}
}
