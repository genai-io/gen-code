package input

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	coreplugin "github.com/genai-io/san/internal/plugin"
)

// ── Commands ─────────────────────────────────────────────────────────────────

// HandlePluginCommand dispatches /plugin subcommands.
func HandlePluginCommand(ctx context.Context, selector *PluginSelector, cwd string, width, height int, args string) (string, error) {
	if selector.registry.Count() == 0 {
		if err := selector.registry.Load(ctx, cwd); err != nil {
			return fmt.Sprintf("Failed to load plugins: %v", err), nil
		}
		_ = selector.registry.LoadClaudePlugins(ctx)
	}

	args = strings.TrimSpace(args)
	parts := strings.Fields(args)

	if len(parts) == 0 {
		if err := selector.EnterSelect(width, height); err != nil {
			return fmt.Sprintf("Failed to open plugin selector: %v", err), nil
		}
		return "", nil
	}

	subCmd := strings.ToLower(parts[0])
	var pluginName string
	if len(parts) > 1 {
		pluginName = parts[1]
	}

	switch subCmd {
	case "list":
		return pluginHandleList(selector.registry)
	case "install":
		return pluginHandleInstall(selector.registry, ctx, cwd, parts[1:])
	case "marketplace":
		return pluginHandleMarketplace(ctx, cwd, parts[1:])
	case "enable":
		return pluginHandleEnable(selector.registry, ctx, pluginName)
	case "disable":
		return pluginHandleDisable(selector.registry, ctx, pluginName)
	case "info":
		return pluginHandleInfo(selector.registry, pluginName)
	case "errors":
		return pluginHandleErrors(selector.registry)
	default:
		return pluginHandleInfo(selector.registry, subCmd)
	}
}

// pluginHandleList shows all installed plugins.
func pluginHandleList(reg *coreplugin.Registry) (string, error) {
	plugins := reg.List()

	if len(plugins) == 0 {
		return "No plugins installed.\n\nInstall with: san plugin install <plugin>@<marketplace>", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugins (%d installed, %d enabled):\n\n",
		reg.Count(),
		reg.EnabledCount())

	for _, p := range plugins {
		pluginWriteSummary(&sb, p)
	}

	sb.WriteString("\nLegend: ● enabled  ○ disabled  👤 user  📁 project  💻 local")
	sb.WriteString("\n\nCommands:\n")
	sb.WriteString("  /plugin install <ref>    Install a plugin from a marketplace\n")
	sb.WriteString("  /plugin marketplace ...  Manage plugin marketplaces\n")
	sb.WriteString("  /plugin enable <name>   Enable a plugin\n")
	sb.WriteString("  /plugin disable <name>  Disable a plugin\n")
	sb.WriteString("  /plugin info <name>     Show plugin details\n")

	return sb.String(), nil
}

func pluginWriteSummary(sb *strings.Builder, p *coreplugin.Plugin) {
	status := "○"
	if p.Enabled {
		status = "●"
	}

	fmt.Fprintf(sb, "  %s %s %s (%s)\n", status, p.Scope.Icon(), p.FullName(), p.Scope)

	if p.Manifest.Description != "" {
		fmt.Fprintf(sb, "      %s\n", p.Manifest.Description)
	}

	components := pluginFormatComponentCounts(p)
	if len(components) > 0 {
		fmt.Fprintf(sb, "      [%s]\n", strings.Join(components, ", "))
	}
}

func pluginFormatComponentCounts(p *coreplugin.Plugin) []string {
	var components []string
	if n := len(p.Components.Skills); n > 0 {
		components = append(components, fmt.Sprintf("%d skills", n))
	}
	if n := len(p.Components.Agents); n > 0 {
		components = append(components, fmt.Sprintf("%d agents", n))
	}
	if n := len(p.Components.Commands); n > 0 {
		components = append(components, fmt.Sprintf("%d commands", n))
	}
	if p.Components.Hooks != nil {
		if n := len(p.Components.Hooks.Hooks); n > 0 {
			components = append(components, fmt.Sprintf("%d hooks", n))
		}
	}
	if n := len(p.Components.MCP); n > 0 {
		components = append(components, fmt.Sprintf("%d MCP", n))
	}
	if n := len(p.Components.LSP); n > 0 {
		components = append(components, fmt.Sprintf("%d LSP", n))
	}
	return components
}

