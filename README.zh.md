<div align="center">
  <h1>&lt; SAN ✦ /&gt;</h1>
  <p><strong>快速、开放的终端 Agent 运行框架</strong></p>
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

San 是一个开源的**终端 Agent 运行框架** —— 一个原生 Go 二进制，把任意模型包进一个快速、可检视、受权限管控的循环。自带你的模型与扩展即可，无需另装 Node.js 或 Python 运行时。

**为什么选 San**

- **快速** —— 约 12 MB 单文件，约 0.01s 冷启动，无需额外运行时。
- **开放** —— 模型、搜索、工具都可在运行时切换；自带你的 persona 人设与扩展。
- **框架** —— 能调的是策略，不只是零件：自定义 **autopilot** 减少 human-in-the-loop，自定义 **self-learning** —— 在你工作时不断增长与打磨记忆和技能。

<sub>*关于名字 —— **San**，即 **三**，符号取自 **☰**。语出《道德经》「三生万物」—— 一个运行时即可化身为任意 Agent，并以三步循环运转（推理 → 行动 → 观察）。命令仍是 `san`。*</sub>

## 特性

<details>
<summary><b>开放架构</b> &nbsp;·&nbsp; 总览图</summary>

<div align="center">
  <img src="assets/san.png" alt="San —— 可插拔模型、搜索后端、人设、技能与扩展，以及自我进化的 Agent" width="100%">
</div>

</details>

- **模型** —— Anthropic、OpenAI、Google、DeepSeek、Moonshot、Alibaba、MiniMax、Z.ai（GLM）、SenseNova、Mimo、Volcengine（Ark）、Ollama（本地）、Agnes-AI。`/models`
- **搜索** —— Exa、Tavily、Brave、Serper。`/search`
- **人设与扩展** —— 可复用的 persona 配置，加上 Claude Code 的 skills、plugins、MCP servers、hooks 与沙箱化 subagents —— 全部无需改动即可运行。`/persona`
- **自我学习** —— 可选开启；把近期工作沉淀为持久记忆与可复用技能，节奏与容量上限均可配置。*（Level 1；更高等级仍在路上。）*

### 工程实现

- **随处运行** —— 面向 Windows、macOS、Linux 的单一静态二进制；同一文件可在笔记本、边缘设备或 `scratch` 容器中运行（[体积](docs/operations/footprint.md) · [基准测试](#基准测试san-vs-claude-code)）。
- **权限** —— 三种模式（询问 · 自动接受 · 自动审查），`Shift+Tab` 切换；subagent 继承权限门控（[详情](docs/concepts/permission-model.md)）。
- **会话** —— 自动保存、恢复（`--continue` / `--resume`）、Fork（`/fork`）、自动压缩（`/compact`），以及逐消息成本追踪。
- **检查器** —— 本地 Web UI，回放转录并检视系统 prompt（`san inspector`）。
- 另有事件驱动的 subagent 协同、TUI 主题与 prompt 预测。


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
