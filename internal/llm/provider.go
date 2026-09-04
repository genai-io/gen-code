package llm

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"

	"github.com/genai-io/san/internal/core"
)

// What San asks of a provider: one interface, the types that cross it, and a
// few optional extensions a provider implements only when its answer differs
// from the common one. One vendor's way of satisfying it is in vendor.go.

// ProviderID identifies a vendor by the lowercase slug it is stored and
// configured under. It is not what the user sees — ProviderDisplayName is.
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
// several ways, differing in credentials, host and often catalog, so a
// connection is identified by provider *and* method.
type AuthMethod string

const (
	AuthAPIKey  AuthMethod = "api_key"
	AuthVertex  AuthMethod = "vertex"
	AuthBedrock AuthMethod = "bedrock"
	AuthCoding  AuthMethod = "coding"

	// AuthSubscription is a consumer subscription (OAuth) rather than a metered
	// API key — e.g. an OpenAI ChatGPT plan. Deliberately provider-agnostic, so
	// other subscription logins reuse it.
	AuthSubscription AuthMethod = "subscription"
)

// Meta is what the registry knows about one provider/auth-method pair: what it
// is called, and what it needs to work.
type Meta struct {
	Provider    ProviderID
	AuthMethod  AuthMethod
	EnvVars     []string // credentials, read through the secret store
	DisplayName string   // per-auth-method name, e.g. "Direct API", "Vertex AI"
}

// ProviderDisplay is how a provider is presented, shared by its auth methods.
type ProviderDisplay struct {
	Name  string // e.g. "Anthropic"
	Order int    // display order in the picker; lower comes first
}

// Provider is one configured endpoint San can infer through.
type Provider interface {
	// Client hands over the SDK client for one model, built and cached by this
	// provider. Streaming is the SDK's; what a provider owns is reaching the
	// endpoint — credentials, headers, the model's own entry.
	Client(modelID string, headers map[string]string) (*ai.Client, error)

	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Name is the provider's identity, "vendor:auth_method".
	Name() string
}

// Factory opens a connection, called when one is actually needed.
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

// CompletionResponse aliases core.InferResponse so the streaming layer and the
// agent loop exchange one type, with no conversion between them to drift.
type CompletionResponse = core.InferResponse

// Usage and ToolSchema are likewise core's, defined once in the foundation
// layer rather than restated here.
type (
	Usage      = core.Usage
	ToolSchema = core.ToolSchema
)

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

// ModelLifecycle is where a model sits between announcement and retirement.
// A retired model never reaches here: ListModels drops it.
type ModelLifecycle string

const (
	ModelStable     ModelLifecycle = ""
	ModelPreview    ModelLifecycle = "preview"
	ModelDeprecated ModelLifecycle = "deprecated"
)

// ModelInfo is one model as the picker and the store see it.
//
// Two rules, both because this record is cached to disk. Every field is a fact
// rather than a rendering of one, since prose here would fix the wording in a
// file that outlives the release that wrote it. And each flag is phrased so the
// common case is the zero value, since a listing written by an older San
// decodes with them unset and that has to read as "nothing unusual" rather than
// as a claim. A zero token limit likewise means unknown, not zero.
type ModelInfo struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	DisplayName      string               `json:"displayName,omitempty"`
	InputTokenLimit  int                  `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit int                  `json:"outputTokenLimit,omitempty"`
	Reasoning        *ReasoningCapability `json:"reasoning,omitempty"`

	// Replacement is the actionable half of a deprecation.
	Lifecycle   ModelLifecycle `json:"lifecycle,omitempty"`
	Replacement string         `json:"replacement,omitempty"`

	// TextOnly is the exception: nearly every model listed takes an image.
	TextOnly bool `json:"textOnly,omitempty"`
}

// Complete sends one non-streaming call and returns the whole answer. The
// collection it used to do by hand is ai.Client.Complete.
func Complete(ctx context.Context, provider Provider, opts CompletionOptions) (CompletionResponse, error) {
	client, err := provider.Client(opts.Model, TurnHeaders(provider, opts.Messages))
	if err != nil {
		return CompletionResponse{}, err
	}
	callOpts := []ai.Option{ai.WithSystem(opts.SystemPrompt)}
	if len(opts.Tools) > 0 {
		callOpts = append(callOpts, ai.WithTools(core.ToAITools(opts.Tools)...))
	}
	callOpts = append(callOpts, callOptions(opts.MaxTokens, opts.ThinkingEffort, opts.Temperature)...)
	resp, err := client.Complete(ctx, opts.Messages, callOpts...)
	if err != nil {
		return CompletionResponse{}, err
	}
	return *core.FromAIResponse(resp), nil
}

// callOptions turns San's per-call settings into the SDK's options.
//
// A setting is only passed when San actually set it: passing an option is what
// marks it explicit, so sending a zero would override the model's own default
// rather than inherit it. Effort is the exception — an empty rung *is*
// ai.EffortDefault, which says the same thing.
func callOptions(maxTokens int, effort string, temperature float64) []ai.Option {
	var out []ai.Option
	if maxTokens > 0 {
		out = append(out, ai.WithMaxTokens(maxTokens))
	}
	if temperature > 0 {
		out = append(out, ai.WithTemperature(temperature))
	}
	return append(out, ai.WithEffort(ai.Effort(effort)))
}

// The optional extensions. Each has a default that suits most providers, so it
// is opted into rather than added to Provider — and read through the helper
// beside it, never by asserting at the call site.

// ImageSupportProvider lets a text-only provider (e.g. DeepSeek) opt out of
// image input, which everyone else is assumed to accept.
type ImageSupportProvider interface {
	SupportsImages(model string) bool
}

func SupportsImages(p Provider, model string) bool {
	if ip, ok := p.(ImageSupportProvider); ok {
		return ip.SupportsImages(model)
	}
	return true
}

// PromptPrefixCacheProvider is implemented by providers that place their
// prompt-cache breakpoint at the end of the system prompt. Anthropic renders a
// request as tools → system → messages, so a breakpoint there makes the cache
// token counts an exact measurement of the tools plus the system prompt.
type PromptPrefixCacheProvider interface {
	CachesToolsAndSystemPrompt() bool
}

// CachesToolsAndSystemPrompt defaults to false, which is the honest answer for
// everyone else: a provider that caches automatically picks its own prefix
// boundary, so its counts cover an unknown and usually larger span. Reading
// those as a measurement of the prompt would silently overstate it.
func CachesToolsAndSystemPrompt(p Provider) bool {
	cp, ok := p.(PromptPrefixCacheProvider)
	return ok && cp.CachesToolsAndSystemPrompt()
}

// ModelLimitsFetcher is implemented by an endpoint that answers about one model
// at a time rather than in its listing — Model Studio serves hundreds and
// publishes a window for none. Reached for only when a listing came back
// without one.
type ModelLimitsFetcher interface {
	FetchModelLimits(ctx context.Context, modelID string) (inputLimit, outputLimit int, err error)
}

// TurnHeaderProvider is implemented by an endpoint whose headers depend on
// what the turn sends. Copilot is the only one: it meters an agent's follow-up
// differently from a turn the user typed, and it rejects image content unless
// the request opts into vision.
type TurnHeaderProvider interface {
	TurnHeaders(msgs []core.Message) map[string]string
}

// TurnHeaders returns the headers this turn needs beyond the endpoint's fixed
// ones. Nil for every provider whose headers never vary, which is all but one.
func TurnHeaders(p Provider, msgs []core.Message) map[string]string {
	th, ok := p.(TurnHeaderProvider)
	if !ok {
		return nil
	}
	return th.TurnHeaders(msgs)
}
