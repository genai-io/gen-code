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
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/san/internal/tool"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
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

	// ResultFilter may replace a tool result before the model is told about it — an
	// output too large for the window, kept elsewhere and referenced. Nil
	// sends every result through as it came.
	ResultFilter core.ResultFilter

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

// System returns the system prompt these params build. Exported so a caller
// can inspect the prompt — its size, its sections — before any session exists;
// buildAgent renders from this same function, so the two cannot drift.
func (p BuildParams) System() core.System {
	return system.Build(core.ScopeMain,
		system.WithPersona(p.Persona),
		system.WithEnvironment(system.Environment{Cwd: p.CWD}),
	)
}

// Schemas returns the built-in tool definitions these params produce: the
// standard set after the disabled-tools filter, plus any conditional extras.
// MCP tools are not included — they arrive as ready-made core.Tools rather
// than schemas, and are wired in separately.
func (p BuildParams) Schemas() []core.ToolSchema {
	return (&tool.Set{
		Disabled:       p.DisabledTools,
		AgentDirectory: p.AgentDirectory,
		ExtraTools:     p.ExtraTools,
	}).Tools()
}

func buildAgent(p BuildParams) (core.Agent, *PermissionGate, error) {
	if p.Provider == nil {
		return nil, nil, fmt.Errorf("no LLM provider configured")
	}

	client := llm.NewClient(p.Provider, p.ModelID, p.MaxTokens)
	client.SetThinkingEffort(p.ThinkingEffort)

	sys := p.System()

	cwdFunc := p.CWDFunc
	if cwdFunc == nil {
		cwd := p.CWD
		cwdFunc = func() string { return cwd }
	}

	schemas := p.Schemas()
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
		if p.DisabledTools[t.Schema().Name] {
			continue
		}
		tools.Add(t, "mcp:"+t.Schema().Name)
	}

	compactClient := client
	compactFunc := func(ctx context.Context, msgs []core.Message) (string, error) {
		text := core.BuildCompactionText(msgs)
		resp, err := compactClient.Complete(ctx, system.CompactPrompt(), []core.Message{core.UserMessage(text, nil)}, core.CompactMaxTokens)
		if err != nil {
			return "", err
		}
		summary := strings.TrimSpace(resp.Content.Text())
		if summary == "" {
			return "", fmt.Errorf("compaction produced empty summary")
		}
		return summary, nil
	}

	ag = core.NewAgent(core.Config{
		ID:           "main",
		Client:       client.TurnClient,
		CallOptions:  client.CallOptions,
		InputLimit:   client.InputLimit,
		System:       sys,
		Tools:        tools,
		Gate:         tool.HookedPermission(p.HookEngine, pg),
		ResultFilter: p.ResultFilter,
		CompactFunc:  compactFunc,
		OnEvent:      p.OnEvent,

		StreamFirstChunkTimeout: p.StreamFirstChunkTimeout,
		StreamIdleTimeout:       p.StreamIdleTimeout,
	})

	return ag, pg, nil
}

// RunOnce answers one message with no session around it: build the agent, hand
// it the message, run one exchange, and report what the model said.
//
// It is the direct path — Append then ThinkAct, the same one a subagent takes —
// because print mode has no inbox to wait on and nothing to interrupt. onText
// receives the answer as it streams, so a caller can print it live.
//
// Permission is the caller's: a run with nobody at the keyboard cannot prompt,
// so BuildParams.PermissionRules decides alone.
func RunOnce(ctx context.Context, p BuildParams, message core.Message, onText func(string)) (*core.Result, error) {
	// The project's own instructions, the way the interactive path reads them.
	// A run without them answers differently in the same repo, which is the
	// one thing a headless run must not do.
	message = withInstructions(p.CWD, message)

	if onText != nil {
		caller := p.OnEvent
		p.OnEvent = func(ev core.Event) {
			if u, ok := ev.(sdkagent.MessageUpdate); ok {
				if text := u.Text(); text != "" {
					onText(text)
				}
			}
			if caller != nil {
				caller(ev)
			}
		}
	}
	ag, gate, err := buildAgent(p)
	if err != nil {
		return nil, err
	}
	if gate != nil {
		defer gate.Close()
	}
	ag.Append(ctx, message)
	return ag.ThinkAct(ctx)
}

// withInstructions attaches the AGENTS.md chain to the message that opens a
// headless run, as a reminder rather than a prompt section — the same shape
// and the same scopes the interactive path uses, so what the model is told
// does not depend on which one asked.
func withInstructions(cwd string, msg core.Message) core.Message {
	var user, project []string
	for _, f := range system.LoadMemoryFiles(cwd) {
		if f.Level == "global" {
			user = append(user, f.Content)
		} else {
			project = append(project, f.Content)
		}
	}
	reminders := []string{
		reminder.WrapMemory("user", strings.Join(user, "\n\n")),
		reminder.WrapMemory("project", strings.Join(project, "\n\n")),
	}
	text := reminder.AttachToContent(msg.Text(), reminders)
	if text == msg.Text() {
		return msg
	}
	return core.UserMessage(text, nil)
}
