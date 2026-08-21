package plugin

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"

	"github.com/genai-io/san/internal/setting"
)

const (
	// EnvLoadClaudePlugins controls whether to load Claude Code plugins.
	EnvLoadClaudePlugins = "SAN_LOAD_CLAUDE_PLUGINS"
)

// LoadClaudePlugins loads plugins from Claude Code's plugin directories.
// This is controlled by the SAN_LOAD_CLAUDE_PLUGINS environment variable.
func (r *Registry) LoadClaudePlugins(ctx context.Context) error {
	if setting.Getenv("LOAD_CLAUDE_PLUGINS") != "true" {
		return nil
	}

	homeDir, _ := os.UserHomeDir()

	// Claude Code plugin locations
	claudeDirs := []string{
		filepath.Join(homeDir, ".claude", "plugins", "cache"),
		filepath.Join(homeDir, ".claude", "plugins"),
	}

	// Load enabled plugins from Claude settings
	claudeEnabled := loadClaudeEnabledPlugins(homeDir)

	// Collect all Claude plugins first, then merge under a single lock
	// to prevent concurrent callers from seeing partially-loaded state.
	collected := make(map[string]*Plugin)
	for _, dir := range claudeDirs {
		plugins, err := LoadPluginsFromDir(dir, ScopeUser, "claude")
		if err != nil {
			continue
		}
		for _, p := range plugins {
			convertClaudePlugin(p)

			key := p.FullName()
			if enabled, ok := claudeEnabled[key]; ok {
				p.Enabled = enabled
			} else if enabled, ok := claudeEnabled[p.Name()]; ok {
				p.Enabled = enabled
			} else {
				p.Enabled = true
			}
			collected[key] = p
		}
	}

	r.mu.Lock()
	maps.Copy(r.plugins, collected)
	r.mu.Unlock()

	return nil
}

// loadClaudeEnabledPlugins loads enabled plugin settings from Claude Code settings.
func loadClaudeEnabledPlugins(homeDir string) map[string]bool {
	result := make(map[string]bool)

	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return result
	}

	var settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return result
	}

	return settings.EnabledPlugins
}

// convertClaudePlugin sets the source suffix for Claude Code plugins.
func convertClaudePlugin(p *Plugin) {
	if p.Source == "" {
		p.Source = p.Name() + "@claude"
	}
}
