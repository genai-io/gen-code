package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/genai-io/san/internal/app/input"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

type autopilotStubProvider struct {
	content string
	// replies, when set, is served one entry per call so a test can stage a
	// failed attempt followed by a good one; the last entry repeats.
	replies []string
	calls   int
	// lastOptions records what the steer actually sent, for prompt assertions.
	lastOptions llm.CompletionOptions
}

func (s *autopilotStubProvider) Stream(_ context.Context, opts llm.CompletionOptions) <-chan llm.StreamChunk {
	s.lastOptions = opts
	content := s.content
	if len(s.replies) > 0 {
		content = s.replies[min(s.calls, len(s.replies)-1)]
	}
	s.calls++
	ch := make(chan llm.StreamChunk, 1)
	ch <- llm.StreamChunk{Type: llm.ChunkTypeDone, Response: &llm.CompletionResponse{Content: content}}
	close(ch)
	return ch
}

func (s *autopilotStubProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (s *autopilotStubProvider) Name() string                                        { return "stub" }

func TestAutopilotRecentTranscriptIncludesCompletionEvidence(t *testing.T) {
	messages := []core.ChatMessage{
		{Role: core.RoleUser, Content: core.FormatCompactSummary("created the package skeleton")},
		{Role: core.RoleAssistant, Content: "Run the tests."},
		{Role: core.RoleUser, Content: "ok", ToolResult: &core.ToolResult{
			ToolName: "Bash", Content: "ok  github.com/genai-io/san/internal/app", IsError: false,
		}},
	}

	got := autopilotRecentTranscript(messages, 3000)
	for _, want := range []string{"session summary:", "created the package skeleton", "tool Bash result:", "ok  github.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("recent transcript missing %q:\n%s", want, got)
		}
	}
}

func TestAutopilotRecentTranscriptSkipsEmptyCompactSummary(t *testing.T) {
	messages := []core.ChatMessage{
		{Role: core.RoleUser, Content: core.FormatCompactSummary("")},
		{Role: core.RoleAssistant, Content: "Working on it."},
	}

	got := autopilotRecentTranscript(messages, 3000)
	if strings.Contains(got, "Previous context:") {
		t.Errorf("empty compact summary leaked the sentinel marker:\n%s", got)
	}
	if strings.Contains(got, "session summary:") {
		t.Errorf("empty compact summary should be dropped, not emitted as a row:\n%s", got)
	}
}

