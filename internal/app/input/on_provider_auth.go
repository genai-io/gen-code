// Provider selector: API-key entry, credential editing/removal, and the
// connect/refresh flow with its in-flight spinner.
package input

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"go.uber.org/zap"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/secret"
)

// HandlePaste inserts bracketed-paste content into the API key input when it's active.
func (s *ProviderSelector) HandlePaste(content string) tea.Cmd {
	if s.customFormActive {
		content = strings.NewReplacer("\r", "", "\n", "").Replace(content)
		content = strings.TrimSpace(content)
		if content == "" {
			return nil
		}
		s.customFormInputs[s.customFormFocus].SetValue(content)
		s.customFormInputs[s.customFormFocus].CursorEnd()
		return nil
	}
	if !s.apiKeyActive {
		return nil
	}
	content = strings.NewReplacer("\r", "", "\n", "").Replace(content)
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	s.apiKeyInput.SetValue(content)
	s.apiKeyInput.CursorEnd()
	return nil
}

func (s *ProviderSelector) handleAPIKeyInput(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "enter":
		value := strings.TrimSpace(s.apiKeyInput.Value())
		if value == "" {
			return nil
		}
		if store := secret.Default(); store != nil {
			_ = store.Set(s.apiKeyEnvVar, value)
		}
		os.Setenv(s.apiKeyEnvVar, value)
		s.apiKeyActive = false

		// Find the auth method and trigger connection
		if s.apiKeyProviderIdx >= 0 && s.apiKeyProviderIdx < len(s.allProviders) {
			dp := &s.allProviders[s.apiKeyProviderIdx]
			if s.apiKeyAuthIdx >= 0 && s.apiKeyAuthIdx < len(dp.AuthMethods) {
				am := dp.AuthMethods[s.apiKeyAuthIdx]
				return s.connectAuthMethod(am, s.selectedIdx)
			}
		}
		return nil

	case "esc":
		s.apiKeyActive = false
		return nil

	default:
		var cmd tea.Cmd
		s.apiKeyInput, cmd = s.apiKeyInput.Update(key)
		return cmd
	}
}

// handleCredentialEdit handles the 'e' key for editing credentials on connected providers.
// For providers with a single auth method: activates API key input directly.
// For providers with multiple auth methods: expands the provider first, then allows editing.
func (s *ProviderSelector) handleCredentialEdit() tea.Cmd {
	if s.selectedIdx < 0 || s.selectedIdx >= len(s.visibleItems) {
		return nil
	}

	item := s.visibleItems[s.selectedIdx]

	switch item.Kind {
	case providerItemProvider:
		return s.handleCredentialEditForProvider(item)
	case providerItemAuthMethod:
		return s.handleCredentialEditForAuthMethod(item)
	default:
		return nil
	}
}

// handleCredentialEditForProvider handles credential edit for a provider row.
func (s *ProviderSelector) handleCredentialEditForProvider(item providerListItem) tea.Cmd {
	if item.Provider == nil {
		return nil
	}
	p := item.Provider

	// The custom provider edits all three fields (ID / baseURL / apiKey) in its form.
	if s.isCustomProvider(p.Provider) {
		s.openCustomForm()
		return nil
	}

	// Ollama edits its base URL via a dedicated form (no API key).
	if s.isOllamaProvider(p.Provider) {
		s.openOllamaForm()
		return nil
	}

	// Single auth method: activate API key input directly
	if len(p.AuthMethods) == 1 {
		am := p.AuthMethods[0]
		envVar := providerFirstEnvVar(am.EnvVars)
		if envVar == "" {
			return nil
		}
		s.apiKeyProviderIdx = item.ProviderIdx
		s.apiKeyAuthIdx = 0
		s.initAPIKeyInput(envVar)
		return nil
	}

	// Multiple auth methods: expand if not already expanded
	if len(p.AuthMethods) == 0 {
		return nil
	}

	if s.expandedProviderIdx != item.ProviderIdx {
		s.expandedProviderIdx = item.ProviderIdx
		s.resetConnectionResult()
		s.rebuildVisibleItems()
	}

	return nil
}

