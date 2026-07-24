package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/confdir"
)

const (
	// modelCacheTTL is the time-to-live for cached models
	modelCacheTTL = 24 * time.Hour
)

// ConnectionInfo stores connection information for a provider
type ConnectionInfo struct {
	AuthMethod  AuthMethod `json:"authMethod"`
	ConnectedAt time.Time  `json:"connectedAt"`
}

// modelCache stores cached model information
type modelCache struct {
	CachedAt time.Time   `json:"cachedAt"`
	Models   []ModelInfo `json:"models"`
}

// CurrentModelInfo stores the current model with its provider info
type CurrentModelInfo struct {
	ModelID    string     `json:"modelId"`
	Provider   Name       `json:"provider"`
	AuthMethod AuthMethod `json:"authMethod"`
}

// tokenLimitOverride stores custom token limits for a model
type tokenLimitOverride struct {
	InputTokenLimit  int `json:"inputTokenLimit"`
	OutputTokenLimit int `json:"outputTokenLimit"`
}

// CustomProviderConfig stores the user-defined OpenAI-compatible provider added
// via the /models Providers tab. The API key is not kept here — it lives in the
// secret store under the provider's env var.
type CustomProviderConfig struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseURL"`
}

// storeData is the persisted data structure
type storeData struct {
	Connections     map[string]ConnectionInfo     `json:"connections"`               // key: provider
	Models          map[string]modelCache         `json:"models"`                    // key: provider:authMethod
	Current         *CurrentModelInfo             `json:"current"`                   // current model with provider info
	SearchProvider  *string                       `json:"searchProvider,omitempty"`  // search provider name (exa, serper, brave)
	TokenLimits     map[string]tokenLimitOverride `json:"tokenLimits,omitempty"`     // key: modelID
	ThinkingEfforts map[string]string             `json:"thinkingEfforts,omitempty"` // key: modelID; value: provider-native effort label
	CustomProvider  *CustomProviderConfig         `json:"customProvider,omitempty"`  // user-defined OpenAI-compatible provider
}

// Store manages provider configuration persistence
type Store struct {
	mu   sync.RWMutex
	path string
	data storeData
}

// NewStore creates a new Store instance
func NewStore() (*Store, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configDir := confdir.Dir(homeDir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, err
	}

	store := &Store{
		path: filepath.Join(configDir, "providers.json"),
		data: storeData{
			Connections: make(map[string]ConnectionInfo),
			Models:      make(map[string]modelCache),
		},
	}

	// Load existing data if available
	if err := store.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	return store, nil
}

// load reads the store data from disk
func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read provider store %s: %w", s.path, err)
	}

	if err := json.Unmarshal(data, &s.data); err != nil {
		return fmt.Errorf("parse provider store: %w", err)
	}

	// Initialize maps if nil
	s.ensureMapsInitialized()
	return nil
}

// Reload re-reads the store from disk, refreshing this instance's in-memory
// caches with data written by another Store instance.
//
// The provider selector operates on its own Store (a separate NewStore), so the
// model metadata it caches — display names and context-window limits — and the
// current-model choice it persists land on disk but not in the shared app-level
// Store the status bar reads. Reloading after a model switch lets the status bar
// pick up the new model's name and limit instead of falling back to the raw ID
// and "--". A missing file is not an error: nothing has been persisted yet.
func (s *Store) Reload() error {
	if err := s.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ensureMapsInitialized ensures all map fields are non-nil
func (s *Store) ensureMapsInitialized() {
	if s.data.Connections == nil {
		s.data.Connections = make(map[string]ConnectionInfo)
	}
	if s.data.Models == nil {
		s.data.Models = make(map[string]modelCache)
	}
	if s.data.TokenLimits == nil {
		s.data.TokenLimits = make(map[string]tokenLimitOverride)
	}
	if s.data.ThinkingEfforts == nil {
		s.data.ThinkingEfforts = make(map[string]string)
	}
}

// save writes the store data to disk
func (s *Store) save() error {
	return atomicfile.WriteJSON(s.path, s.data, 0o644)
}

// Connect saves a connection for a provider
func (s *Store) Connect(provider Name, authMethod AuthMethod) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Connections[string(provider)] = ConnectionInfo{
		AuthMethod:  authMethod,
		ConnectedAt: time.Now(),
	}

	return s.save()
}

