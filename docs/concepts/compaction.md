# Compaction

When a conversation approaches the model's input limit, San **compacts**
it: an LLM summarizes the history, and that summary replaces the old turns so
the conversation can keep going within the window.

This page covers the mechanism end to end — what happens to each
[harness channel](harness-channels.md) (system prompt, `<system-reminder>`
blocks, messages), how the summary is recorded for replay, and how the two
entry points (automatic and manual `/compact`) differ and agree.

The window itself is resolved as: `SAN_INPUT_LIMIT` if set, else the limit the
model reports (or the one configured for it), else unknown. There is no guessed
default — an invented window is acted on silently, and guessing low discards
context on every compaction while guessing high never fires. An unknown window
skips proactive compaction and leaves the reactive *prompt-too-long* retry to
recover; the status bar shows `--` rather than a percentage of a guess. Set
`SAN_INPUT_LIMIT` for a model San cannot size on its own.

## What compaction touches (and what it doesn't)

The three channels are treated very differently. The whole design hinges on
leaving the cacheable prefix alone and only rewriting the volatile tail.

```
            BEFORE compaction                      AFTER compaction
  ┌──────────────────────────────┐       ┌──────────────────────────────┐
  │ SYSTEM PROMPT (cached)         │      │ SYSTEM PROMPT (same bytes,     │
  │  identity / policy / guideline │ ===> │  reused straight from cache)   │  ← never rebuilt
  ├──────────────────────────────┤       ├──────────────────────────────┤
  │ MESSAGES                       │      │ MESSAGES                       │
  │  user  "hi"                    │      │  user  "Previous context:      │
  │  asst  ...                     │ ===> │         <summary>"             │  ← whole chain → 1 msg
  │  user  ...   (20+ turns)       │      │                                │
  ├──────────────────────────────┤       ├──────────────────────────────┤
  │ <system-reminder> on the last  │      │ (nothing here yet — reminders  │
  │  user message (skills, memory) │ ===> │  re-emit on the NEXT user msg) │  ← stripped, re-rendered
  └──────────────────────────────┘       └──────────────────────────────┘
```

- **System prompt — not rebuilt.** Compaction never calls `System.Use` /
  `Refresh`, so `System.Prompt()` returns the same cached string. The prompt is
  session-invariant (identity, policy, guidelines, environment), so there is
  nothing to recompute, and reusing it keeps the provider's prompt-cache prefix
  valid across the compaction.
- **Messages — replaced by one summary.** The entire chain becomes a single
  **plain user message**, `core.FormatCompactSummary(summary)` =
  `"Previous context:\n" + summary`. Not a system-reminder, not the system
  prompt — durable conversation state belongs in the message channel.
