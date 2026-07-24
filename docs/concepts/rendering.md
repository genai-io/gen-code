# Rendering: Model → Terminal

> 中文版本：[`rendering.zh.md`](rendering.zh.md)

Companion to [`data-flow.md`](data-flow.md). Data flow covers how input
becomes state; this doc covers how state becomes characters on screen.

> **"Render" means: return a string.** Every `Render*` function in
> this codebase returns a `string` — ANSI escape codes for color and
> style, plain UTF-8 for content. No off-screen buffers, no canvases.
> Bubble Tea's `View()` returns a string and the framework writes it
> to the terminal; `tea.Println` takes a string and writes it above
> the alt-screen region. The "rendering pipeline" below is entirely
> string composition; the terminal does the actual drawing.

## Mental model: two surfaces

The terminal window has **two surfaces** during a session, and every
rendered string ends up on exactly one of them:

```
m.conv.Messages = [ msg0, msg1, msg2, msg3 | msg4, msg5 ]
                                            ▲
                                            CommittedCount = 4

┌─ surface ──────────────────┬─ written by ───────┬─ contents ────────────┐
│ Native terminal scrollback │ tea.Println        │ msg0..msg3            │
│ (frozen once written;      │ (one call per      │ — committed messages  │
│  you can scroll up to it)  │  committed message)│                       │
├────────────────────────────┼────────────────────┼───────────────────────┤
│ Bubble Tea repaint zone    │ View()             │ msg4..msg5 +          │
│ (bottom N lines; rebuilt   │ (called every      │ pending spinners +    │
│  on every Update)          │  Update)           │ input strip           │
└────────────────────────────┴────────────────────┴───────────────────────┘
```

A **streaming** assistant reply lives in the repaint zone while
`Stream.Active == true`; when the stream finishes, `CommitMessages`
makes one `tea.Println` call to **move** the same rendered string into
scrollback above. `CommittedCount` then advances so the repaint zone
stops re-drawing it. The user sees one visual transition, not a
duplicate. The rule that prevents double-rendering is in
`renderAndCommit(checkReady=true)`: never commit the last message while
`Stream.Active` is true.

**Both surfaces share the same render functions.** `RenderMessageAt`
is what produces each message's string; what differs is the index
range (scrollback: `0..CommittedCount`; repaint zone:
`CommittedCount..len(Messages)`).

## View() composes the repaint zone

`(*model).View()` in [`internal/app/view.go`](../../internal/app/view.go)
runs after every `Update` and returns the string for the repaint zone.

```go
func (m *model) View() string {
    //   ^ Go method on *model; `m` is the instance (Go's
    //     equivalent of `this`/`self`). The whole codebase uses `m`
    //     for the foreground model.
    ...
}
```

**No input parameters** — that's a Bubble Tea contract. Everything
View reads comes from `m`'s fields. The sub-renderers it calls
(`RenderActiveContent` etc.) take a `conv.RenderContext` struct, which
`m.messageRenderParams()` assembles from `m`'s fields on each call.

View() picks one of four layouts, top-down:

```
View()
  1. !m.env.Ready              ──► "\n  Loading..."
  2. active popup?             ──► popup.Render() — fullscreen
                                   (slash-command pickers: /models,
                                   /tools, /skills, ...)
  3. active modal?             ──► modal.Render() wrapped between
                                   separator bars
                                   (Question modal, Approval modal)
  4. otherwise (normal mode) ──► renderNormalView()
        ├─ chat section        ── conv.RenderActiveContent
        ├─ turn-usage summary
        ├─ separator
        ├─ queue preview       ── if input was queued during a stream
        ├─ textarea
        ├─ suggestion list     ── /-command and @-file autocomplete
        ├─ separator
        └─ status line         ── model name, tokens, mode
```

Popups (full-screen) and modals (wrapped) look like the same idea but
have different render flows because the chat content stays visible
behind a modal, not behind a popup.

## How a single message is rendered

`RenderMessageAt(ctx, idx, isStreaming)` dispatches by `msg.Role`:

