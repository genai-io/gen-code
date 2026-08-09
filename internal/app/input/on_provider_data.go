// Provider selector: data loading, visible-list building, and lifecycle.
package input

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/llm"
)

// Enter opens the unified model & provider kit.
func (s *ProviderSelector) Enter(ctx context.Context, width, height int) (tea.Cmd, error) {
	s.resetNavigation()
	s.resetModelSearch()
	s.resetConnectionResult()
	s.expandedProviderIdx = -1
	s.apiKeyActive = false
	s.closeCustomForm()
	s.closeOllamaForm()
	s.active = true
	s.activeTab = providerTabModels
	s.width = width
	s.height = height

	cmd, err := s.loadProviderData()
	if err != nil {
		return nil, err
	}
	s.rebuildVisibleItems()
	return cmd, nil
}

// loadProviderData refreshes provider and model data from a fresh store.
// Does NOT reset UI state (tabs, selection, expansion) or call rebuildVisibleItems.
func (s *ProviderSelector) loadProviderData() (tea.Cmd, error) {
	store, err := llm.NewStore()
	if err != nil {
		return nil, fmt.Errorf("failed to load store: %w", err)
	}
	s.store = store

	providersWithStatus := llm.GetProvidersWithStatus(store)

	s.connectedProviders = nil
	s.allProviders = nil

	for _, p := range llm.ProvidersByOrder() {
		infos, ok := providersWithStatus[p]
		if !ok || len(infos) == 0 {
			continue
		}

		item := providerProviderItem{
			Provider:    p,
			DisplayName: llm.ProviderDisplayName(p),
			AuthMethods: make([]providerAuthMethodItem, 0, len(infos)),
		}

		connected := false
		for _, info := range infos {
			item.AuthMethods = append(item.AuthMethods, providerAuthMethodItem{
				Provider:    info.Meta.Provider,
				AuthMethod:  info.Meta.AuthMethod,
				DisplayName: info.Meta.DisplayName,
				Status:      info.Status,
				EnvVars:     info.Meta.EnvVars,
			})
			if info.Status == llm.StatusConnected {
				connected = true
			}
		}

		item.Connected = connected
		s.allProviders = append(s.allProviders, item)
		if connected {
			s.connectedProviders = append(s.connectedProviders, item)
		}
	}

	current := store.GetCurrentModel()

	s.allModels = nil
	allCached := store.GetAllCachedModels()
	if len(allCached) == 0 {
		allCached = store.GetAllCachedModelsIncludeExpired()
	}

	if len(allCached) > 0 {
		s.loadModelsCached(allCached, current)
	}
	asyncCmd := s.loadModelsAsync(store, current)

	s.ensureModelProvidersExist()
	s.sortConnectedProviders(current)

	return asyncCmd, nil
}

// ensureModelProvidersExist ensures every provider that has cached models
// is represented in connectedProviders (handles cases where registry doesn't
// have the provider registered but models exist in cache).
func (s *ProviderSelector) ensureModelProvidersExist() {
	existing := make(map[string]bool)
	for _, cp := range s.connectedProviders {
		existing[string(cp.Provider)] = true
	}

	// Collect unique provider names from models
	seen := make(map[string]bool)
	for _, m := range s.allModels {
		if existing[m.ProviderName] || seen[m.ProviderName] {
			continue
		}
		seen[m.ProviderName] = true

		displayName := llm.ProviderDisplayName(llm.Name(m.ProviderName))
		if displayName == "" {
			displayName = m.ProviderName
		}

		s.connectedProviders = append(s.connectedProviders, providerProviderItem{
			Provider:    llm.Name(m.ProviderName),
			DisplayName: displayName,
			Connected:   true,
		})
	}
}

