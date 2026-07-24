---
package: github.com/genai-io/san/internal/command
layer: feature
---

# command

Registry for slash commands: built-in handlers (`/help`, `/models`,
`/identity`, …), dynamic provider-supplied entries, builtin prompt
commands embedded in the binary (`/simplify`), and user-defined markdown
commands under `commands/` directories. Resolves names, fuzzy prefixes,
and plugin-scoped command paths; a user or project command shadows a
builtin prompt command of the same name.

## Purpose

Slash commands are the user-input side of the TUI's command palette. This
package owns the unified lookup surface: `Get("/help")`, `List()`,
fuzzy-prefix matching for the autocompleter, and the registry of custom
commands loaded from disk.

## Contract

Slash command registry. Combines built-in handlers, dynamic providers (skill/agent surfaces), and custom markdown commands. The package exposes `*Registry` directly — no Service interface.

```go
package command

// Registry is the opaque handle. Type exported; fields unexported.
type Registry struct { /* internal fields */ }

func (s *Registry) Get(name string) (Info, bool)
func (s *Registry) List() []Info
func (s *Registry) ListCustom() []CustomCommand
func (s *Registry) GetMatching(prefix string) []Info
func (s *Registry) IsCustomCommand(cmd string) (*CustomCommand, bool)
func (s *Registry) BuiltinNames() map[string]Info
func (s *Registry) GetCustomCommands() []Info

// Package-level access
func Initialize(opts Options)
func Default() *Registry
func SetDefaultRegistry(s *Registry)  // test-only
func ResetDefaultRegistry()          // test-only
```


## Internals

- `service` (`service.go`) — concrete implementation, holds cwd, dynamic
  provider functions, and a plugin-command-path callback.
- `registry.go` — combines three sources at query time:
  - **Built-ins** registered from `builtin/` subpackage at init.
  - **Dynamic** — `DynamicProviders` callbacks returning `Info` slices
    (used by skill/agent slash-command surfaces).
  - **Custom** — markdown files under `~/.san/commands/` and
    `<project>/.san/commands/`, plus plugin-scoped paths returned by
    `PluginCommandPaths`.
- `Info` carries name, description, namespace, source path.

## Lifecycle

- Construction: `Initialize(Options{CWD, DynamicProviders, PluginCommandPaths})`
  at app startup, after `skill` and `subagent` are initialized so their
  dynamic providers are wired in.
- Reload on plugin reload: callers re-call `Initialize` with refreshed
  provider closures.

## Tests

```
internal/command/registry_test.go    — name lookup, fuzzy matching,
                                        custom + built-in precedence.
```

## See Also

- Code: `internal/command/`
- Related: [`packages/skill.md`](skill.md), [`packages/subagent.md`](subagent.md) (dynamic providers), [`packages/plugin.md`](plugin.md)
- Reference: [`reference/slash-commands.md`](../../reference/slash-commands.md)
- Layer: `feature`
