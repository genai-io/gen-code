---
package: github.com/genai-io/san/internal/llm
layer: feature
---

# llm

Provider registry, model store, and active-connection handle for every LLM
backend (Anthropic, OpenAI, GitHub Copilot, Google, Moonshot, Alibaba, MiniMax,
Z.ai/GLM, DeepSeek, Ollama, SenseNova, Volcengine Ark, Xiaomi MiMo, Agnes-AI,
plus a user-defined OpenAI-compatible endpoint). Reaching them is one adapter,
the `vendor_*` files, over
[`genai-io/sdk-go`](https://github.com/genai-io/sdk-go); adding a vendor is a
row in `vendor_table.go`, not a package.

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

// Package-level access
func Initialize()
func Default() *Conn
```


## Internals

One file per subject:

```
provider.go    the contract: Provider, the types crossing it, the optional extensions
effort.go      which reasoning rung applies to a model, and cycling between them
registry.go    every provider/auth pair San can open, and its connection status
auth.go        interactive (OAuth) sign-in, as the registry sees it
conn.go        the package-level *Conn, provider resolution, the cross-vendor pool
client.go      Client: a Provider plus a model, as core.LLM
store.go       providers.json — what the user connected and chose
modelcache.go  the cached listings, and the context window resolved from them
cost.go        Money, the multi-currency total, and per-vendor pricing
errors.go      a provider failure, tagged for the agent loop
logging.go     CompletionOptions, as the log package reads it
vendor*.go     every vendor, over genai-io/sdk-go
```

The test double lives outside the binary, in
`tests/integration/testutil.FakeProvider`.

Worth knowing beyond the names:

- **One identity string.** `providerKey(provider, auth)` builds the
  `"vendor:auth_method"` form that the registry keys entries by, the store keys
  cached listings by, and a `Provider` reports as its own `Name()`. One
  function, so the three cannot drift.
- **`errors.go` translates rather than classifies.** The SDK decides a
  failure's kind from the provider's typed error, which is the only place that
  decision is reliable; San maps that kind onto `core.RetryableError` and
  `core.ContextExceededError`, the two things the agent loop reads. San keeps
  no second copy of the SDK's tables.
- **`modelcache.go` owns the window.** The status bar's percentage and the
  agent's auto-compaction trigger are the same number, so both resolve it
  through `EffectiveInputLimit` — env override, then the user's `/tokenlimit`,
  then this provider's cache, then the largest figure cached anywhere for the
  ID. Issue #338 was the two disagreeing.
- **`ModelInfo.Reasoning`** carries live supported/default effort values when a
  provider advertises them; `effort.go` prefers that metadata and falls back to
  `ThinkingEffortProvider` for catalogs (such as the standard OpenAI
  `/v1/models` response) that omit reasoning capabilities.
- **`vendor*.go` is one adapter, not one package per vendor.** The wire
  protocols, their streaming shapes and their reasoning dialects belong to the
  SDK's drivers; what stays here is the seam — `Provider` on one side,
  `ai.Client` on the other — plus the table saying which San provider is which
  catalog vendor. `vendor.go` names the file each subject lives in.

## Lifecycle

- Construction: `Initialize()` loads `~/.san/providers.json`,
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

`ModelInfo` carries the rest of a catalog row as facts — `Pricing`, `Stage`,
`Replacement`, `AcceptsImages`, `RejectsTools` — never as a rendering of them.
The `/models` picker aligns the figures into columns and writes the labels
itself (`modelLabels`), because prose stored in the cache would fix the wording
in a file that outlives the release that wrote it, and because the `·` it
separates with is East Asian Ambiguous: a string measured anywhere but where it
is drawn measures wrong.

## Tests

```
internal/llm/client_test.go   — Client.Infer plumbing and its failure paths.
internal/llm/errors_test.go   — what the agent loop is told about a failure.
internal/llm/provider_test.go — the optional-extension defaults.
internal/llm/store_test.go    — provider config persistence.
internal/llm/cost_test.go     — pricing dispatch and the multi-currency total.
internal/llm/vendor_test.go   — the vendor seam, against stub endpoints.
internal/llm/vendor_live_test.go — one real turn per configured vendor,
                                opt-in via SAN_SDK_LIVE.
```

## See Also

- Code: `internal/llm/`
- Primitive: [`packages/core.md`](../3-core/core.md) (`LLM` interface)
- Cost tracking surfaced via [`packages/session.md`](session.md) recorder.
- Layer: `feature`