// IsConnected checks if a provider is connected with the specified auth method
func (s *Store) IsConnected(provider Name, authMethod AuthMethod) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	conn, ok := s.data.Connections[string(provider)]
	if !ok {
		return false
	}
	return conn.AuthMethod == authMethod
}

// GetConnection returns the connection info for a provider
func (s *Store) GetConnection(provider Name) (ConnectionInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	conn, ok := s.data.Connections[string(provider)]
	return conn, ok
}

// ResolveAuthMethod returns the model's own auth method, falling back to its
// provider's stored connection when the model doesn't carry one. Provider-scoped
// cache lookups (token limits, reasoning) key on provider+auth, so they resolve
// the auth this way to avoid missing the cache for a model selected without an
// explicit method.
func (s *Store) ResolveAuthMethod(current *CurrentModelInfo) AuthMethod {
	if current == nil {
		return ""
	}
	if current.AuthMethod != "" {
		return current.AuthMethod
	}
	return s.ConnectionAuthMethod(current.Provider)
}

// ConnectionAuthMethod returns the auth method of a provider's active
// connection, or "" when it has none. Nil-receiver safe, for callers that hold
// a provider but no model (llm.Client resolving its own context window).
func (s *Store) ConnectionAuthMethod(provider Name) AuthMethod {
	if s == nil {
		return ""
	}
	if conn, ok := s.GetConnection(provider); ok {
		return conn.AuthMethod
	}
	return ""
}

// GetConnections returns all connections
func (s *Store) GetConnections() map[string]ConnectionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]ConnectionInfo, len(s.data.Connections))
	maps.Copy(result, s.data.Connections)
	return result
}

// Disconnect removes the connection for a provider.
func (s *Store) Disconnect(provider Name) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.data.Connections, string(provider))
	return s.save()
}

// RemoveCachedModels removes cached models for a provider and auth method.
func (s *Store) RemoveCachedModels(provider Name, authMethod AuthMethod) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.data.Models, makemodelCacheKey(provider, authMethod))
	return s.save()
}

// ClearCurrentModel clears the current model selection.
func (s *Store) ClearCurrentModel() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Current = nil
	return s.save()
}

// CacheModels saves model information for a provider.
func (s *Store) CacheModels(provider Name, authMethod AuthMethod, models []ModelInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := makemodelCacheKey(provider, authMethod)
	s.data.Models[key] = modelCache{
		CachedAt: time.Now(),
		Models:   models,
	}

	return s.save()
}

// GetCachedModels returns cached models if they exist and are not expired
func (s *Store) GetCachedModels(provider Name, authMethod AuthMethod) ([]ModelInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cache, ok := s.data.Models[makemodelCacheKey(provider, authMethod)]
	if !ok {
		return nil, false
	}
	if time.Since(cache.CachedAt) > modelCacheTTL {
		return nil, false
	}

	return cache.Models, true
}

// makemodelCacheKey creates a cache key for provider and auth method
func makemodelCacheKey(provider Name, authMethod AuthMethod) string {
	return string(provider) + ":" + string(authMethod)
}

// GetAllCachedModels returns all cached models grouped by provider key
func (s *Store) GetAllCachedModels() map[string][]ModelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]ModelInfo)
	for key, cache := range s.data.Models {
		if time.Since(cache.CachedAt) > modelCacheTTL {
			continue
		}
		result[key] = cache.Models
	}
	return result
}

// GetAllCachedModelsIncludeExpired returns all cached models regardless of TTL.
// Used to show stale data immediately rather than blocking the UI.
func (s *Store) GetAllCachedModelsIncludeExpired() map[string][]ModelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]ModelInfo)
	for key, cache := range s.data.Models {
		if len(cache.Models) > 0 {
			result[key] = cache.Models
		}
	}
	return result
}

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

