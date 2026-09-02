package hook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/setting"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func stubCompleter(response string) LLMCompleter {
	return func(_ context.Context, _, _, _ string) (string, error) {
		return response, nil
	}
}

func Test_matchesEvent(t *testing.T) {
	tests := []struct {
		name       string
		matcher    string
		matchValue string
		want       bool
	}{
		{"empty matcher matches everything", "", "anything", true},
		{"wildcard matcher matches everything", "*", "anything", true},
		{"exact match", "Bash", "Bash", true},
		{"exact match fails", "Bash", "Edit", false},
		{"regex or pattern", "Write|Edit", "Write", true},
		{"regex or pattern second", "Write|Edit", "Edit", true},
		{"regex or pattern fails", "Write|Edit", "Bash", false},
		{"regex prefix", "Bash.*", "BashTool", true},
		{"regex prefix fails", "Bash.*", "XBash", false},
		{"invalid regex falls back to exact", "[invalid", "[invalid", true},
		{"invalid regex fails", "[invalid", "other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesEvent(tt.matcher, tt.matchValue)
			if got != tt.want {
				t.Errorf("matchesEvent(%q, %q) = %v, want %v", tt.matcher, tt.matchValue, got, tt.want)
			}
		})
	}
}

func Test_getMatchValue(t *testing.T) {
	tests := []struct {
		event EventType
		input HookInput
		want  string
	}{
		{PreToolUse, HookInput{ToolName: "Bash"}, "Bash"},
		{PostToolUse, HookInput{ToolName: "Edit"}, "Edit"},
		{PostToolUseFailure, HookInput{ToolName: "Write"}, "Write"},
		{PermissionRequest, HookInput{ToolName: "Task"}, "Task"},
		{PermissionDenied, HookInput{ToolName: "Bash"}, "Bash"},
		{Setup, HookInput{Trigger: "init"}, "init"},
		{SessionStart, HookInput{Source: "startup"}, "startup"},
		{SessionEnd, HookInput{Reason: "quit"}, "quit"},
		{Notification, HookInput{NotificationType: "permission_prompt"}, "permission_prompt"},
		{SubagentStart, HookInput{AgentName: "Explore"}, "Explore"},
		{SubagentStop, HookInput{AgentName: "Plan"}, "Plan"},
		{TaskCreated, HookInput{TaskSubject: "Explore"}, "Explore"},
		{TaskCompleted, HookInput{TaskSubject: "Plan"}, "Plan"},
		{ConfigChange, HookInput{Source: "user_settings"}, "user_settings"},
		{InstructionsLoaded, HookInput{FilePath: "/tmp/AGENTS.md"}, "/tmp/AGENTS.md"},
		{CwdChanged, HookInput{NewCwd: "/tmp/worktree"}, "/tmp/worktree"},
		{FileChanged, HookInput{FilePath: "/tmp/file.txt"}, "/tmp/file.txt"},
		{PreCompact, HookInput{Trigger: "auto"}, "auto"},
		{PostCompact, HookInput{Trigger: "manual"}, "manual"},
		{WorktreeCreate, HookInput{Name: "feature-123"}, "feature-123"},
		{WorktreeRemove, HookInput{WorktreePath: "/tmp/wt"}, "/tmp/wt"},
		{UserPromptSubmit, HookInput{Prompt: "hello"}, ""}, // No matcher support
		{Stop, HookInput{}, ""},                            // No matcher support
	}

	for _, tt := range tests {
		t.Run(string(tt.event), func(t *testing.T) {
			got := getMatchValue(tt.event, tt.input)
			if got != tt.want {
				t.Errorf("getMatchValue(%v, %+v) = %q, want %q", tt.event, tt.input, got, tt.want)
			}
		})
	}
}

func Test_eventSupportsMatcher(t *testing.T) {
	supported := []EventType{
		PreToolUse, PostToolUse, PostToolUseFailure, PermissionRequest,
		PermissionDenied, Setup, SessionStart, SessionEnd, Notification,
		SubagentStart, SubagentStop, TaskCreated, TaskCompleted, ConfigChange, InstructionsLoaded, CwdChanged, FileChanged, PreCompact, PostCompact,
		WorktreeCreate, WorktreeRemove,
	}

	notSupported := []EventType{
		UserPromptSubmit, Stop, StopFailure,
	}

	for _, event := range supported {
		if !eventSupportsMatcher(event) {
			t.Errorf("eventSupportsMatcher(%v) = false, want true", event)
		}
	}

	for _, event := range notSupported {
		if eventSupportsMatcher(event) {
			t.Errorf("eventSupportsMatcher(%v) = true, want false", event)
		}
	}
}

func TestEngineNoHooks(t *testing.T) {
	settings := setting.NewData()
	engine := NewEngine(settings, "test-session", "/tmp", "")

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	if !outcome.ShouldContinue {
		t.Error("Expected ShouldContinue=true when no hooks configured")
	}
	if outcome.ShouldBlock {
		t.Error("Expected ShouldBlock=false when no hooks configured")
	}
}

