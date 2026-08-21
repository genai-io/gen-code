package conv

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/tool"
)

// QuestionPrompt manages the question prompt UI for AskUserQuestion tool
type QuestionPrompt struct {
	active          bool
	request         *tool.QuestionRequest
	width           int
	currentQuestion int
	selectedOption  map[int]int
	selected        map[int][]int
	customAnswers   map[int]string
	customInput     textinput.Model
	showingCustom   bool
}

// NewQuestionPrompt creates a new QuestionPrompt
func NewQuestionPrompt() *QuestionPrompt {
	ti := textinput.New()
	ti.Placeholder = "Type your answer..."
	ti.CharLimit = 200
	ti.SetWidth(50)

	return &QuestionPrompt{
		selectedOption: make(map[int]int),
		selected:       make(map[int][]int),
		customAnswers:  make(map[int]string),
		customInput:    ti,
	}
}

// Show displays the question prompt with the given request
func (p *QuestionPrompt) Show(req *tool.QuestionRequest, width int) {
	p.active = true
	p.request = req
	p.width = width
	p.currentQuestion = 0
	p.selectedOption = make(map[int]int)
	p.selected = make(map[int][]int)
	p.customAnswers = make(map[int]string)
	p.showingCustom = false
	p.customInput.Reset()
}

// Hide hides the question prompt
func (p *QuestionPrompt) Hide() {
	p.active = false
	p.request = nil
	p.showingCustom = false
	p.selectedOption = make(map[int]int)
	p.selected = make(map[int][]int)
	p.customAnswers = make(map[int]string)
}

// IsActive returns whether the prompt is visible
func (p *QuestionPrompt) IsActive() bool {
	return p.active
}

func (p *QuestionPrompt) isAnswered(questionIdx int) bool {
	return len(p.selected[questionIdx]) > 0 || p.customAnswers[questionIdx] != ""
}

func (p *QuestionPrompt) restoreCustomInput() {
	p.customInput.Reset()
	if text := p.customAnswers[p.currentQuestion]; text != "" {
		p.customInput.SetValue(text)
	}
}

func customOptionIndex(q tool.Question) int {
	for i, opt := range q.Options {
		if strings.EqualFold(strings.TrimSpace(opt.Label), "other") {
			return i
		}
	}
	return len(q.Options)
}

// HandleKeypress consumes a keypress while the prompt is open. When the user
// finishes (answers or cancels) it emits the QuestionResponseMsg as a command,
// so the root model handles the result in Update like any other message. That
// keeps the modal a uniform overlayPanel rather than a special case in the key
// router.
func (p *QuestionPrompt) HandleKeypress(msg tea.KeyMsg) tea.Cmd {
	cmd, resp := p.handleKeypress(msg)
	if resp == nil {
		return cmd
	}
	out := *resp
	return tea.Batch(cmd, func() tea.Msg { return out })
}

// handleKeypress advances prompt state for a keypress, returning a response
// once the user has answered or cancelled.
func (p *QuestionPrompt) handleKeypress(msg tea.KeyMsg) (tea.Cmd, *QuestionResponseMsg) {
	if !p.active || p.request == nil {
		return nil, nil
	}

	if p.showingCustom {
		switch msg.String() {
		case "enter":
			return p.submitCustomInput()
		case "esc":
			p.showingCustom = false
			p.customInput.Blur()
			return nil, nil
		default:
			var cmd tea.Cmd
			p.customInput, cmd = p.customInput.Update(msg)
			return cmd, nil
		}
	}

	currentQ := p.request.Questions[p.currentQuestion]
	customIdx := customOptionIndex(currentQ)
	numOptions := len(currentQ.Options)
	if customIdx == len(currentQ.Options) {
		numOptions++
	}
	curOption := p.selectedOption[p.currentQuestion]

	switch msg.String() {
	case "left":
		if len(p.request.Questions) > 1 && p.currentQuestion > 0 {
			p.currentQuestion--
			p.restoreCustomInput()
		}
		return nil, nil

	case "right":
		if len(p.request.Questions) > 1 && p.currentQuestion < len(p.request.Questions)-1 {
			p.currentQuestion++
			p.restoreCustomInput()
		}
		return nil, nil

	case "tab":
		if len(p.request.Questions) > 1 {
			p.currentQuestion = (p.currentQuestion + 1) % len(p.request.Questions)
			p.restoreCustomInput()
		}
		return nil, nil

	case "up", "ctrl+p":
		if curOption > 0 {
			p.selectedOption[p.currentQuestion] = curOption - 1
		}
		return nil, nil

	case "down", "ctrl+n":
		if curOption < numOptions-1 {
			p.selectedOption[p.currentQuestion] = curOption + 1
		}
		return nil, nil

	case "space":
		if curOption == customIdx {
			p.showingCustom = true
			p.customInput.Focus()
			p.restoreCustomInput()
			return nil, nil
		}
		if currentQ.MultiSelect {
			p.toggleSelection()
		} else {
			cur := p.selected[p.currentQuestion]
			if len(cur) == 1 && cur[0] == curOption {
				p.selected[p.currentQuestion] = nil
			} else {
				p.selected[p.currentQuestion] = []int{curOption}
			}
		}
		return nil, nil

	case "enter":
		if curOption == customIdx {
			p.showingCustom = true
			p.customInput.Focus()
			p.restoreCustomInput()
			return nil, nil
		}

		if !currentQ.MultiSelect {
			p.selected[p.currentQuestion] = []int{curOption}
		} else if len(p.selected[p.currentQuestion]) == 0 {
			p.selected[p.currentQuestion] = []int{curOption}
		}

		return p.tryFinishOrAdvance()

	case "esc", "ctrl+c":
		req := p.request
		p.Hide()
		return nil, &QuestionResponseMsg{
			Request:   req,
			Cancelled: true,
		}
	}

	key := msg.String()
	if len(key) == 1 && key >= "1" && key <= "9" {
		optionIdx := int(key[0] - '1')
		if optionIdx < numOptions {
			p.selectedOption[p.currentQuestion] = optionIdx

			if optionIdx == customIdx {
				p.showingCustom = true
				p.customInput.Focus()
				p.restoreCustomInput()
				return nil, nil
			}

			if !currentQ.MultiSelect {
				p.selected[p.currentQuestion] = []int{optionIdx}
				return p.tryFinishOrAdvance()
			}
			p.toggleSelection()
		}
	}

	return nil, nil
}