// handleCredentialEditForAuthMethod handles credential edit for an auth method row.
func (s *ProviderSelector) handleCredentialEditForAuthMethod(item providerListItem) tea.Cmd {
	if item.AuthMethod == nil {
		return nil
	}
	am := item.AuthMethod

	envVar := providerFirstEnvVar(am.EnvVars)
	if envVar == "" {
		return nil
	}

	s.apiKeyProviderIdx = item.ProviderIdx
	s.apiKeyAuthIdx = s.findAuthMethodIndex(item)
	s.initAPIKeyInput(envVar)
	return nil
}

// resolveRemovableAuthMethod resolves the auth method targeted by a Ctrl+D
// removal from a visible list item: a provider row with a single auth method,
// or an auth-method row directly. Returns nil for anything else.
func resolveRemovableAuthMethod(item providerListItem) *providerAuthMethodItem {
	switch item.Kind {
	case providerItemProvider:
		if item.Provider == nil || len(item.Provider.AuthMethods) != 1 {
			return nil
		}
		return &item.Provider.AuthMethods[0]
	case providerItemAuthMethod:
		return item.AuthMethod
	default:
		return nil
	}
}

// handleCredentialRemove handles Ctrl+D: shows a confirmation prompt before removing.
func (s *ProviderSelector) handleCredentialRemove() tea.Cmd {
	if s.activeTab != providerTabProviders {
		return nil
	}
	if s.selectedIdx < 0 || s.selectedIdx >= len(s.visibleItems) {
		return nil
	}

	am := resolveRemovableAuthMethod(s.visibleItems[s.selectedIdx])
	if am == nil {
		return nil
	}

	// Interactive-login auth has no env var, but its stored OAuth tokens are
	// still removable, so allow the confirm prompt for either credential kind.
	envVar := providerFirstEnvVar(am.EnvVars)
	if envVar == "" && !llm.SupportsInteractiveLogin(am.Provider, am.AuthMethod) {
		return nil
	}

	s.confirmRemoveActive = true
	s.confirmRemoveEnvVar = envVar
	s.confirmRemoveItemIdx = s.selectedIdx
	return nil
}

// handleConfirmRemove handles keypresses while the confirm-remove prompt is active.
func (s *ProviderSelector) handleConfirmRemove(key tea.KeyMsg) tea.Cmd {
	s.confirmRemoveActive = false
	switch key.String() {
	case "y", "Y":
		return s.executeCredentialRemove()
	default:
		return nil
	}
}

// executeCredentialRemove performs the actual credential removal.
func (s *ProviderSelector) executeCredentialRemove() tea.Cmd {
	envVar := s.confirmRemoveEnvVar

	// Resolve the provider and auth method from the item
	am := resolveRemovableAuthMethod(s.visibleItems[s.confirmRemoveItemIdx])
	if am == nil {
		return nil
	}
	providerName := am.Provider
	authMethod := am.AuthMethod

	// Clear the credential: OAuth tokens for interactive-login auth, otherwise
	// the API-key env var in the secret store.
	if llm.SupportsInteractiveLogin(providerName, authMethod) {
		_ = llm.Logout(providerName, authMethod)
	} else {
		if store := secret.Default(); store != nil {
			_ = store.Delete(envVar)
		}
		os.Unsetenv(envVar)
	}

	// Disconnect provider and remove cached models from the llm store
	if s.store != nil {
		_ = s.store.Disconnect(providerName)
		_ = s.store.RemoveCachedModels(providerName, authMethod)

		// Clear current model if it belongs to the disconnected provider
		if cur := s.store.GetCurrentModel(); cur != nil && cur.Provider == providerName {
			_ = s.store.ClearCurrentModel()
			llm.Default().SetCurrentModel(nil)
		}

		// If no connections remain, clear the runtime provider too
		if len(s.store.GetConnections()) == 0 {
			llm.Default().SetProvider(nil)
		}
	}

	// Reload provider data to reflect the removed credential
	s.resetConnectionResult()
	_, _ = s.loadProviderData()
	s.rebuildVisibleItems()
	return nil
}

