package input

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/tool/perm"
)

const (
	approvalDefaultMaxCommandLines = 20
)

type approvalBashPreview struct {
	bashMeta   *perm.BashMetadata
	expanded   bool
	maxVisible int
}

func newApprovalBashPreview(meta *perm.BashMetadata) *approvalBashPreview {
	return &approvalBashPreview{
		bashMeta:   meta,
		expanded:   false,
		maxVisible: approvalDefaultMaxCommandLines,
	}
}

func (b *approvalBashPreview) toggleExpand() {
	b.expanded = !b.expanded
}

func approvalBashCommandStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text)
}

func approvalBashBgStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Warning)
}

func approvalBashDescStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim).Italic(true)
}

func (b *approvalBashPreview) render(width int) string {
	if b.bashMeta == nil || b.bashMeta.Command == "" {
		return approvalBashCommandStyle().Render("   (no command)")
	}

	var sb strings.Builder

	if b.bashMeta.RunBackground {
		sb.WriteString("   ")
		sb.WriteString(approvalBashBgStyle().Render("[background]"))
		sb.WriteString("\n")
	}

	lines := strings.Split(b.bashMeta.Command, "\n")
	showCount := len(lines)
	truncated := false

	if !b.expanded && showCount > b.maxVisible {
		showCount = b.maxVisible
		truncated = true
	}

	const indent = 3
	contentWidth := max(width-indent, 8)

	for i := 0; i < showCount; i++ {
		sb.WriteString("   ")
		sb.WriteString(approvalBashCommandStyle().Render(approvalTruncateContent(lines[i], contentWidth)))
		sb.WriteString("\n")
	}

	if b.bashMeta.Description != "" {
		sb.WriteString("   ")
		sb.WriteString(approvalBashDescStyle().Render(approvalTruncateContent(b.bashMeta.Description, contentWidth)))
		sb.WriteString("\n")
	}

	if truncated {
		remaining := len(lines) - b.maxVisible
		msg := fmt.Sprintf("   ... %d more lines (Ctrl+O to expand)", remaining)
		sb.WriteString(approvalDiffMoreStyle().Render(msg))
		sb.WriteString("\n")
	}

	return sb.String()
}

func (b *approvalBashPreview) needsExpand() bool {
	if b.bashMeta == nil {
		return false
	}
	return b.bashMeta.LineCount > b.maxVisible
}
