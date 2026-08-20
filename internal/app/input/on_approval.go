package input

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
)

// ApprovalModel manages the permission request UI with Claude Code style.
type ApprovalModel struct {
	active       bool
	request      *perm.PermissionRequest
	diffPreview  *approvalDiffPreview
	bashPreview  *approvalBashPreview
	skillPreview *approvalSkillPreview
	agentPreview *approvalAgentPreview
	width        int
	selectedIdx  int
}

// NewApproval creates a new ApprovalModel instance
func NewApproval() ApprovalModel {
	return ApprovalModel{
		selectedIdx: 0,
	}
}

func (p *ApprovalModel) setRequest(req *perm.PermissionRequest, width int) {
	p.active = true
	p.request = req
	p.width = width
	p.selectedIdx = 0

	if req.DiffMeta != nil {
		p.diffPreview = newApprovalDiffPreview(req.DiffMeta, req.FilePath)
	} else {
		p.diffPreview = nil
	}

	if req.BashMeta != nil {
		p.bashPreview = newApprovalBashPreview(req.BashMeta)
	} else {
		p.bashPreview = nil
	}

	if req.SkillMeta != nil {
		p.skillPreview = newApprovalSkillPreview(req.SkillMeta)
	} else {
		p.skillPreview = nil
	}

	if req.AgentMeta != nil {
		p.agentPreview = newApprovalAgentPreview(req.AgentMeta)
	} else {
		p.agentPreview = nil
	}
}

// Show displays the permission prompt with the given request.
func (p *ApprovalModel) Show(req *perm.PermissionRequest, width, height int) {
	p.setRequest(req, width)
}

// Hide hides the permission prompt
func (p *ApprovalModel) Hide() {
	p.active = false
	p.request = nil
	p.diffPreview = nil
	p.bashPreview = nil
	p.skillPreview = nil
	p.agentPreview = nil
}

// IsActive returns whether the prompt is visible
func (p *ApprovalModel) IsActive() bool {
	return p.active
}

// TogglePreview toggles the expand state of diff/bash previews.
func (p *ApprovalModel) TogglePreview() {
	if p.diffPreview != nil {
		p.diffPreview.toggleExpand()
	}
	if p.bashPreview != nil {
		p.bashPreview.toggleExpand()
	}
}

// GetRequest returns the current permission request
func (p *ApprovalModel) GetRequest() *perm.PermissionRequest {
	return p.request
}

// ApprovalResponseMsg is sent when the user responds to a permission request
type ApprovalResponseMsg struct {
	Approved bool
	AllowAll bool
	Persist  bool
	Request  *perm.PermissionRequest
}

// HandleKeypress consumes a keypress while the modal is open. When the user
// makes a decision it emits the ApprovalResponseMsg as a command, so the root
// model handles the result in Update like any other message. That keeps the
// modal a uniform overlayPanel rather than a special case in the key router.
func (p *ApprovalModel) HandleKeypress(msg tea.KeyMsg) tea.Cmd {
	cmd, resp := p.handleKeypress(msg)
	if resp == nil {
		return cmd
	}
	out := *resp
	return tea.Batch(cmd, func() tea.Msg { return out })
}

