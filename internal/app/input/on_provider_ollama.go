// Provider selector: the Ollama provider form for setting the base URL.
// Ollama is a local provider that speaks the OpenAI-compatible API. Unlike
// API-key-based providers, its configuration is just a base URL — no secret
// key — so the form shows a single (non-password-masked) URL input.
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

// ollamaEnvVar is the env/secret-store key holding the Ollama base URL.
const ollamaEnvVar = "OLLAMA_BASE_URL"

// isOllamaProvider reports whether name is the Ollama provider.
func (s *ProviderSelector) isOllamaProvider(name llm.ProviderID) bool {
	return name == llm.Ollama
}

// openOllamaForm initializes the inline URL form, prefilled from the saved config.
func (s *ProviderSelector) openOllamaForm() {
	baseURL := ""
	if store := secret.Default(); store != nil {
		if v := store.Get(ollamaEnvVar); v != "" {
			baseURL = v
		}
	}
	if baseURL == "" {
		baseURL = os.Getenv(ollamaEnvVar)
	}

	urlInput := textinput.New()
	urlInput.Placeholder = "http://localhost:11434/v1"
	urlInput.SetValue(baseURL)
	urlInput.CharLimit = 256
	urlInput.SetWidth(40)
	urlInput.Focus()

	s.ollamaURLInput = urlInput
	s.ollamaFormErr = ""
	s.ollamaFormActive = true
}

func (s *ProviderSelector) closeOllamaForm() {
	s.ollamaFormActive = false
	s.ollamaFormErr = ""
}

func (s *ProviderSelector) handleOllamaFormKey(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "esc":
		s.closeOllamaForm()
		return nil
	case "enter":
		return s.submitOllamaForm()
	default:
		var cmd tea.Cmd
		s.ollamaURLInput, cmd = s.ollamaURLInput.Update(key)
		return cmd
	}
}

// validateOllamaForm normalizes and validates the URL field.
func (s *ProviderSelector) validateOllamaForm() (string, error) {
	baseURL := strings.TrimSpace(s.ollamaURLInput.Value())
	if baseURL == "" {
		return "", fmt.Errorf("base URL is required")
	}
	if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return "", fmt.Errorf("base URL must start with https:// or http://")
	}
	return baseURL, nil
}

// submitOllamaForm persists the URL to the secret store, then connects.
func (s *ProviderSelector) submitOllamaForm() tea.Cmd {
	baseURL, err := s.validateOllamaForm()
	if err != nil {
		s.ollamaFormErr = err.Error()
		return nil
	}

	if store := secret.Default(); store != nil {
		_ = store.Set(ollamaEnvVar, baseURL)
	}
	os.Setenv(ollamaEnvVar, baseURL)
	s.closeOllamaForm()

	// Rebuild from the store so the Providers tab reflects the update, then
	// point the selection at the Ollama row so the connect result renders there.
	_, _ = s.loadProviderData()
	s.rebuildVisibleItems()
	for i := range s.allProviders {
		p := &s.allProviders[i]
		if p.Provider != llm.Ollama || len(p.AuthMethods) != 1 {
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
