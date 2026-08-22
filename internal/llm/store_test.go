package llm

import "testing"

func TestStore_PersistsConnectionsCurrentModelSearchProviderAndTokenLimits(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	if err := store.Connect(OpenAI, AuthAPIKey); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := store.SetCurrentModel("gpt-5", OpenAI, AuthAPIKey); err != nil {
		t.Fatalf("SetCurrentModel() error = %v", err)
	}
	if err := store.SetSearchProvider("brave"); err != nil {
		t.Fatalf("SetSearchProvider() error = %v", err)
	}
	if err := store.SetTokenLimit("gpt-5", 200000, 32000); err != nil {
		t.Fatalf("SetTokenLimit() error = %v", err)
	}

	reloaded, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore(reload) error = %v", err)
	}

	if !reloaded.IsConnected(OpenAI, AuthAPIKey) {
		t.Fatal("expected OpenAI API key connection to persist")
	}
	current := reloaded.GetCurrentModel()
	if current == nil || current.ModelID != "gpt-5" || current.Provider != OpenAI || current.AuthMethod != AuthAPIKey {
		t.Fatalf("unexpected current model after reload: %#v", current)
	}
	if reloaded.GetSearchProvider() != "brave" {
		t.Fatalf("search provider = %q, want %q", reloaded.GetSearchProvider(), "brave")
	}
	in, out, ok := reloaded.GetTokenLimit("gpt-5")
	if !ok || in != 200000 || out != 32000 {
		t.Fatalf("unexpected token limit after reload: in=%d out=%d ok=%v", in, out, ok)
	}
}

// The provider selector caches model metadata through its own Store instance;
// the shared app-level Store the status bar reads is a separate instance backed
// by the same file. Reload must let that shared instance pick up the selector's
// writes so a just-switched model's display name and context window appear in
// the status bar instead of the raw ID and "--".
func TestStore_ReloadPicksUpAnotherInstancesWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	shared, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore(shared) error = %v", err)
	}

	// Before the selector runs, the shared store knows nothing about the model.
	if name := shared.CachedModelDisplayName("kimi-k2"); name != "" {
		t.Fatalf("CachedModelDisplayName before selector = %q, want empty", name)
	}
	if in, _ := shared.CachedModelLimits("kimi-k2"); in != 0 {
		t.Fatalf("CachedModelLimits before selector = %d, want 0", in)
	}

	// The selector (a separate instance on the same file) fetches and caches
	// the model, then persists it as the current model.
	selector, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore(selector) error = %v", err)
	}
	if err := selector.CacheModels(Moonshot, AuthAPIKey, []ModelInfo{
		{ID: "kimi-k2", Name: "kimi-k2", DisplayName: "Kimi K2", InputTokenLimit: 256_000, OutputTokenLimit: 16_384},
	}); err != nil {
		t.Fatalf("CacheModels() error = %v", err)
	}
	if err := selector.SetCurrentModel("kimi-k2", Moonshot, AuthAPIKey); err != nil {
		t.Fatalf("SetCurrentModel() error = %v", err)
	}

	// The shared instance's in-memory cache is still stale until it reloads.
	if name := shared.CachedModelDisplayName("kimi-k2"); name != "" {
		t.Fatalf("CachedModelDisplayName without reload = %q, want empty (stale)", name)
	}

	if err := shared.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}

	if name := shared.CachedModelDisplayName("kimi-k2"); name != "Kimi K2" {
		t.Fatalf("CachedModelDisplayName after reload = %q, want %q", name, "Kimi K2")
	}
	in, out := shared.CachedModelLimitsForProvider(Moonshot, AuthAPIKey, "kimi-k2")
	if in != 256_000 || out != 16_384 {
		t.Fatalf("CachedModelLimitsForProvider after reload = (%d, %d), want (256000, 16384)", in, out)
	}
	if cur := shared.GetCurrentModel(); cur == nil || cur.ModelID != "kimi-k2" {
		t.Fatalf("GetCurrentModel after reload = %#v, want kimi-k2", cur)
	}
}

// Reload on a store whose file was never written (fresh install) must be a
// no-op, not an error.
func TestStore_ReloadMissingFileIsNoError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload() on unwritten store error = %v, want nil", err)
	}
}

