# Configuration System

## Overview

Configuration is loaded from multiple files at different scopes. Higher-priority files override lower-priority ones.

**Load priority** (lowest → highest):

| Priority | File |
|----------|------|
| 1 | `~/.claude/settings.json` (Claude user compat) |
| 2 | `~/.san/settings.json` (San user) |
| 3 | `./.claude/settings.json` (Claude project compat) |
| 4 | `./.san/settings.json` (San project) |
| 5 | `./.claude/settings.local.json` |
| 6 | `./.san/settings.local.json` |
| 7 | CLI arguments / environment variables |
| 8 | `managed-settings.json` (read-only system policy) |

**settings.json schema:**

```json
{
  "permissions": {
    "allow": ["Read(**)", "Bash(go test:*)"],
    "deny":  ["Bash(rm -rf*)"],
    "ask":   ["Write(**)"]
  },
  "model": "claude-sonnet-4-6",
  "hooks": { "PreToolUse": [...] },
  "env": { "MY_VAR": "value" },
  "enabledPlugins": { "my-plugin": true },
  "disabledTools": { "WebSearch": true, "Cron": false },
  "theme": "dark"
}
```

## UI Interactions

- **`/tools`**: shows which tools are disabled via `disabledTools`. Some
  tools ship disabled (currently `Cron`); an explicit `"Cron": false` entry —
  what the panel writes when you enable it — overrides the factory default.
- **Env vars**: injected into the Bash tool's environment automatically.
- **Theme**: applied at startup; no restart needed when changed via `/models` or similar commands.

## Automated Tests

```bash
go test ./internal/setting/... -v
```

Covered:

```
# Permission & rule matching
TestMatchRule                               — rule pattern matching (11 sub-tests)
TestBuildRule                               — rule building (5 sub-tests)
TestCheckPermission                         — permission checks (13+ sub-tests)
TestCheckPermissionWithReason               — permission with reason tracking
TestDenialTracking                          — denial fallback mechanism

# Config loading & merging
TestLoaderLoad                              — config file loading
TestConfig_LocalOverridesProject            — local.json overrides project
TestConfig_LocalOverridesProject_MergesNotReplaces — additive merge
TestConfig_UserLevelOverriddenByProject     — project overrides user

# Environment & tools
TestConfig_Env_InjectedIntoBashEnvironment  — env vars available in Bash
TestConfig_DisabledTools_HiddenFromModel    — disabled tools hidden from LLM

# Security
TestIsDestructiveCommand                    — dangerous command detection (13 sub-tests)
TestIsSensitivePath                         — sensitive path detection (13 sub-tests)
TestSensitivePathsRequireConfirmation       — sensitive paths need confirm
TestCheckBashSecurity                       — bash security checks (13 sub-tests)
TestBashSecurityRequiresConfirmation        — bash security needs confirm
TestDenyRulesPriorityOverSession            — deny rules override session
TestDestructiveCommandsRequireConfirmation  — destructive commands need confirm
TestWorkingDirectoryConstraint              — edits outside project root blocked

# Permission modes
TestBypassPermissionsMode                   — bypass permissions mode
TestDontAskMode                             — DontAsk mode
TestDenyRuleBlocksBypass                    — deny rules block bypass
TestSafeToolAllowlist                       — safe tool whitelist
TestPassthroughBehavior                     — passthrough behavior
TestResolveHookAllow                        — hook allow resolution
TestOperationModeNext                       — operation mode cycling

# Suggestions
TestGenerateSuggestions_Bash                — bash rule suggestions
TestGenerateSuggestions_File                — file rule suggestions
TestGenerateSuggestions_Skill               — skill rule suggestions
TestSuggestBashRules_MaxLimit               — suggestion max limit
TestSuggestBashRules_DangerousFiltered      — dangerous rules filtered
TestSuggestBashRules_Dedup                  — deduplicated suggestions

# Path handling
TestIsInWorkingDirectory                    — working directory check
TestNormalizeMacOSPath                      — macOS path normalization
TestIsSubpath                               — subpath detection

# Bash AST
TestParseBashAST                            — bash command parsing
TestExtractCommandsAST                      — command extraction
TestExtractCommandsAST_Pipe                 — pipe command extraction
TestExtractCommandsAST_Redirect             — redirect extraction
TestExtractCommandsAST_PathStripping        — path stripping
TestCheckASTSecurity                        — AST security checks
TestCheckASTSecurity_ExcessiveCommands      — excessive command blocking
```

