// Provider selector: cursor/tab navigation, model search, key routing, and
// model/provider/auth-method selection.
package input

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/llm"
)

func (s *ProviderSelector) ensureVisible() {
	if s.selectedIdx < s.scrollOffset {
		s.scrollOffset = s.selectedIdx
	}
	if s.selectedIdx >= s.scrollOffset+s.maxVisible {
		s.scrollOffset = s.selectedIdx - s.maxVisible + 1
	}
}

func (s *ProviderSelector) MoveUp() {
	for s.selectedIdx > 0 {
		s.selectedIdx--
		if s.visibleItems[s.selectedIdx].Kind != providerItemProviderHeader {
			break
		}
	}
	if s.selectedIdx == 0 {
		s.searchFocused = true
	}
	s.ensureVisible()
}

func (s *ProviderSelector) MoveDown() {
	for s.selectedIdx < len(s.visibleItems)-1 {
		s.selectedIdx++
		if s.visibleItems[s.selectedIdx].Kind != providerItemProviderHeader {
			break
		}
	}
	s.searchFocused = false
	s.ensureVisible()
}

func (s *ProviderSelector) switchTab(t providerTab) {
	if t == s.activeTab {
		return
	}
	s.activeTab = t
	s.resetNavigation()
	s.resetModelSearch()
	s.resetConnectionResult()
	s.expandedProviderIdx = -1
	s.apiKeyActive = false
	s.closeCustomForm()
	s.closeOllamaForm()
	s.rebuildVisibleItems()
}

func (s *ProviderSelector) NextTab() { s.switchTab((s.activeTab + 1) % 2) }
func (s *ProviderSelector) PrevTab() { s.switchTab((s.activeTab + 1 + 2) % 2) }

func (s *ProviderSelector) GoBack() bool {
	if s.customFormActive {
		s.closeCustomForm()
		return true
	}
	if s.ollamaFormActive {
		s.closeOllamaForm()
		return true
	}
	if s.apiKeyActive {
		s.apiKeyActive = false
		return true
	}
	if s.expandedProviderIdx >= 0 {
		s.expandedProviderIdx = -1
		s.resetConnectionResult()
		s.rebuildVisibleItems()
		return true
	}
	return false
}

func (s *ProviderSelector) clearModelSearch() bool {
	if s.searchQuery == "" {
		return false
	}
	s.searchQuery = ""
	s.searchFocused = false
	s.rebuildVisibleItems()
	return true
}

func (s *ProviderSelector) trimModelSearch() {
	if len(s.searchQuery) == 0 {
		return
	}
	s.searchQuery = s.searchQuery[:len(s.searchQuery)-1]
	if s.searchQuery == "" {
		// Empty query means we're no longer typing in the search box, so Space
		// returns to marking models rather than inserting a literal space.
		s.searchFocused = false
	}
	s.rebuildVisibleItems()
}

func (s *ProviderSelector) appendModelSearch(text string) {
	s.searchQuery += text
	s.searchFocused = true
	s.rebuildVisibleItems()
}

func (s *ProviderSelector) HandleKeypress(key tea.KeyMsg) tea.Cmd {
	// Route to the custom provider form if active
	if s.customFormActive {
		return s.handleCustomFormKey(key)
	}

	// Route to the Ollama form if active
	if s.ollamaFormActive {
		return s.handleOllamaFormKey(key)
	}

	// Route to API key input if active
	if s.apiKeyActive {
		return s.handleAPIKeyInput(key)
	}

	// Route to confirm-remove if active
	if s.confirmRemoveActive {
		return s.handleConfirmRemove(key)
	}

	// Navigation is arrow-driven and works regardless of the search query:
	// ↑/↓ (and ctrl+p/ctrl+n) move the list, ←/→ (and tab/shift+tab) switch
	// tabs. Switching tabs clears the query (switchTab).
	switch key.String() {
	case "tab", "right":
		s.NextTab()
		return nil

	case "shift+tab":
		s.PrevTab()
		return nil

	case "up", "ctrl+p":
		s.MoveUp()
		return nil

	case "down", "ctrl+n":
		s.MoveDown()
		return nil

	case "enter":
		return s.Select()

	case "left":
		if !s.GoBack() {
			s.PrevTab()
		}
		return nil

	case "esc":
		if s.clearModelSearch() {
			return nil
		}
		if s.GoBack() {
			return nil
		}
		s.Cancel()
		return func() tea.Msg { return kit.DismissedMsg{} }

	case "backspace":
		s.trimModelSearch()
		return nil

	case "space":
		if s.activeTab == providerTabModels && !s.searchFocused {
			return s.toggleModel()
		}
		s.appendModelSearch(" ")
		return nil

	case "ctrl+e":
		return s.handleCredentialEdit()

	case "ctrl+d":
		return s.handleCredentialRemove()

	default:
		// Typed text capture: every printable rune is search input. Navigation
		// is arrows + ctrl+p/ctrl+n; tabs are tab/shift+tab and left/right — no
		// bare-letter vim keys — so a query can start with any character
		// (e.g. "kimi", "haiku", "llama").
		if text := key.Key().Text; text != "" {
			s.appendModelSearch(text)
			return nil
		}
	}

	return nil
}