- **`<system-reminder>` blocks — skipped, then re-rendered fresh.** Reminders
  ride *inside* the last user message's content, so the summarizer would
  otherwise bake stale skills/memory text into the permanent summary.
  `core.BuildCompactionText` strips them before summarizing; afterwards the
  harness re-emits them so they reattach to the *next* user turn (see
  [Reminder freshness](#reminder-freshness)).

## The common pipeline

Both entry points run the same core steps:

```
  trigger
    │
    ▼
  summarize  ── system.CompactPrompt() over BuildCompactionText(msgs)
    │            (reminders stripped; reminder-only turns dropped)
    ▼
  replace    ── messages := [ UserMessage("Previous context:\n"+summary) ]
    │
    ├─▶ record    message.appended(summary, stable ID)
    │             session.compacted(boundary = summary ID)
    │
    ├─▶ refresh   re-read memory from disk (refreshMemoryContext)
    │
    └─▶ reminders DiscardPendingNotices() + RequeueSystemReminders()
                  → skills/memory reattach on the next user message
```

The summary text itself is identical between the two paths (same
`FormatCompactSummary`), so a reader/replayer cannot tell which path produced a
given summary.

## Auto-compact vs. manual `/compact`

```
AUTO  (core agent, inside the ThinkAct loop)
  ┌────────────────────────────────────────────────────────────┐
  │ each step (measurement carried across turns):                │
  │   prompt size the provider reported last step, which is      │
  │   TotalInputTokens (fresh + cache read + cache creation),    │
  │   not InputTokens — under prompt caching the latter is only  │
  │   the uncached delta and reads near-empty on a full context. │
  │   Before the first inference there is no such count, so the  │
  │   size is estimated from system prompt + conversation bytes; │
  │   that covers an agent seeded with a resumed history.        │
  │        │                                                     │
  │        ├─ ≥ 90% of input limit ──▶ compact() ──▶ continue ───┼─▶ re-infer NOW
  │        │                                          (in-loop)  │   with [summary]
  │   streamInfer()                                              │
  │        └─ "prompt too long" error ─▶ compact() ─▶ continue ──┼─▶ retry
  └────────────────────────────────────────────────────────────┘

MANUAL  (/compact [focus], app layer)
  user: /compact [focus]
     │  PreCompact hook  (focus += AdditionalContext)
     │  summarize ──▶ stop the agent ──▶ conv := [summary]
     │                                ──▶ restore recently-accessed files
     └─ next user message ──▶ ensureAgentSession reseeds the agent from conv ──▶ Run
```

### Commonalities

- Summarize with the same compaction prompt over reminder-stripped text.
- Replace the chain with one `Previous context:` user message.
- Re-read memory from disk, then `DiscardPendingNotices` + `RequeueSystemReminders`
  so the next user turn carries fresh skills/memory.
- Fire the `PostCompact` hook.
- The system prompt is reused from cache (neither path rebuilds it).

### Differences

| Aspect | Auto-compact | Manual `/compact` |
|---|---|---|
| Trigger | proactive size estimate (≥90% of limit) or reactive *prompt-too-long* retry | user runs `/compact [focus]` |
| Driver | core agent `compact()` — runs **in-loop** | app layer; summary computed, then agent **stopped** |
| Continuation | `continue` re-infers immediately with `[summary]` | agent stopped; **next** user message reseeds it from the conversation |
| Focus | none | optional focus string; `PreCompact` hook can add context |
| Recent-file restore | not performed | restores recently-accessed files after the summary |
| Transcript boundary | recorded (summary append + `session.compacted`) | not yet recorded — tracked with the post-compaction unification work |

> The remaining divergences (file restore, env-reset call, and manual-path
> transcript recording) are being unified so both paths share one
> post-compaction routine. See the unification follow-up.

## Transcript recording & replay

Compaction does not delete the old turns from the transcript — that history
stays on disk for audit and scroll-back. Instead it records a **boundary** so
replay knows where the active chain begins.

The core agent gives the summary a stable ID, records it as a normal
`message.appended` (parented to the last pre-compaction message), and emits a
`session.compacted` record whose `summaryMessageId` is that summary's ID. Replay
uses that ID as the boundary to stop walking parents at:

```
transcript (append-only)            replay: walk leaf → parent, STOP at boundary
  appended  m1                        m1   ─ excluded (ancestor of boundary)
  appended  m2                        m2   ─ excluded
  ...                                 ...
  appended  SUM   ◀── boundary        SUM  ◀── stop ┐
  appended  m8  (parent = SUM)        m8            ├─ active chain = [SUM, m8, m9]
  appended  m9                        m9  (leaf)    ┘
  session.compacted  summaryMessageId=SUM
```

Without the boundary, replay would walk all the way back through `m1…m2…` and
reconstruct the *pre*-compaction chain — which is exactly the
"`messageIds — 2 expected vs 7 replayed`" mismatch the inspector reports when
the summary append and boundary are missing. `materializeActiveChain`
(`internal/session/transcript/projector.go`) stops at the boundary so the
reconstructed chain matches what the live agent actually sent.

## Reminder freshness

Reminder providers render from cached instructions
(`env.CachedUserInstructions` / `CachedProjectInstructions`). On PostCompact the
harness calls `refreshMemoryContext` to **re-read the memory files from disk
before** re-emitting, so an `AGENTS.md` edited mid-session re-injects
its *latest* content rather than a stale cached copy. This is the key asymmetry
with the system prompt: the prompt is reused from cache (it cannot have
changed), but reminders are deliberately rebuilt.

## Implementation

| Concern | Location |
|---|---|
| Trigger + in-loop compaction | `internal/core/agent_impl.go` (`ThinkAct`, `compact`) |
| Summary text (reminders stripped) | `internal/core/message.go` (`BuildCompactionText`) |
| Compaction LLM call | `internal/agent/build.go`, `internal/app/conv/compact.go` |
| Boundary recording | `internal/session/recorder.go` (`onCompact`) → `transcript.FileStore.Compact` |
| Replay truncation | `internal/session/transcript/projector.go` (`materializeActiveChain`) |
| App-side handlers + memory refresh | `internal/app/model_compact.go` |

## See Also

- [`concepts/harness-channels.md`](harness-channels.md) — the system-prompt /
  reminder / message channels compaction operates on.
- [`packages/reminder.md`](../packages/2-feature/reminder.md) — reminder providers and
  re-emission.
- [`packages/session.md`](../packages/2-feature/session.md) — transcript records and replay.