```
                ┌── Role: User ──┐
                │                │
                │ ToolResult?    ──► RenderToolResultInline
                │ otherwise      ──► RenderUserMessage
                │                     (text + images, md-rendered)
                │
RenderMessageAt ─┤
                │
                ├── Role: Notice ──► RenderSystemMessage
                │                     (plain text, muted color)
                │
                └── Role: Assistant ──► renderAssistantWithTools
                                          ├─ assistant text + thinking
                                          │   (md-rendered)
                                          └─ tool-calls block (each
                                              call + its inlined
                                              result, if available)
```

`renderAssistantWithTools` does **not** scan the message list to find
its paired results — `ctx.InlinedResults` was precomputed once at the
top of the render pass and tells it which `ToolCallID → ToolResultData`
entries to inline. See "Tool calls and inlined results" below.

## Markdown via MDRenderer

[`internal/app/conv/markdown.go`](../../internal/app/conv/markdown.go)
wraps [glamour](https://github.com/charmbracelet/glamour). Five
behaviors are intentional and not glamour defaults:

| Concern | Behavior |
| --- | --- |
| Width | Built for the current terminal width, minus 4 for the `● ` indent. `ResizeMDRenderer` rebuilds it on `WindowSizeMsg`. |
| Background | Auto-detects dark vs light. `rebuildIfNeeded()` rebuilds inside `Render` if the terminal flipped themes. |
| Tables | Pulled out before glamour sees them; rendered with lipgloss table primitives for full border control. |
| Soft line breaks | LLMs hard-wrap at ~80 cols. Soft-wrapped paragraphs get joined before glamour so it can re-wrap at the real width. |
| Inline tokens | A custom inline-markdown pass styles things glamour handles poorly (e.g. backticks inside other formatting). |

Width matters: glamour computes column widths from its configured
width. If the terminal resizes, glamour-wrapped content already in
scrollback is now sized for the old width but the repaint zone uses
the new width. That mismatch is exactly what `reflowScrollback`
addresses (see Resize below).

## Tool calls and inlined results

[`internal/app/conv/tool_render.go`](../../internal/app/conv/tool_render.go)
renders the tool-calls block under an assistant message:

```
● Bash(npm test)                        ← tool name + summary args
    ⎿  > vitest run                     ← collapsed result preview
        ✓ src/foo.test.ts (12)
        ✓ src/bar.test.ts (8)
       … 47 more lines (Ctrl-O to expand)
```

State that drives it:

- **Pending vs done** — a tool call sits in `m.conv.Tool.PendingCalls`
  until its `ToolResult` arrives. While pending, the tool name shows
  a spinner.
- **Expanded / collapsed** — per-message `Expanded` flag, toggled by
  Ctrl-O. Collapsed = preview + line count; expanded = full content.
- **Error** — `ToolResult.IsError` flips the icon ✓ → ✗ and tints the result.
- **Parallel mode** — when multiple tool calls run in parallel, each
  call shows its own progress.

The pairing between an assistant's tool calls and their result messages
is precomputed by `PrecomputeInlinedResults(messages)` and lives on
`RenderContext.InlinedResults`. Three lookups consume it:

```
InlinedResults.ownerOf(resultIdx)      // which assistant owns this result?
                                        // used by RenderMessageRange to skip
                                        // the result (it's drawn inline)

InlinedResults.resultsFor(assistantIdx) // (callID → ToolResultData) for an
                                        // assistant; used by
                                        // renderAssistantWithTools

InlinedResults.IsResultInlined(idx)     // is this result already going to
                                        // be drawn under its owner?
                                        // used by RenderSingleMessage to
                                        // skip standalone Println
```

One pass over the message list, three consumers, zero re-scanning.

## Worked example: streaming reply + tool call

End-to-end trace. The user typed `list files` and pressed Enter; that
part is the input flow ([data-flow.md](data-flow.md) Path A). Below
picks up at the moment the agent goroutine starts emitting events.

`conv.Messages` starts as `[user "list files"]` with
`CommittedCount=1` (the user message was already committed by the
Enter handler).

### Step 1 — PreInfer: open an empty assistant stub

```
event:           core.PreInfer
applyPreInfer:   m.Stream.Active = true
                 m.Append({Role: assistant, Content: ""})
                 start spinner

conv.Messages:   [user, assistant{Content:""}]
CommittedCount:  1   (only user is committed so far)
```

View() runs after this Update:

```
View → renderNormalView
     → conv.RenderActiveContent(ctx)
       ctx.InlinedResults = PrecomputeInlinedResults(Messages)
         = {} (no ToolCalls anywhere yet)
       → RenderMessageRange(ctx, startIdx=1, endIdx=2, includeSpinner=true)
         i=1: ownerOf(1) = -1 (not a result) → don't skip
              isStreaming = (1 == lastIdx && Stream.Active && role==assistant)
                          = true
              → RenderMessageAt(ctx, 1, isStreaming=true)
                → renderAssistantWithTools(ctx, msg, 1, isLast=true)
                  → RenderAssistantMessage(content="", streamActive=true,...)
                    returns the "● ▮" stub
                  msg.ToolCalls == nil → just return base
         + pending-tool spinner
```

Repaint zone shows `● ▮ ⋯`. Scrollback unchanged.

### Step 2 — OnChunk (text): grow the message

```
event:           core.OnChunk{Text: "I'll list them with ls.", Done: false}
applyChunk:      m.AppendToLast(text, "")

conv.Messages:   [user, assistant{Content:"I'll list them with ls."}]
Stream.Active:   still true (Done=false)
```

Same call chain as Step 1, but `RenderAssistantMessage` now has
non-empty content and `MDRenderer.Render` styles it. Repaint zone:
`● I'll list them with ls. ▮ ⋯`. More OnChunks may follow — each is
`AppendToLast` + a View() repaint.

### Step 3 — PostInfer: tool calls land on the assistant message

```
event:           core.PostInfer{Response: {ToolCalls: [{ID:"tc-1", Name:"Bash", Input:{cmd:"ls"}}]}}
applyPostInfer:  rt.OnTokenUsage(resp)
                 m.SetLastToolCalls(resp.ToolCalls)
                 m.Tool.Track(resp.ToolCalls)

conv.Messages:   [user,
                  assistant{Content:"I'll list them with ls.", ToolCalls:[tc-1]}]
```

`renderAssistantWithTools` now takes the second branch:

```
base = RenderAssistantMessage(...)             ← the text part
msg.ToolCalls != nil
resultMap = ctx.InlinedResults.resultsFor(1)
          = nil                                 ← tc-1 hasn't finished
RenderToolCalls(ToolCallsParams{
  ToolCalls:    [tc-1],
  ResultMap:    {},                             ← nil → empty
  PendingCalls: [tc-1],                         ← spinner driver
  CurrentIdx:   0,
  SpinnerView:  "⋯",
  ...
})
```

Repaint zone now shows:

```
● I'll list them with ls.
  ⋯ Bash(ls)
```

### Step 4 — PostTool: result arrives, gets inlined

```
event:           core.PostTool{Result: {ToolCallID:"tc-1", Content:"file1\nfile2"}}
m.ProcessToolResult(tr):
  applyToolSideEffects(...)
  firePostToolHook(...)
  (the agent appends the ToolResult as a user-role message)

conv.Messages:   [user "list files",
                  assistant{Content+ToolCalls:[tc-1]},
                  user{ToolResult:{ToolCallID:"tc-1", Content:"file1\nfile2"}}]
```

View() rebuilds `ctx`. **InlinedResults earns its keep:**

```
PrecomputeInlinedResults(Messages):
  i=1 is an assistant with ToolCalls [tc-1]; scan forward:
    j=2: ToolResult.ToolCallID == "tc-1" → pair
  resultOwner         = {2: 1}
  resultsForAssistant = {1: {"tc-1": ToolResultData{Content:"file1\nfile2", ...}}}

RenderMessageRange(ctx, 1, 3, includeSpinner=true):
  i=1 (assistant):
    ownerOf(1) = -1 (not a result) → render
    renderAssistantWithTools:
      resultMap = resultsFor(1) = {"tc-1": ToolResultData{...}}    ← populated now
      RenderToolCalls draws "● Bash(ls)" with the file listing INLINE below
  i=2 (ToolResult):
    ownerOf(2) = 1, which is >= startIdx → SKIP
    (already drawn under its owning assistant; standalone render would duplicate)
```

Repaint zone:

```
● I'll list them with ls.
  ● Bash(ls)
      ⎿  file1
         file2
```

### Step 5 — OnChunk(Done): promote the block to scrollback

```
event:           core.OnChunk{Done: true, Response: {...}}
applyChunk:      m.AppendToLast(...)       (possibly a final text chunk)
                 if chunk.Done && no tool calls remaining:
                     m.Stream.Active = false
                     return rt.CommitMessages()
```

`CommitMessages → renderAndCommit(checkReady=true)`:

```
for i in CommittedCount..len(Messages):    // i = 1, 2
  msg = Messages[i]
  if checkReady && i == lastIdx && role==assistant && Stream.Active:
      break                                  // but Stream.Active is now false
  rendered = conv.RenderSingleMessage(ctx, i)
    i=1: RenderMessageAt(ctx, 1, false)      // no longer streaming → no cursor
         returns the same assistant + tool block as before
    i=2: msg.ToolResult != nil
         InlinedResults.IsResultInlined(2) = true → return ""       ← skipped
  if rendered != "": append to parts

tea.Println(strings.Join(parts, "\n"))       // ONE Println, ONE block
CommittedCount = 3                           // caught up
```

What changed on screen:

- **Scrollback** gains one block:
  `● I'll list them with ls. / ● Bash(ls) / ⎿ file1 / file2`. Frozen.
- **Repaint zone** is now empty (`CommittedCount == len(Messages)`).
- The next `View()` paints just the input strip — ready for the next
  user prompt.

The user watched the same string grow in the repaint zone; now that
same string lives in scrollback, written exactly once. The
`IsResultInlined` short-circuit in `RenderSingleMessage` is what stops
the ToolResult from also being Println'd standalone.

## Resize behavior

Terminal resize is the **only event that invalidates already-painted
scrollback** (glamour wraps at its configured width). `handleWindowResize`
in [`internal/app/update_resize.go`](../../internal/app/update_resize.go):

1. Update `m.env.Width / Height` and the textarea width.
2. `m.conv.ResizeMDRenderer(newWidth)` — rebuilds glamour at the new
   width.
3. If width actually changed and any messages are committed:
   `reflowScrollback` clears the screen and re-Printlns every committed
   message at the new width.
4. Bubble Tea calls `View()` next to repaint the bottom strip at the
   new width.

## File pointers

| Concern | File |
| --- | --- |
| `View()` composition | [`internal/app/view.go`](../../internal/app/view.go) |
| Per-message rendering + pairing | [`internal/app/conv/view.go`](../../internal/app/conv/view.go) |
| User / assistant / notice rendering | [`internal/app/conv/message.go`](../../internal/app/conv/message.go) |
| Markdown rendering | [`internal/app/conv/markdown.go`](../../internal/app/conv/markdown.go) |
| Tool call / result rendering | [`internal/app/conv/tool_render.go`](../../internal/app/conv/tool_render.go) |
| Compact / progress / tracker | [`internal/app/conv/compact.go`](../../internal/app/conv/compact.go), [`progress.go`](../../internal/app/conv/progress.go), [`tracker_view.go`](../../internal/app/conv/tracker_view.go) |
| `MDRenderer` lifecycle | [`internal/app/conv/model.go`](../../internal/app/conv/model.go) |
| Scrollback commit | [`internal/app/model_scrollback.go`](../../internal/app/model_scrollback.go) |
| Resize + reflow | [`internal/app/update_resize.go`](../../internal/app/update_resize.go) |
