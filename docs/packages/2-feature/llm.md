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
- Provider subpackages: `anthropic/`, `openai/`, `google/`, `moonshot/`,
  `alibaba/`, `bigmodel/`, `minmax/`, `mimo/`, `deepseek/`, `ollama/`, `sensenova/`, `volcengine/`, `agnesai/`, `openaicompat/`.

## Lifecycle

- Construction: `Initialize(Options{})` loads `~/.san/providers.json`,
  picks the last-used provider (or the first connectable one), and stores
  it.
- Switching: `/models` slash command calls `SetCurrentModel` + reload.
- Per-call: `NewClient(model, maxTokens)` produces a `*Client` for one
  inference; the client wraps `Provider.Infer`.

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
