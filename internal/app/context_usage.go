// Context-window accounting for /context: what currently occupies the
// model's context window, broken down by category.
package app

import (
	"encoding/json"
	"strings"

	"go.uber.org/zap"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/log"
	"github.com/genai-io/san/internal/mcp"
	"github.com/genai-io/san/internal/reminder"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// contextUsage measures the running agent wherever there is one: System() and
// Tools() are the values actually being sent, and its chain already carries the
// harness reminders attached to past user turns. Building the prompt fresh
// would report what the current settings would produce, which is not
// necessarily what the live agent is holding — a session keeps its agent until
// something forces a rebuild. Only before the first message, when no agent
// exists yet, does the measurement fall back to building.
//
// Every category is an estimate — the provider reports one exact prompt size
// and no breakdown, so the split has to be derived from the text itself.
func (m *model) contextUsage() conv.ContextUsage {
	usage := conv.ContextUsage{
		ModelName: m.env.GetModelDisplayName(),
		Limit:     kit.GetEffectiveInputLimit(m.services.LLM.Store(), m.env.CurrentModel),
		Measured:  m.env.InputTokens,
	}

	sys, tools := m.services.Agent.System(), m.services.Agent.Tools()
	messages := m.services.Agent.Messages()
	if sys == nil || tools == nil {
		m.addUnstartedAgentUsage(&usage)
		messages = m.seedAgentMessages("")
	} else {
		usage.SystemPrompt = kit.EstimateTokens(sys.Prompt())
		for _, t := range tools.All() {
			size := kit.EstimateTokens(toolSchemaWire(t.Schema()))
			if mcp.IsMCPTool(t.Schema().Name) {
				usage.MCPTools += size
				continue
			}
			usage.Tools += size
		}
	}

	// Skills and memory ride inside user turns as <system-reminder> blocks, so
	// the chain is scanned for them instead of measuring the providers
	// separately — otherwise every one of them would be counted twice, once on
	// its own and once as part of Messages.
	for _, msg := range messages {
		addContextUsage(&usage, messageWire(msg))
	}
	// Reminders queued for the next turn are not in the chain yet. Counting
	// them keeps a fresh session from reporting zero skills and zero memory
	// when both are about to be injected on the very next message.
	for _, pending := range m.services.Reminder.Pending() {
		addContextUsage(&usage, pending)
	}

	usage.CachedPrefix = m.measuredPromptPrefix(usage)
	return usage
}

// measuredPromptPrefix returns the provider's exact token count for the system
// prompt plus the tool definitions, or 0 when no trustworthy count is
// available. Anthropic marks the system block as its cache breakpoint and
// renders tools ahead of system, so the cached prefix it reports is exactly
// those two — the one part of the window San can measure rather than estimate.
//
// The reading is corroborated against the estimate before being used, rather
// than trusted on the strength of how the request happens to be built today.
// A provider that caches on its own boundary, a moved breakpoint, or a prefix
// that grew to include conversation would all report a number that is real but
// measures something else; the estimate is crude but it is not wrong by
// multiples, so disagreement on that scale means the assumption no longer
// holds. Returning 0 then falls the display back to scaling everything to the
// measured total, which is the same honest answer every other provider gets.
func (m *model) measuredPromptPrefix(usage conv.ContextUsage) int {
	prefix := m.env.CachedPrefixTokens
	if prefix <= 0 || prefix >= usage.Measured {
		// Nothing cached — no turn yet, or a prefix below the model's
		// cacheable minimum (512–4096 tokens, depending on the model, so
		// small toolsets routinely miss it). A prefix at or above the whole
		// prompt would leave the conversation nothing and is not a prefix.
		return 0
	}
	if !llm.CachesToolsAndSystemPrompt(m.env.LLMProvider) {
		return 0
	}

	estimated := usage.SystemPrompt + usage.Tools + usage.MCPTools
	if estimated <= 0 {
		return 0
	}
	if ratio := float64(prefix) / float64(estimated); ratio < 0.5 || ratio > 2 {
		log.Logger().Warn("cached prefix disagrees with the estimated prompt size; falling back to estimation",
			zap.Int("cached_prefix_tokens", prefix),
			zap.Int("estimated_tokens", estimated))
		return 0
	}
	return prefix
}

// addUnstartedAgentUsage fills in the prompt and toolset for a session whose
// agent has not been built yet. The agent starts on the first message, so
// until then there is nothing to read System() and Tools() off of, and
// reporting zero would understate a fresh window by everything the very next
// request is about to carry.
//
// It goes through promptParams — the same side-effect-free constructor
// buildAgentParams starts from — so both read one description of what shapes
// the prompt. A new prompt-shaping input added there reaches this measurement
// without anyone remembering to update it, which a second hand-written literal
// would not.
func (m *model) addUnstartedAgentUsage(usage *conv.ContextUsage) {
	params := m.promptParams()

	usage.SystemPrompt = kit.EstimateTokens(params.System().Prompt())
	for _, schema := range params.Schemas() {
		usage.Tools += kit.EstimateTokens(toolSchemaWire(schema))
	}
	// MCP tools arrive as ready-made core.Tools rather than schemas, so they sit
	// outside Schemas() and are counted off the same list params already carries.
	for _, t := range params.MCPTools {
		usage.MCPTools += kit.EstimateTokens(toolSchemaWire(t.Schema()))
	}
}

// addContextUsage attributes one message's wire text: each <system-reminder>
// block is credited to the provider that emitted it, and whatever is left over
// counts as conversation.
func addContextUsage(usage *conv.ContextUsage, text string) {
	conversation := kit.EstimateTokens(text)
	for _, block := range reminder.Blocks(text) {
		size := kit.EstimateTokens(block.Text)
		switch block.Source {
		case reminder.ProviderSkillsDirectory:
			usage.Skills += size
			conversation -= size
		case reminder.ProviderMemoryUser, reminder.ProviderMemoryProject, reminder.ProviderMemoryAuto:
			usage.MemoryFiles += size
			conversation -= size
		}
		// A block with no source is a one-time notice (a cancel notice, hook
		// output). It belongs to the turn that carried it, so it stays in
		// Messages rather than earning a category of its own.
	}
	usage.Messages += max(conversation, 0)
}

// messageWire concatenates the parts of a message that reach the provider.
// Display-only fields (DisplayContent, the rendered view state) are left out;
// images are not text and are not estimated.
func messageWire(msg core.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case ai.BlockText, ai.BlockThinking:
			b.WriteString(block.Text)
		case ai.BlockToolCall:
			if block.ToolCall != nil {
				b.WriteString(block.ToolCall.Name)
				b.WriteString(block.ToolCall.Input)
			}
		case ai.BlockToolResult:
			if block.ToolResult != nil {
				b.WriteString(block.ToolResult.Content.Text())
			}
		}
	}
	return b.String()
}

// toolSchemaWire renders a tool definition the way it is sent — name,
// description, and JSON Schema — so the estimate covers the whole definition
// and not just its prose.
func toolSchemaWire(schema core.ToolSchema) string {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return schema.Name + schema.Description
	}
	return string(encoded)
}