func TestEngineNilSettings(t *testing.T) {
	engine := NewEngine(nil, "test-session", "/tmp", "")

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	if !outcome.ShouldContinue {
		t.Error("Expected ShouldContinue=true with nil settings")
	}
}

func TestEngineHasHooks(t *testing.T) {
	settings := setting.NewData()
	settings.Hooks["Stop"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: "echo done"}}},
	}

	engine := NewEngine(settings, "test-session", "/tmp", "")

	if !engine.HasHooks(Stop) {
		t.Error("Expected HasHooks(Stop)=true")
	}
	if engine.HasHooks(PreToolUse) {
		t.Error("Expected HasHooks(PreToolUse)=false")
	}
}

func TestMatchesIfConditionMatchesBashSubcommands(t *testing.T) {
	engine := NewEngine(setting.NewData(), "test-session", "/tmp/repo", "")
	cmd := setting.HookCmd{If: "Bash(git:*)"}
	input := HookInput{
		HookEventName: string(PreToolUse),
		ToolName:      "Bash",
		ToolInput:     map[string]any{"command": "cd /tmp/repo && git status"},
	}

	if !engine.matchesIfCondition(cmd, input) {
		t.Fatal("expected hook If condition to match bash subcommand after leading cd")
	}
}

func TestEngineRuntimeAndSessionHooks(t *testing.T) {
	engine := NewEngine(setting.NewData(), "test-session", "/tmp", "")
	engine.store.AddRuntimeHook(PreToolUse, "Bash", setting.HookCmd{Type: "command", Command: "echo runtime"})
	engine.store.AddSessionHook(Stop, "", setting.HookCmd{Type: "command", Command: "echo session"})
	engine.AddRuntimeFunctionHook(StopFailure, "", FunctionHook{
		Callback: func(_ context.Context, _ HookInput) (HookOutput, error) {
			msg := "runtime function"
			return HookOutput{SystemMessage: msg}, nil
		},
	})
	engine.AddSessionFunctionHook(Notification, "", FunctionHook{
		Callback: func(_ context.Context, _ HookInput) (HookOutput, error) {
			msg := "session function"
			return HookOutput{SystemMessage: msg}, nil
		},
	})

	if !engine.HasHooks(PreToolUse) {
		t.Fatal("expected runtime hook to be visible")
	}
	if !engine.HasHooks(Stop) {
		t.Fatal("expected session hook to be visible")
	}
	if !engine.HasHooks(StopFailure) {
		t.Fatal("expected runtime function hook to be visible")
	}
	if !engine.HasHooks(Notification) {
		t.Fatal("expected session function hook to be visible")
	}

	engine.ClearSessionHooks()
	if !engine.HasHooks(PreToolUse) {
		t.Fatal("runtime hook should remain after clearing session hooks")
	}
	if !engine.HasHooks(StopFailure) {
		t.Fatal("runtime function hook should remain after clearing session hooks")
	}
	if engine.HasHooks(Notification) {
		t.Fatal("session function hook should be cleared")
	}
}

func TestEngineSessionFunctionHook(t *testing.T) {
	engine := NewEngine(setting.NewData(), "test-session", "/tmp", "")
	id := engine.AddSessionFunctionHook(PreToolUse, "Bash", FunctionHook{
		Callback: func(_ context.Context, input HookInput) (HookOutput, error) {
			if input.ToolName != "Bash" {
				t.Fatalf("unexpected tool name: %q", input.ToolName)
			}
			msg := "function hook fired"
			return HookOutput{SystemMessage: msg}, nil
		},
	})
	if id == "" {
		t.Fatal("expected generated function hook ID")
	}

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})
	if outcome.AdditionalContext != "function hook fired" {
		t.Fatalf("expected function hook context, got %q", outcome.AdditionalContext)
	}

	outcome = engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Write"})
	if outcome.AdditionalContext != "" {
		t.Fatalf("expected matcher-missed function hook to skip, got %q", outcome.AdditionalContext)
	}
}

func TestEngineRemoveSessionFunctionHook(t *testing.T) {
	engine := NewEngine(setting.NewData(), "test-session", "/tmp", "")
	id := engine.AddSessionFunctionHook(Stop, "", FunctionHook{
		ID: "fn-stop",
		Callback: func(_ context.Context, _ HookInput) (HookOutput, error) {
			msg := "should not run"
			return HookOutput{SystemMessage: msg}, nil
		},
	})
	if id != "fn-stop" {
		t.Fatalf("expected stable hook ID, got %q", id)
	}
	if !engine.store.RemoveSessionFunctionHook(Stop, id) {
		t.Fatal("expected function hook removal to succeed")
	}

	outcome := engine.Execute(context.Background(), Stop, HookInput{})
	if outcome.AdditionalContext != "" {
		t.Fatalf("expected removed function hook to stop running, got %q", outcome.AdditionalContext)
	}
}