// handleKeypress advances modal state for a keypress, returning a response
// once the user has made a decision.
func (p *ApprovalModel) handleKeypress(msg tea.KeyMsg) (tea.Cmd, *ApprovalResponseMsg) {
	if !p.active {
		return nil, nil
	}
	options := buildApprovalOptionRows(p.request)

	switch msg.String() {
	case "up", "ctrl+p":
		if p.selectedIdx > 0 {
			p.selectedIdx--
		}
		return nil, nil

	case "down", "ctrl+n":
		if p.selectedIdx < len(options)-1 {
			p.selectedIdx++
		}
		return nil, nil

	case "enter":
		return p.respondAt(options, p.selectedIdx)

	case "shift+tab":
		// shift+tab is the accelerator for "Yes, allow all this session".
		// Look it up by AllowAll instead of hardcoding the index so the
		// accelerator survives reordering.
		for i, opt := range options {
			if opt.AllowAll {
				return p.respondAt(options, i)
			}
		}
		return nil, nil

	case "ctrl+o":
		if p.diffPreview != nil {
			p.diffPreview.toggleExpand()
		}
		if p.bashPreview != nil {
			p.bashPreview.toggleExpand()
		}
		return nil, nil

	case "esc", "ctrl+c":
		// Esc/Ctrl+C maps to whichever option carries no approval — the
		// modal must always offer one such row (a "No" or equivalent).
		for i, opt := range options {
			if !opt.Approved {
				return p.respondAt(options, i)
			}
		}
		return nil, nil
	}

	// Digit keys: "1".."9" map to options by 1-based index. "y"/"n" remain
	// shortcuts for the first approved / first non-approved row.
	if s := msg.String(); len(s) == 1 {
		if s[0] >= '1' && s[0] <= '9' {
			if idx := int(s[0] - '1'); idx < len(options) {
				return p.respondAt(options, idx)
			}
		}
		switch s {
		case "y", "Y":
			for i, opt := range options {
				if opt.Approved && !opt.AllowAll && !opt.Persist {
					return p.respondAt(options, i)
				}
			}
		case "n", "N":
			for i, opt := range options {
				if !opt.Approved {
					return p.respondAt(options, i)
				}
			}
		}
	}

	return nil, nil
}

func (p *ApprovalModel) respondAt(options []approvalOption, i int) (tea.Cmd, *ApprovalResponseMsg) {
	if i < 0 || i >= len(options) {
		return nil, nil
	}
	opt := options[i]
	req := p.request
	p.Hide()
	return nil, &ApprovalResponseMsg{
		Approved: opt.Approved, AllowAll: opt.AllowAll, Persist: opt.Persist, Request: req,
	}
}

// --- Approval style helpers ---

func approvalSeparatorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Separator)
}

func approvalQuestionStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
}

func approvalSelectedStyle() lipgloss.Style {
	// Neutral bright+bold for the focused option; the teal FocusBar carries the
	// "selected" signal. Green is reserved for genuine success/confirm states.
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextBright).Bold(true)
}

func approvalUnselectedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
}

func approvalHintStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Italic(true)
}

func approvalFooterStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
}

func approvalTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Primary).Bold(true)
}

func (p *ApprovalModel) renderInline() string {
	if !p.active || p.request == nil {
		return ""
	}

	var sb strings.Builder
	contentWidth := max(p.width-2, 40)

	title := p.getTitle()
	sb.WriteString(" ")
	sb.WriteString(approvalTitleStyle().Render(title))
	sb.WriteString("\n\n")

	if p.diffPreview != nil {
		sb.WriteString(p.diffPreview.render(contentWidth))
	} else if p.bashPreview != nil {
		sb.WriteString(p.bashPreview.render(contentWidth))
	} else if p.skillPreview != nil {
		sb.WriteString(p.skillPreview.render(contentWidth))
	} else if p.agentPreview != nil {
		sb.WriteString(p.agentPreview.render(contentWidth))
	}
	sb.WriteString("\n")

	sb.WriteString(" ")
	sb.WriteString(approvalQuestionStyle().Render("Do you want to proceed?"))
	sb.WriteString("\n")

	sb.WriteString(p.renderMenu())
	sb.WriteString("\n")

	footer := " Esc to cancel"
	hasExpandableContent := (p.diffPreview != nil && len(p.diffPreview.diffMeta.Lines) > approvalDefaultMaxVisibleLines) ||
		(p.bashPreview != nil && p.bashPreview.needsExpand())
	if hasExpandableContent {
		footer += " · Ctrl+O expand"
	}
	sb.WriteString(approvalFooterStyle().Render(footer))
	sb.WriteString("\n")

	solidSep := strings.Repeat("─", contentWidth)
	sb.WriteString(approvalSeparatorStyle().Render(solidSep))

	return sb.String()
}

func (p *ApprovalModel) getTitle() string {
	var title string
	switch p.request.ToolName {
	case "Edit":
		title = "Edit file"
	case "Write":
		title = "Write to file"
	case "Bash":
		title = "Bash command"
	case tool.ToolSkill:
		title = "Load skill"
	case tool.ToolAgent, tool.ToolSendMessage:
		title = "Spawn agent"
	default:
		title = p.request.Description
	}

	if p.request.CallerAgent != "" {
		title = "@" + p.request.CallerAgent + " · " + title
	}
	return title
}

