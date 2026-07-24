// Package custom implements a user-defined, OpenAI-compatible provider.
//
// Unlike the built-in providers, its baseURL is supplied at runtime through the
// /models Providers tab and persisted in the llm Store; the API key lives in the
// secret store under APIKeyEnvVar. The provider ID is fixed (DefaultID) — there
// is only one custom provider, so a user-chosen ID would add rename bookkeeping
// without distinguishing anything. Registration is static (init) since the ID
// never changes; newClient reads the persisted baseURL lazily on connect.
package custom

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/llm/openaicompat"
	"github.com/genai-io/san/internal/secret"
)

const (
	// DefaultID is the custom provider's fixed ID.
	DefaultID = "custom"
	// APIKeyEnvVar is the env/secret-store key holding the custom provider's API key.
	APIKeyEnvVar = "SAN_CUSTOM_API_KEY"
	// displayOrder places Custom after every built-in provider (max built-in is 130).
	displayOrder = 140
)

// meta is the registration metadata for the custom provider.
var meta = llm.Meta{
	Provider:    llm.Name(DefaultID),
	AuthMethod:  llm.AuthAPIKey,
	EnvVars:     []string{APIKeyEnvVar},
	DisplayName: "Direct API",
}

func init() {
	llm.RegisterProviderDisplay(llm.Name(DefaultID), llm.ProviderDisplay{Name: "Custom", Order: displayOrder})
	llm.Register(meta, newClient)
}

// newClient builds a client from the persisted config (baseURL) and the secret
// store (API key).
func newClient(_ context.Context) (llm.Provider, error) {
	store, err := llm.NewStore()
	if err != nil {
		return nil, fmt.Errorf("failed to load store: %w", err)
	}
	cfg := store.CustomProvider()
	if cfg == nil || cfg.BaseURL == "" {
		return nil, fmt.Errorf("custom provider not configured: set a base URL under /models > Providers > Custom")
	}
	return &client{
		api: openai.NewClient(
			option.WithAPIKey(secret.Resolve(APIKeyEnvVar)),
			option.WithBaseURL(cfg.BaseURL),
			option.WithMaxRetries(0),
		),
		name: DefaultID + ":" + string(llm.AuthAPIKey),
	}, nil
}

// client implements llm.Provider for any OpenAI-compatible endpoint.
type client struct {
	api  openai.Client
	name string
}

// Name returns the provider name.
func (c *client) Name() string { return c.name }

// Stream sends a completion request and returns a channel of streaming chunks.
func (c *client) Stream(ctx context.Context, opts llm.CompletionOptions) <-chan llm.StreamChunk {
	return openaicompat.StreamChatCompletions(ctx, openaicompat.ChatStreamConfig{
		Client:           c.api,
		ProviderName:     c.name,
		Options:          opts,
		ConvertAssistant: openaicompat.DefaultAssistantMessage,
		// Compatible platforms commonly pass reasoning_content through from the
		// upstream model; extracting it is a no-op when absent.
		ExtractReasoning: true,
	})
}

// ListModels returns the models exposed by the endpoint's GET /models.
func (c *client) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	page, err := c.api.Models.List(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]llm.ModelInfo, 0, len(page.Data))
	for _, m := range page.Data {
		info := llm.ModelInfo{ID: m.ID, Name: m.ID, DisplayName: m.ID}
		// OpenAI-compatible platforms expose a context window in the raw model
		// object; pull it into InputTokenLimit so the picker hides the
		// no-context-window warning when the platform reports one.
		if raw := m.RawJSON(); raw != "" {
			var extra struct {
				ContextLength    int `json:"context_length"`
				MaxContextLength int `json:"max_context_length"`
				ContextWindow    int `json:"context_window"`
			}
			if json.Unmarshal([]byte(raw), &extra) == nil {
				info.InputTokenLimit = max(extra.ContextLength, extra.MaxContextLength, extra.ContextWindow)
			}
		}
		models = append(models, info)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("endpoint returned no models")
	}
	return models, nil
}

// Ensure client implements Provider.
var _ llm.Provider = (*client)(nil)
