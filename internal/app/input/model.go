package input

import (
	"math"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/app/kit/history"
	"github.com/genai-io/san/internal/app/kit/suggest"
	"github.com/genai-io/san/internal/core"
	coremcp "github.com/genai-io/san/internal/mcp"
	corepersona "github.com/genai-io/san/internal/persona"
	coreplugin "github.com/genai-io/san/internal/plugin"
	coresetting "github.com/genai-io/san/internal/setting"
	coreskill "github.com/genai-io/san/internal/skill"
)

type PastedChunk struct {
	Text      string // the full pasted text
	LineCount int    // total line count
}

type HistoryNav struct {
	Items   []string
	Index   int    // -1 = not navigating
	Stashed string // stashed textarea input while navigating
}

type Model struct {
	Textarea         textarea.Model
	History          HistoryNav
	PromptSuggestion PromptSuggestionState
	Suggestions      suggest.State
	LastCtrlO        time.Time
	LastCtrlC        time.Time
	Images           ImageState
	terminalHeight   int // set via SetTerminalHeight, which also re-caps the box
	PastedChunks     []PastedChunk
	Queue            Queue

	// Overlay modals. Each field is a full-screen overlay the input area can
	// hand control to. Two shapes, by convention:
	//
	//	XxxSelector — a self-contained list picker (cursor + render only).
	//	XxxState    — a selector wrapped with ambient state the app/runtime
	//	              needs around it: a pending invocation, an in-flight
	//	              external-editor handoff, or a status line. The picker
	//	              itself is always the embedded .Selector field.
	//
	// Approval is the odd one out: a permission confirm dialog, not a picker.
	Approval ApprovalModel
	Secret   SecretPromptModel

	// Self-contained selectors.
	Agent     AgentSelector
	Persona   PersonaSelector
	Search    SearchSelector
	Plugin    PluginSelector
	Tool      ToolSelector
	Config    PanelPopup        // /config: appearance (+ future provider/permissions)
	Evolve    PanelPopup        // /evolve: self-learning skills + memory
	Autopilot AutopilotSelector // /autopilot: the session copilot

	// Selectors carrying ambient state (the picker is the .Selector field).
	Skill    SkillState    // + pending skill invocation
	Session  SessionState  // + pending-open flag
	Memory   MemoryState   // + in-flight editor file
	MCP      MCPState      // + in-flight editor file/server/scope
	Provider ProviderState // + fetching / status-line state
}

type PendingImage struct {
	ID   int
	Data core.Attachment
}

type ImageSelection struct {
	Active       bool
	PendingIdx   int
	CursorAbsPos int
}

type ImageState struct {
	Pending   []PendingImage
	NextID    int
	Selection ImageSelection
}

func (img *ImageState) RemoveAt(idx int) {
	if idx < 0 || idx >= len(img.Pending) {
		return
	}
	img.Pending = append(img.Pending[:idx], img.Pending[idx+1:]...)
	if len(img.Pending) == 0 {
		img.Selection = ImageSelection{}
		return
	}
	if img.Selection.PendingIdx == idx {
		img.Selection = ImageSelection{}
		return
	}
	if img.Selection.PendingIdx > idx {
		img.Selection.PendingIdx--
	}
}

type SelectorDeps struct {
	AgentRegistry   AgentRegistry
	PersonaRegistry *corepersona.Registry
	SkillRegistry   *coreskill.Registry
	MCPRegistry     *coremcp.Registry
	PluginRegistry  *coreplugin.Registry
	Setting         *coresetting.Settings
	LoadDisabled    func(userLevel bool) map[string]bool
	UpdateDisabled  func(disabled map[string]bool, userLevel bool) error
	// Evolve bundles the /evolve popup's dependencies: the live workspace
	// source, the learned skill/memory stores, and the recent-activity
	// accessor. See EvolveDeps.
	Evolve EvolveDeps
}

