package llm

import (
	"os"
	"strconv"
	"time"
)

// What San remembers about models.
//
// Two things live here. The first is the cache: listing a provider's models is
// a network round-trip, and the picker, the status bar and the compaction
// check all need the answer, so it is kept in the store keyed by
// provider:auth. The second is what San concludes from it — a model's display
// name, and above all its context window.
//
// The window is the denominator of every "how full is the context" question
// San asks: the status bar's percentage and the agent's auto-compaction
// trigger. Both must get the same answer, so both resolve it through
// EffectiveInputLimit. Issue #338 was the display and the trigger disagreeing;
// one resolver is what stops that from recurring.

// modelCacheTTL is how long a provider's listing is trusted before it is
// fetched again.
const modelCacheTTL = 24 * time.Hour

// modelCache is one provider's listing, as of when it was fetched.
type modelCache struct {
	CachedAt time.Time   `json:"cachedAt"`
	Models   []ModelInfo `json:"models"`
}

// tokenLimitOverride is the window and output cap the user set by hand for a
// model, via /tokenlimit. It outranks anything a provider published.
type tokenLimitOverride struct {
	InputTokenLimit  int `json:"inputTokenLimit"`
	OutputTokenLimit int `json:"outputTokenLimit"`
}

// ---------------------------------------------------------------------------
// The cached listings
// ---------------------------------------------------------------------------

// CacheModels saves model information for a provider.
func (s *Store) CacheModels(provider ProviderID, authMethod AuthMethod, models []ModelInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := providerKey(provider, authMethod)
	s.data.Models[key] = modelCache{
		CachedAt: time.Now(),
		Models:   models,
	}

	return s.save()
}

// GetCachedModels returns cached models if they exist and are not expired
func (s *Store) GetCachedModels(provider ProviderID, authMethod AuthMethod) ([]ModelInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cache, ok := s.data.Models[providerKey(provider, authMethod)]
	if !ok {
		return nil, false
	}
	if time.Since(cache.CachedAt) > modelCacheTTL {
		return nil, false
	}

	return cache.Models, true
}

// CachedModelsByProvider returns every cached listing, keyed by
// provider:auth_method.
//
// Fresh entries win; the expired ones are handed back only when nothing is
// fresh, which is what lets the picker draw immediately from a stale cache
// instead of blocking on a refresh. Both halves are gathered in one locked
// pass, so the two answers cannot come from two different moments — and an
// empty listing counts as nothing either way, whatever its age.
func (s *Store) CachedModelsByProvider() map[string][]ModelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fresh := make(map[string][]ModelInfo)
	stale := make(map[string][]ModelInfo)
	for key, cache := range s.data.Models {
		if len(cache.Models) == 0 {
			continue
		}
		if time.Since(cache.CachedAt) > modelCacheTTL {
			stale[key] = cache.Models
		} else {
			fresh[key] = cache.Models
		}
	}
	if len(fresh) > 0 {
		return fresh
	}
	return stale
}

// RemoveCachedModels removes cached models for a provider and auth method.
func (s *Store) RemoveCachedModels(provider ProviderID, authMethod AuthMethod) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.data.Models, providerKey(provider, authMethod))
	return s.save()
}

// ---------------------------------------------------------------------------
// What the listings say about one model
// ---------------------------------------------------------------------------

// CachedModelDisplayName returns the display name for a model ID found in any
// cached provider list, ignoring TTL. Returns "" if the ID isn't cached.
//
// The same model can be cached under several provider/auth keys (e.g. a model
// offered both directly and via an aggregator). One provider may list a real
// display name ("DeepSeek V4 Pro") while another only echoes the raw ID
// ("deepseek-v4-pro"). Returning whichever entry we hit first would make the
// status bar flicker between the two, because Go randomizes map iteration
// order between renders. So we prefer a real display name — one that differs
// from the ID — and only fall back to the raw name/ID when no real name
// exists. Scans in place without allocating, since it runs on every render.
func (s *Store) CachedModelDisplayName(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	raw := "" // the raw ID echoed back as a name; used only if no real name is found
	for _, cache := range s.data.Models {
		for _, m := range cache.Models {
			if m.ID != id {
				continue
			}
			name := m.DisplayName
			if name == "" {
				name = m.Name
			}
			if name != "" && name != id {
				return name // a real, human-readable display name
			}
			raw = name // keep scanning in case another provider has a real name
		}
	}
	return raw
}

