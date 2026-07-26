# Package Map

This map describes current package ownership and the layer each package
belongs to. It should match the code under `cmd/` and `internal/`. Layer
names are defined in `dependency-rules.md` — update both files together when
the assignment changes.

## Entrypoints

| Path | Layer | Responsibility |
| --- | --- | --- |
| `cmd/san` | `cmd` | Main CLI binary. Parses flags, initializes runtime, wires subcommands. |

## Application Shell

| Path | Layer | Responsibility |
| --- | --- | --- |
| `internal/app` | `app` | Bubble Tea root model, service composition, event routing, session restore, app lifecycle. |
| `internal/app/conv` | `app` | Conversation rendering state and agent outbox observation. |
| `internal/app/desktop` | `app` | Alt-screen full-screen surface: scrollable transcript viewport above the input strip. |
| `internal/app/input` | `app` | Text input, selectors, approvals, user-input flow. |
| `internal/app/trigger` | `app` | System triggers such as cron and async hook polling. |
| `internal/broker` | `feature` | Routes messages between agents: addresses keyed by agent id, for main↔subagent messages and completions. |
| `internal/app/kit` | `app` | TUI support helpers shared by app subpackages. |

## Core Contracts

| Path | Layer | Responsibility |
| --- | --- | --- |
| `internal/core` | `core` | Shared contracts for messages, tools, agents, sections, and system prompts. |

## Feature Packages

Agent, persistence, and orchestration:

| Path | Layer | Responsibility |
| --- | --- | --- |
| `internal/agent` | `feature` | Agent construction, permission adapter, and session-facing setup. |
| `internal/llm` | `feature` | LLM service, provider registry, provider setup, cost tracking, logging. |
| `internal/tool` | `feature` | Built-in tool schemas, registry, adapters, permission checks, execution. |
| `internal/session` | `feature` | Session metadata, transcript persistence, resume, projection, message conversion. |
| `internal/session/transcript` | `feature` | Transcript records, filesystem store, projection, renderable views. |
| `internal/task` | `feature` | Background task management, bash and agent task execution, output storage. |
| `internal/todo` | `feature` | Agent to-do list state and background tracker service (lifted from `task/tracker`). |
| `internal/subagent` | `feature` | Subagent registry, loading, matching, execution, storage, progress tools. |
| `internal/cron` | `feature` | Cron definitions, storage, service, loop. |
| `internal/selflearn` | `feature` | Background self-learning reviewer, durable memory store, and restricted skill-update surface. |

Extension surfaces:

| Path | Layer | Responsibility |
| --- | --- | --- |
| `internal/command` | `feature` | Slash command registry, built-ins, Markdown command loading. |
| `internal/skill` | `feature` | Skill registry, loading, lazy loading metadata. |
| `internal/plugin` | `feature` | Plugin compatibility, loading, installation, marketplace, integration. |
| `internal/mcp` | `feature` | MCP config, client, registry, caller, hook integration. |
| `internal/mcp/transport` | `feature` | MCP transport implementations. |
| `internal/hook` | `feature` | Hook registry, matcher, engine, executors, store. |

Configuration and supporting capabilities:

| Path | Layer | Responsibility |
| --- | --- | --- |
| `internal/setting` | `feature` | Settings loading, merge, permissions, operation mode, workdir, env. |
| `internal/persona` | `feature` | Persona registry (system-prompt parts, skills, settings overlay), template, paths. |
| `internal/search` | `feature` | Search provider implementations and factory. |
| `internal/inspector` | `feature` | Transcript inspector server, replay, stream, embedded UI. |
| `internal/reminder` | `feature` | Runtime reminder queue and provider integration. |
| `internal/reviewer` | `feature` | Auto-review LLM judge for gray-zone permissions and bash prompts. |
| `internal/image` | `feature` | Image handling + `core.Image` adapter; provisional — see note in `dependency-rules.md`. |

## Infrastructure Helpers

| Path | Layer | Responsibility |
| --- | --- | --- |
| `internal/log` | `infrastructure` | Logging, request/response logs, development log paths. |
| `internal/secret` | `infrastructure` | Secret storage helpers. |
| `internal/filecache` | `infrastructure` | File restore/cache helpers. |
| `internal/markdown` | `infrastructure` | Markdown frontmatter parsing. |
| `internal/proc` | `infrastructure` | Cross-platform process-group / signal helpers (Unix/Windows variants). |
| `internal/confdir` | `infrastructure` | Shared `.san` configuration directory naming helper. |
| `internal/atomicfile` | `infrastructure` | Temp-file-then-rename file replacement, so a reader never sees a half-written file. |

## Ownership Rule

New behavior should live in the package that owns the capability. Add a new
top-level `internal/` package only when the behavior has a distinct lifecycle,
state model, or dependency boundary that does not fit an existing package.
When adding a package, assign it a layer in this map and confirm the imports
match `dependency-rules.md`.
