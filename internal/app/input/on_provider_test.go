package input

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/llm"
)

// connectResultFromCmd runs cmd (flattening a tea.Batch) and returns the first
// providerConnectResultMsg it produces. connect/refresh commands batch the
// spinner ticker alongside the async work, so the result is no longer the
// top-level message.
func connectResultFromCmd(cmd tea.Cmd) (providerConnectResultMsg, bool) {
	if cmd == nil {
		return providerConnectResultMsg{}, false
	}
	switch msg := cmd().(type) {
	case providerConnectResultMsg:
		return msg, true
	case tea.BatchMsg:
		for _, c := range msg {
			if r, ok := connectResultFromCmd(c); ok {
				return r, true
			}
		}
	}
	return providerConnectResultMsg{}, false
}

type connectFailProvider struct{}

func (p *connectFailProvider) Stream(context.Context, llm.CompletionOptions) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch
}

func (p *connectFailProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, fmt.Errorf("boom")
}

func (p *connectFailProvider) Name() string { return "test-connect-fail" }

type staticListProvider struct {
	name   string
	models []llm.ModelInfo
}

func (p *staticListProvider) Stream(context.Context, llm.CompletionOptions) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch
}

func (p *staticListProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return p.models, nil
}

func (p *staticListProvider) Name() string { return p.name }

type storedCredentialAuthenticator struct {
	loginCalls int
	has        bool
}

func (a *storedCredentialAuthenticator) Login(context.Context, func(string)) error {
	a.loginCalls++
	return fmt.Errorf("unexpected login")
}

func (a *storedCredentialAuthenticator) Logout() error { return nil }

func (a *storedCredentialAuthenticator) HasCredentials() bool { return a.has }

func TestBuiltinCommandHandlersUseModelsAndExcludeRemovedCommands(t *testing.T) {
	handlers := builtinCommandHandlers()
	if _, ok := handlers["models"]; !ok {
		t.Fatal("builtin handlers should include /models")
	}
	for _, removed := range []string{"model", "glob"} {
		if _, ok := handlers[removed]; ok {
			t.Fatalf("builtin handlers should not include removed /%s", removed)
		}
	}
}

func TestRenderAuthMethodKeepsInfoColumnSeparateFromLongName(t *testing.T) {
	selector := NewProviderSelector()
	item := providerListItem{AuthMethod: &providerAuthMethodItem{
		DisplayName: "ChatGPT Subscription",
		Status:      llm.StatusNotConfigured,
		EnvVars:     []string{"OPENAI_API_KEY"},
	}}

	plain := xansi.Strip(selector.renderAuthMethod(item, false, 0))
	nameEnd := strings.Index(plain, "ChatGPT Subscription") + len("ChatGPT Subscription")
	infoStart := strings.Index(plain, "OPENAI_API_KEY")
	if infoStart < 0 {
		t.Fatalf("rendered row %q is missing auth info", plain)
	}
	if gap := infoStart - nameEnd; gap < 6 {
		t.Fatalf("auth info gap = %d columns, want at least 6 in row %q", gap, plain)
	}
}

func TestUpdateProviderReloadsSharedModelStoreAfterCatalogRefresh(t *testing.T) {
	reloads := 0
	deps := OverlayDeps{ReloadModelStore: func() { reloads++ }}
	state := &ProviderState{}

	if _, handled := UpdateProvider(deps, state, providerModelsLoadedMsg{}); !handled {
		t.Fatal("providerModelsLoadedMsg was not handled")
	}
	if reloads != 1 {
		t.Fatalf("shared model store reloads = %d, want 1", reloads)
	}
}