// approvalOption is one row of the approval modal: its label, an optional
// keyboard hint, and the response flags that fire when the user picks it.
// Behavior + presentation collapsed into one struct so adding a new option
// is a single append to buildApprovalOptionRows — renderer, keyboard
// dispatch, and trace emit all stay in sync automatically.
type approvalOption struct {
	Label    string
	Hint     string
	Approved bool
	AllowAll bool
	Persist  bool
}

// buildApprovalOptionRows is the single source of truth for the modal's
// option set. Adding "this directory only", removing "Always allow",
// reordering — happens here and propagates to renderMenu, HandleKeypress,
// and BuildApprovalOptions automatically.
func buildApprovalOptionRows(req *perm.PermissionRequest) []approvalOption {
	return []approvalOption{
		{Label: "Yes", Approved: true},
		{Label: allSessionLabel(req), Hint: "(shift+tab)", Approved: true, AllowAll: true},
		{Label: alwaysAllowLabel(req), Approved: true, Persist: true},
		{Label: "No"},
	}
}

// BuildApprovalOptions returns just the labels in display order. Used by the
// trace recorder; the modal renderer uses buildApprovalOptionRows directly.
func BuildApprovalOptions(req *perm.PermissionRequest) []string {
	rows := buildApprovalOptionRows(req)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Label
	}
	return out
}

func allSessionLabel(req *perm.PermissionRequest) string {
	if req == nil {
		return "Yes, allow all during this session"
	}
	switch req.ToolName {
	case "Edit":
		return "Yes, allow all edits during this session"
	case "Write":
		return "Yes, allow all writes during this session"
	case "Bash":
		return "Yes, allow all commands during this session"
	case tool.ToolSkill:
		return "Yes, allow all skills during this session"
	case tool.ToolAgent, tool.ToolSendMessage:
		return "Yes, allow all agents during this session"
	default:
		return "Yes, allow all during this session"
	}
}

func alwaysAllowLabel(req *perm.PermissionRequest) string {
	if req != nil && len(req.SuggestedRules) > 0 {
		return "Always allow: " + req.SuggestedRules[0]
	}
	return "Always allow"
}

