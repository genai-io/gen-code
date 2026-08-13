// Package subagent provides subagent execution for San.
// Subagents are specialized LLM instances that can be spawned to handle
// specific tasks with isolated contexts and tool restrictions.
package subagent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"gopkg.in/yaml.v3"
)

// PermissionMode controls the default permission policy for an agent.
// See docs/concepts/permission-model.md for the full pipeline.
type PermissionMode string

const (
	// PermissionDefault: reads auto, mutations Ask. In a subagent Ask
	// collapses to Deny, so mutations are blocked.
	PermissionDefault PermissionMode = "default"

	// PermissionAcceptEdits: reads + edit/write auto, Bash/exec/agent Ask.
	PermissionAcceptEdits PermissionMode = "acceptEdits"

	// PermissionExplore: reads auto, mutations explicitly Deny (never Ask).
	// Used for research and read-only investigation.
	PermissionExplore PermissionMode = "explore"

	// PermissionBypass: everything Allow after deny_tools and the
	// root/home-removal circuit breaker, which still gate the call.
	PermissionBypass PermissionMode = "bypassPermissions"

	// PermissionDontAsk: reads auto, everything else silently Deny.
	// TODO: not yet wired into the main loop pipeline; behaves as
	// PermissionDefault in subagent context (Ask -> Deny is automatic).
	PermissionDontAsk PermissionMode = "dontAsk"

	// PermissionAuto: long-running subagent mode. Auto-approves more than
	// acceptEdits, including benign Bash, with a safety classifier on the
	// rest. TODO: classifier not implemented; treated as PermissionAcceptEdits.
	PermissionAuto PermissionMode = "auto"
)

// NormalizePermissionMode trims a permission mode string and folds accepted
// aliases onto the canonical constants. "edit" is the spelling the Agent
// tool's schema exposes; "bypass" and "readonly" appear in agent frontmatter.
// Empty string defaults to PermissionDefault.
func NormalizePermissionMode(s string) PermissionMode {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "":
		return PermissionDefault
	case "edit", "acceptedits":
		return PermissionAcceptEdits
	case "explore", "readonly", "read-only":
		return PermissionExplore
	case "bypass", "bypasspermissions", "yolo":
		return PermissionBypass
	case "dontask", "dont-ask":
		return PermissionDontAsk
	case "auto":
		return PermissionAuto
	case "default":
		return PermissionDefault
	}
	return PermissionMode(s)
}

// ToolRule is a single allow/deny entry. Pattern follows the unified
// "Tool(pattern)" glob syntax shared with settings.permissions — see
// docs/concepts/permission-model.md.
type ToolRule struct {
	Name    string `yaml:"name" json:"name"`
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
}

// Rule renders the rule as "Tool(pattern)" for the matcher, or just "Tool"
// when there is no pattern.
func (r ToolRule) Rule() string {
	if r.Pattern == "" {
		return r.Name
	}
	return r.Name + "(" + r.Pattern + ")"
}

// ToolList is a list of tool rules. nil means "all tools" in allow contexts.
// A bare entry covers the whole tool; a parameterized entry covers only
// calls whose arguments match the pattern.
type ToolList []ToolRule

func ToolNames(names ...string) ToolList {
	tools := make(ToolList, 0, len(names))
	for _, name := range names {
		tools = append(tools, ToolRule{Name: name})
	}
	return tools
}

func (t ToolList) Names() []string {
	if t == nil {
		return nil
	}
	names := make([]string, 0, len(t))
	for _, rule := range t {
		if rule.Name != "" {
			names = append(names, rule.Name)
		}
	}
	return names
}

func (t ToolList) BareNames() []string {
	if t == nil {
		return nil
	}
	names := make([]string, 0, len(t))
	for _, rule := range t {
		if rule.Name != "" && rule.Pattern == "" {
			names = append(names, rule.Name)
		}
	}
	return names
}

// DisplayNames renders rules for UI listing: "Read" or "Bash(git diff*)".
func (t ToolList) DisplayNames() []string {
	names := make([]string, 0, len(t))
	for _, rule := range t {
		if rule.Name == "" {
			continue
		}
		names = append(names, rule.Rule())
	}
	return names
}

