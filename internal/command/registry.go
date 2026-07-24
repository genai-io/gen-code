// Package command provides slash-command metadata, parsing, and matching logic.
// Handler dispatch remains in the tui package since handlers reference the tui model.
package command

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/markdown"

	"gopkg.in/yaml.v3"
)

// Info holds the metadata for a slash command (name, description, visibility).
type Info struct {
	Name        string
	Description string
	Hidden      bool
}

// builtinCommands returns the static set of built-in command metadata.
// This is the single source of truth for command names and descriptions.
func builtinCommands() []Info {
	return []Info{
		{Name: "models", Description: "Select model and manage provider connections"},
		{Name: "clear", Description: "Clear chat history"},
		{Name: "fork", Description: "Fork current conversation into a new session"},
		{Name: "resume", Description: "Resume a previous session (opens session selector)"},
		{Name: "help", Description: "Show available commands"},
		{Name: "tools", Description: "Manage available tools (enable/disable)"},
		{Name: "skills", Description: "Manage skills (enable/disable/activate)"},
		{Name: "agents", Description: "Manage available agents (enable/disable)"},
		{Name: "identity", Description: "Alias of /persona — switch the active persona (/identity <name>, or /identity to open the picker)"},
		{Name: "persona", Description: "Switch the active persona (/persona <name>, or /persona to open the picker)"},
		{Name: "tokenlimit", Description: "View or set token limits for current model"},
		{Name: "compact", Description: "Summarize conversation to reduce context size"},
		{Name: "init", Description: "Initialize memory files (SAN.md, local, rules)"},
		{Name: "memory", Description: "View and manage memory files (list/show/edit) with @import support"},
		{Name: "mcp", Description: "Manage MCP servers (add/edit/remove/connect/list)"},
		{Name: "plugin", Description: "Manage plugins (list/install/marketplace/enable/disable/info)"},
		{Name: "reload-plugins", Description: "Reload plugins and refresh plugin-backed skills, agents, MCP, and hooks"},
		{Name: "think", Description: "Toggle provider-native thinking effort"},
		{Name: "loop", Description: "Schedule recurring or one-shot prompts and manage loop jobs"},
		{Name: "search", Description: "Select search engine for web search"},
		{Name: "config", Description: "Configure appearance and other settings"},
		{Name: "evolve", Description: "Configure self-learning (skills & memory)"},
		{Name: "autopilot", Description: "Configure the autopilot copilot (steers, system prompt, mission)"},
		{Name: "goal", Description: "State a goal and let autopilot drive until it's met (/goal <text>, /goal clear)"},
		{Name: "name", Description: "Set or change the name of the current conversation session"},
		{Name: "quit", Description: "Exit the application (/exit also works)"},
	}
}

// ParseCommand splits a slash-command input into the command name, arguments,
// and a boolean indicating whether the input was a command at all.
// This is a pure function with no state dependency.
func ParseCommand(input string) (cmd string, args string, isCmd bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return "", "", false
	}

	input = strings.TrimPrefix(input, "/")
	parts := strings.SplitN(input, " ", 2)
	cmd = strings.ToLower(parts[0])
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return cmd, args, true
}

// ── implementation ─────────────────────────────────────────

// commandScope represents where a custom command was loaded from.
// Higher values have higher priority.
type commandScope int

const (
	scopeUser          commandScope = iota // ~/.san/commands/
	scopeUserPlugin                        // ~/.san/plugins/*/commands/
	scopeProjectPlugin                     // .san/plugins/*/commands/
	scopeProject                           // .san/commands/
	scopeBuiltin                           // embedded in the binary (internal/command/prompts/)
)

// CustomCommand represents a user-defined slash command from
// ~/.san/commands/, .san/commands/, or a plugin's commands/ directory.
// Unlike active skills, custom commands are never injected into the system
// prompt — they only execute when the user explicitly invokes /name.
type CustomCommand struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Namespace   string `yaml:"namespace"`
	FilePath    string
	Body        string // embedded workflow body for builtin commands (FilePath is empty)
	Scope       commandScope
}

// FullName returns the namespaced command name (namespace:name or just name).
func (cc *CustomCommand) FullName() string {
	if cc.Namespace != "" {
		return cc.Namespace + ":" + cc.Name
	}
	return cc.Name
}

