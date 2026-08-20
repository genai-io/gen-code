// Plugin selector: navigation, actions, loading, and reset.
package input

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	coreplugin "github.com/genai-io/san/internal/plugin"
)

// ── Navigation ───────────────────────────────────────────────────────────────

// Tab navigation
func (s *PluginSelector) NextTab() { s.switchTab((s.activeTab + 1) % 3) }
func (s *PluginSelector) PrevTab() { s.switchTab((s.activeTab + 2) % 3) }

func (s *PluginSelector) switchTab(tab pluginTab) {
	s.activeTab = tab
	s.resetListState()
	s.resetDetailState()
	s.resetBrowseState()
	s.searchQuery = ""
	s.refreshCurrentTab()
}

// updateFilter filters items based on search query
func (s *PluginSelector) updateFilter() {
	query := strings.ToLower(s.searchQuery)
	s.filteredItems = s.filterItemsForTab(query)
	s.selectedIdx = 0
	s.scrollOffset = 0
}

// filterItemsForTab returns filtered items based on the active tab and query
func (s *PluginSelector) filterItemsForTab(query string) []any {
	switch s.activeTab {
	case pluginTabInstalled:
		return pluginFilterItems(s.installedFlatList, query, func(p pluginItem) []string {
			return []string{p.Name, p.Description}
		})
	case pluginTabDiscover:
		return pluginFilterItems(s.discoverPlugins, query, func(p pluginDiscoverItem) []string {
			return []string{p.Name, p.Description, p.Marketplace}
		})
	case pluginTabMarketplaces:
		return pluginFilterItems(s.marketplaces, query, func(m pluginMarketplaceItem) []string {
			return []string{m.ID, m.Source}
		})
	default:
		return nil
	}
}

// pluginFilterItems is a generic filter function for any slice type
func pluginFilterItems[T any](items []T, query string, getFields func(T) []string) []any {
	if query == "" {
		result := make([]any, len(items))
		for i, item := range items {
			result[i] = item
		}
		return result
	}

	result := make([]any, 0, len(items))
	for _, item := range items {
		for _, field := range getFields(item) {
			if kit.FuzzyMatch(strings.ToLower(field), query) {
				result = append(result, item)
				break
			}
		}
	}
	return result
}

// Navigation
func (s *PluginSelector) MoveUp() {
	s.clearMessage()
	switch s.level {
	case pluginLevelDetail, pluginLevelInstallOptions:
		if s.actionIdx > 0 {
			s.actionIdx--
		} else if s.detailScroll > 0 {
			s.detailScroll--
		}
	default:
		if s.selectedIdx > 0 {
			s.selectedIdx--
			s.ensureVisible()
		}
	}
}

func (s *PluginSelector) MoveDown() {
	s.clearMessage()
	switch s.level {
	case pluginLevelDetail, pluginLevelInstallOptions:
		if s.actionIdx < len(s.actions)-1 {
			s.actionIdx++
		} else {
			s.detailScroll++
		}
	default:
		maxIdx := s.getMaxIndex()
		if s.selectedIdx < maxIdx {
			s.selectedIdx++
			s.ensureVisible()
		}
	}
}

// getMaxIndex returns the maximum selectable index for the current view.
func (s *PluginSelector) getMaxIndex() int {
	switch s.level {
	case pluginLevelBrowsePlugins:
		return len(s.browsePlugins) - 1
	default:
		maxIdx := len(s.filteredItems) - 1
		if s.activeTab == pluginTabMarketplaces {
			maxIdx++
		}
		return maxIdx
	}
}

func (s *PluginSelector) ensureVisible() {
	visible := s.maxVisible
	switch s.level {
	case pluginLevelBrowsePlugins:
		visible = max(4, s.height-14)
	default:
		switch s.activeTab {
		case pluginTabDiscover:
			visible = max(3, (s.height-14)/3)
		case pluginTabMarketplaces:
			visible = max(4, (s.height-14)/2)
		default:
			visible = max(4, s.height-14)
		}
	}
	if s.selectedIdx < s.scrollOffset {
		s.scrollOffset = s.selectedIdx
	}
	if s.selectedIdx >= s.scrollOffset+visible {
		s.scrollOffset = s.selectedIdx - visible + 1
	}
}