// ConstrainedDisplayNames returns only rules with parameter constraints,
// for surfacing in the agent's system prompt.
func (t ToolList) ConstrainedDisplayNames() []string {
	names := make([]string, 0, len(t))
	for _, rule := range t {
		if rule.Pattern == "" {
			continue
		}
		names = append(names, rule.Rule())
	}
	return names
}

func (t ToolList) HasName(name string) bool {
	for _, rule := range t {
		if rule.Name == name {
			return true
		}
	}
	return false
}

func (t ToolList) HasPattern(name string) bool {
	for _, rule := range t {
		if rule.Name == name && rule.Pattern != "" {
			return true
		}
	}
	return false
}

// UnmarshalYAML accepts every form documented in docs/concepts/permission-model.md:
//   - string list:    "Read, Write, Bash"        (comma-separated)
//   - sequence:       [Read, Write, Bash(git diff*)]
//   - sequence map:   [{name: Bash, pattern: "git diff*"}]
//   - mapping:        {Read: true, Bash: "git diff*"}
func (t *ToolList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		for p := range strings.SplitSeq(value.Value, ",") {
			if rule, ok := parseRuleString(p); ok {
				*t = append(*t, rule)
			}
		}
	case yaml.SequenceNode:
		for _, item := range value.Content {
			rules, err := parseToolRuleNode(item)
			if err != nil {
				return err
			}
			*t = append(*t, rules...)
		}
	case yaml.MappingNode:
		rules, err := parseToolRuleNode(value)
		if err != nil {
			return err
		}
		*t = append(*t, rules...)
	}
	return nil
}

// parseRuleString accepts "Read" or "Bash(git diff*)" and returns a ToolRule.
func parseRuleString(s string) (ToolRule, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ToolRule{}, false
	}
	open := strings.IndexByte(s, '(')
	if open < 0 || !strings.HasSuffix(s, ")") {
		return ToolRule{Name: s}, true
	}
	name := strings.TrimSpace(s[:open])
	if name == "" {
		return ToolRule{}, false
	}
	return ToolRule{Name: name, Pattern: strings.TrimSpace(s[open+1 : len(s)-1])}, true
}

func parseToolRuleNode(node *yaml.Node) ([]ToolRule, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		if rule, ok := parseRuleString(node.Value); ok {
			return []ToolRule{rule}, nil
		}
		return nil, nil
	case yaml.MappingNode:
		return parseToolRuleMap(node)
	default:
		return nil, fmt.Errorf("unsupported tool rule format")
	}
}

func parseToolRuleMap(node *yaml.Node) ([]ToolRule, error) {
	var rules []ToolRule
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		value := node.Content[i+1]
		// Object form: { name: Bash, pattern: "git diff*" }.
		if key == "name" || key == "pattern" {
			if rule := parseObjectToolRule(node); rule.Name != "" {
				return []ToolRule{rule}, nil
			}
			return nil, nil
		}
		rule, ok, err := parseNamedToolRule(key, value)
		if err != nil {
			return nil, err
		}
		if ok {
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

// parseObjectToolRule reads { name: Bash, pattern: "git diff*" }.
func parseObjectToolRule(node *yaml.Node) ToolRule {
	var rule ToolRule
	for i := 0; i+1 < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "name":
			rule.Name = strings.TrimSpace(node.Content[i+1].Value)
		case "pattern":
			rule.Pattern = strings.TrimSpace(node.Content[i+1].Value)
		}
	}
	return rule
}

// parseNamedToolRule reads a tool-name keyed entry:
//
//	Read: true                         → {Name: Read}
//	Read: false                        → omitted
//	Bash: "git diff*"                  → {Name: Bash, Pattern: "git diff*"}
//	Bash: { pattern: "git diff*" }     → {Name: Bash, Pattern: "git diff*"}
func parseNamedToolRule(name string, value *yaml.Node) (ToolRule, bool, error) {
	switch value.Kind {
	case yaml.ScalarNode:
		switch value.Value {
		case "true":
			return ToolRule{Name: name}, true, nil
		case "false", "":
			return ToolRule{}, false, nil
		default:
			return ToolRule{Name: name, Pattern: strings.TrimSpace(value.Value)}, true, nil
		}
	case yaml.MappingNode:
		var pattern string
		for i := 0; i+1 < len(value.Content); i += 2 {
			if value.Content[i].Value == "pattern" {
				pattern = strings.TrimSpace(value.Content[i+1].Value)
			}
		}
		return ToolRule{Name: name, Pattern: pattern}, true, nil
	}
	return ToolRule{}, false, fmt.Errorf("unsupported rule for tool %q", name)
}

