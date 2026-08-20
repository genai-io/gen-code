package anthropic

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestCatalogModelLimitsAndThinking(t *testing.T) {
	tests := []struct {
		model           string
		wantReasoning   bool
		wantInputLimit  int
		wantOutputLimit int
	}{
		// Claude 4.6 and later carry the full 1M window at standard pricing.
		{model: "claude-opus-5", wantReasoning: true, wantInputLimit: 1000000, wantOutputLimit: 128000},
		{model: "claude-fable-5", wantReasoning: true, wantInputLimit: 1000000, wantOutputLimit: 128000},
		{model: "claude-sonnet-5", wantReasoning: true, wantInputLimit: 1000000, wantOutputLimit: 128000},
		{model: "claude-opus-4-6", wantReasoning: true, wantInputLimit: 1000000, wantOutputLimit: 128000},
		// The 4.5 generation stayed at 200K.
		{model: "claude-opus-4-5-20251101", wantReasoning: true, wantInputLimit: 200000, wantOutputLimit: 64000},
		{model: "claude-sonnet-4-5@20250929", wantReasoning: true, wantInputLimit: 200000, wantOutputLimit: 64000},
		{model: "claude-haiku-4-5-20251001", wantReasoning: true, wantInputLimit: 200000, wantOutputLimit: 64000},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			info, ok := CatalogModel(tt.model)
			if !ok {
				t.Fatalf("CatalogModel(%q) not found", tt.model)
			}
			if info.InputTokenLimit != tt.wantInputLimit {
				t.Fatalf("InputTokenLimit = %d, want %d", info.InputTokenLimit, tt.wantInputLimit)
			}
			if info.OutputTokenLimit != tt.wantOutputLimit {
				t.Fatalf("OutputTokenLimit = %d, want %d", info.OutputTokenLimit, tt.wantOutputLimit)
			}
			if got := supportsThinkingModel(tt.model); got != tt.wantReasoning {
				t.Fatalf("supportsThinkingModel(%q) = %v, want %v", tt.model, got, tt.wantReasoning)
			}
		})
	}
}

// Retired models must not linger in the catalog: offering one produces a turn
// that fails at the API with nothing in San explaining why.
func TestRetiredModelsAreGone(t *testing.T) {
	for _, id := range []string{
		"claude-3-7-sonnet-20250219", // retired 2026-02-19
		"claude-3-5-haiku-20241022",  // retired 2026-02-19
		"claude-opus-4-1-20250805",   // retired 2026-08-05
	} {
		if _, ok := CatalogModel(id); ok {
			t.Errorf("retired model %q is still in the catalog", id)
		}
	}
}

// A budget is only ever sent on the pre-4.6 shape. Claude 4.6 and later take
// adaptive thinking, and from Opus 4.7 on a budget_tokens request is rejected
// with a 400 rather than merely deprecated.
func TestThinkingStyleIsPerModel(t *testing.T) {
	tests := map[string]thinkingStyle{
		"claude-opus-5":              styleAdaptive,
		"claude-opus-4-8":            styleAdaptive,
		"claude-opus-4-6":            styleAdaptive,
		"claude-sonnet-4-6":          styleAdaptive,
		"claude-opus-4-5-20251101":   styleBudget,
		"claude-sonnet-4-5-20250929": styleBudget,
		"claude-haiku-4-5-20251001":  styleBudget,
		// An Anthropic-compatible third-party endpoint reuses this client and
		// implements only the older shape.
		"MiniMax-M2.7":         styleBudget,
		"xiaomi/mimo-v2.5-pro": styleBudget,
	}
	for model, want := range tests {
		if got := modelThinkingStyle(model); got != want {
			t.Errorf("modelThinkingStyle(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestAnthropicThinkingBudget(t *testing.T) {
	tests := []struct {
		model  string
		effort string
		want   int
	}{
		{model: "claude-opus-4-5-20251101", effort: ThinkingNormal, want: 5000},
		{model: "claude-sonnet-4-5-20250929", effort: ThinkingUltra, want: 128000},
		{model: "claude-haiku-4-5-20251001", effort: ThinkingHigh, want: 32000},
		{model: "unknown-model", effort: ThinkingHigh, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := anthropicThinkingBudget(tt.model, tt.effort); got != tt.want {
				t.Fatalf("anthropicThinkingBudget(%q, %q) = %d, want %d", tt.model, tt.effort, got, tt.want)
			}
		})
	}
}

func TestAdaptiveEffortMapping(t *testing.T) {
	tests := map[string]anthropic.OutputConfigEffort{
		ThinkingNormal: anthropic.OutputConfigEffortLow,
		ThinkingHigh:   anthropic.OutputConfigEffortHigh,
		ThinkingUltra:  anthropic.OutputConfigEffortMax,
		ThinkingOff:    "",
		"":             "",
	}
	for effort, want := range tests {
		if got := adaptiveEffort(effort); got != want {
			t.Errorf("adaptiveEffort(%q) = %q, want %q", effort, got, want)
		}
	}
}

// Fable 5 rejects an explicit thinking: {"type": "disabled"}, so the effort
// list must not offer one.
func TestAlwaysThinkingModelHidesOff(t *testing.T) {
	c := &Client{}
	efforts := c.ThinkingEfforts("claude-fable-5")
	for _, e := range efforts {
		if e == ThinkingOff {
			t.Fatalf("claude-fable-5 efforts = %v, must not include %q", efforts, ThinkingOff)
		}
	}
	if got := c.DefaultThinkingEffort("claude-fable-5"); got == ThinkingOff || got == "" {
		t.Errorf("DefaultThinkingEffort = %q, want a thinking level", got)
	}
	// Every other model keeps the switch.
	opus := c.ThinkingEfforts("claude-opus-5")
	if len(opus) == 0 || opus[0] != ThinkingOff {
		t.Errorf("claude-opus-5 efforts = %v, want %q first", opus, ThinkingOff)
	}
}

func TestStaticModelsUsesOfficialCatalog(t *testing.T) {
	models := StaticModels()
	if len(models) == 0 {
		t.Fatal("expected static models")
	}

	seen := map[string]bool{}
	for _, model := range models {
		seen[model.ID] = true
	}

	for _, required := range []string{
		"claude-fable-5",
		"claude-opus-5",
		"claude-opus-4-8",
		"claude-sonnet-5",
		"claude-haiku-4-5",
	} {
		if !seen[required] {
			t.Fatalf("expected %q in static model list", required)
		}
	}
}
