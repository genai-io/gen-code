<div align="center">
  <h1>&lt; SAN ✦ /&gt;</h1>
  <p><strong>开销最小，Agent 最强。</strong></p>
  <p>上下文精简，原生性能，从里到外都开放。</p>
  <p>
    <a href="https://github.com/genai-io/san/releases"><img src="https://img.shields.io/github/v/release/genai-io/san?style=flat-square" alt="Release"></a>
    <a href="https://genai-io.github.io/san/"><img src="https://img.shields.io/badge/%E5%AE%98%E7%BD%91-0d9488?style=flat-square" alt="官网"></a>
    <a href="https://genai-io.github.io/san/getting-started.html"><img src="https://img.shields.io/badge/%E5%BF%AB%E9%80%9F%E5%BC%80%E5%A7%8B-0d9488?style=flat-square" alt="快速开始"></a>
    <a href="docs/index.md"><img src="https://img.shields.io/badge/%E6%96%87%E6%A1%A3-0d9488?style=flat-square" alt="文档"></a>
    <a href="https://www.producthunt.com/products/san?launch=san"><img src="https://img.shields.io/badge/Product%20Hunt-da552f?style=flat-square&logo=producthunt&logoColor=white" alt="Product Hunt"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue?style=flat-square" alt="License"></a>
  </p>
  <p>
    <a href="README.md">English</a> · <strong>简体中文</strong>
  </p>
  <p>
    <a href="https://genai-io.github.io/san/intro.html"><img src="assets/san-intro.gif" alt="San 动态简介" width="100%"></a>
  </p>
  <sub><a href="https://genai-io.github.io/san/intro.html">打开高清完整版 ↗</a></sub>
  <p>
    ⚡ <strong>~0.01s</strong> 冷启动&nbsp;&nbsp;·&nbsp;&nbsp;📦 <strong>~12 MB</strong> 单文件&nbsp;&nbsp;·&nbsp;&nbsp;🪶 <strong>零</strong>运行时依赖
  </p>
</div>

San 是一个开源的终端 Agent 运行时：一个原生 Go 二进制，不需要 Node.js 或 Python。模型碰到的一切 —— prompt、工具、provider、扩展 —— 都留给你替换。

## 为什么选 San

**三** —— 三个特性，谁也不为谁让路。

**小** —— 你的第一句话之前，只有约 2.3k token 的框架开销；剩下的上下文窗口全留给你的正事。落到磁盘上是一个 12 MB 的二进制、零运行时依赖 —— 笔记本、CI runner、`scratch` 容器，丢进去就能跑。

