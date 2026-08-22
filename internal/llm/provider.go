package llm

import (
	"context"
	"fmt"

	"github.com/genai-io/san/internal/core"
)

// What San asks of a provider.
//
// One interface — send a turn, list the models, say your name — plus the types
// that cross it, and a handful of optional extensions a provider implements
// only if the answer is not the common one. Everything above this file talks
// to Provider; everything below it is one vendor's way of satisfying it (see
// vendor.go).

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// ProviderID identifies a vendor San can talk to, as the lowercase slug it is
// stored and configured under — "anthropic", "deepseek". It is not what the
// user sees: ProviderDisplayName answers that.
type ProviderID string

const (
	AgnesAI    ProviderID = "agnesai"
	Anthropic  ProviderID = "anthropic"
	OpenAI     ProviderID = "openai"
	Copilot    ProviderID = "copilot"
	Google     ProviderID = "google"
	Moonshot   ProviderID = "moonshot"
	Alibaba    ProviderID = "alibaba"
	MinMax     ProviderID = "minmax"
	BigModel   ProviderID = "bigmodel"
	DeepSeek   ProviderID = "deepseek"
	SenseNova  ProviderID = "sensenova"
	Ollama     ProviderID = "ollama"
	Mimo       ProviderID = "mimo"
	Volcengine ProviderID = "volcengine"
)

// AuthMethod is how a provider is reached. The same models can be served
// several ways — an Anthropic key, a Vertex deployment, a Copilot
// subscription — and they differ in credentials, host and often catalog, so a
// connection is identified by provider *and* method.
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

// Meta is what the registry knows about one provider/auth-method pair before
// anything is opened: what it is called, and what it needs to work.
type Meta struct {
	Provider    ProviderID
	AuthMethod  AuthMethod
	EnvVars     []string // credentials this method requires, read through the secret store
	DisplayName string   // per-auth-method name, e.g. "Direct API", "Vertex AI"
}

// ProviderDisplay is how a provider is presented in the UI, shared across all
// of its auth methods.
type ProviderDisplay struct {
	Name  string // UI display name, e.g. "Anthropic"
	Order int    // display order in the picker; lower comes first
}

// ---------------------------------------------------------------------------
// The contract
// ---------------------------------------------------------------------------

// Provider is one configured endpoint San can infer through.
type Provider interface {
	// Stream sends one turn and returns its chunks, closing the channel when
	// the turn ends.
	Stream(ctx context.Context, opts CompletionOptions) <-chan StreamChunk

	// ListModels returns the models this endpoint serves.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Name returns the provider's identity, "vendor:auth_method".
	Name() string
}

// Factory opens a connection to one provider auth method. The registry holds
// one per registered entry and calls it when a connection is actually needed.
type Factory func(ctx context.Context) (Provider, error)

// CompletionOptions is one turn, as San hands it over.
type CompletionOptions struct {
	Model          string
	Messages       []core.Message
	MaxTokens      int
	Temperature    float64
	Tools          []ToolSchema
	SystemPrompt   string
	ThinkingEffort string
}

// CompletionResponse is the provider-facing completion result. It aliases
// core.InferResponse: the provider streaming layer and the agent loop exchange
// one response type, so there is no field-for-field conversion between them and
// no way for the two to drift. The logging accessors (LogStopReason, …) live on
// core.InferResponse.
type CompletionResponse = core.InferResponse

// Usage is an alias for core.Usage — token accounting is defined once in the
// foundation layer so the provider response and core.InferResponse share it.
type Usage = core.Usage

// ToolSchema is an alias for core.ToolSchema, so a provider and the agent loop
// describe a tool the same way.
type ToolSchema = core.ToolSchema

// ChunkType is what a stream chunk carries.
type ChunkType string

const (
	ChunkTypeText     ChunkType = "text"
	ChunkTypeThinking ChunkType = "thinking"
	ChunkTypeDone     ChunkType = "done"
	ChunkTypeError    ChunkType = "error"
)