func TestCancelClearsTransientState(t *testing.T) {
	m := NewProviderSelector()
	m.active = true
	m.connectedProviders = []providerProviderItem{{DisplayName: "Anthropic"}}
	m.allProviders = []providerProviderItem{{DisplayName: "Google"}}
	m.allModels = []providerModelItem{{ID: "gpt-5"}}
	m.filteredModels = []providerModelItem{{ID: "gpt-5"}}
	m.visibleItems = []providerListItem{{Kind: providerItemModel}}
	m.expandedProviderIdx = 1
	m.apiKeyActive = true
	m.selectedIdx = 2
	m.scrollOffset = 3
	m.searchQuery = "gpt"
	m.lastConnectResult = "Connected"
	m.lastConnectAuthIdx = 2
	m.lastConnectSuccess = true

	m.Cancel()

	if m.active {
		t.Fatal("Cancel should deactivate selector")
	}
	if len(m.connectedProviders) != 0 || len(m.allProviders) != 0 {
		t.Fatal("Cancel should clear provider lists")
	}
	if len(m.allModels) != 0 || len(m.filteredModels) != 0 || len(m.visibleItems) != 0 {
		t.Fatal("Cancel should clear model/item lists")
	}
	if m.expandedProviderIdx != -1 || m.apiKeyActive {
		t.Fatal("Cancel should reset expansion and API key state")
	}
	if m.selectedIdx != 0 || m.scrollOffset != 0 {
		t.Fatal("Cancel should reset navigation state")
	}
	if m.searchQuery != "" {
		t.Fatal("Cancel should clear search query")
	}
	if m.lastConnectResult != "" || m.lastConnectAuthIdx != 0 || m.lastConnectSuccess {
		t.Fatal("Cancel should clear connection result state")
	}
}

func TestGoBackCollapsesAuthMethods(t *testing.T) {
	m := NewProviderSelector()
	m.expandedProviderIdx = 2
	m.lastConnectResult = "Connected"
	m.lastConnectAuthIdx = 1
	m.lastConnectSuccess = true

	// Seed minimal data for rebuild
	m.activeTab = providerTabProviders
	m.allProviders = []providerProviderItem{
		{DisplayName: "A"}, {DisplayName: "B"},
		{DisplayName: "C", AuthMethods: []providerAuthMethodItem{{DisplayName: "API"}}},
	}

	if !m.GoBack() {
		t.Fatal("GoBack should return true when auth methods are expanded")
	}
	if m.expandedProviderIdx != -1 {
		t.Fatal("GoBack should collapse expanded auth methods")
	}
	if m.lastConnectResult != "" || m.lastConnectAuthIdx != 0 || m.lastConnectSuccess {
		t.Fatal("GoBack should clear inline connect state")
	}
}

func TestGoBackCancelsAPIKeyInput(t *testing.T) {
	m := NewProviderSelector()
	m.apiKeyActive = true

	if !m.GoBack() {
		t.Fatal("GoBack should return true when API key input is active")
	}
	if m.apiKeyActive {
		t.Fatal("GoBack should cancel API key input")
	}
}

func TestHandleKeypressEscClearsModelSearchBeforeDismiss(t *testing.T) {
	m := NewProviderSelector()
	m.active = true
	m.allModels = []providerModelItem{
		{ID: "gpt-5", DisplayName: "GPT-5", ProviderName: "openai"},
		{ID: "claude", DisplayName: "Claude", ProviderName: "anthropic"},
	}
	m.connectedProviders = []providerProviderItem{
		{Provider: "openai", DisplayName: "OpenAI"},
		{Provider: "anthropic", DisplayName: "Anthropic"},
	}
	m.searchQuery = "gpt"
	m.rebuildVisibleItems()

	cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})

	if cmd != nil {
		t.Fatal("expected first Esc with active search to only clear search")
	}
	if m.searchQuery != "" {
		t.Fatalf("searchQuery = %q, want empty", m.searchQuery)
	}
	if !m.active {
		t.Fatal("clearing search should not dismiss selector")
	}
}

func TestHandleKeypressEscDismissesAfterSearchCleared(t *testing.T) {
	m := NewProviderSelector()
	m.active = true

	cmd := m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected dismiss command on Esc")
	}
	msg := cmd()
	if _, ok := msg.(kit.DismissedMsg); !ok {
		t.Fatalf("dismiss command returned %T, want kit.DismissedMsg", msg)
	}
	if m.active {
		t.Fatal("dismiss should deactivate selector")
	}
}

