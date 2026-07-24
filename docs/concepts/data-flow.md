# Data Flow: Input → Agent → Render

> 中文版本：[`data-flow.zh.md`](data-flow.zh.md)
>
> Companion: [`rendering.md`](rendering.md) — how the rendered output
> is actually composed (View() layout, Markdown pipeline, tool blocks).

How a keystroke (or a cron fire, or a hub event) travels through the TUI
and becomes either a slash-command result or an agent response that the
user sees in their terminal.

## Cast

The TUI is a [Bubble Tea](https://github.com/charmbracelet/bubbletea) MVU
loop. Three Bubble Tea primitives drive everything:

- **`tea.Msg`** — an event entering the model (key press, window resize,
  spinner tick, custom in-process message).
- **`Update(msg)`** — pure function that mutates the model and returns a
  `tea.Cmd`.
- **`tea.Cmd`** — a function the framework will run; its return value is
  injected back as a new `tea.Msg`. **This is how async work re-enters the
  model.** Whenever you see a function "return a cmd," that cmd will be
  scheduled by Bubble Tea, its output captured as a `tea.Msg`, and fed
  back into `Update`.

Convention: many internal handlers return `(tea.Cmd, bool)`. The bool
means **"did I claim this event?"** — `true` stops the chain, `false`
lets the caller try the next layer. `(nil, false)` is the common
"not for me" return.

Input sources land as `tea.Msg`. **`SubmitToAgent`** is the single exit
to the running agent. Rendering happens via `tea.Println` (terminal
scrollback) plus `View()` (bottom UI strip).

```
   ┌──────────────────────────────────────────────────────────────┐
   │  Inputs                                                      │
   │                                                              │
   │   keyboard      slash command     cron       async hook      │
   │     │                │             │             │           │
   │     ▼                ▼             ▼             ▼           │
   │  handleSubmit  → SlashController  inject*    inject*         │
   │     │                │             │             │           │
   │     └────────────────┼─────────────┴─────────────┘           │
   │                      ▼                                       │
   │               SubmitToAgent(content, images)                 │
   │                      │                                       │
   │                      ▼ agent.Send (push to inbox)            │
   └──────────────────────┼───────────────────────────────────────┘
                          │
   ┌──────────────────────┼───────────────────────────────────────┐
   │  Agent loop          ▼                                       │
   │           ┌─────────────────────┐                            │
   │           │  Inbox → LLM → Tool │   ← runs in goroutine     │
   │           │     ↘    ↙          │                            │
   │           │     Outbox          │ → core.Event stream        │
   │           └─────────────────────┘                            │
   └──────────────────────┼───────────────────────────────────────┘
                          │
   ┌──────────────────────┼───────────────────────────────────────┐
   │  Render              ▼                                       │
   │             ContinueOutbox tea.Cmd                           │
   │                      │                                       │
   │                      ▼ tea.Msg                               │
   │                  Update → conv.Update → callbacks            │
   │                      │                                       │
   │                      ▼                                       │
   │             CommitMessages → tea.Println → scrollback        │
   │                      │                                       │
   │                      ▼                                       │
   │                   View() → bottom UI strip                   │
   └──────────────────────────────────────────────────────────────┘
```

## Path A — Text input

User types `hello`, presses **Enter**.

```
tea.KeyMsg('h')                  ── per keystroke
   │
   ▼
Update                            update.go
   │
   ├─ case tea.KeyMsg → routeKeypress
   │     │
   │     ├─ tryActivePopup           — question modal, approval modal,
   │     │                             or one of the slash-command
   │     │                             pickers (/models, /tools, ...)
   │     │                             nothing active for typing 'h'
   │     │
   │     ├─ HandleImageSelectKey     — image picker mode (off)
   │     ├─ HandleSuggestionKey      — prompt-suggestion mode (off)
   │     ├─ HandleQueueSelectKey     — queue-navigation mode (off)
   │     │
   │     └─ handleTextareaShortcut   — Ctrl-shortcuts / Tab / Enter / ...
   │           └─ no match for KeyRunes('h') → (nil, false)
   │
   ├─ routeToSubModel                — no sub-model claims a KeyRunes msg
   │
   └─ updateTextarea                  — textarea consumes the rune
   ▼
View                              view.go      bottom UI shows "h▮"
```

The dispatch in `routeKeypress` is a **priority stack**: a popup that is
shown (e.g. the model picker after `/models`) gets first refusal on every
key; only if nothing higher up claims the key does it reach the textarea
shortcuts, and only then the textarea itself.

Five rune-keystrokes later, textarea holds `hello`. User presses **Enter**:

```
tea.KeyMsg(Enter)
   │
   ▼
routeKeypress → handleTextareaShortcut
   │   "shortcut" = keys with a special meaning to the textarea
   │   (Ctrl-C/D/L/E/O/U/V/Y/T, Tab, Shift+Tab, Enter, Esc, ↑↓ history)
   └─ case tea.KeyEnter → m.handleSubmit()       update_submit.go
        │
        ▼
   handleSubmit
        Step 1: read textarea ────► "hello"
        Step 2: stream active? ───► no (no answer mid-stream)
        Step 3: → dispatchSubmission("hello")
                  │
                  ▼
   dispatchSubmission
        Step 1: "exit" literal? ──► no
        Step 2: prompt hook ──────► allowed
                    │  Any UserPromptSubmit hook (see hook pkg) gets to
                    │  read the prompt and optionally block it (e.g. a
                    │  policy hook rejecting prompts containing secrets).
                    │  "Allowed" = no hook blocked.
        Step 3: record to history (↑/↓ recall in the textarea)
        Step 4: slash command? ───► no (no leading "/")
        Step 5: send to agent
                  ├─ buildUserMessage("hello") → ChatMessage{Role: user}
                  │     Resolves image refs (`[image.png]` → bytes) and
                  │     splits inline-pasted images out of the text.
                  │
                  ├─ conv.Append(msg)
                  │     Appends to m.conv.Messages. This is the TUI's
                  │     own display copy — View() renders it as
                  │     scrollback, and PersistSession writes it to
                  │     disk. The agent does NOT read this slice on
                  │     every send; it keeps its own internal message
                  │     history. The two stay in sync via events
                  │     (see Path D). conv.Append is read-only by the
                  │     agent in one case only: ensureAgentSession
                  │     uses it to seed a freshly-started agent.
                  │
                  ├─ userInput.Reset()
                  │     Clears textarea + pending images so the user can
                  │     start the next message.
                  │
                  └─ SubmitToAgent(msg.Content, msg.Images)
                        Pushes `msg` onto the agent's INBOX (a separate
                        Go channel). The agent's own loop will read it,
                        append it to its internal history, then call the
                        LLM. That's why both calls are needed:
                          conv.Append(msg)  → makes the user SEE it
                          SubmitToAgent     → makes the agent ACT on it
                        │
                        ▼
   SubmitToAgent
        ├─ provider connected?    yes
        ├─ ensureAgentSession()    starts agent goroutine if needed
        ├─ sendToAgent ───────────► agent.Session inbox channel
        │                           (a Go channel; non-blocking push)
        │
        └─ returns ContinueOutbox cmd  (see Path D)
              That cmd, when bubbletea runs it, will read one event off
              the agent's Outbox channel and produce a tea.Msg back into
              Update. The first event normally arrives within ms once
              the LLM starts streaming.
```

## Path B — Slash command

User types `/clear`, presses **Enter**. The path overlaps with Path A
up to Step 4:

```
handleSubmit → dispatchSubmission
   Step 1..3 same as Path A
   Step 4: runSlashCommandIfMatched("/clear")
              │
              ▼
   input.NewSlashCommandController(slashCommandEnv)         slash_command.go
              │
              ▼
   SlashCommandController.HandleSubmit
              │ ParseCommand("/clear") → ("clear", "")
              ▼
   handleClearCommand(c, ctx, "")
        ├─ env.StopAgentSession()         clears agent state
        ├─ env.PersistSession()           saves current conv
        ├─ env.Conversation.Clear()       wipes display
        ├─ env.Input.Reset()
        └─ returns (result="conversation cleared", cmd=nil, nil)
              │
              ▼
   c.env.Conversation.AddNotice(result)    "conversation cleared"
   c.env.CommitMessages()                  → tea.Println to scrollback
```

A slash command's handler reads live state via `env.*` (services),
mutates UI through callbacks (e.g. `env.PersistSession`), and returns
a short `result` string the controller wraps as a notice.

Some slash commands (`/loop`, `/init`) end up calling
`env.SubmitToAgent(prompt, nil)` to hand off to the agent — they
rejoin Path A at the SubmitToAgent step.

## Path C — Background trigger

Three producers can run while no user is typing. They each park their
output in a queue/channel, then the **turn boundary** (the moment an
agent turn ends) drains them.

### Producers

```
┌─ Producer ────────────┬─ Where it lives ────┬─ Where it parks ──────────────┐
│ Cron tick             │ trigger.StartCron-  │ m.systemInput.CronQueue       │
│   (every minute the   │ Ticker (background  │   []string (prompts queued)   │
│    ticker checks      │ goroutine)          │                               │
│    durable jobs)      │                     │                               │
├───────────────────────┼─────────────────────┼───────────────────────────────┤
│ Async hook follow-up  │ trigger.StartAsync- │ m.systemInput.AsyncHookQueue  │
│   (a hook script's    │ HookTicker          │   each item carries Notice +  │
│    JSON output said   │                     │   Context lines + Contin-     │
│    `nextPrompt: ...`) │                     │   uationPrompt                │
├───────────────────────┼─────────────────────┼───────────────────────────────┤
│ Subagent completes    │ task.SetLife-       │ broker.Send to "main"; the    │
│   (background Task,   │ cycleHandler →      │ main-loop deliver fn pushes   │
│    spawned by the     │ broker.Send         │ it onto m.mainNotices         │
│    Agent tool)        │                     │ (a Go channel)                │
└───────────────────────┴─────────────────────┴───────────────────────────────┘
```

`m.mainNotices` carries notices bound for the main loop: subagent completions,
interim messages (`SendMessage to="main"`), and self-learn notices. Agent
mail routes through the process-wide [`broker`](../packages/2-feature/broker.md);
the main loop opens `broker.Main` and forwards onto this channel.

### Drain at turn boundary

When the live agent finishes a turn, `OnTurnEnd` calls
`drainTurnQueues` to take the next-highest-priority queued item and
play it as if the user had just typed it.

```
OnTurnEnd                                    model_agent_events.go
   └─ drainTurnQueues                        model_turn_queue.go
        First non-empty wins, priority high → low:
        │
        ├─ user input queue?  ─── parked while a turn was streaming
        │                        (handled inline; not an inject*)
        ├─ cron queue?       ──► injectCronPrompt(prompt)
        ├─ async hook queue? ──► injectAsyncHookContinuation(item)
        └─ main notice batch ► injectNotices(batch)
```

### Waking the Update loop (idle path)

`drainTurnQueues` only fires at `OnTurnEnd`, so mail that arrives
*between* turns (the common case for a subagent that finishes minutes
after launch) needs another way to wake the Update loop. The broker
subscription delivers onto a Go channel (`m.mainNotices`), so we use the
same trick the agent outbox uses — a **blocking-receive `tea.Cmd`** that
turns "next item on the chan" into a `tea.Msg`:

```
Init                                       model.go
   └─ awaitMainNotice(m.mainNotices)         model_turn_queue.go
        └─ blocks on chan, yields mainNoticeMsg{event} when one arrives

Update                                     update.go
   case mainNoticeMsg:
        └─ onMainNotice(n)                 model_turn_queue.go
              ├─ if Stream.Active: append n to m.pendingNotices
              ├─ start awaitMainNotice again so the next publish wakes us
              └─ if !Stream.Active:
                   injectNotices([]mainNotice{n})
```

`onMainNotice` always starts a fresh `awaitMainNotice` — safe because
the chan is empty when we restart, so the next firing waits for the
next publish (no spin loop). There are two delivery paths depending
on what the live agent is doing when the event arrives:

| When the event arrives | Who delivers it | Latency |
|---|---|---|
| Mid-stream (agent answering) | `OnTurnEnd → drainTurnQueues` drains `m.pendingNotices` | next turn boundary |
| Idle (between turns) | `onMainNotice` itself takes the `!Stream.Active` branch and injects directly | immediate |

The idle branch is what handles the common case of a background
subagent finishing long after the launching turn ended. `pendingNotices`
exists only to bridge events that landed *during* a stream — those
must wait so they don't collide with the answer in progress.

The producer side: a subagent (or any background task) finishes →
`notifyTaskCompleted` → the task-lifecycle handler registered in
`wireTaskLifecycle` sends the completion to the "main" address → the
`broker.Register(broker.Main, …)` delivery function pushes onto `m.mainNotices`.
Background tasks are one producer; subagents posting interim messages
(`SendMessage to="main"`) and self-learn notices are others — all arrive on
the same channel.

Same pattern as `conv.DrainAgentOutbox` reading the agent outbox chan
— two Go channels, two blocking-receive cmds, one Update loop. No
polling, no tick, zero idle CPU.

### What inject* does

Each inject* function has the same shape: tell the user what triggered
this round (a notice), make the trigger's payload look like a user
message in conv, then hand the payload to SubmitToAgent so the agent
actually responds. The "payload" varies by producer:

| Producer | Payload sent to agent |
| --- | --- |
| Cron | The cron job's `Prompt` string (what the user wrote when scheduling) |
| Async hook | The hook's `ContinuationPrompt` field |
| Subagent done | `Content` from the merged `mainNotice` — usually the subagent's final output |

```
each inject*
   ├─ conv.AddNotice(...)                          ◄── "Scheduled task fired", etc.
   ├─ conv.Append(ChatMessage{Role: user, ...})    ◄── shows in scrollback
   └─ SubmitToAgent(<payload>, nil)                ◄── kicks off the next turn
```

All three converge on **SubmitToAgent**. Same provider check, same
`ensureAgentSession`, same `sendToAgent` push. There is no other way
to reach the agent's inbox from the TUI.

### End-to-end trace: subagent done → main agent inbox

Putting all the pieces above together — this is exactly what happens
between the moment a background subagent finishes and the moment its
result hits the main agent's inbox to drive the next turn. Three
goroutines are involved; each `─ ─ ─►` is a handoff across one.

```
   Subagent goroutine               TUI Update goroutine            Main Agent goroutine
   ──────────────────               ────────────────────            ────────────────────
   ① task.Run() returns
       │
       ▼
   ② notifyTaskCompleted(info)
       │   task/hooks.go
       ▼
   ③ lifecycleHandler.TaskCompleted
       │   model_lifecycle.go:194
       ▼
   ④ broker.Send(msg)
       → addressed to "main"
       │   model_lifecycle.go
       ▼
   ⑤ broker.Register(Main) deliver fn
       │   model_lifecycle.go
       ▼
   ⑥ m.mainNotices <- n  ─ ─ ─ ─ ─►  ⑦ awaitMainNotice unblocks
                                       returns mainNoticeMsg{event}
                                       (was parked on chan in own
                                        goroutine spawned by Init)
                                          │   bubbletea routes the
                                          │   msg to Update loop
                                          ▼
                                       ⑧ Update case mainNoticeMsg:
                                          → onMainNotice(n)
                                          │   model_turn_queue.go
                                          ├─ append to pendingNotices
                                          ├─ restart awaitMainNotice
                                          ▼
                                       ⑨ Stream.Active?
                                          ├─ true → return; wait OnTurnEnd
                                          │         to call drainTurnQueues
                                          └─ false → fall through ↓
                                          ▼
                                      ⑩ injectNotices(merged)
                                          ├─ conv.AddNotice("…completed")
                                          └─ SubmitToAgent(content, nil)
                                          │   update_submit.go
                                          ├─ check LLMProvider
                                          ├─ ensureAgentSession
                                          ▼
                                      ⑪ sendToAgent(content, images)
                                          │   agent.go
                                          ├─ attachPendingReminders
                                          ▼
                                      ⑫ m.services.Agent.Send(...)
                                          ◄── ENTERS MAIN AGENT INBOX ──►  ⑬ agent picks up
                                                                                from inbox,
                                                                                runs a turn
                                                                                (now → Path D)
```

Key handoffs:

| Step | What crosses what | Mechanism |
|---|---|---|
| ⑥ → ⑦ | subagent goroutine → TUI Update goroutine | Go chan (`m.mainNotices`) + blocking-receive `tea.Cmd` |
| ⑫ → ⑬ | TUI Update goroutine → main agent goroutine | `Agent.Send` writes the agent's internal inbox chan |

Two chans, two goroutine boundaries. The TUI sits in the middle on
purpose — that's where `AddNotice`, provider/session checks, and
priority ordering happen. If a Stream.Active=true diverted us into
the `pendingNotices` branch at ⑨, the same ⑩-⑫ sequence runs
later from `drainTurnQueues` at the next OnTurnEnd; the only
difference is *when*.

## Path D — Agent → render

The agent goroutine runs the inbox, calls the LLM, streams tokens,
emits tool calls, emits a final result. Every emission goes onto its
`Outbox` channel.

```
agent goroutine                         (runs in core.Agent.Run)
   │
   ▼
core.Event onto Outbox channel
   │   Event types: OnStart, PreInfer, OnChunk (×N), PostInfer,
   │   PreTool, PostTool, OnMessage, end-of-turn, AgentStop, etc.
   │
   ▼
ContinueOutbox tea.Cmd                  agent.go: blocks on the channel,
   │                                    reads ONE event, returns a
   │                                    tea.Msg that ALSO carries the
   │                                    next ContinueOutbox cmd. So
   │                                    Update keeps scheduling fresh
   │                                    polls until events stop arriving
   │                                    (one-shot tea.Cmds simulating a
   │                                    continuous listener).
   ▼
tea.Msg (typed as a conv.* msg)
   │
   ▼
Update → routeToSubModel                update.go
   └─ conv.Update(m, &m.conv, msg)      app/conv/update.go
         │
         │ Routes by event type. The streaming flow is:
         │
         ▼
   PreInfer                             applyPreInfer
       ├─ m.Stream.Active = true
       ├─ m.Append({Role: assistant})   empty assistant message — chunks
       │                                will be appended to it
       └─ start spinner
        │
        ▼
   OnChunk (one per LLM token batch)    applyChunk
       ├─ m.AppendToLast(text)          grow the in-progress message
       └─ if chunk.Done && no tools:
              Stream.Active = false
              rt.CommitMessages()       promote to scrollback (see below)
        │
        ▼
   PostInfer                            applyPostInfer
       ├─ rt.OnTokenUsage(resp)         model_agent_events.go
       └─ if tool calls: track them
        │
        ▼
   PreTool / PostTool                   tool execution stream
       ├─ applyPreTool                  show "running tool X" spinner
       └─ applyPostTool
             └─ rt.OnToolResult(tr)     model_agent_events.go
        │
        ▼
   end-of-turn event                    rt.OnTurnEnd(result)
        │                               model_agent_events.go
        ├─ m.CommitMessages()           model_scrollback.go
        ├─ m.drainTurnQueues()          model_turn_queue.go (Path C)
        └─ fire idle hooks

   rt.OnAgentStop(err)                  turn ended (or canceled)
   rt.OnAutoCompact / OnCompactResult / OnTokenLimitResult ...
```

### Where the user sees streaming text

The terminal window has two distinct surfaces during a session:

```
   terminal native scrollback                  Bubble Tea repaint zone
   (text you can scroll up to                  (the bottom N lines
    review; never repainted —                   redrawn every Update;
    written line-by-line via                    contents discarded
    tea.Println)                                between repaints)
```

Same window, but written by two different mechanisms. Bubble Tea owns
the bottom N lines; everything above is regular terminal output it
emitted with `tea.Println`.

**While a message is streaming** the in-progress text lives in the
repaint zone, not the scrollback. Each OnChunk grows the assistant
message in `m.conv.Messages`; View() rebuilds the repaint zone every
Update so the user sees it advance token-by-token:

```
   ─── terminal scrollback (frozen) ──────────────────
     user: write a poem about the sea               ← committed
   ─── Bubble Tea repaint zone (repainted) ──────────
     assistant: Whispers of waves on ancient
                stone, the tides▮                   ← in conv.Messages,
                                                      Stream.Active=true,
                                                      NOT yet committed
     ─────────────────────────────────────────────
     ❯ (textarea waits, disabled mid-stream)
```

**When the stream finishes** (last OnChunk with `Done=true` and no tool
calls), `CommitMessages` calls `tea.Println` for the completed message.
That **lifts** the message out of the repaint zone and into the
scrollback above:

```
   ─── terminal scrollback (frozen) ──────────────────
     user: write a poem about the sea
     assistant: Whispers of waves on ancient
                stone, the tides retreat and        ← now committed,
                return, ...                            written once via
                                                       tea.Println
   ─── Bubble Tea repaint zone (repainted) ──────────
     (empty — CommittedCount caught up with len(Messages))
     ─────────────────────────────────────────────
     ❯ type your message...
```

The rule preventing the message from appearing in both places at once
is in `renderAndCommit(checkReady=true)`: it never commits the last
message while `Stream.Active == true`. So during streaming the message
is only in the repaint zone; once the stream finishes, exactly one
`tea.Println` moves it to scrollback and `CommittedCount` advances so
the repaint zone no longer redraws it.

There is also a renderer-level ordering rule at this boundary. Bubble Tea
normally queues the latest `View()` and flushes it on a frame ticker, while
`tea.Println` inserts into scrollback immediately. San's local Bubble Tea patch
flushes the queued managed frame synchronously before `insertAbove` calculates
its scroll geometry. Without that barrier, `CommittedCount` can hide content
in the logical view before the shorter frame reaches the terminal, allowing
rows from the previous live frame to become permanently embedded in native
scrollback. The replacement in `go.mod` is pinned to
[`yanmxa/bubbletea@81ba608`](https://github.com/yanmxa/bubbletea/commit/81ba608a2d90bacdc115d6b3acc6b1c12cb44e5d);
remove it once [upstream issue #1736](https://github.com/charmbracelet/bubbletea/issues/1736)
ships an equivalent barrier.

Tool-call spinners live in the repaint zone the same way: they appear
while a tool is running and disappear once the result lands.

## Path E — Cancel mid-stream and resume

User hits **Esc** or **Ctrl+C** while the agent is streaming. The agent
goroutine stays alive — only the in-flight turn is cancelled — and a
follow-up user message resumes the same session via the inbox channel.

```
   UI / tea.Update           Agent goroutine             Provider stream goroutine
   ───────────────           ───────────────             ─────────────────────────

   Esc key                   in ThinkAct/streamInfer     mid HTTP stream,
   ──▶ handleStreamCancel    turn = &turnHandle{c,d}     EmitText(ctx, ch, …)
       │
       │ 1. Agent.InterruptTurn()
       │      ├─ interruptPending.Store(true)
       │      ├─ h := turn.Swap(nil)
       │      ├─ h.cancel()  ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─▶  turnCtx.Done() fires
       │      │                                       streamInfer returns
       │      │                                       execTools Phase 3 breaks
       │      │                                       EmitText select picks
       │      │                                       ctx.Done → no leak
       │      │                                       ThinkAct returns
       │      │                                       close(h.done) ──┐
       │      └─ <-h.done   (≤ 250 ms)  ◀ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─┘
       │
       │ 2. Reminder.Enqueue(InterruptReminder)
       │      └─ "The previous response was interrupted by the user."
       │         attached to the NEXT user message via the same
       │         <system-reminder> channel skills/memory already use.
       │
       │ 3. conv-side UI updates (display only — never sync into agent)
       │      ├─ Stream.Stop / hide modals / drain pending questions
       │      ├─ cancelPendingToolCalls   → cancelled tool_result rows
       │      └─ MarkLastInterrupted      → ⏸ interrupted badge on
       │                                    the last assistant
       │
       │ 4. CommitMessages + drainInputQueueAfterCancel
       │
       │                          inner break (turn cancel detected),
       │                          interruptPending.Store(false),
       │                          TurnEvent(StopCancelled) emitted
       │
       │   ◀── TurnEvent ─────────┤
       │   OnTurnEnd: StopReason==Cancelled
       │              → skip idle hooks, no Stop/Notification hook fires
       │
       │                          outer loop: waitForInput (idle)

   user types "do B instead"
   ──▶ SubmitToAgent
       └─ ensureAgentSession sees Active=true — NO rebuild
       └─ sendToAgent → attachPendingReminders
                          → "<system-reminder>The previous response
                             was interrupted…</system-reminder>do B"
       └─ Agent.Send ──────────▶  inbox
                                  waitForInput unblocks
                                  loop top: interruptPending=false → proceed
                                  new turnHandle, fresh ThinkAct
                                                                ─▶ new stream
```

### Why agent state is never touched on cancel

The cancel handler does not synthesize any marker into `a.messages` and
does not push conv state back into the agent — both layers stay on
their own copy with their own IDs. This works because:

- **Orphaned `tool_use` blocks are sanitized at convert time.** If the
  cancel happened after `PostInfer` appended an assistant with
  `tool_calls` but before all results came back, the next inference's
  convert pass (anthropic `sanitizeToolResults`,
  openaicompat `SanitizeToolMessages`) strips the unmatched
  `tool_use` blocks before sending to the provider.
- **Consecutive user messages are tolerated.** Anthropic's converter
  calls `mergeConsecutiveMessages`; OpenAI / DeepSeek / Moonshot etc.
  accept user-after-user without complaint.
- **The interrupt signal rides the reminder.** Instead of injecting a
  fake assistant or user turn, the cancel handler enqueues a one-shot
  reminder body; the existing `attachPendingReminders` wraps it in a
  `<system-reminder>` block on the very next user submission. The
  model gets an explicit "previous turn was interrupted" cue without
  any synthetic message in the chain.

Three pieces carry the cancel safely:

| Mechanism | What it protects |
|---|---|
| `turn atomic.Pointer[turnHandle]` | The "live turn handle" — `Swap(nil)` makes the cancel atomic so two interrupts can't double-cancel the next turn. |
| `interruptPending atomic.Bool` | Latches an interrupt that lands between turns (when `turn` is momentarily nil), so the next iteration of Run's inner loop bails to `waitForInput` instead of running an unwanted ThinkAct. |
| `turnHandle.done` chan + 250 ms timeout | Lets the UI wait for ThinkAct to actually unwind before returning to the user — keeps spinner / modal state from racing the agent's last few ctx-cancelled emits. The timeout is a backstop for a tool that ignores ctx; in practice the wait is sub-millisecond. |

Why this matters versus the pre-rewrite path: the old cancel called
`Agent.Stop`, killed the goroutine, and rebuilt the entire agent on the
next message — full `buildAgent`, fresh `llm.Client`, plus a spurious
Stop/Start event pair in the session record. The new path keeps the
agent alive, so the next `Agent.Send` is just an inbox push and the
LLM provider sees the same conversation prefix (better server-side
prompt cache behaviour).

## File pointers

| Path step | File |
|---|---|
| `Update` dispatch | [`internal/app/update.go`](../../internal/app/update.go) |
| Keyboard handling | [`internal/app/update_keys.go`](../../internal/app/update_keys.go) |
| Submit + SubmitToAgent | [`internal/app/update_submit.go`](../../internal/app/update_submit.go) |
| Slash command controller | [`internal/app/input/slash_command.go`](../../internal/app/input/slash_command.go) |
| Slash command env builder | [`internal/app/update_command.go`](../../internal/app/update_command.go) |
| Inject paths (cron/hook/hub) | [`internal/app/model_turn_queue.go`](../../internal/app/model_turn_queue.go) |
| Agent event callbacks | [`internal/app/model_agent_events.go`](../../internal/app/model_agent_events.go) |
| Scrollback commit | [`internal/app/model_scrollback.go`](../../internal/app/model_scrollback.go) |
| Conv event router | [`internal/app/conv/update.go`](../../internal/app/conv/update.go) |
| `agent.Send` / outbox poll | [`internal/app/agent.go`](../../internal/app/agent.go) |
| Cancel mid-stream | [`internal/app/update_input_effects.go`](../../internal/app/update_input_effects.go) |
| `InterruptTurn` | [`internal/agent/session.go`](../../internal/agent/session.go) |
| `turn` / `InterruptCurrentTurn` / Run loop | [`internal/core/agent_impl.go`](../../internal/core/agent_impl.go) |
| Bottom UI compose | [`internal/app/view.go`](../../internal/app/view.go) |
