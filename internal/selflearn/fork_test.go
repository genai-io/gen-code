package selflearn

import (
	"iter"

	"github.com/genai-io/sdk-go/pkg/ai"

	"context"
	"strings"
	"sync"
	"testing"

	"github.com/genai-io/san/internal/core"
)

// scriptedLLM returns a queued sequence of InferResponses, one per Infer call,
// and records the last request so tests can assert on the assembled prompt.
type scriptedLLM struct {
	mu        sync.Mutex
	responses []core.InferResponse
	lastReq   *ai.Request
	calls     int
}

func (s *scriptedLLM) Name() string { return "scripted" }

func (s *scriptedLLM) Stream(_ context.Context, req *ai.Request) iter.Seq2[ai.Delta, error] {
	s.mu.Lock()
	s.lastReq = req
	s.calls++
	r := core.InferResponse{Content: "Nothing to save.", StopReason: core.StopEndTurn}
	if len(s.responses) > 0 {
		r = s.responses[0]
		s.responses = s.responses[1:]
	}
	s.mu.Unlock()

	return func(yield func(ai.Delta, error) bool) {
		if r.Content != "" {
			if !yield(ai.Delta{Block: ai.TextBlock(r.Content)}, nil) || !yield(ai.Delta{EndBlock: true}, nil) {
				return
			}
		}
		for _, c := range r.ToolCalls {
			if !yield(ai.Delta{Block: ai.ToolCallBlock(ai.ToolCall{ID: c.ID, Name: c.Name, Input: c.Input})}, nil) {
				return
			}
		}
		stop := ai.StopEndTurn
		if len(r.ToolCalls) > 0 {
			stop = ai.StopToolUse
		}
		yield(ai.Delta{StopReason: stop}, nil)
	}
}

// TestTrimTrailingPendingMessages guards against the "messages must
// alternate" provider rejection: when the snapshot ends with a tool_result
// (a RoleUser message with a ToolResult) or any trailing user turn, the fork's
// own UserMessage(prompt) would put two consecutive user-role messages on the wire.
func TestTrimTrailingPendingMessages(t *testing.T) {
	asst := core.Message{Role: core.RoleAssistant, Content: "ok"}
	usr := core.Message{Role: core.RoleUser, Content: "ask"}
	toolResult := core.Message{Role: core.RoleUser, ToolResult: &core.ToolResult{Content: "tool result"}}

	// Snapshot ends with two trailing user messages → both dropped.
	in := []core.Message{asst, usr, usr}
	out := trimTrailingPendingMessages(in)
	if len(out) != 1 || out[0].Role != core.RoleAssistant {
		t.Fatalf("trailing user not trimmed: %+v", out)
	}
	// Snapshot ends with tool_result(s) → all dropped (provider encodes them
	// as user role and the appended prompt would be the second user turn).
	in = []core.Message{asst, toolResult, toolResult}
	out = trimTrailingPendingMessages(in)
	if len(out) != 1 || out[0].Role != core.RoleAssistant {
		t.Fatalf("trailing tool_result not trimmed: %+v", out)
	}
	// Snapshot ends with assistant after a trailing user-then-tool mix → all
	// pending tail dropped down to the last assistant message.
	in = []core.Message{asst, usr, toolResult, usr}
	out = trimTrailingPendingMessages(in)
	if len(out) != 1 || out[0].Role != core.RoleAssistant {
		t.Fatalf("mixed trailing pending not trimmed: %+v", out)
	}
	// Snapshot already ends with assistant → unchanged.
	in = []core.Message{usr, asst}
	out = trimTrailingPendingMessages(in)
	if len(out) != 2 {
		t.Fatalf("non-trailing user incorrectly trimmed: %+v", out)
	}
	// Empty input is safe.
	if got := trimTrailingPendingMessages(nil); got != nil {
		t.Fatalf("nil input should return nil, got %v", got)
	}
}

