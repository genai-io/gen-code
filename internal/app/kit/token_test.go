package kit

import (
	"strings"
	"testing"

	"github.com/genai-io/san/internal/llm"
)

// TestGetModelTokenLimitsPrefersCurrentProvider guards against the status-bar
// context window flickering when the same model ID is cached under multiple
// providers with different windows: it must resolve to the connected provider's
// value deterministically, not a random map hit.
func TestGetModelTokenLimitsPrefersCurrentProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := store.CacheModels(llm.OpenAI, llm.AuthAPIKey, []llm.ModelInfo{
		{ID: "gpt-5.5", InputTokenLimit: 400000, OutputTokenLimit: 16384},
	}); err != nil {
		t.Fatalf("CacheModels(api_key): %v", err)
	}
	if err := store.CacheModels(llm.OpenAI, llm.AuthSubscription, []llm.ModelInfo{
		{ID: "gpt-5.5", InputTokenLimit: 272000, OutputTokenLimit: 16384},
	}); err != nil {
		t.Fatalf("CacheModels(subscription): %v", err)
	}

	current := &llm.CurrentModelInfo{
		ModelID:    "gpt-5.5",
		Provider:   llm.OpenAI,
		AuthMethod: llm.AuthSubscription,
	}

	// Repeat to catch the non-deterministic map iteration the bug relied on.
	for range 30 {
		if got := GetEffectiveInputLimit(store, current); got != 272000 {
			t.Fatalf("input limit = %d, want 272000 (current provider's cache, not the 400k api_key entry)", got)
		}
	}
}

func TestGetModelTokenLimitsUsesConnectedAuthWhenCurrentAuthMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := store.Connect(llm.OpenAI, llm.AuthSubscription); err != nil {
		t.Fatalf("Connect(subscription): %v", err)
	}
	if err := store.CacheModels(llm.OpenAI, llm.AuthAPIKey, []llm.ModelInfo{
		{ID: "gpt-5.5", InputTokenLimit: 400000, OutputTokenLimit: 16384},
	}); err != nil {
		t.Fatalf("CacheModels(api_key): %v", err)
	}
	if err := store.CacheModels(llm.OpenAI, llm.AuthSubscription, []llm.ModelInfo{
		{ID: "gpt-5.5", InputTokenLimit: 272000, OutputTokenLimit: 16384},
	}); err != nil {
		t.Fatalf("CacheModels(subscription): %v", err)
	}

	current := &llm.CurrentModelInfo{
		ModelID:  "gpt-5.5",
		Provider: llm.OpenAI,
		// Older providers.json files did not persist authMethod on current.
	}

	for range 30 {
		if got := GetEffectiveInputLimit(store, current); got != 272000 {
			t.Fatalf("input limit = %d, want 272000 from connected subscription auth", got)
		}
	}
}

// A model whose window San cannot discover resolves to 0, which the status bar
// renders as "--". Inventing a figure would show a percentage of a guess and,
// worse, have compaction act on it.
func TestGetEffectiveInputLimitUnknownIsZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(llm.InputLimitEnvVar, "")
	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got := GetEffectiveInputLimit(store, &llm.CurrentModelInfo{
		ModelID: "unknown-model", Provider: llm.OpenAI, AuthMethod: llm.AuthAPIKey,
	})
	if got != 0 {
		t.Fatalf("GetEffectiveInputLimit() = %d, want 0 for an undiscoverable window", got)
	}
}

// The env override wins over the cache, so a user can correct a provider that
// under-reports its window.
func TestGetEffectiveInputLimitEnvOverrideWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(llm.InputLimitEnvVar, "1000000")
	store, err := llm.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.CacheModels(llm.OpenAI, llm.AuthAPIKey, []llm.ModelInfo{
		{ID: "m", InputTokenLimit: 200000},
	}); err != nil {
		t.Fatalf("CacheModels: %v", err)
	}

	got := GetEffectiveInputLimit(store, &llm.CurrentModelInfo{
		ModelID: "m", Provider: llm.OpenAI, AuthMethod: llm.AuthAPIKey,
	})
	if got != 1_000_000 {
		t.Fatalf("GetEffectiveInputLimit() = %d, want the 1000000 override", got)
	}
}

// No model selected is genuinely unknown, not a case for guessing — the status
// bar renders 0 as "--" rather than a percentage against an invented window.
func TestGetEffectiveInputLimitWithoutModelIsZero(t *testing.T) {
	t.Setenv(llm.InputLimitEnvVar, "500000")
	if got := GetEffectiveInputLimit(nil, nil); got != 0 {
		t.Fatalf("GetEffectiveInputLimit(nil, nil) = %d, want 0", got)
	}
}

// A single ratio over the whole string is what makes a Chinese AGENTS.md read as
// a quarter of its real cost, so the two scripts are checked against each
// other rather than against exact numbers a tokenizer would have to confirm.
func TestEstimateTokensCountsCJKHeavierThanASCII(t *testing.T) {
	const runes = 40
	ascii := strings.Repeat("a", runes)
	chinese := strings.Repeat("中", runes)

	if got := EstimateTokens(ascii); got != runes/4 {
		t.Errorf("EstimateTokens(%d ascii chars) = %d, want %d", runes, got, runes/4)
	}
	if EstimateTokens(chinese) <= EstimateTokens(ascii) {
		t.Errorf("%d Chinese characters (%d) should cost more than %d ASCII (%d)",
			runes, EstimateTokens(chinese), runes, EstimateTokens(ascii))
	}
}

// Short strings must not vanish: a legend row that reports 0 for content that
// exists reads as "this is not loaded".
func TestEstimateTokensNeverRoundsNonEmptyToZero(t *testing.T) {
	if got := EstimateTokens("hi"); got != 1 {
		t.Errorf(`EstimateTokens("hi") = %d, want 1`, got)
	}
	if got := EstimateTokens(""); got != 0 {
		t.Errorf(`EstimateTokens("") = %d, want 0`, got)
	}
}
