package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/genai-io/san/internal/atomicfile"
	"github.com/genai-io/san/internal/confdir"
)

// Installer handles plugin installation and management.
type Installer struct {
	registry           *Registry
	cwd                string
	marketplaces       map[string]MarketplaceSource
	marketplaceManager *MarketplaceManager
}

// NewInstaller creates a new plugin installer.
func NewInstaller(registry *Registry, cwd string) *Installer {
	if registry != nil {
		// plugin.Install builds an Installer inside a tea.Cmd goroutine, so a
		// concurrent cwd change (discoverPlugins -> Load) would race a bare
		// assignment here.
		registry.SetCwd(cwd)
	}
	return &Installer{
		registry:           registry,
		cwd:                cwd,
		marketplaces:       make(map[string]MarketplaceSource),
		marketplaceManager: NewMarketplaceManager(cwd),
	}
}

// LoadMarketplaces loads known marketplace definitions.
func (i *Installer) LoadMarketplaces() error {
	// Load via new marketplace manager
	if err := i.marketplaceManager.Load(); err != nil {
		return err
	}

	// Sync marketplace manager data to i.marketplaces
	for _, id := range i.marketplaceManager.List() {
		entry, ok := i.marketplaceManager.Get(id)
		if !ok {
			continue
		}
		source := MarketplaceSource{
			Name: id,
			Type: entry.Source.Source,
		}
		switch entry.Source.Source {
		case "github":
			source.Repository = entry.Source.Repo
		case "directory":
			source.Path = entry.Source.Path
		}
		i.marketplaces[id] = source
	}

	homeDir, _ := os.UserHomeDir()

	// Also load legacy format for backward compatibility
	paths := []string{
		filepath.Join(confdir.Dir(homeDir), "plugins", "known_marketplaces.json"),
		filepath.Join(confdir.Dir(i.cwd), "plugins", "known_marketplaces.json"),
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var km KnownMarketplaces
		if err := json.Unmarshal(data, &km); err != nil {
			continue
		}
		for _, m := range km.Marketplaces {
			i.marketplaces[m.Name] = m
		}
	}

	return nil
}

// resolveMarketplaceByName searches for a marketplace whose marketplace.json
// "name" field matches the given name. Returns the marketplace ID if found.
func (i *Installer) resolveMarketplaceByName(name string) string {
	for _, id := range i.marketplaceManager.List() {
		meta, err := i.marketplaceManager.GetMarketplaceMetadata(id)
		if err != nil {
			continue
		}
		if meta.Name == name {
			return id
		}
	}
	return ""
}

// ParsePluginRef parses a plugin reference like "git@my-marketplace" or "git".
func ParsePluginRef(ref string) (name, marketplace string) {
	parts := strings.SplitN(ref, "@", 2)
	name = parts[0]
	if len(parts) > 1 {
		marketplace = parts[1]
	}
	return name, marketplace
}

// FormatPluginRef builds a plugin reference from a name and optional
// marketplace. It is the inverse of ParsePluginRef: an empty marketplace
// yields just the name, otherwise "name@marketplace".
func FormatPluginRef(name, marketplace string) string {
	if marketplace == "" {
		return name
	}
	return name + "@" + marketplace
}

// Install wires a fresh installer to reg/cwd, loads known marketplaces, and
// installs the plugin referenced by ref ("name@marketplace" or "name") into
// scope. It bundles the marketplace-load step every caller needs so call sites
// don't repeat the sequence; cancellation and timeout are the caller's via ctx.
func Install(ctx context.Context, reg *Registry, cwd, ref string, scope Scope) error {
	installer := NewInstaller(reg, cwd)
	if err := installer.LoadMarketplaces(); err != nil {
		return fmt.Errorf("load marketplaces: %w", err)
	}
	return installer.Install(ctx, ref, scope)
}