func (s *ProviderSelector) Select() tea.Cmd {
	// On the Models tab: once the user has explicitly marked a model with
	// Space, Enter confirms that marked model regardless of cursor position.
	// Without an explicit mark, fall through to the highlighted row so that
	// plain navigation + Enter and search + Enter still select what the cursor
	// is on (the active model is shown [*] on open, but that is not a mark).
	if s.activeTab == providerTabModels && s.modelMarked {
		if cmd := s.selectMarkedModel(); cmd != nil {
			return cmd
		}
	}

	if s.selectedIdx < 0 || s.selectedIdx >= len(s.visibleItems) {
		return nil
	}

	item := s.visibleItems[s.selectedIdx]
	switch item.Kind {
	case providerItemModel:
		return s.selectModel(item.Model)
	case providerItemProvider:
		return s.selectProvider(item)
	case providerItemAuthMethod:
		return s.selectAuthMethod(item)
	default:
		return nil
	}
}

func (s *ProviderSelector) selectModel(m *providerModelItem) tea.Cmd {
	if m == nil {
		return nil
	}
	s.active = false
	return func() tea.Msg {
		return providerModelSelectedMsg{
			ModelID:      m.ID,
			ProviderName: m.ProviderName,
			AuthMethod:   m.AuthMethod,
		}
	}
}

// selectModelFromIDs is like selectModel but takes the model identity as strings
// and constructs the message directly, without requiring a model pointer.
func (s *ProviderSelector) selectModelFromIDs(id, provider string, auth llm.AuthMethod) tea.Cmd {
	s.active = false
	return func() tea.Msg {
		return providerModelSelectedMsg{
			ModelID:      id,
			ProviderName: provider,
			AuthMethod:   auth,
		}
	}
}

// selectMarkedModel confirms the model the user marked with Space (the one
// rendered [*]). Used by Select() when an explicit mark exists, so the choice
// does not depend on cursor position. Returns nil if nothing is marked.
func (s *ProviderSelector) selectMarkedModel() tea.Cmd {
	for _, m := range s.allModels {
		if m.IsCurrent {
			return s.selectModelFromIDs(m.ID, m.ProviderName, m.AuthMethod)
		}
	}
	return nil
}

// toggleModel marks the currently highlighted model item (radio-style: marking
// one clears the others). Unlike Select (Enter), it only updates the IsCurrent
// flag visually and does NOT activate the model or close the overlay; the mark
// is what a subsequent Enter confirms.
func (s *ProviderSelector) toggleModel() tea.Cmd {
	if s.selectedIdx < 0 || s.selectedIdx >= len(s.visibleItems) {
		return nil
	}
	item := s.visibleItems[s.selectedIdx]
	if item.Kind != providerItemModel || item.Model == nil {
		return nil
	}
	m := item.Model
	// Match on auth method too: the same model ID can be offered by more than one
	// auth method (e.g. OpenAI via Direct API and via ChatGPT subscription), and
	// marking must pin the exact row so the selection routes through the intended
	// auth method rather than silently falling back to the other.
	for i := range s.allModels {
		s.allModels[i].IsCurrent = markedModel(s.allModels[i], m)
	}
	for i := range s.filteredModels {
		s.filteredModels[i].IsCurrent = markedModel(s.filteredModels[i], m)
	}
	for i := range s.visibleItems {
		if s.visibleItems[i].Kind == providerItemModel && s.visibleItems[i].Model != nil {
			vi := s.visibleItems[i].Model
			vi.IsCurrent = markedModel(*vi, m)
		}
	}
	s.modelMarked = true
	return nil
}

