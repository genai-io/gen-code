package llm

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/secret"
)

// The table of everything San can connect to, filled at init by the vendor
// table. A package-level singleton rather than a value callers pass around,
// because a second one would be a second answer to "which providers exist".

// registry holds what is known about each provider/auth-method pair. The four
// maps are keyed and locked together so a registration is visible whole.
type registry struct {
	mu             sync.RWMutex
	entries        map[string]registryEntry       // key: providerKey
	displays       map[ProviderID]ProviderDisplay // provider-level UI presentation
	costs          map[ProviderID]CostEstimator   // per-provider turn-cost pricing
	authenticators map[string]Authenticator       // key: providerKey; interactive (OAuth) login
}

// registryEntry is one provider/auth-method pair: what it is, and how to open it.
type registryEntry struct {
	meta    Meta
	factory Factory
}

var globalRegistry = &registry{
	entries:        make(map[string]registryEntry),
	displays:       make(map[ProviderID]ProviderDisplay),
	costs:          make(map[ProviderID]CostEstimator),
	authenticators: make(map[string]Authenticator),
}

// providerKey identifies one provider/auth-method pair, as "vendor:auth_method".
//
// It is the form the registry keys entries by, the store keys cached model
// listings by, and a provider reports as its own Name. One function so the
// three can never drift into three slightly different strings.
func providerKey(provider ProviderID, authMethod AuthMethod) string {
	return string(provider) + ":" + string(authMethod)
}

// parseProviderKey splits the identity back apart, for the one direction that
// needs it: a Provider reports its own Name() in this form, while the store
// keys connections by the bare slug. Converting the whole string to a
// ProviderID instead misses every lookup silently.
func parseProviderKey(name string) (ProviderID, AuthMethod) {
	provider, authMethod, _ := strings.Cut(name, ":")
	return ProviderID(provider), AuthMethod(authMethod)
}

// Register records a provider auth method and the factory that opens it.
func Register(meta Meta, factory Factory) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.entries[providerKey(meta.Provider, meta.AuthMethod)] = registryEntry{meta: meta, factory: factory}
}

// RegisterProviderDisplay records a provider's UI presentation (display name
// and order), which is shared by all of its auth methods.
func RegisterProviderDisplay(provider ProviderID, display ProviderDisplay) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.displays[provider] = display
}

// Unregister removes everything registered for a provider/auth-method pair.
//
// All four maps, not just the entry: a test that registers a display or an
// authenticator and unregisters the entry would otherwise leave the provider
// visible to ProvidersByOrder and IsProvider for the rest of the run, and the
// next test to assert on the provider list would see it.
func Unregister(provider ProviderID, authMethod AuthMethod) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()

	key := providerKey(provider, authMethod)
	delete(globalRegistry.entries, key)
	delete(globalRegistry.authenticators, key)

	// The display and the pricing are per provider, shared by its auth
	// methods, so they go only once the last one does.
	for _, entry := range globalRegistry.entries {
		if entry.meta.Provider == provider {
			return
		}
	}
	delete(globalRegistry.displays, provider)
	delete(globalRegistry.costs, provider)
}

// GetProvider opens a connection to a registered provider auth method.
func GetProvider(ctx context.Context, provider ProviderID, authMethod AuthMethod) (Provider, error) {
	globalRegistry.mu.RLock()
	entry, ok := globalRegistry.entries[providerKey(provider, authMethod)]
	globalRegistry.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("provider not registered: %s:%s", provider, authMethod)
	}
	return entry.factory(ctx)
}

// GetMeta returns the metadata registered for a provider auth method.
func GetMeta(provider ProviderID, authMethod AuthMethod) (Meta, bool) {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	entry, ok := globalRegistry.entries[providerKey(provider, authMethod)]
	return entry.meta, ok
}

// IsProvider reports whether name is a registered provider vendor. It is the
// source of truth for telling a "vendor/model" routing ref apart from a bare
// model id that merely contains a slash.
func IsProvider(name ProviderID) bool {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	_, ok := globalRegistry.displays[name]
	return ok
}

// ParseVendorModel reads the explicit "vendor/model" routing form (e.g.
// "deepseek/deepseek-v4"). A ref is vendor-qualified only when the part before
// the first slash is a registered provider name; a bare model id that merely
// contains a slash (e.g. mimo's "xiaomi/mimo-v2-flash") is not, so it reports
// ok=false and the caller keeps the ref on the current provider. A known but
// unconnected vendor still parses here; resolving it is the caller's job.
func ParseVendorModel(ref string) (vendor ProviderID, model string, ok bool) {
	v, m, found := strings.Cut(ref, "/")
	if !found || v == "" || m == "" || !IsProvider(ProviderID(v)) {
		return "", "", false
	}
	return ProviderID(v), m, true
}

// ProviderDisplayName returns a provider's human-readable name, falling back
// to its identifier when none was registered.
func ProviderDisplayName(provider ProviderID) string {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	if display, ok := globalRegistry.displays[provider]; ok {
		return display.Name
	}
	return string(provider)
}

// ProvidersByOrder returns every registered provider, in display order.
func ProvidersByOrder() []ProviderID {
	globalRegistry.mu.RLock()
	displays := maps.Clone(globalRegistry.displays)
	globalRegistry.mu.RUnlock()

	names := slices.Collect(maps.Keys(displays))
	slices.SortFunc(names, func(a, b ProviderID) int {
		if order := displays[a].Order - displays[b].Order; order != 0 {
			return order
		}
		// Equal orders would otherwise come back in map order, which Go
		// randomizes — the picker would reshuffle between renders.
		return strings.Compare(string(a), string(b))
	})
	return names
}

// Status is how far a provider auth method is from being usable.
type Status string

const (
	StatusConnected     Status = "connected"
	StatusAvailable     Status = "available"
	StatusNotConfigured Status = "not_configured"
)

// AuthMethodStatus is one registered auth method with its current status.
type AuthMethodStatus struct {
	Meta   Meta
	Status Status
}

// GetProvidersWithStatus reports every registered auth method, grouped by
// provider, with how far each is from being usable.
func GetProvidersWithStatus(store *Store) map[ProviderID][]AuthMethodStatus {
	// Snapshot under the lock and classify outside it: IsReady reaches into
	// the secret store and the authenticators, and holding a read lock across
	// that would block every registration behind it.
	globalRegistry.mu.RLock()
	entries := slices.Collect(maps.Values(globalRegistry.entries))
	globalRegistry.mu.RUnlock()

	result := make(map[ProviderID][]AuthMethodStatus, len(entries))
	for _, entry := range entries {
		status := StatusNotConfigured
		switch {
		case store.IsConnected(entry.meta.Provider, entry.meta.AuthMethod):
			status = StatusConnected
		case IsReady(entry.meta):
			status = StatusAvailable
		}
		result[entry.meta.Provider] = append(result[entry.meta.Provider], AuthMethodStatus{Meta: entry.meta, Status: status})
	}
	return result
}

// IsReady reports whether an auth method's credentials are in place, so it can
// be shown as available to connect.
func IsReady(meta Meta) bool {
	// No env vars means the credential comes from an interactive sign-in (e.g.
	// a ChatGPT subscription). Without stored credentials it is not ready —
	// treat it as "not configured" rather than showing a misleading
	// "Available".
	if len(meta.EnvVars) == 0 {
		return HasInteractiveCredentials(meta.Provider, meta.AuthMethod)
	}
	for _, envVar := range meta.EnvVars {
		if secret.Resolve(envVar) == "" {
			return false
		}
	}
	return true
}
