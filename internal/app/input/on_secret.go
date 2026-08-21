package input

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
)

// SecretPromptModel is a masked one-line prompt for secrets requested by an
// already-approved interactive command.
type SecretPromptModel struct {
	active bool
	prompt string
	width  int
	input  textinput.Model
}

type SecretPromptResponseMsg struct {
	Value     string
	Cancelled bool
}

func NewSecretPrompt() SecretPromptModel {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.Focus()
	styles := ti.Styles()
	styles.Focused.Text = approvalSelectedStyle()
	styles.Focused.Prompt = approvalSelectedStyle()
	styles.Blurred.Text = approvalUnselectedStyle()
	ti.SetStyles(styles)
	return SecretPromptModel{input: ti}
}

func (p *SecretPromptModel) Show(prompt string, width int) {
	p.active = true
	p.prompt = strings.TrimSpace(prompt)
	p.width = width
	p.input.SetValue("")
	p.input.Focus()
	// Width is sized by Render from p.width before the input is ever displayed.
}

func (p *SecretPromptModel) Hide() {
	p.active = false
	p.prompt = ""
	p.input.SetValue("")
}

func (p *SecretPromptModel) IsActive() bool { return p.active }

func (p *SecretPromptModel) HandlePaste(content string) tea.Cmd {
	if !p.active {
		return nil
	}
	content = strings.NewReplacer("\r", "", "\n", "").Replace(content)
	p.input.SetValue(p.input.Value() + content)
	p.input.CursorEnd()
	return nil
}

func (p *SecretPromptModel) HandleKeypress(msg tea.KeyMsg) tea.Cmd {
	if !p.active {
		return nil
	}
	switch msg.String() {
	case "enter":
		value := p.input.Value()
		p.Hide()
		return func() tea.Msg { return SecretPromptResponseMsg{Value: value} }
	case "esc", "ctrl+c":
		p.Hide()
		return func() tea.Msg { return SecretPromptResponseMsg{Cancelled: true} }
	default:
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return cmd
	}
}

func (p *SecretPromptModel) Render() string {
	if !p.active {
		return ""
	}

	contentWidth := max(p.width-2, 40)
	p.input.SetWidth(inputWidth(contentWidth))

	var sb strings.Builder
	sb.WriteString(" ")
	sb.WriteString(approvalTitleStyle().Render("Command requests secret"))
	sb.WriteString("\n\n")
	if p.prompt != "" {
		sb.WriteString("   ")
		sb.WriteString(approvalQuestionStyle().Render(kit.TruncateText(p.prompt, contentWidth-6)))
		sb.WriteString("\n")
	}
	sb.WriteString("   ")
	sb.WriteString(p.input.View())
	sb.WriteString("\n\n")
	sb.WriteString(approvalFooterStyle().Render(" Enter to submit · Esc to skip"))
	sb.WriteString("\n")
	sb.WriteString(approvalSeparatorStyle().Render(strings.Repeat("─", contentWidth)))
	return sb.String()
}

func inputWidth(width int) int {
	if width < 20 {
		return 20
	}
	return width - 6
}
