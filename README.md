<div align="center">
  <h1>&lt; SAN ✦ /&gt;</h1>
  <p><strong>A fast, open agent harness for the terminal, built on a flexible and extensible architecture.</strong></p>
  <p>
    <a href="https://github.com/genai-io/san/releases"><img src="https://img.shields.io/github/v/release/genai-io/san?style=flat-square" alt="Release"></a>
    <a href="https://genai-io.github.io/san/"><img src="https://img.shields.io/badge/Website-0d9488?style=flat-square" alt="Website"></a>
    <a href="https://genai-io.github.io/san/getting-started.html"><img src="https://img.shields.io/badge/Getting%20Started-0d9488?style=flat-square" alt="Getting Started"></a>
    <a href="docs/index.md"><img src="https://img.shields.io/badge/Docs-0d9488?style=flat-square" alt="Docs"></a>
    <a href="https://www.producthunt.com/products/san?launch=san"><img src="https://img.shields.io/badge/Product%20Hunt-da552f?style=flat-square&logo=producthunt&logoColor=white" alt="Product Hunt"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue?style=flat-square" alt="License"></a>
  </p>
  <p>
    <strong>English</strong> · <a href="README.zh.md">简体中文</a>
  </p>
  <p>
    <a href="https://genai-io.github.io/san/intro.html"><img src="assets/san-intro.gif" alt="San — animated intro" width="100%"></a>
  </p>
  <sub><a href="https://genai-io.github.io/san/intro.html">Open the full-quality intro ↗</a></sub>
  <p>
    ⚡ <strong>~0.01s</strong> cold start&nbsp;&nbsp;·&nbsp;&nbsp;📦 <strong>~12 MB</strong> single binary&nbsp;&nbsp;·&nbsp;&nbsp;🪶 <strong>zero</strong> runtime deps
  </p>
</div>

San is an open-source terminal agent harness: one native Go binary for
model-driven work, with no Node.js or Python runtime.

**Why San**

- **Fast** — a ~12 MB single binary, ~0.01s cold start, no separate runtime.
- **Open** — swap the model, search, and tools at runtime; bring your own persona profiles and extensions.
- **Harness** — configure permissions, autopilot, memory, and skills for your workflow.

<sub>*The name — **San**, written **三** ("three") and drawn **☰**. From the Dao De Jing, 三生万物 — "three begets the ten-thousand things": one runtime that becomes any agent, running a three-step loop (reason → act → observe). The command stays `san`.*</sub>

## Features

<details>
<summary><b>Open architecture</b> &nbsp;·&nbsp; overview diagram</summary>

<div align="center">
  <img src="assets/san.png" alt="San — pluggable models, search backends, personas, skills &amp; extensions, and a self-evolving agent" width="100%">
</div>

</details>

- **Models** — Anthropic, OpenAI, Google, DeepSeek, Moonshot, Alibaba, MiniMax, Z.ai (GLM), SenseNova, Mimo, Volcengine (Ark), Ollama (local), Agnes-AI. `/models`
- **Search** — Exa, Tavily, Brave, Serper. `/search`
- **Personas & extensions** — reusable profiles, skills, plugins, MCP servers, hooks, and permission-gated subagents. `/persona`
- **Self-learning** — opt-in; distills durable memory and reusable skills with configurable cadence and caps. *(Level 1; deeper levels on the way.)*

### Engineering

