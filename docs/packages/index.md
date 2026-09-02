# Packages

One document per Go package under `internal/` (plus the `cmd` entrypoint) that
has non-trivial behavior. Pages are grouped into **rank-numbered layer folders**
so the directory listing mirrors the dependency stack, top → bottom:

```
packages/
├── 0-cmd/             rank 0  entrypoint / wiring
├── 1-app/             rank 1  Bubble Tea TUI
├── 2-feature/         rank 2  domain capabilities
├── 3-core/            rank 3  shared contracts
└── 4-infrastructure/  rank 4  stateless helpers
```

A higher layer may import a lower one; never the reverse (enforced by
`tools/layercheck`). See [`../reference/dependency-rules.md`](../reference/dependency-rules.md)
and [`../reference/package-map.md`](../reference/package-map.md) for the
authoritative layer assignment.

Filenames match the package name with no suffix (`hook.md`, not `hooks.md`).
Translations sit **beside** the source with a `.zh.md` suffix (`hook.zh.md`), so
the layer structure is expressed once and a missing or stale translation is
visible right next to its original. Every page follows [`TEMPLATE.md`](TEMPLATE.md).

## 0 · cmd

| Package | One-liner |
|---|---|
| [`cmd`](0-cmd/cmd.md) | Entrypoint: flag parsing, dependency wiring, provider blank-imports. |

## 1 · app

| Package | One-liner |
|---|---|
| [`app`](1-app/app.md) | Bubble Tea TUI shell, MVU loop, sub-model decomposition. *Seed page; rewrite to TEMPLATE pending.* |

## 2 · feature

| Package | One-liner |
|---|---|
| [`agent`](2-feature/agent.md) | Main agent session lifecycle (Start/Stop/Send/Outbox + permission bridge). |
| [`broker`](2-feature/broker.md) | Routes messages between agents: addresses keyed by agent id, for main↔subagent messages and completions. |
| [`command`](2-feature/command.md) | Slash command registry (builtin + dynamic + custom + plugin-scoped). |
| [`cron`](2-feature/cron.md) | Cron expressions and one-shot scheduling for `/loop` and `/schedule`. |
| [`hook`](2-feature/hook.md) | Pre/post hook engine with command / HTTP / LLM / function executors. |
| [`inspector`](2-feature/inspector.md) | Local web UI for transcript replay; SSE live-tail. |
| [`llm`](2-feature/llm.md) | Provider registry, model store, and the per-turn `*ai.Client` the agent loop streams through. |
| [`mcp`](2-feature/mcp.md) | MCP client + transport + `Caller` for external tool servers. |
| [`plugin`](2-feature/plugin.md) | Plugin loader / installer / marketplace; pushes contributions to other feature packages. |
| [`reminder`](2-feature/reminder.md) | `<system-reminder>` queue with provider re-emission. |
| [`search`](2-feature/search.md) | Pluggable web search backends behind a small `Provider` interface. |
| [`session`](2-feature/session.md) | Transcript persistence, resume, fork, projection. |
| [`setting`](2-feature/setting.md) | Settings loader + central permission decision gate. |
| [`selflearn`](2-feature/selflearn.md) | Background review loop that writes durable memory and agent-created skills. |
| [`skill`](2-feature/skill.md) | Skill loader, state store, active-skills block consumed by the `skills-directory` reminder. |
| [`subagent`](2-feature/subagent.md) | Subagent registry + `Executor` that spawns background `core.Agent` instances. |
| [`task`](2-feature/task.md) | Background task manager (bash and agent tasks). |
| [`tool`](2-feature/tool.md) | Tool registry, schemas, permission gate, side-effect store. |

## 3 · core

| Package | One-liner |
|---|---|
| [`core`](3-core/core.md) | Agent primitive, `System`, `Tools`, `LLM`, `Message` — the stable contracts every feature shares. |

## 4 · infrastructure

| Package | One-liner |
|---|---|
| [`infrastructure`](4-infrastructure/infrastructure.md) | `log` / `secret` / `filecache` / `markdown` / `confdir` / `proc` — stateless helpers documented together. |

## Reference-Shape Pages

Model citizens for what `feature` packages should look like — minimal interface,
concrete return types, no kitchen-sink `Service`:

- [`reminder`](2-feature/reminder.md) — concrete `*Service` struct, small 2-method `Provider` interface.
- [`search`](2-feature/search.md) — pure consumer-defined `Provider`, no singleton.
- [`worktree`](2-feature/worktree.md) — two functions, no types.