func (p *QuestionPrompt) toggleSelection() {
	current := p.selected[p.currentQuestion]
	curOption := p.selectedOption[p.currentQuestion]
	found := false
	newSelection := []int{}

	for _, idx := range current {
		if idx == curOption {
			found = true
		} else {
			newSelection = append(newSelection, idx)
		}
	}

	if !found {
		newSelection = append(newSelection, curOption)
	}

	p.selected[p.currentQuestion] = newSelection
}

func (p *QuestionPrompt) submitCustomInput() (tea.Cmd, *QuestionResponseMsg) {
	customText := strings.TrimSpace(p.customInput.Value())
	if customText == "" {
		return nil, nil
	}

	p.customAnswers[p.currentQuestion] = customText
	p.showingCustom = false
	p.customInput.Blur()
	p.customInput.Reset()

	return p.tryFinishOrAdvance()
}

func (p *QuestionPrompt) tryFinishOrAdvance() (tea.Cmd, *QuestionResponseMsg) {
	n := len(p.request.Questions)

	nextUnanswered := -1
	for offset := 1; offset < n; offset++ {
		idx := (p.currentQuestion + offset) % n
		if !p.isAnswered(idx) {
			nextUnanswered = idx
			break
		}
	}

	if nextUnanswered >= 0 {
		p.currentQuestion = nextUnanswered
		return nil, nil
	}

	if !p.isAnswered(p.currentQuestion) {
		return nil, nil
	}

	req := p.request
	answers := make(map[int][]string)

	for qIdx := 0; qIdx < len(req.Questions); qIdx++ {
		if customText, ok := p.customAnswers[qIdx]; ok {
			answers[qIdx] = []string{customText}
			continue
		}
		q := req.Questions[qIdx]
		selectedIndices := p.selected[qIdx]
		labels := []string{}
		for _, optIdx := range selectedIndices {
			if optIdx < len(q.Options) {
				labels = append(labels, q.Options[optIdx].Label)
			}
		}
		answers[qIdx] = labels
	}

	p.Hide()

	return nil, &QuestionResponseMsg{
		Request: req,
		Response: &tool.QuestionResponse{
			RequestID: req.ID,
			Answers:   answers,
			Cancelled: false,
		},
	}
}

func getQuestionSeparatorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Separator)
}

func getQuestionHeaderStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(kit.CurrentTheme.Primary).
		Bold(true)
}

func getQuestionTextStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Text)
}

func getQuestionSelectedStyle() lipgloss.Style {
	// Neutral bright+bold; the teal FocusBar is the selection signal. Green
	// stays reserved for success/confirm semantics.
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextBright).Bold(true)
}

func getQuestionUnselectedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
}

func getQuestionDescStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted).Italic(true)
}

func getQuestionFooterStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
}

func getQuestionTabActiveStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(kit.CurrentTheme.Focus).
		Bold(true).
		Underline(true)
}

func getQuestionTabInactiveStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.TextDim)
}

func getQuestionTabAnsweredStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(kit.CurrentTheme.Success)
}