- **Runs anywhere** — one static binary for Windows, macOS, and Linux; the same file runs on a laptop, an edge device, or a `scratch` container ([footprint](docs/operations/footprint.md) · [benchmark](#benchmark-san-vs-claude-code)).
- **Permissions** — three modes (ask · auto-accept · autopilot) toggled with `Shift+Tab`; subagents inherit the gates ([details](docs/concepts/permission-model.md)).
- **Sessions** — auto-save, resume (`--continue` / `--resume`), fork (`/fork`), auto-compaction (`/compact`), and per-message cost tracking.
- **Inspector** — replay transcripts and inspect system prompts in a local web UI (`san inspector`).
- Plus event-driven subagent coordination, TUI themes, and prompt prediction.


## Installation

**macOS / Linux**

```bash
curl -fsSL https://raw.githubusercontent.com/genai-io/san/main/install.sh | bash
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/genai-io/san/main/install.ps1 | iex
```

Start with `san`. On first launch, choose a model and add its API key when prompted. To update later, run `san update`.

<details>
<summary><b>Other methods</b></summary>

**Uninstall**

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/genai-io/san/main/install.sh | bash -s uninstall
```

```powershell
# Windows (PowerShell)
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/genai-io/san/main/install.ps1))) uninstall
```

**Go Install (requires Go 1.25.8+)**

```bash
go install github.com/genai-io/san/cmd/san@latest
```

**Build from Source**

```bash
git clone https://github.com/genai-io/san.git
cd san
go build -o san ./cmd/san
mkdir -p ~/.local/bin && mv san ~/.local/bin/
```

</details>

## Usage

```bash
san                              # interactive
san "explain this function"      # one-shot
san -p "do something"            # print mode (no TUI), pipe-friendly
san --continue                   # resume the latest session
san --resume                     # pick a past session to resume

# Subcommands (run `san <command> --help` for the full list)
san inspector                    # session transcript viewer
san agent run --prompt "..."                     # run a headless agent
san plugin <list|install|enable|...>          # manage plugins
san mcp <add|list|remove|...>                 # manage MCP servers
```

| What | How |
|---|---|
| Pick / switch model | `/models` — saved to `~/.san/providers.json` |
| Cycle thinking budget | `Ctrl+T` or `/think` (levels vary by provider) |
| Toggle permission mode | `Shift+Tab` (ask · auto-accept · autopilot) |
| Search / persona / memory | `/search` · `/persona` · `/memory` |
| Skills / agents / tools | `/skills` · `/agents` · `/tools` |
| Plugins / MCP / config | `/plugin` · `/mcp` · `/config` |
| Session / loop / misc | `/fork` · `/compact` · `/loop` · `/init` · `/clear` |
| All slash commands | `/help` |
| Send · newline · stop | `Enter` · `Alt+Enter` · `Esc` |
| Expand tool · cancel · exit | `Ctrl+O` · `Ctrl+C` · `Ctrl+D` |

For API keys, set the matching env var (see Credentials below) or paste when prompted on first launch. Full walkthrough: [`docs/guides/getting-started.md`](docs/guides/getting-started.md).

### Configuration

Configuration is loaded from `~/.san/` and `<project>/.san/` (project settings override user settings). Project instructions are read from `.san/SAN.md`, `SAN.md`, `.claude/CLAUDE.md`, or `CLAUDE.md`, in that order.

<details>
<summary><b>Credentials</b></summary>

| Service | Variable |
|:--------|:---------|
| **Anthropic** (Claude) | `ANTHROPIC_API_KEY` or [Vertex AI](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude) |
| **OpenAI** (GPT, o-series, Codex) | `OPENAI_API_KEY`, or a ChatGPT subscription (sign in via `/models`) |
| **Google** (Gemini) | `GOOGLE_API_KEY` |
| **DeepSeek** (DeepSeek V4) | `DEEPSEEK_API_KEY` |
| **Moonshot** (Kimi) | `MOONSHOT_API_KEY` |
| **Alibaba** (Qwen) | `DASHSCOPE_API_KEY` |
| **MiniMax** | `MINIMAX_API_KEY` |
| **Z.ai** (GLM / GLM Coding Plan) | `BIGMODEL_API_KEY` |
| **SenseNova** | `SENSENOVA_API_KEY` |
| **Mimo** | `MIMO_API_KEY` |
| **Volcengine** (Ark) | `VOLCENGINE_API_KEY` |
| **Ollama** (local) | `OLLAMA_BASE_URL` (default `http://localhost:11434/v1`) |
| **Agnes-AI** | `AGNESAI_API_KEY` |
| **Exa** search | _none_ (default) |
| **Tavily** search | `TAVILY_API_KEY` |
| **Brave** search | `BRAVE_API_KEY` |
| **Serper** search | `SERPER_API_KEY` |

</details>

<details>
<summary><b>Directory layout</b></summary>

User-level (`~/.san/`):