// enterDetail enters the detail view for the selected item.
func (s *PluginSelector) enterDetail() {
	s.parentIdx = s.selectedIdx
	s.clearMessage() // a fresh action view shouldn't show a prior op's result

	switch s.activeTab {
	case pluginTabInstalled:
		s.enterInstalledDetail()
	case pluginTabDiscover:
		s.enterDiscoverDetail()
	case pluginTabMarketplaces:
		s.enterMarketplaceDetail()
	}
}

func (s *PluginSelector) enterInstalledDetail() {
	if s.selectedIdx >= len(s.filteredItems) {
		return
	}
	if p, ok := s.filteredItems[s.selectedIdx].(pluginItem); ok {
		s.detailPlugin = &p
		s.actions = s.buildInstalledActions(p)
		s.actionIdx = 0
		s.level = pluginLevelDetail
	}
}

func (s *PluginSelector) enterDiscoverDetail() {
	if s.selectedIdx >= len(s.filteredItems) {
		return
	}
	if p, ok := s.filteredItems[s.selectedIdx].(pluginDiscoverItem); ok {
		s.detailDiscover = &p
		s.actions = s.buildDiscoverActions(p)
		s.actionIdx = 0
		s.level = pluginLevelDetail
	}
}

func (s *PluginSelector) enterMarketplaceDetail() {
	if s.selectedIdx == 0 {
		s.level = pluginLevelAddMarketplace
		s.addMarketplaceInput = ""
		s.addDialogCursor = 0
		return
	}
	mktIdx := s.selectedIdx - 1
	if mktIdx >= len(s.filteredItems) {
		return
	}
	if m, ok := s.filteredItems[mktIdx].(pluginMarketplaceItem); ok {
		s.detailMarketplace = &m
		s.actions = s.buildMarketplaceActions(m)
		s.actionIdx = 0
		s.level = pluginLevelDetail
	}
}

// goBack returns to the previous view.
func (s *PluginSelector) goBack() bool {
	switch s.level {
	case pluginLevelDetail:
		s.level = pluginLevelTabList
		s.selectedIdx = s.parentIdx
		s.resetDetailState()
		s.clearMessage()
		return true
	case pluginLevelInstallOptions:
		s.level = pluginLevelDetail
		s.actions = s.buildDiscoverActions(*s.detailDiscover)
		s.actionIdx = 0
		return true
	case pluginLevelAddMarketplace:
		s.level = pluginLevelTabList
		s.addMarketplaceInput = ""
		return true
	case pluginLevelBrowsePlugins:
		s.level = pluginLevelDetail
		s.resetBrowseState()
		s.selectedIdx = 0
		return true
	}
	return false
}

// ── Actions ──────────────────────────────────────────────────────────────────

// buildInstalledActions returns actions for an installed plugin.
func (s *PluginSelector) buildInstalledActions(p pluginItem) []pluginAction {
	actions := []pluginAction{}
	if p.Enabled {
		actions = append(actions, pluginAction{Label: "Disable plugin", Action: "disable"})
	} else {
		actions = append(actions, pluginAction{Label: "Enable plugin", Action: "enable"})
	}
	actions = append(actions,
		pluginAction{Label: "Uninstall", Action: "uninstall"},
		pluginAction{Label: "Back to plugin list", Action: "back"},
	)
	return actions
}

// buildDiscoverActions returns actions for a discoverable plugin.
func (s *PluginSelector) buildDiscoverActions(p pluginDiscoverItem) []pluginAction {
	actions := []pluginAction{}
	if !p.Installed {
		actions = append(actions,
			pluginAction{Label: "Install for you (user scope)", Action: "install_user"},
			pluginAction{Label: "Install for all collaborators (project scope)", Action: "install_project"},
			pluginAction{Label: "Install for you, in this repo only (local scope)", Action: "install_local"},
		)
	} else {
		actions = append(actions, pluginAction{Label: "Already installed", Action: "none"})
	}
	if p.Homepage != "" {
		actions = append(actions, pluginAction{Label: "Open homepage", Action: "homepage"})
	}
	actions = append(actions, pluginAction{Label: "Back to plugin list", Action: "back"})
	return actions
}

// buildMarketplaceActions returns actions for a marketplace.
func (s *PluginSelector) buildMarketplaceActions(m pluginMarketplaceItem) []pluginAction {
	return []pluginAction{
		{Label: fmt.Sprintf("Browse plugins (%d)", m.Available), Action: "browse"},
		{Label: "Update marketplace", Action: "update"},
		{Label: "Remove marketplace", Action: "remove"},
		{Label: "Back", Action: "back"},
	}
}

