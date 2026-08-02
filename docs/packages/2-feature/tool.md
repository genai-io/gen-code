---
package: github.com/genai-io/san/internal/tool
layer: feature
---

# tool

Registry of built-in tools the agent can call, with their JSON schemas,
permission gate, side-effect plumbing, and per-call dispatch.

## Purpose

Every built-in tool (Bash, Read, Edit, Write, WebFetch, …)
registers into this package's singleton at init time. The agent loop calls
`Execute(name, params, cwd)` to dispatch; the registry resolves the tool,
runs the permission check (`internal/setting`), invokes the tool, and
returns a `toolresult.ToolResult` with stdout, error, side-effect handle,
and audit metadata.

## Contract

Built-in tool registry: tools register at init time; consumers fetch by name and dispatch through the permission gate. The package exposes `*Registry` directly — no Service interface.

```go
package tool

// Registry is the opaque handle. Type exported; fields unexported.
type Registry struct { /* internal fields */ }

func (r *Registry) Register(t Tool)
func (r *Registry) RegisterAlias(alias string, t Tool)
func (r *Registry) Get(name string) (Tool, bool)
func (r *Registry) List() []string
func (r *Registry) Execute(ctx context.Context, name string, params map[string]any, cwd string) toolresult.ToolResult
func (r *Registry) PopSideEffect(toolCallID string) any

// Package-level access
func Initialize(opts Options)
func Default() *Registry
func SetDefaultRegistry(r *Registry)  // test-only
func ResetDefaultRegistry()           // test-only
```


## Internals

- `Registry` (`registry.go`) — name → `Tool` map plus alias map. The
  package-level `defaultRegistry` is the singleton; subpackages call
  `tool.Register(...)` from `init()`.
- Schemas (`schema_base.go`, `schema_agent.go`, `schema_task.go`) — JSON
  schema fragments shared by the built-in tools.
- Permission gate (`perm/`) — wraps tool execution with
  `setting.HasPermissionToUseTool`. Returns deny / ask / allow.
- Side-effect store — `Execute` may return a token; the consumer calls
  `PopSideEffect(token)` to retrieve a queued action (e.g. a pending
  file write).
- Subpackages own their tools: `fs/` for filesystem, `web/` for fetch,
  `agent/` for the Agent tool, `task/` for task tools, `skill/` for skill
  invocation, etc.

## Lifecycle

- Registration: tools register from `init()` in their subpackages.
- `Initialize(Options{})` flips the singleton to `defaultRegistry` —
  before that, `Default()` already returns `defaultRegistry`, so
  `init()`-time registrations are not lost.
- Per-call: `Execute` is goroutine-safe; the registry uses an RWMutex.

## Tests

```
internal/tool/execute_test.go         — dispatch and not-found behavior.
internal/tool/schema_registry_test.go — schema generation, including the Agent tool.
internal/tool/pretool_hook_test.go    — PreToolUse hook outcomes and the permission gate.
```

## See Also

- Code: `internal/tool/`
- Primitive: [`packages/core.md`](../3-core/core.md) (`Tool` and `Tools` interfaces)
- Permission gate: [`packages/setting.md`](setting.md), [`concepts/permission-model.md`](../../concepts/permission-model.md)
- MCP-registered tools: [`packages/mcp.md`](mcp.md)
- Layer: `feature`
