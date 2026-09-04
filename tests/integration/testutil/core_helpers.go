package testutil

import (
	"iter"

	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
)

// PermitAllPermission allows every tool call. Replaces the former
// perm.AsPermissionFunc(perm.PermitAll()) test fixture.
func PermitAllPermission() perm.PermissionFunc {
	return func(context.Context, string, map[string]any) (bool, string) { return true, "" }
}

// ReadOnlyPermission allows safe (read-only) tools and denies the rest.
func ReadOnlyPermission() perm.PermissionFunc {
	return func(_ context.Context, name string, _ map[string]any) (bool, string) {
		if perm.IsSafeTool(name) {
			return true, ""
		}
		return false, "read-only mode: " + name + " not permitted"
	}
}

// DenyAllPermission denies every tool call (including safe tools).
func DenyAllPermission() perm.PermissionFunc {
	return func(_ context.Context, name string, _ map[string]any) (bool, string) {
		return false, name + " denied"
	}
}

// FakeLLM answers with queued responses. It fakes the protocol rather than an
// abstraction over it: a driver is what pkg/ai asks for, and what five real
// implementations sit behind.
type FakeLLM struct {
	Responses []llm.CompletionResponse
	callIdx   int
}

func (f *FakeLLM) Name() string { return "fake" }

func (f *FakeLLM) Stream(_ context.Context, _ *ai.Request) iter.Seq2[ai.Delta, error] {
	resp := llm.CompletionResponse{Content: ai.TextContent("no more responses"), StopReason: ai.StopEndTurn}
	if f.callIdx < len(f.Responses) {
		resp = f.Responses[f.callIdx]
		f.callIdx++
	}
	return func(yield func(ai.Delta, error) bool) {
		for _, delta := range FakeDeltas(resp) {
			if !yield(delta, nil) {
				return
			}
		}
	}
}

// FakeDeltas renders one answer as the stream a driver would produce. The
// answer is already an ordered block sequence, so this walks it rather than
// reassembling one from parallel fields.
func FakeDeltas(r llm.CompletionResponse) []ai.Delta {
	var out []ai.Delta
	calls := 0
	for _, b := range r.Content {
		switch b.Type {
		case ai.BlockToolCall:
			calls++
			out = append(out, ai.Delta{Block: b})
		default:
			out = append(out, ai.Delta{Block: b}, ai.Delta{EndBlock: true})
		}
	}
	stop := ai.StopEndTurn
	if calls > 0 {
		stop = ai.StopToolUse
	}
	return append(out, ai.Delta{StopReason: stop, Usage: &ai.Usage{
		Input: r.Usage.Input, Output: r.Usage.Output,
	}})
}

// stubClient wraps a fake driver as the per-turn client an agent is built on.
// The same client answers every turn: only a real endpoint varies its headers
// with what the turn sends.
func stubClient(d ai.Driver) func([]core.Message) (*ai.Client, error) {
	client := ai.NewClientWithDriver(d, ai.Model{ID: "stub", API: "stub"})
	return func([]core.Message) (*ai.Client, error) { return client, nil }
}

// NewTestAgent creates a core.Agent backed by a FakeLLM with queued responses.
// All globally registered tools (including dynamically registered fakes) are included.
func NewTestAgent(t *testing.T, responses ...llm.CompletionResponse) (core.Agent, *FakeLLM) {
	t.Helper()
	fakeLLM := &FakeLLM{Responses: responses}
	cwd := t.TempDir()
	return core.NewAgent(core.Config{
		ID:     "test-agent",
		Client: stubClient(fakeLLM),
		System: core.NewSystem(),
		Tools:  buildAllRegisteredTools(cwd),

		MaxSteps: 100,
	}), fakeLLM
}

// buildAllRegisteredTools creates a core.Tools wrapping ALL tools in the global registry,
// including dynamically registered fake tools. Unlike AdaptToolRegistry which only finds
// tools that have schemas in GetToolSchemas(), this walks the entire registry directly.
func buildAllRegisteredTools(cwd string) core.Tools {
	var adapted []core.Tool
	for _, name := range tool.Default().List() {
		t, ok := tool.Get(name)
		if !ok {
			continue
		}
		// The tool's own name, not the registry's lookup key: List() lowercases
		// for matching, and the schema is what the model is told to call.
		schema := core.ToolSchema{Name: t.Name(), Description: t.Description()}
		adapted = append(adapted, tool.AdaptTool(t, schema, func() string { return cwd }))
	}
	return core.NewTools(adapted...)
}

// NewTestAgentWithPermission creates a core.Agent with a permission function wrapping tools.
func NewTestAgentWithPermission(t *testing.T, permFn perm.PermissionFunc, responses ...llm.CompletionResponse) (core.Agent, *FakeLLM) {
	t.Helper()
	fakeLLM := &FakeLLM{Responses: responses}
	cwd := t.TempDir()
	return core.NewAgent(core.Config{
		ID:       "test-agent",
		Client:   stubClient(fakeLLM),
		System:   core.NewSystem(),
		Tools:    buildAllRegisteredTools(cwd),
		Gate:     tool.Permission(permFn),
		MaxSteps: 100,
	}), fakeLLM
}

// NewTestAgentWithMaxSteps creates a core.Agent with a specific max steps limit.
func NewTestAgentWithMaxSteps(t *testing.T, maxSteps int, responses ...llm.CompletionResponse) (core.Agent, *FakeLLM) {
	t.Helper()
	fakeLLM := &FakeLLM{Responses: responses}
	cwd := t.TempDir()
	return core.NewAgent(core.Config{
		ID:     "test-agent",
		Client: stubClient(fakeLLM),
		System: core.NewSystem(),
		Tools:  buildAllRegisteredTools(cwd),

		MaxSteps: maxSteps,
	}), fakeLLM
}

// BuildTestTools adapts all globally registered tools into a core.Tools for use in tests.
func BuildTestTools(t *testing.T) core.Tools {
	t.Helper()
	return buildAllRegisteredTools(t.TempDir())
}

// RunAgent sends a prompt to the agent, drains its outbox, and returns the result.
// It sends SigStop after the first OnTurn event (single cycle).
func RunAgent(ctx context.Context, ag core.Agent, prompt string) (core.Result, error) {
	if err := ctx.Err(); err != nil {
		return core.Result{}, err
	}

	var agentErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		agentErr = ag.Run(ctx)
	}()

	select {
	case ag.Inbox() <- core.Inbound{Msg: core.UserMessage(prompt, nil)}:
	case <-ctx.Done():
		<-done
		return core.Result{}, ctx.Err()
	}

	var result core.Result
	var hasResult bool
	for ev := range ag.Outbox() {
		if turn, ok := ev.(core.TurnEnded); ok {
			result = turn.Result
			hasResult = true
			select {
			case ag.Inbox() <- core.Inbound{Signal: core.SigStop}:
			case <-ctx.Done():
			}
		}
	}

	<-done

	if agentErr != nil && !hasResult {
		return result, agentErr
	}
	return result, nil
}