// cachedModel returns one model's cached record from a single provider/auth
// listing, ignoring TTL.
//
// Provider-scoped on purpose: the same model ID can be served by several auth
// methods with different windows and different capabilities (gpt-5.5 at 400k
// via the API, 272k via the ChatGPT subscription), so a lookup that scanned
// every cache could answer differently between two renders. TTL is ignored
// equally on purpose — a context window rarely changes, and a stale-but-real
// figure beats falling back to a cross-provider guess once the cache expires.
func (s *Store) cachedModel(provider ProviderID, authMethod AuthMethod, id string, match func(ModelInfo) bool) (ModelInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// A missing key yields the zero modelCache, whose nil Models ranges zero
	// times — no separate absence check needed.
	for _, m := range s.data.Models[providerKey(provider, authMethod)].Models {
		if m.ID == id && match(m) {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// CachedModelLimitsForProvider returns a model's token limits from one
// provider's listing, or (0, 0) when it states no window.
func (s *Store) CachedModelLimitsForProvider(provider ProviderID, authMethod AuthMethod, id string) (inputLimit, outputLimit int) {
	m, ok := s.cachedModel(provider, authMethod, id, func(m ModelInfo) bool { return m.InputTokenLimit > 0 })
	if !ok {
		return 0, 0
	}
	return m.InputTokenLimit, m.OutputTokenLimit
}

// CachedModelLimits returns the token limits for a model ID found in any
// cached provider list, ignoring TTL. Returns (0, 0) when no cached entry
// reports a context window for the ID.
//
// The companion to CachedModelDisplayName, and for the same reason: the same
// model can be cached under several provider/auth keys, and only some report a
// context window. An OpenAI-compatible aggregator often echoes the raw model ID
// with no context length (limit 0), while the model's native provider knows the
// real window (e.g. DeepSeek V4 Pro at 1M). Resolving the limit from only the
// current provider's cache would then render "--" even though another cache
// knows the answer. So we scan all caches for the ID. When several report a
// non-zero window we keep the largest, both because it best reflects the
// model's real capability and because a fixed choice is deterministic — Go
// randomizes map iteration order, so returning the first hit would flicker the
// status bar between providers. Scans in place without allocating, since it
// feeds the status bar on every render.
func (s *Store) CachedModelLimits(id string) (inputLimit, outputLimit int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, cache := range s.data.Models {
		for _, m := range cache.Models {
			if m.ID == id && m.InputTokenLimit > inputLimit {
				inputLimit, outputLimit = m.InputTokenLimit, m.OutputTokenLimit
			}
		}
	}
	return inputLimit, outputLimit
}

// CachedModelReasoningForProvider returns a model's reasoning ladder from one
// provider's listing. The capability was normalized by NewReasoningCapability
// at write time, so it is handed back as-is rather than re-normalized on every
// (hot-path) lookup; callers treat it as read-only.
func (s *Store) CachedModelReasoningForProvider(provider ProviderID, authMethod AuthMethod, id string) (*ReasoningCapability, bool) {
	m, ok := s.cachedModel(provider, authMethod, id, func(m ModelInfo) bool { return m.Reasoning != nil })
	if !ok {
		return nil, false
	}
	return m.Reasoning, true
}

// ---------------------------------------------------------------------------
// The context window
// ---------------------------------------------------------------------------

// SetTokenLimit sets custom token limits for a model.
// It also updates the model cache so subsequent model listings reflect these limits.
func (s *Store) SetTokenLimit(modelID string, inputLimit, outputLimit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initMaps()
	s.data.TokenLimits[modelID] = tokenLimitOverride{
		InputTokenLimit:  inputLimit,
		OutputTokenLimit: outputLimit,
	}

	// Update the model cache entry so model listings show the limits.
	// We copy the slice before modifying to avoid mutating arrays shared with
	// callers that received a slice from GetCachedModels.
	for key, cache := range s.data.Models {
		modified := false
		for _, m := range cache.Models {
			if m.ID == modelID {
				modified = true
				break
			}
		}
		if !modified {
			continue
		}
		newModels := make([]ModelInfo, len(cache.Models))
		copy(newModels, cache.Models)
		for i := range newModels {
			if newModels[i].ID == modelID {
				newModels[i].InputTokenLimit = inputLimit
				newModels[i].OutputTokenLimit = outputLimit
			}
		}
		cache.Models = newModels
		s.data.Models[key] = cache
	}

	return s.save()
}

// GetTokenLimit returns custom token limits for a model
func (s *Store) GetTokenLimit(modelID string) (inputLimit, outputLimit int, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	override, exists := s.data.TokenLimits[modelID]
	if !exists {
		return 0, 0, false
	}
	return override.InputTokenLimit, override.OutputTokenLimit, true
}

// InputLimitEnvVar sets the window for a model San cannot size on its own,
// e.g. an aggregator serving a model without publishing its limits. There is
// deliberately no default to stand in for it: a guessed window is acted on
// silently, and guessing low costs real context on every compaction while
// guessing high never fires at all. An unknown window resolves to 0, which
// skips proactive compaction and leaves the prompt-too-long retry
// (isPromptTooLong) to recover — one wasted request, no invented number, and
// the status bar honestly reads "--" instead of a percentage of a guess.
const InputLimitEnvVar = "SAN_INPUT_LIMIT"

// inputLimitOverride returns the window forced by InputLimitEnvVar, or 0 when
// unset or not a positive integer.
func inputLimitOverride() int {
	n, err := strconv.Atoi(os.Getenv(InputLimitEnvVar))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// EffectiveInputLimit resolves a model's context window from configuration and
// cache, returning 0 when it cannot be determined. Callers treat 0 as
// "unknown" and skip whatever they would have done with a window rather than
// substituting a guess.
//
// Order: the env override, then the user's configured limit, then this
// provider+auth's cached figure, then the largest figure cached for the ID
// under any provider (an aggregator may serve a model without publishing its
// window while the native provider knows it).
//
// auth disambiguates a model ID cached under several auth methods with
// different windows (gpt-5.5: 400k via the API, 272k via a subscription).
func (s *Store) EffectiveInputLimit(provider ProviderID, auth AuthMethod, modelID string) int {
	if n := inputLimitOverride(); n > 0 {
		return n
	}
	if s == nil || modelID == "" {
		return 0
	}
	if in, _, ok := s.GetTokenLimit(modelID); ok && in > 0 {
		return in
	}
	if in, _ := s.CachedModelLimitsForProvider(provider, auth, modelID); in > 0 {
		return in
	}
	in, _ := s.CachedModelLimits(modelID)
	return in
}