func TestEngineMatcherFiltering(t *testing.T) {
	// Create a temp script that outputs JSON
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "hook.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"systemMessage":"hook executed"}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{
			Matcher: "Bash",
			Hooks:   []setting.HookCmd{{Type: "command", Command: scriptPath}},
		},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")

	// Should match
	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})
	if outcome.AdditionalContext != "hook executed" {
		t.Errorf("Expected context from hook, got %q", outcome.AdditionalContext)
	}

	// Should not match
	outcome = engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Edit"})
	if outcome.AdditionalContext != "" {
		t.Errorf("Expected no context for non-matching tool, got %q", outcome.AdditionalContext)
	}
}

func TestEngineIfConditionFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "hook.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"systemMessage":"hook executed"}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{
			Hooks: []setting.HookCmd{{
				Type:    "command",
				Command: scriptPath,
				If:      "Bash(git *)",
			}},
		},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git status"},
	})
	if outcome.AdditionalContext != "hook executed" {
		t.Fatalf("expected if-filtered hook to match git command, got %q", outcome.AdditionalContext)
	}

	outcome = engine.Execute(context.Background(), PreToolUse, HookInput{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "npm test"},
	})
	if outcome.AdditionalContext != "" {
		t.Fatalf("expected if-filtered hook to skip non-matching command, got %q", outcome.AdditionalContext)
	}
}

func TestEngineBlockingHook(t *testing.T) {
	// Create a script that exits with code 2 (blocking)
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "block.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo "Blocked by policy" >&2
exit 2
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	if outcome.ShouldContinue {
		t.Error("Expected ShouldContinue=false for blocking hook")
	}
	if !outcome.ShouldBlock {
		t.Error("Expected ShouldBlock=true for blocking hook")
	}
	if outcome.BlockReason != "Blocked by policy" {
		t.Errorf("Expected BlockReason='Blocked by policy', got %q", outcome.BlockReason)
	}
}

func TestEngineJSONBlockingOutput(t *testing.T) {
	// Create a script that outputs JSON with continue=false
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "deny.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"continue":false,"stopReason":"Denied by hook"}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	if outcome.ShouldContinue {
		t.Error("Expected ShouldContinue=false")
	}
	if !outcome.ShouldBlock {
		t.Error("Expected ShouldBlock=true")
	}
	if outcome.BlockReason != "Denied by hook" {
		t.Errorf("Expected BlockReason='Denied by hook', got %q", outcome.BlockReason)
	}
}

func TestEngineUpdatedInput(t *testing.T) {
	// Create a script that modifies tool input
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "modify.sh")
	updatedInput := map[string]any{"command": "safe-command"}
	updatedJSON, _ := json.Marshal(updatedInput)
	script := `#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":` + string(updatedJSON) + `}}'
`
	err := os.WriteFile(scriptPath, []byte(script), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "rm -rf /"},
	})

	if outcome.UpdatedInput == nil {
		t.Fatal("Expected UpdatedInput to be set")
	}
	if outcome.UpdatedInput["command"] != "safe-command" {
		t.Errorf("Expected command='safe-command', got %v", outcome.UpdatedInput["command"])
	}
}

func TestEngineEnvironmentVariables(t *testing.T) {
	// Create a script that outputs env vars
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "env.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo "{\"systemMessage\":\"SAN=$SAN_PROJECT_DIR CLAUDE=$CLAUDE_PROJECT_DIR\"}"
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["Stop"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")

	outcome := engine.Execute(context.Background(), Stop, HookInput{})

	expected := "SAN=" + tmpDir + " CLAUDE=" + tmpDir
	if outcome.AdditionalContext != expected {
		t.Errorf("Expected context=%q, got %q", expected, outcome.AdditionalContext)
	}
}

func TestEnginePermissionMode(t *testing.T) {
	engine := NewEngine(setting.NewData(), "test-session", "/tmp", "")

	engine.SetPermissionMode("auto")

	// Just verify it doesn't panic - we'd need a hook that reads stdin to fully test
}

func TestHooks_Timeout_TerminatesHook(t *testing.T) {
	// Create a script that uses exec to replace the shell process so
	// exec.CommandContext can kill it directly (no orphaned children).
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "sleep.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
exec sleep 30
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	// Timeout of 1 second — the sleep process will be killed after 1s
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath, Timeout: 1}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")

	start := time.Now()
	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})
	elapsed := time.Since(start)

	// Should terminate well before the sleep duration (30s)
	if elapsed > 5*time.Second {
		t.Errorf("expected hook to be killed within ~1s, took %v", elapsed)
	}

	// A timeout-killed process: engine should still continue (not exit 2)
	if !outcome.ShouldContinue {
		t.Error("expected main loop to continue after hook timeout")
	}
	if outcome.ShouldBlock {
		t.Error("expected ShouldBlock=false after hook timeout (not exit 2)")
	}
}

