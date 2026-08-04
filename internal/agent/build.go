package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/core/system"
	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/tool"
)

// BuildParams contains all values needed to construct a core.Agent.
// The app layer assembles this from env, services, and workspace state.
type BuildParams struct {
	Provider       llm.Provider
	ModelID        string
	MaxTokens      int
	ThinkingEffort string

	CWD     string
	CWDFunc func() string // dynamic CWD for tool execution; falls back to CWD if nil

	// Stream timeout tuning. Zero values (default) use the core defaults:
	// FirstChunkTimeout = 5m, IdleTimeout = 3m.
	StreamFirstChunkTimeout time.Duration
	StreamIdleTimeout       time.Duration

	// AgentDirectory, when non-nil, supplies the available-agents listing
	// embedded into the Agent tool's description. Returning an empty string
	// hides the listing entirely (used by subagent contexts to discourage
	// recursive spawning).
	AgentDirectory func() string

	// Persona overrides the system-prompt parts (identity / behavior / rules)
	// from the active persona. Empty fields keep San's built-in defaults.
	Persona system.Persona

	DisabledTools map[string]bool
	MCPTools      []core.Tool

	// ExtraTools are caller-built conditional tool schemas appended to the
	// toolset (e.g. the self-learning Evolve trigger, injected only when
	// self-learning is active). Nil ⇒ no extra tools.
	ExtraTools []core.ToolSchema

	// PermissionRules and PermissionReview are the two stages of the
	// pre-execution permission gate: the rules stage applies the static rules
	// (permit/reject/prompt); the review stage is the LLM auto-review consulted
	// only on a gray-zone prompt (AutoPilot.Permission). HookAllowResolver
	// guards the way past both: it vets a PreToolUse hook's "allow" against the
	// settings, so a hook cannot waive a deny rule, the circuit breaker, a
	// confirmation tier or an explicit ask rule. Nil fails closed.
	PermissionRules   PermDecisionFunc
	PermissionReview  PermReviewFunc
	HookAllowResolver PermHookAllowFunc

	HookEngine   hook.Handler
	AskUser      tool.AskUserFunc
	ToolActivity func(toolCallID string, msg string)
	// BashPromptResponder answers prompts a command raises *while it runs*
	// (AutoPilot.BashPrompt plus the masked secret input) — a separate concern
	// from the pre-execution gate above.
	BashPromptResponder tool.BashPromptResponderProvider

	// OnEvent observes every agent lifecycle event synchronously, alongside
	// outbox delivery. Used by the trace recorder; nil leaves recording off.
	OnEvent func(core.Event)
}

func buildAgent(p BuildParams) (core.Agent, *PermissionGate, error) {
	if p.Provider == nil {
		return nil, nil, fmt.Errorf("no LLM provider configured")
	}

	client := llm.NewClient(p.Provider, p.ModelID, p.MaxTokens)
	client.SetThinkingEffort(p.ThinkingEffort)

	sys := system.Build(core.ScopeMain,
		system.WithPersona(p.Persona),
		system.WithEnvironment(system.Environment{Cwd: p.CWD}),
	)

	cwdFunc := p.CWDFunc
	if cwdFunc == nil {
		cwd := p.CWD
		cwdFunc = func() string { return cwd }
	}

	schemas := (&tool.Set{
		Disabled:       p.DisabledTools,
		AgentDirectory: p.AgentDirectory,
		ExtraTools:     p.ExtraTools,
	}).Tools()
	var adaptOpts []tool.AdaptOption
	if p.AskUser != nil {
		adaptOpts = append(adaptOpts, tool.WithAskUser(p.AskUser))
	}
	if p.ToolActivity != nil {
		adaptOpts = append(adaptOpts, tool.WithToolActivity(p.ToolActivity))
	}
	if p.BashPromptResponder != nil {
		adaptOpts = append(adaptOpts, tool.WithBashPromptResponderProvider(p.BashPromptResponder))
	}
	pg := NewPermissionGate(p.PermissionRules)
	pg.SetReviewer(p.PermissionReview)
	pg.SetHookAllowResolver(p.HookAllowResolver)
	var ag core.Agent
	adaptOpts = append(adaptOpts, tool.WithMessagesGetterProvider(func() []core.Message {
		if ag == nil {
			return nil
		}
		return ag.Messages()
	}))
	tools := tool.AdaptToolRegistry(schemas, cwdFunc, adaptOpts...)
	for _, t := range p.MCPTools {
		// Built-in tools are filtered by tool.Set{Disabled}, but MCP tools are
		// registered here directly — honor the /tool panel's disable for them too.
		if p.DisabledTools[t.Name()] {
			continue
		}
		tools.Add(t, "mcp:"+t.Name())
	}

	compactClient := client
	compactFunc := func(ctx context.Context, msgs []core.Message) (string, error) {
		text := core.BuildCompactionText(msgs)
		resp, err := compactClient.Complete(ctx, system.CompactPrompt(), []core.Message{core.UserMessage(text, nil)}, core.CompactMaxTokens)
		if err != nil {
			return "", err
		}
		summary := strings.TrimSpace(resp.Content)
		if summary == "" {
			return "", fmt.Errorf("compaction produced empty summary")
		}
		return summary, nil
	}

	ag = core.NewAgent(core.Config{
		ID:          "main",
		LLM:         client,
		System:      sys,
		Tools:       tool.WithPreToolUseAndPermission(tools, p.HookEngine, pg),
		CompactFunc: compactFunc,
		CWD:         p.CWD,
		OnEvent:     p.OnEvent,

		StreamFirstChunkTimeout: p.StreamFirstChunkTimeout,
		StreamIdleTimeout:       p.StreamIdleTimeout,
	})

	return ag, pg, nil
}