// executeAction executes the currently selected action.
func (s *PluginSelector) executeAction() tea.Cmd {
	if s.actionIdx >= len(s.actions) {
		return nil
	}
	action := s.actions[s.actionIdx]

	switch action.Action {
	case "enable":
		if s.detailPlugin != nil {
			return func() tea.Msg { return pluginEnableMsg{PluginName: s.detailPlugin.FullName} }
		}
	case "disable":
		if s.detailPlugin != nil {
			return func() tea.Msg { return pluginDisableMsg{PluginName: s.detailPlugin.FullName} }
		}
	case "uninstall":
		if s.detailPlugin != nil {
			return func() tea.Msg { return pluginUninstallMsg{PluginName: s.detailPlugin.FullName} }
		}
	case "install_user":
		if s.detailDiscover != nil {
			return s.installPlugin(coreplugin.ScopeUser)
		}
	case "install_project":
		if s.detailDiscover != nil {
			return s.installPlugin(coreplugin.ScopeProject)
		}
	case "install_local":
		if s.detailDiscover != nil {
			return s.installPlugin(coreplugin.ScopeLocal)
		}
	case "homepage":
		if s.detailDiscover != nil && s.detailDiscover.Homepage != "" {
			s.setSuccess("Homepage: " + s.detailDiscover.Homepage)
		}
	case "browse":
		if s.detailMarketplace != nil {
			s.browseMarketplace()
		}
	case "update":
		if s.detailMarketplace != nil {
			return s.syncMarketplace(s.detailMarketplace.ID)
		}
	case "remove":
		if s.detailMarketplace != nil {
			return func() tea.Msg { return pluginMarketplaceRemoveMsg{ID: s.detailMarketplace.ID} }
		}
	case "back":
		s.goBack()
	}
	return nil
}

// installPlugin creates an install command for the selected plugin.
func (s *PluginSelector) installPlugin(scope coreplugin.Scope) tea.Cmd {
	if s.detailDiscover == nil {
		return nil
	}
	name := s.detailDiscover.Name
	marketplace := s.detailDiscover.Marketplace
	// The spinner starts when UpdatePlugin handles pluginInstallMsg, so install
	// shares the one beginLoading path with every other mutating op.
	return func() tea.Msg {
		return pluginInstallMsg{
			PluginName:  name,
			Marketplace: marketplace,
			Scope:       scope,
		}
	}
}

// browseMarketplace enters the browse view for a marketplace.
func (s *PluginSelector) browseMarketplace() {
	if s.detailMarketplace == nil {
		return
	}

	s.browseMarketplaceID = s.detailMarketplace.ID
	s.browsePlugins = []pluginDiscoverItem{}

	plugins, err := s.marketplaceManager.MarketplacePlugins(s.detailMarketplace.ID)
	if err != nil {
		s.setError(fmt.Sprintf("Failed to list plugins: %v", err))
		return
	}

	installedNames := s.getInstalledNames()
	for _, mp := range plugins {
		item := s.newDiscoverItem(mp.Name, s.detailMarketplace.ID, installedNames)
		applyMarketplacePlugin(&item, mp)
		s.enrichDiscoverItem(&item)
		s.browsePlugins = append(s.browsePlugins, item)
	}

	s.level = pluginLevelBrowsePlugins
	s.selectedIdx = 0
	s.scrollOffset = 0
}

// syncMarketplace clones or updates a marketplace, driving the shared spinner.
func (s *PluginSelector) syncMarketplace(id string) tea.Cmd {
	mgr := s.marketplaceManager
	return tea.Batch(
		s.beginLoading("Syncing "+id+"..."),
		runPluginOp(pluginOpResultMsg{okMsg: "Synced " + id, errCtx: "sync " + id},
			func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				return mgr.SyncOrPrune(ctx, id)
			}),
	)
}

// toggleSelectedPlugin toggles enable/disable for the selected plugin.
func (s *PluginSelector) toggleSelectedPlugin() tea.Cmd {
	if s.activeTab == pluginTabInstalled && s.level == pluginLevelTabList && s.selectedIdx < len(s.filteredItems) {
		if p, ok := s.filteredItems[s.selectedIdx].(pluginItem); ok {
			if p.Enabled {
				return func() tea.Msg { return pluginDisableMsg{PluginName: p.FullName} }
			}
			return func() tea.Msg { return pluginEnableMsg{PluginName: p.FullName} }
		}
	}
	return nil
}

