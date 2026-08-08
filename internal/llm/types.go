package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/core"
)

// Name represents a provider name
type Name string

const (
	AgnesAI    Name = "agnesai"
	Anthropic  Name = "anthropic"
	OpenAI     Name = "openai"
	Copilot    Name = "copilot"
	Google     Name = "google"
	Moonshot   Name = "moonshot"
	Alibaba    Name = "alibaba"
	MinMax     Name = "minmax"
	BigModel   Name = "bigmodel"
	DeepSeek   Name = "deepseek"
	SenseNova  Name = "sensenova"
	Ollama     Name = "ollama"
	Mimo       Name = "mimo"
	Volcengine Name = "volcengine"
)

// AuthMethod represents an authentication method for an LLM provider.
type AuthMethod string

const (
	AuthAPIKey  AuthMethod = "api_key"
	AuthVertex  AuthMethod = "vertex"
	AuthBedrock AuthMethod = "bedrock"
	AuthCoding  AuthMethod = "coding"

	// AuthSubscription authenticates with a consumer subscription (OAuth) rather
	// than a metered API key — e.g. an OpenAI ChatGPT Plus/Pro plan. The category
	// is provider-agnostic so other subscription logins can reuse it.
	AuthSubscription AuthMethod = "subscription"
)

// Meta contains static metadata about a provider auth method.
type Meta struct {
	Provider    Name
	AuthMethod  AuthMethod
	EnvVars     []string // Required environment variables
	DisplayName string   // per-authMethod name (e.g. "Direct API", "Vertex AI")
}

// ProviderDisplay describes how a provider is presented in the UI,
// shared across all of its auth methods.
type ProviderDisplay struct {
	Name  string // UI display name (e.g. "Anthropic")
	Order int    // display order in UI (lower = earlier)
}

// Key returns a unique key for this provider configuration
func (m Meta) Key() string {
	return string(m.Provider) + ":" + string(m.AuthMethod)
}

// ThinkingEffortProvider is implemented by providers that expose native thinking
// or reasoning effort values.
type ThinkingEffortProvider interface {
	ThinkingEfforts(model string) []string
	DefaultThinkingEffort(model string) string
}

// ReasoningCapability describes the reasoning-effort values advertised for one
// model. Providers that return this metadata from ListModels let the rest of the
// app follow the live catalog instead of guessing capabilities from model IDs.
type ReasoningCapability struct {
	SupportedEfforts []string `json:"supportedEfforts,omitempty"`
	DefaultEffort    string   `json:"defaultEffort,omitempty"`
}

// NewReasoningCapability normalizes provider-supplied reasoning metadata.
// Unknown effort labels are intentionally preserved so newly introduced
// provider-native levels work without a san release.
func NewReasoningCapability(efforts []string, defaultEffort string) *ReasoningCapability {
	normalized := make([]string, 0, len(efforts))
	seen := make(map[string]struct{}, len(efforts))
	for _, effort := range efforts {
		effort = strings.ToLower(strings.TrimSpace(effort))
		if effort == "" {
			continue
		}
		if _, ok := seen[effort]; ok {
			continue
		}
		seen[effort] = struct{}{}
		normalized = append(normalized, effort)
	}
	if len(normalized) == 0 {
		return nil
	}

	defaultEffort = normalizeThinkingEffort(defaultEffort, normalized)
	return &ReasoningCapability{
		SupportedEfforts: normalized,
		DefaultEffort:    defaultEffort,
	}
}

// ImageSupportProvider is implemented by providers that declare whether a model
// accepts image input. Providers that don't implement it are assumed to support
// images (the common case); a text-only provider opts out by returning false.
type ImageSupportProvider interface {
	SupportsImages(model string) bool
}

// SupportsImages reports whether the provider's model accepts image input. It
// defaults to true so vision-capable providers need no change; text-only
// providers (e.g. DeepSeek) opt out via ImageSupportProvider.
func SupportsImages(p Provider, model string) bool {
	if ip, ok := p.(ImageSupportProvider); ok {
		return ip.SupportsImages(model)
	}
	return true
}

func ThinkingEfforts(p Provider, model string) []string {
	ep, ok := p.(ThinkingEffortProvider)
	if !ok {
		return nil
	}
	efforts := ep.ThinkingEfforts(model)
	if len(efforts) == 0 {
		return nil
	}
	out := make([]string, len(efforts))
	copy(out, efforts)
	return out
}

func DefaultThinkingEffort(p Provider, model string) string {
	ep, ok := p.(ThinkingEffortProvider)
	if !ok {
		return ""
	}
	return normalizeThinkingEffort(ep.DefaultThinkingEffort(model), ep.ThinkingEfforts(model))
}

// ThinkingEffortsForModel returns live cached model metadata when available,
// falling back to the provider's static ThinkingEffortProvider implementation.
func ThinkingEffortsForModel(p Provider, store *Store, current *CurrentModelInfo) []string {
	capability := reasoningCapabilityForModel(p, store, current)
	if capability == nil {
		return nil
	}
	out := make([]string, len(capability.SupportedEfforts))
	copy(out, capability.SupportedEfforts)
	return out
}

