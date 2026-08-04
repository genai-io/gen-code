package agent

import (
	"context"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/san/internal/tool"
)

// These tests drive the whole hook → gate → settings path the main agent is
// built with, because the bypass they guard against lived in the seam between
// those three: a PreToolUse hook answering "allow" used to skip the gate
// outright, so the call never reached HasPermissionToUseTool.

type recordingTool struct{ ran bool }

func (r *recordingTool) Name() string            { return "Bash" }
func (r *recordingTool) Description() string     { return "" }
func (r *recordingTool) Schema() core.ToolSchema { return core.ToolSchema{Name: "Bash"} }
func (r *recordingTool) Execute(context.Context, map[string]any) (string, error) {
	r.ran = true
	return "executed", nil
}

// allowingHook answers every PreToolUse with "allow", the way a permissive user
// hook does.
type allowingHook struct{}

func (allowingHook) HasHooks(hook.EventType) bool                { return true }
func (allowingHook) ExecuteAsync(hook.EventType, hook.HookInput) {}
func (allowingHook) StopHookActive() *bool                       { return nil }
func (allowingHook) Execute(context.Context, hook.EventType, hook.HookInput) hook.HookOutcome {
	return hook.HookOutcome{ShouldContinue: true, PermissionAllow: true, HookSource: "PreToolUse"}
}

// gatedTools wires a tool behind the same hook + gate stack buildAgent uses.
func gatedTools(t *testing.T, data *setting.Data, inner core.Tool) (core.Tools, *PermissionGate) {
	t.Helper()
	gate := NewPermissionGate(func(name string, args map[string]any) PermDecisionResult {
		decision := data.HasPermissionToUseTool(name, args, nil)
		return PermDecisionResult{Decision: decision.Behavior, Reason: decision.Reason}
	})
	gate.SetHookAllowResolver(func(name string, args map[string]any) bool {
		return data.ResolveHookAllow(name, args, nil)
	})
	t.Cleanup(gate.Close)
	return tool.WithPreToolUseAndPermission(core.NewTools(inner), allowingHook{}, gate), gate
}

// runBash executes one command through the stack and reports whether the gate
// prompted on the way. Racing the gate's request channel against the call's own
// return is what keeps every regression a clean failure: a bypass returns with
// prompted=false instead of parking the test on a prompt that never arrives,
// and a prompt is answered (deny) instead of parking the call on an answer that
// never comes.
func runBash(t *testing.T, tools core.Tools, gate *PermissionGate, command string) (prompted bool, err error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, execErr := tools.Get("Bash").Execute(context.Background(), map[string]any{"command": command})
		done <- execErr
	}()

	select {
	case err = <-done:
		return false, err
	case req := <-gate.requests:
		req.Response <- PermGateResponse{Allow: false, Reason: "denied by test"}
		return true, <-done
	}
}

func TestHookAllowCannotWaiveDenyRule(t *testing.T) {
	data := &setting.Data{Permissions: setting.PermissionSettings{Deny: []string{"Bash(rm:*)"}}}
	inner := &recordingTool{}
	tools, gate := gatedTools(t, data, inner)

	_, err := runBash(t, tools, gate, "rm -rf important/")
	if err == nil {
		t.Error("expected the deny rule to block the call despite the hook allow")
	}
	if inner.ran {
		t.Error("hook allow executed a command covered by an explicit deny rule")
	}
}

func TestHookAllowCannotWaiveCircuitBreaker(t *testing.T) {
	inner := &recordingTool{}
	tools, gate := gatedTools(t, &setting.Data{}, inner)

	prompted, err := runBash(t, tools, gate, "rm -rf ~")
	if !prompted {
		t.Error("hook allow skipped the gate: the circuit breaker never reached the user")
	}
	if err == nil {
		t.Error("expected the refused prompt to block the call")
	}
	if inner.ran {
		t.Error("hook allow executed a command the circuit breaker holds")
	}
}

// The hook stays useful: on a call no rule and no safety check speaks to, its
// allow still waives the routine "do you want to run this?" prompt. The command
// has to be one the gate would really prompt on — a read-only one is permitted
// by the mode default whether or not the waiver works, so it would pin nothing.
func TestHookAllowWaivesRoutinePrompt(t *testing.T) {
	inner := &recordingTool{}
	tools, gate := gatedTools(t, &setting.Data{}, inner)

	prompted, err := runBash(t, tools, gate, "touch marker")
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if prompted {
		t.Error("hook allow should have waived the routine prompt")
	}
	if !inner.ran {
		t.Error("the waived call should have run")
	}
}
