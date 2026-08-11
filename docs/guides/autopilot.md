# Autopilot

## Overview

Autopilot is San's autonomy system, designed to minimize human intervention: a
copilot model cruises the session, keeping routine work moving and handing
control back only when something genuinely needs you. It acts through a set of
independently enabled **steers** — proposing the next step, approving gray-zone
tool calls, answering a command's interactive prompts, answering
`AskUserQuestion`, and continuing finished turns toward a mission. Automatic
input suggestions and gray-zone permission judging are on by default.

Enter Autopilot mode with `shift+tab` (cycle until the amber
`⏵⏵ autopilot`), and configure it with the `/autopilot` panel. A resumed
session (`san -r <id>`) comes back in the mode it was saved in. If you just want
to see it drive, [`/goal`](#goal) is the shortest path in — it is one preset of
everything below.

## The six steers

Steers are à-la-carte toggles, ordered by increasing autonomy. None fire unless
Autopilot mode is engaged, except Suggest: its toggle controls automatic input
hints in every mode.

| Steer | Default | What it does |
|---|---|---|
| **Suggest** | **on** | Controls automatic input hints in every mode. Off means no hint is generated; on shows a next-input suggestion. When Autopilot is driving a mission, the suggestion follows that mission; otherwise it uses generic input prediction. `tab` accepts the suggestion, and `enter` sends it. It never submits on its own. |
| **Permission** | **on** | Auto-approves gray-zone tool calls the static rules couldn't resolve, judging reversibility, blast radius, and data exfiltration. In a git working tree it counts history as the safety net: changes to tracked files are routine, and git's own sharp edges (`reset --hard`, `clean -f`, `stash drop`, force-push, `branch -D`) are weighed against the session's intent rather than blocked outright. It still escalates what leaves the tree — untracked files elsewhere, paths outside the project, a shared default branch. Fails closed: any error escalates to you. |
| **Bash** | off | Answers an already-approved command's interactive prompt (`Continue? [Y/n]`) when the answer just continues the approved action; skips anything that would widen scope. |
| **Skill** | off | Approves the copilot's skill loads outright, without the judge — a deliberate "trust skills" toggle, separate from Permission because the judge tends to escalate a skill load (it can run scripts). Off ⇒ skill loads fall to the Permission judge (or you). |
| **Question** | off | Answers `AskUserQuestion` for you whenever the mission or the conversation makes a reasonable choice clear, preferring the conservative option over stalling the run. It defers only when the call is genuinely yours — irreversible, costly to get wrong, or a matter of your preference or judgement. Option labels are validated verbatim — a partial or invented answer becomes a defer. |
| **End** | off | After a turn, decides whether to continue toward the mission and types the next instruction itself. Bounded by **Continue at most N times** (default 20, `0` = no limit); the counter resets on every human turn. With no mission briefed it infers the objective from the conversation, and stands down if the conversation shows none. |

## Mission

The mission is what the copilot drives toward this session — written in the
`/autopilot` panel's Mission dialog, a small editor: the text you type is the
mission (`enter` saves it, `alt+enter` for a newline; paste works), `ctrl+r` asks
the copilot to refine the draft in place, `ctrl+c` clears it, and `esc` saves and
leaves. Every steer reads it: the steering steers (Suggest, Question, End) drive
toward it — falling back to the conversation's own objective when none is
briefed — and the safety steers (Permission, Bash) take it as intent context — a
tool call or prompt that plainly advances the mission reads as expected, routine
work. Intent never overrides safety, though: they still escalate anything
irreversible, destructive, out-of-project, or data-leaking, mission or not.

When the End steer decides the mission is **fully accomplished**, it retires
it: the mission is cleared and the steers reset to the passive baseline
(Permission + Bash) — Autopilot stays on, you take the wheel back with the
auto-approve safety net intact.

## Start the mission

The panel's bottom row is two buttons — **Save** and **Start** (`←`/`→` to
pick, `enter` to run):