func TestHooks_Once_ExecutesExactlyOnce(t *testing.T) {
	tmpDir := t.TempDir()
	counterFile := filepath.Join(tmpDir, "count.txt")

	// Script increments a counter file each time it runs
	scriptPath := filepath.Join(tmpDir, "counter.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo -n "x" >> `+counterFile+`
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath, Once: true}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")

	// Fire the hook twice
	engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})
	engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	// Read the counter — should be exactly 1 character (fired once)
	content, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatalf("counter file not created: %v", err)
	}
	if len(content) != 1 {
		t.Errorf("expected hook to fire exactly once, but counter=%d (content=%q)", len(content), content)
	}
}

func TestHooks_InputContains_SessionContext(t *testing.T) {
	tmpDir := t.TempDir()
	captureFile := filepath.Join(tmpDir, "input.json")

	// Script captures stdin (the hook input JSON) to a file
	scriptPath := filepath.Join(tmpDir, "capture.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
cat > `+captureFile+`
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	const sessionID = "test-session-xyz"
	engine := NewEngine(settings, sessionID, tmpDir, "")

	engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	// Read captured JSON
	data, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("capture file not created: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("captured input is not valid JSON: %v\nContent: %s", err, data)
	}

	// Must include session_id
	sid, ok := parsed["session_id"].(string)
	if !ok || sid != sessionID {
		t.Errorf("expected session_id=%q in hook input, got %v", sessionID, parsed["session_id"])
	}

	// Must include cwd
	cwd, ok := parsed["cwd"].(string)
	if !ok || cwd != tmpDir {
		t.Errorf("expected cwd=%q in hook input, got %v", tmpDir, parsed["cwd"])
	}
}

func TestHooks_PermissionModeIncludedOnlyForRelevantEvents(t *testing.T) {
	tmpDir := t.TempDir()
	captureFile := filepath.Join(tmpDir, "input.json")
	scriptPath := filepath.Join(tmpDir, "capture.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
cat > `+captureFile+`
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PermissionDenied"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	engine.SetPermissionMode("plan")
	engine.Execute(context.Background(), PermissionDenied, HookInput{ToolName: "Write"})

	data, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("capture file not created: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("captured input is not valid JSON: %v\nContent: %s", err, data)
	}

	if parsed["permission_mode"] != "plan" {
		t.Fatalf("expected permission_mode=plan, got %v", parsed["permission_mode"])
	}
	if parsed["tool_name"] != "Write" {
		t.Fatalf("expected tool_name=Write, got %v", parsed["tool_name"])
	}
}

// === Reverse Control #3: Inject System Context (additionalContext) ===

func TestHooks_InjectAdditionalContext(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "context.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"Review: all edits must include tests"}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Edit"})

	if outcome.AdditionalContext != "Review: all edits must include tests" {
		t.Errorf("expected additionalContext, got %q", outcome.AdditionalContext)
	}
	if outcome.ShouldBlock {
		t.Error("additionalContext should not block")
	}
}

func TestHooks_ExtractWatchPaths(t *testing.T) {
	settings := setting.NewData()
	engine := NewEngine(settings, "test-session", t.TempDir(), "")
	engine.AddSessionFunctionHook(SessionStart, "", FunctionHook{
		Callback: func(_ context.Context, _ HookInput) (HookOutput, error) {
			return HookOutput{
				HookSpecificOutput: &HookSpecificOutput{
					HookEventName: "SessionStart",
					WatchPaths:    []string{"/tmp/.env", "/tmp/.envrc"},
				},
			}, nil
		},
	})

	outcome := engine.Execute(context.Background(), SessionStart, HookInput{})
	if len(outcome.WatchPaths) != 2 {
		t.Fatalf("expected 2 watch paths, got %#v", outcome.WatchPaths)
	}
	if outcome.WatchPaths[0] != "/tmp/.env" || outcome.WatchPaths[1] != "/tmp/.envrc" {
		t.Fatalf("unexpected watch paths: %#v", outcome.WatchPaths)
	}
}

func TestHooks_MergeWatchPathsFromMultipleHooks(t *testing.T) {
	engine := NewEngine(setting.NewData(), "test-session", t.TempDir(), "")
	engine.AddSessionFunctionHook(SessionStart, "", FunctionHook{
		ID: "watch-a",
		Callback: func(_ context.Context, _ HookInput) (HookOutput, error) {
			return HookOutput{
				HookSpecificOutput: &HookSpecificOutput{
					HookEventName: "SessionStart",
					WatchPaths:    []string{"/tmp/a", "/tmp/b"},
				},
			}, nil
		},
	})
	engine.AddSessionFunctionHook(SessionStart, "", FunctionHook{
		ID: "watch-b",
		Callback: func(_ context.Context, _ HookInput) (HookOutput, error) {
			return HookOutput{
				HookSpecificOutput: &HookSpecificOutput{
					HookEventName: "SessionStart",
					WatchPaths:    []string{"/tmp/c"},
				},
			}, nil
		},
	})

	outcome := engine.Execute(context.Background(), SessionStart, HookInput{})
	want := []string{"/tmp/a", "/tmp/b", "/tmp/c"}
	if len(outcome.WatchPaths) != len(want) {
		t.Fatalf("expected %d watch paths, got %#v", len(want), outcome.WatchPaths)
	}
	for i, path := range want {
		if outcome.WatchPaths[i] != path {
			t.Fatalf("unexpected watch paths at %d: %#v", i, outcome.WatchPaths)
		}
	}
}

