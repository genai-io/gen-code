// Package llm is San's connection to a language model: the registry of every
// provider it can reach, the store of what the user connected and chose, the
// Client the agent loop infers through, and the adapter that serves every
// vendor over github.com/genai-io/sdk-go (see vendor.go).
//
// Default() returns the package-level *Conn — the active provider, model and
// store, behind one mutex.
package llm

import (
	"context"
	"fmt"
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

// Default returns the package-level *Conn.
func Default() *Conn { return defaultConn }

// Initialize discovers and connects to the best available LLM provider, then
// records the provider/model/store on the package-level *Conn.
func Initialize() {
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

// ---------------------------------------------------------------------------
// Accessors (mutex-protected)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Opening a connection
// ---------------------------------------------------------------------------

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
	if current := store.GetCurrentModel(); current != nil {
		if p, err := GetProvider(ctx, current.Provider, current.AuthMethod); err == nil {
			return ResolvedProvider{Provider: p, ModelID: current.ModelID, AuthMethod: current.AuthMethod}, true
		}
	}
	for provider, conn := range store.GetConnections() {
		if p, err := GetProvider(ctx, ProviderID(provider), conn.AuthMethod); err == nil {
			return ResolvedProvider{Provider: p, AuthMethod: conn.AuthMethod}, true
		}
	}
	return ResolvedProvider{}, false
}

// ProviderPool hands out a live Provider for a connected vendor, opening each
// vendor's connection once and reusing it. A session that routes work across
// vendors — say a planner on Anthropic and a coder on DeepSeek — shares one
// pool so every subagent on the same vendor talks through the same client.
type ProviderPool struct {
	store *Store
	mu    sync.Mutex
	byKey map[string]Provider // providerKey(vendor, authMethod)
}

// NewProviderPool returns a pool backed by the given connection store.
func NewProviderPool(store *Store) *ProviderPool {
	return &ProviderPool{store: store, byKey: make(map[string]Provider)}
}

// Resolve returns the provider for a connected vendor (e.g. "deepseek").
//
// The auth method comes from how the user connected that vendor, so a model
// family served via Vertex or Bedrock resolves like a direct API key — the
// "vendor/model" routing form names the vendor, never the serving platform.
// Resolution fails when the vendor is not connected; it never falls back to a
// different vendor.
func (p *ProviderPool) Resolve(ctx context.Context, vendor ProviderID) (Provider, error) {
	if p == nil || p.store == nil {
		return nil, fmt.Errorf("provider pool is not configured")
	}

	conn, ok := p.store.GetConnection(vendor)
	if !ok {
		return nil, fmt.Errorf("provider %q is not connected", vendor)
	}
	key := providerKey(vendor, conn.AuthMethod)

	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.byKey[key]; ok {
		return existing, nil
	}
	provider, err := GetProvider(ctx, vendor, conn.AuthMethod)
	if err != nil {
		return nil, err
	}
	p.byKey[key] = provider
	return provider, nil
}
