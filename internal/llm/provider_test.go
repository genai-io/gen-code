package llm

import (
	"context"
	"slices"
	"testing"
)

// plainProvider implements Provider without declaring image support, so the
// helper must default it to true.
type plainProvider struct{}

func (plainProvider) Stream(context.Context, CompletionOptions) <-chan StreamChunk { return nil }
func (plainProvider) ListModels(context.Context) ([]ModelInfo, error)              { return nil, nil }
func (plainProvider) Name() string                                                 { return "plain" }

// textOnlyProvider opts out of image input via ImageSupportProvider.
type textOnlyProvider struct{ plainProvider }

func (textOnlyProvider) SupportsImages(string) bool { return false }

type staticReasoningProvider struct{ plainProvider }

func (staticReasoningProvider) ThinkingEfforts(string) []string { return []string{"high"} }
func (staticReasoningProvider) DefaultThinkingEffort(string) string {
	return "high"
}

func TestSupportsImages(t *testing.T) {
	if !SupportsImages(plainProvider{}, "m") {
		t.Error("a provider that doesn't implement ImageSupportProvider should default to supported")
	}
	if SupportsImages(textOnlyProvider{}, "m") {
		t.Error("a provider that opts out should report no image support")
	}
}

func TestNewReasoningCapabilityNormalizesProviderValues(t *testing.T) {
	capability := NewReasoningCapability([]string{" Low ", "ULTRA", "low", ""}, " Ultra ")
	if capability == nil {
		t.Fatal("NewReasoningCapability() = nil")
	}
	if !slices.Equal(capability.SupportedEfforts, []string{"low", "ultra"}) {
		t.Fatalf("efforts = %v, want [low ultra]", capability.SupportedEfforts)
	}
	if capability.DefaultEffort != "ultra" {
		t.Fatalf("default = %q, want ultra", capability.DefaultEffort)
	}
	if got := NewReasoningCapability(nil, "high"); got != nil {
		t.Fatalf("empty capability = %+v, want nil", got)
	}
}

func TestModelReasoningMetadataOverridesProviderFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.CacheModels(OpenAI, AuthSubscription, []ModelInfo{{
		ID:        "gpt-dynamic",
		Reasoning: NewReasoningCapability([]string{"low", "ultra"}, "low"),
	}}); err != nil {
		t.Fatalf("CacheModels() error = %v", err)
	}
	reloaded, err := NewStore()
	if err != nil {
		t.Fatalf("reload NewStore() error = %v", err)
	}

	provider := staticReasoningProvider{}
	current := &CurrentModelInfo{ModelID: "gpt-dynamic", Provider: OpenAI, AuthMethod: AuthSubscription}
	if got := ThinkingEffortsForModel(provider, reloaded, current); !slices.Equal(got, []string{"low", "ultra"}) {
		t.Fatalf("ThinkingEffortsForModel() = %v, want dynamic [low ultra]", got)
	}
	if got := ResolveThinkingEffortForModel(provider, reloaded, current, "invalid"); got != "low" {
		t.Fatalf("ResolveThinkingEffortForModel(invalid) = %q, want dynamic default low", got)
	}
	if got, ok := NextThinkingEffortForModel(provider, reloaded, current, "low"); !ok || got != "ultra" {
		t.Fatalf("NextThinkingEffortForModel(low) = (%q, %v), want (ultra, true)", got, ok)
	}

	legacy := &CurrentModelInfo{ModelID: "legacy", Provider: OpenAI, AuthMethod: AuthSubscription}
	if got := ThinkingEffortsForModel(provider, reloaded, legacy); !slices.Equal(got, []string{"high"}) {
		t.Fatalf("legacy fallback efforts = %v, want [high]", got)
	}
}
