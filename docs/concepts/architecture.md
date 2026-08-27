# Architecture

San is a single-binary terminal AI coding assistant. The runtime is an
event-driven agent loop running inside a Bubble Tea TUI shell, with extension
surfaces for skills, plugins, MCP servers, hooks, slash commands, and
subagents.

This page is the system-level overview. For per-package design see
[`packages/`](../packages/). For dependency rules and package-layer assignments
see [`reference/dependency-rules.md`](../reference/dependency-rules.md) and
[`reference/package-map.md`](../reference/package-map.md).

## Primitives

1. **Agent** — an LLM-in-a-loop with `Inbox` and `Outbox` channels. Contracts
   in `internal/core`; construction in `internal/agent`. Agents communicate
   only through messages, no shared mutable state.
2. **Tools** — built-in capabilities the agent can call. Registered in
   `internal/tool` and gated by `internal/setting` permissions.
3. **Extensions** — skill, plugin, MCP server, hook, slash command, subagent.
   Four extension primitives; *plugin* is one *source* among many — see
   [`concepts/extension-model.md`](extension-model.md).
4. **Sessions** — transcript persistence, resume, fork, projection
   (`internal/session`). Replayable in the inspector
   (`internal/inspector`).
5. **Providers** — every LLM vendor through one adapter
   (`internal/llm/vendor*.go`, over `genai-io/sdk-go`) and search backends
   (`internal/search/{exa,tavily,brave,serper}`).

## Runtime Model

The TUI is a [Bubble Tea](https://github.com/charmbracelet/bubbletea) MVU
loop. Three input sources feed the agent inbox; the agent's outbox produces
events that mutate the TUI model.

```
   Source 1 (User)         Source 2 (Agents)        Source 3 (System)
   submit                  agentDone                cronTick
   slash command           sendMsg                  asyncHook callback
   modal response          selfInject               file watcher
            \                    |                       /
             \___________________|______________________/
                                 |
                          sendToAgent()
                                 v
                  ┌────────────────────────────┐
                  │           Agent            │
                  │   Inbox  →  Run  →  Outbox │
                  │   LLM  ↔  Tool  ↔  LLM ... │
                  └──────────────┬─────────────┘
                                 |
                                 v
                          TUI observation
                  (conv view, permission bridge,
                   token tracker, status line)
```

- Source 1 routes through `internal/app/input/`.
- Source 2 (agents → main) routes through `internal/broker/` (the shared message queue).
- Source 3 routes through `internal/app/trigger/`.
- Output side renders through `internal/app/conv/`.

The detailed walkthrough (sub-model conventions, runtime adapters, cmd
chains, directory structure of `internal/app/`) lives in
[`packages/ui.md`](../packages/1-app/app.md). For the step-by-step trace from a
keystroke (or cron fire, or hub event) through the agent and back to
the terminal, see [`concepts/data-flow.md`](data-flow.md).
For how rendered output is composed (View() layout, Markdown pipeline,
tool blocks, scrollback vs repaint zone), see
[`concepts/rendering.md`](rendering.md).

## Layer Model

Code is divided into five layers with a single allowed dependency direction:

```
cmd  →  app  →  feature  →  core  →  infrastructure
```

| Layer | Members |
| --- | --- |
| `cmd` | `cmd/*` |
| `app` | `internal/app` and subpackages |
| `feature` | Business-domain packages (agent, hook, skill, plugin, mcp, llm, tool, task, subagent, session, command, cron, persona, inspector, search, worktree, setting, reminder, selflearn, image) |
| `core` | `internal/core` |
| `infrastructure` | `internal/{log,secret,filecache,markdown,proc,confdir}` |

Full membership list and allowed-edge rules live in
[`reference/dependency-rules.md`](../reference/dependency-rules.md) and
[`reference/package-map.md`](../reference/package-map.md). Both files are the
source of truth — update them together when a package moves or a new package
is added.

## Where to Read Next

| Want to understand… | Read |
|---|---|
| One specific package | [`packages/<name>.md`](../packages/) |
| A cross-cutting concept (extension model, prompt slots, permissions) | [`concepts/`](./) |
| A fact (slash command list, config field, env var, token limits) | [`reference/`](../reference/) |
| Why a decision was made | [`decisions/`](../design/decisions/) |
| How to accomplish a task | [`guides/`](../guides/) |
| Build, test, release the repo | [`operations/`](../operations/) |

## Change Rule

When a change alters package responsibility, dependency direction, runtime
flow, or extension behavior, update the corresponding document in the same
pull request.

- Adding or moving a top-level package: update
  [`reference/package-map.md`](../reference/package-map.md) and
  [`reference/dependency-rules.md`](../reference/dependency-rules.md).
- Changing a package's public interface: update
  [`packages/<name>.md`](../packages/) Contract section.
- Adding a new cross-cutting concept: write a
  [`concepts/`](./) page; do not bury it inside one package doc.