func TestStore_SetTokenLimitUpdatesCachedModelCopy(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	models := []ModelInfo{
		{ID: "gpt-5", Name: "GPT-5"},
		{ID: "gpt-5-mini", Name: "GPT-5 mini"},
	}
	if err := store.CacheModels(OpenAI, AuthAPIKey, models); err != nil {
		t.Fatalf("CacheModels() error = %v", err)
	}

	cachedBefore, ok := store.GetCachedModels(OpenAI, AuthAPIKey)
	if !ok {
		t.Fatal("expected cached models")
	}

	if err := store.SetTokenLimit("gpt-5", 256000, 64000); err != nil {
		t.Fatalf("SetTokenLimit() error = %v", err)
	}

	cachedAfter, ok := store.GetCachedModels(OpenAI, AuthAPIKey)
	if !ok {
		t.Fatal("expected cached models after override")
	}
	if cachedAfter[0].InputTokenLimit != 256000 || cachedAfter[0].OutputTokenLimit != 64000 {
		t.Fatalf("expected cached override applied, got %#v", cachedAfter[0])
	}
	if cachedAfter[1].InputTokenLimit != 0 || cachedAfter[1].OutputTokenLimit != 0 {
		t.Fatalf("expected unrelated model unchanged, got %#v", cachedAfter[1])
	}
	if cachedBefore[0].InputTokenLimit != 0 || cachedBefore[0].OutputTokenLimit != 0 {
		t.Fatalf("expected previously returned cached slice to remain unchanged, got %#v", cachedBefore[0])
	}
}

// When the same model ID is cached under multiple provider/auth keys — one with
// a real display name and another that only echoes the raw ID — the display
// name must be stable across renders. Returning the first map match would
// flicker because Go randomizes map iteration order; we must deterministically
// prefer the real display name.
func TestStore_CachedModelDisplayNamePrefersRealNameAndIsStable(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	// alibaba echoes the raw ID as the display name; deepseek has a real one.
	if err := store.CacheModels(Alibaba, AuthAPIKey, []ModelInfo{
		{ID: "deepseek-v4-pro", Name: "deepseek-v4-pro", DisplayName: "deepseek-v4-pro"},
	}); err != nil {
		t.Fatalf("CacheModels(alibaba) error = %v", err)
	}
	if err := store.CacheModels(DeepSeek, AuthAPIKey, []ModelInfo{
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", DisplayName: "DeepSeek V4 Pro"},
	}); err != nil {
		t.Fatalf("CacheModels(deepseek) error = %v", err)
	}

	// Call many times; with randomized map order an order-dependent
	// implementation would return both values across iterations.
	for i := range 100 {
		if got := store.CachedModelDisplayName("deepseek-v4-pro"); got != "DeepSeek V4 Pro" {
			t.Fatalf("CachedModelDisplayName() = %q, want %q (unstable/wrong on iteration %d)", got, "DeepSeek V4 Pro", i)
		}
	}

	// A model that only ever echoes its ID still falls back to that ID.
	if err := store.CacheModels(OpenAI, AuthAPIKey, []ModelInfo{
		{ID: "raw-only", Name: "raw-only", DisplayName: "raw-only"},
	}); err != nil {
		t.Fatalf("CacheModels(openai) error = %v", err)
	}
	if got := store.CachedModelDisplayName("raw-only"); got != "raw-only" {
		t.Fatalf("CachedModelDisplayName(raw-only) = %q, want %q", got, "raw-only")
	}

	// An uncached ID returns "".
	if got := store.CachedModelDisplayName("missing"); got != "" {
		t.Fatalf("CachedModelDisplayName(missing) = %q, want empty", got)
	}
}