// AgentConfig defines an agent.
type AgentConfig struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Color       string `yaml:"color,omitempty" json:"color,omitempty"`
	WhenToUse   string `yaml:"when-to-use,omitempty" json:"when_to_use,omitempty"`
	// Model selects the model for this agent. Accepted forms:
	//   - "inherit" or empty — use the parent conversation's model
	//   - an alias ("opus", "sonnet", "haiku") or a bare model id served by the
	//     parent's provider
	//   - "vendor/model" (e.g. "deepseek/deepseek-v4") to route to another
	//     connected provider; the named vendor must be connected
	Model string `yaml:"model" json:"model"`

	// PermissionMode is the default policy when no allow/deny rule matches.
	PermissionMode PermissionMode `yaml:"mode" json:"mode"`

	// AllowTools is the per-agent allow list. Same Tool(pattern) syntax as
	// settings.permissions.allow. nil = all tools available; non-nil also
	// acts as a schema filter (LLM only sees these tools).
	AllowTools ToolList `yaml:"allow_tools" json:"allow_tools"`

	// DenyTools is the per-agent deny list. Same syntax as
	// settings.permissions.deny. Always wins over AllowTools and mode.
	DenyTools ToolList `yaml:"deny_tools,omitempty" json:"deny_tools,omitempty"`

	Skills       []string `yaml:"skills,omitempty" json:"skills,omitempty"`
	SystemPrompt string   `yaml:"system-prompt,omitempty" json:"system_prompt,omitempty"`
	MaxSteps     int      `yaml:"max-steps" json:"max_steps"`
	Source       string   `yaml:"-" json:"source,omitempty"`
	McpServers   []string `yaml:"mcp-servers,omitempty" json:"mcp_servers,omitempty"`
	SourceFile   string   `yaml:"-" json:"-"`

	// displayOnly marks a runtime-only base-template config whose Name is just a
	// UI label. It is never loaded from an agent definition.
	displayOnly bool

	systemPromptOnce sync.Once `yaml:"-" json:"-"`
}

// GetSystemPrompt returns the system prompt, loading it lazily if needed.
func (c *AgentConfig) GetSystemPrompt() string {
	c.systemPromptOnce.Do(func() {
		if c.SourceFile != "" {
			if prompt := LoadAgentSystemPrompt(c.SourceFile); prompt != "" {
				c.SystemPrompt = prompt
			}
		}
	})
	return c.SystemPrompt
}

// AgentResult contains the result of an agent execution
type AgentResult struct {
	AgentID        string
	AgentName      string
	Model          string
	Success        bool
	Content        string
	TranscriptPath string
	Messages       []core.Message
	StepCount      int
	ToolUses       int
	TokenUsage     llm.Usage
	Duration       time.Duration
	Activity       []string
	Error          string
}

const (
	baseAgentDescription = "Subagent for research and implementation tasks."

	// defaultMaxSteps is both the default and minimum number of LLM inference steps.
	defaultMaxSteps = 500
)

// modelAliases maps short model aliases to current-generation model ids.
// Un-dated ids resolve on Anthropic-family providers (API and Vertex both
// serve these prefixes); a miss falls back to the parent model via
// shouldRetryWithParentModel.
var modelAliases = map[string]string{
	"sonnet": "claude-sonnet-4-6",
	"opus":   "claude-opus-4-7",
	"haiku":  "claude-haiku-4-5",
}

// resolveModelAlias returns the full model ID for a known alias,
// or the input unchanged if it is not an alias.
func resolveModelAlias(model string) string {
	if full, ok := modelAliases[model]; ok {
		return full
	}
	return model
}