func New(cwd string, width int, matchFunc suggest.Matcher, deps SelectorDeps) Model {
	suggestions := suggest.NewState(matchFunc)
	suggestions.SetCwd(cwd)
	return Model{
		Textarea:    newTextarea(width),
		History:     HistoryNav{Items: history.Load(cwd), Index: -1},
		Suggestions: suggestions,
		Queue:       NewQueue(),

		Approval:  NewApproval(),
		Secret:    NewSecretPrompt(),
		Agent:     NewAgentSelector(deps.AgentRegistry),
		Persona:   NewPersonaSelector(deps.PersonaRegistry, deps.Setting),
		Search:    NewSearchSelector(deps.Setting),
		Skill:     SkillState{Selector: NewSkillSelector(deps.SkillRegistry)},
		Session:   SessionState{Selector: NewSessionSelector()},
		Memory:    MemoryState{Selector: NewMemorySelector()},
		MCP:       MCPState{Selector: NewMCPSelector(deps.MCPRegistry)},
		Plugin:    NewPluginSelector(deps.PluginRegistry),
		Provider:  ProviderState{Selector: NewProviderSelector()},
		Tool:      NewToolSelector(deps.LoadDisabled, deps.UpdateDisabled),
		Config:    NewConfigSelector(deps.Setting),
		Autopilot: NewAutopilotSelector(),
		Evolve:    NewEvolveSelector(deps.Evolve),
	}
}

// newChromelessTextarea builds a bare textarea with the composer's borderless
// styling (no prompt, no line numbers, muted placeholder) but no focus or size —
// callers add those. Shared by the main composer and the /autopilot editors.
func newChromelessTextarea() textarea.Model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	styles := ta.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.Base = lipgloss.NewStyle()
	styles.Focused.Prompt = lipgloss.NewStyle()
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	ta.SetStyles(styles)
	ta.KeyMap.InsertNewline.SetEnabled(true)
	return ta
}

// newTextarea is the main composer's textarea: chromeless, focused, sized, with
// a muted blurred state.
func newTextarea(width int) textarea.Model {
	ta := newChromelessTextarea()
	ta.Focus()
	ta.SetWidth(width)

	// Let the textarea size itself: it counts soft-wrapped rows through the
	// same memoized wrap its renderer uses, so the box can't disagree with what
	// gets drawn — including CJK, where one rune occupies two columns.
	ta.DynamicHeight = true
	ta.MinHeight = minTextareaHeight
	ta.MaxHeight = defaultMaxHeight
	// MaxHeight caps the viewport, and with MaxContentHeight left at zero it
	// would also stop accepting input at that many lines. Setting it separately
	// keeps the buffer scrollable past what's on screen; MaxInt means "no limit
	// of ours" — the textarea already stops at its own 10000-line guard, and any
	// tighter number here silently trims a large paste before the placeholder
	// logic ever sees it.
	ta.MaxContentHeight = math.MaxInt

	// Drive the terminal's own cursor instead of painting a reverse-video block
	// into the frame: the block leaves the real cursor parked wherever the last
	// paint ended (out in the streamed output), which is what the user's eye and
	// the terminal's own IME follow. View() positions it — see model.inputCursor.
	// Composer only; TestOverlayEditorsKeepVirtualCursor covers why the overlay
	// editors built from newChromelessTextarea must not follow.
	ta.SetVirtualCursor(false)

	// Enter submits (see handleTextareaShortcut), so a newline needs its own
	// keys. shift+enter is the one users reach for, but a terminal only reports
	// it separately once key disambiguation is on (Kitty protocol: Ghostty,
	// Kitty, WezTerm, iTerm2); elsewhere it arrives as a bare "enter" and
	// submits. alt+enter and ctrl+j come through everywhere, so they stay as the
	// portable fallbacks.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter", "ctrl+j"),
		key.WithHelp("shift+enter", "insert newline"),
	)

	styles := ta.Styles()
	styles.Blurred.Base = lipgloss.NewStyle().Foreground(kit.CurrentTheme.Muted)
	ta.SetStyles(styles)
	return ta
}