// tryConnectOrPromptKey connects if env vars are available, otherwise shows API
// key input.
//
// formAuthIdx addresses the auth method within its provider, which is what the
// API-key form needs to route the entered key. The connect helpers want
// something different — the visible row their spinner and result render on —
// so they take s.selectedIdx, the row the user pressed Enter on. The two agree
// only for the first row, which is why mixing them up parks the spinner on
// whatever provider happens to be listed first.
func (s *ProviderSelector) tryConnectOrPromptKey(am providerAuthMethodItem, providerIdx, formAuthIdx int) tea.Cmd {
	// Interactive (OAuth) auth signs in via the browser, not an API key. If a
	// prior token is present, validate it with the normal connect path instead
	// of forcing a fresh browser login.
	if llm.SupportsInteractiveLogin(am.Provider, am.AuthMethod) {
		if llm.HasInteractiveCredentials(am.Provider, am.AuthMethod) {
			return s.connectAuthMethod(am, s.selectedIdx)
		}
		return s.connectInteractive(am, s.selectedIdx)
	}

	if am.Status == llm.StatusAvailable || providerIsEnvReady(am.EnvVars) {
		return s.connectAuthMethod(am, s.selectedIdx)
	}

	// Show inline API key input
	envVar := providerFirstEnvVar(am.EnvVars)
	if envVar == "" {
		return nil
	}
	s.apiKeyProviderIdx = providerIdx
	s.apiKeyAuthIdx = formAuthIdx
	s.initAPIKeyInput(envVar)
	return nil
}

// initAPIKeyInput initializes the textinput for API key entry.
func (s *ProviderSelector) initAPIKeyInput(envVar string) {
	ti := textinput.New()
	ti.Placeholder = envVar
	ti.Focus()
	ti.CharLimit = 256
	ti.SetWidth(40)
	ti.EchoMode = textinput.EchoPassword
	s.apiKeyInput = ti
	s.apiKeyActive = true
	s.apiKeyEnvVar = envVar
}

func providerIsEnvReady(envVars []string) bool {
	for _, v := range envVars {
		if v != "" && secret.Resolve(v) != "" {
			return true
		}
	}
	return false
}

func providerFirstEnvVar(envVars []string) string {
	for _, v := range envVars {
		if v != "" {
			return v
		}
	}
	return ""
}

// providerSpinnerInterval is the spin cadence while a connect/refresh runs —
// fast enough to read as a smooth spinner (independent of the slower global
// thinking-spinner tick).
const providerSpinnerInterval = 90 * time.Millisecond

// providerConnectingTickMsg is the periodic "still connecting/refreshing" tick that
// advances the in-flight spinner; the terminal counterpart to
// providerConnectResultMsg, which signals the work is done.
type providerConnectingTickMsg struct{}

// providerConnectingTickCmd schedules the next connecting tick (spinner frame).
func providerConnectingTickCmd() tea.Cmd {
	return tea.Tick(providerSpinnerInterval, func(time.Time) tea.Msg {
		return providerConnectingTickMsg{}
	})
}

// AdvanceSpinner moves the in-flight spinner to its next frame.
func (s *ProviderSelector) AdvanceSpinner() { s.spinnerTick++ }

// Transient in-flight result markers. While lastConnectResult equals one of
// these, the row shows an animated spinner instead of static text.
const (
	providerStatusRefreshing = "Refreshing..."
	providerStatusConnecting = "Connecting..."
)

// connectVerifyTimeout caps the model listing that records a connection after an
// interactive sign-in. It runs detached from the sign-in's cancellable context —
// the credentials are already stored by then — so it needs its own ceiling.
const connectVerifyTimeout = 30 * time.Second

// IsConnecting reports whether a connect/refresh is in flight, so the spinner-tick
// loop keeps ticking and the row renders an animated frame.
func (s *ProviderSelector) IsConnecting() bool {
	return s.active &&
		(s.lastConnectResult == providerStatusRefreshing || s.lastConnectResult == providerStatusConnecting)
}

