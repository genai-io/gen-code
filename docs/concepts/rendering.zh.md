# 渲染：Model → 终端

> English version: [`rendering.md`](rendering.md)

[`data-flow.zh.md`](data-flow.zh.md) 的姊妹篇。数据流转讲的是输入怎么变成
状态；本文讲的是状态怎么变成屏幕上的字符。

> **"渲染"在这个代码库里的意思是：返回一个字符串。** 所有 `Render*`
> 函数返回的就是 `string`——ANSI 转义码控制颜色和样式，UTF-8 字符是
> 内容。没有 off-screen buffer，没有 canvas。Bubble Tea 的 `View()`
> 返回一个 string，框架把它写到终端；`tea.Println` 接受一个 string，
> 把它写到 alt-screen 区域之上。下面的"渲染管线"全程都是字符串组装；
> 真正的"画"是终端干的。

## 心智模型：两块表面

会话期间终端窗口有**两块表面**，每条渲染出的字符串都恰好落到其中一块：

```
m.conv.Messages = [ msg0, msg1, msg2, msg3 | msg4, msg5 ]
                                            ▲
                                            CommittedCount = 4

┌─ 表面 ───────────────────┬─ 写入方式 ─────────┬─ 内容 ────────────────┐
│ 终端原生 scrollback       │ tea.Println        │ msg0..msg3            │
│ （写入即冻结；可以        │ （每条 commit 消息  │ — 已 commit 的消息    │
│  滚轮翻回去看）           │  调一次）           │                       │
├──────────────────────────┼────────────────────┼───────────────────────┤
│ Bubble Tea 重绘区         │ View()             │ msg4..msg5 +          │
│ （底部 N 行；每次 Update  │ （每次 Update      │ pending spinner +     │
│  整块重画）               │  都调）             │ 输入条                │
└──────────────────────────┴────────────────────┴───────────────────────┘
```

**流式中**的 assistant 回复待在重绘区，期间 `Stream.Active == true`；
流完时 `CommitMessages` 调一次 `tea.Println`，把**同一段渲染好的字符串**
搬到上面的 scrollback。`CommittedCount` 前进一格，重绘区就不再画这条。
用户看到的是一次视觉过渡，不是重复显示。防止双重渲染的规则在
`renderAndCommit(checkReady=true)` 里：`Stream.Active` 为 true 时绝不
commit 最后一条消息。

**两块表面用同一套渲染函数。** `RenderMessageAt` 产出每条消息的字符串；
差别只在消息索引范围（scrollback：`0..CommittedCount`；重绘区：
`CommittedCount..len(Messages)`）。

## View() 组合出重绘区

[`internal/app/view.go`](../../internal/app/view.go) 里的
`(*model).View()` 每次 `Update` 后都跑一遍，返回重绘区那串字符。

```go
func (m *model) View() string {
    //   ^ Go 里 *model 上的方法；`m` 是当前实例
    //     （相当于其它语言的 `this`/`self`）。
    //     整个 codebase 都用 `m` 指代前台 model。
    ...
}
```

**没有输入参数**——这是 Bubble Tea 的约定。View 要读的全部状态都来自
`m` 的字段。它派发的子渲染函数（`RenderActiveContent` 等）接收
`conv.RenderContext` struct，这个 struct 是 `m.messageRenderParams()`
每次调用时从 `m` 的字段组装出来的。

View 从四种布局里挑一种，自顶向下：

```
View()
  1. !m.env.Ready              ──► "\n  Loading..."
  2. 有 popup 活动？           ──► popup.Render() — 全屏
                                   （slash 命令选择器：/models、
                                   /tools、/skills 等）
  3. 有 modal 活动？           ──► modal.Render() 夹在分隔符之间
                                   （Question modal、Approval modal）
  4. 否则（普通模式）         ──► renderNormalView()
        ├─ chat section        ── conv.RenderActiveContent
        ├─ 本回合 token 用量
        ├─ 分隔符
        ├─ 队列预览            ── 流式期间排队的输入
        ├─ textarea
        ├─ suggestion list     ── /-命令、@-文件名的自动补全
        ├─ 分隔符
        └─ status line         ── 模型名、token、模式
```

Popup（全屏）和 modal（夹在中间）听起来差不多，但渲染流程不同——modal
后面还能看到 chat，popup 后面看不到。

## 单条消息怎么渲染