Cases to add:

```go
func TestConfig_ManagedSettings_ReadOnly(t *testing.T) {
    // managed-settings.json values must not be overridden by user settings
}

func TestConfig_CLIArgs_OverrideAll(t *testing.T) {
    // CLI arguments must override all file-based settings
}

func TestConfig_AllEightPriorities(t *testing.T) {
    // All 8 priority levels loaded simultaneously with correct precedence
}

func TestConfig_MalformedJSON_Error(t *testing.T) {
    // Malformed JSON in settings must produce a descriptive error
}

func TestConfig_Theme_AppliedAtStartup(t *testing.T) {
    // Theme setting must be applied when the TUI starts
}

func TestConfig_EnabledPlugins_ActivatesPlugin(t *testing.T) {
    // enabledPlugins: true must activate the plugin's components
}

```

## Interactive Tests (tmux)

```bash
mkdir -p /tmp/cfg_test/.san

# User-level env var (backup existing)
cp ~/.san/settings.json ~/.san/settings.json.bak 2>/dev/null || true
cat > ~/.san/settings.json << 'EOF'
{"env": {"SCOPE": "user"}}
EOF

# Project-level overrides it + disables a tool
cat > /tmp/cfg_test/.san/settings.json << 'EOF'
{"env": {"SCOPE": "project"}, "disabledTools": {"WebSearch": true}}
EOF

tmux new-session -d -s t_cfg -x 220 -y 60
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 2

# Test 1: Verify env override (project wins over user)
tmux send-keys -t t_cfg 'run: echo $SCOPE' Enter
sleep 5
tmux capture-pane -t t_cfg -p
# Expected: output is "project" (project config wins)

# Test 2: Verify disabled tool
tmux send-keys -t t_cfg '/tools' Enter
sleep 2
tmux capture-pane -t t_cfg -p
# Expected: WebSearch shown as disabled

# Test 3: Local settings override project
tmux send-keys -t t_cfg C-c
cat > /tmp/cfg_test/.san/settings.local.json << 'EOF'
{"env": {"SCOPE": "local"}}
EOF
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 2
tmux send-keys -t t_cfg 'run: echo $SCOPE' Enter
sleep 5
tmux capture-pane -t t_cfg -p
# Expected: output is "local" (local overrides project)

# Test 4: Allow rule auto-approves
tmux send-keys -t t_cfg C-c
cat > /tmp/cfg_test/.san/settings.json << 'EOF'
{"permissions": {"allow": ["Bash(echo*)"]}}
EOF
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 2
tmux send-keys -t t_cfg 'run: echo auto-approved' Enter
sleep 5
tmux capture-pane -t t_cfg -p
# Expected: Bash runs without permission dialog

# Test 5: Deny rule blocks
tmux send-keys -t t_cfg C-c
cat > /tmp/cfg_test/.san/settings.json << 'EOF'
{"permissions": {"deny": ["Bash(rm*)"]}}
EOF
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 2
tmux send-keys -t t_cfg 'run: rm -f /tmp/test' Enter
sleep 5
tmux capture-pane -t t_cfg -p
# Expected: Bash blocked by deny rule

# Test 6: Hooks from config
tmux send-keys -t t_cfg C-c
cat > /tmp/cfg_test/.san/settings.json << 'EOF'
{
  "hooks": {
    "SessionStart": [{
      "hooks": [{"type": "command", "command": "echo cfg-hook >> /tmp/cfg_hook.txt"}]
    }]
  }
}
EOF
tmux send-keys -t t_cfg 'cd /tmp/cfg_test && san' Enter
sleep 3
cat /tmp/cfg_hook.txt
# Expected: "cfg-hook"

tmux send-keys -t t_cfg C-c
tmux kill-session -t t_cfg
mv ~/.san/settings.json.bak ~/.san/settings.json 2>/dev/null || true
rm -rf /tmp/cfg_test /tmp/cfg_hook.txt
```
