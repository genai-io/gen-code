package tool

import (
	"context"
	"encoding/json"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/tool/perm"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
)

// The gate: the only thing between the model's call and the tool.

// Permission refuses a call the checker will not allow.
//
// It is a gate rather than a wrapper around every tool: a decorator has to be
// applied on every path that hands a tool out, and a set whose members differ
// depending on how you asked for them is a set somebody will read the wrong
// way. The loop asks this once per call, and a refusal reaches the model as a
// tool error it may work around.
//
// Nil check gates nothing. The checker itself decides whether safe tools are
// allowed, so deny and ask rules still apply consistently.
func Permission(check perm.PermissionFunc) core.Gate {
	if check == nil {
		return nil
	}
	return func(ctx context.Context, c sdkagent.PreToolContext) (sdkagent.Decision, error) {
		input, _ := core.ParseToolInput(c.Call.Input)
		if allow, reason := check(ctx, c.Call.Name, input); !allow {
			return sdkagent.Decision{Block: true, Reason: reason}, nil
		}
		return sdkagent.Decision{}, nil
	}
}

// HookAwareChecker is the permission check as the hook path needs it: one that
// can be told a hook already asked, and asked whether a hook's "allow" is
// enough on its own.
type HookAwareChecker interface {
	Check(ctx context.Context, name string, input map[string]any, forcePrompt bool, reason string) (bool, string)
	// HonorsHookAllow reports whether a PreToolUse hook's "allow" is enough to
	// skip Check for this call. False sends the call through the gate anyway.
	HonorsHookAllow(name string, input map[string]any) bool
}

// HookedPermission is Permission with the PreToolUse hooks in front of it.
//
// One gate rather than two chained, because the second half reads the first's
// verdict: a hook's "allow" waives the routine prompt and nothing more, and
// whether the waiver holds is the checker's answer, not the hook's.
//
// A hook may also rewrite the arguments, which comes back as
// Decision.Arguments — the call the tool runs is the one that survived the
// gate, not the one the model sent.
func HookedPermission(hooks hook.Handler, check HookAwareChecker) core.Gate {
	if hooks == nil && check == nil {
		return nil
	}
	return func(ctx context.Context, c sdkagent.PreToolContext) (sdkagent.Decision, error) {
		input, _ := core.ParseToolInput(c.Call.Input)
		edited := false
		allowByHook, forceAsk, permissionReason := false, false, ""

		if hooks != nil && hooks.HasHooks(hook.PreToolUse) {
			out := hooks.Execute(ctx, hook.PreToolUse, hook.HookInput{
				ToolName:  c.Call.Name,
				ToolInput: input,
			})
			if !out.ShouldContinue || out.ShouldBlock {
				return sdkagent.Decision{Block: true, Reason: blockReason(out)}, nil
			}
			if out.UpdatedInput != nil {
				input, edited = out.UpdatedInput, true
			}
			allowByHook, forceAsk, permissionReason = out.PermissionAllow, out.ForceAsk, out.PermissionReason
		}

		if check != nil {
			// A hook "allow" waives the routine prompt and nothing more. A hook
			// "ask" outranks it, because prompting is the safe resolution when
			// two hooks disagree; so does a deny rule, the circuit breaker,
			// either confirmation tier or an explicit ask rule, which is what
			// HonorsHookAllow reports on.
			waived := allowByHook && !forceAsk && check.HonorsHookAllow(c.Call.Name, input)
			if !waived {
				if allow, reason := check.Check(ctx, c.Call.Name, input, forceAsk, permissionReason); !allow {
					return sdkagent.Decision{Block: true, Reason: reason}, nil
				}
			}
		}

		if !edited {
			return sdkagent.Decision{}, nil
		}
		rewritten, err := json.Marshal(input)
		if err != nil {
			// The hook's edit cannot be expressed, and running the original
			// would run what the hook rejected. Refuse instead.
			return sdkagent.Decision{Block: true, Reason: "the hook's arguments could not be encoded"}, nil
		}
		return sdkagent.Decision{Arguments: string(rewritten)}, nil
	}
}

func blockReason(out hook.HookOutcome) string {
	for _, s := range []string{out.BlockReason, out.AdditionalContext} {
		if s != "" {
			return s
		}
	}
	return "blocked by PreToolUse hook"
}
