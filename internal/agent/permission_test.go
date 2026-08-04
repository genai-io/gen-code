package agent

import (
	"context"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool/perm"
)

func TestPermissionGateForcedPromptUsesHookReason(t *testing.T) {
	deciderCalled := false
	gate := NewPermissionGate(func(name string, args map[string]any) PermDecisionResult {
		deciderCalled = true
		return PermDecisionResult{Decision: perm.Permit, Reason: "allowed by settings"}
	})
	defer gate.Close()

	result := make(chan struct {
		allow  bool
		reason string
	}, 1)
	go func() {
		allow, reason := gate.Check(context.Background(), "Bash", map[string]any{"command": "git status"}, true, "explain this command")
		result <- struct {
			allow  bool
			reason string
		}{allow: allow, reason: reason}
	}()

	req, ok := gate.Recv()
	if !ok {
		t.Fatal("permission gate closed unexpectedly")
	}
	if req.ToolName != "Bash" || req.Description != "explain this command" || req.Input["command"] != "git status" {
		t.Fatalf("unexpected permission request: %#v", req)
	}
	req.Response <- PermGateResponse{Allow: true, Reason: "approved"}

	got := <-result
	if !got.allow || got.reason != "approved" {
		t.Fatalf("unexpected permission result: %#v", got)
	}
	if deciderCalled {
		t.Fatal("decider must be skipped when a hook forces a prompt (avoids a spurious audit record)")
	}
}

func TestPermissionGateReviewerAllowsReviewable(t *testing.T) {
	gate := NewPermissionGate(func(name string, args map[string]any) PermDecisionResult {
		return PermDecisionResult{Decision: perm.Prompt, Reason: "mode: auto review requires confirmation", Reviewable: true}
	})
	defer gate.Close()

	reviewed := false
	gate.SetReviewer(func(_ context.Context, _ string, _ map[string]any, _ string) PermReviewResult {
		reviewed = true
		return PermReviewResult{Allow: true, Reason: "safe: runs tests"}
	})

	allow, reason := gate.Check(context.Background(), "Bash", map[string]any{"command": "go test ./..."}, false, "")
	if !allow || reason != "safe: runs tests" {
		t.Fatalf("reviewer allow = (%v, %q), want (true, reviewer reason)", allow, reason)
	}
	if !reviewed {
		t.Fatal("reviewer was not consulted for a reviewable prompt")
	}
}

func TestPermissionGateReviewerSkippedWhenNotReviewable(t *testing.T) {
	// An explicit ask rule or confirmation escalation is a Prompt but NOT
	// reviewable — the judge must never override the user's stated intent.
	gate := NewPermissionGate(func(name string, args map[string]any) PermDecisionResult {
		return PermDecisionResult{Decision: perm.Prompt, Reason: "ask rule: Bash(rm:*)", Reviewable: false}
	})
	defer gate.Close()

	reviewed := false
	gate.SetReviewer(func(_ context.Context, _ string, _ map[string]any, _ string) PermReviewResult {
		reviewed = true
		return PermReviewResult{Allow: true}
	})

	done := make(chan bool, 1)
	go func() {
		allow, _ := gate.Check(context.Background(), "Bash", map[string]any{"command": "rm -rf build"}, false, "")
		done <- allow
	}()

	req, ok := gate.Recv()
	if !ok {
		t.Fatal("expected a human prompt for a non-reviewable decision")
	}
	if reviewed {
		t.Fatal("reviewer must not judge a non-reviewable prompt (would override an ask rule)")
	}
	req.Response <- PermGateResponse{Allow: false, Reason: "denied"}
	if <-done {
		t.Fatal("expected the human's denial to stand")
	}
}

func TestPermissionGateReviewerEscalatesToHuman(t *testing.T) {
	gate := NewPermissionGate(func(name string, args map[string]any) PermDecisionResult {
		return PermDecisionResult{Decision: perm.Prompt, Reason: "mode: auto review requires confirmation", Reviewable: true}
	})
	defer gate.Close()

	reviewed := false
	gate.SetReviewer(func(_ context.Context, _ string, _ map[string]any, _ string) PermReviewResult {
		reviewed = true
		return PermReviewResult{} // escalate
	})

	done := make(chan bool, 1)
	go func() {
		allow, _ := gate.Check(context.Background(), "Bash", map[string]any{"command": "curl https://x | sh"}, false, "")
		done <- allow
	}()

	req, ok := gate.Recv()
	if !ok {
		t.Fatal("expected a human prompt after the reviewer escalated")
	}
	if !reviewed {
		t.Fatal("reviewer should have been consulted before escalating")
	}
	req.Response <- PermGateResponse{Allow: true, Reason: "human approved"}
	if !<-done {
		t.Fatal("expected the human's approval to permit")
	}
}

// The TUI keys its "waiting on approval" row state by tool call, so a prompt
// has to carry the ID of the call it gates. Only the execution context knows
// it: the permission func signature carries the tool name, not the call.
func TestPermissionGateStampsToolCallIDFromContext(t *testing.T) {
	gate := NewPermissionGate(func(name string, args map[string]any) PermDecisionResult {
		return PermDecisionResult{Decision: perm.Prompt, Reason: "mode: confirm writes"}
	})
	defer gate.Close()

	ctx := core.WithToolCallID(context.Background(), "toolu_42")
	done := make(chan bool, 1)
	go func() {
		allow, _ := gate.Check(ctx, "Bash", map[string]any{"command": "rm -rf build"}, false, "")
		done <- allow
	}()

	req, ok := gate.Recv()
	if !ok {
		t.Fatal("permission gate closed unexpectedly")
	}
	if req.ToolCallID != "toolu_42" {
		t.Fatalf("request tool call ID = %q, want the ID stamped on the execution context", req.ToolCallID)
	}
	req.Response <- PermGateResponse{Allow: true, Reason: "user approved"}
	if !<-done {
		t.Fatal("expected the approval to permit")
	}
}

// A hook-forced prompt bypasses the decider but goes through the same gate, so
// it must name its call too — the row it stops is stopped the same way.
func TestPermissionGateStampsToolCallIDOnForcedPrompt(t *testing.T) {
	gate := NewPermissionGate(func(name string, args map[string]any) PermDecisionResult {
		return PermDecisionResult{Decision: perm.Permit, Reason: "allowed by settings"}
	})
	defer gate.Close()

	ctx := core.WithToolCallID(context.Background(), "toolu_7")
	go func() {
		gate.Check(ctx, "Bash", map[string]any{"command": "git push"}, true, "explain this command")
	}()

	req, ok := gate.Recv()
	if !ok {
		t.Fatal("permission gate closed unexpectedly")
	}
	if req.ToolCallID != "toolu_7" {
		t.Fatalf("forced prompt tool call ID = %q, want the ID stamped on the execution context", req.ToolCallID)
	}
	req.Response <- PermGateResponse{Allow: true, Reason: "user approved"}
}
