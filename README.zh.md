<div align="center">
  <h1>&lt; SAN ✦ /&gt;</h1>
  <p><strong>框架最小，Agent 最强。</strong></p>
  <p>一个精简的终端 Agent 运行时 —— 上下文小、性能原生，每一块都可替换。</p>
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

**小** —— 你的第一句话之前，只有约 **2.3k token** 的框架开销：262 token 的 system prompt 加 9 个工具 schema，且跨轮次稳定、缓存命中。不常用的工具默认关闭，不去摊薄每一次对话；memory、skills 与项目指令也只在真正用到时才加载。同样一个空回合，Claude Code 要发 ~21k —— **多约 9 倍**（[测量方法](docs/operations/benchmark.md#7-context-overhead-first-turn)）。

**快** —— **~0.01s** 冷启动，常驻约 32 MB，**12 MB** 单文件、零运行时依赖。同一个工具调用任务，端到端 **~3.3s vs ~26s** —— 这个差距来自客户端开销，不是模型推理（[基准测试](#基准测试san-vs-claude-code) · [体积](docs/operations/footprint.md)）。

**开** —— 会话中随时换模型；接入 MCP servers、subagents、skills、plugins、hooks 与 slash commands；system prompt 由 identity、behavior、rules、persona 与项目指令自由拼装；`san inspector` 回放任意会话，模型看到了什么一览无余。**小的是框架，不是 Agent 的能力。**

<sub>*关于名字 —— **San**，即 **三**，符号取自 **☰**。语出《道德经》「三生万物」—— 一个运行时即可化身为任意 Agent，并以三步循环运转（推理 → 行动 → 观察）。命令仍是 `san`。*</sub>

## 开放架构

<details>
<summary><b>总览图</b></summary>

<div align="center">
  <img src="assets/san.png" alt="San —— 可插拔模型、搜索后端、人设、技能与扩展，以及自我进化的 Agent" width="100%">
</div>

</details>

- **模型** —— Anthropic、OpenAI、Google、DeepSeek、Moonshot、Alibaba、MiniMax、Z.ai（GLM）、SenseNova、Mimo、Volcengine（Ark）、Ollama（本地）、Agnes-AI。`/models`
- **搜索** —— Exa、Tavily、Brave、Serper。`/search`
- **人设与扩展** —— 可复用的 persona 配置，加上 skills、plugins、MCP servers、hooks 与受权限管控的 subagents。`/persona`
- **Prompt** —— identity、behavior、rules、persona 与项目指令自由组合成 system prompt（[详情](docs/concepts/harness-channels.md)）。
- **自我学习** —— 可选开启；以可配置策略、操作限制与容量上限，把近期工作沉淀为持久记忆与可复用技能。*（Level 1；更高等级仍在路上。）*
- **权限** —— 姿态由你决定：询问、自动接受、Autopilot 或 Bypass，`Shift+Tab` 切换；subagent 继承同一道门控（[详情](docs/concepts/permission-model.md)）。


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
san                              # 交互模式
san "解释这个函数"               # 一次性运行
san -p "做某件事"                 # print 模式（无 TUI），可管道
san --continue                   # 恢复最近的会话
san --resume                     # 选择历史会话恢复

# 子命令（运行 `san <command> --help` 查看完整列表）
san inspector                    # 会话转录查看器
san agent run --prompt "..."                    # 运行 headless agent
san plugin <list|install|enable|...>          # 管理插件
san mcp <add|list|remove|...>                 # 管理 MCP 服务器
```

| 操作 | 命令 / 快捷键 |
|---|---|
| 选择 / 切换模型 | `/models` —— 保存到 `~/.san/providers.json` |
| 切换 thinking 级别 | `Ctrl+T` 或 `/think`（可选级别因提供商而异） |
| 切换权限模式 | `Shift+Tab`（询问 · 自动接受 · 自动审查） |
| 搜索 / 人设 / 记忆 | `/search` · `/persona` · `/memory` |
| 技能 / 代理 / 工具 | `/skills` · `/agents` · `/tools` |
| 插件 / MCP / 配置 | `/plugin` · `/mcp` · `/config` |
| 会话 / 循环 / 其他 | `/fork` · `/compact` · `/loop` · `/init` · `/clear` |
| 全部 slash 命令 | `/help` |
| 发送 · 换行 · 停止 | `Enter` · `Alt+Enter` · `Esc` |
| 展开工具 · 取消 · 退出 | `Ctrl+O` · `Ctrl+C` · `Ctrl+D` |

API Key：设置对应的环境变量（见下方凭据表）或在首次启动时按提示粘贴。完整入门：[`docs/guides/getting-started.md`](docs/guides/getting-started.md)。

### 配置文件

配置位于 `~/.san/`（用户级）与 `<项目>/.san/`（项目级，覆盖用户级）。项目根目录下的 `SAN.md` 或 `CLAUDE.md` 会被自动加载到系统 prompt。

<details>
<summary><b>凭据</b></summary>

| 服务 | 环境变量 |
|:--------|:---------|
| **Anthropic** (Claude) | `ANTHROPIC_API_KEY` 或 [Vertex AI](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude) |
| **OpenAI** (GPT, o 系列, Codex) | `OPENAI_API_KEY`，或使用 ChatGPT 订阅（通过 `/models` 登录） |
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
<summary><b>目录结构</b></summary>

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

两者特性大体可比（hooks、skills、plugins、session、MCP 等）。性能差距来自 Go 的原生编译、精简的架构设计和克制的 prompt 工程 —— 对比 Node.js 的 V8/JIT/GC 运行时开销。

完整数据见：[docs/operations/benchmark.md](docs/operations/benchmark.md)

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