```
providers.json    # Provider connections and current model
settings.json     # Permissions, hooks, env, active persona
skills.json       # Skill states
personas/         # Persona bundles: system prompt parts, skills, settings
skills/           # Custom skill definitions
agents/           # Agent definitions
commands/         # Custom slash commands
plugins/          # Installed plugins
projects/         # Session transcripts + indexes
```

Project-level (`.san/`):

```
settings.json       # Permissions, hooks, disabled tools
mcp.json            # MCP server definitions (team shared)
mcp.local.json      # MCP server definitions (personal, git-ignored)
personas/           # Project-scoped persona bundles (override user-level)
agents/*.md         # Subagent definitions
skills/*/SKILL.md   # Skills
commands/*.md       # Slash commands
plugins/            # Project-level plugins
plugins-local/      # Local plugins (git-ignored)
```

</details>

## Benchmark: San vs Claude Code

Compared with [Claude Code](https://claude.ai/code) v2.1.112 on Apple Silicon, same model (`claude-sonnet-4-6`):

| Metric | San | Claude Code | Advantage |
|--------|---------|-------------|-----------|
| Download size | 12 MB | 63 MB (+ Node.js 112 MB) | **5x smaller** |
| Disk footprint | 38 MB | 175 MB | **4.6x smaller** |
| Startup time | ~0.01s | ~0.20s | **20x faster** |
| Startup memory | ~32 MB | ~189 MB | **5.8x less** |
| Simple task | ~2.4s / 39 MB | ~10.4s / 286 MB | **4.3x faster, 7.3x less memory** |
| Tool-use task | ~3.3s / 39 MB | ~26.0s / 285 MB | **7.9x faster, 7.2x less memory** |

Both tools have comparable features (hooks, skills, plugins, session, MCP, etc.). The performance gap comes from Go's native compilation, minimal architecture design, and lean prompt engineering — vs Node.js V8/JIT/GC runtime overhead.

See full details: [docs/operations/benchmark.md](docs/operations/benchmark.md)

## Documentation

- [Documentation Index](docs/index.md) — map of architecture, features, operations, and references
- [Architecture](docs/concepts/architecture.md) — architecture entrypoint and reading order
- [Package Map](docs/reference/package-map.md) — package ownership and dependency boundaries
- [Personas](docs/concepts/persona.md) — bundled system prompt, skills, agents, and settings
- [System Prompt](docs/concepts/harness-channels.md) — Slot model, persona, skill/agent injection
- [Subagents](docs/packages/2-feature/subagent.md) · [Skills](docs/packages/2-feature/skill.md) · [Plugins](docs/packages/2-feature/plugin.md) · [MCP](docs/packages/2-feature/mcp.md)
- [Hooks](docs/packages/2-feature/hook.md) · [Permissions](docs/concepts/permission-model.md) · [Tasks](docs/packages/2-feature/task.md)
- [Inspector](docs/packages/2-feature/inspector.md) — local web UI for transcript replay and debugging
- Per-package design under [`docs/packages/`](docs/packages/) — start at [Package Index](docs/packages/index.md)

## Related Projects

- [Claude Code](https://claude.ai/code) — Anthropic's AI coding assistant
- [Aider](https://github.com/paul-gauthier/aider) — AI pair programming in terminal
- [Continue](https://github.com/continuedev/continue) — Open-source AI code assistant

## Community

Two ways in — WeChat for the Chinese community, Slack for everyone else:

<div align="center">
<table>
<tr>
<td align="center" width="50%">
  <img src="assets/wechat.jpg" alt="WeChat official account 极客外传 QR code" width="200"><br>
  <sub>关注公众号「极客外传」· 回复 <code>san</code> 或 <code>三</code> 入群</sub>
</td>
<td align="center" width="50%">
  <img src="assets/slack.png" alt="San Slack QR code" width="200"><br>
  <sub>Scan or <a href="https://join.slack.com/t/sanaico/shared_invite/zt-3zvfr8v6f-dchFpvpufY7fKA7tG7lhIg">join our Slack</a></sub>
</td>
</tr>
</table>
</div>

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Apache License 2.0 - see [LICENSE](LICENSE) for details.
