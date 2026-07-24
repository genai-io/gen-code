# MiniMax Provider Integration Design

## Goal

Integrate MiniMax into `san` as a first-class LLM provider with:

- runtime provider selection through `/provider` and `/models`
- stable streaming, thinking, and tool-call support
- correct token usage capture
- safe model limits for compaction and `max_tokens`

## Recommendation

Use **MiniMax Anthropic-compatible API** as the primary integration path.

Why:

1. MiniMax's own docs recommend the Anthropic-compatible path for text models.
2. `san` already has a mature Anthropic provider with:
   - thinking block streaming
   - tool use / tool result round-trip
   - prompt-cache usage fields
   - stable tool ID sanitization
3. The existing OpenAI-compatible path in `san` currently expects reasoning in `reasoning_content`, while MiniMax documents `reasoning_details` or inline `<think>` content. That path needs extra compatibility work.

## Proposed Shape

Add a new provider named `minmax`.

Environment:

- `MINIMAX_API_KEY`
- `MINIMAX_BASE_URL` optional, default `https://api.minimaxi.com/anthropic`

Provider metadata:

- provider name: `minmax`
- auth method: `api_key`
- display name: `MiniMax`

Default model:

- `MiniMax-M2.7`

Initial model catalog:

- `MiniMax-M2.7`
- `MiniMax-M2.7-highspeed`
- `MiniMax-M2.5`
- `MiniMax-M2.5-highspeed`
- `MiniMax-M2.1`
- `MiniMax-M2.1-highspeed`
- `MiniMax-M2`

## Implementation Plan

### 1. Add provider identity

Touch points:

- `internal/llm/types.go`
- `internal/app/input/on_provider.go`
- `internal/setting/client_options.go`
- `cmd/san/main.go`

Changes:

- add `llm.MinMax Name = "minmax"`
- add provider display label and ordering
- add default model mapping
- import the new provider package in `cmd/san/main.go`

### 2. Add `internal/llm/minmax`

Files:

- `internal/llm/minmax/apikey.go`
- `internal/llm/minmax/client.go`

Recommended implementation:

- build an `anthropic.Client` with `option.WithAPIKey(secret.Resolve("MINIMAX_API_KEY"))`
- set `option.WithBaseURL(MINIMAX_BASE_URL)` with default `https://api.minimaxi.com/anthropic`
- reuse the existing Anthropic streaming logic instead of creating a new protocol adapter

The cleanest approach is to wrap or embed the existing Anthropic client implementation rather than reimplement stream parsing.

### 3. Model listing strategy

Do **not** rely on MiniMax exposing Anthropic's `/models` endpoint.

Use:

1. static model list first
2. optional dynamic fetch later if confirmed supported

Reason:

- current `internal/llm/anthropic/client.go` fetches models from the Anthropic Models API
- MiniMax docs clearly document supported text models, but do not clearly promise a compatible model-list endpoint
- a static list is enough for `/models` and avoids startup failures

## Token Limits

This is the highest-risk area.

MiniMax docs currently show:

- **context window** for supported text models: `204,800`
- **OpenAI-compatible `max_completion_tokens` upper bound** on the documented chat endpoint: `2048`

That means `san` should **not** assume output limit equals context window.

### Safe initial limits

Set static model limits to:

- `InputTokenLimit = 204800`
- `OutputTokenLimit = 8192`

Reason:

- `san` falls back to `8192` output tokens when a provider does not specify a limit
- this project now intentionally keeps MiniMax's default output limit at `8192`

If runtime verification shows MiniMax's Anthropic-compatible endpoint needs a smaller bound, reduce the static limit then.

## Token Usage Handling

### What already works well

`san` already has usage fields that align with MiniMax's Anthropic-compatible docs:

- `input_tokens`
- `output_tokens`
- `cache_creation_input_tokens`
- `cache_read_input_tokens`

Relevant code:

- `internal/llm/types.go`
- `internal/llm/stream/stream.go`
- `internal/llm/anthropic/client.go`

