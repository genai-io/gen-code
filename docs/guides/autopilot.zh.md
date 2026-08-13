# Autopilot(副驾)

## 概览

Autopilot 是 San 的自动驾驶系统,旨在最大限度减少人工介入:由一个 copilot
模型对会话进行巡航,让例行工作持续推进,只在真正需要人的时刻交还控制权。
它通过一组可独立启用的介入点(**steer**)行动 —— 提议下一步、放行灰区
工具调用、回答命令的交互问询、回答 `AskUserQuestion`,以及在回合结束后朝
mission 继续推进。默认开启自动输入提示和灰区权限判定。

用 `shift+tab` 切换到 Autopilot 模式(循环到琥珀色的 `⏵⏵ autopilot`),
用 `/autopilot` 面板配置。恢复会话(`san -r <id>`)会回到保存时所在的模式。
只想先看它跑起来的话,[`/goal`](#goal) 是最短的入口 —— 它就是下面这一整套的
一个预设。

## 六个 steer

Steer 是按需组合的开关,按自主程度从低到高排列。Autopilot 模式未开启时,
任何 steer 都不会触发 —— Suggest 除外:它的开关在任何模式下都控制自动输入提示。

| Steer | 默认 | 作用 |
|---|---|---|
| **Suggest** | **开** | 控制任何模式下的自动输入提示。关闭时完全不生成提示;开启后在输入框中显示下一条输入建议。Autopilot 正在推进 mission 时,建议围绕 mission;否则使用通用输入预测。`tab` 接受建议,`enter` 发送;它不会自行提交。 |
| **Permission** | **开** | 自动放行静态规则解不了的灰区工具调用,按可逆性、影响面、数据外泄三轴判断。工作区在 git 下时,把版本历史当作安全网:对已跟踪文件的改动属于常规活儿;git 自身那些利器(`reset --hard`、`clean -f`、`stash drop`、force-push、`branch -D`)按本次会话的意图来权衡,而不是一律拦下。真正出了工作区的仍然升级给你 —— 别处的未跟踪文件、项目之外的路径、共享的主干分支。失败即收紧:任何错误都升级给你。 |
| **Bash** | 关 | 回答已批准命令的交互问询(`Continue? [Y/n]`),仅当回答只是让已批准的动作继续;会扩大范围的一律跳过。 |
| **Skill** | 关 | 直接放行副驾的 skill 加载(不经判官)—— 一个独立的"信任 skill"开关。因为 skill 可能跑脚本,判官往往会把它升级给你;单开这个就能让副驾自动加载 skill,而不必打开整个灰区。关闭时,skill 加载回落到 Permission 判官(或你)。 |
| **Question** | 关 | 只要 mission 或对话让某个选择站得住脚,就替你回答 `AskUserQuestion` —— 宁可选保守可逆的那项,也不让整轮停在这里等你。只有当这一步确实该你拍板(不可逆、代价高、取决于你的偏好或判断)时才留给你。选项标签逐字校验 —— 部分或凭空的回答一律转为留给你。 |
| **End** | 关 | 回合结束后判断是否朝 mission 续跑,并自己敲出下一条指令。受 **Continue at most N times** 约束(默认 20,填 `0` 表示不限);计数在每次人类回合重置。没有交代 mission 时,它从对话里推断目标;对话里也看不出目标就交回给你。 |

## Mission(任务)

Mission 是副驾本会话要开往的目标 —— 在 `/autopilot` 面板的 Mission 对话框里
撰写:这是个小编辑器,你打的字就是 mission(`enter` 保存、`alt+enter` 换行、
可粘贴),`ctrl+r` 让副驾就地精炼草稿、`ctrl+c` 清空、`esc` 保存并退出。每个
steer 都读它:推进类 steer(Suggest、Question、End)朝它开 —— 没交代 mission 时
则退回到对话本身透出的目标;安全类 steer
(Permission、Bash)把它当作意图上下文 —— 明显在推进 mission 的调用或提示,会被
看作预期内的常规活儿。但意图不凌驾于安全:凡是不可逆、破坏性、越出项目、或会外泄
数据的动作,无论是否契合 mission,一律仍升级给你。

当 End steer 判定 mission **已完全达成**,会将其退役:清空 mission、steer 归位
到被动基线(Permission + Bash)—— Autopilot 保持开启,你重新接手,自动放行的
安全网仍在。

## 启动 mission

面板底部一行是两个按钮 —— **Save** 和 **Start**(`←`/`→` 选择、`enter`
执行):

- **Save** 把配置应用到实时会话,并写入 `settings.json` 作为默认种子,但不
  改变模式。只调 steer、或想稍后再用 `shift+tab` 启动时用它。
- **Start** 先做 Save 做的一切,再开启 Autopilot 并免人工发动 mission:从
  mission 推出开场那一步并自己提交 —— 交代好 mission、按下 Start 就是完整的
  启动。Start 需要一个 mission,没设时它会提示你而不是空转开启。

用 `shift+tab` 落到 Autopilot 不再自动起步,只会浮出 Suggest steer 的提议
(若开启)。发动 mission 始终是显式的 Start 按钮。

## /goal

最常见的那条路 —— 交代 mission、打开让副驾能动手的几个 steer、解除上限、
发车 —— 压成一行:

```
/goal 给 internal/setting 补表驱动测试,直到 go test ./... 全绿
```

`/goal` 是一个预设,不是另一种模式:开的还是同一个 Autopilot,只是配置固定。

- 这个目标成为 [mission](#mission任务)
- 推进类 steer 全部打开(Bash、Skill、Question、End)
- 解除续跑上限 —— 这一轮在目标达成时结束,而不是计数器耗尽时结束
- 开启 Autopilot,并由副驾自己发出第一步

在回合进行中输入,它会等当前回合落地后接管。单独输入 `/goal` 会显示当前目标;
`/goal clear` 让副驾停手。

它刻意只作用于本会话:不同于面板的 Save,它不会改写你保存的默认配置 ——
定个目标是这一次会话的事。你的 **Permission** steer 保持原样,因为把它显式
关成 `false` 是一个安全选择,不该因为人定了个目标就被推翻。目标结束时
(达成或清空)steer 会回滚到 `/goal` 接手前的样子,它打开的自主权不会活得
比目标更久。

想要别的组合就还是用面板:只提议不提交、限定续跑次数、或换一套驾驶指令。

## 让它一直自主跑下去

开着 End steer 时,那些本会让会话停下来等你的情况,副驾都会自己开过去:

- **回合半途停住**(撞上步数上限、输出截断且无法恢复)会被接着往下开,副驾知道
  上一回合是怎么停的。你自己按的 `esc` 是另一回事:取消回合意味着你要接管;
  stop hook 同理。
- **回合直接失败**时按递增退避等待(5s、10s、15s)再判断能否续跑,最多连续三次,
  任何跑到结束的回合都会重置计数。真正需要你处理的错误仍会交回给你。
- **steer 自己抽风**最多重试三次,网络抖动或没吐 JSON 都不至于终结 mission。
- **决策撞上上下文压缩**时,裁决会等压缩落地,而不是被丢掉。
- **回合数用光**:把 **Continue at most** 设为 `0`(显示为 `∞`),这一轮就在
  mission 完成时结束,而不是在计数器耗尽时结束。

不限次数的长跑,建议配一个便宜够快的 steer 模型 —— 见[配置](#配置)。

## Demo:一次免人工的脚手架搭建

两分钟跑通完整闭环 —— 起步、灰区放行、自动续跑、目标达成 ——
全程不触碰临时目录以外的任何东西。

**1. 在一个空仓库里启动 San。** 只在那里跑 —— 下面这条 goal 会在启动目录下直接
建 `notes/`,放到你自己的项目里跑就是往真实目录里乱写:

```bash
mkdir /tmp/autopilot-demo && cd /tmp/autopilot-demo && git init -q && san
```

`git init` 不是顺手加的:工作区在 git 下,Permission steer 会把对已跟踪文件的
改动当作可恢复,这正是这一轮不会停下来问你的原因。

**2. 交代目标。** 一行,而且是你要按的最后一个键:

```
/goal 搭建一个 notes/ 目录:todo.md 放一个 3 项的清单、done.md 留空、
README.md 说明目录结构。每回合只处理一个文件。三个文件齐了之后用
ls notes/ 验证 —— 然后目标即达成。
```

这段话里有三个细节在起作用:*每回合只处理一个文件* 逼出多次续跑,好让你看清;
*ls notes/* 在路径上放了一个灰区 bash 调用;*然后目标即达成* 给了副驾一个它真
能验证的完成条件。

**3. 观察运行。** 预期的转录大致是:

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

每个 `❭` 都带绿色 `⎿ autopilot` 标记 —— 包括开场那条,全部由副驾敲入,你没有
碰过输入框。那条 `ls` 是灰区调用,由 Permission steer 就地放行。出现
`✓ mission complete` 时,目标被清空、steer 回滚到 `/goal` 接手前的样子(打开
`/autopilot` 可确认),而 Autopilot 保持开启。想中途停下就 `/goal clear`。

同一轮用面板走:打开 **End**、把上面那段话交代成 **Mission**、按 **Start**。
想要别的组合时才走这条路 —— 比如体验最轻的一档:只开 **Suggest** 重跑一遍、
用 `shift+tab` 启动,副驾把每一步以幽灵文本提议在输入框里,你用 `tab` +
`enter` 接受发送。

## 读懂转录里的标记

| 标记 | 含义 |
|---|---|
| 绿 `⎿ autopilot · 2/5` | 上方那条 `❭` 是副驾敲的(第 2 / 共 5 次续跑;不限次数时显示 `step 2`) |
| 琥珀 `⏵ autopilot · turn failed · retrying in 5s` | 某个回合出错,副驾稍后判断能否续跑 |
| 绿 `↳ auto-approved · <理由>` | 判官放行了上方的工具调用 |
| 琥珀 `↳ escalated · <理由>` | 判官把调用退回给你 |
| 绿 `⏵ autopilot · answered for you` | 副驾替你回答了 `AskUserQuestion` |
| 琥珀 `↩ autopilot · this question is yours` | 它把问题留给了你 |
| 琥珀 `↩ autopilot · over to you` | 它停手并交还控制权(判定出错时错误原因缀在后面) |
| 绿 `✓ autopilot · mission complete` | mission 完成并退役 |

判定进行中,模式行显示 `⏵⏵ autopilot · thinking…`;放行计数也在那里
(`· 3 approved · 1 escalated`)。

## 配置

面板编辑的是本会话的实时配置。model、steer、续跑上限保存进 `settings.json`,作为
新会话的默认值。**Steering Prompt（驾驶指令）**和 **mission** 则按会话走:它们随
转录持久化、`/resume` 时恢复,但不会被写成默认值 —— 新会话从内置驾驶指令、无
mission 起步。要把自定义驾驶指令或 mission 带到另一个会话,导出成预设再导入。

Steering Prompt 只负责定义副驾“怎么开”,不会替换不可覆盖的 control-plane policy。
每个 LLM steer 始终携带这层固定 policy,由它约束信任边界、fail-closed 行为、各任务的
安全规则和输出格式。配置键 `systemPrompt` / `systemPromptFile` 为兼容保留,其内容只
作为可编辑的驾驶指令。

```jsonc
{
  "autoPilot": {
    "model": "anthropic/claude-haiku-4-5", // steer 判定用的模型;留空用会话模型
    "systemPrompt": "…",                   // Steering Prompt;按会话走,面板不会写到这里
    "systemPromptFile": "~/prompts/pilot.md", // 持久驾驶指令;systemPrompt 为空时生效
    "mission": "…",                        // 按会话;在面板里设置
    "maxContinuations": 20,                // -1 表示不限次数(面板里填 0 即写入这个值)
    "steers": {
      "suggest": true,
      "permission": true,  // 省略即默认开;false 则一律升级给你
      "bashPrompt": true,  // Bash steer
      "skill": true,       // Skill steer —— 信任 skill 加载
      "question": true,
      "turnEnd": true      // End steer
    }
  }
}
```

命名预设打包整份副驾配置 —— Steering Prompt、mission 和 steer。在 `/autopilot`
菜单里,`e` 导出当前配置、`i` 导入,存取于 `~/.san/autopilot/<name>.json`。

## 关联

- [权限模型](../concepts/permission-model.md) —— Permission steer 判定的灰区
  来自这套静态规则;被硬性拦截的动作永远到不了判官面前。
- 判官组件在 `internal/reviewer`(`reviewer.Judge`);steer 与面板在
  `internal/app` / `internal/app/input`。
