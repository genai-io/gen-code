// Package conv: status-bar components — context bar, color tiers,
// compressions badge, and the responsive segment allocator. Pure
// functions over primitives; the orchestrator (RenderModeStatus) wires
// them to env state.
package conv

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

// Threshold percentages for the 4 PRD §7.2 color tiers.
const (
	pctGood     = 50.0
	pctWarn     = 80.0
	pctCritical = autoCompactThreshold // critical tier == auto-compact trigger

	// contextBarWidth is the cell count for the visual bar (PRD §7.1).
	contextBarWidth = 10
)

// contextTier classifies a context-window fill percentage into one of
// the 4 PRD §7.2 tiers. Off-by-one preserved: 80 itself falls into warn,
// only strictly-greater-than-80 is bad. ≥90 is critical.
type contextTier int

const (
	tierNone     contextTier = iota // pct unknown — denominator missing
	tierGood                        // [0, 50]    healthy
	tierWarn                        // (50, 80]   watch
	tierBad                         // (80, 90)   pressure
	tierCritical                    // [90, 100+] imminent compression
)

// classifyContextTier maps a percentage to its tier. Defensive for
// out-of-range inputs: pct < 0 returns tierGood, pct > 100 returns
// tierCritical. Renderers should still clamp for clean display.
func classifyContextTier(pct float64) contextTier {
	switch {
	case pct <= pctGood: // pct ≤ 50
		return tierGood
	case pct <= pctWarn: // 50 < pct ≤ 80
		return tierWarn
	case pct < pctCritical: // 80 < pct < 90
		return tierBad
	default: // pct ≥ 90
		return tierCritical
	}
}

// style resolves a tier to a lipgloss style composed from existing
// theme tokens (per project decision: no new theme infrastructure).
func (t contextTier) style() lipgloss.Style {
	switch t {
	case tierGood:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success)
	case tierWarn:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning)
	case tierBad:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)
	case tierCritical:
		// Critical = Error + Bold. Distinct from "bad" without adding a
		// new theme token.
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error).Bold(true)
	default: // tierNone
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	}
}

// RenderContextBar renders the 10-cell bar with a percentage label:
//
//	"[██████░░░░] 71%"   normal case
//	"[----------] --"    when limit is 0 (unknown)
//
// The percentage is rounded to an integer at this layer (PRD §4.2); the
// engine itself never rounds. `used` is clamped to [0, limit] before
// computing pct so callers cannot accidentally render negatives or >100%.
func RenderContextBar(used, limit int) string {
	if limit <= 0 {
		dim := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
		return dim.Render("[" + strings.Repeat("-", contextBarWidth) + "] --")
	}
	if used < 0 {
		used = 0
	}
	pct := float64(used) / float64(limit) * 100
	if pct > 100 {
		pct = 100
	}
	filled := int((pct/100)*float64(contextBarWidth) + 0.5) // round to nearest cell
	if filled > contextBarWidth {
		filled = contextBarWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", contextBarWidth-filled)
	style := classifyContextTier(pct).style()
	return style.Render(fmt.Sprintf("[%s] %d%%", bar, int(pct+0.5)))
}

// contextLabel renders the muted "ctx used/limit" segment. An empty limitText
// renders the limit as "--" (unknown).
func contextLabel(usedText, limitText string) string {
	muted := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	if limitText == "" {
		return muted.Render(fmt.Sprintf("ctx %s/--", usedText))
	}
	return muted.Render(fmt.Sprintf("ctx %s/%s", usedText, limitText))
}

// RenderContextLabel renders the "ctx X/Y" segment using compact
// humanized numbers (PRD §7.4). Limit renders as "--" when unknown.
func RenderContextLabel(used, limit int) string {
	if limit <= 0 {
		return contextLabel(kit.FormatTokenCount(used), "")
	}
	return contextLabel(kit.FormatTokenCount(used), kit.FormatTokenCount(limit))
}

// compressionBadgeStyle escalates color with count (PRD §7.5):
//
//	<5     muted
//	5–9    warn
//	≥10    error
func compressionBadgeStyle(n int) lipgloss.Style {
	switch {
	case n >= 10:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Error)
	case n >= 5:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning)
	default:
		return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	}
}

// RenderCompressionsBadge returns the "compacted ×N" badge or "" when n ≤ 0.
// Plain text (no emoji) keeps the status line aligned across terminals; color
// escalates with the count per compressionBadgeStyle.
func RenderCompressionsBadge(n int) string {
	if n <= 0 {
		return ""
	}
	return compressionBadgeStyle(n).Render(fmt.Sprintf("compacted ×%d", n))
}

// statusSegment is one keep-or-drop unit of the status line's right cluster.
// Under width pressure whole segments are dropped, never truncated mid-text.
type statusSegment struct {
	text     string // already-rendered (styled) text
	priority int    // 1 = most important (dropped last); larger drops first
}

// fitStatusSegments returns the segments that fit within maxWidth when joined
// by a separator of sepWidth visible columns, preserving their caller-supplied
// order. Under width pressure it drops the highest-priority-number segments
// first; survivors keep the original order so the layout still reads naturally.
func fitStatusSegments(segments []statusSegment, maxWidth, sepWidth int) []string {
	if maxWidth <= 0 || len(segments) == 0 {
		return nil
	}

	// Decide keep/drop in priority order (most important first) without
	// disturbing the original order the survivors are emitted in.
	byPriority := make([]int, len(segments))
	for i := range byPriority {
		byPriority[i] = i
	}
	sort.SliceStable(byPriority, func(a, b int) bool {
		return segments[byPriority[a]].priority < segments[byPriority[b]].priority
	})

	keep := make([]bool, len(segments))
	remaining := maxWidth
	kept := 0
	for _, i := range byPriority {
		need := lipgloss.Width(segments[i].text)
		if kept > 0 {
			need += sepWidth // every segment after the first costs a separator
		}
		if need > remaining {
			continue
		}
		keep[i] = true
		remaining -= need
		kept++
	}

	out := make([]string, 0, kept)
	for i, seg := range segments {
		if keep[i] {
			out = append(out, seg.text)
		}
	}
	return out
}

