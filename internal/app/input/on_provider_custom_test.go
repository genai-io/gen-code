package input

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/llm"

	// The custom endpoint has to be a registered provider for a connect to
	// reach it, and registration is what importing this does.
	_ "github.com/genai-io/san/internal/llm/sdk"
)

// customModelsServer serves an OpenAI-compatible GET /models endpoint.
func customModelsServer(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	var data strings.Builder
	for i, id := range ids {
		if i > 0 {
			data.WriteString(",")
		}
		data.WriteString(`{"id":"` + id + `","object":"model"}`)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[` + data.String() + `]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenCustomFormPrefillsSavedConfig(t *testing.T) {
	store := newProviderTestStore(t)
	if err := store.SetCustomProvider(llm.CustomProviderConfig{ID: string(llm.CustomProvider), BaseURL: "https://api.example.com/v1"}); err != nil {
		t.Fatalf("SetCustomProvider() error = %v", err)
	}

	m := NewProviderSelector()
	m.store = store
	m.openCustomForm()

	if !m.customFormActive {
		t.Fatal("openCustomForm should activate the form")
	}
	if got := m.customFormInputs[customFormFieldBaseURL].Value(); got != "https://api.example.com/v1" {
		t.Fatalf("baseURL field = %q", got)
	}
	if got := m.customFormInputs[customFormFieldAPIKey].Value(); got != "" {
		t.Fatalf("apiKey field should start empty, got %q", got)
	}
	if !m.isCustomProvider(llm.CustomProvider) {
		t.Fatal("isCustomProvider should recognize the fixed ID")
	}
}

func TestValidateCustomForm(t *testing.T) {
	isolateSecretStore(t)
	t.Setenv(llm.CustomAPIKeyEnvVar, "")

	tests := []struct {
		name    string
		baseURL string
		apiKey  string
		wantErr string
	}{
		{name: "missing baseURL", baseURL: "", apiKey: "k", wantErr: "base URL is required"},
		{name: "baseURL without scheme", baseURL: "api.x.com", apiKey: "k", wantErr: "https://"},
		{name: "missing api key", baseURL: "https://x.com", apiKey: "", wantErr: "API key is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewProviderSelector()
			m.store, _ = llm.NewStore()
			m.openCustomForm()
			m.customFormInputs[customFormFieldBaseURL].SetValue(tt.baseURL)
			m.customFormInputs[customFormFieldAPIKey].SetValue(tt.apiKey)

			_, _, err := m.validateCustomForm()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateCustomForm() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateCustomFormKeepsStoredAPIKey(t *testing.T) {
	isolateSecretStore(t)
	t.Setenv(llm.CustomAPIKeyEnvVar, "stored-key")

	m := NewProviderSelector()
	m.store, _ = llm.NewStore()
	m.openCustomForm()
	m.customFormInputs[customFormFieldBaseURL].SetValue("https://api.example.com/v1//")

	baseURL, apiKey, err := m.validateCustomForm()
	if err != nil {
		t.Fatalf("validateCustomForm() error = %v", err)
	}
	if apiKey != "" {
		t.Fatalf("empty key field should mean keep-stored, got %q", apiKey)
	}
	if baseURL != "https://api.example.com/v1" {
		t.Fatalf("all trailing slashes should be trimmed, got %q", baseURL)
	}
}

func TestSubmitCustomFormSavesAndConnects(t *testing.T) {
	isolateSecretStore(t)
	t.Setenv(llm.CustomAPIKeyEnvVar, "")
	t.Cleanup(func() { _ = os.Unsetenv(llm.CustomAPIKeyEnvVar) })

	srv := customModelsServer(t, "model-1")
	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabProviders
	m.store = store
	m.openCustomForm()
	m.customFormInputs[customFormFieldBaseURL].SetValue(srv.URL)
	m.customFormInputs[customFormFieldAPIKey].SetValue("sk-test")

	cmd := m.submitCustomForm()
	if cmd == nil {
		t.Fatal("submitCustomForm should trigger a connect")
	}
	if m.customFormActive {
		t.Fatal("form should close after a successful submit")
	}

	msg, ok := connectResultFromCmd(cmd)
	if !ok {
		t.Fatal("expected a providerConnectResultMsg")
	}
	if !msg.Success {
		t.Fatalf("connect failed: %s", msg.Message)
	}

	cfg := m.store.CustomProvider()
	if cfg == nil || cfg.BaseURL != srv.URL {
		t.Fatalf("custom provider config not saved: %+v", cfg)
	}
	if !m.store.IsConnected(llm.CustomProvider, llm.AuthAPIKey) {
		t.Fatal("provider should be connected after submit")
	}
	if models, ok := m.store.GetCachedModels(llm.CustomProvider, llm.AuthAPIKey); !ok || len(models) != 1 || models[0].ID != "model-1" {
		t.Fatalf("models should be cached after connect: %+v", models)
	}
	if got := os.Getenv(llm.CustomAPIKeyEnvVar); got != "sk-test" {
		t.Fatalf("api key env = %q, want sk-test", got)
	}
}

func TestSubmitCustomFormConnectFailureKeepsConfig(t *testing.T) {
	isolateSecretStore(t)
	t.Setenv(llm.CustomAPIKeyEnvVar, "")
	t.Cleanup(func() { _ = os.Unsetenv(llm.CustomAPIKeyEnvVar) })

	// Point at a closed port so the connect fails.
	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabProviders
	m.store = store
	m.openCustomForm()
	m.customFormInputs[customFormFieldBaseURL].SetValue("http://127.0.0.1:1")
	m.customFormInputs[customFormFieldAPIKey].SetValue("sk-test")

	cmd := m.submitCustomForm()
	if cmd == nil {
		t.Fatal("submitCustomForm should trigger a connect even when it will fail")
	}
	if m.customFormActive {
		t.Fatal("form should close after submit regardless of connect outcome")
	}

	msg, ok := connectResultFromCmd(cmd)
	if !ok {
		t.Fatal("expected a providerConnectResultMsg")
	}
	if msg.Success {
		t.Fatal("connect to a closed port should fail")
	}

	// The config is saved so the user can Ctrl+E to fix the key or URL; the
	// connection is not.
	if cfg := m.store.CustomProvider(); cfg == nil || cfg.BaseURL != "http://127.0.0.1:1" {
		t.Fatalf("config should be saved despite connect failure: %+v", cfg)
	}
	if m.store.IsConnected(llm.CustomProvider, llm.AuthAPIKey) {
		t.Fatal("failed connect must not mark the provider connected")
	}
}

func TestSelectProviderOpensCustomForm(t *testing.T) {
	store := newProviderTestStore(t)

	m := NewProviderSelector()
	m.active = true
	m.activeTab = providerTabProviders
	m.store = store
	m.allProviders = []providerProviderItem{{
		Provider:    llm.CustomProvider,
		DisplayName: "Custom",
		AuthMethods: []providerAuthMethodItem{{
			Provider:    llm.CustomProvider,
			AuthMethod:  llm.AuthAPIKey,
			DisplayName: "Direct API",
			Status:      llm.StatusNotConfigured,
			EnvVars:     []string{llm.CustomAPIKeyEnvVar},
		}},
	}}
	m.rebuildVisibleItems()
	m.selectedIdx = 0

	if cmd := m.selectProvider(m.visibleItems[0]); cmd != nil {
		t.Fatal("opening the form should not return a command")
	}
	if !m.customFormActive {
		t.Fatal("Enter on the custom provider should open the form")
	}
	if m.apiKeyActive {
		t.Fatal("custom provider should not open the single API-key input")
	}
}

func TestCustomFormKeyRouting(t *testing.T) {
	m := NewProviderSelector()
	m.store = newProviderTestStore(t)
	m.openCustomForm()

	// Tab cycles focus through the two fields.
	m.handleCustomFormKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.customFormFocus != customFormFieldAPIKey {
		t.Fatalf("focus after Tab = %d, want apiKey field", m.customFormFocus)
	}
	m.handleCustomFormKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.customFormFocus != customFormFieldBaseURL {
		t.Fatalf("focus after Shift+Tab = %d, want baseURL field", m.customFormFocus)
	}

	// Esc closes the form without submitting.
	m.handleCustomFormKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.customFormActive {
		t.Fatal("Esc should close the form")
	}
}

func TestGoBackClosesCustomForm(t *testing.T) {
	m := NewProviderSelector()
	m.store = newProviderTestStore(t)
	m.openCustomForm()

	if !m.GoBack() {
		t.Fatal("GoBack should consume the event while the form is open")
	}
	if m.customFormActive {
		t.Fatal("GoBack should close the form")
	}
}