func (p *ApprovalModel) renderMenu() string {
	var sb strings.Builder

	options := buildApprovalOptionRows(p.request)
	for i, opt := range options {
		if i == p.selectedIdx {
			bar := kit.FocusBarStyle().Render(kit.FocusBar)
			sb.WriteString(" " + bar + " " + approvalSelectedStyle().Render(fmt.Sprintf("%d. %s", i+1, opt.Label)))
		} else {
			sb.WriteString(approvalUnselectedStyle().Render(fmt.Sprintf("   %d. %s", i+1, opt.Label)))
		}
		if opt.Hint != "" {
			sb.WriteString(" ")
			sb.WriteString(approvalHintStyle().Render(opt.Hint))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// Render renders the permission prompt (calls renderInline)
func (p *ApprovalModel) Render() string {
	return p.renderInline()
}

// --- Agent preview ---

type approvalAgentPreview struct {
	agentMeta *perm.AgentMetadata
}

func newApprovalAgentPreview(meta *perm.AgentMetadata) *approvalAgentPreview {
	return &approvalAgentPreview{agentMeta: meta}
}

func (p *approvalAgentPreview) render(width int) string {
	if p.agentMeta == nil {
		return ""
	}

	var sb strings.Builder

	nameStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Primary).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
	labelStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	modeStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success)

	sb.WriteString("   ")
	sb.WriteString(nameStyle.Render(p.agentMeta.AgentName))
	if p.agentMeta.Background {
		sb.WriteString(" ")
		sb.WriteString(modeStyle.Render("[background]"))
	}
	sb.WriteString("\n")

	if p.agentMeta.Description != "" {
		sb.WriteString("   ")
		sb.WriteString(dimStyle.Render(p.agentMeta.Description))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")

	if p.agentMeta.Model != "" {
		sb.WriteString("   ")
		sb.WriteString(labelStyle.Render("Model: "))
		sb.WriteString(dimStyle.Render(p.agentMeta.Model))
		sb.WriteString("\n")
	}

	sb.WriteString("   ")
	sb.WriteString(labelStyle.Render("Mode: "))
	modeLabel := approvalFormatPermissionMode(p.agentMeta.PermissionMode)
	sb.WriteString(dimStyle.Render(modeLabel))
	sb.WriteString("\n")

	if len(p.agentMeta.Tools) > 0 {
		sb.WriteString("   ")
		sb.WriteString(labelStyle.Render("Tools: "))
		sb.WriteString(dimStyle.Render(strings.Join(p.agentMeta.Tools, ", ")))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")

	sb.WriteString("   ")
	sb.WriteString(labelStyle.Render("Task:"))
	sb.WriteString("\n")

	// A subagent prompt is model-authored and may be any language, so the cap
	// is applied in display columns and cut on a rune boundary.
	prompt := kit.TruncateText(p.agentMeta.Prompt, 500)

	lines := strings.Split(prompt, "\n")
	for i, line := range lines {
		if i >= 10 {
			sb.WriteString("   ")
			sb.WriteString(dimStyle.Render("..."))
			sb.WriteString("\n")
			break
		}
		sb.WriteString("   ")
		// max(...) guards a terminal too narrow for the budget, which the old
		// line[:width-9] would have sliced out of range.
		sb.WriteString(dimStyle.Render(kit.TruncateText(line, max(width-6, 1))))
		sb.WriteString("\n")
	}

	return sb.String()
}

func approvalFormatPermissionMode(mode string) string {
	switch mode {
	case "explore":
		return "Explore mode"
	case "edit":
		return "Edit mode"
	case "default":
		return "Default mode"
	default:
		return mode
	}
}

// --- Skill preview ---

type approvalSkillPreview struct {
	skillMeta *perm.SkillMetadata
}

func newApprovalSkillPreview(meta *perm.SkillMetadata) *approvalSkillPreview {
	return &approvalSkillPreview{skillMeta: meta}
}

func (p *approvalSkillPreview) render(width int) string {
	if p.skillMeta == nil {
		return ""
	}

	var sb strings.Builder

	nameStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Primary).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
	labelStyle := lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)

	sb.WriteString("   ")
	sb.WriteString(nameStyle.Render(p.skillMeta.SkillName))
	sb.WriteString("\n")

	if p.skillMeta.Description != "" {
		sb.WriteString("   ")
		sb.WriteString(dimStyle.Render(p.skillMeta.Description))
		sb.WriteString("\n")
	}

	if p.skillMeta.Args != "" {
		sb.WriteString("   ")
		sb.WriteString(labelStyle.Render("Args: "))
		sb.WriteString(dimStyle.Render(p.skillMeta.Args))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")

	var resources []string
	if p.skillMeta.ScriptCount > 0 {
		if p.skillMeta.ScriptCount == 1 {
			resources = append(resources, "1 script")
		} else {
			resources = append(resources, fmt.Sprintf("%d scripts", p.skillMeta.ScriptCount))
		}
	}
	if p.skillMeta.RefCount > 0 {
		if p.skillMeta.RefCount == 1 {
			resources = append(resources, "1 reference")
		} else {
			resources = append(resources, fmt.Sprintf("%d references", p.skillMeta.RefCount))
		}
	}

	if len(resources) > 0 {
		sb.WriteString("   ")
		sb.WriteString(labelStyle.Render("Resources: "))
		sb.WriteString(dimStyle.Render(strings.Join(resources, ", ")))
		sb.WriteString("\n")
	}

	if len(p.skillMeta.Scripts) > 0 && len(p.skillMeta.Scripts) <= 5 {
		sb.WriteString("   ")
		sb.WriteString(labelStyle.Render("Scripts: "))
		sb.WriteString(dimStyle.Render(strings.Join(p.skillMeta.Scripts, ", ")))
		sb.WriteString("\n")
	}

	if len(p.skillMeta.References) > 0 && len(p.skillMeta.References) <= 5 {
		sb.WriteString("   ")
		sb.WriteString(labelStyle.Render("Refs: "))
		sb.WriteString(dimStyle.Render(strings.Join(p.skillMeta.References, ", ")))
		sb.WriteString("\n")
	}

	return sb.String()
}