func TestHooks_ExtractInitialUserMessage(t *testing.T) {
	engine := NewEngine(setting.NewData(), "test-session", t.TempDir(), "")
	engine.AddSessionFunctionHook(SessionStart, "", FunctionHook{
		Callback: func(_ context.Context, _ HookInput) (HookOutput, error) {
			return HookOutput{
				HookSpecificOutput: &HookSpecificOutput{
					HookEventName:      "SessionStart",
					InitialUserMessage: "Inspect the repo and summarize risks.",
				},
			}, nil
		},
	})

	outcome := engine.Execute(context.Background(), SessionStart, HookInput{})
	if outcome.InitialUserMessage != "Inspect the repo and summarize risks." {
		t.Fatalf("unexpected initial user message: %q", outcome.InitialUserMessage)
	}
}

func TestHooks_CurrentStatusMessageTracksActiveHook(t *testing.T) {
	engine := NewEngine(setting.NewData(), "test-session", t.TempDir(), "")
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	engine.AddSessionFunctionHook(Notification, "", FunctionHook{
		StatusMessage: "running notification hook",
		Callback: func(_ context.Context, _ HookInput) (HookOutput, error) {
			started <- struct{}{}
			<-release
			return HookOutput{}, nil
		},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		engine.Execute(context.Background(), Notification, HookInput{NotificationType: "idle_prompt"})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hook to start")
	}

	if got := engine.CurrentStatusMessage(); got != "running notification hook" {
		t.Fatalf("expected active status message, got %q", got)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hook to finish")
	}

	if got := engine.CurrentStatusMessage(); got != "" {
		t.Fatalf("expected status message to clear, got %q", got)
	}
}

func TestHooks_ExtractPermissionDeniedRetry(t *testing.T) {
	engine := NewEngine(setting.NewData(), "test-session", t.TempDir(), "")
	engine.AddSessionFunctionHook(PermissionDenied, "", FunctionHook{
		Callback: func(_ context.Context, _ HookInput) (HookOutput, error) {
			return HookOutput{
				HookSpecificOutput: &HookSpecificOutput{
					HookEventName: "PermissionDenied",
					Retry:         true,
				},
			}, nil
		},
	})

	outcome := engine.Execute(context.Background(), PermissionDenied, HookInput{ToolName: "Write"})
	if !outcome.Retry {
		t.Fatal("expected retry=true from PermissionDenied hook output")
	}
}

func TestHooks_InjectSystemMessage(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "sysmsg.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"systemMessage":"injected context from hook"}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	if outcome.AdditionalContext != "injected context from hook" {
		t.Errorf("expected systemMessage as context, got %q", outcome.AdditionalContext)
	}
}

// === Reverse Control #4: PreToolUse Permission Decision ===

func TestHooks_PreToolUse_PermissionAllow(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "allow.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Write"})

	if !outcome.PermissionAllow {
		t.Error("expected PermissionAllow=true")
	}
	if outcome.HookSource != "PreToolUse" {
		t.Errorf("expected HookSource=PreToolUse, got %q", outcome.HookSource)
	}
	if outcome.ShouldBlock {
		t.Error("allow should not block")
	}
}

func TestHooks_PromptHook(t *testing.T) {
	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{
			Type:   "prompt",
			Prompt: `Evaluate $ARGUMENTS and allow.`,
		}}},
	}

	engine := NewEngine(settings, "test-session", t.TempDir(), "")
	engine.SetLLMCompleter(stubCompleter(
		`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`,
	), "test-model")

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})
	if !outcome.PermissionAllow {
		t.Fatal("expected prompt hook to allow execution")
	}
}

func TestHooks_AgentHook(t *testing.T) {
	settings := setting.NewData()
	settings.Hooks["PermissionRequest"] = []setting.Hook{
		{Matcher: "*", Hooks: []setting.HookCmd{{
			Type:   "agent",
			Prompt: `Review $ARGUMENTS and return a permission decision.`,
		}}},
	}

	engine := NewEngine(settings, "test-session", t.TempDir(), "")
	engine.SetLLMCompleter(stubCompleter(
		`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`,
	), "test-model")

	outcome := engine.Execute(context.Background(), PermissionRequest, HookInput{ToolName: "Write"})
	if !outcome.PermissionAllow {
		t.Fatal("expected agent hook to allow execution")
	}
}

func TestHooks_HTTPHook(t *testing.T) {
	t.Setenv("HOOK_TOKEN", "token-123")

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{
			Type:           "http",
			URL:            "https://hooks.example.test/pre-tool",
			Headers:        map[string]string{"Authorization": "Bearer $HOOK_TOKEN"},
			AllowedEnvVars: []string{"HOOK_TOKEN"},
		}}},
	}

	engine := NewEngine(settings, "test-session", t.TempDir(), "")
	engine.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
				t.Fatalf("unexpected Authorization header: %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`,
				)),
			}, nil
		}),
	}

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})
	if !outcome.PermissionAllow {
		t.Fatal("expected http hook to allow execution")
	}
}

