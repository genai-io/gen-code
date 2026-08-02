package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/genai-io/san/internal/agent"
	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/todo"
)

// The bottom-right ctx readout must reflect only the most recent infer call's
// full context, never a running sum across the turn's infer steps. Two
// consecutive OnInference calls (as happens around a tool loop): the second
// must fully replace the first, not accumulate on top of it.
func TestOnInferenceUsesLatestCallNotAccumulated(t *testing.T) {
	m := &model{}

	// First infer: a large cached prompt. ctx = full prompt (fresh + cached).
	m.OnInference(&core.InferResponse{Usage: core.Usage{
		InputTokens:          500,
		OutputTokens:         80,
		CacheReadInputTokens: 140000,
	}})
	if m.env.InputTokens != 140500 || m.env.OutputTokens != 80 {
		t.Fatalf("first update = in:%d out:%d, want in:140500 out:80", m.env.InputTokens, m.env.OutputTokens)
	}

	// Second infer in the same turn: ctx must become THIS call's full context,
	// not the sum of both calls (which would be 281800).
	m.OnInference(&core.InferResponse{Usage: core.Usage{
		InputTokens:          300,
		OutputTokens:         25,
		CacheReadInputTokens: 141000,
	}})
	if m.env.InputTokens != 141300 || m.env.OutputTokens != 25 {
		t.Fatalf("latest update = in:%d out:%d, want in:141300 out:25 (latest full context, not accumulated)", m.env.InputTokens, m.env.OutputTokens)
	}
}

// The ctx readout must count the cached prompt (reported separately in
// Anthropic-style usage) so it reflects real window occupancy: the full prompt
// is fresh + cache read + cache creation.
func TestOnInferenceCountsCachedPromptInContext(t *testing.T) {
	m := &model{}

	m.OnInference(&core.InferResponse{Usage: core.Usage{
		InputTokens:              500,
		OutputTokens:             80,
		CacheReadInputTokens:     140000,
		CacheCreationInputTokens: 1000,
	}})

	if want := 141500; m.env.InputTokens != want {
		t.Fatalf("context InputTokens = %d, want %d (fresh + cache read + cache creation)", m.env.InputTokens, want)
	}
}

func TestResumeCommandForSessionRequiresPersistedTranscript(t *testing.T) {
	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")

	if got := resumeCommandForSession("session-1", transcriptPath); got != "" {
		t.Fatalf("resumeCommandForSession() = %q, want empty command for missing transcript", got)
	}

	if err := os.WriteFile(transcriptPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	if got := resumeCommandForSession("session-1", transcriptPath); got != "san -r session-1" {
		t.Fatalf("resumeCommandForSession() = %q, want san -r session-1", got)
	}
}

func TestOnInferenceClearsCompactedStatusOnNextInfer(t *testing.T) {
	m := &model{}
	m.userInput.Provider.StatusMessage = "compacted"

	m.OnInference(&core.InferResponse{Usage: core.Usage{InputTokens: 400, OutputTokens: 25}})

	if m.userInput.Provider.StatusMessage != "" {
		t.Fatalf("StatusMessage = %q, want compacted badge cleared on next infer", m.userInput.Provider.StatusMessage)
	}
}

// ResetContextDisplay zeroes the bottom-right context readout (latest infer
// call's input/output) so a post-compaction transitional frame doesn't show
// stale occupancy until the next infer.
func TestResetContextDisplayZeroesContextReadout(t *testing.T) {
	m := &model{}
	m.env.InputTokens = 1200
	m.env.OutputTokens = 80

	m.env.ResetContextDisplay()

	if m.env.InputTokens != 0 || m.env.OutputTokens != 0 {
		t.Fatalf("context display reset = in:%d out:%d, want zeros", m.env.InputTokens, m.env.OutputTokens)
	}
}

// The agent's echo of a user message must not reach the conversation: every
// path that hands one to the agent (idle submit, queue release, cron prompt,
// async hook, a notice delivered mid-turn) appends it at the call site, so
// acting on the echo would double-display it.
func TestAgentMessageEchoIsIgnored(t *testing.T) {
	m := &model{
		userInput: input.Model{Queue: input.NewQueue()},
		conv:      conv.NewModel(80),
		services:  services{Tracker: todo.NewStore(), Agent: &agent.Session{}},
	}

	echo := core.MessageEvent("agent-1", core.UserMessage("anything", nil))
	cmd, handled := conv.Update(m, &m.conv, conv.AgentOutboxMsg{Event: echo})

	if !handled {
		t.Fatal("conv.Update did not handle an agent outbox message")
	}
	if cmd != nil {
		t.Fatal("an echoed user message produced work")
	}
	if len(m.conv.Messages) != 0 {
		t.Fatalf("conv messages = %d, want 0 (echo must not append)", len(m.conv.Messages))
	}
}