// setError sets an error message.
func (s *PluginSelector) setError(msg string) {
	s.lastMessage = msg
	s.isError = true
}

// setSuccess sets a success message.
func (s *PluginSelector) setSuccess(msg string) {
	s.lastMessage = msg
	s.isError = false
}

// clearMessage clears the status message.
func (s *PluginSelector) clearMessage() {
	s.lastMessage = ""
	s.isError = false
}

// ── Load ─────────────────────────────────────────────────────────────────────

// EnterSelect enters plugin selection mode
func (s *PluginSelector) EnterSelect(width, height int) error {
	s.active = true
	s.width = width
	s.height = height
	s.resetListState()
	s.resetDetailState()
	s.resetBrowseState()
	s.resetInputState()
	s.resetLoadingState()
	s.clearMessage()

	s.maxVisible = max(4, height-14)

	if err := s.marketplaceManager.Load(); err != nil {
		s.setError(fmt.Sprintf("Failed to load marketplaces: %v", err))
	}
	_ = s.installer.LoadMarketplaces() // Non-fatal

	s.refreshCurrentTab()
	return nil
}

// refreshCurrentTab refreshes data for the current tab
func (s *PluginSelector) refreshCurrentTab() {
	switch s.activeTab {
	case pluginTabInstalled:
		s.refreshInstalledPlugins()
	case pluginTabDiscover:
		s.refreshDiscoverPlugins()
	case pluginTabMarketplaces:
		s.refreshMarketplaces()
	}
	s.updateFilter()
}

// refreshInstalledPlugins loads installed plugins grouped by scope
func (s *PluginSelector) refreshInstalledPlugins() {
	plugins := s.registry.List()
	s.installedPlugins = make(map[coreplugin.Scope][]pluginItem)

	for _, p := range plugins {
		item := pluginItem{
			Name:        p.Manifest.Name,
			FullName:    p.FullName(),
			Description: p.Manifest.Description,
			Version:     p.Manifest.Version,
			Scope:       p.Scope,
			Enabled:     p.Enabled,
			Path:        p.Path,
			Skills:      len(p.Components.Skills),
			Agents:      len(p.Components.Agents),
			Commands:    len(p.Components.Commands),
			MCP:         len(p.Components.MCP),
			LSP:         len(p.Components.LSP),
			Errors:      p.Errors,
		}
		if p.Components.Hooks != nil {
			item.Hooks = len(p.Components.Hooks.Hooks)
		}
		if p.Manifest.Author != nil {
			item.Author = p.Manifest.Author.Name
		}
		item.Homepage = p.Manifest.Homepage

		if idx := strings.Index(p.Source, "@"); idx != -1 {
			item.Marketplace = p.Source[idx+1:]
		}

		s.installedPlugins[p.Scope] = append(s.installedPlugins[p.Scope], item)
	}

	for scope := range s.installedPlugins {
		sort.Slice(s.installedPlugins[scope], func(i, j int) bool {
			return s.installedPlugins[scope][i].Name < s.installedPlugins[scope][j].Name
		})
	}

	s.installedScopes = []coreplugin.Scope{}
	for _, scope := range []coreplugin.Scope{coreplugin.ScopeUser, coreplugin.ScopeProject, coreplugin.ScopeLocal, coreplugin.ScopeManaged} {
		if len(s.installedPlugins[scope]) > 0 {
			s.installedScopes = append(s.installedScopes, scope)
		}
	}

	s.installedFlatList = []pluginItem{}
	for _, scope := range s.installedScopes {
		s.installedFlatList = append(s.installedFlatList, s.installedPlugins[scope]...)
	}
}

