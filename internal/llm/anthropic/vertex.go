package anthropic

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/secret"
)

// VertexMeta is the metadata for Anthropic via Vertex AI
var VertexMeta = llm.Meta{
	Provider:    llm.Anthropic,
	AuthMethod:  llm.AuthVertex,
	EnvVars:     []string{"CLOUD_ML_REGION", "ANTHROPIC_VERTEX_PROJECT_ID"},
	DisplayName: "Vertex AI",
}

// vertexModels is the static list of Claude models available on Vertex AI.
//
// Source:
// - https://docs.anthropic.com/en/docs/about-claude/models/overview
// - https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude
//
// Note: Vertex AI does not provide a stable Anthropic Models API, so we use a
// static list and refresh it when Anthropic/Vertex documentation changes.
// Last checked 2026-08-20.
//
// This list is deliberately longer than the first-party catalog: Opus 4.1,
// Opus 4 and Sonnet 4 are retired on the Claude API but remain available on
// Google Cloud, which the pricing page states per model.
//
// There is no separate 1M-context variant. Claude 4.6 and later carry the full
// 1M window at standard pricing, so the "[1m]" suffix that used to select a
// beta header is meaningless on them.
var vertexModels = []llm.ModelInfo{
	newVertexModel("claude-fable-5", "Claude Fable 5", "Claude Fable 5 (Most Capable)"),
	newVertexModel("claude-opus-5", "Claude Opus 5", "Claude Opus 5"),
	newVertexModel("claude-opus-4-8", "Claude Opus 4.8", "Claude Opus 4.8"),
	newVertexModel("claude-opus-4-7", "Claude Opus 4.7", "Claude Opus 4.7"),
	newVertexModel("claude-opus-4-6", "Claude Opus 4.6", "Claude Opus 4.6"),
	newVertexModel("claude-opus-4-5@20251101", "Claude Opus 4.5", "Claude Opus 4.5"),
	newVertexModel("claude-opus-4-1@20250805", "Claude Opus 4.1", "Claude Opus 4.1"),
	newVertexModel("claude-opus-4@20250514", "Claude Opus 4", "Claude Opus 4"),
	newVertexModel("claude-sonnet-5", "Claude Sonnet 5", "Claude Sonnet 5"),
	newVertexModel("claude-sonnet-4-6", "Claude Sonnet 4.6", "Claude Sonnet 4.6"),
	newVertexModel("claude-sonnet-4-5@20250929", "Claude Sonnet 4.5", "Claude Sonnet 4.5"),
	newVertexModel("claude-sonnet-4@20250514", "Claude Sonnet 4", "Claude Sonnet 4"),
	newVertexModel("claude-haiku-4-5@20251001", "Claude Haiku 4.5", "Claude Haiku 4.5 (Fast)"),
	newVertexModel("claude-3-5-haiku@20241022", "Claude Haiku 3.5", "Claude Haiku 3.5"),
}

// VertexClient wraps the standard Client with Vertex-specific behavior
type VertexClient struct {
	*Client
}

// ListModels tries the Anthropic Models API first, falling back to a static
// list with a warning error when the API is unavailable (e.g. 404 on Vertex AI).
// A failed fetch does not permanently cache the fallback — subsequent calls retry.
func (c *VertexClient) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	c.modelsMu.Lock()
	defer c.modelsMu.Unlock()

	if c.cachedModels != nil {
		return c.cachedModels, nil
	}

	models, err := c.fetchModels(ctx)
	if err == nil {
		c.cachedModels = models
		return c.cachedModels, nil
	}
	// Return static fallback but don't cache it so we retry next time
	return vertexModels, nil
}

// NewVertexClient creates a new Anthropic client using Vertex AI authentication
func NewVertexClient(ctx context.Context) (llm.Provider, error) {
	region := secret.Resolve("CLOUD_ML_REGION")
	if region == "" {
		region = "us-east5"
	}
	projectID := secret.Resolve("ANTHROPIC_VERTEX_PROJECT_ID")

	// Retries are owned by the app-level decorator; disable the SDK's own.
	client := anthropic.NewClient(
		vertex.WithGoogleAuth(ctx, region, projectID),
		option.WithMaxRetries(0),
	)

	baseClient := NewClient(client, "anthropic:vertex")
	return &VertexClient{Client: baseClient}, nil
}

// Ensure VertexClient implements Provider
var _ llm.Provider = (*VertexClient)(nil)

// init registers the Vertex AI provider
func init() {
	llm.Register(VertexMeta, NewVertexClient)
}

func newVertexModel(id, name, displayName string) llm.ModelInfo {
	info, ok := CatalogModel(id)
	if !ok {
		return llm.ModelInfo{ID: id, Name: name, DisplayName: displayName}
	}
	info.Name = name
	info.DisplayName = displayName
	return info
}