// Install installs a plugin from a reference.
// Reference format: "plugin-name@marketplace" or "plugin-name" (uses default)
func (i *Installer) Install(ctx context.Context, ref string, scope Scope) error {
	name, marketplace := ParsePluginRef(ref)

	// Resolve the marketplace: explicit ID, marketplace.json "name", or — when
	// none is given — the first known marketplace that offers this plugin.
	if marketplace != "" {
		if _, ok := i.marketplaces[marketplace]; !ok {
			if resolved := i.resolveMarketplaceByName(marketplace); resolved != "" {
				marketplace = resolved
			} else {
				return fmt.Errorf("unknown marketplace: %s", marketplace)
			}
		}
	} else if found := i.findMarketplaceFor(name); found != "" {
		marketplace = found
	} else {
		return fmt.Errorf("could not find plugin: %s", name)
	}

	// Resolve the plugin's declared source from the local marketplace.json.
	// Only refresh a GitHub marketplace from the network when its manifest
	// isn't available yet: installing shouldn't force a re-sync, and a
	// transient pull failure shouldn't block installing a plugin that lives in
	// its own repo.
	psrc, declared := i.resolvePluginSource(marketplace, name)
	if !declared && i.marketplaces[marketplace].Type == "github" {
		if err := i.marketplaceManager.Sync(ctx, marketplace); err != nil {
			return fmt.Errorf("failed to sync marketplace %s: %w", marketplace, err)
		}
		psrc, declared = i.resolvePluginSource(marketplace, name)
	}

	// Locate the plugin's content. Following Claude Code's model, a marketplace
	// may merely declare a plugin whose content lives in its own repo — fetch
	// that; otherwise the content sits inside the marketplace repo.
	var srcPath string
	if declared && psrc.External() {
		path, cleanup, err := fetchExternalPlugin(ctx, psrc)
		if err != nil {
			return fmt.Errorf("failed to fetch plugin %s: %w", name, err)
		}
		defer cleanup()
		srcPath = path
	} else {
		path, err := i.marketplaceManager.ResolveLocalPluginPath(marketplace, name, psrc)
		if err != nil {
			return fmt.Errorf("plugin %s not found in marketplace %s: %w", name, marketplace, err)
		}
		srcPath = path
	}

	// Install into the scope dir as a fresh copy (no .git history).
	installDir := i.getInstallDir(scope)
	pluginPath := filepath.Join(installDir, name)
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("failed to create install directory: %w", err)
	}
	if err := os.RemoveAll(pluginPath); err != nil {
		return fmt.Errorf("failed to clear previous install: %w", err)
	}
	if err := copyDir(srcPath, pluginPath); err != nil {
		return fmt.Errorf("failed to install plugin: %w", err)
	}

	// Add to installed_plugins.json
	fullName := name
	if marketplace != "" {
		fullName = name + "@" + marketplace
	}
	if err := i.addToInstalled(scope, InstalledPlugin{
		Name:        name,
		Source:      fullName,
		Path:        pluginPath,
		InstalledAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		return err
	}

	// Load the plugin into registry
	plugin, err := LoadPlugin(pluginPath, scope, fullName)
	if err != nil {
		return fmt.Errorf("failed to load installed plugin: %w", err)
	}
	plugin.Enabled = true
	i.registry.Register(plugin)

	// Enable the plugin
	return i.registry.Enable(fullName, scope)
}

// Uninstall removes a plugin.
func (i *Installer) Uninstall(name string, scope Scope) error {
	// Get plugin from registry
	plugin, ok := i.registry.Get(name)
	if !ok {
		return fmt.Errorf("plugin not found: %s", name)
	}

	// Remove plugin directory
	if plugin.Path != "" {
		if err := os.RemoveAll(plugin.Path); err != nil {
			return fmt.Errorf("failed to remove plugin directory: %w", err)
		}
	}

	// Remove from installed_plugins.json
	if err := i.removeFromInstalled(scope, plugin.FullName()); err != nil {
		return err
	}

	// Unregister from registry
	i.registry.Unregister(plugin.FullName())

	return nil
}

// Update updates a plugin to the latest version.
func (i *Installer) Update(ctx context.Context, name string, scope Scope) error {
	plugin, ok := i.registry.Get(name)
	if !ok {
		return fmt.Errorf("plugin not found: %s", name)
	}

	// For git-based plugins, try git pull
	gitDir := filepath.Join(plugin.Path, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		cmd := exec.CommandContext(ctx, "git", "-C", plugin.Path, "pull", "--ff-only")
		return cmd.Run()
	}

	// For directory-based plugins, re-install
	return i.Install(ctx, plugin.FullName(), scope)
}

// getInstallDir returns the installation directory for a scope.
func (i *Installer) getInstallDir(scope Scope) string {
	homeDir, _ := os.UserHomeDir()

	switch scope {
	case ScopeUser:
		return filepath.Join(confdir.Dir(homeDir), "plugins", "cache")
	case ScopeProject:
		return filepath.Join(confdir.Dir(i.cwd), "plugins")
	case ScopeLocal:
		return filepath.Join(confdir.Dir(i.cwd), "plugins-local")
	default:
		return filepath.Join(confdir.Dir(homeDir), "plugins", "cache")
	}
}

