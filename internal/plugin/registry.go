package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/confdir"
)

// Registry manages all loaded plugins.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]*Plugin // key: plugin full name (e.g., "git@marketplace")
	cwd     string             // Current working directory
	loaded  bool               // Whether plugins have been loaded
}

// NewRegistry creates a new plugin registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins: make(map[string]*Plugin),
	}
}

// Load loads all plugins from user, project, and local scopes.
func (r *Registry) Load(ctx context.Context, cwd string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cwd = cwd
	r.plugins = make(map[string]*Plugin)

	// Load enabled plugins from settings
	enabledPlugins, err := r.loadEnabledPlugins(cwd)
	if err != nil {
		// Non-fatal error, continue with empty enabled list
		enabledPlugins = make(map[string]bool)
	}

	// Load plugins from each scope in deterministic precedence order.
	// Later scopes override earlier ones when the same plugin key exists.
	dirsByScope := GetPluginDirs(cwd)
	for _, scope := range []Scope{ScopeUser, ScopeProject, ScopeLocal} {
		dirs := dirsByScope[scope]
		for _, dir := range dirs {
			plugins, err := LoadPluginsFromDir(dir, scope, "")
			if err != nil {
				continue
			}
			for _, p := range plugins {
				key := p.FullName()
				// Check if enabled
				if enabled, ok := enabledPlugins[key]; ok {
					p.Enabled = enabled
				} else {
					// Default: user plugins enabled, others disabled
					p.Enabled = scope == ScopeUser
				}
				r.plugins[key] = p
			}
		}
	}

	// Load from installed_plugins.json files
	for _, scope := range []Scope{ScopeUser, ScopeProject, ScopeLocal} {
		installedFile := GetInstalledPluginsFile(cwd, scope)
		plugins, err := LoadInstalledPlugins(installedFile, scope)
		if err != nil {
			continue
		}
		for _, p := range plugins {
			// A project/local install copies the plugin into a scope dir, so the
			// scope scan above already loaded it under a bare name. Drop that
			// duplicate — this entry carries the real "name@marketplace" source.
			for k, existing := range r.plugins {
				if existing.Path == p.Path {
					delete(r.plugins, k)
				}
			}
			key := p.FullName()
			if enabled, ok := enabledPlugins[key]; ok {
				p.Enabled = enabled
			} else {
				p.Enabled = true // Installed plugins default to enabled
			}
			r.plugins[key] = p
		}
	}

	r.loaded = true
	return nil
}

