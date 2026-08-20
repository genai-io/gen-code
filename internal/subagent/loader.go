package subagent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/markdown"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// PluginAgentPath represents a plugin agent path with namespace metadata.
type PluginAgentPath struct {
	Path      string
	Namespace string
}

// agentSearchPath represents an agent search location with optional namespace.
type agentSearchPath struct {
	path      string
	namespace string // Default namespace for agents in this path (from plugin)
}

// additionalAgentPaths stores plugin agent paths.
var (
	additionalAgentPaths   []agentSearchPath
	additionalAgentPathsMu sync.Mutex
)

// AddPluginAgentPath adds a plugin agent path to be searched.
func AddPluginAgentPath(path, namespace string) {
	additionalAgentPathsMu.Lock()
	defer additionalAgentPathsMu.Unlock()
	additionalAgentPaths = append(additionalAgentPaths, agentSearchPath{
		path:      path,
		namespace: namespace,
	})
}

// ClearPluginAgentPaths clears all plugin agent paths.
func ClearPluginAgentPaths() {
	additionalAgentPathsMu.Lock()
	defer additionalAgentPathsMu.Unlock()
	additionalAgentPaths = nil
}

// LoadAgents loads agent definitions from standard locations.
// Note: .claude/plugins/ loading is removed - plugins are handled by the plugin system.
// Priority when the same agent name appears in several locations:
//  1. .san/agents/*.md (project level, preferred)
//  2. ~/.san/agents/*.md (user level, preferred)
//  3. .claude/agents/*.md (project level, Claude Code compatible)
//  4. ~/.claude/agents/*.md (user level, Claude Code compatible)
//  5. Plugin agent paths
//
// Registry.Register overwrites by name, so sources load lowest-priority
// first — the highest-priority definition lands last and wins.
func LoadAgents(cwd string) {
	homeDir, _ := os.UserHomeDir()

	priorityOrdered := []agentSearchPath{
		{path: filepath.Join(confdir.Dir(cwd), "agents")},
		{path: filepath.Join(confdir.Dir(homeDir), "agents")},
		{path: filepath.Join(cwd, ".claude", "agents")},
		{path: filepath.Join(homeDir, ".claude", "agents")},
	}
	additionalAgentPathsMu.Lock()
	priorityOrdered = append(priorityOrdered, additionalAgentPaths...)
	additionalAgentPathsMu.Unlock()

	for _, sp := range slices.Backward(priorityOrdered) {

		loadAgentsFromDirWithNamespace(sp.path, sp.namespace)
	}
}

// loadAgentsFromDirWithNamespace loads agents with an optional namespace prefix.
// The path can be either a directory (scanned for .md files) or a direct file path.
func loadAgentsFromDirWithNamespace(path string, namespace string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}

	// If path is a file, load it directly
	if !info.IsDir() {
		if strings.HasSuffix(path, ".md") {
			loadAgentFromFileWithNamespace(path, namespace)
		}
		return
	}

	// Path is a directory, scan for .md files
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		filePath := filepath.Join(path, name)
		loadAgentFromFileWithNamespace(filePath, namespace)
	}
}

// loadAgentFromFileWithNamespace loads an agent with optional namespace.
func loadAgentFromFileWithNamespace(filePath string, namespace string) {
	config, err := parseAgentFile(filePath)
	if err != nil {
		log.Logger().Debug("Failed to parse agent file",
			zap.String("path", filePath),
			zap.Error(err))
		return
	}

	if config != nil {
		if namespace != "" && !strings.Contains(config.Name, ":") {
			config.Name = namespace + ":" + config.Name
			config.Source = "plugin"
		}

		defaultRegistry.Register(config)
		log.Logger().Info("Loaded agent",
			zap.String("name", config.Name),
			zap.String("source", filePath))
	}
}

// frontmatterAliases are alternate key spellings accepted in agent
// frontmatter: `tools` is Claude Code's key, the hyphenated forms are the
// spellings docs/guides/writing-a-subagent.md documents. Canonical keys on
// AgentConfig win when both are present.
type frontmatterAliases struct {
	Tools          ToolList       `yaml:"tools"`
	AllowedTools   ToolList       `yaml:"allowed-tools"`
	PermissionMode PermissionMode `yaml:"permission-mode"`
	MaxSteps       int            `yaml:"max_steps"`
}

func (a frontmatterAliases) applyTo(config *AgentConfig) {
	if config.AllowTools == nil {
		if a.AllowedTools != nil {
			config.AllowTools = a.AllowedTools
		} else if a.Tools != nil {
			config.AllowTools = a.Tools
		}
	}
	if config.PermissionMode == "" && a.PermissionMode != "" {
		config.PermissionMode = a.PermissionMode
	}
	if config.MaxSteps <= 0 && a.MaxSteps > 0 {
		config.MaxSteps = a.MaxSteps
	}
}

// parseAgentFile parses an AGENT.md file with YAML frontmatter.
func parseAgentFile(filePath string) (*AgentConfig, error) {
	frontmatter, _, err := markdown.ParseFrontmatterFile(filePath)
	if err != nil {
		return nil, err
	}
	if frontmatter == "" {
		return nil, nil
	}

	var config AgentConfig
	if err := yaml.Unmarshal([]byte(frontmatter), &config); err != nil {
		return nil, err
	}
	var aliases frontmatterAliases
	if err := yaml.Unmarshal([]byte(frontmatter), &aliases); err == nil {
		aliases.applyTo(&config)
	}

	config.PermissionMode = NormalizePermissionMode(string(config.PermissionMode))

	if config.Name == "" {
		config.Name = strings.TrimSuffix(filepath.Base(filePath), ".md")
	}
	if config.Model == "" {
		config.Model = "inherit"
	}
	if config.MaxSteps <= 0 {
		config.MaxSteps = defaultMaxSteps
	}
	if config.PermissionMode == "" {
		config.PermissionMode = PermissionDefault
	}

	// Body is lazily loaded via GetSystemPrompt()
	config.SourceFile = filePath

	if config.Source == "" {
		homeDir, _ := os.UserHomeDir()
		switch {
		case strings.HasPrefix(filePath, filepath.Join(confdir.Dir(homeDir), "agents")),
			strings.HasPrefix(filePath, filepath.Join(homeDir, ".claude", "agents")):
			config.Source = "user"
		default:
			config.Source = "project"
		}
	}

	return &config, nil
}

// LoadAgentSystemPrompt loads just the system prompt (body) from an agent file.
func LoadAgentSystemPrompt(filePath string) string {
	_, body, err := markdown.ParseFrontmatterFile(filePath)
	if err != nil {
		log.Logger().Debug("Failed to read agent file for system prompt",
			zap.String("path", filePath),
			zap.Error(err))
		return ""
	}
	return body
}
