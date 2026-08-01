package conv

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
)

// maxAgentActivityLines is the maximum number of activity lines to display.
// Older lines scroll off the top, keeping the view compact.
const maxAgentActivityLines = 8

const (
	maxCompactAgentToolLines  = 3
	maxParallelAgentToolLines = 1
)

type AgentStats struct {
	Model        string
	InputTokens  int
	OutputTokens int
}

// renderAgentActivity renders the most recent agent activity lines,
// capped at maxAgentActivityLines to keep the view height bounded.
func renderAgentActivity(activity []string) string {
	if len(activity) == 0 {
		return ""
	}

	// Only show the most recent lines
	visible := activity
	if len(visible) > maxAgentActivityLines {
		visible = visible[len(visible)-maxAgentActivityLines:]
	}

	var sb strings.Builder
	for _, p := range visible {
		sb.WriteString(toolResultStyle.Render(fmt.Sprintf("  ⎿  %s", p)) + "\n")
	}
	return sb.String()
}

func renderAgentActivityInline(tc core.ToolCall, pendingCalls []core.ToolCall, taskActivity map[int][]string, expanded bool, limit int, stats AgentStats) string {
	idx := -1
	for i, pending := range pendingCalls {
		if pending.ID == tc.ID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ""
	}

	activity := taskActivity[idx]
	if expanded {
		return renderAgentActivity(activity)
	}
	return renderAgentActivityCompact(tc.Input, activity, limit, stats)
}

func renderAgentActivityCompact(input string, activity []string, limit int, stats AgentStats) string {
	var sb strings.Builder
	if summary := agentSummary(input, activity, stats); summary != "" {
		sb.WriteString(toolResultStyle.Render("  ⎿  "+summary) + "\n")
	}

	toolLines := agentToolLines(activity, limit)
	for _, line := range toolLines {
		sb.WriteString(toolResultStyle.Render("  ⎿  "+line) + "\n")
	}
	if len(toolLines) == 0 {
		sb.WriteString(toolResultStyle.Render("  ⎿  "+agentStatus(activity)) + "\n")
	}
	return sb.String()
}

func agentSummary(input string, activity []string, stats AgentStats) string {
	parts := make([]string, 0, 4)
	if model := agentModel(activity, stats.Model); model != "" {
		parts = append(parts, "model: "+model)
	}
	if mode := agentMode(input, activity); mode != "" {
		parts = append(parts, "mode: "+mode)
	}
	if n := len(agentToolLines(activity, 0)); n > 0 {
		parts = append(parts, fmt.Sprintf("tools: %d", n))
	}
	if tokens := agentTokens(activity, stats); tokens != "" {
		parts = append(parts, "tokens: "+tokens)
	}
	return strings.Join(parts, "   ")
}

func agentModel(activity []string, fallback string) string {
	for i := len(activity) - 1; i >= 0; i-- {
		if model, ok := strings.CutPrefix(strings.TrimSpace(activity[i]), "Model: "); ok {
			return strings.TrimSpace(model)
		}
	}
	return fallback
}

func agentMode(input string, activity []string) string {
	if mode := parseAgentInput(input).Mode; mode != "" {
		return mode
	}
	for _, line := range activity {
		if after, ok := strings.CutPrefix(line, "Mode: "); ok {
			mode, _, _ := strings.Cut(after, " · ")
			return strings.TrimSpace(mode)
		}
	}
	return "default"
}

func tokenSummary(inputTokens, outputTokens int) string {
	if inputTokens <= 0 && outputTokens <= 0 {
		return ""
	}
	return fmt.Sprintf("↑%s ↓%s", kit.FormatTokenCount(inputTokens), kit.FormatTokenCount(outputTokens))
}

