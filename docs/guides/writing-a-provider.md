# Writing a Provider

Providers connect San to an LLM vendor. Most providers live under
`internal/llm/<provider>/` and register themselves at startup so `/models`
can discover them.

This guide uses `internal/llm/deepseek/` as the reference because it shows
the common OpenAI-compatible path while still documenting the provider
catalog, credentials, registration, streaming, and tests.

For the package-level design, see [`packages/llm.md`](../packages/2-feature/llm.md).

## Before You Start

First decide whether the new provider is OpenAI-compatible:

| Provider shape | Recommended path |
|---|---|
| Chat completions compatible with OpenAI-style streaming | Reuse `internal/llm/openaicompat/`. |
| Custom request, response, streaming, or auth shape | Implement the `llm.Provider` interface directly. |

If the provider exposes `/v1/chat/completions`-style streaming and can be
used through `openai-go` with a custom base URL, start from DeepSeek.

## Files to Add

A typical provider package has these files:

```
internal/llm/<provider>/
|- apikey.go
|- catalog.go
|- client.go
`- client_test.go
```

### `apikey.go`

Define the provider metadata and factory. The metadata controls what `/models`
shows and which environment variables must be present before the provider is
available.

DeepSeek uses:

```go
var APIKeyMeta = llm.Meta{
    Provider:    llm.DeepSeek,
    AuthMethod:  llm.AuthAPIKey,
    EnvVars:     []string{"DEEPSEEK_API_KEY"},
    DisplayName: "Direct API",
}
```

The factory resolves secrets, constructs the SDK client, and returns a
provider implementation:

```go
func NewAPIKeyClient(ctx context.Context) (llm.Provider, error) {
    baseURL := secret.Resolve("DEEPSEEK_BASE_URL")
    if baseURL == "" {
        baseURL = "https://api.deepseek.com"
    }

    client := openai.NewClient(
        option.WithAPIKey(secret.Resolve("DEEPSEEK_API_KEY")),
        option.WithBaseURL(baseURL),
        option.WithMaxRetries(0),
    )
    return NewClient(client, "deepseek:api_key"), nil
}
```

Register the provider in `init()`:

```go
func init() {
    llm.RegisterProviderDisplay(llm.DeepSeek, llm.ProviderDisplay{Name: "DeepSeek", Order: 40})
    llm.Register(APIKeyMeta, NewAPIKeyClient)
    llm.RegisterCostEstimator(llm.DeepSeek, EstimateCost)
}
```

### `catalog.go`

List known models and any pricing or token-limit metadata the provider needs.
At minimum, define static model metadata and lookup helpers:

```go
func StaticModels() []llm.ModelInfo
func CatalogModel(modelID string) (llm.ModelInfo, bool)
```

If the provider supports cost tracking, add an estimator and register it from
`apikey.go`:

```go
func EstimateCost(modelID string, usage llm.Usage) (llm.Money, bool)
```

When choosing a catalog shape, also check
[`internal/llm/CATALOGS.md`](../../internal/llm/CATALOGS.md).

### `client.go`

Implement the runtime provider. For an OpenAI-compatible service, wrap an
`openai.Client` and use `openaicompat.StreamChatCompletions`:

```go
type Client struct {
    client openai.Client
    name   string
}

func (c *Client) Name() string { return c.name }

func (c *Client) Stream(ctx context.Context, opts llm.CompletionOptions) <-chan llm.StreamChunk {
    return openaicompat.StreamChatCompletions(ctx, openaicompat.ChatStreamConfig{
        Client:       c.client,
        ProviderName: c.name,
        Options:      opts,
    })
}
```

Add provider-specific behavior only where needed. DeepSeek, for example,
customizes assistant-message conversion and adds `reasoning_effort` for
thinking models.

If the provider can list models, implement:

```go
func (c *Client) ListModels(ctx context.Context) ([]llm.ModelInfo, error)
```

If the provider has image or thinking support, implement the optional
interfaces and assert them at the bottom of the file:

```go
var _ llm.Provider = (*Client)(nil)
var _ llm.ThinkingEffortProvider = (*Client)(nil)
var _ llm.ImageSupportProvider = (*Client)(nil)
```

Only assert optional interfaces that the provider actually supports.

### `client_test.go`

Keep tests local and deterministic. Prefer fake HTTP transports over live API
calls. DeepSeek tests cover:

- propagating model-list errors;
- stream request shape;
- text-only image support;
- cost estimation;
- thinking-effort request fields;
- provider interface support.

At minimum, test request construction and metadata behavior without requiring
real credentials.

## Register the Provider Name

Provider names are defined in `internal/llm/types.go`. Add a new constant if
the provider is new, then import the package for side-effect registration from
`cmd/san/main.go`:

```go
_ "github.com/genai-io/san/internal/llm/<provider>"
```

This makes the provider available to `/models` through the global registry.

## Wire Credentials and Docs

Add the required environment variable to the credentials table in `README.md`.
For an API-key provider, use a row like:

```markdown
| **ProviderName** | `PROVIDER_API_KEY` |
```

If the provider supports a configurable base URL, document that near the
provider package or in the relevant reference page when it changes user
behavior.

## Validation

Before opening a PR:

```bash
go test ./internal/llm/<provider>/...
go test ./internal/llm/...
make ci
```

If local tools are unavailable, still run what you can and describe the
environment limitation in the PR body.

## Common Pitfalls

- **Skipping registration.** If the package is not imported from
  `cmd/san/main.go`, `/models` will not discover it.
- **Live API tests.** Unit tests should not require a real provider key.
- **Missing token limits.** Static catalog entries should include useful
  `InputTokenLimit` and `OutputTokenLimit` values when known.
- **Over-customizing conversions.** Reuse `openaicompat/` unless the provider
  really needs a custom wire format.
- **Forgetting docs.** Update the README credentials table and link any
  provider-specific reference page the same PR introduces.

## See Also

- [`packages/llm.md`](../packages/2-feature/llm.md) - LLM package contract.
- [`reference/token-limits.md`](../reference/token-limits.md) - token-limit
  sources and caching.
- [`reference/cost-tracking.md`](../reference/cost-tracking.md) - cost
  estimation behavior.
- [`internal/llm/CATALOGS.md`](../../internal/llm/CATALOGS.md) - catalog
  patterns.