// refreshAuthMethod re-fetches models for an already connected provider auth method.
func (s *ProviderSelector) refreshAuthMethod(item providerAuthMethodItem, authIdx int) tea.Cmd {
	if !s.beginConnect(providerStatusRefreshing, authIdx) {
		return nil
	}

	work := func() tea.Msg {
		ctx := context.Background()

		result := providerConnectResultMsg{
			AuthIdx:    authIdx,
			Provider:   item.Provider,
			AuthMethod: item.AuthMethod,
		}
		fail := func(err error) tea.Msg {
			result.Success = false
			result.Message = fmt.Sprintf("failed to load models for %s: %s", item.Provider, err.Error())
			return result
		}

		llmProvider, err := llm.GetProvider(ctx, item.Provider, item.AuthMethod)
		if err != nil {
			return fail(err)
		}

		models, err := llmProvider.ListModels(ctx)

		store, _ := llm.NewStore()
		if store != nil && len(models) > 0 {
			_ = store.CacheModels(item.Provider, item.AuthMethod, models)
		}

		if err != nil && len(models) == 0 {
			return fail(err)
		}

		result.Success = true
		result.NewStatus = llm.StatusConnected
		result.Models = models
		if err != nil {
			result.Message = fmt.Sprintf("⚠ %d models loaded with refresh warning", len(models))
		} else {
			result.Message = fmt.Sprintf("● %d models", len(models))
		}
		return result
	}
	// Start the spinner alongside the async work.
	return tea.Batch(providerConnectingTickCmd(), work)
}

// beginConnect marks a connect/refresh as in flight for authIdx under the given
// status marker, returning false if one is already running — re-entry is ignored
// so we never start a second spinner-tick loop or a concurrent store write.
func (s *ProviderSelector) beginConnect(status string, authIdx int) bool {
	if s.IsConnecting() {
		return false
	}
	s.lastConnectResult = status
	s.lastConnectAuthIdx = authIdx
	s.lastConnectSuccess = false
	// Any sign-in instruction belongs to the connect this call starts; every
	// connect/refresh passes through here, so clearing it once here is enough.
	s.loginPrompt = llm.LoginPrompt{}
	return true
}

// connectResultMsg runs the actual provider connection and builds the result
// message shared by connectAuthMethod and connectInteractive.
func (s *ProviderSelector) connectResultMsg(ctx context.Context, item providerAuthMethodItem, authIdx int) tea.Msg {
	result, err := s.ConnectProvider(ctx, item.Provider, item.AuthMethod)
	if err != nil {
		return providerConnectResultMsg{
			AuthIdx: authIdx,
			Success: false,
			Message: err.Error(),
		}
	}
	return providerConnectResultMsg{
		AuthIdx:   authIdx,
		Success:   true,
		Message:   result,
		NewStatus: llm.StatusConnected,
	}
}

// connectInteractive runs an OAuth sign-in for an auth method that
// authenticates in the browser, then records the connection. It reuses the
// connect spinner and result plumbing so the row animates while the browser
// flow is in progress.
//
// The sign-in also reports what the user has to do — a URL, plus a code to type
// there for device flows. That arrives partway through the blocking Login call,
// so it rides its own channel and its own command, letting the footer show the
// instruction while the same flow is still waiting on the browser.
func (s *ProviderSelector) connectInteractive(item providerAuthMethodItem, authIdx int) tea.Cmd {
	if !s.beginConnect(providerStatusConnecting, authIdx) {
		return nil
	}
	prompts := make(chan llm.LoginPrompt, 1)
	// The cancellable context covers the sign-in only. Once it returns the
	// credentials are already stored, so closing the selector during the model
	// listing that follows must not abandon a connection the user completed.
	signIn, cancel := context.WithCancel(context.Background())
	s.cancelLogin = cancel

	work := func() tea.Msg {
		defer cancel()

		onPrompt := func(p llm.LoginPrompt) {
			// Log it too, so it's recoverable from the log when the browser
			// can't be opened automatically (e.g. over SSH).
			log.Logger().Info("provider sign-in",
				zap.String("provider", string(item.Provider)),
				zap.String("url", p.URL),
				zap.String("user_code", p.UserCode))
			select {
			case prompts <- p:
			default: // a prompt is already queued; the first one is the live instruction.
			}
		}
		defer close(prompts)

		if err := llm.Login(signIn, item.Provider, item.AuthMethod, onPrompt); err != nil {
			return providerConnectResultMsg{
				AuthIdx: authIdx,
				Success: false,
				Message: fmt.Sprintf("sign-in failed: %s", err.Error()),
			}
		}
		// Detached from signIn on purpose — the credentials are stored, so this
		// must survive the user closing the selector — but still bounded, since
		// nothing in this generic path guarantees ListModels ever returns.
		listing, done := context.WithTimeout(context.Background(), connectVerifyTimeout)
		defer done()
		return s.connectResultMsg(listing, item, authIdx)
	}

	// Resolves when the flow publishes an instruction, or to nil when it
	// finishes without one (the channel closes on the way out either way).
	waitPrompt := func() tea.Msg {
		p, ok := <-prompts
		if !ok {
			return nil
		}
		return providerLoginPromptMsg{AuthIdx: authIdx, Prompt: p}
	}

	return tea.Batch(providerConnectingTickCmd(), work, waitPrompt)
}