// StreamChunk is one piece of a turn in flight. Tool-call deltas are not
// streamed; completed tool calls ride in the final Response on the done chunk.
type StreamChunk struct {
	Type     ChunkType
	Text     string              // for text and thinking chunks
	Response *CompletionResponse // for the done chunk
	Error    error               // for an error chunk
}

// ModelStage is where a model sits in its vendor's lifecycle. The zero value
// is a generally available model; a retired one never reaches here, because the
// picker lists only what still serves requests.
type ModelStage string

const (
	ModelStable     ModelStage = ""
	ModelPreview    ModelStage = "preview"
	ModelDeprecated ModelStage = "deprecated"
)

// ModelInfo is one model as the picker and the store see it.
//
// Every field is a fact, never a rendering of one: the picker aligns these into
// columns and writes the labels itself, and prose stored here would fix the
// wording — separators included — in a file that outlives the release that
// wrote it. A zero token limit means unknown; callers skip the check rather
// than acting on a guess.
type ModelInfo struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	DisplayName      string               `json:"displayName,omitempty"`
	InputTokenLimit  int                  `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit int                  `json:"outputTokenLimit,omitempty"`
	Reasoning        *ReasoningCapability `json:"reasoning,omitempty"`
	Pricing          *ModelPricing        `json:"pricing,omitempty"`

	// Stage, and the model to move to when it is deprecated.
	Stage       ModelStage `json:"stage,omitempty"`
	Replacement string     `json:"replacement,omitempty"`

	// AcceptsImages reports vision input. RejectsTools reports the opposite of
	// what its name suggests being false: San is a tool-calling agent, so a
	// model that takes no tools cannot do the job at all, and the flag is
	// phrased so that the common case is the zero value.
	AcceptsImages bool `json:"acceptsImages,omitempty"`
	RejectsTools  bool `json:"rejectsTools,omitempty"`
}

// Complete runs a turn to the end and returns it whole, for the callers that
// have nothing to show mid-flight (compaction, the autopilot steers).
func Complete(ctx context.Context, provider Provider, opts CompletionOptions) (CompletionResponse, error) {
	var response CompletionResponse

	gotDone := false
	for chunk := range provider.Stream(ctx, opts) {
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

// ---------------------------------------------------------------------------
// Optional extensions
// ---------------------------------------------------------------------------
//
// Each of these has a default that is right for most providers, so it is an
// interface a provider opts into rather than a method on Provider that every
// implementation would have to answer. Read them through the helper beside
// each one, never by asserting at the call site.

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

// PromptPrefixCacheProvider is implemented by providers that place their
// prompt-cache breakpoint at the end of the system prompt. Anthropic renders a
// request as tools → system → messages, so a breakpoint there makes the cached
// prefix exactly the tool definitions plus the system prompt — and the cache
// token counts an exact measurement of those two.
type PromptPrefixCacheProvider interface {
	CachesToolsAndSystemPrompt() bool
}

// CachesToolsAndSystemPrompt reports whether the provider's reported cache
// tokens (creation plus read) count exactly the tool definitions plus the
// system prompt.
//
// It defaults to false, which is the honest answer for everyone else: providers
// that cache automatically pick their own prefix boundary, so their cache
// counts cover an unknown span — usually rather more than the prompt, since a
// stable conversation head caches too. Reading those as a measurement of the
// prompt would silently overstate it.
func CachesToolsAndSystemPrompt(p Provider) bool {
	cp, ok := p.(PromptPrefixCacheProvider)
	return ok && cp.CachesToolsAndSystemPrompt()
}

// ModelLimitsFetcher is implemented by an endpoint that answers about one
// model at a time rather than in its listing — Alibaba's Model Studio serves
// hundreds of models and publishes a window for none of them. San reaches for
// it only when the listing already came back without one.
type ModelLimitsFetcher interface {
	FetchModelLimits(ctx context.Context, modelID string) (inputLimit, outputLimit int, err error)
}
