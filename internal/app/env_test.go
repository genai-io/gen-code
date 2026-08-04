package app

import (
	"slices"
	"testing"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/setting"
)

func TestApplyDefaultPermissionMode_RestoresAllModes(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		allowBypass bool
		want        setting.OperationMode
	}{
		{"normal", "normal", true, setting.ModeNormal},
		{"auto accept", "auto-accept", true, setting.ModeAutoAccept},
		{"autopilot", "auto-pilot", true, setting.ModeAutoPilot},
		{"bypass", "bypass", true, setting.ModeBypassPermissions},
		{"bypass disabled", "bypass", false, setting.ModeNormal},
		{"dont ask", "dont-ask", true, setting.ModeDontAsk},
		{"read only", "read-only", true, setting.ModeReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := env{SessionPermissions: setting.NewSessionPermissions()}
			e.ApplyDefaultPermissionMode(tt.mode, "/workspace", tt.allowBypass)
			if e.OperationMode != tt.want {
				t.Fatalf("OperationMode = %v, want %v", e.OperationMode, tt.want)
			}
			if e.SessionPermissions.Mode != tt.want {
				t.Fatalf("SessionPermissions.Mode = %v, want %v", e.SessionPermissions.Mode, tt.want)
			}
		})
	}
}

// `san -r` restores a recorded mode, and leaves the launch's alone when the
// session recorded none.
func TestResumeOperationMode(t *testing.T) {
	tests := []struct {
		name     string
		recorded string
		startup  setting.OperationMode
		want     setting.OperationMode
	}{
		{"no recorded mode keeps accept edits", "", setting.ModeAutoAccept, setting.ModeAutoAccept},
		{"recorded normal wins over startup", "normal", setting.ModeAutoAccept, setting.ModeNormal},
		{"recorded autopilot restores", "auto-pilot", setting.ModeNormal, setting.ModeAutoPilot},
		{"recorded bypass never round-trips", "bypass", setting.ModeAutoAccept, setting.ModeNormal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resumeOperationMode(tt.recorded, tt.startup); got != tt.want {
				t.Fatalf("resumeOperationMode(%q, %v) = %v, want %v", tt.recorded, tt.startup, got, tt.want)
			}
		})
	}
}

// Shift+Tab cycles modes on the UI goroutine while the agent goroutine stamps
// each permission decision with SessionMode(). Run under -race: the reader must
// go through the synchronized posture, never the UI's OperationMode field.
func TestSessionModeReadsPostureWhileModesCycle(t *testing.T) {
	e := env{SessionPermissions: setting.NewSessionPermissions(), CWD: t.TempDir()}
	cycle := []setting.OperationMode{
		setting.ModeNormal, setting.ModeAutoAccept, setting.ModeAutoPilot, setting.ModeBypassPermissions,
	}

	cycled := make(chan struct{})
	go func() {
		defer close(cycled)
		for i := range 200 {
			e.OperationMode = cycle[i%len(cycle)]
			e.ApplyModePermissions(e.CWD)
		}
	}()

	for range 200 {
		_ = e.SessionMode()
	}
	<-cycled
}

func TestResetContextDisplay_PreservesCompressions(t *testing.T) {
	e := &env{Compressions: 3, InputTokens: 100, OutputTokens: 50}
	e.ResetContextDisplay()
	if e.Compressions != 3 {
		t.Errorf("Compressions = %d, want 3 (must survive ResetContextDisplay)", e.Compressions)
	}
	if e.InputTokens != 0 || e.OutputTokens != 0 {
		t.Errorf("InputTokens=%d OutputTokens=%d, want 0/0", e.InputTokens, e.OutputTokens)
	}
}

// ResetContextDisplay runs on every (auto-)compaction; the session-cumulative
// cost must survive it, otherwise a long session's displayed spend resets to
// zero each time the context is compacted.
func TestResetContextDisplay_PreservesConversationCost(t *testing.T) {
	cost := llm.NewCostTotal(llm.Money{Amount: 1.25, Currency: llm.CurrencyUSD})
	e := &env{InputTokens: 100, OutputTokens: 50, ConversationCost: cost}
	e.ResetContextDisplay()
	if !slices.Equal(e.ConversationCost.Amounts(), cost.Amounts()) {
		t.Errorf("ConversationCost = %v, want %v (must survive ResetContextDisplay)", e.ConversationCost.Amounts(), cost.Amounts())
	}
}

func TestResetTokens_ZeroesCompressions(t *testing.T) {
	e := &env{Compressions: 5, InputTokens: 80, OutputTokens: 40}
	e.ResetTokens()
	if e.Compressions != 0 {
		t.Errorf("Compressions = %d, want 0 after ResetTokens", e.Compressions)
	}
	if e.InputTokens != 0 || e.OutputTokens != 0 {
		t.Errorf("context tokens not zeroed: %d/%d", e.InputTokens, e.OutputTokens)
	}
}

func TestResetTokens_ZeroesConversationCost(t *testing.T) {
	e := &env{ConversationCost: llm.NewCostTotal(llm.Money{Amount: 2.50, Currency: llm.CurrencyUSD})}
	e.ResetTokens()
	if !e.ConversationCost.IsZero() {
		t.Errorf("ConversationCost = %v, want zero after ResetTokens", e.ConversationCost)
	}
}

func TestCompressions_StartsAtZero(t *testing.T) {
	e := &env{}
	if e.Compressions != 0 {
		t.Errorf("Compressions = %d, want 0 at session start", e.Compressions)
	}
}