- **Save** applies the config to the live session and writes it to
  `settings.json` as the default seed, without changing the mode. Use it when
  you're only tuning steers, or want to engage later with `shift+tab`.
- **Start** does everything Save does, then engages Autopilot and kicks the
  mission hands-free: it derives the opening step from the mission and submits
  it itself, so briefing a mission and hitting Start is the whole launch. Start
  needs a mission — with none set it nudges you instead of engaging.

Landing on Autopilot via `shift+tab` no longer auto-starts; it only surfaces the
Suggest steer's proposal (if on). Kicking the mission is always the explicit
Start button.

## /goal

The common case — brief a mission, switch on the steers that let the copilot
act, take the cap off, start — collapses into one line:

```
/goal add table-driven tests for internal/setting until go test ./... passes
```

`/goal` is a preset, not a separate mode: it engages the same Autopilot with a
particular configuration.

- the goal becomes the [mission](#mission)
- the driving steers come on (Bash, Skill, Question, End)
- the continuation cap is lifted — the run ends when the goal is met, not when
  a counter expires
- Autopilot engages and the copilot opens the first step itself

Stated mid-turn, it takes over when the current turn lands. `/goal` on its own
reports the current goal; `/goal clear` stands the copilot down.

It is deliberately session-scoped: unlike the panel's Save, it does not rewrite
your saved defaults — stating a goal is something you do for this session. Your
**Permission** steer is left exactly as configured, since an explicit `false`
there is a safety choice a goal has no business overriding. Ending the goal —
met or cleared — rewinds the steers to what `/goal` found, so the autonomy it
switched on doesn't outlive it.

Reach for the panel instead when you want a different mix: a run that suggests
but never submits, a bounded number of continuations, or a custom steering
prompt.

## Staying autonomous

With the End steer on, the copilot drives through the things that would
otherwise park the session until you came back:

- **A turn that stopped mid-work** — step limit, or output truncated beyond
  recovery — is picked back up, with the copilot told how it ended. Your own
  `esc` is different: a cancelled turn is you taking the helm. So is a stop hook.
- **A turn that failed outright** gets a growing backoff (5s, 10s, 15s) and then
  a resume decision, up to three consecutive attempts — reset by any turn that
  reaches its end. An error that needs you still lands as a handback.
- **A steer that misfired** retries up to three times, so a network blip or a
  non-JSON reply doesn't end the mission.
- **A compaction mid-decision** holds the verdict instead of dropping it.
- **Running out of turns**: set **Continue at most** to `0` (shown as `∞`) and
  the run ends when the mission is done, not when a counter expires.

Uncapped runs pair well with a fast, cheap steer model — see
[Configuration](#configuration).

## Demo: a hands-free scaffold

A two-minute run that exercises the full loop — kick-off, gray-zone approval,
auto-continuation, and completion — without touching anything outside a scratch
directory.

**1. Start San in an empty repository.** Run it there and nowhere else — the
goal below writes `notes/` wherever it starts, so in one of your own projects it
would scribble into a real directory:

```bash
mkdir /tmp/autopilot-demo && cd /tmp/autopilot-demo && git init -q && san
```

`git init` is not incidental — under git the Permission steer treats changes to
tracked files as recoverable, which is what keeps the run from stopping to ask.

**2. State the goal.** One line, and it is the last key you press:

```
/goal Scaffold a notes/ directory: todo.md with a 3-item checklist, done.md
empty, and README.md explaining the layout. Work one file per turn. When all
three exist, verify with ls notes/ — then the goal is met.
```

Three details in that wording are doing work: *one file per turn* forces several
continuations so you can watch them, *ls notes/* puts a gray-zone bash call in
the path, and *then the goal is met* gives the copilot a completion test it can
actually check.

**3. Watch the run.** Expect a transcript like:

```
⏵ autopilot · goal set

❭ Create notes/todo.md with a 3-item checklist.
  ⎿  autopilot · step 1
● Write(notes/todo.md)
  ⎿  Write → 5 lines

❭ Create an empty notes/done.md.
  ⎿  autopilot · step 2
...
● Bash(ls notes/)
  ↳ auto-approved · read-only directory listing
  ⎿  Bash → 3 lines

✓ autopilot · mission complete
```

Every `❭` carries the green `⎿ autopilot` mark — the copilot typed them all,
opening step included; you never touched the composer. The `ls` is a gray-zone
call the Permission steer approved inline. On `✓ mission complete` the goal is
cleared and the steers rewind to what `/goal` found — open `/autopilot` to
confirm — while Autopilot stays engaged. To stop early, `/goal clear`.

The same run through the panel: toggle **End** on, brief the text above as the
**Mission**, and press **Start**. Use that route when you want a different mix —
to see the gentle end of the spectrum, run it with only **Suggest** on and
engage with `shift+tab`: the copilot proposes each step as ghost text in the
composer and you accept with `tab` + `enter`.

## Reading the transcript

| Mark | Meaning |
|---|---|
| green `⎿ autopilot · 2/5` | the `❭` line above was typed by the copilot (continuation 2 of 5; an uncapped run counts `step 2` instead) |
| amber `⏵ autopilot · turn failed · retrying in 5s` | a turn errored out; the copilot will decide whether to resume |
| green `↳ auto-approved · <reason>` | the permission judge let the tool call above through |
| amber `↳ escalated · <reason>` | the judge sent the call back to you |
| green `⏵ autopilot · answered for you` | the copilot answered an `AskUserQuestion` |
| amber `↩ autopilot · this question is yours` | it deferred the question to you |
| amber `↩ autopilot · over to you` | it stopped and handed control back (a decide error rides after it) |
| green `✓ autopilot · mission complete` | the mission is done and retired |

While a decision is in flight the mode line reads `⏵⏵ autopilot · thinking…`;
approvals tally there too (`· 3 approved · 1 escalated`).

## Configuration

The panel edits the live session config. The model, steers, and continuation cap
are saved to `settings.json` as the default for new sessions. The **Steering
Prompt** and **mission** are per-session: they ride the transcript and restore on
`/resume`, but are never written as the default — a new session starts from the
built-in steering instructions with no mission. To carry custom steering
instructions or a mission to another session, export them as a preset and import
the preset there.

The Steering Prompt controls how the copilot drives; it does not replace the
immutable control-plane policy. Every LLM steer always receives that policy,
which fixes the trust boundaries, fail-closed behavior, task-specific safety
rules, and output contract. The existing `systemPrompt` / `systemPromptFile`
configuration keys are retained for compatibility and supply only the editable
steering-instructions portion.

```jsonc
{
  "autoPilot": {
    "model": "anthropic/claude-haiku-4-5", // steer decisions; empty = session model
    "systemPrompt": "…",                   // Steering Prompt; per-session, not written here by the panel
    "systemPromptFile": "~/prompts/pilot.md", // persistent steering default; used when systemPrompt is empty
    "mission": "…",                        // per-session; set via the panel
    "maxContinuations": 20,                // -1 = uncapped (the panel writes this when you enter 0)
    "steers": {
      "suggest": true,
      "permission": true,  // omit for the default (on); false escalates everything
      "bashPrompt": true,  // the Bash steer
      "skill": true,       // the Skill steer — trust skill loads
      "question": true,
      "turnEnd": true      // the End steer
    }
  }
}
```

Named presets bundle the whole copilot config — Steering Prompt, mission, and
steers. In the `/autopilot` menu, `e` exports the current config and `i` imports
one, stored under `~/.san/autopilot/<name>.json`.

## Relationship to other features

- [Permission model](../concepts/permission-model.md) — the static rules whose
  gray zone the Permission steer judges; hard-blocked actions never reach it.
- The judge component lives in `internal/reviewer` (`reviewer.Judge`); the
  steers and panel live in `internal/app` / `internal/app/input`.
