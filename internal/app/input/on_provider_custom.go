// Provider selector: the custom (third-party, OpenAI-compatible) provider form.
// Built-in providers only need an API key, so Enter opens the single inline
// input; the custom provider also needs a baseURL, so it gets a two-field form
// instead. The provider ID is fixed (llm.CustomProvider) — there is only one
// custom provider, so a user-chosen ID would add rename bookkeeping without
// distinguishing anything.
package input

import (
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/secret"
)

// customFormField indexes customFormInputs.
const (
	customFormFieldBaseURL = iota
	customFormFieldAPIKey
	customFormFieldCount
)

// isCustomProvider reports whether name is the custom provider's fixed name.
func (s *ProviderSelector) isCustomProvider(name llm.Name) bool {
	return name == llm.CustomProvider
}

// openCustomForm initializes the inline form, prefilled from the saved config.
// The API key field starts empty like the built-in edit flow; on submit an
// empty key keeps the stored one.
func (s *ProviderSelector) openCustomForm() {
	baseURL := ""
	if s.store != nil {
		if cfg := s.store.CustomProvider(); cfg != nil {
			baseURL = cfg.BaseURL
		}
	}

	urlInput := textinput.New()
	urlInput.Placeholder = "https://api.example.com/v1"
	urlInput.SetValue(baseURL)
	urlInput.CharLimit = 256
	urlInput.SetWidth(40)
	urlInput.Focus()

	keyInput := textinput.New()
	keyInput.Placeholder = llm.CustomAPIKeyEnvVar
	keyInput.CharLimit = 256
	keyInput.SetWidth(40)
	keyInput.EchoMode = textinput.EchoPassword

	s.apiKeyActive = false
	s.customFormInputs = [customFormFieldCount]textinput.Model{urlInput, keyInput}
	s.customFormFocus = customFormFieldBaseURL
	s.customFormErr = ""
	s.customFormActive = true
}

func (s *ProviderSelector) closeCustomForm() {
	s.customFormActive = false
	s.customFormErr = ""
}

func (s *ProviderSelector) handleCustomFormKey(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "esc":
		s.closeCustomForm()
		return nil
	case "tab", "down":
		s.focusCustomFormField((s.customFormFocus + 1) % customFormFieldCount)
		return nil
	case "shift+tab", "up":
		s.focusCustomFormField((s.customFormFocus + customFormFieldCount - 1) % customFormFieldCount)
		return nil
	case "enter":
		return s.submitCustomForm()
	default:
		var cmd tea.Cmd
		s.customFormInputs[s.customFormFocus], cmd = s.customFormInputs[s.customFormFocus].Update(key)
		return cmd
	}
}

func (s *ProviderSelector) focusCustomFormField(i int) {
	s.customFormInputs[s.customFormFocus].Blur()
	s.customFormFocus = i
	s.customFormInputs[i].Focus()
}

// validateCustomForm normalizes and validates the two fields. An empty API key
// means "keep the stored one" and only fails when nothing is stored.
func (s *ProviderSelector) validateCustomForm() (baseURL, apiKey string, err error) {
	baseURL = strings.TrimRight(strings.TrimSpace(s.customFormInputs[customFormFieldBaseURL].Value()), "/")
	if baseURL == "" {
		return "", "", fmt.Errorf("base URL is required")
	}
	if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return "", "", fmt.Errorf("base URL must start with https:// or http://")
	}

	apiKey = strings.TrimSpace(s.customFormInputs[customFormFieldAPIKey].Value())
	if apiKey == "" && secret.Resolve(llm.CustomAPIKeyEnvVar) == "" {
		return "", "", fmt.Errorf("API key is required")
	}
	return baseURL, apiKey, nil
}

// submitCustomForm persists the form (baseURL to the llm store, API key to the
// secret store), then connects: a successful connect fetches the model list
// into the Models tab.
func (s *ProviderSelector) submitCustomForm() tea.Cmd {
	baseURL, apiKey, err := s.validateCustomForm()
	if err != nil {
		s.customFormErr = err.Error()
		return nil
	}

	if apiKey != "" {
		if store := secret.Default(); store != nil {
			_ = store.Set(llm.CustomAPIKeyEnvVar, apiKey)
		}
		os.Setenv(llm.CustomAPIKeyEnvVar, apiKey)
	}

	if err := s.store.SetCustomProvider(llm.CustomProviderConfig{ID: string(llm.CustomProvider), BaseURL: baseURL}); err != nil {
		s.customFormErr = err.Error()
		return nil
	}
	s.closeCustomForm()

	// Rebuild from the store so the Providers tab reflects the update, then
	// point the selection at the custom row so the connect result renders there.
	_, _ = s.loadProviderData()
	s.rebuildVisibleItems()
	for i := range s.allProviders {
		p := &s.allProviders[i]
		if p.Provider != llm.CustomProvider || len(p.AuthMethods) != 1 {
			continue
		}
		for vi, item := range s.visibleItems {
			if item.Kind == providerItemProvider && item.ProviderIdx == i {
				s.selectedIdx = vi
				break
			}
		}
		return s.connectAuthMethod(p.AuthMethods[0], s.selectedIdx)
	}
	return nil
}