### Current gap

Session-level accumulation only tracks:

- input tokens
- output tokens

It does **not** accumulate:

- cache creation input tokens
- cache read input tokens

Relevant code:

- `internal/llm/llm.go`
- `internal/core/llm.go`

### Recommended fix

Extend session token accounting so MiniMax prompt-cache usage is not lost.

Suggested changes:

1. add cache counters to `llm.TokenUsage`
2. update `Client.AddUsage` to accumulate cache creation and cache read counts
3. optionally extend `core.InferResponse` if UI or pricing needs these fields directly
4. keep existing `TokensIn` and `TokensOut` semantics unchanged for compatibility

### Pricing note

If cost tracking is later implemented for MiniMax, billable usage should be computed separately for:

- input tokens
- output tokens
- cache write tokens
- cache read tokens

Do not fold cache tokens into normal input tokens for pricing.

## Thinking / Reasoning

MiniMax Anthropic-compatible docs are slightly inconsistent:

- one section marks `thinking` as supported
- a note later says some Anthropic parameters such as `thinking` may be ignored

Practical design:

- keep `san`'s existing Anthropic thinking path enabled
- treat missing thinking blocks as acceptable behavior
- do not make correctness depend on thinking being returned

This means:

- thinking can remain a best-effort capability
- tool use and final text must continue to work when no thinking blocks arrive

## Prompt Caching

MiniMax documents prompt caching on the Anthropic-compatible path and returns:

- `cache_creation_input_tokens`
- `cache_read_input_tokens`

`san` already marks the system prompt as ephemeral in the Anthropic provider path, so MiniMax can benefit from this with little or no additional protocol work.

This is another reason to prefer Anthropic-compatible integration over OpenAI-compatible integration.

## Why Not OpenAI-Compatible First

OpenAI-compatible MiniMax is still a valid fallback, but it is a worse first integration for `san`.

Problems to solve there:

1. MiniMax documents `reasoning_split=True` and `reasoning_details`, but `san`'s OpenAI-compatible layer extracts `reasoning_content`.
2. MiniMax requires preserving the full assistant message during tool-calling, including thought content or separated reasoning data.
3. Prompt-cache usage is documented on the Anthropic-compatible path, not the OpenAI-compatible path.

If an OpenAI-compatible MiniMax provider is added later, it should be a separate provider mode or internal fallback, not the first implementation.

## Test Plan

### Unit tests

- provider registration exposes `minmax:api_key`
- `ListModels` returns the static MiniMax list
- `ResolveMaxTokens` returns `2048` for MiniMax models by default
- token accumulation includes cache read/write usage
- provider selector shows `MiniMax`

### Integration checks

1. connect with `/provider`
2. select `MiniMax-M2.7`
3. send a plain text turn
4. send a tool-using turn
5. confirm thinking blocks do not break the stream when present
6. confirm no failure when thinking blocks are absent
7. verify usage values are logged and session totals update

## Concrete File List

- `internal/llm/types.go`
- `internal/llm/minmax/apikey.go`
- `internal/llm/minmax/client.go`
- `internal/app/input/on_provider.go`
- `internal/app/input/on_provider_test.go`
- `internal/setting/client_options.go`
- `cmd/san/main.go`
- `internal/llm/llm.go`
- `internal/core/llm.go` if cache-token fields are surfaced to UI

## Sources

- API overview: `https://platform.minimaxi.com/docs/api-reference/api-overview`
- Anthropic-compatible text API: `https://platform.minimaxi.com/docs/api-reference/text-anthropic-api`
- OpenAI-compatible text API: `https://platform.minimaxi.com/docs/api-reference/text-openai-api`
- OpenAI-compatible chat endpoint: `https://platform.minimaxi.com/docs/api-reference/text-chat-openai`
- Anthropic prompt caching: `https://platform.minimaxi.com/docs/api-reference/anthropic-api-compatible-cache`