// CachedModelLimitsForProvider returns the token limits for a model ID from a
// single provider/auth cache, ignoring TTL. Returns (0, 0) when that cache has
// no entry reporting a context window for the ID.
//
// Unlike CachedModelLimits it reads only the one cache keyed by provider+auth,
// so it is deterministic: it can never flicker between two providers that
// advertise different windows for the same ID (e.g. gpt-5.5 at 400k via Direct
// API and 272k via the ChatGPT subscription). It ignores TTL on purpose — the
// status bar wants the current model's own window even from a stale cache,
// since context windows rarely change and a stale-but-correct value beats
// falling back to a random cross-provider one once the cache expires.
func (s *Store) CachedModelLimitsForProvider(provider Name, authMethod AuthMethod, id string) (inputLimit, outputLimit int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cache, ok := s.data.Models[makemodelCacheKey(provider, authMethod)]
	if !ok {
		return 0, 0
	}
	for _, m := range cache.Models {
		if m.ID == id && m.InputTokenLimit > 0 {
			return m.InputTokenLimit, m.OutputTokenLimit
		}
	}
	return 0, 0
}

// CachedModelReasoningForProvider returns normalized reasoning metadata for one
// model from a single provider/auth cache, ignoring TTL. Like token limits, the
// same model ID can expose different capabilities through different auth paths,
// so this lookup must remain provider-scoped and deterministic.
func (s *Store) CachedModelReasoningForProvider(provider Name, authMethod AuthMethod, id string) (*ReasoningCapability, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cache, ok := s.data.Models[makemodelCacheKey(provider, authMethod)]
	if !ok {
		return nil, false
	}
	for _, model := range cache.Models {
		// The cached capability was already normalized by NewReasoningCapability
		// at write time, so hand it back as-is rather than re-normalizing on every
		// (hot-path) lookup. Callers treat it as read-only.
		if model.ID == id && model.Reasoning != nil {
			return model.Reasoning, true
		}
	}
	return nil, false
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

// SetCurrentModel sets the current model with provider info
func (s *Store) SetCurrentModel(modelID string, provider Name, authMethod AuthMethod) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Current = &CurrentModelInfo{
		ModelID:    modelID,
		Provider:   provider,
		AuthMethod: authMethod,
	}
	return s.save()
}

// GetCurrentModel returns the current model info
func (s *Store) GetCurrentModel() *CurrentModelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.data.Current
}

// GetSearchProvider returns the current search provider name
func (s *Store) GetSearchProvider() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.data.SearchProvider == nil {
		return "" // Will use default (exa)
	}
	return *s.data.SearchProvider
}

// SetSearchProvider sets the search provider
func (s *Store) SetSearchProvider(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.SearchProvider = &name
	return s.save()
}

// SetTokenLimit sets custom token limits for a model.
// It also updates the model cache so subsequent model listings reflect these limits.
func (s *Store) SetTokenLimit(modelID string, inputLimit, outputLimit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureMapsInitialized()
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

// GetThinkingEffort returns the persisted thinking effort for modelID,
// or "" when no preference has been saved (fall back to provider default).
func (s *Store) GetThinkingEffort(modelID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.ThinkingEfforts[modelID]
}

// SetThinkingEffort saves the thinking effort for modelID.
// Passing "" deletes the entry so future loads fall back to the provider default.
func (s *Store) SetThinkingEffort(modelID, effort string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureMapsInitialized()
	if effort == "" {
		delete(s.data.ThinkingEfforts, modelID)
	} else {
		s.data.ThinkingEfforts[modelID] = effort
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

// CustomProvider returns the stored custom provider config, or nil when the
// user hasn't defined one. Returns a copy so callers can't mutate the store.
func (s *Store) CustomProvider() *CustomProviderConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.data.CustomProvider == nil {
		return nil
	}
	cfg := *s.data.CustomProvider
	return &cfg
}

// SetCustomProvider saves the custom provider config.
func (s *Store) SetCustomProvider(cfg CustomProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.CustomProvider = &cfg
	return s.save()
}