func TestAutopilotDecideContinueRejectsContradictoryStates(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{"continue", `{"continue":true,"done":false,"instruction":"Run the tests."}`, false},
		{"done", `{"continue":false,"done":true,"instruction":""}`, false},
		{"handback", `{"continue":false,"done":false,"instruction":""}`, false},
		{"continue and done", `{"continue":true,"done":true,"instruction":"Run tests."}`, true},
		{"continue without instruction", `{"continue":true,"done":false,"instruction":""}`, true},
		{"done with instruction", `{"continue":false,"done":true,"instruction":"Do more."}`, true},
		{"stop with instruction", `{"continue":false,"done":false,"instruction":"Do more."}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noSteerBackoff(t)
			_, _, _, err := autopilotDecideContinue(context.Background(), &autopilotStubProvider{content: tt.content}, "model", "system", "mission", "evidence", "")
			if (err != nil) != tt.wantErr {
				t.Fatalf("autopilotDecideContinue() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// noSteerBackoff removes the retry sleep so a test that exercises the retry path
// doesn't wait out the real backoff.
func noSteerBackoff(t *testing.T) {
	t.Helper()
	previous := autopilotSteerBackoff
	autopilotSteerBackoff = 0
	t.Cleanup(func() { autopilotSteerBackoff = previous })
}

func TestAutopilotDecideContinueRetriesAnUnusableReply(t *testing.T) {
	noSteerBackoff(t)
	provider := &autopilotStubProvider{replies: []string{
		"I think we should keep going!", // prose, not JSON — a transient model slip
		`{"continue":true,"done":false,"instruction":"Run the tests."}`,
	}}

	cont, done, instruction, err := autopilotDecideContinue(context.Background(), provider, "model", "system", "mission", "evidence", "")
	if err != nil {
		t.Fatalf("autopilotDecideContinue() err = %v, want nil after retry", err)
	}
	if !cont || done || instruction != "Run the tests." {
		t.Fatalf("got cont=%v done=%v instruction=%q, want the retried decision", cont, done, instruction)
	}
	if provider.calls != 2 {
		t.Errorf("provider calls = %d, want 2 (one failure, one retry)", provider.calls)
	}
}

func TestAutopilotDecideContinueGivesUpAfterAttemptsAreSpent(t *testing.T) {
	noSteerBackoff(t)
	provider := &autopilotStubProvider{content: "still not JSON"}

	if _, _, _, err := autopilotDecideContinue(context.Background(), provider, "model", "system", "mission", "evidence", ""); err == nil {
		t.Fatal("autopilotDecideContinue() err = nil, want the last parse failure")
	}
	if provider.calls != autopilotSteerAttempts {
		t.Errorf("provider calls = %d, want %d", provider.calls, autopilotSteerAttempts)
	}
}

func TestAutopilotDecideContinueCarriesTheSituationAndMissionlessTask(t *testing.T) {
	noSteerBackoff(t)
	provider := &autopilotStubProvider{content: `{"continue":false,"done":false,"instruction":""}`}

	if _, _, _, err := autopilotDecideContinue(context.Background(), provider, "model", "system", "", "evidence",
		"the previous turn hit its step limit"); err != nil {
		t.Fatalf("autopilotDecideContinue() err = %v", err)
	}
	sent := provider.lastOptions.Messages[0].Content
	if !strings.Contains(sent, "the previous turn hit its step limit") {
		t.Errorf("prompt missing the stop situation:\n%s", sent)
	}
	if !strings.Contains(sent, "No mission was briefed") {
		t.Errorf("prompt missing the mission-less instructions:\n%s", sent)
	}
}

// /goal switches steers on wholesale, so standing it down has to put back what
// the user actually configured — otherwise Bash and Skill stay on behind them.
func TestGoalStandDownRestoresTheConfigItFound(t *testing.T) {
	m := &model{autopilot: &atomic.Pointer[autopilotRuntime]{}}
	suggestOn := true
	configured := setting.AutoPilotSettings{
		Steers:           setting.SteerSettings{Suggest: &suggestOn},
		MaxContinuations: 5,
	}
	// What startGoal leaves behind: the pre-goal snapshot plus the driving set.
	m.env.AutoPilot = configured.Clone()
	before := configured.Clone()
	m.beforeGoal = &before
	m.env.AutoPilot.Mission = "ship it"
	m.env.AutoPilot.EngageDriving()

	m.retireAutopilotMission()
	if m.env.AutoPilot.Mission != "" {
		t.Errorf("mission = %q, want it cleared", m.env.AutoPilot.Mission)
	}
	if got := m.env.AutoPilot; !got.Equal(configured) {
		t.Errorf("stand-down left %+v, want the pre-goal config %+v", got, configured)
	}
	if m.beforeGoal != nil {
		t.Error("the pre-goal snapshot outlived the goal")
	}
}

// Without a goal the wind-down stays subtractive: it stops the copilot driving
// without touching safety steers the user set for themselves.
func TestMissionRetireLeavesConfiguredSafetySteers(t *testing.T) {
	m := &model{autopilot: &atomic.Pointer[autopilotRuntime]{}}
	m.userInput.PromptSuggestion.Text = "stale mission hint"
	suggestOn := true
	m.env.AutoPilot = setting.AutoPilotSettings{
		Mission: "ship it",
		Steers:  setting.SteerSettings{BashPrompt: true, Suggest: &suggestOn, TurnEnd: true},
	}

	m.retireAutopilotMission()
	if !m.env.AutoPilot.Steers.BashPrompt {
		t.Error("retire flipped off a Bash steer the user configured")
	}
	if m.env.AutoPilot.Steers.SuggestOn() || m.env.AutoPilot.Steers.TurnEnd {
		t.Error("retire left a driving steer on")
	}
	if m.userInput.PromptSuggestion.Text != "" {
		t.Error("retire left a stale mission suggestion on screen")
	}
}

// newSuggestionModel builds a model wired far enough for the input hint to
// fire: a stub provider, a conversation to predict from, and the mode entered
// the way the app enters it (the steer gates read the permission posture, not
// the raw field).
func newSuggestionModel(t *testing.T, suggest bool, mode setting.OperationMode) *model {
	m := &model{autopilot: &atomic.Pointer[autopilotRuntime]{}}
	m.services.LLM = &llm.Conn{}
	m.env.LLMProvider = &autopilotStubProvider{}
	m.env.SessionPermissions = setting.NewSessionPermissions()
	m.env.OperationMode = mode
	m.env.ApplyModePermissions(t.TempDir())
	m.env.AutoPilot.Steers.Suggest = &suggest
	m.conv.Messages = []core.ChatMessage{
		{Role: core.RoleAssistant, Content: "Done."},
		{Role: core.RoleUser, Content: "Next."},
		{Role: core.RoleAssistant, Content: "Also done."},
	}
	return m
}

// The Suggest steer is the input hint's feature switch in every mode: when
// explicitly off the hint never fires — including outside AutoPilot, where
// generic prediction used to be unconditional.
func TestPromptSuggestionGatedOnSuggestSteerInEveryMode(t *testing.T) {
	if cmd := newSuggestionModel(t, false, setting.ModeNormal).startPromptSuggestion(); cmd != nil {
		t.Error("suggest steer off: the hint still fired outside AutoPilot")
	}
	if cmd := newSuggestionModel(t, false, setting.ModeAutoPilot).startPromptSuggestion(); cmd != nil {
		t.Error("suggest steer off: the hint still fired in AutoPilot mode")
	}
	if cmd := newSuggestionModel(t, true, setting.ModeNormal).startPromptSuggestion(); cmd == nil {
		t.Error("suggest steer on: no hint outside AutoPilot")
	}
}

// Shift+Tab can now land on AutoPilot mid-turn. The opening proposal is ghost
// text for a textarea the user cannot type into yet, so it must not cost an LLM
// call — OnTurnEnd re-arms the hint once the turn is over.
func TestAutopilotModeSettledSkipsSuggestionMidTurn(t *testing.T) {
	m := newSuggestionModel(t, true, setting.ModeAutoPilot)

	if cmd := m.handleAutopilotModeSettled(); cmd == nil {
		t.Fatal("settling on AutoPilot while idle did not start the proposal")
	}

	m.conv.Stream.Active = true
	if cmd := m.handleAutopilotModeSettled(); cmd != nil {
		t.Fatal("settling on AutoPilot mid-turn started the proposal anyway")
	}
}

func TestAutopilotSaveWithSuggestOffClearsPromptSuggestion(t *testing.T) {
	off := false
	m := &model{autopilot: &atomic.Pointer[autopilotRuntime]{}}
	m.userInput.PromptSuggestion.Text = "stale hint"

	_, _ = m.Update(input.AutopilotSavedMsg{Config: setting.AutoPilotSettings{
		Steers: setting.SteerSettings{Suggest: &off},
	}})

	if m.userInput.PromptSuggestion.Text != "" {
		t.Fatal("saving Suggest off left stale prompt suggestion text")
	}
}

func TestAutopilotStopEvidence(t *testing.T) {
	resumable := map[core.StopReason]bool{
		core.StopEndTurn:                    true,
		core.StopMaxSteps:                   true,
		core.StopMaxOutputRecoveryExhausted: true,
		core.StopCancelled:                  false, // the human took the helm
		core.StopHook:                       false, // a configured halt
	}
	for reason, want := range resumable {
		got, situation := autopilotStopEvidence(reason)
		if got != want {
			t.Errorf("autopilotStopEvidence(%q) resumable = %v, want %v", reason, got, want)
		}
		// Every resumable stop but a clean end_turn must explain itself, or the
		// copilot sees an inexplicably truncated transcript.
		if wantSituation := want && reason != core.StopEndTurn; wantSituation != (situation != "") {
			t.Errorf("autopilotStopEvidence(%q) situation = %q, want explained = %v", reason, situation, wantSituation)
		}
	}
}