// markedModel reports whether candidate is the same model row as marked,
// identified by model ID, provider, and auth method. Auth method is part of the
// identity because one model ID can be reachable through several auth methods.
func markedModel(candidate providerModelItem, marked *providerModelItem) bool {
	return candidate.ID == marked.ID &&
		candidate.ProviderName == marked.ProviderName &&
		candidate.AuthMethod == marked.AuthMethod
}

// selectProvider handles Enter on a provider row (Providers tab).
// Connected single auth method: refresh models.
// Disconnected single auth method: auto-connect or show API key input.
// Multiple auth methods: expand inline to show auth method list.
func (s *ProviderSelector) selectProvider(item providerListItem) tea.Cmd {
	if item.Provider == nil {
		return nil
	}
	p := item.Provider

	// The custom provider needs ID + baseURL + apiKey, so it gets its own form
	// instead of the single API-key input. Once connected, Enter refreshes as usual.
	if len(p.AuthMethods) == 1 && s.isCustomProvider(p.Provider) && p.AuthMethods[0].Status != llm.StatusConnected {
		s.openCustomForm()
		return nil
	}

	// Ollama similarly uses a dedicated base-URL form (no API key needed).
	if len(p.AuthMethods) == 1 && s.isOllamaProvider(p.Provider) && p.AuthMethods[0].Status != llm.StatusConnected {
		s.openOllamaForm()
		return nil
	}

	if len(p.AuthMethods) == 1 {
		am := p.AuthMethods[0]
		if am.Status == llm.StatusConnected {
			// Refresh: re-fetch models for this connected provider
			return s.refreshAuthMethod(am, s.selectedIdx)
		}
		return s.tryConnectOrPromptKey(am, item.ProviderIdx, 0)
	}

	if len(p.AuthMethods) == 0 {
		return nil
	}

	// Multiple auth methods: toggle inline expansion
	if s.expandedProviderIdx == item.ProviderIdx {
		s.expandedProviderIdx = -1
	} else {
		s.expandedProviderIdx = item.ProviderIdx
	}
	s.resetConnectionResult()
	s.rebuildVisibleItems()
	return nil
}

func (s *ProviderSelector) selectAuthMethod(item providerListItem) tea.Cmd {
	if item.AuthMethod == nil {
		return nil
	}
	am := item.AuthMethod

	if am.Status == llm.StatusConnected {
		// Refresh: re-fetch models for this connected auth method
		return s.refreshAuthMethod(*am, s.selectedIdx)
	}

	return s.tryConnectOrPromptKey(*am, item.ProviderIdx, s.findAuthMethodIndex(item))
}

func (s *ProviderSelector) findAuthMethodIndex(item providerListItem) int {
	if item.AuthMethod == nil || item.ProviderIdx < 0 || item.ProviderIdx >= len(s.allProviders) {
		return 0
	}
	p := &s.allProviders[item.ProviderIdx]
	for i, am := range p.AuthMethods {
		if am.Provider == item.AuthMethod.Provider && am.AuthMethod == item.AuthMethod.AuthMethod {
			return i
		}
	}
	return 0
}

// SetModel sets the current model.
func (s *ProviderSelector) SetModel(modelID string, providerName string, authMethod llm.AuthMethod) (string, error) {
	if s.store == nil {
		store, err := llm.NewStore()
		if err != nil {
			return "", fmt.Errorf("failed to load store: %w", err)
		}
		s.store = store
	}

	if err := s.store.SetCurrentModel(modelID, llm.ProviderID(providerName), authMethod); err != nil {
		return "", fmt.Errorf("failed to set model: %w", err)
	}

	return fmt.Sprintf("Model set to: %s (%s)", modelID, providerName), nil
}
