package tool

import (
	"context"
	"fmt"
	"sync"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool/toolresult"
	"github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// sideEffects stores HookResponse values keyed by tool call ID.
// The TUI retrieves these when handling PostTool events to apply
// environment side effects (cwd changes, file cache, background tasks).
var sideEffects sync.Map
var resultDetails sync.Map

// PopSideEffect retrieves and removes the HookResponse for a tool call.
// Returns nil if no side effect was stored.
func PopSideEffect(toolCallID string) any {
	val, ok := sideEffects.LoadAndDelete(toolCallID)
	if !ok {
		return nil
	}
	return val
}

// PopResultDetails retrieves structured result data for a tool call.
func PopResultDetails(toolCallID string) any {
	val, ok := resultDetails.LoadAndDelete(toolCallID)
	if !ok {
		return nil
	}
	return val
}

// AskUserFunc poses a structured question to the user and returns their answer.
// The AskUserQuestion tool calls it mid-execution; the TUI layer supplies the
// implementation via AgentToUI.Ask().
type AskUserFunc func(ctx context.Context, req *QuestionRequest) (*QuestionResponse, error)

// AdaptOption configures tool adaptation behavior.
type AdaptOption func(*adaptConfig)

type adaptConfig struct {
	askFn           AskUserFunc
	messagesGetter  MessagesGetter
	activityFn      func(toolCallID string, msg string)
	promptResponder BashPromptResponderProvider
}

// WithAskUser sets the handler the AskUserQuestion tool uses to ask the user.
func WithAskUser(fn AskUserFunc) AdaptOption {
	return func(c *adaptConfig) { c.askFn = fn }
}

// WithMessagesGetterProvider provides the current parent conversation to tools that
// need it, such as Agent when fork=true.
func WithMessagesGetterProvider(fn MessagesGetter) AdaptOption {
	return func(c *adaptConfig) { c.messagesGetter = fn }
}

// WithToolActivity sets the handler for activity lines emitted by agent-like tools.
func WithToolActivity(fn func(toolCallID string, msg string)) AdaptOption {
	return func(c *adaptConfig) { c.activityFn = fn }
}

// WithBashPromptResponderProvider sets the responder provider for tools that can
// safely handle interactive prompts during execution.
func WithBashPromptResponderProvider(fn BashPromptResponderProvider) AdaptOption {
	return func(c *adaptConfig) { c.promptResponder = fn }
}

// AdaptTool wraps a legacy Tool as a core.Tool with a dynamic CWD resolver.
func AdaptTool(t Tool, schema core.ToolSchema, cwd func() string) core.Tool {
	return &toolAdapter{inner: t, schema: schema, cwd: cwd}
}

// AdaptToolRegistry wraps all tools from the global registry as core.Tools.
// The schema list maps tool names to their JSON schemas.
func AdaptToolRegistry(schemas []core.ToolSchema, cwd func() string, opts ...AdaptOption) core.Tools {
	var cfg adaptConfig
	for _, o := range opts {
		o(&cfg)
	}

	schemaByName := make(map[string]core.ToolSchema, len(schemas))
	for _, s := range schemas {
		if s.Name != "" {
			schemaByName[s.Name] = s
		}
	}

	var adapted []core.Tool
	for name, schema := range schemaByName {
		if t, ok := Get(name); ok {
			adapted = append(adapted, &toolAdapter{inner: t, schema: schema, cwd: cwd, askFn: cfg.askFn, messagesGetter: cfg.messagesGetter, activityFn: cfg.activityFn, promptResponder: cfg.promptResponder})
		}
	}
	return core.NewTools(adapted...)
}

// toolAdapter wraps a legacy Tool as a core.Tool.
type toolAdapter struct {
	inner           Tool
	schema          core.ToolSchema
	cwd             func() string
	askFn           AskUserFunc
	messagesGetter  MessagesGetter
	activityFn      func(toolCallID string, msg string)
	promptResponder BashPromptResponderProvider
}

func (a *toolAdapter) Schema() core.ToolSchema { return a.schema }

func (a *toolAdapter) Run(ctx context.Context, call ai.ToolCall) (agent.Result, error) {
	input, _ := core.ParseToolInput(call.Input)
	cwd := ""
	if a.cwd != nil {
		cwd = a.cwd()
	}
	if a.messagesGetter != nil {
		ctx = WithMessagesGetter(ctx, a.messagesGetter)
	}
	if a.promptResponder != nil {
		ctx = ContextWithBashPromptResponderProvider(ctx, a.promptResponder)
	}
	// A running command (Bash) reports its growing output through the same
	// activity callback agent tools use; the receiver shows it as the row's
	// live counter rather than an activity-feed line.
	if a.activityFn != nil {
		if callID := core.ToolCallIDFromContext(ctx); callID != "" {
			ctx = ContextWithBashProgress(ctx, func(lines int, bytes int64) {
				if text := formatBashProgress(lines, bytes); text != "" {
					a.activityFn(callID, text)
				}
			})
		}
	}
	if IsAgentToolName(a.inner.Name()) {
		if a.activityFn != nil {
			if callID := core.ToolCallIDFromContext(ctx); callID != "" {
				input["_onActivity"] = ActivityFunc(func(msg string) {
					a.activityFn(callID, msg)
				})
			}
		}
		// Forward the interaction channel so foreground subagents can route
		// AskUserQuestion prompts to the user through their spawning turn.
		if a.askFn != nil {
			input["_onQuestion"] = AskQuestionFunc(a.askFn)
		}
	}

	var result toolresult.ToolResult
	if it, ok := a.inner.(InteractiveTool); ok && it.RequiresInteraction() && a.askFn != nil {
		request, err := it.PrepareInteraction(ctx, input, cwd)
		if err != nil {
			return agent.Result{}, err
		}
		qr, ok := request.(*QuestionRequest)
		if !ok {
			return agent.Result{}, fmt.Errorf("unexpected interaction type")
		}
		resp, err := a.askFn(ctx, qr)
		if err != nil {
			return agent.Result{}, err
		}
		result = it.ExecuteWithResponse(ctx, input, resp, cwd)
	} else {
		result = a.inner.Execute(ctx, input, cwd)
	}

	if callID := core.ToolCallIDFromContext(ctx); callID != "" {
		if result.HookResponse != nil {
			sideEffects.Store(callID, result.HookResponse)
		}
		if result.Details != nil {
			resultDetails.Store(callID, result.Details)
		}
	}

	text := result.FormatForLLM()
	if !result.Success {
		return agent.TextResult(text), fmt.Errorf("%s", text)
	}
	return agent.TextResult(text), nil
}