// A model can be cached under several provider keys where only some report a
// context window: an aggregator echoes the ID with no limit, while the native
// provider knows the real one. CachedModelLimits must prefer the known window
// over the zero, deterministically, so the status bar never flickers to "--".
func TestStore_CachedModelLimitsPrefersKnownWindow(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	// alibaba echoes the model with no context window; deepseek knows the real one.
	if err := store.CacheModels(Alibaba, AuthAPIKey, []ModelInfo{
		{ID: "deepseek-v4-pro"},
	}); err != nil {
		t.Fatalf("CacheModels(alibaba) error = %v", err)
	}
	if err := store.CacheModels(DeepSeek, AuthAPIKey, []ModelInfo{
		{ID: "deepseek-v4-pro", InputTokenLimit: 1_000_000, OutputTokenLimit: 384_000},
	}); err != nil {
		t.Fatalf("CacheModels(deepseek) error = %v", err)
	}

	// Call many times; with randomized map order an order-dependent
	// implementation would return the zero limit on some iterations.
	for i := range 100 {
		in, out := store.CachedModelLimits("deepseek-v4-pro")
		if in != 1_000_000 || out != 384_000 {
			t.Fatalf("CachedModelLimits() = (%d, %d), want (1000000, 384000) on iteration %d", in, out, i)
		}
	}

	// An uncached ID returns zero limits.
	if in, out := store.CachedModelLimits("missing"); in != 0 || out != 0 {
		t.Fatalf("CachedModelLimits(missing) = (%d, %d), want (0, 0)", in, out)
	}
}

// TestStore_CachedModelLimitsPrefersLargestWindow guards the deterministic
// fallback: when the same ID is cached under two providers that both report a
// non-zero but different window, CachedModelLimits must always return the same
// one (the largest) rather than a random map hit that flickers the status bar.
func TestStore_CachedModelLimitsPrefersLargestWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	if err := store.CacheModels(OpenAI, AuthSubscription, []ModelInfo{
		{ID: "gpt-5.5", InputTokenLimit: 272_000, OutputTokenLimit: 16_384},
	}); err != nil {
		t.Fatalf("CacheModels(subscription) error = %v", err)
	}
	if err := store.CacheModels(OpenAI, AuthAPIKey, []ModelInfo{
		{ID: "gpt-5.5", InputTokenLimit: 400_000, OutputTokenLimit: 16_384},
	}); err != nil {
		t.Fatalf("CacheModels(api_key) error = %v", err)
	}

	for i := range 100 {
		in, out := store.CachedModelLimits("gpt-5.5")
		if in != 400_000 || out != 16_384 {
			t.Fatalf("CachedModelLimits() = (%d, %d), want (400000, 16384) on iteration %d", in, out, i)
		}
	}
}

// TestStore_CachedModelLimitsForProvider verifies the provider-scoped lookup is
// deterministic and ignores TTL: it must return the current provider's own
// window (272k for the subscription) even when an api_key cache advertises a
// different one (400k) and even after the cache has expired — the exact state
// that made the status-bar limit flicker every render once the 24h TTL lapsed.
func TestStore_CachedModelLimitsForProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	if err := store.CacheModels(OpenAI, AuthAPIKey, []ModelInfo{
		{ID: "gpt-5.5", InputTokenLimit: 400_000, OutputTokenLimit: 16_384},
	}); err != nil {
		t.Fatalf("CacheModels(api_key) error = %v", err)
	}
	if err := store.CacheModels(OpenAI, AuthSubscription, []ModelInfo{
		{ID: "gpt-5.5", InputTokenLimit: 272_000, OutputTokenLimit: 16_384},
	}); err != nil {
		t.Fatalf("CacheModels(subscription) error = %v", err)
	}

	// Backdate the subscription cache well past the TTL to reproduce the bug.
	key := providerKey(OpenAI, AuthSubscription)
	cache := store.data.Models[key]
	cache.CachedAt = cache.CachedAt.Add(-2 * modelCacheTTL)
	store.data.Models[key] = cache

	if in, ok := store.GetCachedModels(OpenAI, AuthSubscription); ok {
		t.Fatalf("expected subscription cache to be expired, got %v", in)
	}

	for i := range 100 {
		in, out := store.CachedModelLimitsForProvider(OpenAI, AuthSubscription, "gpt-5.5")
		if in != 272_000 || out != 16_384 {
			t.Fatalf("CachedModelLimitsForProvider() = (%d, %d), want (272000, 16384) on iteration %d", in, out, i)
		}
	}

	// An unknown provider/auth pair reports no window.
	if in, out := store.CachedModelLimitsForProvider(OpenAI, AuthMethod("none"), "gpt-5.5"); in != 0 || out != 0 {
		t.Fatalf("CachedModelLimitsForProvider(none) = (%d, %d), want (0, 0)", in, out)
	}
}