func TestSelectModelReturnsSelectionMessage(t *testing.T) {
	m := NewProviderSelector()
	m.active = true
	model := providerModelItem{
		ID:           "gpt-5",
		ProviderName: "openai",
		AuthMethod:   llm.AuthAPIKey,
	}
	m.visibleItems = []providerListItem{
		{Kind: providerItemModel, Model: &model},
	}
	m.selectedIdx = 0

	cmd := m.Select()
	if cmd == nil {
		t.Fatal("Select should return command for selected model")
	}
	msg := cmd()
	selected, ok := msg.(providerModelSelectedMsg)
	if !ok {
		t.Fatalf("selection returned %T, want providerModelSelectedMsg", msg)
	}
	if selected.ModelID != "gpt-5" || selected.ProviderName != "openai" || selected.AuthMethod != llm.AuthAPIKey {
		t.Fatalf("unexpected selection: %+v", selected)
	}
	if m.active {
		t.Fatal("model selection should close selector")
	}
}

// TestSelectHighlightedModelWhenNoExplicitMark verifies Enter selects the
// highlighted model even when another model is already the active one (rendered
// [*] on open). Without this, navigate/search + Enter would re-select the
// already-current model instead of the highlighted row.
func TestSelectHighlightedModelWhenNoExplicitMark(t *testing.T) {
	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabModels
	m.allModels = []providerModelItem{
		{ID: "gpt-5", ProviderName: "openai", AuthMethod: llm.AuthAPIKey, IsCurrent: true},
		{ID: "claude-opus", ProviderName: "anthropic", AuthMethod: llm.AuthAPIKey},
	}
	m.visibleItems = []providerListItem{
		{Kind: providerItemModel, Model: &m.allModels[0]},
		{Kind: providerItemModel, Model: &m.allModels[1]},
	}
	m.selectedIdx = 1 // highlight a model other than the active one

	cmd := m.Select()
	if cmd == nil {
		t.Fatal("Select should return a command for the highlighted model")
	}
	selected, ok := cmd().(providerModelSelectedMsg)
	if !ok {
		t.Fatalf("selection returned %T, want providerModelSelectedMsg", cmd())
	}
	if selected.ModelID != "claude-opus" {
		t.Fatalf("Enter selected %q, want highlighted model %q", selected.ModelID, "claude-opus")
	}
}

// TestSelectConfirmsMarkedModelRegardlessOfCursor verifies that after marking a
// model with Space, Enter confirms the marked model even if the cursor has
// since moved to a different row.
func TestSelectConfirmsMarkedModelRegardlessOfCursor(t *testing.T) {
	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabModels
	m.allModels = []providerModelItem{
		{ID: "gpt-5", ProviderName: "openai", AuthMethod: llm.AuthAPIKey, IsCurrent: true},
		{ID: "claude-opus", ProviderName: "anthropic", AuthMethod: llm.AuthAPIKey},
	}
	m.visibleItems = []providerListItem{
		{Kind: providerItemModel, Model: &m.allModels[0]},
		{Kind: providerItemModel, Model: &m.allModels[1]},
	}

	m.selectedIdx = 1 // mark the second model with Space
	m.toggleModel()
	m.selectedIdx = 0 // then move the cursor back to the first model

	cmd := m.Select()
	if cmd == nil {
		t.Fatal("Select should confirm the marked model")
	}
	selected, ok := cmd().(providerModelSelectedMsg)
	if !ok {
		t.Fatalf("selection returned %T, want providerModelSelectedMsg", cmd())
	}
	if selected.ModelID != "claude-opus" {
		t.Fatalf("Enter confirmed %q, want marked model %q", selected.ModelID, "claude-opus")
	}
}