`RenderMessageAt(ctx, idx, isStreaming)` 按 `msg.Role` 派发：

```
                ┌── Role: User ──┐
                │                │
                │ 含 ToolResult? ──► RenderToolResultInline
                │ 否则           ──► RenderUserMessage
                │                     （文字 + 图片，md 渲染）
                │
RenderMessageAt ─┤
                │
                ├── Role: Notice ──► RenderSystemMessage
                │                     （纯文本，灰色）
                │
                └── Role: Assistant ──► renderAssistantWithTools
                                          ├─ assistant 文字 + thinking
                                          │   （md 渲染）
                                          └─ 工具调用块（每个调用 +
                                              配对的 result inline，
                                              如果已有结果的话）
```

`renderAssistantWithTools` **不会**扫消息列表去找它的配对 result——
`ctx.InlinedResults` 在渲染开始就预算好了，告诉它哪个
`ToolCallID → ToolResultData` 该 inline。见下面"工具调用 + inline 结果"。

## Markdown 走 MDRenderer

[`internal/app/conv/markdown.go`](../../internal/app/conv/markdown.go)
包了 [glamour](https://github.com/charmbracelet/glamour)。五处行为是
有意为之，不是 glamour 默认值：

| 关注点 | 行为 |
| --- | --- |
| 宽度 | 按当前终端宽度构建，减 4 给 `● ` 缩进。`ResizeMDRenderer` 在 `WindowSizeMsg` 时重建。 |
| 背景 | 自动检测深色/浅色。每次 `Render` 在内部 `rebuildIfNeeded()`，主题切换就重建。 |
| 表格 | 在 glamour 看到之前抽出来，用 lipgloss 表格原语渲染，边框可控。 |
| 软换行 | LLM 在 ~80 列硬换行；把软换行合成段落后再交给 glamour 按真实宽度换行。 |
| 内联标记 | 自定义内联 markdown pass 处理 glamour 渲染不好的部分（如嵌套格式里的反引号）。 |

宽度为什么重要：glamour 根据配置宽度算列宽。终端 resize 后，scrollback
里的内容是按旧宽度换行的，但重绘区按新宽度。这正是 `reflowScrollback`
要解决的问题（见下文 Resize 一节）。

## 工具调用 + inline 结果

[`internal/app/conv/tool_render.go`](../../internal/app/conv/tool_render.go)
渲染 assistant 下面的工具调用块：

```
● Bash(npm test)                        ← 工具名 + 摘要参数
    ⎿  > vitest run                     ← 折叠的结果预览
        ✓ src/foo.test.ts (12)
        ✓ src/bar.test.ts (8)
       … 47 more lines (Ctrl-O to expand)
```

驱动渲染的状态：

- **Pending vs done** — 工具调用在 `m.conv.Tool.PendingCalls` 里直到
  对应的 `ToolResult` 到达；pending 期间工具名旁显示 spinner。
- **Expanded / collapsed** — 消息级别的 `Expanded`，Ctrl-O 切换。折叠
  显示预览 + 行数；展开显示全部内容。
- **错误** — `ToolResult.IsError` 翻转图标 ✓ → ✗ 并把结果染色。
- **并行模式** — 多个工具调用并发跑时，每个调用独立显示进度。

assistant 的工具调用和它的 result 消息怎么配对——一开始就由
`PrecomputeInlinedResults(messages)` 预算好，挂在
`RenderContext.InlinedResults` 上。三个 lookup 消费它：

```
InlinedResults.ownerOf(resultIdx)       // 这条 result 归哪个 assistant 拥有？
                                         // RenderMessageRange 用它来跳过
                                         // 这条 result（它已经 inline 画了）

InlinedResults.resultsFor(assistantIdx)  // 这条 assistant 的
                                         // (callID → ToolResultData) map
                                         // renderAssistantWithTools 用

InlinedResults.IsResultInlined(idx)      // 这条 result 是否会被
                                         // inline 画在 assistant 下？
                                         // RenderSingleMessage 用它跳过
                                         // 独立的 Println
```

一次扫描预算，三处消费，再不重新扫了。

## 实例走查：流式回复 + 工具调用

走一遍完整路径。用户敲了 `list files` 并按 Enter——那一段是输入流
（[data-flow.zh.md](data-flow.zh.md) Path A）；下面从 agent goroutine
开始往 Outbox 发事件的那一刻接着往下讲。

起点：`conv.Messages` 是 `[user "list files"]`，`CommittedCount=1`
（user 消息已经被 Enter handler commit 了）。

### Step 1 — PreInfer：开一个空的 assistant 桩位

```
event:           core.PreInfer
applyPreInfer:   m.Stream.Active = true
                 m.Append({Role: assistant, Content: ""})
                 启动 spinner

conv.Messages:   [user, assistant{Content:""}]
CommittedCount:  1   （只有 user 是已 commit）
```

这次 Update 之后 View() 跑：

```
View → renderNormalView
     → conv.RenderActiveContent(ctx)
       ctx.InlinedResults = PrecomputeInlinedResults(Messages)
         = {} （还没有 ToolCalls）
       → RenderMessageRange(ctx, startIdx=1, endIdx=2, includeSpinner=true)
         i=1: ownerOf(1) = -1（不是 result）→ 不跳过
              isStreaming = (1 == lastIdx && Stream.Active && role==assistant)
                          = true
              → RenderMessageAt(ctx, 1, isStreaming=true)
                → renderAssistantWithTools(ctx, msg, 1, isLast=true)
                  → RenderAssistantMessage(content="", streamActive=true,...)
                    返回 "● ▮" 这种桩位
                  msg.ToolCalls == nil → 直接返回 base
         + 还有 pending-tool spinner
```

重绘区显示 `● ▮ ⋯`。Scrollback 不动。

### Step 2 — OnChunk（文字）：消息变长

```
event:           core.OnChunk{Text: "我用 ls 列一下。", Done: false}
applyChunk:      m.AppendToLast(text, "")

conv.Messages:   [user, assistant{Content:"我用 ls 列一下。"}]
Stream.Active:   仍然 true（Done=false）
```

和 Step 1 同样的调用链，但 `RenderAssistantMessage` 这次看到非空内容，
`MDRenderer.Render` 给它上 style。重绘区：`● 我用 ls 列一下。 ▮ ⋯`。
后面还会来几个 OnChunk，每个都是 `AppendToLast` + 一次 View() 重画。

### Step 3 — PostInfer：工具调用挂到 assistant 消息上

```
event:           core.PostInfer{Response: {ToolCalls: [{ID:"tc-1", Name:"Bash", Input:{cmd:"ls"}}]}}
applyPostInfer:  rt.OnTokenUsage(resp)
                 m.SetLastToolCalls(resp.ToolCalls)
                 m.Tool.Track(resp.ToolCalls)

conv.Messages:   [user,
                  assistant{Content:"我用 ls 列一下。", ToolCalls:[tc-1]}]
```

`renderAssistantWithTools` 这次走 ToolCalls 那条分支：

```
base = RenderAssistantMessage(...)             ← 文本部分
msg.ToolCalls != nil
resultMap = ctx.InlinedResults.resultsFor(1)
          = nil                                 ← tc-1 还没出结果
RenderToolCalls(ToolCallsParams{
  ToolCalls:    [tc-1],
  ResultMap:    {},                             ← nil → 空 map
  PendingCalls: [tc-1],                         ← 驱动 spinner
  CurrentIdx:   0,
  SpinnerView:  "⋯",
  ...
})
```

重绘区现在显示：

```
● 我用 ls 列一下。
  ⋯ Bash(ls)
```

### Step 4 — PostTool：结果到了，配对 inline

```
event:           core.PostTool{Result: {ToolCallID:"tc-1", Content:"file1\nfile2"}}
m.ProcessToolResult(tr):
  applyToolSideEffects(...)
  firePostToolHook(...)
  （agent 把 ToolResult 作为 user-role message append 进来）

conv.Messages:   [user "list files",
                  assistant{Content+ToolCalls:[tc-1]},
                  user{ToolResult:{ToolCallID:"tc-1", Content:"file1\nfile2"}}]
```

View() 重建 `ctx`。**InlinedResults 在这里发挥作用：**

```
PrecomputeInlinedResults(Messages):
  i=1 是 assistant，ToolCalls=[tc-1]；往后扫:
    j=2: ToolResult.ToolCallID == "tc-1" → 配对
  resultOwner         = {2: 1}
  resultsForAssistant = {1: {"tc-1": ToolResultData{Content:"file1\nfile2", ...}}}

RenderMessageRange(ctx, 1, 3, includeSpinner=true):
  i=1（assistant）:
    ownerOf(1) = -1（不是 result）→ 渲染
    renderAssistantWithTools:
      resultMap = resultsFor(1) = {"tc-1": ToolResultData{...}}    ← 现在有了
      RenderToolCalls 把 "● Bash(ls)" 画出来，结果 inline 在下面
  i=2（ToolResult）:
    ownerOf(2) = 1，>= startIdx → SKIP
    （已经在 assistant 块里画过了；再单独画一次就是重复）
```

重绘区：

```
● 我用 ls 列一下。
  ● Bash(ls)
      ⎿  file1
         file2
```

### Step 5 — OnChunk(Done)：把整块送进 scrollback

```
event:           core.OnChunk{Done: true, Response: {...}}
applyChunk:      m.AppendToLast(...)       （可能还有最后一段文字 chunk）
                 if chunk.Done && 没有未完成的工具调用:
                     m.Stream.Active = false
                     return rt.CommitMessages()
```

`CommitMessages → renderAndCommit(checkReady=true)`：

```
for i in CommittedCount..len(Messages):    // i = 1, 2
  msg = Messages[i]
  if checkReady && i == lastIdx && role==assistant && Stream.Active:
      break                                  // Stream.Active 已经是 false
  rendered = conv.RenderSingleMessage(ctx, i)
    i=1: RenderMessageAt(ctx, 1, false)      // 不再 streaming → 不画光标
         返回和 Step 4 同样的 assistant + 工具块
    i=2: msg.ToolResult != nil
         InlinedResults.IsResultInlined(2) = true → return ""    ← 跳过
  if rendered != "": 加到 parts

tea.Println(strings.Join(parts, "\n"))       // 一次 Println，一整块
CommittedCount = 3                           // 追上
```

屏幕上的变化：

- **Scrollback** 多出一整块：
  `● 我用 ls 列一下。 / ● Bash(ls) / ⎿ file1 / file2`。冻在那儿。
- **重绘区** 现在空了（`CommittedCount == len(Messages)`）。
- 下一次 `View()` 只画底部输入条——等下一条用户消息。

刚才用户看到一直在增长的同一段字符，现在原原本本住进了 scrollback——
通过**一次** `tea.Println` 写过去的。`RenderSingleMessage` 里
`IsResultInlined` 的 short-circuit 是阻止 ToolResult 被独立 Println
一遍的关键。

## Resize 行为

终端 resize 是**唯一会让已经写到 scrollback 里的内容失效**的事件
（glamour 按配置宽度换行）。
[`internal/app/update_resize.go`](../../internal/app/update_resize.go)
里的 `handleWindowResize`：

1. 更新 `m.env.Width / Height` 和 textarea 宽度
2. `m.conv.ResizeMDRenderer(newWidth)`——按新宽度重建 glamour
3. 宽度真的变了且已经有 commit 的消息：`reflowScrollback` 清屏，
   用新宽度对每条 commit 消息重新 `tea.Println` 一次
4. Bubble Tea 接着调 `View()` 用新宽度重画底部条

## 文件指路

| 关注点 | 文件 |
| --- | --- |
| `View()` 组合 | [`internal/app/view.go`](../../internal/app/view.go) |
| 单消息渲染 + 配对 | [`internal/app/conv/view.go`](../../internal/app/conv/view.go) |
| User / Assistant / Notice 渲染 | [`internal/app/conv/message.go`](../../internal/app/conv/message.go) |
| Markdown 渲染 | [`internal/app/conv/markdown.go`](../../internal/app/conv/markdown.go) |
| 工具调用 / 结果渲染 | [`internal/app/conv/tool_render.go`](../../internal/app/conv/tool_render.go) |
| Compact / 进度 / tracker | [`internal/app/conv/compact.go`](../../internal/app/conv/compact.go)、[`progress.go`](../../internal/app/conv/progress.go)、[`tracker_view.go`](../../internal/app/conv/tracker_view.go) |
| `MDRenderer` 生命周期 | [`internal/app/conv/model.go`](../../internal/app/conv/model.go) |
| Scrollback commit | [`internal/app/model_scrollback.go`](../../internal/app/model_scrollback.go) |
| Resize + reflow | [`internal/app/update_resize.go`](../../internal/app/update_resize.go) |