// resolvePluginSource returns the source declared for a plugin in a
// marketplace's marketplace.json, and whether such a declaration was found.
func (i *Installer) resolvePluginSource(marketplaceID, name string) (PluginSource, bool) {
	if marketplaceID == "" {
		return PluginSource{}, false
	}
	meta, err := i.marketplaceManager.GetMarketplaceMetadata(marketplaceID)
	if err != nil {
		return PluginSource{}, false
	}
	for _, p := range meta.Plugins {
		if p.Name == name {
			return p.Source, true
		}
	}
	return PluginSource{}, false
}

// findMarketplaceFor returns the first known marketplace that offers a plugin
// with the given name — either declared in its marketplace.json or vendored as
// a subdirectory.
func (i *Installer) findMarketplaceFor(name string) string {
	for _, id := range i.marketplaceManager.List() {
		if _, ok := i.resolvePluginSource(id, name); ok {
			return id
		}
		if _, err := i.marketplaceManager.GetPluginPath(id, name); err == nil {
			return id
		}
	}
	return ""
}

// fetchExternalPlugin clones a plugin's own git repository into a temp dir and
// returns the directory holding the plugin root (the repo, or a subdirectory of
// it for git-subdir sources), along with a cleanup func the caller must defer.
func fetchExternalPlugin(ctx context.Context, src PluginSource) (string, func(), error) {
	if src.Type == SourceNPM {
		return "", nil, fmt.Errorf("npm plugin sources are not supported yet")
	}

	tmp, err := os.MkdirTemp("", "san-plugin-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	url := src.URL
	if src.Type == SourceGitHub {
		url = "https://github.com/" + src.Repo + ".git"
	}
	url = normalizeGitURL(url)
	if url == "" {
		cleanup()
		return "", nil, fmt.Errorf("plugin source has no repository URL")
	}

	if err := cloneRepo(ctx, url, src.Ref, src.SHA, tmp); err != nil {
		cleanup()
		return "", nil, err
	}
	_ = os.RemoveAll(filepath.Join(tmp, ".git"))

	root := tmp
	if src.Type == SourceGitSubdir && src.Path != "" {
		clean, err := safeRelPath(src.Path)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		root = filepath.Join(tmp, clean)
	}
	return root, cleanup, nil
}

// normalizeGitURL expands a bare "owner/repo" shorthand into a full GitHub
// HTTPS URL. Full URLs (https://, ssh://, git@) are returned unchanged.
func normalizeGitURL(url string) string {
	if url == "" || strings.Contains(url, "://") || strings.HasPrefix(url, "git@") {
		return url
	}
	if strings.Count(url, "/") == 1 && !strings.ContainsAny(url, " \t") {
		return "https://github.com/" + url + ".git"
	}
	return url
}

// cloneRepo shallow-clones url into dest, honoring an optional ref (branch/tag)
// and sha (exact commit, which takes precedence). Pinning to a sha that a
// shallow clone doesn't contain triggers a deepen-then-checkout fallback.
func cloneRepo(ctx context.Context, url, ref, sha, dest string) error {
	args := []string{"clone", "--depth", "1"}
	if ref != "" && sha == "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, url, dest)
	if out, err := runGit(ctx, "", args...); err != nil {
		return fmt.Errorf("git clone %s: %w: %s", url, err, strings.TrimSpace(out))
	}

	if sha != "" {
		if _, err := runGit(ctx, dest, "checkout", sha); err != nil {
			if _, ferr := runGit(ctx, dest, "fetch", "--unshallow"); ferr != nil {
				_, _ = runGit(ctx, dest, "fetch", "--depth", "1", "origin", sha)
			}
			if out, cerr := runGit(ctx, dest, "checkout", sha); cerr != nil {
				return fmt.Errorf("git checkout %s: %w: %s", sha, cerr, strings.TrimSpace(out))
			}
		}
	}
	return nil
}

// runGit runs a git command (optionally inside dir) and returns its combined
// output for error context.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// addToInstalled adds a plugin to installed_plugins.json using v2 format.
func (i *Installer) addToInstalled(scope Scope, plugin InstalledPlugin) error {
	return i.addToInstalledV2(scope, plugin.Source, PluginInstallInfo{
		Scope:       string(scope),
		InstallPath: plugin.Path,
		Version:     plugin.Version,
		InstalledAt: plugin.InstalledAt,
		LastUpdated: plugin.InstalledAt,
	})
}

// addToInstalledV2 adds a plugin to installed_plugins.json using v2 format.
func (i *Installer) addToInstalledV2(scope Scope, pluginKey string, info PluginInstallInfo) error {
	installedFile := GetInstalledPluginsFile(i.cwd, scope)
	if err := os.MkdirAll(filepath.Dir(installedFile), 0o755); err != nil {
		return err
	}

	v2 := loadInstalledPluginsV2(installedFile)
	existing := v2.Plugins[pluginKey]

	// Update existing entry for this scope or prepend new one
	updated := false
	for idx, inst := range existing {
		if inst.Scope == info.Scope {
			existing[idx] = info
			updated = true
			break
		}
	}
	if !updated {
		existing = append([]PluginInstallInfo{info}, existing...)
	}
	v2.Plugins[pluginKey] = existing

	return atomicfile.WriteJSON(installedFile, v2, 0o644)
}

// loadInstalledPluginsV2 loads the installed plugins in v2 format.
func loadInstalledPluginsV2(path string) *InstalledPluginsV2 {
	v2 := &InstalledPluginsV2{
		Version: 2,
		Plugins: make(map[string][]PluginInstallInfo),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return v2
	}

	// Try v2 format first
	if err := json.Unmarshal(data, v2); err == nil && v2.Version == 2 {
		if v2.Plugins == nil {
			v2.Plugins = make(map[string][]PluginInstallInfo)
		}
		return v2
	}

	// Try v1 format (array of InstalledPlugin)
	var v1 []InstalledPlugin
	if err := json.Unmarshal(data, &v1); err == nil {
		// Convert to v2
		for _, p := range v1 {
			info := PluginInstallInfo{
				Scope:       "user",
				InstallPath: p.Path,
				Version:     p.Version,
				InstalledAt: p.InstalledAt,
			}
			v2.Plugins[p.Source] = append(v2.Plugins[p.Source], info)
		}
	}

	return v2
}

// removeFromInstalled removes a plugin from installed_plugins.json.
func (i *Installer) removeFromInstalled(scope Scope, source string) error {
	installedFile := GetInstalledPluginsFile(i.cwd, scope)

	v2 := loadInstalledPluginsV2(installedFile)

	// Remove the plugin key entirely or just this scope's entry
	if entries, ok := v2.Plugins[source]; ok {
		// Filter out entries for this scope
		var filtered []PluginInstallInfo
		for _, e := range entries {
			if e.Scope != string(scope) {
				filtered = append(filtered, e)
			}
		}
		if len(filtered) == 0 {
			delete(v2.Plugins, source)
		} else {
			v2.Plugins[source] = filtered
		}
	}

	if len(v2.Plugins) == 0 {
		return os.Remove(installedFile)
	}

	return atomicfile.WriteJSON(installedFile, v2, 0o644)
}

// copyDir copies a directory recursively.
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		// Skip symlinks to prevent symlink escape attacks
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}

		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copies a single file.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	return os.WriteFile(dst, data, srcInfo.Mode())
}

