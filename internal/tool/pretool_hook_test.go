package tool

import (
	"context"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/hook"
)

type captureCoreTool struct {
	input map[string]any
}

func (t *captureCoreTool) Name() string            { return "Bash" }
func (t *captureCoreTool) Description() string     { return "test" }
func (t *captureCoreTool) Schema() core.ToolSchema { return core.ToolSchema{Name: t.Name()} }
func (t *captureCoreTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	t.input = input
	return "ok", nil
}

type fakeHookHandler struct {
	outcome hook.HookOutcome
	input   hook.HookInput
}

func (h *fakeHookHandler) Execute(ctx context.Context, event hook.EventType, input hook.HookInput) hook.HookOutcome {
	h.input = input
	outcome := h.outcome
	if !outcome.ShouldBlock {
		outcome.ShouldContinue = true
	}
	return outcome
}
func (h *fakeHookHandler) ExecuteAsync(event hook.EventType, input hook.HookInput) {}
func (h *fakeHookHandler) HasHooks(event hook.EventType) bool                      { return event == hook.PreToolUse }
func (h *fakeHookHandler) StopHookActive() *bool                                   { return nil }

func TestWithPreToolUseHooksAppliesUpdatedInput(t *testing.T) {
	inner := &captureCoreTool{}
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{
		UpdatedInput: map[string]any{"command": "rtk git status"},
	}}
	tools := WithPreToolUseHooks(core.NewTools(inner), hooks)

	_, err := tools.Get("Bash").Execute(context.Background(), map[string]any{"command": "git status"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if hooks.input.ToolName != "Bash" || hooks.input.ToolInput["command"] != "git status" {
		t.Fatalf("hook received unexpected input: %#v", hooks.input)
	}
	if inner.input["command"] != "rtk git status" {
		t.Fatalf("tool executed with input %#v", inner.input)
	}
}

type fakePreToolPermissionChecker struct {
	called      bool
	forcePrompt bool
	reason      string
	allow       bool
	// refuseHookAllow makes the checker answer HonorsHookAllow with false, the
	// way the real gate does for a deny rule, the circuit breaker, a
	// confirmation tier or an explicit ask rule.
	refuseHookAllow bool
}

func (c *fakePreToolPermissionChecker) Check(ctx context.Context, name string, input map[string]any, forcePrompt bool, reason string) (bool, string) {
	c.called = true
	c.forcePrompt = forcePrompt
	c.reason = reason
	if c.allow {
		return true, ""
	}
	return false, "should not be used"
}

func (c *fakePreToolPermissionChecker) HonorsHookAllow(name string, input map[string]any) bool {
	return !c.refuseHookAllow
}

func TestPreToolUseAllowOverridesPermissionPrompt(t *testing.T) {
	inner := &captureCoreTool{}
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{PermissionAllow: true}}
	checker := &fakePreToolPermissionChecker{}
	tools := WithPreToolUseAndPermission(core.NewTools(inner), hooks, checker)

	_, err := tools.Get("Bash").Execute(context.Background(), map[string]any{"command": "git status"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if checker.called {
		t.Fatal("permission checker should not run after PreToolUse allow")
	}
}

// A hook allow waives the routine prompt, not the rules. When the checker says
// the waiver does not hold — a deny rule, the circuit breaker, a confirmation
// tier or an explicit ask rule — the call is gated as if no hook had spoken.
func TestPreToolUseAllowStillGatedWhenNotHonored(t *testing.T) {
	inner := &captureCoreTool{}
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{PermissionAllow: true}}
	checker := &fakePreToolPermissionChecker{refuseHookAllow: true}
	tools := WithPreToolUseAndPermission(core.NewTools(inner), hooks, checker)

	_, err := tools.Get("Bash").Execute(context.Background(), map[string]any{"command": "rm -rf important/"})
	if err == nil {
		t.Fatal("expected the gate to block the call")
	}
	if !checker.called {
		t.Fatal("permission checker should run when the hook allow is not honored")
	}
	if inner.input != nil {
		t.Fatalf("tool ran despite the gate refusing the call, input %#v", inner.input)
	}
}

func TestPreToolUseAskForcesPermissionPrompt(t *testing.T) {
	inner := &captureCoreTool{}
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{ForceAsk: true, PermissionReason: "explain this command"}}
	checker := &fakePreToolPermissionChecker{allow: true}
	tools := WithPreToolUseAndPermission(core.NewTools(inner), hooks, checker)

	_, err := tools.Get("Bash").Execute(context.Background(), map[string]any{"command": "git status"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !checker.called || !checker.forcePrompt || checker.reason != "explain this command" {
		t.Fatalf("permission checker received forcePrompt=%v reason=%q called=%v", checker.forcePrompt, checker.reason, checker.called)
	}
}

func TestPreToolUseContinueFalseBlocksWithSystemMessage(t *testing.T) {
	inner := &captureCoreTool{}
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{ShouldContinue: false, ShouldBlock: true, AdditionalContext: "stop here"}}
	tools := WithPreToolUseHooks(core.NewTools(inner), hooks)

	_, err := tools.Get("Bash").Execute(context.Background(), map[string]any{"command": "git status"})
	if err == nil || err.Error() != "blocked: stop here" {
		t.Fatalf("Execute returned error %v", err)
	}
}

func TestPreToolUseAskWinsOverAllow(t *testing.T) {
	inner := &captureCoreTool{}
	// Two hooks disagree: one allows, one asks. The user must still be prompted.
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{PermissionAllow: true, ForceAsk: true, PermissionReason: "double-check"}}
	checker := &fakePreToolPermissionChecker{allow: true}
	tools := WithPreToolUseAndPermission(core.NewTools(inner), hooks, checker)

	_, err := tools.Get("Bash").Execute(context.Background(), map[string]any{"command": "git status"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !checker.called || !checker.forcePrompt {
		t.Fatalf("expected a forced prompt despite allow; called=%v forcePrompt=%v", checker.called, checker.forcePrompt)
	}
}