func TestHooks_PreToolUse_PermissionDeny(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "deny.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"audit policy violation"}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	if !outcome.ShouldBlock {
		t.Error("expected ShouldBlock=true for permission deny")
	}
	if outcome.BlockReason != "audit policy violation" {
		t.Errorf("expected deny reason, got %q", outcome.BlockReason)
	}
}

// === Reverse Control #5: PermissionRequest Decision + Permission Updates ===

func TestHooks_PermissionRequest_AllowSimple(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "pr_allow.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PermissionRequest"] = []setting.Hook{
		{Matcher: "*", Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PermissionRequest, HookInput{ToolName: "Write"})

	if !outcome.PermissionAllow {
		t.Error("expected PermissionAllow=true")
	}
	if outcome.HookSource != "PermissionRequest" {
		t.Errorf("expected HookSource=PermissionRequest, got %q", outcome.HookSource)
	}
}

func TestHooks_PermissionRequest_Deny(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "pr_deny.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"admin rejected"}}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PermissionRequest"] = []setting.Hook{
		{Matcher: "*", Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PermissionRequest, HookInput{ToolName: "Bash"})

	if !outcome.ShouldBlock {
		t.Error("expected ShouldBlock=true for deny")
	}
	if outcome.BlockReason != "admin rejected" {
		t.Errorf("expected block reason 'admin rejected', got %q", outcome.BlockReason)
	}
}

func TestHooks_PermissionRequest_AllowWithSetMode(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "pr_bypass.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"bypassPermissions","destination":"session"}]}}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PermissionRequest"] = []setting.Hook{
		{Matcher: "*", Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PermissionRequest, HookInput{ToolName: "Edit"})

	if !outcome.PermissionAllow {
		t.Error("expected PermissionAllow=true")
	}
	if len(outcome.UpdatedPermissions) != 1 {
		t.Fatalf("expected 1 permission update, got %d", len(outcome.UpdatedPermissions))
	}
	pu := outcome.UpdatedPermissions[0]
	if pu.Type != "setMode" {
		t.Errorf("expected type=setMode, got %q", pu.Type)
	}
	if pu.Mode != "bypassPermissions" {
		t.Errorf("expected mode=bypassPermissions, got %q", pu.Mode)
	}
	if pu.Destination != "session" {
		t.Errorf("expected destination=session, got %q", pu.Destination)
	}
}

func TestHooks_PermissionRequest_AllowWithAddRules(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "pr_rules.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedPermissions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"git"}],"behavior":"allow","destination":"session"}]}}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PermissionRequest"] = []setting.Hook{
		{Matcher: "*", Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PermissionRequest, HookInput{ToolName: "Bash"})

	if !outcome.PermissionAllow {
		t.Error("expected PermissionAllow=true")
	}
	if len(outcome.UpdatedPermissions) != 1 {
		t.Fatalf("expected 1 permission update, got %d", len(outcome.UpdatedPermissions))
	}
	pu := outcome.UpdatedPermissions[0]
	if pu.Type != "addRules" {
		t.Errorf("expected type=addRules, got %q", pu.Type)
	}
	if len(pu.Rules) != 1 || pu.Rules[0].ToolName != "Bash" || pu.Rules[0].RuleContent != "git" {
		t.Errorf("unexpected rules: %+v", pu.Rules)
	}
	if pu.Behavior != "allow" {
		t.Errorf("expected behavior=allow, got %q", pu.Behavior)
	}
}

func TestHooks_PermissionRequest_AllowWithAddDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "pr_dirs.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedPermissions":[{"type":"addDirectories","directories":["/tmp","/var/log"],"destination":"session"}]}}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PermissionRequest"] = []setting.Hook{
		{Matcher: "*", Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PermissionRequest, HookInput{ToolName: "Write"})

	if !outcome.PermissionAllow {
		t.Error("expected PermissionAllow=true")
	}
	if len(outcome.UpdatedPermissions) != 1 {
		t.Fatalf("expected 1 permission update, got %d", len(outcome.UpdatedPermissions))
	}
	pu := outcome.UpdatedPermissions[0]
	if pu.Type != "addDirectories" {
		t.Errorf("expected type=addDirectories, got %q", pu.Type)
	}
	if len(pu.Directories) != 2 || pu.Directories[0] != "/tmp" || pu.Directories[1] != "/var/log" {
		t.Errorf("unexpected directories: %v", pu.Directories)
	}
}

