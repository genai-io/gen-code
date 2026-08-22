# Adding a Provider

A provider is a row in a table. There is no package to write, no client, no
streaming loop and no conversion code — those belong to
[`genai-io/sdk-go`](https://github.com/genai-io/sdk-go), which speaks four wire
protocols and covers every vendor San reaches through
the `vendor_*` files in
[`internal/llm`](../packages/2-feature/llm.md).

Most vendors need no code at all, because most of them ship an endpoint
speaking somebody else's protocol: OpenAI Chat Completions covers the majority,
and MiniMax, Xiaomi MiMo and Volcengine Ark speak Anthropic Messages.

## Is the vendor already in the SDK's catalog?

```sh
go doc github.com/genai-io/sdk-go/pkg/ai/catalog Models
```

If it is, adding it to San is one entry in `vendorEntries` in
`internal/llm/vendor_table.go`:

```go
{
	meta:     llm.Meta{Provider: llm.DeepSeek, AuthMethod: llm.AuthAPIKey,
		EnvVars: []string{"DEEPSEEK_API_KEY"}, DisplayName: "Direct API"},
	vendorID: "deepseek",
},
```

Plus a row in `vendorDisplays` for the provider's name and sort order, and a constant
in `internal/llm/provider.go` if the provider name is new. That is the whole of it:
the credential comes from San's secret store through the vendor's own
`KeyEnv`, the host from its `BaseURL`, and the models, windows, prices and
reasoning ladder from its catalog row.

An entry needing more than a key and a host sets `configure`, which is where
the Vertex deployment, the Coding Plan path and the interactive sign-ins live.

## Is it not in the catalog?

Add it there first — it is a row in `pkg/ai/catalog/vendors.go`, stating the
base URL, the environment variable the vendor documents, its reasoning dialect
and its models. See the SDK's own
[contributing guide](https://github.com/genai-io/sdk-go/blob/main/CONTRIBUTING.md).

Two things are worth getting right, because both fail silently:

- **The context window.** A model that reaches a picker without one switches
  off the context percentage and auto-compaction, and says nothing. State it in
  the row; where the vendor encodes it in the model ID instead, read it there
  with `Vendor.Infer`. Never substitute a guess — San treats 0 as "unknown"
  and recovers, but a wrong number is acted on.
- **Whether the endpoint takes its own reasoning back.** An
  OpenAI-compatible endpoint that emits `reasoning_content` and does not accept
  it in history cannot have a thinking turn replayed at all, which ends the
  conversation on the second turn. `OpenAIChatCompat.ReasoningContent` is what
  says it does.

## Does it speak a protocol nobody implements?

Then it is a new driver package in the SDK, not in San. The driver interface is
one required method and two optional ones; the SDK's contributing guide walks
through it.

## Interactive sign-in

A vendor that authenticates a person rather than a service — GitHub Copilot, a
ChatGPT subscription — runs its grant through `pkg/ai/auth` and keeps the
credential in San's own secret store, so a login survives an upgrade. See
`vendor_signin.go` and `vendor_credentials.go`: the flow belongs to the SDK,
the storage to San.

## Tests

`internal/llm/vendor_test.go` drives the seam against stub endpoints — what
reached the wire, and what came back. `vendor_live_test.go` runs one real turn
per configured vendor and is opt-in:

```sh
SAN_SDK_LIVE=1 go test ./internal/llm/ -run TestLive -v
```

## See Also

- [`packages/llm.md`](../packages/2-feature/llm.md) — the package design.
- [SDK reference](https://pkg.go.dev/github.com/genai-io/sdk-go/pkg/ai).
