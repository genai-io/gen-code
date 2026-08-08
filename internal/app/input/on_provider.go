// Provider selector: data model, overlay state, messages, and top-level
// message routing for the unified model & provider selection overlay.
//
// Navigation and selection live in on_provider_nav.go; credential entry and
// the connect/refresh flow in on_provider_auth.go; data loading in
// on_provider_data.go; rendering in on_provider_view.go.
package input

import (
	"context"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
)

// providerTab represents which tab is active in the provider selector.
type providerTab int

const (
	providerTabModels    providerTab = iota // model selection tab
	providerTabProviders                    // provider management tab
)

// providerItemKind represents a row type in the visible-items list.
type providerItemKind int

const (
	providerItemProviderHeader providerItemKind = iota // non-selectable provider group header (Models tab)
	providerItemModel                                  // selectable model row (Models tab)
	providerItemProvider                               // provider row (Providers tab)
	providerItemAuthMethod                             // expanded auth-method sub-row (Providers tab)
)

// providerListItem is a single row in the flattened visible-items list.
type providerListItem struct {
	Kind        providerItemKind
	Model       *providerModelItem
	Provider    *providerProviderItem
	AuthMethod  *providerAuthMethodItem
	ProviderIdx int // index into allProviders
}

// providerProviderItem represents a provider with its auth methods.
type providerProviderItem struct {
	Provider    llm.Name
	DisplayName string
	AuthMethods []providerAuthMethodItem
	Connected   bool // whether this provider has at least one connected auth method
}

// providerAuthMethodItem represents an auth method in the second level.
type providerAuthMethodItem struct {
	Provider    llm.Name
	AuthMethod  llm.AuthMethod
	DisplayName string
	Status      llm.Status
	EnvVars     []string
}

// providerModelItem represents a model in the provider selector.
type providerModelItem struct {
	ID               string
	Name             string
	DisplayName      string
	Description      string
	ProviderName     string
	AuthMethod       llm.AuthMethod
	IsCurrent        bool
	InputTokenLimit  int
	OutputTokenLimit int
}

func newProviderModelItem(mdl llm.ModelInfo, providerName string, authMethod llm.AuthMethod, current *llm.CurrentModelInfo) providerModelItem {
	return providerModelItem{
		ID:               mdl.ID,
		Name:             mdl.Name,
		DisplayName:      mdl.DisplayName,
		Description:      mdl.Description,
		ProviderName:     providerName,
		AuthMethod:       authMethod,
		IsCurrent:        current != nil && current.ModelID == mdl.ID && string(current.Provider) == providerName && current.AuthMethod == authMethod,
		InputTokenLimit:  mdl.InputTokenLimit,
		OutputTokenLimit: mdl.OutputTokenLimit,
	}
}

// ProviderSelector holds the state for the unified model & provider kit.
type ProviderSelector struct {
	active bool
	width  int
	height int
	store  *llm.Store

	// Tab
	activeTab providerTab

	// Data
	connectedProviders []providerProviderItem // providers with models (Models tab headers)
	allProviders       []providerProviderItem // all providers (Providers tab)
	allModels          []providerModelItem

	// Flattened visible-items list (rebuilt on state changes)
	visibleItems []providerListItem
	selectedIdx  int
	scrollOffset int
	maxVisible   int

	// Providers tab: expanded provider
	expandedProviderIdx int // index into allProviders; -1 = none

	// Inline API-key input
	apiKeyInput       textinput.Model
	apiKeyActive      bool
	apiKeyEnvVar      string
	apiKeyProviderIdx int // index into allProviders
	apiKeyAuthIdx     int // index into that provider's AuthMethods

	// Inline custom provider form (baseURL / apiKey); see on_provider_custom.go
	customFormActive bool
	customFormInputs [2]textinput.Model
	customFormFocus  int
	customFormErr    string

	// Inline Ollama form (base URL only); see on_provider_ollama.go
	ollamaFormActive bool
	ollamaURLInput   textinput.Model
	ollamaFormErr    string

	// Inline confirm-remove prompt
	confirmRemoveActive  bool
	confirmRemoveEnvVar  string
	confirmRemoveItemIdx int // index into visibleItems for the pending remove

	// Models tab: search filter and the two flags that disambiguate keys
	// whose meaning depends on what the user is doing.
	searchQuery    string              // active filter text; "" means no filter
	filteredModels []providerModelItem // allModels narrowed to searchQuery

	// searchFocused routes Space: true while the search box has focus (the user
	// is typing a query) so Space inserts a literal space; false while
	// navigating the list so Space marks the highlighted model instead.
	searchFocused bool

	// modelMarked routes Enter: true once the user has explicitly marked a
	// model with Space, so Enter confirms that mark regardless of cursor; false
	// until then, so Enter acts on the highlighted row. (The active model is
	// rendered [*] on open, but that display state is not a mark.)
	modelMarked bool

	// Provider connection result (shown inline)
	lastConnectResult  string
	lastConnectAuthIdx int // item index that triggered the connection
	lastConnectSuccess bool

	// loginPrompt holds the instruction for an interactive sign-in still in
	// flight — the page to open and, for device flows, the code to type there.
	// It replaces the hint line until the sign-in resolves; a zero value means
	// nothing is pending.
	loginPrompt llm.LoginPrompt

	// cancelLogin stops an interactive sign-in when the user closes the
	// selector. A device flow otherwise keeps polling GitHub every few seconds
	// for its full 15-minute lifetime with nobody left to read the result.
	cancelLogin context.CancelFunc

	// spinnerTick advances on each providerConnectingTickMsg; used to pick a braille
	// frame while a connect/refresh is in flight.
	spinnerTick int
}