// loadEnabledPlugins loads enabled plugin settings from all config sources.
func (r *Registry) loadEnabledPlugins(cwd string) (map[string]bool, error) {
	result := make(map[string]bool)
	homeDir, _ := os.UserHomeDir()

	// Load from each settings file in priority order
	settingsFiles := []string{
		filepath.Join(homeDir, ".claude", "settings.json"),
		filepath.Join(confdir.Dir(homeDir), "settings.json"),
		filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(confdir.Dir(cwd), "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
		filepath.Join(confdir.Dir(cwd), "settings.local.json"),
	}

	for _, f := range settingsFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var settings struct {
			EnabledPlugins map[string]bool `json:"enabledPlugins"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			continue
		}
		for k, v := range settings.EnabledPlugins {
			result[k] = v
		}
	}

	return result, nil
}

// snapshotLocked copies a plugin for a caller to read outside the lock.
//
// Enable/Disable write p.Enabled under r.mu, and they run on a tea.Cmd
// goroutine during /plugin install — after a git clone that can take the full
// 120s timeout. Handing out the live pointer let the UI read that field while
// it was being written. The Components slices and maps are shared: they are
// built once at load time and never mutated after.
func snapshotLocked(p *Plugin) *Plugin {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// SetCwd re-points the registry at a working directory. r.cwd is written
// under r.mu by Load and read under it by saveEnabledState, so a caller
// outside the package cannot assign it directly.
func (r *Registry) SetCwd(cwd string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cwd = cwd
}

// Get returns a plugin by name.
func (r *Registry) Get(name string) (*Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Try exact match first
	if p, ok := r.plugins[name]; ok {
		return snapshotLocked(p), true
	}

	// Try partial match (name without marketplace)
	for key, p := range r.plugins {
		if p.Name() == name {
			return snapshotLocked(p), true
		}
		// Match the prefix before @
		if idx := len(name); idx < len(key) && key[:idx] == name && key[idx] == '@' {
			return snapshotLocked(p), true
		}
	}

	return nil, false
}

// List returns all plugins sorted by name.
func (r *Registry) List() []*Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()

	plugins := make([]*Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		plugins = append(plugins, snapshotLocked(p))
	}

	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].FullName() < plugins[j].FullName()
	})

	return plugins
}

// GetEnabled returns all enabled plugins.
func (r *Registry) GetEnabled() []*Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var enabled []*Plugin
	for _, p := range r.plugins {
		if p.Enabled {
			enabled = append(enabled, snapshotLocked(p))
		}
	}

	sort.Slice(enabled, func(i, j int) bool {
		return enabled[i].FullName() < enabled[j].FullName()
	})

	return enabled
}

// Enable enables a plugin.
func (r *Registry) Enable(name string, scope Scope) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.plugins[name]
	if !ok {
		return fmt.Errorf("plugin not found: %s", name)
	}

	p.Enabled = true
	return r.saveEnabledState(name, true, scope)
}

// Disable disables a plugin.
func (r *Registry) Disable(name string, scope Scope) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.plugins[name]
	if !ok {
		return fmt.Errorf("plugin not found: %s", name)
	}

	p.Enabled = false
	return r.saveEnabledState(name, false, scope)
}

// saveEnabledState persists plugin enabled state to settings.
func (r *Registry) saveEnabledState(name string, enabled bool, scope Scope) error {
	homeDir, _ := os.UserHomeDir()

	var settingsPath string
	switch scope {
	case ScopeUser:
		settingsPath = filepath.Join(confdir.Dir(homeDir), "settings.json")
	case ScopeProject:
		settingsPath = filepath.Join(confdir.Dir(r.cwd), "settings.json")
	case ScopeLocal:
		settingsPath = filepath.Join(confdir.Dir(r.cwd), "settings.local.json")
	default:
		settingsPath = filepath.Join(confdir.Dir(homeDir), "settings.json")
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}

	// Load existing settings
	var settings map[string]any
	if err := atomicfile.ReadJSON(settingsPath, &settings); err != nil {
		return err
	}
	if settings == nil {
		settings = make(map[string]any)
	}

	// Update enabled plugins
	enabledPlugins, _ := settings["enabledPlugins"].(map[string]any)
	if enabledPlugins == nil {
		enabledPlugins = make(map[string]any)
	}
	enabledPlugins[name] = enabled
	settings["enabledPlugins"] = enabledPlugins

	// Write back
	if err := atomicfile.WriteJSON(settingsPath, settings, 0o644); err != nil {
		return err
	}
	fireConfigChanged(scopeConfigSource(scope), settingsPath)
	return nil
}

func scopeConfigSource(scope Scope) string {
	switch scope {
	case ScopeProject:
		return "project_settings"
	case ScopeLocal:
		return "local_settings"
	default:
		return "user_settings"
	}
}

// Register adds a plugin to the registry.
func (r *Registry) Register(plugin *Plugin) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.plugins[plugin.FullName()] = plugin
}

// Unregister removes a plugin from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.plugins, name)
}

// LoadFromPath loads a plugin from a specific path (for --plugin-dir).
func (r *Registry) LoadFromPath(ctx context.Context, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	plugin, err := LoadPlugin(path, ScopeLocal, "")
	if err != nil {
		return err
	}

	plugin.Enabled = true // Always enable plugins loaded via --plugin-dir
	r.plugins[plugin.FullName()] = plugin

	return nil
}

// Count returns the number of loaded plugins.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.plugins)
}

// EnabledCount returns the number of enabled plugins.
func (r *Registry) EnabledCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, p := range r.plugins {
		if p.Enabled {
			count++
		}
	}
	return count
}

// GetByScope returns plugins filtered by scope.
func (r *Registry) GetByScope(scope Scope) []*Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Plugin
	for _, p := range r.plugins {
		if p.Scope == scope {
			result = append(result, snapshotLocked(p))
		}
	}
	return result
}

// GetAllMCPServers returns all MCP server configs from enabled plugins.
func (r *Registry) GetAllMCPServers() map[string]MCPServerConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]MCPServerConfig)
	for _, p := range r.plugins {
		if !p.Enabled || p.Components.MCP == nil {
			continue
		}
		for name, config := range p.Components.MCP {
			// Namespace the server name
			key := p.Name() + ":" + name
			result[key] = config
		}
	}
	return result
}

// GetAllLSPServers returns all LSP server configs from enabled plugins.
func (r *Registry) GetAllLSPServers() map[string]LSPServerConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]LSPServerConfig)
	for _, p := range r.plugins {
		if !p.Enabled || p.Components.LSP == nil {
			continue
		}
		for name, config := range p.Components.LSP {
			result[name] = config
		}
	}
	return result
}

// GetAllHooks returns all hooks from enabled plugins.
func (r *Registry) GetAllHooks() map[string][]HookMatcher {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string][]HookMatcher)
	for _, p := range r.plugins {
		if !p.Enabled || p.Components.Hooks == nil {
			continue
		}
		for event, matchers := range p.Components.Hooks.Hooks {
			result[event] = append(result[event], matchers...)
		}
	}
	return result
}

// defaultRegistry is the package-level plugin registry.
var defaultRegistry = NewRegistry()
