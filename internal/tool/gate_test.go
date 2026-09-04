package tool

import (
	"encoding/json"

	"github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/hook"
)

// ask puts one call through a gate, the way the loop does.
func ask(t *testing.T, g core.Gate, args map[string]any) agent.Decision {
	t.Helper()
	d, err := g(context.Background(), agent.PreToolContext{
		Call: ai.ToolCall{Name: "Bash", Input: mustJSON(args)},
	})
	if err != nil {
		t.Fatalf("the gate failed rather than answering: %v", err)
	}
	return d
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

func TestAHookRewritesTheArgumentsTheToolRuns(t *testing.T) {
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{
		UpdatedInput: map[string]any{"command": "rtk git status"},
	}}
	d := ask(t, HookedPermission(hooks, nil), map[string]any{"command": "git status"})

	if hooks.input.ToolName != "Bash" || hooks.input.ToolInput["command"] != "git status" {
		t.Fatalf("hook received unexpected input: %#v", hooks.input)
	}
	got, _ := core.ParseToolInput(d.Arguments)
	if got["command"] != "rtk git status" {
		t.Fatalf("the call the tool would run = %#v", got)
	}
}

type fakeHookAwareChecker struct {
	called      bool
	forcePrompt bool
	reason      string
	allow       bool
	// refuseHookAllow makes the checker answer HonorsHookAllow with false, the
	// way the real gate does for a deny rule, the circuit breaker, a
	// confirmation tier or an explicit ask rule.
	refuseHookAllow bool
}

func (c *fakeHookAwareChecker) Check(ctx context.Context, name string, input map[string]any, forcePrompt bool, reason string) (bool, string) {
	c.called = true
	c.forcePrompt = forcePrompt
	c.reason = reason
	if c.allow {
		return true, ""
	}
	return false, "should not be used"
}

func (c *fakeHookAwareChecker) HonorsHookAllow(name string, input map[string]any) bool {
	return !c.refuseHookAllow
}

func TestPreToolUseAllowOverridesPermissionPrompt(t *testing.T) {
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{PermissionAllow: true}}
	checker := &fakeHookAwareChecker{}
	if d := ask(t, HookedPermission(hooks, checker), map[string]any{"command": "git status"}); d.Block {
		t.Fatalf("the gate refused: %s", d.Reason)
	}
	if checker.called {
		t.Fatal("permission checker should not run after PreToolUse allow")
	}
}

// A hook allow waives the routine prompt, not the rules. When the checker says
// the waiver does not hold — a deny rule, the circuit breaker, a confirmation
// tier or an explicit ask rule — the call is gated as if no hook had spoken.
func TestPreToolUseAllowStillGatedWhenNotHonored(t *testing.T) {
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{PermissionAllow: true}}
	checker := &fakeHookAwareChecker{refuseHookAllow: true}
	if d := ask(t, HookedPermission(hooks, checker), map[string]any{"command": "rm -rf important/"}); !d.Block {
		t.Fatal("expected the gate to block the call")
	}
	if !checker.called {
		t.Fatal("permission checker should run when the hook allow is not honored")
	}
}

func TestPreToolUseAskForcesPermissionPrompt(t *testing.T) {
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{ForceAsk: true, PermissionReason: "explain this command"}}
	checker := &fakeHookAwareChecker{allow: true}
	if d := ask(t, HookedPermission(hooks, checker), map[string]any{"command": "git status"}); d.Block {
		t.Fatalf("the gate refused: %s", d.Reason)
	}
	if !checker.called || !checker.forcePrompt || checker.reason != "explain this command" {
		t.Fatalf("permission checker received forcePrompt=%v reason=%q called=%v", checker.forcePrompt, checker.reason, checker.called)
	}
}

func TestPreToolUseContinueFalseBlocksWithSystemMessage(t *testing.T) {
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{ShouldContinue: false, ShouldBlock: true, AdditionalContext: "stop here"}}
	d := ask(t, HookedPermission(hooks, nil), map[string]any{"command": "git status"})
	if !d.Block || d.Reason != "stop here" {
		t.Fatalf("decision = %+v, want a block reading \"stop here\"", d)
	}
}

func TestPreToolUseAskWinsOverAllow(t *testing.T) {
	// Two hooks disagree: one allows, one asks. The user must still be prompted.
	hooks := &fakeHookHandler{outcome: hook.HookOutcome{PermissionAllow: true, ForceAsk: true, PermissionReason: "double-check"}}
	checker := &fakeHookAwareChecker{allow: true}
	if d := ask(t, HookedPermission(hooks, checker), map[string]any{"command": "git status"}); d.Block {
		t.Fatalf("the gate refused: %s", d.Reason)
	}
	if !checker.called || !checker.forcePrompt {
		t.Fatalf("expected a forced prompt despite allow; called=%v forcePrompt=%v", checker.called, checker.forcePrompt)
	}
}

// mustJSON renders a tool's arguments the way a model sends them: raw JSON.
func mustJSON(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