**快** —— ~0.01s 冷启动，一次完整的工具调用任务端到端 ~3.3s。你等的是模型，不是客户端（[基准测试](#基准测试san-vs-claude-code)）。

**开** —— 模型、skills、subagents、MCP servers，想接就接；system prompt、Autopilot 的目标、自我学习的策略，都由你来写；`san inspector` 回放任意一次运行。

**小的是开销，不是 Agent 的能力。**

<sub>*关于名字 —— **San**，即 **三**，符号取自 **☰**。语出《道德经》「三生万物」—— 一个运行时即可化身为任意 Agent，并以三步循环运转（推理 → 行动 → 观察）。命令仍是 `san`。*</sub>

## 开放架构

<details>
<summary><b>总览图</b></summary>

<div align="center">
  <img src="assets/san.png" alt="San —— 可插拔模型、搜索后端、人设、技能与扩展，以及自我进化的 Agent" width="100%">
</div>

</details>

**接** —— 没有一块是焊死的。模型可选 Anthropic、OpenAI、Google、DeepSeek、Ollama 等十余家；联网搜索后端任选；扩展涵盖 skills、subagents、MCP servers、plugins、hooks。

**写** —— Agent 怎么做事，是你能改的文本，不是编死在二进制里的东西。拼装 system prompt（[原理](docs/concepts/harness-channels.md)），打包成可随时切换的 persona，给 Autopilot 一个目标，为自我学习定一套策略。

**看** —— 没有一步是暗箱。San 能自作主张到什么程度由你定，subagent 继承同一个选择（[权限模型](docs/concepts/permission-model.md)）；Inspector 回放任意一次运行，模型看到的一切原样呈现。


## 安装

**macOS / Linux**

```bash
curl -fsSL https://raw.githubusercontent.com/genai-io/san/main/install.sh | bash
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/genai-io/san/main/install.ps1 | iex
```

升级直接重新执行同样的命令。

<details>
<summary><b>其他方式</b></summary>

**卸载**

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/genai-io/san/main/install.sh | bash -s uninstall
```

```powershell
# Windows (PowerShell)
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/genai-io/san/main/install.ps1))) uninstall
```

**通过 Go Install**

```bash
go install github.com/genai-io/san/cmd/san@latest
```

**从源码构建**

```bash
git clone https://github.com/genai-io/san.git
cd san
go build -o san ./cmd/san
mkdir -p ~/.local/bin && mv san ~/.local/bin/
```

</details>

## 使用

```bash
san                          # 交互模式
san "解释这个函数"            # 一次性运行
san -p "做某件事"             # print 模式（无 TUI），可管道
san --continue               # 恢复最近的会话
san --resume                 # 选择历史会话恢复
```

子命令：`inspector` · `agent` · `plugin` · `mcp` —— 各自运行 `san <command> --help` 查看。

| 操作 | 命令 / 快捷键 |
|---|---|
| 模型 · thinking 级别 | `/models` · `Ctrl+T` |
| 权限模式 | `Shift+Tab`（询问 · 自动接受 · 自动审查） |
| 长任务 · 自我学习 | `/autopilot` · `/goal` · `/evolve` |
| 全部 slash 命令 | `/help` |
| 快捷键 | `Enter` 发送 · `Alt+Enter` 换行 · `Esc` 停止 · `Ctrl+O` 展开工具 · `Ctrl+C` 取消 · `Ctrl+D` 退出 |

[`docs/guides/getting-started.md`](docs/guides/getting-started.md)

### 配置文件

<details>
<summary><b>凭据</b></summary>

| 服务 | 环境变量 |
|:--------|:---------|
| **Anthropic** (Claude) | `ANTHROPIC_API_KEY` 或 [Vertex AI](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude) |
| **OpenAI** (GPT, o 系列, Codex) | `OPENAI_API_KEY`，或使用 ChatGPT 订阅（通过 `/models` 登录） |
| **GitHub Copilot**（Copilot 全部模型） | 使用 Copilot 订阅（通过 `/models` 登录） |
| **Google** (Gemini) | `GOOGLE_API_KEY` |
| **DeepSeek** (DeepSeek V4) | `DEEPSEEK_API_KEY` |
| **Moonshot** (Kimi) | `MOONSHOT_API_KEY` |
| **Alibaba** (Qwen) | `DASHSCOPE_API_KEY` |
| **MiniMax** | `MINIMAX_API_KEY` |
| **Z.ai** (GLM / GLM Coding Plan) | `BIGMODEL_API_KEY` |
| **SenseNova** | `SENSENOVA_API_KEY` |
| **Mimo** | `MIMO_API_KEY` |
| **Volcengine**（Ark） | `VOLCENGINE_API_KEY` |
| **Ollama** (本地) | `OLLAMA_BASE_URL`（默认 `http://localhost:11434/v1`） |
| **Agnes-AI** | `AGNESAI_API_KEY` |
| **Exa** 搜索 | _无需_（默认） |
| **Tavily** 搜索 | `TAVILY_API_KEY` |
| **Brave** 搜索 | `BRAVE_API_KEY` |
| **Serper** 搜索 | `SERPER_API_KEY` |

</details>

<details>
<summary><b>配置文件与目录结构</b></summary>

配置从 `~/.san/`（用户级）与 `<项目>/.san/`（项目级）加载，项目级覆盖用户级。项目指令依次读取 `.san/SAN.md`、`SAN.md`、`.claude/CLAUDE.md`、`CLAUDE.md`。

用户级（`~/.san/`）：

```
providers.json    # 提供商连接信息与当前模型
settings.json     # 权限、hooks、env、当前 persona
skills.json       # 技能状态
personas/         # persona 包：系统 prompt 片段、技能、设置
skills/           # 自定义技能定义
agents/           # agent 定义
commands/         # 自定义 slash 命令
plugins/          # 已安装插件
projects/         # 会话记录与索引
```

项目级（`.san/`）：

