# Harness Channels

San delivers context to the model through **three distinct channels**,
each with different cache, lifecycle, and stability properties:

| Channel | What lives there | Cache-friendly? | Mutable mid-session? |
|---|---|---|---|
| **System prompt** | Identity, output style, engineering defaults, policy, guidelines, environment footer. Slot-sectioned. | Yes — invariant per session unless a section mutates. | Yes (Use/Drop), but expensive (cache miss). |
| **`<system-reminder>` blocks** | Session-level / project-level dynamic content: **active-skills directory**, AGENTS.md memory, one-time notices. Attached below the next user message. | Yes — once attached, the user message is immutable. | No (re-emitted as new attachments, never mutated). |
| **User messages** | The actual prompt the user typed. | Yes — already cached. | No. |

> The active-skills list (what skills the model is currently aware of) used
> to live in the system prompt. It now rides on user messages as a
> `<system-reminder source="skills-directory">` block — toggling a skill
> no longer busts the prompt cache.

The harness chooses which channel to use based on **how often the content
changes** and **whether the LLM's prompt cache should survive the
change**.

## Why Three Channels?

The LLM's prompt cache works on **exact prefix match**. Anything in the
system prompt that mutates invalidates the cache prefix from that point
onward — so frequent system-prompt edits are expensive.

The harness optimizes:

- **System prompt** = "things true for every turn of this session".
  Identity, policy, communication style, engineering defaults, tool-
  usage guidelines, environment. Mutates rarely. (Tool *schemas* are
  passed separately via the LLM API's `tools` parameter, not in the
  system prompt text.)
- **`<system-reminder>` blocks** = "things true now, but may change". Each
  reminder is attached to a *user message* (not the system prompt) and
  re-emitted on session start and after every PostCompact. Because user
  messages are immutable once attached, the cache from prior turns stays
  valid; only the new user message + reminder is freshly evaluated.
- **User messages** = actual user input.

## System Prompt: Slot Sections

The system prompt is composed of **Sections**, each owning a numbered
**Slot**. Slots define ordering. Sections within the same slot use
insertion order, so several sections can stack inside one slot (a
subagent's injected charter lands in slot 0 alongside the identity).
Mutations to a section trigger `Refresh` (lazy re-render).

```
slot 0   identity        who you are — built-in or custom persona, and
                         a subagent's injected charter
slot 1   behavior        how you communicate and work: tone and updates
                         merged with the engineering defaults
                         (main agent only — a subagent carries its
                         working style in its charter)
slot 2   rules           safety contract plus the tool / task / git
                         protocols
slot 3   environment     date, cwd, git branch, platform — volatile,
                         placed last so daily/cwd changes don't bust
                         the cache prefix above
```

Slot constants live in `internal/core/section.go`; the default applier
and renderers live in `internal/core/system/catalog.go`. Skills, memory,
and the agent directory are intentionally **not** slots — they ride on
the reminder channel (skills, memory) or on tool schemas (agent
directory) instead.

See [`packages/core.md`](../packages/3-core/core.md) for the `Section` and
`System` types.

## Reminders

Reminders carry "session-level" or "project-level" mutable content. The
harness has standard providers:

| Provider ID | Source | Re-emit triggers |
|---|---|---|
| `skills-directory` | active skills (and the "use Skill tool to invoke" preamble) | session start, PostCompact, skill enable/disable/activate |
| `memory-user` | `~/.san/AGENTS.md` and `~/.san/rules/*.md` | session start, PostCompact, file change |
| `memory-project` | every `AGENTS.md` from the repository root down to the cwd, `<cwd>/.san/rules/*.md`, and `<root>/AGENTS.local.md` | session start, PostCompact, file change, cwd change |

Each provider has a stable ID; re-emitting from the same ID **drops the
previous queued entry**, so toggling a skill three times in a row
produces one final reminder, not three.

Reminders wrap their body in:

```xml
<system-reminder source="skills-directory">
  Enabled skills:
  - github:create-pr — ...
  - jira:link-ticket — ...
</system-reminder>
```

The LLM is instructed to treat the `<system-reminder>` tag as a system
instruction even though it appears inside a user message. That directive
is the "System reminders" part of the `<rules>` section (source:
`internal/core/system/prompts/rules.txt`), applied to both main and
subagent scopes — subagents receive reminders too (the skills
directory). It also tells the model to act on the most recent reminder
values (they refresh and re-inject after compaction) and not to echo the
tags back to the user.

Implementation: [`packages/reminder.md`](../packages/2-feature/reminder.md).

## Memory: AGENTS.md

San follows the [AGENTS.md](https://agents.md) standard. Two memory tiers:

- **User memory**: `~/.san/AGENTS.md`, plus `~/.san/rules/*.md`. Loaded
  once per session, attached as the `memory-user` reminder.
- **Project memory**: every `AGENTS.md` from the repository root down to
  the working directory, then `<cwd>/.san/rules/*.md`, then the
  git-ignored `<root>/AGENTS.local.md`. Files nearer the working
  directory are injected last, so the closest instructions win — the
  precedence Codex and Cursor use. The combined body is capped at 32 KB.
  Attached as the `memory-project` reminder.

Memory is **never** in the system prompt — that would invalidate the
prompt cache every time the user edited their memory file.

Each memory reminder leads with a one-line **preamble** framing the
content for the model (e.g. *"The following is the user's saved memory
(preferences and standing instructions). Apply it throughout this
session."*) before the `<memory scope="…">` envelope. The raw memory text
alone carries no instruction to follow it; the preamble supplies that
framing, mirroring how the skills directory self-introduces.
`reminder.WrapMemory` owns this shape.

**Subagents do not receive memory.** Only the long-lived main loop agent
gets `memory-user` / `memory-project`. A subagent is a one-shot worker
bounded by its own charter, so it carries the skills directory (to invoke
capabilities) but not the human's project/user instructions. See
`internal/subagent/executor.go` (`collectSubagentReminders`).

## Compaction

Compaction is **not** a channel by itself — it's a mutation of the
user-message channel that leaves the other two alone:

- the **system prompt** is reused from cache (never rebuilt),
- the **messages** collapse into a single `Previous context:` summary message,
- the **`<system-reminder>` blocks** are stripped from the summarization input
  (`core.BuildCompactionText` peels the trailing reminder run off each user
  message) and re-emitted fresh on the next user turn, so every reminder
  provider shares one lifecycle: injected on the first user message, skipped
  from the summary during compaction, re-injected after `PostCompact`.

`BuildCompactionText`'s sibling `BuildConversationText` keeps reminders intact
and is used for proactive-compaction *size estimation*, where the real prompt —
reminders included — is what the estimate must track.

For the full mechanism — the common pipeline, auto-compact vs. manual
`/compact`, transcript boundary recording, and reminder freshness — see
[`concepts/compaction.md`](compaction.md).

## See Also

- [`concepts/extension-model.md`](extension-model.md) — skills (one
  reminder source) and how plugins contribute to it.
- [`packages/core.md`](../packages/3-core/core.md) — `System`, `Section`, slot
  layout.
- [`packages/reminder.md`](../packages/2-feature/reminder.md) — runtime API.
- [`concepts/compaction.md`](compaction.md) — the full compaction mechanism
  (channels, auto vs. manual, transcript boundary).
- [`packages/session.md`](../packages/2-feature/session.md) — how compaction
  records flow into the transcript.
