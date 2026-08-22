// Package llm holds the connection to the active LLM provider, the registry of
// available providers/models, and the adapter that reaches every vendor
// through github.com/genai-io/sdk-go — see vendor.go. Default() returns the
// package-level *Conn — the mutable provider/model/store handle, guarded by a
// single mutex.
package llm

import (
	"context"
	"sync"
)

// Conn is the handle to the active LLM: the connected Provider, the current
// model, and the Store of available providers/models. Every accessor is
// mutex-protected; the fields are unexported so all access goes through the
// locked methods. Callers obtain the package-level singleton via Default().
type Conn struct {
	mu           sync.RWMutex
	store        *Store
	provider     Provider
	currentModel *CurrentModelInfo
}

// defaultConn is the package-level singleton, populated by Initialize().
var defaultConn = &Conn{}

// Options holds configuration for Initialize.
type Options struct{}

// Initialize discovers and connects to the best available LLM provider, then
// records the provider/model/store on the package-level *Conn.
func Initialize(opts Options) {
	store, _ := NewStore()
	if store == nil {
		return
	}

	defaultConn.mu.Lock()
	defaultConn.store = store
	defaultConn.currentModel = store.GetCurrentModel()
	defaultConn.mu.Unlock()

	if resolved, ok := ResolveProvider(context.Background(), store); ok {
		defaultConn.SetProvider(resolved.Provider)
	}
}

// ResolvedProvider is a connected provider plus the identity used to reach it.
// ModelID carries the saved current model when one is set, and is empty when
// resolution fell back to a connection without a saved model — in that case the
// caller picks a model (see setting.DefaultModel).
type ResolvedProvider struct {
	Provider   Provider
	ModelID    string
	AuthMethod AuthMethod
}

// ResolveProvider connects to the best available provider recorded in the
// store: the saved current model's provider first, then any other connected
// provider. It reports ok=false when no provider can be connected. This is the
// single resolution order shared by Initialize (interactive startup) and the
// one-shot print / headless entry points, so they can never drift apart.
func ResolveProvider(ctx context.Context, store *Store) (ResolvedProvider, bool) {
	if store == nil {
		return ResolvedProvider{}, false
	}
	if cm := store.GetCurrentModel(); cm != nil {
		if p, err := GetProvider(ctx, cm.Provider, cm.AuthMethod); err == nil {
			return ResolvedProvider{Provider: p, ModelID: cm.ModelID, AuthMethod: cm.AuthMethod}, true
		}
	}
	for providerName, conn := range store.GetConnections() {
		if p, err := GetProvider(ctx, Name(providerName), conn.AuthMethod); err == nil {
			return ResolvedProvider{Provider: p, AuthMethod: conn.AuthMethod}, true
		}
	}
	return ResolvedProvider{}, false
}

// Default returns the package-level *Conn.
func Default() *Conn { return defaultConn }

// SetDefaultConn replaces the package-level *Conn. Intended for tests. A nil
// argument restores a fresh empty *Conn.
func SetDefaultConn(c *Conn) {
	if c == nil {
		defaultConn = &Conn{}
		return
	}
	defaultConn = c
}

// ResetDefaultConn restores a fresh empty *Conn. Intended for tests.
func ResetDefaultConn() { defaultConn = &Conn{} }

// --- accessors (mutex-protected) ---

func (c *Conn) Provider() Provider {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.provider
}

func (c *Conn) SetProvider(p Provider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.provider = p
}

// ModelID returns the current model ID, or empty string if none.
func (c *Conn) ModelID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.currentModel != nil {
		return c.currentModel.ModelID
	}
	return ""
}

func (c *Conn) CurrentModel() *CurrentModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.currentModel
}

func (c *Conn) SetCurrentModel(info *CurrentModelInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentModel = info
}

func (c *Conn) Store() *Store {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.store
}

// NewClient builds a one-shot *Client for the active provider.
func (c *Conn) NewClient(model string, maxTokens int) *Client {
	return NewClient(c.Provider(), model, maxTokens)
}

// ListProviders reports every known provider with its connection status.
func (c *Conn) ListProviders() map[Name][]Info {
	return GetProvidersWithStatus(c.Store())
}