// pluginHandleEnable enables a plugin.
func pluginHandleEnable(reg *coreplugin.Registry, _ context.Context, name string) (string, error) {
	if name == "" {
		return "Usage: /plugin enable <plugin-name>", nil
	}

	if err := reg.Enable(name, coreplugin.ScopeUser); err != nil {
		return fmt.Sprintf("Failed to enable '%s': %v", name, err), nil
	}

	return fmt.Sprintf("Enabled plugin '%s'\n\nRun /reload-plugins to apply changes in the current session.", name), nil
}

// pluginHandleDisable disables a plugin.
func pluginHandleDisable(reg *coreplugin.Registry, _ context.Context, name string) (string, error) {
	if name == "" {
		return "Usage: /plugin disable <plugin-name>", nil
	}

	if err := reg.Disable(name, coreplugin.ScopeUser); err != nil {
		return fmt.Sprintf("Failed to disable '%s': %v", name, err), nil
	}

	return fmt.Sprintf("Disabled plugin '%s'\n\nRun /reload-plugins to apply changes in the current session.", name), nil
}

// pluginHandleInfo shows detailed info for a plugin.
func pluginHandleInfo(reg *coreplugin.Registry, name string) (string, error) {
	if name == "" {
		return "Usage: /plugin info <plugin-name>", nil
	}

	p, ok := reg.Get(name)
	if !ok {
		return fmt.Sprintf("Plugin not found: %s\n\nUse /plugin list to see available plugins.", name), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin: %s\n", p.FullName())
	fmt.Fprintf(&sb, "Scope: %s\n", p.Scope)
	fmt.Fprintf(&sb, "Enabled: %v\n", p.Enabled)
	fmt.Fprintf(&sb, "Path: %s\n", p.Path)

	pluginWriteOptionalField(&sb, "Version", p.Manifest.Version)
	pluginWriteOptionalField(&sb, "Description", p.Manifest.Description)
	if p.Manifest.Author != nil {
		pluginWriteOptionalField(&sb, "Author", p.Manifest.Author.Name)
	}
	pluginWriteOptionalField(&sb, "Repository", p.Manifest.Repository)

	sb.WriteString("\nComponents:\n")
	pluginWriteComponentCount(&sb, "Commands", len(p.Components.Commands))
	pluginWriteComponentCount(&sb, "Skills", len(p.Components.Skills))
	pluginWriteComponentCount(&sb, "Agents", len(p.Components.Agents))
	if p.Components.Hooks != nil {
		pluginWriteComponentCount(&sb, "Hook events", len(p.Components.Hooks.Hooks))
	}
	pluginWriteComponentCount(&sb, "MCP servers", len(p.Components.MCP))
	pluginWriteComponentCount(&sb, "LSP servers", len(p.Components.LSP))

	if len(p.Errors) > 0 {
		sb.WriteString("\nErrors:\n")
		for _, err := range p.Errors {
			fmt.Fprintf(&sb, "  - %s\n", err)
		}
	}

	return sb.String(), nil
}

func pluginWriteOptionalField(sb *strings.Builder, label, value string) {
	if value != "" {
		fmt.Fprintf(sb, "%s: %s\n", label, value)
	}
}

func pluginWriteComponentCount(sb *strings.Builder, label string, count int) {
	if count > 0 {
		fmt.Fprintf(sb, "  %s: %d\n", label, count)
	}
}

// pluginHandleErrors shows all plugin errors.
func pluginHandleErrors(reg *coreplugin.Registry) (string, error) {
	plugins := reg.List()

	var sb strings.Builder
	hasErrors := false

	for _, p := range plugins {
		if len(p.Errors) > 0 {
			hasErrors = true
			fmt.Fprintf(&sb, "%s:\n", p.FullName())
			for _, err := range p.Errors {
				fmt.Fprintf(&sb, "  - %s\n", err)
			}
			sb.WriteString("\n")
		}
	}

	if !hasErrors {
		return "No plugin errors.", nil
	}

	return sb.String(), nil
}

// pluginHandleInstall installs a plugin from a configured marketplace.
func pluginHandleInstall(reg *coreplugin.Registry, ctx context.Context, cwd string, args []string) (string, error) {
	if len(args) == 0 {
		return "Usage: /plugin install <plugin>@<marketplace> [user|project|local]", nil
	}
	if len(args) > 2 {
		return "Usage: /plugin install <plugin>@<marketplace> [user|project|local]", nil
	}

	scope, err := pluginParseScopeArg("")
	if err != nil {
		return "", err
	}
	if len(args) == 2 {
		scope, err = pluginParseScopeArg(args[1])
		if err != nil {
			return err.Error(), nil
		}
	}

	ref := args[0]
	if err := coreplugin.Install(ctx, reg, cwd, ref, scope); err != nil {
		return fmt.Sprintf("Failed to install plugin '%s': %v", ref, err), nil
	}

	return fmt.Sprintf(
		"Installed plugin '%s' to %s scope.\n\nRun /reload-plugins to refresh skills, agents, MCP servers, and hooks.",
		ref,
		scope,
	), nil
}

// pluginHandleMarketplace dispatches /plugin marketplace subcommands.
func pluginHandleMarketplace(ctx context.Context, cwd string, args []string) (string, error) {
	if len(args) == 0 {
		return strings.Join([]string{
			"Usage: /plugin marketplace <subcommand>",
			"",
			"Subcommands:",
			"  /plugin marketplace list",
			"  /plugin marketplace add <owner/repo|path> [marketplace-id]",
			"  /plugin marketplace remove <marketplace-id>",
			"  /plugin marketplace sync <marketplace-id|all>",
		}, "\n"), nil
	}

	switch strings.ToLower(args[0]) {
	case "list":
		return pluginHandleMarketplaceList(cwd)
	case "add":
		return pluginHandleMarketplaceAdd(cwd, args[1:])
	case "remove":
		return pluginHandleMarketplaceRemove(cwd, args[1:])
	case "sync":
		return pluginHandleMarketplaceSync(ctx, cwd, args[1:])
	default:
		return fmt.Sprintf("Unknown marketplace subcommand: %s", args[0]), nil
	}
}

// pluginHandleMarketplaceList shows configured plugin marketplaces.
func pluginHandleMarketplaceList(cwd string) (string, error) {
	manager := coreplugin.NewMarketplaceManager(cwd)
	if err := manager.Load(); err != nil {
		return fmt.Sprintf("Failed to load marketplaces: %v", err), nil
	}

	ids := manager.List()
	sort.Strings(ids)
	if len(ids) == 0 {
		return "No marketplaces configured.\n\nAdd one with: /plugin marketplace add <owner/repo|path> [marketplace-id]", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Marketplaces (%d configured):\n\n", len(ids))
	for _, id := range ids {
		entry, ok := manager.Get(id)
		if !ok {
			continue
		}

		source := entry.Source.Path
		if entry.Source.Source == "github" {
			source = entry.Source.Repo
		}
		fmt.Fprintf(&sb, "  %s (%s)\n", id, entry.Source.Source)
		if source != "" {
			fmt.Fprintf(&sb, "      %s\n", source)
		}
	}

	sb.WriteString("\nCommands:\n")
	sb.WriteString("  /plugin marketplace add <owner/repo|path> [marketplace-id]\n")
	sb.WriteString("  /plugin marketplace remove <marketplace-id>\n")
	sb.WriteString("  /plugin marketplace sync <marketplace-id|all>\n")
	return sb.String(), nil
}

// pluginHandleMarketplaceAdd registers a new marketplace source.
func pluginHandleMarketplaceAdd(cwd string, args []string) (string, error) {
	if len(args) == 0 || len(args) > 2 {
		return "Usage: /plugin marketplace add <owner/repo|path> [marketplace-id]", nil
	}

	source := strings.TrimSpace(args[0])
	explicitID := ""
	if len(args) == 2 {
		explicitID = strings.TrimSpace(args[1])
	}

	id, normalizedSource, addFn, err := pluginParseMarketplaceSource(source, explicitID)
	if err != nil {
		return err.Error(), nil
	}

	manager := coreplugin.NewMarketplaceManager(cwd)
	if err := manager.Load(); err != nil {
		return fmt.Sprintf("Failed to load marketplaces: %v", err), nil
	}
	if err := addFn(manager, id); err != nil {
		return fmt.Sprintf("Failed to add marketplace: %v", err), nil
	}

	return fmt.Sprintf(
		"Added marketplace '%s'.\n\nSource: %s\nInstall plugins with: /plugin install <plugin>@%s",
		id,
		normalizedSource,
		id,
	), nil
}

// pluginHandleMarketplaceRemove removes a configured marketplace.
func pluginHandleMarketplaceRemove(cwd string, args []string) (string, error) {
	if len(args) != 1 {
		return "Usage: /plugin marketplace remove <marketplace-id>", nil
	}

	manager := coreplugin.NewMarketplaceManager(cwd)
	if err := manager.Load(); err != nil {
		return fmt.Sprintf("Failed to load marketplaces: %v", err), nil
	}

	id := strings.TrimSpace(args[0])
	if _, ok := manager.Get(id); !ok {
		return fmt.Sprintf("Marketplace not found: %s", id), nil
	}
	if err := manager.Remove(id); err != nil {
		return fmt.Sprintf("Failed to remove marketplace '%s': %v", id, err), nil
	}

	return fmt.Sprintf("Removed marketplace '%s'.", id), nil
}

// pluginHandleMarketplaceSync updates one or all configured marketplaces.
func pluginHandleMarketplaceSync(ctx context.Context, cwd string, args []string) (string, error) {
	if len(args) != 1 {
		return "Usage: /plugin marketplace sync <marketplace-id|all>", nil
	}

	manager := coreplugin.NewMarketplaceManager(cwd)
	if err := manager.Load(); err != nil {
		return fmt.Sprintf("Failed to load marketplaces: %v", err), nil
	}

	target := strings.TrimSpace(args[0])
	if target == "all" {
		errs := manager.SyncAll(ctx)
		if len(errs) == 0 {
			return "Synced all marketplaces.", nil
		}
		var sb strings.Builder
		sb.WriteString("Failed to sync some marketplaces:\n")
		for _, err := range errs {
			fmt.Fprintf(&sb, "  - %v\n", err)
		}
		return strings.TrimRight(sb.String(), "\n"), nil
	}

	if err := manager.SyncOrPrune(ctx, target); err != nil {
		return fmt.Sprintf("Failed to sync marketplace '%s': %v", target, err), nil
	}
	return fmt.Sprintf("Synced marketplace '%s'.", target), nil
}

func pluginParseScopeArg(raw string) (coreplugin.Scope, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(coreplugin.ScopeUser):
		return coreplugin.ScopeUser, nil
	case string(coreplugin.ScopeProject):
		return coreplugin.ScopeProject, nil
	case string(coreplugin.ScopeLocal):
		return coreplugin.ScopeLocal, nil
	default:
		return "", fmt.Errorf("invalid scope: %s (expected user, project, or local)", raw)
	}
}

func pluginParseMarketplaceSource(source, explicitID string) (id, normalizedSource string, addFn func(*coreplugin.MarketplaceManager, string) error, err error) {
	source = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(source, "]"), "["))
	if source == "" {
		return "", "", nil, fmt.Errorf("usage: /plugin marketplace add <owner/repo|path> [marketplace-id]")
	}

	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~") {
		absPath, err := pluginExpandMarketplacePath(source)
		if err != nil {
			return "", "", nil, err
		}
		id = explicitID
		if id == "" {
			id = filepath.Base(absPath)
		}
		return id, absPath, func(manager *coreplugin.MarketplaceManager, id string) error {
			return manager.AddDirectory(id, absPath)
		}, nil
	}

	if after, ok := strings.CutPrefix(source, "https://github.com/"); ok {
		repo := after
		repo = strings.TrimSuffix(repo, ".git")
		repo = strings.TrimSuffix(repo, "/")
		return pluginParseGitHubMarketplace(repo, explicitID)
	}

	if strings.Contains(source, "/") && !strings.Contains(source, "://") {
		return pluginParseGitHubMarketplace(source, explicitID)
	}

	return "", "", nil, fmt.Errorf("invalid source format. Use owner/repo, https://github.com/owner/repo, or ./path")
}

func pluginParseGitHubMarketplace(repo, explicitID string) (id, normalizedSource string, addFn func(*coreplugin.MarketplaceManager, string) error, err error) {
	parts := strings.Split(repo, "/")
	if len(parts) < 2 {
		return "", "", nil, fmt.Errorf("invalid GitHub repository: %s", repo)
	}

	id = explicitID
	if id == "" {
		id = parts[len(parts)-1]
	}

	return id, repo, func(manager *coreplugin.MarketplaceManager, id string) error {
		return manager.AddGitHub(id, repo)
	}, nil
}

func pluginExpandMarketplacePath(path string) (string, error) {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[1:])
	}
	return filepath.Abs(path)
}