// Render renders the question prompt
func (p *QuestionPrompt) Render() string {
	if !p.active || p.request == nil {
		return ""
	}

	var sb strings.Builder
	contentWidth := max(p.width-2, 40)

	currentQ := p.request.Questions[p.currentQuestion]

	if len(p.request.Questions) > 1 {
		sb.WriteString(" ")
		for i, q := range p.request.Questions {
			label := q.Header
			if label == "" {
				label = fmt.Sprintf("Q%d", i+1)
			}

			var tabStyle lipgloss.Style
			if i == p.currentQuestion {
				tabStyle = getQuestionTabActiveStyle()
			} else if p.isAnswered(i) {
				label = "\u2713 " + label
				tabStyle = getQuestionTabAnsweredStyle()
			} else {
				tabStyle = getQuestionTabInactiveStyle()
			}

			sb.WriteString(tabStyle.Render(label))
			if i < len(p.request.Questions)-1 {
				sb.WriteString(getQuestionSeparatorStyle().Render(" \u2502 "))
			}
		}
		sb.WriteString("\n")

		thinSep := strings.Repeat("\u2504", contentWidth)
		sb.WriteString(getQuestionSeparatorStyle().Render(thinSep))
		sb.WriteString("\n")
	}

	header := getQuestionHeaderStyle().Render(currentQ.Header)
	sb.WriteString(" ")
	sb.WriteString(header)
	sb.WriteString("\n")

	sb.WriteString(" ")
	sb.WriteString(getQuestionTextStyle().Render(currentQ.Question))
	sb.WriteString("\n\n")

	isMulti := currentQ.MultiSelect
	curOption := p.selectedOption[p.currentQuestion]
	customIdx := customOptionIndex(currentQ)
	selectedSet := make(map[int]bool)
	for _, idx := range p.selected[p.currentQuestion] {
		selectedSet[idx] = true
	}

	for i, opt := range currentQ.Options {
		isHighlighted := i == curOption
		isSelected := selectedSet[i]

		var prefix string
		switch {
		case isMulti && isSelected:
			prefix = "[\u2713]"
		case isMulti:
			prefix = "[ ]"
		case isSelected || isHighlighted:
			prefix = "(\u25CF)"
		default:
			prefix = "( )"
		}

		optBody := fmt.Sprintf("%s %d. %s", prefix, i+1, opt.Label)
		if isHighlighted {
			sb.WriteString(" " + kit.FocusBarStyle().Render(kit.FocusBar) + " " + getQuestionSelectedStyle().Render(optBody))
		} else {
			sb.WriteString(getQuestionUnselectedStyle().Render("   " + optBody))
		}

		if i == customIdx {
			desc := opt.Description
			if desc == "" {
				desc = "Type custom response"
			}
			sb.WriteString(" - ")
			sb.WriteString(getQuestionDescStyle().Render(desc))
		} else if opt.Description != "" {
			sb.WriteString(" - ")
			sb.WriteString(getQuestionDescStyle().Render(opt.Description))
		}
		sb.WriteString("\n")
	}

	if customIdx == len(currentQ.Options) {
		isOtherHighlighted := curOption == customIdx

		otherPrefix := "( )"
		if isMulti {
			otherPrefix = "[ ]"
		}

		otherBody := fmt.Sprintf("%s %d. Other", otherPrefix, customIdx+1)
		if isOtherHighlighted {
			sb.WriteString(" " + kit.FocusBarStyle().Render(kit.FocusBar) + " " + getQuestionSelectedStyle().Render(otherBody))
		} else {
			sb.WriteString(getQuestionUnselectedStyle().Render("   " + otherBody))
		}
		sb.WriteString(" - ")
		sb.WriteString(getQuestionDescStyle().Render("Type custom response"))
		sb.WriteString("\n")
	}

	if p.showingCustom {
		sb.WriteString("\n")
		sb.WriteString("   ")
		sb.WriteString(p.customInput.View())
		sb.WriteString("\n")
	}

	if !p.showingCustom {
		if customText, ok := p.customAnswers[p.currentQuestion]; ok {
			sb.WriteString("   ")
			sb.WriteString(getQuestionDescStyle().Render("answered: " + customText))
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n")

	var hints []string
	if len(p.request.Questions) > 1 {
		hints = append(hints, "\u2190/\u2192 switch question")
	}
	hints = append(hints, "\u2191/\u2193 navigate", "Space toggle", "Enter confirm", "Esc cancel")

	footer := " " + strings.Join(hints, " \u00B7 ")
	sb.WriteString(getQuestionFooterStyle().Render(footer))
	sb.WriteString("\n")

	solidSep := strings.Repeat("\u2500", contentWidth)
	sb.WriteString(getQuestionSeparatorStyle().Render(solidSep))

	return sb.String()
}
