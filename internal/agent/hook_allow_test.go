package agent

import (
	"encoding/json"
	"errors"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"

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

// allowingHook answers every PreToolUse with "allow", the way a permissive user
// hook does.
type allowingHook struct{}

func (allowingHook) HasHooks(hook.EventType) bool                { return true }
func (allowingHook) ExecuteAsync(hook.EventType, hook.HookInput) {}
func (allowingHook) StopHookActive() *bool                       { return nil }
func (allowingHook) Execute(context.Context, hook.EventType, hook.HookInput) hook.HookOutcome {
	return hook.HookOutcome{ShouldContinue: true, PermissionAllow: true, HookSource: "PreToolUse"}
}

// gatedTools wires the same hook + gate stack buildAgent uses.
func gatedTools(t *testing.T, data *setting.Data) (core.Gate, *PermissionGate) {
	t.Helper()
	gate := NewPermissionGate(func(name string, args map[string]any) PermDecisionResult {
		decision := data.HasPermissionToUseTool(name, args, nil)
		return PermDecisionResult{Decision: decision.Behavior, Reason: decision.Reason}
	})
	gate.SetHookAllowResolver(func(name string, args map[string]any) bool {
		return data.ResolveHookAllow(name, args, nil)
	})
	t.Cleanup(gate.Close)
	return tool.HookedPermission(allowingHook{}, gate), gate
}

// runBash executes one command through the stack and reports whether the gate
// prompted on the way. Racing the gate's request channel against the call's own
// return is what keeps every regression a clean failure: a bypass returns with
// prompted=false instead of parking the test on a prompt that never arrives,
// and a prompt is answered (deny) instead of parking the call on an answer that
// never comes.
func runBash(t *testing.T, ask core.Gate, gate *PermissionGate, command string) (prompted bool, err error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		d, gateErr := ask(context.Background(), sdkagent.PreToolContext{
			Call: ai.ToolCall{Name: "Bash", Input: mustJSON(map[string]any{"command": command})},
		})
		if gateErr == nil && d.Block {
			gateErr = errors.New(d.Reason)
		}
		done <- gateErr
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
	tools, gate := gatedTools(t, data)

	_, err := runBash(t, tools, gate, "rm -rf important/")
	if err == nil {
		t.Error("expected the deny rule to block the call despite the hook allow")
	}
}

func TestHookAllowCannotWaiveCircuitBreaker(t *testing.T) {
	tools, gate := gatedTools(t, &setting.Data{})

	prompted, err := runBash(t, tools, gate, "rm -rf ~")
	if !prompted {
		t.Error("hook allow skipped the gate: the circuit breaker never reached the user")
	}
	if err == nil {
		t.Error("expected the refused prompt to block the call")
	}
}

// The hook stays useful: on a call no rule and no safety check speaks to, its
// allow still waives the routine "do you want to run this?" prompt. The command
// has to be one the gate would really prompt on — a read-only one is permitted
// by the mode default whether or not the waiver works, so it would pin nothing.
func TestHookAllowWaivesRoutinePrompt(t *testing.T) {
	tools, gate := gatedTools(t, &setting.Data{})

	prompted, err := runBash(t, tools, gate, "touch marker")
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if prompted {
		t.Error("hook allow should have waived the routine prompt")
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