// loadModelsAsync returns a tea.Cmd that fetches models from all connected
// providers concurrently, sending a providerModelsLoadedMsg when done.
func (s *ProviderSelector) loadModelsAsync(store *llm.Store, current *llm.CurrentModelInfo) tea.Cmd {
	connections := store.GetConnections()
	return func() tea.Msg {
		ctx := context.Background()

		type providerResult struct {
			providerName string
			authMethod   llm.AuthMethod
			models       []llm.ModelInfo
		}

		ch := make(chan providerResult, len(connections))
		var wg sync.WaitGroup

		for name, conn := range connections {
			wg.Add(1)
			go func(providerName string, authMethod llm.AuthMethod) {
				defer wg.Done()
				// On failure, fall back to this provider's cached models. The
				// result replaces the whole list, so returning nothing on a
				// transient blip would drop an otherwise-connected provider's
				// models from the picker.
				if p, err := llm.GetProvider(ctx, llm.Name(providerName), authMethod); err == nil {
					if mdls, err := p.ListModels(ctx); err == nil {
						ch <- providerResult{providerName, authMethod, mdls}
						return
					}
				}
				if cached, ok := store.GetCachedModels(llm.Name(providerName), authMethod); ok {
					ch <- providerResult{providerName, authMethod, cached}
				}
			}(name, conn.AuthMethod)
		}

		go func() { wg.Wait(); close(ch) }()

		var models []providerModelItem
		for r := range ch {
			prov := llm.Name(r.providerName)
			_ = store.CacheModels(prov, r.authMethod, r.models)

			for _, mdl := range r.models {
				models = append(models, newProviderModelItem(mdl, r.providerName, r.authMethod, current))
			}
		}
		return providerModelsLoadedMsg{Models: models}
	}
}

// HandleModelsLoaded updates the panel with asynchronously loaded models.
func (s *ProviderSelector) HandleModelsLoaded(msg providerModelsLoadedMsg) {
	s.allModels = msg.Models
	s.ensureModelProvidersExist()

	var current *llm.CurrentModelInfo
	if s.store != nil {
		current = s.store.GetCurrentModel()
	}
	s.sortConnectedProviders(current)
	s.rebuildVisibleItems()
}

// loadModelsCached loads models from the store cache.
func (s *ProviderSelector) loadModelsCached(allCached map[string][]llm.ModelInfo, current *llm.CurrentModelInfo) {
	for key, models := range allCached {
		parts := strings.SplitN(key, ":", 2)
		providerName := key
		var authMethod llm.AuthMethod
		if len(parts) >= 2 {
			providerName = parts[0]
			authMethod = llm.AuthMethod(parts[1])
		}

		for _, mdl := range models {
			s.allModels = append(s.allModels, newProviderModelItem(mdl, providerName, authMethod, current))
		}
	}
}

func (s *ProviderSelector) replaceModelsForAuthMethod(provider llm.Name, authMethod llm.AuthMethod, models []llm.ModelInfo) {
	if provider == "" {
		return
	}

	var current *llm.CurrentModelInfo
	if s.store != nil {
		current = s.store.GetCurrentModel()
	}

	providerName := string(provider)
	s.allModels = slices.DeleteFunc(s.allModels, func(item providerModelItem) bool {
		return item.ProviderName == providerName && item.AuthMethod == authMethod
	})
	for _, mdl := range models {
		s.allModels = append(s.allModels, newProviderModelItem(mdl, providerName, authMethod, current))
	}

	s.ensureModelProvidersExist()
	s.sortConnectedProviders(current)
	s.updateFilter()
}

// sortConnectedProviders sorts connected providers so that the current
// selection's provider comes first, then alphabetical.
func (s *ProviderSelector) sortConnectedProviders(current *llm.CurrentModelInfo) {
	if current == nil {
		return
	}
	currentProvider := current.Provider
	sort.SliceStable(s.connectedProviders, func(i, j int) bool {
		iMatch := s.connectedProviders[i].Provider == currentProvider
		jMatch := s.connectedProviders[j].Provider == currentProvider
		if iMatch != jMatch {
			return iMatch
		}
		return false
	})
}

// rebuildVisibleItems constructs the flat visible-items list from current state.
func (s *ProviderSelector) rebuildVisibleItems() {
	s.visibleItems = nil

	switch s.activeTab {
	case providerTabModels:
		s.rebuildModelsTab()
	case providerTabProviders:
		s.rebuildProvidersTab()
	}

	s.clampSelection()
}

// rebuildModelsTab builds visible items for the Models tab.
func (s *ProviderSelector) rebuildModelsTab() {
	s.updateFilter()

	// Group filtered models by provider
	providerModels := make(map[string][]providerModelItem)
	for i := range s.filteredModels {
		m := &s.filteredModels[i]
		providerModels[m.ProviderName] = append(providerModels[m.ProviderName], *m)
	}

	for i := range s.connectedProviders {
		cp := &s.connectedProviders[i]
		models := providerModels[string(cp.Provider)]
		if len(models) == 0 && s.searchQuery != "" {
			continue
		}

		s.visibleItems = append(s.visibleItems, providerListItem{
			Kind:        providerItemProviderHeader,
			Provider:    cp,
			ProviderIdx: i,
		})

		sortProviderModelsByNameDescending(models)

		for j := range models {
			s.visibleItems = append(s.visibleItems, providerListItem{
				Kind:        providerItemModel,
				Model:       &models[j],
				ProviderIdx: i,
			})
		}
	}
}