// OperationModeParams holds the parameters needed for rendering mode status.
type OperationModeParams struct {
	Mode              setting.OperationMode
	InputTokens       int
	InputLimit        int
	ModelName         string
	StatusMessage     string
	ConversationCost  llm.CostTotal
	Compressions      int  // session compact count, drives the "compacted ×N" badge
	ShowContextBar    bool // render the visual [██████░░░░] 71% bar (opt-in)
	Width             int
	ReviewApprovals   int  // auto-review approvals this session, shown next to the mode
	ReviewEscalations int  // auto-review escalations to the user this session
	AutopilotThinking bool // the copilot is mid-decision — show "thinking…" on the mode indicator
}

// RenderModeStatus renders the combined mode status line.
func RenderModeStatus(params OperationModeParams) string {
	left := RenderOperationModeIndicator(params.Mode, params.ReviewApprovals, params.ReviewEscalations, params.AutopilotThinking)

	right := renderStatusCluster(params)
	if right == "" || params.Width <= 0 {
		return left
	}

	gap := max(2, params.Width-lipgloss.Width(left)-lipgloss.Width(right)-1)
	return left + strings.Repeat(" ", gap) + right
}

// renderStatusCluster composes the status line's right-hand cluster, in
// display order: model name, optional transient status message, the numeric
// "ctx X/Y" label, the optional visual context bar, the optional compressions
// badge, and the optional cost. Each piece is a statusSegment with a drop
// priority; fitStatusSegments drops the least important first when the
// terminal is too narrow to hold them all.
func renderStatusCluster(p OperationModeParams) string {
	if p.ModelName == "" {
		return ""
	}
	muted := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	sep := muted.Render(" · ")

	// Priority 1 = most important (dropped last). The model name always
	// renders; everything else drops before it under width pressure.
	segments := []statusSegment{
		{text: muted.Render(p.ModelName), priority: 1},
	}
	if p.StatusMessage != "" {
		segments = append(segments, statusSegment{text: muted.Render(p.StatusMessage), priority: 2})
	}

	// The numeric label always renders — it falls back to "ctx X/--" when the
	// limit is unknown, so the slot stays visible instead of silently hiding.
	segments = append(segments, statusSegment{
		text:     RenderContextLabel(p.InputTokens, p.InputLimit),
		priority: 3,
	})

	// The visual bar is opt-in (off by default). When shown it also carries
	// the auto-compact hint as a near-full warning.
	if p.ShowContextBar {
		bar := RenderContextBar(p.InputTokens, p.InputLimit)
		if p.InputLimit > 0 {
			if hint := compactStatusHint(float64(p.InputTokens) / float64(p.InputLimit) * 100); hint != "" {
				bar += sep + muted.Render(hint)
			}
		}
		segments = append(segments, statusSegment{text: bar, priority: 4})
	}

	if badge := RenderCompressionsBadge(p.Compressions); badge != "" {
		segments = append(segments, statusSegment{text: badge, priority: 5})
	}
	if !p.ConversationCost.IsZero() {
		segments = append(segments, statusSegment{text: muted.Render(kit.FormatCostTotal(p.ConversationCost)), priority: 6})
	}

	survivors := fitStatusSegments(segments, p.Width, lipgloss.Width(sep))
	return strings.Join(survivors, sep)
}

func compactStatusHint(percent float64) string {
	switch {
	case percent >= pctCritical:
		return "auto-compact"
	case percent > pctWarn:
		return fmt.Sprintf("compact at %d%%", int(pctCritical))
	default:
		return ""
	}
}

// RenderOperationModeIndicator returns the mode status indicator for auto-accept, auto-review, or YOLO mode.
func RenderOperationModeIndicator(mode setting.OperationMode, reviewApprovals, reviewEscalations int, autopilotThinking bool) string {
	var icon, label string
	var clr kit.AdaptiveColor

	switch mode {
	case setting.ModeAutoAccept:
		icon = "⏵⏵"
		label = " accept edits"
		clr = kit.CurrentTheme.Success
	case setting.ModeAutoPilot:
		icon = "⏵⏵"
		label = " autopilot"
		clr = kit.CurrentTheme.Warning
	case setting.ModeBypassPermissions:
		icon = "⏵⏵"
		label = " YOLO"
		clr = kit.CurrentTheme.Yolo
	default:
		return ""
	}

	if mode == setting.ModeAutoPilot {
		if autopilotThinking {
			// The copilot is deciding — surface it here instead of a transcript line.
			label = " autopilot · thinking…"
		} else {
			if reviewApprovals > 0 {
				label += fmt.Sprintf(" · %d approved", reviewApprovals)
			}
			if reviewEscalations > 0 {
				label += fmt.Sprintf(" · %d escalated", reviewEscalations)
			}
		}
	}

	style := lipgloss.NewStyle().Foreground(clr)
	hint := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Render(" (shift+tab to cycle)")
	return "  " + style.Render(icon+label) + hint
}

// ModelStatusLabel composes the status line's model segment: the model name
// with its thinking effort in parentheses, e.g. "Opus 5 (high)". Effort is a
// property of the model, so it rides along with the name instead of claiming
// its own segment — one shape for every provider.
func ModelStatusLabel(modelName, effort string) string {
	if effort == "" || effort == "off" || effort == "none" {
		return modelName
	}
	return modelName + " (" + effort + ")"
}