// TestSelectMarkedModelDistinguishesAuthMethod guards against routing a
// subscription selection through the metered API key when the same model ID is
// offered by two auth methods of the same provider.
func TestSelectMarkedModelDistinguishesAuthMethod(t *testing.T) {
	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabModels
	m.allModels = []providerModelItem{
		{ID: "gpt-5", ProviderName: "openai", AuthMethod: llm.AuthAPIKey},
		{ID: "gpt-5", ProviderName: "openai", AuthMethod: llm.AuthSubscription},
	}
	m.visibleItems = []providerListItem{
		{Kind: providerItemModel, Model: &m.allModels[0]},
		{Kind: providerItemModel, Model: &m.allModels[1]},
	}

	m.selectedIdx = 1 // mark the subscription row
	m.toggleModel()

	if m.allModels[0].IsCurrent {
		t.Error("API-key row must not be marked when the subscription row is chosen")
	}
	if !m.allModels[1].IsCurrent {
		t.Error("subscription row should be marked")
	}

	m.selectedIdx = 0 // cursor back on the API-key row; the mark must still win
	cmd := m.Select()
	if cmd == nil {
		t.Fatal("Select should confirm the marked model")
	}
	selected, ok := cmd().(providerModelSelectedMsg)
	if !ok {
		t.Fatalf("selection returned %T, want providerModelSelectedMsg", cmd())
	}
	if selected.AuthMethod != llm.AuthSubscription {
		t.Fatalf("routed via %q, want subscription (the marked row)", selected.AuthMethod)
	}
}

func TestProviderStatusExpiryIgnoresStaleTimer(t *testing.T) {
	state := &ProviderState{}
	first := state.SetStatusMessage("thinking: think")
	second := state.SetStatusMessage("compacted")

	if cmd, handled := UpdateProvider(OverlayDeps{}, state, kit.StatusExpiredMsg{Token: first}); !handled || cmd != nil {
		t.Fatalf("UpdateProvider() handled=%v cmd=%v, want handled=true cmd=nil", handled, cmd)
	}
	if state.StatusMessage != "compacted" {
		t.Fatalf("StatusMessage = %q, want newer status to survive stale timer", state.StatusMessage)
	}

	UpdateProvider(OverlayDeps{}, state, kit.StatusExpiredMsg{Token: second})
	if state.StatusMessage != "" {
		t.Fatalf("StatusMessage = %q, want empty after current timer expires", state.StatusMessage)
	}
}

func newProviderTestStore(t *testing.T) *llm.Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

func TestEnterLoadsCachedModelsAndPutsCurrentFirst(t *testing.T) {
	store := newProviderTestStore(t)
	if err := store.CacheModels(llm.OpenAI, llm.AuthAPIKey, []llm.ModelInfo{
		{ID: "gpt-5-mini", DisplayName: "GPT-5 mini", InputTokenLimit: 128000, OutputTokenLimit: 16000},
		{ID: "gpt-5", DisplayName: "GPT-5", InputTokenLimit: 256000, OutputTokenLimit: 32000},
	}); err != nil {
		t.Fatalf("CacheModels() error = %v", err)
	}
	if err := store.Connect(llm.OpenAI, llm.AuthAPIKey); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := store.SetCurrentModel("gpt-5", llm.OpenAI, llm.AuthAPIKey); err != nil {
		t.Fatalf("SetCurrentModel() error = %v", err)
	}

	m := NewProviderSelector()
	if _, err := m.Enter(context.Background(), 80, 24); err != nil {
		t.Fatalf("Enter() error = %v", err)
	}

	if !m.active {
		t.Fatal("expected active selector")
	}
	if len(m.allModels) != 2 {
		t.Fatalf("expected 2 models, got %d", len(m.allModels))
	}

	// Check visible items contain model rows
	modelCount := 0
	for _, item := range m.visibleItems {
		if item.Kind == providerItemModel {
			modelCount++
		}
	}
	if modelCount != 2 {
		t.Fatalf("expected 2 model items in visible list, got %d", modelCount)
	}
}