func TestHooks_PermissionRequest_AllowWithUpdatedInput(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "pr_input.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedInput":{"file_path":"/safe/path.txt"}}}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PermissionRequest"] = []setting.Hook{
		{Matcher: "*", Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PermissionRequest, HookInput{
		ToolName:  "Write",
		ToolInput: map[string]any{"file_path": "/original/path.txt"},
	})

	if !outcome.PermissionAllow {
		t.Error("expected PermissionAllow=true")
	}
	if outcome.UpdatedInput == nil {
		t.Fatal("expected UpdatedInput to be set")
	}
	if outcome.UpdatedInput["file_path"] != "/safe/path.txt" {
		t.Errorf("expected updated file_path, got %v", outcome.UpdatedInput["file_path"])
	}
}

// === Reverse Control #6: Bidirectional Prompt Protocol ===

func TestHooks_BidirectionalPrompt_SingleRound(t *testing.T) {
	tmpDir := t.TempDir()
	// Script sends a PromptRequest, reads the response, then outputs final JSON
	scriptPath := filepath.Join(tmpDir, "prompt.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
# Read initial input from stdin
read -r INPUT
# Send a prompt request
echo '{"prompt":"confirm","message":"Proceed?","options":[{"key":"yes","label":"Yes"},{"key":"no","label":"No"}]}'
# Read prompt response
read -r RESPONSE
# Parse the selected value and use it in final output
SELECTED=$(echo "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('selected',''))" 2>/dev/null)
if [ "$SELECTED" = "yes" ]; then
  echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
else
  echo '{"continue":false,"reason":"user declined"}'
fi
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	interactive(engine)

	// Set up a prompt callback that auto-approves
	engine.SetPromptCallback(func(req PromptRequest) (PromptResponse, bool) {
		if req.Prompt != "confirm" {
			t.Errorf("unexpected prompt ID: %q", req.Prompt)
		}
		if req.Message != "Proceed?" {
			t.Errorf("unexpected message: %q", req.Message)
		}
		if len(req.Options) != 2 {
			t.Errorf("expected 2 options, got %d", len(req.Options))
		}
		return PromptResponse{
			PromptResponse: "confirm",
			Selected:       "yes",
		}, false
	})

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	if !outcome.PermissionAllow {
		t.Error("expected PermissionAllow=true after user approved prompt")
	}
	if outcome.ShouldBlock {
		t.Error("should not block after approval")
	}
}

func TestHooks_BidirectionalPrompt_UserDeclines(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "prompt_deny.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
read -r INPUT
echo '{"prompt":"confirm","message":"Allow this?","options":[{"key":"yes","label":"Yes"},{"key":"no","label":"No"}]}'
read -r RESPONSE
SELECTED=$(echo "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('selected',''))" 2>/dev/null)
if [ "$SELECTED" = "yes" ]; then
  echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
else
  echo '{"continue":false,"reason":"user declined"}'
fi
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	interactive(engine)
	engine.SetPromptCallback(func(req PromptRequest) (PromptResponse, bool) {
		return PromptResponse{PromptResponse: "confirm", Selected: "no"}, false
	})

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	if !outcome.ShouldBlock {
		t.Error("expected ShouldBlock=true after user declined")
	}
	if outcome.BlockReason != "user declined" {
		t.Errorf("expected reason 'user declined', got %q", outcome.BlockReason)
	}
}

func TestHooks_BidirectionalPrompt_Cancelled(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "prompt_cancel.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
read -r INPUT
echo '{"prompt":"confirm","message":"Continue?"}'
# If stdin closes (cancelled), script exits naturally
read -r RESPONSE || exit 0
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	interactive(engine)
	engine.SetPromptCallback(func(req PromptRequest) (PromptResponse, bool) {
		// User cancels the prompt
		return PromptResponse{}, true
	})

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	// Cancelled prompt should continue (no block, no allow — just pass through)
	if outcome.ShouldBlock {
		t.Error("cancelled prompt should not block")
	}
	if outcome.PermissionAllow {
		t.Error("cancelled prompt should not allow")
	}
}

func TestHooks_BidirectionalPrompt_MultiRound(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "multi_prompt.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
read -r INPUT
# Round 1: ask for environment
echo '{"prompt":"env","message":"Which environment?"}'
read -r RESP1
ENV=$(echo "$RESP1" | python3 -c "import sys,json; print(json.load(sys.stdin).get('selected',''))" 2>/dev/null)
# Round 2: confirm
echo '{"prompt":"confirm","message":"Deploy to '"$ENV"'?"}'
read -r RESP2
CONFIRM=$(echo "$RESP2" | python3 -c "import sys,json; print(json.load(sys.stdin).get('selected',''))" 2>/dev/null)
if [ "$CONFIRM" = "yes" ]; then
  echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
else
  echo '{"continue":false,"reason":"deployment cancelled"}'
fi
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	interactive(engine)

	callCount := 0
	engine.SetPromptCallback(func(req PromptRequest) (PromptResponse, bool) {
		callCount++
		switch req.Prompt {
		case "env":
			return PromptResponse{PromptResponse: "env", Selected: "staging"}, false
		case "confirm":
			return PromptResponse{PromptResponse: "confirm", Selected: "yes"}, false
		default:
			t.Errorf("unexpected prompt: %q", req.Prompt)
			return PromptResponse{}, true
		}
	})

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	if callCount != 2 {
		t.Errorf("expected 2 prompt rounds, got %d", callCount)
	}
	if !outcome.PermissionAllow {
		t.Error("expected PermissionAllow=true after multi-round approval")
	}
}

func TestHooks_BidirectionalPrompt_AsyncDetach(t *testing.T) {
	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "async_marker.txt")
	scriptPath := filepath.Join(tmpDir, "async_detach.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
read -r INPUT
# First line signals async
echo '{"async":true}'
# Background work (simulated)
echo "async_done" > `+markerFile+`
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	interactive(engine)
	engine.SetPromptCallback(func(req PromptRequest) (PromptResponse, bool) {
		t.Error("prompt callback should NOT be called for async-detached hooks")
		return PromptResponse{}, true
	})

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	// Async detach: should continue without blocking or allowing
	if outcome.ShouldBlock {
		t.Error("async detach should not block")
	}
	if outcome.PermissionAllow {
		t.Error("async detach should not set PermissionAllow")
	}

	// Poll for the marker file with a generous timeout for CI
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(markerFile); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		t.Error("expected async hook to have run in background")
	}
}