// refreshDiscoverPlugins loads available plugins from all marketplaces
func (s *PluginSelector) refreshDiscoverPlugins() {
	s.discoverPlugins = []pluginDiscoverItem{}
	installedNames := s.getInstalledNames()

	for _, marketplaceID := range s.marketplaceManager.List() {
		plugins, err := s.marketplaceManager.MarketplacePlugins(marketplaceID)
		if err != nil {
			continue
		}

		for _, mp := range plugins {
			item := s.newDiscoverItem(mp.Name, marketplaceID, installedNames)
			applyMarketplacePlugin(&item, mp)
			s.enrichDiscoverItem(&item)
			s.discoverPlugins = append(s.discoverPlugins, item)
		}
	}

	sort.Slice(s.discoverPlugins, func(i, j int) bool {
		if s.discoverPlugins[i].Marketplace != s.discoverPlugins[j].Marketplace {
			return s.discoverPlugins[i].Marketplace < s.discoverPlugins[j].Marketplace
		}
		return s.discoverPlugins[i].Name < s.discoverPlugins[j].Name
	})
}

// refreshMarketplaces loads marketplace information
func (s *PluginSelector) refreshMarketplaces() {
	s.marketplaces = []pluginMarketplaceItem{}

	installedCounts := make(map[string]int)
	for _, p := range s.registry.List() {
		if idx := strings.Index(p.Source, "@"); idx != -1 {
			marketplace := p.Source[idx+1:]
			installedCounts[marketplace]++
		}
	}

	for _, id := range s.marketplaceManager.List() {
		entry, ok := s.marketplaceManager.Get(id)
		if !ok {
			continue
		}

		item := pluginMarketplaceItem{
			ID:         id,
			SourceType: entry.Source.Source,
			Installed:  installedCounts[id],
		}

		switch entry.Source.Source {
		case "github":
			item.Source = "https://github.com/" + entry.Source.Repo
		case "directory":
			item.Source = entry.Source.Path
		}

		if plugins, err := s.marketplaceManager.MarketplacePlugins(id); err == nil {
			item.Available = len(plugins)
		}

		if entry.LastUpdated != "" {
			if t, err := time.Parse(time.RFC3339, entry.LastUpdated); err == nil {
				item.LastUpdated = t.Format("1/2/2006")
			}
		}

		item.IsOfficial = id == "claude-plugins-official"
		s.marketplaces = append(s.marketplaces, item)
	}

	sort.Slice(s.marketplaces, func(i, j int) bool {
		if s.marketplaces[i].IsOfficial != s.marketplaces[j].IsOfficial {
			return s.marketplaces[i].IsOfficial
		}
		return s.marketplaces[i].ID < s.marketplaces[j].ID
	})
}

// getInstalledNames returns a set of installed plugin names for quick lookup.
func (s *PluginSelector) getInstalledNames() map[string]bool {
	names := make(map[string]bool)
	for _, p := range s.registry.List() {
		names[p.FullName()] = true
		names[p.Name()] = true
	}
	return names
}

// newDiscoverItem creates a pluginDiscoverItem with installed status set.
func (s *PluginSelector) newDiscoverItem(name, marketplaceID string, installed map[string]bool) pluginDiscoverItem {
	fullName := name + "@" + marketplaceID
	return pluginDiscoverItem{
		Name:        name,
		Marketplace: marketplaceID,
		Installed:   installed[fullName] || installed[name],
	}
}

// applyMarketplacePlugin copies the metadata a marketplace.json entry declares
// into a discover item. This is the only metadata available for plugins whose
// content lives in another repo and isn't fetched until install.
func applyMarketplacePlugin(item *pluginDiscoverItem, mp coreplugin.MarketplacePlugin) {
	item.Description = mp.Description
	item.Version = mp.Version
	if mp.Author != nil {
		item.Author = mp.Author.Name
	}
}

// enrichDiscoverItem fills any fields the marketplace.json entry left empty from
// the plugin's own manifest on disk. It is best-effort: plugins whose content
// isn't vendored in the marketplace repo (external sources, not yet installed)
// simply keep the marketplace metadata.
func (s *PluginSelector) enrichDiscoverItem(item *pluginDiscoverItem) {
	fullName := item.Name + "@" + item.Marketplace
	pluginPath, err := s.marketplaceManager.GetPluginPath(item.Marketplace, item.Name)
	if err != nil {
		return
	}
	p, err := coreplugin.LoadPlugin(pluginPath, coreplugin.ScopeUser, fullName)
	if err != nil {
		return
	}
	if item.Description == "" {
		item.Description = p.Manifest.Description
	}
	if item.Version == "" {
		item.Version = p.Manifest.Version
	}
	if item.Author == "" && p.Manifest.Author != nil {
		item.Author = p.Manifest.Author.Name
	}
	if item.Homepage == "" {
		item.Homepage = p.Manifest.Homepage
	}
}

