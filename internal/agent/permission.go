package agent

import (
	"context"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool/perm"
)

// PermDecisionResult holds a permission decision and its reason.
//
// RequestID is set by the decider when Decision == perm.Prompt so the
// matching permission.required and permission.decided audit records can be
// joined. It flows through PermGateRequest to the resolver (TUI), which
// passes it back unchanged when the user/hook decision lands.
type PermDecisionResult struct {
	Decision    perm.Decision
	Reason      string
	ToolName    string
	Description string
	RequestID   string
	// Reviewable marks a Prompt the auto-review agent may judge instead of the
	// user. Set only for the auto-review gray-zone default.
	Reviewable bool
}

// PermDecisionFunc evaluates whether a tool call is allowed, denied, or needs prompting.
type PermDecisionFunc func(name string, args map[string]any) PermDecisionResult

// PermReviewResult is the outcome of a gray-zone review. Allow=true auto-approves
// the call; the zero value (Allow=false) escalates it to the human.
type PermReviewResult struct {
	Allow  bool
	Reason string
}

// PermReviewFunc judges a reviewable gray-zone tool call. It runs on the agent
// goroutine and must fail closed (return the zero value) on any error, so a
// broken or slow judge can never silently approve.
type PermReviewFunc func(ctx context.Context, name string, input map[string]any, reason string) PermReviewResult

// PermHookAllowFunc reports whether a PreToolUse hook's "allow" may stand for
// this call. It backs the safety invariant deny rules > safety checks > ask
// rules > hook allow: the hook can waive the routine prompt, but a deny rule,
// the circuit breaker, either confirmation tier or an explicit ask rule
// outranks it, and the call goes through the gate as if the hook had said
// nothing. A hook cannot vouch for a call the user or the breaker has already
// ruled on.
type PermHookAllowFunc func(name string, args map[string]any) bool

// PermGateRequest is a pending permission request sent to the TUI for approval.
//
// RequestID carries the correlation token the decider stamped so the TUI
// can reference the prior permission.required record when emitting
// permission.decided.
//
// ToolCallID names the call this request gates. The TUI needs it because a
// call is already stamped as started by its PreToolEvent, which fires before
// the request goes out: without the ID no view can tell the call waiting on
// the user apart from a batch sibling that really is executing.
type PermGateRequest struct {
	RequestID   string
	ToolCallID  string
	ToolName    string
	Description string
	Input       map[string]any
	Response    chan PermGateResponse
}

// PermGateResponse is the user's decision on a permission request.
type PermGateResponse struct {
	Allow  bool
	Reason string
}

// PermissionGate gates tool execution by routing permission decisions
// through a channel pair. The agent side blocks on the response; the TUI
// side receives requests and sends back decisions.
type PermissionGate struct {
	requests    chan *PermGateRequest
	decideFn    PermDecisionFunc
	reviewFn    PermReviewFunc    // optional; judges reviewable gray-zone prompts
	hookAllowFn PermHookAllowFunc // optional; vets a PreToolUse hook's "allow"
}

func NewPermissionGate(decideFn PermDecisionFunc) *PermissionGate {
	return &PermissionGate{
		requests: make(chan *PermGateRequest, 1),
		decideFn: decideFn,
	}
}

// SetReviewer installs the gray-zone judge. When set, a reviewable Prompt is
// offered to it before falling back to the user. A nil fn disables review.
func (pg *PermissionGate) SetReviewer(fn PermReviewFunc) {
	pg.reviewFn = fn
}

// SetHookAllowResolver installs the resolver that vets a PreToolUse hook's
// "allow" against the settings. A nil fn fails closed: a gate with no resolver
// has no rules to check the hook against, so it cannot certify the waiver and
// every call goes through the gate.
func (pg *PermissionGate) SetHookAllowResolver(fn PermHookAllowFunc) {
	pg.hookAllowFn = fn
}

// HonorsHookAllow reports whether a PreToolUse hook's "allow" is enough to skip
// the gate for this call, on the precedence PermHookAllowFunc documents. A gate
// with no resolver answers no.
func (pg *PermissionGate) HonorsHookAllow(name string, input map[string]any) bool {
	if pg.hookAllowFn == nil {
		return false
	}
	return pg.hookAllowFn(name, input)
}

func (pg *PermissionGate) PermissionFunc() perm.PermissionFunc {
	return func(ctx context.Context, name string, input map[string]any) (bool, string) {
		return pg.Check(ctx, name, input, false, "")
	}
}

func (pg *PermissionGate) Check(ctx context.Context, name string, input map[string]any, forcePrompt bool, reason string) (bool, string) {
	if forcePrompt {
		// A forced hook prompt is an explicit policy, so it takes precedence over
		// bypass mode just as it takes precedence over any other permission mode.
		// It is intentionally not a confirmation generated by the permission gate.
		return pg.prompt(ctx, &PermGateRequest{ToolName: name, Description: reason, Input: input})
	}

	decision := pg.decideFn(name, input)

	switch decision.Decision {
	case perm.Permit:
		return true, decision.Reason
	case perm.Reject:
		return false, decision.Reason
	}

	if decision.ToolName == "" {
		decision.ToolName = name
	}
	if decision.Description == "" {
		decision.Description = decision.Reason
	}

	// Gray-zone review: offer a reviewable Prompt to the judge before the user.
	// Allow short-circuits; anything else falls through to the human prompt.
	if decision.Reviewable && pg.reviewFn != nil {
		if rv := pg.reviewFn(ctx, name, input, decision.Reason); rv.Allow {
			return true, rv.Reason
		}
	}

	return pg.prompt(ctx, &PermGateRequest{
		RequestID:   decision.RequestID,
		ToolName:    decision.ToolName,
		Description: decision.Description,
		Input:       input,
	})
}

// prompt sends a permission request to the resolver (TUI) and blocks until
// it responds or ctx is cancelled.
//
// The tool call ID is taken from the context the agent stamps before Execute
// (core.WithToolCallID), the only place that knows which call this check runs
// for — the permission func signature carries the tool name, not the call.
func (pg *PermissionGate) prompt(ctx context.Context, req *PermGateRequest) (bool, string) {
	req.ToolCallID = core.ToolCallIDFromContext(ctx)
	req.Response = make(chan PermGateResponse, 1)

	select {
	case pg.requests <- req:
	case <-ctx.Done():
		return false, "cancelled"
	}

	select {
	case <-ctx.Done():
		return false, "cancelled"
	case resp := <-req.Response:
		return resp.Allow, resp.Reason
	}
}

func (pg *PermissionGate) Recv() (*PermGateRequest, bool) {
	req, ok := <-pg.requests
	return req, ok
}

func (pg *PermissionGate) Close() {
	close(pg.requests)
}