func TestAllowOnlyPolicy(t *testing.T) {
	store := newTestStore(t)
	tools := core.NewTools(newMemoryWriteTool(store), newSkillManageTool(NewSkillManager("/tmp", AllowAllSkillActions())))
	policy := allowOnly(tools)

	for _, name := range []string{"memory_write", "skill_manage"} {
		if ok, _ := policy(context.Background(), name, nil); !ok {
			t.Fatalf("%s should be allowed", name)
		}
	}
	for _, name := range []string{"Bash", "Edit", "Agent", "Read"} {
		if ok, _ := policy(context.Background(), name, nil); ok {
			t.Fatalf("%s must be denied for the reviewer", name)
		}
	}
}

func TestBuildReviewPromptSelectsArms(t *testing.T) {
	store := newTestStore(t)
	mustAdd(t, store, "existing fact about the build")
	mgr := NewSkillManager("/work/project-x", SkillPermissions{AllowCreate: true, AllowUpdate: true})

	memOnly := buildReviewPrompt(KindMemory, "/work/project-x", store, mgr, "")
	if !strings.Contains(memOnly, "existing fact about the build") {
		t.Fatal("memory prompt should embed the current store")
	}
	if strings.Contains(memOnly, "skill_manage tool") {
		t.Fatal("memory-only prompt should not include the skill section")
	}

	skillOnly := buildReviewPrompt(KindSkills, "/work/project-x", store, mgr, "")
	if !strings.Contains(skillOnly, "skill_manage tool") {
		t.Fatal("skill prompt should include the skill section")
	}

	combined := buildReviewPrompt(KindMemory|KindSkills, "/work/project-x", store, mgr, "")
	if !strings.Contains(combined, "memory_write tool") || !strings.Contains(combined, "skill_manage tool") {
		t.Fatal("combined prompt should include both sections")
	}
}

func TestRunReviewWritesMemoryAndInheritsSystem(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSkillManager("/work/project-x", AllowAllSkillActions())

	llm := &scriptedLLM{responses: []core.InferResponse{
		{
			ToolCalls: []core.ToolCall{{
				ID:    "call-1",
				Name:  "memory_write",
				Input: `{"action":"add","content":"the user prefers tabs"}`,
			}},
			StopReason: core.StopToolUse,
		},
		{Content: "Saved 1 memory entry.", StopReason: core.StopEndTurn},
	}}

	parentSys := core.NewSystem()
	parentSys.Use(core.Section{
		Slot:   core.SlotIdentity,
		Name:   "test-identity",
		Source: core.Predefined,
		Render: func() string { return "PARENT-SYSTEM-MARKER" },
	}, "test")

	fc := ForkConfig{
		Client: ai.NewClientWithDriver(llm, ai.Model{ID: "stub", API: "stub"}),
		System: parentSys,
		CWD:    "/work/project-x",
		Memory: store,
		Skills: mgr,
	}
	snapshot := []core.Message{
		core.UserMessage("please switch the file to tabs", nil),
		core.AssistantMessage("done", "", nil),
	}

	summary, err := RunReview(context.Background(), fc, KindMemory, snapshot)
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !strings.Contains(summary, "Saved") {
		t.Fatalf("summary = %q", summary)
	}
	if got, ok := readBackMemory(t); !ok || !strings.Contains(got, "prefers tabs") {
		t.Fatalf("memory not written: got=%q ok=%v", got, ok)
	}
	if !strings.Contains(llm.lastReq.System, "PARENT-SYSTEM-MARKER") {
		t.Fatalf("fork did not inherit the parent system prompt; system=%q", llm.lastReq.System)
	}
	// The fork must only ever be offered its two write tools.
	if len(llm.lastReq.Tools) != 2 {
		t.Fatalf("fork tool count = %d, want 2", len(llm.lastReq.Tools))
	}
}

// readBackMemory reads back the store written during the test (HOME is
// already pointed at the temp dir by newTestStore).
func readBackMemory(t *testing.T) (string, bool) {
	t.Helper()
	store := NewMemoryStore("/work/project-x", 0, "")
	entries := readEntries(store.Dir() + "/MEMORY.md")
	if len(entries) == 0 {
		return "", false
	}
	return strings.Join(entries, "\n"), true
}