// ResolveThinkingEffortForModel validates a selected effort against the live
// model metadata, then falls back to that model's advertised default.
func ResolveThinkingEffortForModel(p Provider, store *Store, current *CurrentModelInfo, selected string) string {
	capability := reasoningCapabilityForModel(p, store, current)
	if capability == nil {
		return ""
	}
	if effort := normalizeThinkingEffort(selected, capability.SupportedEfforts); effort != "" {
		return effort
	}
	return capability.DefaultEffort
}

// NextThinkingEffortForModel cycles through the live model-specific effort
// values, using the advertised default when no valid current value is set.
func NextThinkingEffortForModel(p Provider, store *Store, current *CurrentModelInfo, selected string) (string, bool) {
	capability := reasoningCapabilityForModel(p, store, current)
	if capability == nil {
		return "", false
	}
	currentEffort := normalizeThinkingEffort(selected, capability.SupportedEfforts)
	if currentEffort == "" {
		currentEffort = capability.DefaultEffort
	}
	for i, effort := range capability.SupportedEfforts {
		if effort == currentEffort {
			return capability.SupportedEfforts[(i+1)%len(capability.SupportedEfforts)], true
		}
	}
	return capability.SupportedEfforts[0], true
}

func reasoningCapabilityForModel(p Provider, store *Store, current *CurrentModelInfo) *ReasoningCapability {
	if current == nil || current.ModelID == "" {
		return nil
	}
	if store != nil {
		authMethod := store.ResolveAuthMethod(current)
		if capability, ok := store.CachedModelReasoningForProvider(current.Provider, authMethod, current.ModelID); ok {
			return capability
		}
	}
	return NewReasoningCapability(
		ThinkingEfforts(p, current.ModelID),
		DefaultThinkingEffort(p, current.ModelID),
	)
}

func normalizeThinkingEffort(effort string, efforts []string) string {
	effort = strings.TrimSpace(strings.ToLower(effort))
	if effort == "" {
		return ""
	}
	for _, allowed := range efforts {
		if strings.EqualFold(effort, allowed) {
			return allowed
		}
	}
	return ""
}

// ModelInfo represents information about an available model
type ModelInfo struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	DisplayName      string               `json:"displayName,omitempty"`
	Description      string               `json:"description,omitempty"`
	InputTokenLimit  int                  `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit int                  `json:"outputTokenLimit,omitempty"`
	Reasoning        *ReasoningCapability `json:"reasoning,omitempty"`
}

// CompletionOptions contains options for a completion request
type CompletionOptions struct {
	Model          string
	Messages       []core.Message
	MaxTokens      int
	Temperature    float64
	Tools          []ToolSchema
	SystemPrompt   string
	ThinkingEffort string
}

// --- Completion Response Types ---

// CompletionResponse is the provider-facing completion result. It aliases
// core.InferResponse: the provider streaming layer and the agent loop exchange
// one response type, so there is no field-for-field conversion between them and
// no way for the two to drift. The logging accessors (LogStopReason, …) live on
// core.InferResponse.
type CompletionResponse = core.InferResponse

// Usage is an alias for core.Usage — token accounting is defined once in the
// foundation layer so the provider response and core.InferResponse share it.
type Usage = core.Usage

// --- Streaming Types ---

// ChunkType represents the type of a stream chunk from a provider.
type ChunkType string

const (
	ChunkTypeText     ChunkType = "text"
	ChunkTypeThinking ChunkType = "thinking"
	ChunkTypeDone     ChunkType = "done"
	ChunkTypeError    ChunkType = "error"
)

// StreamChunk represents a chunk in a streaming response from a provider.
// Tool-call deltas are not streamed as chunks; completed tool calls ride in the
// final Response on the done chunk.
type StreamChunk struct {
	Type     ChunkType
	Text     string              // For text/thinking chunks
	Response *CompletionResponse // For done chunks
	Error    error               // For error chunks
}

// ToolSchema is a backward-compatible alias for core.ToolSchema.
type ToolSchema = core.ToolSchema

// Provider is the interface that all providers must implement
type Provider interface {
	// Stream sends a completion request and returns a channel of streaming chunks
	Stream(ctx context.Context, opts CompletionOptions) <-chan StreamChunk

	// ListModels returns the available models for this provider
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Name returns the provider name
	Name() string
}

// ModelLimitsFetcher is an optional interface for providers that can fetch
// token limits for a specific model via API (e.g. DashScope model detail endpoint).
type ModelLimitsFetcher interface {
	FetchModelLimits(ctx context.Context, modelID string) (inputLimit, outputLimit int, err error)
}

// Factory creates a new Provider instance
type Factory func(ctx context.Context) (Provider, error)

// Complete is a helper function that collects stream chunks into a complete response
// This provides non-streaming output from any Provider
func Complete(ctx context.Context, provider Provider, opts CompletionOptions) (CompletionResponse, error) {
	var response CompletionResponse

	streamChan := provider.Stream(ctx, opts)

	gotDone := false
	for chunk := range streamChan {
		switch chunk.Type {
		case ChunkTypeText:
			response.Content += chunk.Text
		case ChunkTypeDone:
			if chunk.Response != nil {
				return *chunk.Response, nil
			}
			gotDone = true
		case ChunkTypeError:
			return response, chunk.Error
		}
	}

	if !gotDone {
		return response, fmt.Errorf("stream closed without completion")
	}
	return response, nil
}
