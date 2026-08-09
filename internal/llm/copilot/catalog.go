package copilot

import (
	"cmp"
	"slices"

	"github.com/genai-io/san/internal/llm"
)

// modelsResponse is the Copilot /models catalog. It lists every model the
// account is entitled to, across vendors (OpenAI, Anthropic, Google, …), all
// served through the same Chat Completions-shaped endpoint.
type modelsResponse struct {
	Data []catalogModel `json:"data"`
}

// catalogModel is one catalog entry. model_picker_enabled marks the models the
// editor plugins offer users; the rest are internal or deprecated aliases.
type catalogModel struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	Vendor             string             `json:"vendor"`
	ModelPickerEnabled bool               `json:"model_picker_enabled"`
	Capabilities       modelCapabilities  `json:"capabilities"`
	Policy             *modelPolicyStatus `json:"policy"`
}

// modelCapabilities describes what an entry can do. Type separates chat models
// from the embedding models the same catalog carries.
type modelCapabilities struct {
	Type     string        `json:"type"`
	Limits   modelLimits   `json:"limits"`
	Supports modelSupports `json:"supports"`
}

type modelLimits struct {
	MaxContextWindowTokens int `json:"max_context_window_tokens"`
	MaxOutputTokens        int `json:"max_output_tokens"`
	MaxPromptTokens        int `json:"max_prompt_tokens"`
}

// modelSupports lists per-model features. Only tool calling is read: San drives
// every model through tool calls, so it decides whether an entry is usable at
// all. The catalog also reports `vision` per model, which San can't yet act on
// — llm.ModelInfo has no image-support field to carry it through the model
// store, so image requests opt in unconditionally and let the backend reject.
type modelSupports struct {
	ToolCalls bool `json:"tool_calls"`
}

// modelPolicyStatus reports per-model terms the user must accept once in
// GitHub's settings. Until then the model is listed but rejects requests.
type modelPolicyStatus struct {
	State string `json:"state"`
}

// toModelInfos converts the catalog to llm.ModelInfo, keeping only the chat
// models a user can actually run a turn with: san drives every model through
// tool calls, so an entry without tool support would fail on the first turn.
// Duplicate ids (the catalog repeats a model under several versions) collapse
// to the first entry.
func (r modelsResponse) toModelInfos() []llm.ModelInfo {
	seen := make(map[string]bool, len(r.Data))
	models := make([]llm.ModelInfo, 0, len(r.Data))

	for _, m := range r.Data {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		if !m.ModelPickerEnabled {
			continue
		}
		if m.Capabilities.Type != "" && m.Capabilities.Type != "chat" {
			continue
		}
		if !m.Capabilities.Supports.ToolCalls {
			continue
		}
		// A model whose policy hasn't been accepted answers every request with
		// an error until the user enables it on github.com, so listing it would
		// only offer a broken choice.
		if m.Policy != nil && m.Policy.State != "" && m.Policy.State != "enabled" {
			continue
		}
		seen[m.ID] = true

		name := cmp.Or(m.Name, m.ID)
		models = append(models, llm.ModelInfo{
			ID:               m.ID,
			Name:             name,
			DisplayName:      name,
			InputTokenLimit:  m.Capabilities.Limits.contextWindow(),
			OutputTokenLimit: m.Capabilities.Limits.MaxOutputTokens,
		})
	}

	slices.SortFunc(models, func(a, b llm.ModelInfo) int { return cmp.Compare(a.ID, b.ID) })
	return models
}

// contextWindow returns the usable input window. Most entries report the whole
// window; a few only report the prompt budget, which is the same number for our
// purposes (how much context san may send).
func (l modelLimits) contextWindow() int {
	return cmp.Or(l.MaxContextWindowTokens, l.MaxPromptTokens)
}