// GetInstructions reads the markdown body (excluding frontmatter) from disk,
// or returns the embedded body for a builtin command.
func (cc *CustomCommand) GetInstructions() string {
	if cc.FilePath == "" {
		return cc.Body
	}
	_, body, _ := markdown.ParseFrontmatterFile(cc.FilePath)
	return body
}

// service is the internal implementation of Service.
type Registry struct {
	mu                   sync.RWMutex
	cwd                  string
	cachedCustomCommands []CustomCommand
	dynamicInfoProviders []func() []Info
	pluginCommandPaths   func() []PluginCommandPath
}

func (s *Registry) BuiltinNames() map[string]Info {
	cmds := builtinCommands()
	m := make(map[string]Info, len(cmds))
	for _, c := range cmds {
		m[c.Name] = c
	}
	return m
}

func (s *Registry) Get(name string) (Info, bool) {
	// Check builtins first.
	builtins := s.BuiltinNames()
	if info, ok := builtins[name]; ok {
		return info, true
	}
	// Check dynamic providers.
	for _, provider := range s.getDynamicInfoProviders() {
		for _, cmd := range provider() {
			if cmd.Name == name {
				return cmd, true
			}
		}
	}
	// Check custom commands.
	for _, cmd := range s.loadAllCustomCommands() {
		if cmd.FullName() == name || cmd.Name == name {
			return Info{Name: cmd.FullName(), Description: cmd.Description}, true
		}
	}
	return Info{}, false
}

func (s *Registry) List() []Info {
	seen := make(map[string]bool)
	var all []Info

	for _, cmd := range builtinCommands() {
		all = append(all, cmd)
		seen[cmd.Name] = true
	}

	for _, provider := range s.getDynamicInfoProviders() {
		for _, cmd := range provider() {
			if !seen[cmd.Name] {
				all = append(all, cmd)
				seen[cmd.Name] = true
			}
		}
	}

	for _, cmd := range s.loadAllCustomCommands() {
		name := cmd.FullName()
		if !seen[name] {
			all = append(all, Info{Name: name, Description: cmd.Description})
			seen[name] = true
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Name < all[j].Name
	})
	return all
}

func (s *Registry) ListCustom() []CustomCommand {
	return s.loadAllCustomCommands()
}

func (s *Registry) GetMatching(prefix string) []Info {
	query := strings.ToLower(strings.TrimPrefix(prefix, "/"))
	matches := make([]Info, 0)
	seen := make(map[string]bool)

	builtins := s.BuiltinNames()
	for name, cmd := range builtins {
		if fuzzyMatch(name, query) {
			matches = append(matches, cmd)
			seen[name] = true
		}
	}

	for _, provider := range s.getDynamicInfoProviders() {
		for _, cmd := range provider() {
			if fuzzyMatch(strings.ToLower(cmd.Name), query) && !seen[cmd.Name] {
				matches = append(matches, cmd)
				seen[cmd.Name] = true
			}
		}
	}

	customCmds := s.GetCustomCommands()
	for _, cmd := range customCmds {
		if fuzzyMatch(strings.ToLower(cmd.Name), query) {
			if !seen[cmd.Name] {
				matches = append(matches, cmd)
				seen[cmd.Name] = true
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Name < matches[j].Name
	})

	return matches
}

func (s *Registry) IsCustomCommand(cmd string) (*CustomCommand, bool) {
	for _, c := range s.loadAllCustomCommands() {
		if c.FullName() == cmd || c.Name == cmd {
			return &c, true
		}
	}
	return nil, false
}

func (s *Registry) GetCustomCommands() []Info {
	cmds := s.loadAllCustomCommands()
	infos := make([]Info, 0, len(cmds))
	for _, c := range cmds {
		infos = append(infos, Info{
			Name:        c.FullName(),
			Description: c.Description,
		})
	}
	return infos
}

func (s *Registry) getDynamicInfoProviders() []func() []Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]func() []Info(nil), s.dynamicInfoProviders...)
}