// ListInstalled returns all installed plugins for a scope.
// Handles both v1 (JSON array) and v2 (versioned object) formats.
func (i *Installer) ListInstalled(scope Scope) ([]InstalledPlugin, error) {
	installedFile := GetInstalledPluginsFile(i.cwd, scope)

	v2 := loadInstalledPluginsV2(installedFile)

	var installed []InstalledPlugin
	for source, entries := range v2.Plugins {
		for _, info := range entries {
			installed = append(installed, InstalledPlugin{
				Name:        source,
				Source:      source,
				Path:        info.InstallPath,
				Version:     info.Version,
				InstalledAt: info.InstalledAt,
			})
		}
	}
	return installed, nil
}

// GetMarketplaces returns all known marketplaces.
func (i *Installer) GetMarketplaces() []MarketplaceSource {
	result := make([]MarketplaceSource, 0, len(i.marketplaces))
	for _, m := range i.marketplaces {
		result = append(result, m)
	}
	return result
}

// AddMarketplace adds a new marketplace source.
func (i *Installer) AddMarketplace(source MarketplaceSource) error {
	homeDir, _ := os.UserHomeDir()
	path := filepath.Join(confdir.Dir(homeDir), "plugins", "known_marketplaces.json")

	// Load existing
	var km KnownMarketplaces
	if err := atomicfile.ReadJSON(path, &km); err != nil {
		return err
	}

	// Check if already exists
	found := false
	for idx, m := range km.Marketplaces {
		if m.Name == source.Name {
			km.Marketplaces[idx] = source
			found = true
			break
		}
	}

	if !found {
		km.Marketplaces = append(km.Marketplaces, source)
		i.marketplaces[source.Name] = source
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	return atomicfile.WriteJSON(path, km, 0o644)
}