func TestHooks_AsyncRewakeCallback(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "async_rewake.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
sleep 0.1
echo "background policy blocked this" >&2
exit 2
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath, AsyncRewake: true}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	resultCh := make(chan AsyncHookResult, 1)
	engine.SetAsyncHookCallback(func(result AsyncHookResult) {
		resultCh <- result
	})

	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Write"})
	if outcome.ShouldBlock {
		t.Fatal("asyncRewake should not synchronously block the caller")
	}

	select {
	case result := <-resultCh:
		if result.Event != PreToolUse {
			t.Fatalf("expected PreToolUse async callback, got %v", result.Event)
		}
		if result.BlockReason != "background policy blocked this" {
			t.Fatalf("unexpected block reason: %q", result.BlockReason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async rewake callback")
	}
}

// === PreToolUse "ask" — forces permission prompt even for normally auto-allowed tools ===

func TestHooks_PreToolUse_PermissionAsk(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "ask.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask"}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Read"})

	if !outcome.ForceAsk {
		t.Error("expected ForceAsk=true for permissionDecision:ask")
	}
	if outcome.PermissionAllow {
		t.Error("ask should not set PermissionAllow")
	}
	if outcome.ShouldBlock {
		t.Error("ask should not block")
	}
}

// === PreToolUse decision field with updatedPermissions is IGNORED (PermissionRequest only) ===

func TestHooks_PreToolUse_DecisionFieldIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	// PreToolUse hook tries to return PermissionRequest-style decision — should be ignored
	scriptPath := filepath.Join(tmpDir, "wrongdecision.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","decision":{"behavior":"allow","updatedPermissions":[{"type":"setMode","mode":"bypassPermissions","destination":"session"}]}}}'
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["PreToolUse"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	outcome := engine.Execute(context.Background(), PreToolUse, HookInput{ToolName: "Bash"})

	// The decision field should be ignored for PreToolUse (hookEventName != PermissionRequest)
	if outcome.PermissionAllow {
		t.Error("decision field should be ignored for PreToolUse — PermissionAllow should be false")
	}
	if len(outcome.UpdatedPermissions) > 0 {
		t.Errorf("decision.updatedPermissions should be ignored for PreToolUse, got %d updates", len(outcome.UpdatedPermissions))
	}
}

func TestHooks_SessionStartOmitsPermissionMode(t *testing.T) {
	tmpDir := t.TempDir()
	captureFile := filepath.Join(tmpDir, "input.json")
	scriptPath := filepath.Join(tmpDir, "capture.sh")
	err := os.WriteFile(scriptPath, []byte(`#!/bin/bash
cat > `+captureFile+`
`), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	settings := setting.NewData()
	settings.Hooks["SessionStart"] = []setting.Hook{
		{Hooks: []setting.HookCmd{{Type: "command", Command: scriptPath}}},
	}

	engine := NewEngine(settings, "test-session", tmpDir, "")
	engine.SetPermissionMode("auto")
	engine.Execute(context.Background(), SessionStart, HookInput{Source: "resume"})

	data, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("capture file not created: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("captured input is not valid JSON: %v\nContent: %s", err, data)
	}

	if _, ok := parsed["permission_mode"]; ok {
		t.Fatalf("did not expect permission_mode for SessionStart, got %v", parsed["permission_mode"])
	}
	if parsed["source"] != "resume" {
		t.Fatalf("expected source=resume, got %v", parsed["source"])
	}
}

// interactive gives a hook that talks back enough room to say its first word.
//
// The engine closes a hook's stdin when it has been quiet for a moment, so
// that a hook reading to EOF is not left waiting forever. An interactive hook
// is supposed to beat that timer, and on an idle machine it does — but a test
// cannot control how loaded the machine is, and one that races a wall clock is
// a test that fails for reasons that have nothing to do with what it checks.
func interactive(e *Engine) { e.stdinIdle = 10 * time.Second }