// loadAllCustomCommands returns custom commands from all sources, using cache
// when available. The cache is invalidated by Initialize.
func (s *Registry) loadAllCustomCommands() []CustomCommand {
	s.mu.RLock()
	if s.cachedCustomCommands != nil {
		defer s.mu.RUnlock()
		return s.cachedCustomCommands
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cachedCustomCommands != nil {
		return s.cachedCustomCommands
	}
	s.cachedCustomCommands = appendBuiltinPromptCommands(s.loadCustomCommandsFromDisk())
	return s.cachedCustomCommands
}

// appendBuiltinPromptCommands adds the workflows shipped inside the binary,
// unless a disk command already claims the name — user, project, and plugin
// commands override builtins.
func appendBuiltinPromptCommands(cmds []CustomCommand) []CustomCommand {
	taken := make(map[string]struct{}, len(cmds))
	for _, c := range cmds {
		taken[c.Name] = struct{}{}
	}
	for _, b := range builtinPromptCommands() {
		if _, shadowed := taken[b.Name]; !shadowed {
			cmds = append(cmds, b)
		}
	}
	return cmds
}

// loadCustomCommandsFromDisk loads custom commands from all sources in priority order:
// 1. ~/.san/commands/        (user level, lowest priority)
// 2. ~/.san/plugins/*/commands/ (user-plugin)
// 3. .san/plugins/*/commands/   (project-plugin)
// 4. .san/commands/          (project level, highest priority)
// Higher-priority commands override lower-priority ones with the same full name.
func (s *Registry) loadCustomCommandsFromDisk() []CustomCommand {
	cmdMap := make(map[string]CustomCommand)

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		userDir := filepath.Join(confdir.Dir(homeDir), "commands")
		for _, pc := range loadCommandsFromDir(userDir, "", scopeUser) {
			cmdMap[pc.FullName()] = pc
		}
	}

	if s.pluginCommandPaths != nil {
		for _, pp := range s.pluginCommandPaths() {
			pc := loadCustomCommandFile(pp.Path, pp.Namespace)
			if pc != nil {
				if pp.IsProject {
					pc.Scope = scopeProjectPlugin
				} else {
					pc.Scope = scopeUserPlugin
				}
				cmdMap[pc.FullName()] = *pc
			}
		}
	}

	if s.cwd != "" {
		projectDir := filepath.Join(confdir.Dir(s.cwd), "commands")
		for _, pc := range loadCommandsFromDir(projectDir, "", scopeProject) {
			cmdMap[pc.FullName()] = pc
		}
	}

	cmds := make([]CustomCommand, 0, len(cmdMap))
	for _, c := range cmdMap {
		cmds = append(cmds, c)
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].FullName() < cmds[j].FullName()
	})
	return cmds
}

// loadCommandsFromDir scans a directory for markdown command files.
func loadCommandsFromDir(dir, defaultNamespace string, scope commandScope) []CustomCommand {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var cmds []CustomCommand
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		pc := loadCustomCommandFile(filepath.Join(dir, entry.Name()), defaultNamespace)
		if pc != nil {
			pc.Scope = scope
			cmds = append(cmds, *pc)
		}
	}
	return cmds
}

// loadCustomCommandFile loads a single custom command from a markdown file.
func loadCustomCommandFile(path, defaultNamespace string) *CustomCommand {
	fm, _, _ := markdown.ParseFrontmatterFile(path)
	if fm == "" {
		return defaultCustomCommand(path, defaultNamespace)
	}
	var cc CustomCommand
	if err := yaml.Unmarshal([]byte(fm), &cc); err != nil {
		return defaultCustomCommand(path, defaultNamespace)
	}
	cc.FilePath = path
	if cc.Name == "" {
		cc.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if cc.Namespace == "" && defaultNamespace != "" {
		cc.Namespace = defaultNamespace
	}
	return &cc
}

// defaultCustomCommand creates a CustomCommand with defaults derived from the filename.
func defaultCustomCommand(path, defaultNamespace string) *CustomCommand {
	return &CustomCommand{
		Name:      strings.TrimSuffix(filepath.Base(path), ".md"),
		Namespace: defaultNamespace,
		FilePath:  path,
	}
}

// fuzzyMatch returns true if every character in pattern appears in str in order.
func fuzzyMatch(str, pattern string) bool {
	pi := 0
	for si := 0; si < len(str) && pi < len(pattern); si++ {
		if str[si] == pattern[pi] {
			pi++
		}
	}
	return pi == len(pattern)
}
