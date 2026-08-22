# Documentation Index

This directory is the durable knowledge base for San. Keep `README.md`
concise, use `AGENTS.md` as the short navigation map, and put lasting
explanations here.

## Primary Entrypoints

- `../README.md` — product overview, installation, usage.
- `../AGENTS.md` — short agent and contributor navigation guide.
- `concepts/architecture.md` — system-level overview and primitives.
- `packages/index.md` — per-package design pages (grouped by layer).
- `concepts/index.md` · `guides/index.md` · `reference/index.md` ·
  `operations/index.md` — each category's own index.

## By Reader Goal

- `concepts/architecture.md` — primitives, runtime model, layer model.
- `concepts/index.md` — cross-cutting concepts that span multiple packages
  (data flow, rendering, extension model, harness channels, permission
  model).
- `packages/index.md` — one page per Go package, grouped by layer; each has
  a Contract section with the package's public Go interface.

### Look up a fact

- `reference/index.md` — full reference index. Common lookups:
- `reference/slash-commands.md` — all slash commands.
- `reference/configuration.md` — config files and field reference.
- `reference/dependency-rules.md` — layer / import rules.
- `reference/package-map.md` — package-to-layer assignment.
- `reference/claude-permission-compat.md` — Claude-Code-compatible
  permission rule syntax.
- `reference/token-limits.md`, `reference/cost-tracking.md`,
  `reference/cli-startup.md`, `reference/loop.md`,
  `reference/file-naming.md`.

- `guides/index.md` — task how-tos: getting started, the inspector, explore
  mode, and writing skills / subagents / plugins / providers.
- `operations/index.md` — build, test, release, troubleshoot, benchmark, and
  the small-footprint rationale.

### Know why a decision was made

- `design/principles.md` — engineering principles for structure and docs.
- `design/decisions/` — architecture decision records (ADRs).

## Work-in-Progress Plans

Plans live at the repo root under `notes/` (not in `docs/`):

- `notes/active/` — active restructuring or feature plans.
- `notes/completed/` — completed plans kept for historical context.
- `notes/tech-debt.md` — known structural debt and follow-up candidates.

## Update Policy

When code changes, update the relevant package page in `packages/` in the
same pull request. When a new top-level package is added or a package
moves, update `reference/package-map.md` and `reference/dependency-rules.md`
together. New cross-cutting concept ⇒ a page in `concepts/`. New decision
⇒ an ADR in `design/decisions/`.