func agentTokens(activity []string, stats AgentStats) string {
	for i := len(activity) - 1; i >= 0; i-- {
		inputTokens, outputTokens, ok := parseUsageActivity(activity[i])
		if ok {
			return tokenSummary(inputTokens, outputTokens)
		}
	}
	return tokenSummary(stats.InputTokens, stats.OutputTokens)
}

func parseUsageActivity(line string) (int, int, bool) {
	line = strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(line, "Usage: ")
	if !ok {
		return 0, 0, false
	}
	var inputTokens, outputTokens int
	for _, field := range strings.Fields(rest) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			continue
		}
		switch key {
		case "input":
			inputTokens = n
		case "output":
			outputTokens = n
		}
	}
	return inputTokens, outputTokens, inputTokens > 0 || outputTokens > 0
}

func agentStatus(activity []string) string {
	for i := len(activity) - 1; i >= 0; i-- {
		line := strings.TrimSpace(activity[i])
		if line == "" || isAgentToolLine(line) || strings.HasPrefix(line, "Mode: ") || strings.HasPrefix(line, "Model: ") || strings.HasPrefix(line, "Usage: ") {
			continue
		}
		return line
	}
	return "Starting..."
}

func agentToolLines(activity []string, limit int) []string {
	lines := make([]string, 0, len(activity))
	for _, line := range activity {
		if isAgentToolLine(line) {
			lines = append(lines, line)
		}
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}

func isAgentToolLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "Mode: ") || strings.HasPrefix(line, "Model: ") || strings.HasPrefix(line, "Usage: ") || line == "Thinking..." {
		return false
	}
	return true
}

// PendingToolSpinnerParams holds the parameters for rendering a pending tool spinner.
type PendingToolSpinnerParams struct {
	// DockedModalActive indicates a Question / Approval / secret modal owns
	// the screen, so the turn is parked on the user's answer.
	DockedModalActive bool
	// BuildingTool is the tool name being built during streaming.
	BuildingTool string
	// PendingCalls are the pending tool calls.
	PendingCalls []core.ToolCall
	// CurrentIdx is the index of the current sequential tool.
	CurrentIdx int
	// TaskActivity tracks agent activity messages by index.
	TaskActivity map[int][]string
	// SpinnerView is the current spinner frame.
	SpinnerView string
	// Blink drives the agent running icon.
	Blink int
	// AgentColors maps agent names to display colors.
	AgentColors map[string]string
	// Width is the terminal width for label truncation.
	Width int
	// SuppressAgentLabel avoids duplicating the active agent title when the
	// assistant message already rendered it above the activity lines.
	SuppressAgentLabel bool
}

// RenderPendingToolSpinner renders the spinner for a tool being executed.
func RenderPendingToolSpinner(params PendingToolSpinnerParams) string {
	if params.DockedModalActive {
		return ""
	}

	// Determine which tool is active
	var toolName string
	if params.BuildingTool != "" {
		toolName = params.BuildingTool
	} else if params.PendingCalls != nil && params.CurrentIdx < len(params.PendingCalls) {
		toolName = params.PendingCalls[params.CurrentIdx].Name
	} else {
		return ""
	}

	// Agent tool: render agent label + activity lines
	if tool.IsAgentToolName(toolName) {
		if params.SuppressAgentLabel {
			return ""
		}
		var sb strings.Builder
		// Show Agent label so it remains visible after the assistant message scrolls off.
		if params.PendingCalls != nil && params.CurrentIdx < len(params.PendingCalls) {
			tc := params.PendingCalls[params.CurrentIdx]
			agent := parseAgentInput(tc.Input)
			label := formatAgentLabel(agent)
			sb.WriteString(renderAgentToolLine(label, params.Width, agentIcon(params.Blink), configuredAgentColor(agent, params.AgentColors)) + "\n")
		}
		sb.WriteString(renderAgentActivity(params.TaskActivity[params.CurrentIdx]))
		return sb.String()
	}

	// Standard tools: spinner is shown inline in the assistant message row,
	// no separate spinner line needed.
	return ""
}