```
settings.json       # 权限、hooks、禁用工具
mcp.json            # MCP server 定义（团队共享）
mcp.local.json      # MCP server 定义（个人，git-ignored）
personas/           # 项目级 persona 包（覆盖用户级）
agents/*.md         # Subagent 定义
skills/*/SKILL.md   # 技能
commands/*.md       # Slash 命令
plugins/            # 项目级插件
plugins-local/      # 本地插件（git-ignored）
```

</details>

## 基准测试：San vs Claude Code

在 Apple Silicon 上对比 [Claude Code](https://claude.ai/code) v2.1.112，使用相同模型（`claude-sonnet-4-6`）：

| 指标 | San | Claude Code | 优势 |
|--------|---------|-------------|-----------|
| 下载大小 | 12 MB | 63 MB（+ Node.js 112 MB） | **小 5 倍** |
| 磁盘占用 | 38 MB | 175 MB | **小 4.6 倍** |
| 启动耗时 | ~0.01s | ~0.20s | **快 20 倍** |
| 启动内存 | ~32 MB | ~189 MB | **省 5.8 倍** |
| 简单任务 | ~2.4s / 39 MB | ~10.4s / 286 MB | **快 4.3 倍、省内存 7.3 倍** |
| 工具调用任务 | ~3.3s / 39 MB | ~26.0s / 285 MB | **快 7.9 倍、省内存 7.2 倍** |
| 框架上下文开销* | ~2.3k token | ~20.9k token | **省约 9 倍** |

<sub>*上下文开销 = 空回合下的 system prompt + 工具 schema，单独在 San v1.22.0 与 Claude Code v2.1.220 上测量，[方法见此](docs/operations/benchmark.md#7-context-overhead-first-turn)；其余各行来自 v1.13.2 / v2.1.112 那次测试。</sub>

特性大体可比 —— 差距来自客户端开销，不是能力缺斤少两。[`docs/operations/benchmark.md`](docs/operations/benchmark.md)

## 文档

- [文档索引](docs/index.md) —— 架构、特性、运维、参考资料的入口
- [架构](docs/concepts/architecture.md) —— 架构入口与阅读顺序
- [包结构图](docs/reference/package-map.md) —— 包归属与依赖边界
- [人设 Persona](docs/concepts/persona.md) —— 打包的系统 prompt、技能、agent 与设置
- [系统 Prompt](docs/concepts/harness-channels.md) —— Slot 模型、persona、技能/agent 注入
- [Subagents](docs/packages/2-feature/subagent.md) · [Skills](docs/packages/2-feature/skill.md) · [Plugins](docs/packages/2-feature/plugin.md) · [MCP](docs/packages/2-feature/mcp.md)
- [Hooks](docs/packages/2-feature/hook.md) · [Permissions](docs/concepts/permission-model.md) · [Tasks](docs/packages/2-feature/task.md)
- [Inspector](docs/packages/2-feature/inspector.md) —— 本地 Web UI，用于转录回放与调试
- 每个包的设计文档见 [`docs/packages/`](docs/packages/)，从[包索引](docs/packages/index.md)开始

## 相关项目

- [Claude Code](https://claude.ai/code) —— Anthropic 的 AI 编程助手
- [Aider](https://github.com/paul-gauthier/aider) —— 终端中的 AI 结对编程
- [Continue](https://github.com/continuedev/continue) —— 开源 AI 编程助手

## 社区

两个入口 —— 国内用微信，海外用 Slack，欢迎入群一起讨论：

<div align="center">
<table>
<tr>
<td align="center" width="50%">
  <img src="assets/wechat.jpg" alt="极客外传公众号二维码" width="200"><br>
  <sub>关注公众号「极客外传」· 回复 <code>san</code> 或 <code>三</code> 入群</sub>
</td>
<td align="center" width="50%">
  <img src="assets/slack.png" alt="San Slack 二维码" width="200"><br>
  <sub>扫码或<a href="https://join.slack.com/t/sanaico/shared_invite/zt-3zvfr8v6f-dchFpvpufY7fKA7tG7lhIg">点击加入 Slack</a></sub>
</td>
</tr>
</table>
</div>

## 贡献

欢迎贡献！请阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 中的指南。

## 许可证

Apache License 2.0 —— 详见 [LICENSE](LICENSE)。