// NewProviderSelector creates a new provider selector ProviderSelector.
func NewProviderSelector() ProviderSelector {
	return ProviderSelector{
		active:              false,
		selectedIdx:         0,
		maxVisible:          20,
		expandedProviderIdx: -1,
	}
}

// IsActive returns whether the selector is active.
func (s *ProviderSelector) IsActive() bool {
	return s.active
}

// providerModelSelectedMsg is sent when a model is selected.
type providerModelSelectedMsg struct {
	ModelID      string
	ProviderName string
	AuthMethod   llm.AuthMethod
}

// providerConnectResultMsg is sent when inline connection completes.
type providerConnectResultMsg struct {
	AuthIdx    int
	Success    bool
	Message    string
	NewStatus  llm.Status
	Provider   llm.Name
	AuthMethod llm.AuthMethod
	Models     []llm.ModelInfo
}

// providerLoginPromptMsg carries the instruction an in-flight interactive
// sign-in needs the user to follow.
type providerLoginPromptMsg struct {
	AuthIdx int
	Prompt  llm.LoginPrompt
}

// providerModelsLoadedMsg is sent when async model loading completes.
type providerModelsLoadedMsg struct {
	Models []providerModelItem
}

// providerStatusDisplayInfo contains display information for a provider status.
type providerStatusDisplayInfo struct {
	icon  string
	style lipgloss.Style
	desc  string
}

// providerStatusDisplayMap maps provider status to display information.
var providerStatusDisplayMap = map[llm.Status]providerStatusDisplayInfo{
	llm.StatusConnected: {"●", kit.SelectorStatusConnected(), ""},
	llm.StatusAvailable: {"○", kit.SelectorStatusReady(), "(available)"},
}

// providerGetStatusDisplay returns the icon, style, and description for a provider status.
func providerGetStatusDisplay(status llm.Status) (icon string, style lipgloss.Style, desc string) {
	if info, ok := providerStatusDisplayMap[status]; ok {
		return info.icon, info.style, info.desc
	}
	return "◌", kit.SelectorStatusNone(), ""
}

func providerBestAuthMethodStatus(methods []providerAuthMethodItem) llm.Status {
	for _, m := range methods {
		if m.Status == llm.StatusConnected {
			return llm.StatusConnected
		}
	}
	for _, m := range methods {
		if m.Status == llm.StatusAvailable {
			return llm.StatusAvailable
		}
	}
	return llm.StatusNotConfigured
}

// ProviderState holds provider UI state for the TUI model.
// Domain state (LLM, Store, CurrentModel, tokens, thinking) lives
// on the parent app model, not here.
type ProviderState struct {
	FetchingLimits bool
	Selector       ProviderSelector
	StatusMessage  string // Temporary status shown in status bar
	statusToken    int64
}

// SetStatusMessage sets the temporary status message displayed in the status bar.
func (s *ProviderState) SetStatusMessage(msg string) int64 {
	s.statusToken++
	s.StatusMessage = msg
	return s.statusToken
}

// UpdateProvider routes provider connection and selection messages.
func UpdateProvider(deps OverlayDeps, state *ProviderState, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case providerConnectingTickMsg:
		// Keep ticking the spinner while connect/refresh is in flight; stop once
		// the matching providerConnectResultMsg lands and IsConnecting goes false.
		if state.Selector.IsConnecting() {
			state.Selector.AdvanceSpinner()
			return providerConnectingTickCmd(), true
		}
		return nil, true
	case providerLoginPromptMsg:
		state.Selector.HandleLoginPrompt(msg)
		return nil, true
	case providerConnectResultMsg:
		cmd := state.Selector.HandleConnectResult(msg)
		if msg.Success && deps.ReloadModelStore != nil {
			deps.ReloadModelStore()
		}
		return cmd, true
	case providerModelSelectedMsg:
		return handleProviderModelSelected(deps, state, msg), true
	case providerModelsLoadedMsg:
		state.Selector.HandleModelsLoaded(msg)
		if deps.ReloadModelStore != nil {
			deps.ReloadModelStore()
		}
		return nil, true
	case kit.StatusExpiredMsg:
		if msg.Token == state.statusToken {
			state.StatusMessage = ""
		}
		return nil, true
	}
	return nil, false
}

func handleProviderModelSelected(deps OverlayDeps, state *ProviderState, msg providerModelSelectedMsg) tea.Cmd {
	_, err := state.Selector.SetModel(msg.ModelID, msg.ProviderName, msg.AuthMethod)
	if err != nil {
		deps.Conv.Append(core.ChatMessage{Role: core.RoleNotice, Content: "Error: " + err.Error()})
		return tea.Batch(deps.CommitMessages()...)
	}

	deps.SetCurrentModel(&llm.CurrentModelInfo{
		ModelID:    msg.ModelID,
		Provider:   llm.Name(msg.ProviderName),
		AuthMethod: msg.AuthMethod,
	})
	ctx := context.Background()
	providerRefreshConnection(deps, state, ctx, llm.Name(msg.ProviderName), msg.AuthMethod)
	return tea.Batch(deps.CommitMessages()...)
}

func providerRefreshConnection(deps OverlayDeps, state *ProviderState, ctx context.Context, providerName llm.Name, authMethod llm.AuthMethod) {
	p, err := llm.GetProvider(ctx, providerName, authMethod)
	if err != nil {
		log.Logger().Warn("failed to refresh provider connection",
			zap.String("provider", string(providerName)),
			zap.Error(err))
		return
	}
	deps.SwitchProvider(p)
}
