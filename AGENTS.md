# San Agent Guide

This file is the short navigation map for agents and contributors. Keep durable
knowledge in `docs/`; keep this file focused on where to look and what rules to
follow before changing code.

San follows the [AGENTS.md](https://agents.md) standard, so this file is both
the navigation aid for whoever opens the repository and the project
instructions loaded into the running agent's context at session start. Keep it
short: pointers and rules only. Uncommitted personal notes belong in
`AGENTS.local.md` (git-ignored); a subdirectory with its own conventions can
carry its own `AGENTS.md`, and the file nearest the working directory wins.

## Start Here

- Product overview: `README.md`
- Documentation index: `docs/index.md`
- Detailed architecture: `docs/concepts/architecture.md`
- Package map and ownership: `docs/reference/package-map.md`
- Dependency rules: `docs/reference/dependency-rules.md`
- Feature notes: `docs/packages/index.md`
- Development workflow: `docs/operations/development.md`

## Repository Shape

- `cmd/san`: CLI entrypoint and command wiring.
- `internal/app`: Bubble Tea TUI shell, model composition, event routing.
- `internal/core`: stable agent, message, tool, and system-prompt contracts.
- `internal/agent`: agent construction and session-facing runtime setup.
- `internal/llm`: model provider registry, clients, cost and logging helpers.
- `internal/tool`: built-in tool registry, schemas, adapters, and executors.
- `internal/session`: transcript persistence, projection, metadata, resume.
- `internal/task`, `internal/subagent`, `internal/cron`: background work and orchestration.
- `internal/command`, `internal/skill`, `internal/plugin`, `internal/mcp`, `internal/hook`: extension surfaces.
- `internal/setting`, `internal/persona`: configuration and persona overlays.
- `internal/log`, `internal/secret`, `internal/filecache`, `internal/markdown`, `internal/confdir`: infrastructure helpers.
- `internal/selflearn`: background memory and skill review loop.
- `docs`: durable explanations, design decisions, operations, and references.

## Rules

Before editing internal packages, read:

- `docs/reference/dependency-rules.md` — allowed import directions and the
  rule for each layer.
- `docs/design/principles.md` — coding principles for package structure,
  interfaces, tests, and context handling.

Update those files when the rules change. Do not duplicate them here.

## Common Commands

See `docs/operations/development.md` for build / test / lint / format
and the sandbox-friendly `GOCACHE` workaround. Update that file when
commands change. Do not duplicate them here.

## Documentation Rules

- Add or update docs in the same change as architecture or workflow changes.
- Each feature document should list purpose, entrypoints, core packages, flow,
  configuration, tests, and common pitfalls.
- Architecture decision records live in `docs/design/decisions/`.
- File naming rules live in `docs/reference/file-naming.md`.
- Active plans live in `notes/active/`; completed plans move to
  `notes/completed/`.