// refreshAfterOp reloads every tab's data and the open detail view after a
// mutation, so counts and lists stay consistent no matter which tab the op was
// triggered from. Runs on the warm post-op path, not per keystroke.
func (s *PluginSelector) refreshAfterOp() {
	s.refreshMarketplaces()
	s.refreshDiscoverPlugins()
	s.refreshInstalledPlugins()
	s.updateFilter()
	if s.level == pluginLevelDetail && s.detailPlugin != nil {
		s.refreshDetailView()
	}
}

// refreshDetailView updates the detail plugin and actions after a state change
func (s *PluginSelector) refreshDetailView() {
	if s.detailPlugin == nil {
		return
	}
	name := s.detailPlugin.FullName
	for _, item := range s.filteredItems {
		if p, ok := item.(pluginItem); ok && p.FullName == name {
			s.detailPlugin = &p
			s.actions = s.buildInstalledActions(p)
			s.clampActionIdx()
			return
		}
	}
	s.goBack()
}

func (s *PluginSelector) clampActionIdx() {
	if s.actionIdx >= len(s.actions) {
		s.actionIdx = len(s.actions) - 1
	}
	if s.actionIdx < 0 {
		s.actionIdx = 0
	}
}

// addMarketplace adds a new marketplace
func (s *PluginSelector) addMarketplace() tea.Cmd {
	source := strings.TrimSpace(s.addMarketplaceInput)
	source = strings.TrimPrefix(source, "[")
	source = strings.TrimSuffix(source, "]")
	source = strings.TrimSpace(source)
	if source == "" {
		s.setError("Please enter a marketplace source")
		return nil
	}

	var id string
	var err error

	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~") {
		absPath := source
		if strings.HasPrefix(source, "~") {
			home, _ := os.UserHomeDir()
			absPath = filepath.Join(home, source[1:])
		}
		id = filepath.Base(absPath)
		err = s.marketplaceManager.AddDirectory(id, absPath)
	} else if after, ok := strings.CutPrefix(source, "https://github.com/"); ok {
		repo := after
		repo = strings.TrimSuffix(repo, ".git")
		repo = strings.TrimSuffix(repo, "/")
		parts := strings.Split(repo, "/")
		if len(parts) >= 2 {
			id = parts[len(parts)-1]
			err = s.marketplaceManager.AddGitHub(id, repo)
		} else {
			s.setError("Invalid GitHub URL format")
			return nil
		}
	} else if strings.Contains(source, "/") && !strings.Contains(source, "://") {
		parts := strings.Split(source, "/")
		id = parts[len(parts)-1]
		err = s.marketplaceManager.AddGitHub(id, source)
	} else {
		s.setError("Invalid source format. Use owner/repo, https://github.com/owner/repo, or ./path")
		return nil
	}

	if err != nil {
		s.setError(fmt.Sprintf("Failed to add marketplace: %v", err))
		return nil
	}

	s.level = pluginLevelTabList
	s.addMarketplaceInput = ""
	s.refreshMarketplaces()

	return s.syncMarketplace(id)
}

// ── Reset ────────────────────────────────────────────────────────────────────

func (s *PluginSelector) resetListState() {
	s.level = pluginLevelTabList
	s.selectedIdx = 0
	s.scrollOffset = 0
	s.parentIdx = 0
}

func (s *PluginSelector) resetDetailState() {
	s.detailPlugin = nil
	s.detailDiscover = nil
	s.detailMarketplace = nil
	s.actions = nil
	s.actionIdx = 0
	s.detailScroll = 0
}

func (s *PluginSelector) resetBrowseState() {
	s.browseMarketplaceID = ""
	s.browsePlugins = nil
}

func (s *PluginSelector) resetInputState() {
	s.searchQuery = ""
	s.filteredItems = nil
	s.addMarketplaceInput = ""
	s.addDialogCursor = 0
}

func (s *PluginSelector) resetLoadingState() {
	s.isLoading = false
	s.loadingMsg = ""
	s.loadingFrame = 0
	s.loadingTicking = false
}

// Cancel cancels the selector and clears transient UI state.
func (s *PluginSelector) Cancel() {
	s.active = false
	s.resetListState()
	s.resetDetailState()
	s.resetBrowseState()
	s.resetInputState()
	s.resetLoadingState()
	s.clearMessage()
}