// sortProviderModelsByNameDescending keeps each provider's model picker in a
// predictable descending name order. Context-window metadata is independent of
// presentation order and remains available in InputTokenLimit.
func sortProviderModelsByNameDescending(models []providerModelItem) {
	sort.SliceStable(models, func(a, b int) bool {
		nameA := modelSortName(models[a])
		nameB := modelSortName(models[b])
		if cmp := strings.Compare(strings.ToLower(nameA), strings.ToLower(nameB)); cmp != 0 {
			return cmp > 0
		}
		return strings.Compare(models[a].ID, models[b].ID) > 0
	})
}

func modelSortName(model providerModelItem) string {
	if model.DisplayName != "" {
		return model.DisplayName
	}
	if model.Name != "" {
		return model.Name
	}
	return model.ID
}

// rebuildProvidersTab builds visible items for the Providers tab.
func (s *ProviderSelector) rebuildProvidersTab() {
	for i := range s.allProviders {
		p := &s.allProviders[i]

		// Apply search filter on provider name
		if s.searchQuery != "" {
			query := strings.ToLower(s.searchQuery)
			if !kit.FuzzyMatch(strings.ToLower(p.DisplayName), query) &&
				!kit.FuzzyMatch(strings.ToLower(string(p.Provider)), query) {
				continue
			}
		}

		s.visibleItems = append(s.visibleItems, providerListItem{
			Kind:        providerItemProvider,
			Provider:    p,
			ProviderIdx: i,
		})

		// Show expanded auth methods
		if s.expandedProviderIdx == i {
			for j := range p.AuthMethods {
				s.visibleItems = append(s.visibleItems, providerListItem{
					Kind:        providerItemAuthMethod,
					AuthMethod:  &p.AuthMethods[j],
					ProviderIdx: i,
				})
			}
		}
	}
}

func (s *ProviderSelector) updateFilter() {
	if s.searchQuery == "" {
		s.filteredModels = s.allModels
		return
	}
	query := strings.ToLower(s.searchQuery)
	s.filteredModels = nil
	for _, m := range s.allModels {
		if kit.FuzzyMatch(strings.ToLower(m.ID), query) ||
			kit.FuzzyMatch(strings.ToLower(m.DisplayName), query) ||
			kit.FuzzyMatch(strings.ToLower(m.ProviderName), query) {
			s.filteredModels = append(s.filteredModels, m)
		}
	}
}

func (s *ProviderSelector) clampSelection() {
	if len(s.visibleItems) == 0 {
		s.selectedIdx = 0
		return
	}
	if s.selectedIdx >= len(s.visibleItems) {
		s.selectedIdx = len(s.visibleItems) - 1
	}
	if s.selectedIdx < 0 {
		s.selectedIdx = 0
	}
	// Skip non-selectable items forward
	if s.visibleItems[s.selectedIdx].Kind == providerItemProviderHeader {
		for s.selectedIdx < len(s.visibleItems)-1 {
			s.selectedIdx++
			if s.visibleItems[s.selectedIdx].Kind != providerItemProviderHeader {
				break
			}
		}
	}
}

func (s *ProviderSelector) resetConnectionResult() {
	s.lastConnectResult = ""
	s.lastConnectAuthIdx = 0
	s.lastConnectSuccess = false
}

func (s *ProviderSelector) resetModelSearch() {
	s.searchQuery = ""
	s.filteredModels = nil
	s.scrollOffset = 0
}

func (s *ProviderSelector) resetNavigation() {
	s.selectedIdx = 0
	s.scrollOffset = 0
	s.searchFocused = false
	s.modelMarked = false
}

// Cancel cancels the selector and clears transient state so the next open starts cleanly.
func (s *ProviderSelector) Cancel() {
	s.active = false
	s.cancelInteractiveLogin()
	s.connectedProviders = nil
	s.allProviders = nil
	s.allModels = nil
	s.filteredModels = nil
	s.visibleItems = nil
	s.expandedProviderIdx = -1
	s.apiKeyActive = false
	s.closeCustomForm()
	s.closeOllamaForm()
	s.store = nil
	s.resetNavigation()
	s.resetModelSearch()
	s.resetConnectionResult()
}
