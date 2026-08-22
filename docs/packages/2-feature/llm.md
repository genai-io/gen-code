---
package: github.com/genai-io/san/internal/llm
layer: feature
---

# llm

Provider registry, model store, and active-connection handle for every LLM
backend (Anthropic, OpenAI, GitHub Copilot, Google, Moonshot, Alibaba, MiniMax,
Z.ai/GLM, DeepSeek, Ollama, SenseNova, Volcengine Ark, Xiaomi MiMo, Agnes-AI,
plus a user-defined OpenAI-compatible endpoint). Reaching them is one adapter,
`internal/llm/sdk`, over [`genai-io/sdk-go`](https://github.com/genai-io/sdk-go);
adding a vendor is a row in its table, not a package.

## Purpose

The agent loop talks to LLMs through `core.LLM` (see
[`packages/core.md`](../3-core/core.md)). This package owns the *machinery around*
that contract — discovering providers, persisting the user's chosen
provider/model, switching between them at runtime, and tracking cost and
streaming details for each call.

## Contract

`*Conn` is the handle to the active LLM: the connected Provider, the current
model, and the Store of available providers/models — all under one mutex. The
package exposes `*Conn` directly — no Service interface, no wrapper type.

```go
package llm

// Conn is the opaque handle. Type exported; fields unexported (every
// accessor is mutex-protected).
type Conn struct { /* internal fields */ }

func (c *Conn) Provider() Provider
func (c *Conn) SetProvider(p Provider)
func (c *Conn) ModelID() string
func (c *Conn) CurrentModel() *CurrentModelInfo
func (c *Conn) SetCurrentModel(info *CurrentModelInfo)
func (c *Conn) NewClient(model string, maxTokens int) *Client
func (c *Conn) Store() *Store
func (c *Conn) ListProviders() map[Name][]Info

// Package-level access
func Initialize(opts Options)
func Default() *Conn
func SetDefaultConn(c *Conn)  // test-only
func ResetDefaultConn()       // test-only
```


## Internals

- `Conn` (`service.go`) — the package-level singleton: one mutex guarding the
  current Provider/Model + Store.
- `Provider` registry (`registry.go`) — discovery, dynamic model list
  fetching (per memory: prefer `/models` over hardcoded catalogs).
- `Client` (consolidated `Infer` path) — adapts a `Provider` + model into
  `core.LLM`, tracks per-call token counts, streams `core.Chunk`, applies
  retry/cost logic via `logging.go` and `money.go`.
- `Store` (`store.go`) — persists user's provider connections under
  `~/.san/providers.json`; tracks current model and caches provider-scoped
  model metadata. `ModelInfo.Reasoning` carries live supported/default effort
  values when a provider advertises them; application resolution prefers that
  metadata and falls back to `ThinkingEffortProvider` for catalogs (such as the
  standard OpenAI `/v1/models` response) that omit reasoning capabilities.
- `sdk/` — every vendor, served through
  [`genai-io/sdk-go`](https://github.com/genai-io/sdk-go). It is one adapter,
  not one package per vendor: the wire protocols, their streaming shapes and
  their reasoning dialects belong to the SDK's drivers, and what stays here is
  the seam — `llm.Provider` on one side, `ai.Client` on the other, plus the
  table saying which San provider is which catalog vendor.

## Lifecycle

- Construction: `Initialize(Options{})` loads `~/.san/providers.json`,
  picks the last-used provider (or the first connectable one), and stores
  it.
- Switching: `/models` slash command calls `SetCurrentModel` + reload.
- Per-call: `NewClient(model, maxTokens)` produces a `*Client` for one
  inference; the client wraps `Provider.Infer`.

## Model catalogs

The catalog is the SDK's — `ai/catalog`, one row per vendor, carrying endpoints,
windows, prices, reasoning ladders and per-endpoint quirks as data. It is a
fallback, not an authority: most OpenAI-compatible vendors return the bare
`id`/`object`/`owned_by` shape and publish limits only in their docs, so the
live listing wins on every field it states and the catalog fills the rest.

Three rules keep the fallback honest:

- An unrecognised model reports **0**, not a blanket default. San treats 0 as
  "window unknown" and skips proactive compaction, which is recoverable; a
  guessed window is acted on silently and is wrong in both directions —
  guessing low burns context on every compaction, guessing high never fires.
- A vendor that encodes the window in the model ID reads it from there rather
  than reporting nothing, which is what `catalog.Vendor.Infer` is for. A model
  that reaches a picker with no window is a defect, and a test asserts it.
- Each vendor records the date its figures were last checked against the
  vendor's documentation, because a stale window or price reads exactly like a
  fresh one; `catalog.Stale` reports the ones that have aged out.

Model Studio is the exception that proves the shape: it publishes no window for
any of its hundreds of models and answers per model instead, which is what
`llm.ModelLimitsFetcher` exists for.

## Tests

```
internal/llm/llm_test.go        — Client.Infer plumbing.
internal/llm/store_test.go      — provider config persistence.
internal/llm/fake_llm.go        — test double consumed by other packages.
internal/llm/sdk/sdk_test.go    — the seam, against stub endpoints.
internal/llm/sdk/live_test.go   — one real turn per configured vendor,
                                  opt-in via SAN_SDK_LIVE.
```

## See Also

- Code: `internal/llm/`
- Primitive: [`packages/core.md`](../3-core/core.md) (`LLM` interface)
- Cost tracking surfaced via [`packages/session.md`](session.md) recorder.
- Layer: `feature`
