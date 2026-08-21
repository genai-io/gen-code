---
package: github.com/genai-io/san/internal/llm
layer: feature
---

# llm

Provider registry, model store, and active-connection handle for every LLM backend
(Anthropic, OpenAI, Google, Moonshot, Alibaba, MiniMax, Z.ai/GLM, DeepSeek,
Ollama, SenseNova, Volcengine Ark, Agnes-AI, plus the generic openai-compat shim). Provider implementations live in
`internal/llm/<name>/` subpackages.

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
- `stream/` — provider-side helpers for SSE parsing.
- Thinking is configured two different ways on the Anthropic protocol, and
  which one a model wants is catalog data, not something to infer from the
  model ID. Claude 4.6 and later take `thinking: {"type": "adaptive"}` with the
  level in `output_config.effort`; everything older — and every
  Anthropic-compatible third-party endpoint (MiniMax, Xiaomi MiMo, Volcengine
  Ark), which implements only the older shape — takes
  `thinking: {"type": "enabled", "budget_tokens": N}`. From Opus 4.7 on, a
  budget is rejected with a 400 rather than merely deprecated, so the two are
  not interchangeable. `anthropic/catalog.go` records the style per model and
  defaults an unknown model to the budget shape, which is what keeps the
  third-party endpoints working.
- Provider subpackages: `anthropic/`, `openai/`, `google/`, `moonshot/`,
  `alibaba/`, `bigmodel/`, `minmax/`, `mimo/`, `deepseek/`, `ollama/`, `sensenova/`, `volcengine/`, `agnesai/`, `openaicompat/`.

## Lifecycle

- Construction: `Initialize(Options{})` loads `~/.san/providers.json`,
  picks the last-used provider (or the first connectable one), and stores
  it.
- Switching: `/models` slash command calls `SetCurrentModel` + reload.
- Per-call: `NewClient(model, maxTokens)` produces a `*Client` for one
  inference; the client wraps `Provider.Infer`.

## Model catalogs

Provider subpackages carry a small static catalog used as a fallback where the
endpoint's `/models` response omits context windows — most OpenAI-compatible
vendors return the bare `id`/`object`/`owned_by` shape and publish limits only
in their docs. The live listing stays authoritative; the catalog fills gaps.

Two rules keep the fallback honest:

- An unrecognised model reports **0**, not a blanket default. San treats 0 as
  "window unknown" and skips proactive compaction, which is recoverable; a
  guessed window is acted on silently and is wrong in both directions —
  guessing low burns context on every compaction, guessing high never fires.
- Each catalog records the date its figures were last checked against the
  vendor's documentation, because a stale window or price reads exactly like a
  fresh one.

Catalogs were last verified against vendor documentation on **2026-08-20**.

## Tests

```
internal/llm/llm_test.go        — Client.Infer plumbing.
internal/llm/store_test.go      — provider config persistence.
internal/llm/fake_llm.go        — test double consumed by other packages.
```

## See Also

- Code: `internal/llm/`
- Primitive: [`packages/core.md`](../3-core/core.md) (`LLM` interface)
- Cost tracking surfaced via [`packages/session.md`](session.md) recorder.
- Layer: `feature`