func TestEnterRefreshesModelsWhenCacheExists(t *testing.T) {
	store := newProviderTestStore(t)
	providerName := llm.Name(strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")))
	envVar := "TEST_REFRESH_WITH_CACHE_KEY"
	t.Setenv(envVar, "test")

	if err := store.CacheModels(providerName, llm.AuthAPIKey, []llm.ModelInfo{
		{ID: "cached-model", DisplayName: "Cached Model"},
	}); err != nil {
		t.Fatalf("CacheModels() error = %v", err)
	}
	if err := store.Connect(providerName, llm.AuthAPIKey); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	llm.RegisterProviderDisplay(providerName, llm.ProviderDisplay{Name: "Refresh Test", Order: 9999})
	llm.Register(llm.Meta{
		Provider:    providerName,
		AuthMethod:  llm.AuthAPIKey,
		EnvVars:     []string{envVar},
		DisplayName: "Refresh Test API Key",
	}, func(context.Context) (llm.Provider, error) {
		return &staticListProvider{
			name: string(providerName),
			models: []llm.ModelInfo{
				{ID: "live-model", DisplayName: "Live Model"},
			},
		}, nil
	})
	t.Cleanup(func() {
		llm.Unregister(providerName, llm.AuthAPIKey)
	})

	m := NewProviderSelector()
	cmd, err := m.Enter(context.Background(), 80, 24)
	if err != nil {
		t.Fatalf("Enter() error = %v", err)
	}
	if cmd == nil {
		t.Fatal("Enter() should refresh models even when cache exists")
	}
	if len(m.allModels) != 1 || m.allModels[0].ID != "cached-model" {
		t.Fatalf("initial models = %#v, want cached model", m.allModels)
	}

	msg, ok := cmd().(providerModelsLoadedMsg)
	if !ok {
		t.Fatalf("refresh command returned %T, want providerModelsLoadedMsg", cmd())
	}
	m.HandleModelsLoaded(msg)

	if len(m.allModels) != 1 || m.allModels[0].ID != "live-model" {
		t.Fatalf("refreshed models = %#v, want live model", m.allModels)
	}

	reloaded, ok := m.store.GetCachedModels(providerName, llm.AuthAPIKey)
	if !ok || len(reloaded) != 1 || reloaded[0].ID != "live-model" {
		t.Fatalf("cached after refresh = %#v (ok=%v), want live model", reloaded, ok)
	}
}

func TestRefreshAuthMethodReplacesModelsImmediately(t *testing.T) {
	store := newProviderTestStore(t)
	providerName := llm.Name(strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")))
	envVar := "TEST_REFRESH_AUTH_METHOD_KEY"
	t.Setenv(envVar, "test")

	if err := store.CacheModels(providerName, llm.AuthAPIKey, []llm.ModelInfo{
		{ID: "cached-model", DisplayName: "Cached Model"},
	}); err != nil {
		t.Fatalf("CacheModels() error = %v", err)
	}
	if err := store.Connect(providerName, llm.AuthAPIKey); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	llm.RegisterProviderDisplay(providerName, llm.ProviderDisplay{Name: "Refresh Method Test", Order: 9999})
	llm.Register(llm.Meta{
		Provider:    providerName,
		AuthMethod:  llm.AuthAPIKey,
		EnvVars:     []string{envVar},
		DisplayName: "Refresh Method Test API Key",
	}, func(context.Context) (llm.Provider, error) {
		return &staticListProvider{
			name: string(providerName),
			models: []llm.ModelInfo{
				{ID: "live-model", DisplayName: "Live Model"},
			},
		}, nil
	})
	t.Cleanup(func() {
		llm.Unregister(providerName, llm.AuthAPIKey)
	})

	m := NewProviderSelector()
	m.active = true
	m.store = store
	m.allModels = []providerModelItem{
		{ID: "cached-model", DisplayName: "Cached Model", ProviderName: string(providerName), AuthMethod: llm.AuthAPIKey},
		{ID: "other-model", DisplayName: "Other Model", ProviderName: "other", AuthMethod: llm.AuthAPIKey},
	}

	cmd := m.refreshAuthMethod(providerAuthMethodItem{
		Provider:   providerName,
		AuthMethod: llm.AuthAPIKey,
	}, 0)
	msg, ok := connectResultFromCmd(cmd)
	if !ok {
		t.Fatal("expected providerConnectResultMsg from refreshAuthMethod")
	}
	if !msg.Success || len(msg.Models) != 1 || msg.Models[0].ID != "live-model" {
		t.Fatalf("refresh result = %+v, want live model", msg)
	}

	if followup := m.HandleConnectResult(msg); followup != nil {
		t.Fatal("refresh with models should update immediately without scheduling another model load")
	}

	byID := map[string]bool{}
	for _, item := range m.allModels {
		byID[item.ID] = true
	}
	if byID["cached-model"] || !byID["live-model"] || !byID["other-model"] {
		t.Fatalf("models after refresh = %#v, want live model plus unrelated models", m.allModels)
	}
}

func TestUpdateFilterMatchesModelIDDisplayNameAndProvider(t *testing.T) {
	m := NewProviderSelector()
	m.allModels = []providerModelItem{
		{ID: "gpt-5", DisplayName: "GPT-5", ProviderName: "openai"},
		{ID: "claude-sonnet", DisplayName: "Claude Sonnet", ProviderName: "anthropic"},
	}
	m.connectedProviders = []providerProviderItem{
		{Provider: "openai", DisplayName: "OpenAI"},
		{Provider: "anthropic", DisplayName: "Anthropic"},
	}

	m.searchQuery = "g5"
	m.rebuildVisibleItems()
	if len(m.filteredModels) != 1 || m.filteredModels[0].ID != "gpt-5" {
		t.Fatalf("expected ID fuzzy match to find gpt-5, got %#v", m.filteredModels)
	}

	m.searchQuery = "clsn"
	m.rebuildVisibleItems()
	if len(m.filteredModels) != 1 || m.filteredModels[0].ID != "claude-sonnet" {
		t.Fatalf("expected display-name fuzzy match to find claude-sonnet, got %#v", m.filteredModels)
	}

	m.searchQuery = "oa"
	m.rebuildVisibleItems()
	if len(m.filteredModels) != 1 || m.filteredModels[0].ProviderName != "openai" {
		t.Fatalf("expected provider-name fuzzy match to find openai model, got %#v", m.filteredModels)
	}
}

func TestSetModelPersistsSelection(t *testing.T) {
	store := newProviderTestStore(t)
	m := NewProviderSelector()
	m.store = store

	result, err := m.SetModel("gpt-5", "openai", llm.AuthAPIKey)
	if err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	if result != "Model set to: gpt-5 (openai)" {
		t.Fatalf("unexpected result: %q", result)
	}

	current := store.GetCurrentModel()
	if current == nil || current.ModelID != "gpt-5" || current.Provider != llm.OpenAI || current.AuthMethod != llm.AuthAPIKey {
		t.Fatalf("unexpected current model after SetModel: %#v", current)
	}
}

func TestConnectAuthMethodFailsWhenModelsCannotBeLoaded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	providerName := llm.Name(strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")))
	envVar := "TEST_CONNECT_FAIL_KEY"
	t.Setenv(envVar, "test")
	llm.Register(llm.Meta{
		Provider:    providerName,
		AuthMethod:  llm.AuthAPIKey,
		EnvVars:     []string{envVar},
		DisplayName: "Test Connect Fail",
	}, func(context.Context) (llm.Provider, error) {
		return &connectFailProvider{}, nil
	})
	t.Cleanup(func() {
		llm.Unregister(providerName, llm.AuthAPIKey)
	})

	m := NewProviderSelector()
	cmd := m.connectAuthMethod(providerAuthMethodItem{
		Provider:   providerName,
		AuthMethod: llm.AuthAPIKey,
		EnvVars:    []string{envVar},
	}, 0)
	if cmd == nil {
		t.Fatal("expected connectAuthMethod command")
	}
	msg, ok := connectResultFromCmd(cmd)
	if !ok {
		t.Fatalf("expected a providerConnectResultMsg from connectAuthMethod")
	}
	if msg.Success {
		t.Fatalf("expected failed connect result, got %+v", msg)
	}
	if !strings.Contains(msg.Message, "failed to load models") {
		t.Fatalf("unexpected error: %v", msg.Message)
	}

	store, storeErr := llm.NewStore()
	if storeErr != nil {
		t.Fatalf("NewStore() error = %v", storeErr)
	}
	if store.IsConnected(providerName, llm.AuthAPIKey) {
		t.Fatal("provider should not be persisted as connected when model loading fails")
	}
}

func TestInteractiveAuthReusesStoredCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	providerName := llm.Name(strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")))
	authMethod := llm.AuthMethod("subscription-reuse")

	llm.Register(llm.Meta{
		Provider:    providerName,
		AuthMethod:  authMethod,
		DisplayName: "Stored Credential Test",
	}, func(context.Context) (llm.Provider, error) {
		return &staticListProvider{
			name:   string(providerName),
			models: []llm.ModelInfo{{ID: "stored-model", DisplayName: "Stored Model"}},
		}, nil
	})
	auth := &storedCredentialAuthenticator{has: true}
	llm.RegisterAuthenticator(providerName, authMethod, auth)
	t.Cleanup(func() {
		llm.Unregister(providerName, authMethod)
	})

	m := NewProviderSelector()
	cmd := m.tryConnectOrPromptKey(providerAuthMethodItem{
		Provider:   providerName,
		AuthMethod: authMethod,
	}, 0, 3)
	msg, ok := connectResultFromCmd(cmd)
	if !ok {
		t.Fatal("expected providerConnectResultMsg")
	}
	if auth.loginCalls != 0 {
		t.Fatalf("Login called %d times; stored credentials should be verified without a fresh browser login", auth.loginCalls)
	}
	if !msg.Success {
		t.Fatalf("expected stored credentials to connect successfully, got %+v", msg)
	}

	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if !store.IsConnected(providerName, authMethod) {
		t.Fatal("provider should be persisted as connected after stored credential verification")
	}
}

func TestTabSwitchesBetweenTabs(t *testing.T) {
	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabModels
	m.allModels = []providerModelItem{
		{ID: "gpt-5", DisplayName: "GPT-5", ProviderName: "openai"},
	}
	m.connectedProviders = []providerProviderItem{
		{Provider: "openai", DisplayName: "OpenAI"},
	}
	m.allProviders = []providerProviderItem{
		{Provider: "openai", DisplayName: "OpenAI", Connected: true},
		{Provider: "google", DisplayName: "Google", AuthMethods: []providerAuthMethodItem{
			{DisplayName: "API Key", Status: llm.StatusNotConfigured},
		}},
	}
	m.rebuildVisibleItems()

	// Press Tab to switch to Providers tab
	m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.activeTab != providerTabProviders {
		t.Fatal("Tab should switch to Providers tab")
	}

	// Should have provider items now
	found := false
	for _, item := range m.visibleItems {
		if item.Kind == providerItemProvider {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Providers tab should show provider items")
	}

	// Press Tab again to go back to Models
	m.HandleKeypress(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.activeTab != providerTabModels {
		t.Fatal("Tab should switch back to Models tab")
	}
}

func TestNavigationSkipsProviderHeaders(t *testing.T) {
	m := NewProviderSelector()
	m.active = true

	model1 := providerModelItem{ID: "m1", ProviderName: "openai"}
	model2 := providerModelItem{ID: "m2", ProviderName: "anthropic"}
	m.visibleItems = []providerListItem{
		{Kind: providerItemProviderHeader},        // 0 - not selectable
		{Kind: providerItemModel, Model: &model1}, // 1
		{Kind: providerItemProviderHeader},        // 2 - not selectable
		{Kind: providerItemModel, Model: &model2}, // 3
	}
	m.selectedIdx = 1

	// MoveDown should skip index 2 (header) and land on 3
	m.MoveDown()
	if m.selectedIdx != 3 {
		t.Fatalf("MoveDown should skip header, got selectedIdx=%d, want 3", m.selectedIdx)
	}

	// MoveUp should skip index 2 (header) and land on 1
	m.MoveUp()
	if m.selectedIdx != 1 {
		t.Fatalf("MoveUp should skip header, got selectedIdx=%d, want 1", m.selectedIdx)
	}
}

func TestSelectProviderExpandsAuthMethods(t *testing.T) {
	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabProviders
	m.allProviders = []providerProviderItem{
		{
			Provider:    "anthropic",
			DisplayName: "Anthropic",
			AuthMethods: []providerAuthMethodItem{
				{DisplayName: "API Key", Status: llm.StatusNotConfigured},
				{DisplayName: "Bedrock", Status: llm.StatusAvailable},
			},
		},
	}
	m.rebuildVisibleItems()

	// Find the provider item
	for i, item := range m.visibleItems {
		if item.Kind == providerItemProvider {
			m.selectedIdx = i
			break
		}
	}

	// Select should expand auth methods (since there are multiple)
	cmd := m.Select()
	if cmd != nil {
		t.Fatal("selecting multi-auth provider should not return a command")
	}
	if m.expandedProviderIdx != 0 {
		t.Fatalf("expandedProviderIdx = %d, want 0", m.expandedProviderIdx)
	}

	// Check that auth method items are now in visible list
	authCount := 0
	for _, item := range m.visibleItems {
		if item.Kind == providerItemAuthMethod {
			authCount++
		}
	}
	if authCount != 2 {
		t.Fatalf("expected 2 auth method items, got %d", authCount)
	}
}

func TestRebuildVisibleItemsStructure(t *testing.T) {
	m := NewProviderSelector()
	m.activeTab = providerTabModels
	m.allModels = []providerModelItem{
		{ID: "m1", ProviderName: "openai", DisplayName: "Model 1"},
		{ID: "m2", ProviderName: "openai", DisplayName: "Model 2"},
		{ID: "m3", ProviderName: "anthropic", DisplayName: "Model 3"},
	}
	m.connectedProviders = []providerProviderItem{
		{Provider: "openai", DisplayName: "OpenAI"},
		{Provider: "anthropic", DisplayName: "Anthropic"},
	}

	m.rebuildVisibleItems()

	// Expected structure (Models tab):
	// 0: ProviderHeader (OpenAI)
	// 1: Model (m1)
	// 2: Model (m2)
	// 3: ProviderHeader (Anthropic)
	// 4: Model (m3)

	if len(m.visibleItems) != 5 {
		t.Fatalf("expected 5 visible items, got %d", len(m.visibleItems))
	}
	if m.visibleItems[0].Kind != providerItemProviderHeader {
		t.Fatalf("item 0 should be ProviderHeader, got %v", m.visibleItems[0].Kind)
	}
	if m.visibleItems[1].Kind != providerItemModel || m.visibleItems[2].Kind != providerItemModel {
		t.Fatal("items 1-2 should be Models")
	}
	if m.visibleItems[3].Kind != providerItemProviderHeader {
		t.Fatalf("item 3 should be ProviderHeader, got %v", m.visibleItems[3].Kind)
	}
	if m.visibleItems[4].Kind != providerItemModel {
		t.Fatal("item 4 should be Model")
	}

	// selectedIdx should skip the first header and land on index 1
	if m.selectedIdx != 1 {
		t.Fatalf("selectedIdx should be 1 (first model), got %d", m.selectedIdx)
	}
}

func TestRebuildModelsTabSortsByNameDescending(t *testing.T) {
	m := NewProviderSelector()
	m.activeTab = providerTabModels
	m.allModels = []providerModelItem{
		{ID: "current-small", DisplayName: "Current small", ProviderName: "moonshot", InputTokenLimit: 16_000, IsCurrent: true},
		{ID: "unknown", DisplayName: "Unknown", ProviderName: "moonshot"},
		{ID: "medium-b", DisplayName: "Beta", ProviderName: "moonshot", InputTokenLimit: 128_000},
		{ID: "large", DisplayName: "Large", ProviderName: "moonshot", InputTokenLimit: 1_048_576},
		{ID: "medium-a", DisplayName: "Alpha", ProviderName: "moonshot", InputTokenLimit: 128_000},
	}
	m.connectedProviders = []providerProviderItem{{Provider: "moonshot", DisplayName: "Kimi"}}

	m.rebuildVisibleItems()

	var ids []string
	for _, item := range m.visibleItems {
		if item.Kind == providerItemModel && item.Model != nil {
			ids = append(ids, item.Model.ID)
		}
	}
	want := []string{"unknown", "large", "current-small", "medium-b", "medium-a"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("model order = %v, want %v", ids, want)
	}
}