// connectAuthMethod initiates an async connection to a provider auth method.
func (s *ProviderSelector) connectAuthMethod(item providerAuthMethodItem, authIdx int) tea.Cmd {
	if !s.beginConnect(providerStatusConnecting, authIdx) {
		return nil
	}

	work := func() tea.Msg {
		return s.connectResultMsg(context.Background(), item, authIdx)
	}
	return tea.Batch(providerConnectingTickCmd(), work)
}

// cancelInteractiveLogin stops an in-flight sign-in, if any. It is idempotent,
// so closing the selector twice — or closing it after the sign-in already
// finished — is a no-op.
func (s *ProviderSelector) cancelInteractiveLogin() {
	if s.cancelLogin == nil {
		return
	}
	s.cancelLogin()
	s.cancelLogin = nil
}

// HandleLoginPrompt shows what an in-flight interactive sign-in needs from the
// user. It is ignored once the sign-in it belongs to has resolved, so a late
// prompt can't reinstate the instruction over a finished row.
func (s *ProviderSelector) HandleLoginPrompt(msg providerLoginPromptMsg) {
	if !s.IsConnecting() || msg.AuthIdx != s.lastConnectAuthIdx {
		return
	}
	s.loginPrompt = msg.Prompt
}

// HandleConnectResult updates the selector state with connection result.
func (s *ProviderSelector) HandleConnectResult(msg providerConnectResultMsg) tea.Cmd {
	s.lastConnectAuthIdx = msg.AuthIdx
	s.lastConnectResult = msg.Message
	s.lastConnectSuccess = msg.Success

	if !msg.Success {
		return nil
	}

	if len(msg.Models) > 0 {
		s.replaceModelsForAuthMethod(msg.Provider, msg.AuthMethod, msg.Models)
		s.rebuildVisibleItems()
		return nil
	}

	// Reload provider/model data, preserving UI state (tab, expansion, result).
	cmd, _ := s.loadProviderData()
	s.rebuildVisibleItems()
	return cmd
}

// ConnectProvider connects to a provider and verifies the connection.
func (s *ProviderSelector) ConnectProvider(ctx context.Context, p llm.Name, authMethod llm.AuthMethod) (string, error) {
	if s.store == nil {
		store, err := llm.NewStore()
		if err != nil {
			return "", fmt.Errorf("failed to load store: %w", err)
		}
		s.store = store
	}

	meta, ok := llm.GetMeta(p, authMethod)
	if !ok {
		return "", fmt.Errorf("provider not found: %s:%s", p, authMethod)
	}

	if !llm.IsReady(meta) {
		missingVars := []string{}
		for _, envVar := range meta.EnvVars {
			if envVar == "" {
				continue
			}
			missingVars = append(missingVars, envVar)
		}
		return "", fmt.Errorf("missing required environment variables: %s", strings.Join(missingVars, ", "))
	}

	llmProvider, err := llm.GetProvider(ctx, p, authMethod)
	if err != nil {
		return "", fmt.Errorf("failed to create provider: %w", err)
	}

	models, listErr := llmProvider.ListModels(ctx)
	if listErr != nil && len(models) == 0 {
		return "", fmt.Errorf("failed to load models for %s: %w", meta.DisplayName, listErr)
	}
	if len(models) > 0 {
		_ = s.store.CacheModels(p, authMethod, models)
	}

	if err := s.store.Connect(p, authMethod); err != nil {
		return "", fmt.Errorf("failed to save connection: %w", err)
	}

	if listErr != nil {
		return fmt.Sprintf("Connected to %s via %s (%d models; refresh warning: %v)", meta.DisplayName, authMethod, len(models), listErr), nil
	}

	return fmt.Sprintf("Connected to %s via %s (%d models)", meta.DisplayName, authMethod, len(models)), nil
}
