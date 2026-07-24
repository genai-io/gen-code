# Slash Commands

## Overview

Slash commands are typed directly in the TUI input box. They trigger local UI actions or inject structured prompts without sending a regular chat message.

| Command | Function |
|---------|----------|
| `/models` | Select model and manage provider connections |
| `/persona` | Switch the active persona (alias: `/identity`) |
| `/clear` | Clear chat history |
| `/fork` | Fork the current session |
| `/resume` | Resume a previous session |
| `/help` | Show available commands |
| `/tools` | Enable / disable tools |
| `/plan` | Enter plan mode |
| `/skills` | Manage skill states |
| `/agents` | Manage agents |
| `/tokenlimit` | View / set token budget |
| `/compact` | Compress conversation history |
| `/init` | Create SAN.md and config files |
| `/memory` | View / edit memory files |
| `/mcp` | Manage MCP servers |
| `/plugin` | Manage plugins |
| `/reload-plugins` | Reload plugins and refresh plugin-backed components |
| `/think` | Cycle thinking level (off / normal / high / ultra) |
| `/loop` | Schedule recurring or one-shot prompts and manage loop jobs |
| `/search` | Select search engine for web search |

## UI Interactions

- Commands are matched against the registry as the user types; a suggestion dropdown appears.
- Selector commands (`/models`, `/skills`, `/search`, etc.) open a scrollable picker overlay.
- `/clear` immediately resets the visible conversation.
- `/think` cycles through levels and updates the status bar indicator.
- `/loop` has a dedicated reference page: see [Loop Scheduling Command](./loop.md).

## Automated Tests

```bash
go test ./internal/command/... -v
```

Covered:

```
TestBuiltinCommandsUseModelsAndExcludeRemovedCommands
                                         — /models is registered; /model and /glob are absent
TestExecuteCommandExit                    — /exit returns quit command
TestExecuteCommandUnknown                 — unknown commands show error message
TestHandleInitCommand                     — /init creates .san/SAN.md file
TestHandleInitCommand (local)             — /init local creates .san/SAN.local.md
TestHandleInitCommand (rules)             — /init rules creates .san/rules directory
TestHandleMemoryList                      — /memory list formats output with sections
TestExecuteCommandLoopSchedulesRecurringPrompt
                                         — /loop recurring path is registered and handled
```

Cases to add:

```go
func TestSlashClear_ResetsConversation(t *testing.T) {
    // /clear must empty the message history
}

func TestSlashFork_CreatesNewSession(t *testing.T) {
    // /fork must create a new independent session with original history
}

func TestSlashCompact_TriggersCompaction(t *testing.T) {
    // /compact must trigger compaction and return summary
}

func TestSlashThink_CyclesLevels(t *testing.T) {
    // /think must cycle off → normal → high → ultra → off
}

func TestSlashModels_SwitchesModel(t *testing.T) {
    // /models selection must change active model immediately
}

func TestSlashSearch_SwitchesEngine(t *testing.T) {
    // /search selection must change active search engine
}

func TestSlashTokenlimit_ShowsUsage(t *testing.T) {
    // /tokenlimit must show current usage and context limit
}

func TestSlashResume_OpensSessionSelector(t *testing.T) {
    // /resume must open the session selector overlay
}

func TestSlashTools_OpensToolSelector(t *testing.T) {
    // /tools must open the tool enable/disable overlay
}

func TestSlashPlan_EntersPlanMode(t *testing.T) {
    // /plan must switch the app into plan mode
}

func TestSlashSkills_TogglesState(t *testing.T) {
    // /skills must toggle skill state between disable/enable/active
}
```

## Interactive Tests (tmux)

```bash
tmux new-session -d -s t_cmds -x 220 -y 60
tmux send-keys -t t_cmds 'san' Enter
sleep 2

# Test 1: /help
tmux send-keys -t t_cmds '/help' Enter
sleep 2
tmux capture-pane -t t_cmds -p
# Expected: all available commands listed

# Test 2: /clear
tmux send-keys -t t_cmds 'hello' Enter
sleep 4
tmux send-keys -t t_cmds '/clear' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: blank conversation view

# Test 3: /think — cycle through levels
tmux send-keys -t t_cmds '/think' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: thinking level options (off / normal / high / ultra)

# Test 4: /models (tabbed picker for models and providers)
tmux send-keys -t t_cmds '/models' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: tabbed picker with Models and Providers tabs

# Test 5: /search
tmux send-keys -t t_cmds Escape
tmux send-keys -t t_cmds '/search' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: search engine selector

# Test 6: /tokenlimit
tmux send-keys -t t_cmds '/tokenlimit' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: current token usage and limit

# Test 7: /init — test in a fresh directory
tmux send-keys -t t_cmds C-c
tmux send-keys -t t_cmds 'mkdir -p /tmp/init_test && cd /tmp/init_test && san' Enter
sleep 2
tmux send-keys -t t_cmds '/init' Enter
sleep 3
ls /tmp/init_test/.san/
# Expected: SAN.md created under .san/

# Test 8: Command suggestion dropdown
tmux send-keys -t t_cmds C-c
tmux send-keys -t t_cmds 'san' Enter
sleep 2
tmux send-keys -t t_cmds '/mod'
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: suggestion dropdown shows /models as match

# Test 9: /fork
tmux send-keys -t t_cmds C-c
sleep 0.3
tmux send-keys -t t_cmds 'hello' Enter
sleep 4
tmux send-keys -t t_cmds '/fork' Enter
sleep 2
tmux capture-pane -t t_cmds -p
# Expected: new session created with original history

# Test 10: /compact
tmux send-keys -t t_cmds '/compact' Enter
sleep 5
tmux capture-pane -t t_cmds -p
# Expected: compaction triggered; summary shown

# Test 11: /skills
tmux send-keys -t t_cmds '/skills' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: skill selector titled "Manage Skills"

# Test 12: /agents
tmux send-keys -t t_cmds '/agents' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: agent selector titled "Manage Agents"

# Test 13: /mcp
tmux send-keys -t t_cmds '/mcp' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: MCP selector titled "MCP Servers"

# Test 14: /plugin
tmux send-keys -t t_cmds '/plugin' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: plugin management panel titled "Plugin Manager"

# Test 15: /memory
tmux send-keys -t t_cmds '/memory' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: loaded memory files displayed

# Test 16: /resume
tmux send-keys -t t_cmds '/resume' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: session picker overlay is shown

# Test 17: /tools
tmux send-keys -t t_cmds '/tools' Enter
sleep 1
tmux capture-pane -t t_cmds -p
# Expected: tool selector titled "Manage Tools"

tmux kill-session -t t_cmds
rm -rf /tmp/init_test
```
