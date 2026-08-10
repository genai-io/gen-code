package app

import (
	"context"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/app/conv"
	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/reminder"
)

// A user turn carries the skills directory and memory files inline, so the
// same bytes are reachable two ways. They must be credited to their own
// category and removed from Messages — counting them twice would inflate the
// conversation and hide where the window actually went.
func TestAddContextUsageAttributesRemindersOnce(t *testing.T) {
	skills := reminder.WrapWithSource("skills directory body", reminder.ProviderSkillsDirectory)
	memory := reminder.WrapWithSource(reminder.WrapMemory("project", "project memory body"), reminder.ProviderMemoryProject)
	turn := reminder.AttachToContent("what the user typed", []string{skills, memory})

	var usage conv.ContextUsage
	addContextUsage(&usage, turn)

	if usage.Skills != kit.EstimateTokens(skills) {
		t.Errorf("Skills = %d, want %d", usage.Skills, kit.EstimateTokens(skills))
	}
	if usage.MemoryFiles != kit.EstimateTokens(memory) {
		t.Errorf("MemoryFiles = %d, want %d", usage.MemoryFiles, kit.EstimateTokens(memory))
	}
	if whole := kit.EstimateTokens(turn); usage.Messages >= whole {
		t.Errorf("Messages = %d counts the reminders again; the whole turn is only %d", usage.Messages, whole)
	}
	if usage.Messages == 0 {
		t.Error("the user's own text was dropped from Messages")
	}
}

// A one-time notice belongs to the turn that carried it, not to a category of
// its own — there is nothing the user can configure to make it smaller.
func TestAddContextUsageKeepsNoticesInMessages(t *testing.T) {
	turn := reminder.AttachToContent("what the user typed", []string{reminder.Wrap("a cancel notice")})

	var usage conv.ContextUsage
	addContextUsage(&usage, turn)

	if usage.Skills != 0 || usage.MemoryFiles != 0 {
		t.Errorf("an unsourced notice was filed as skills=%d memory=%d", usage.Skills, usage.MemoryFiles)
	}
	if want := kit.EstimateTokens(turn); usage.Messages != want {
		t.Errorf("Messages = %d, want the whole turn %d", usage.Messages, want)
	}
}

func TestMessageWireCoversEveryFieldSentToTheProvider(t *testing.T) {
	msg := core.Message{
		Role:           core.RoleAssistant,
		Content:        "visible answer",
		DisplayContent: "rendered for the TUI only",
		Thinking:       "reasoning text",
		ToolCalls:      []core.ToolCall{{ID: "1", Name: "Bash", Input: `{"command":"ls"}`}},
		ToolResult:     &core.ToolResult{ToolCallID: "1", Content: "tool output"},
	}

	wire := messageWire(msg)
	for _, want := range []string{"visible answer", "reasoning text", "Bash", `{"command":"ls"}`, "tool output"} {
		if !strings.Contains(wire, want) {
			t.Errorf("wire text is missing %q: %q", want, wire)
		}
	}
	if strings.Contains(wire, "rendered for the TUI only") {
		t.Errorf("display-only content is not sent and must not be counted: %q", wire)
	}
}

func TestToolSchemaWireCoversTheWholeDefinition(t *testing.T) {
	wire := toolSchemaWire(core.ToolSchema{
		Name:        "Bash",
		Description: "run a command",
		Parameters:  map[string]any{"type": "object"},
	})

	for _, want := range []string{"Bash", "run a command", "input_schema"} {
		if !strings.Contains(wire, want) {
			t.Errorf("schema text is missing %q: %q", want, wire)
		}
	}
}

// automaticCachingProvider stands in for the majority: it reports cache tokens
// but chooses its own prefix boundary, so it does not declare
// llm.PromptPrefixCacheProvider.
type automaticCachingProvider struct{}

func (automaticCachingProvider) Stream(context.Context, llm.CompletionOptions) <-chan llm.StreamChunk {
	return nil
}
func (automaticCachingProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (automaticCachingProvider) Name() string                                        { return "stub" }

// prefixCachingProvider stands in for Anthropic: its cache breakpoint sits at
// the end of the system prompt, so the reported prefix is the prompt and tools.
type prefixCachingProvider struct{ automaticCachingProvider }

func (prefixCachingProvider) CachesToolsAndSystemPrompt() bool { return true }

// measuredPromptPrefix decides whether to believe the provider's cache
// accounting. The guard exists because a real number can measure the wrong
// thing: a moved breakpoint or a provider caching on its own boundary reports
// a prefix that covers more than the prompt. The estimate is crude but not
// wrong by multiples, so a reading that disagrees on that scale is rejected
// and the display falls back to estimation.
func TestMeasuredPromptPrefixRejectsImplausibleReadings(t *testing.T) {
	usage := conv.ContextUsage{
		Measured:     41200,
		SystemPrompt: 5000,
		Tools:        19000,
		MCPTools:     3000,
	}
	const estimated = 27000 // the three cached categories

	for name, tc := range map[string]struct {
		cachedPrefix int
		want         int
	}{
		"agrees with the estimate":  {26000, 26000},
		"estimate ran a little low": {estimated * 3 / 2, estimated * 3 / 2},
		"covers far more than the prompt": {
			// A prefix that swallowed the conversation too.
			estimated * 3, 0,
		},
		"implausibly small":     {estimated / 4, 0},
		"nothing cached":        {0, 0},
		"at the measured total": {usage.Measured, 0},
		"beyond the total":      {usage.Measured + 1, 0},
	} {
		m := &model{}
		m.env.CachedPrefixTokens = tc.cachedPrefix
		m.env.LLMProvider = prefixCachingProvider{}

		if got := m.measuredPromptPrefix(usage); got != tc.want {
			t.Errorf("%s: measuredPromptPrefix(%d) = %d, want %d", name, tc.cachedPrefix, got, tc.want)
		}
	}
}

// Providers that cache on a boundary they choose report a real number that
// measures an unknown span — believing it would overstate the prompt.
func TestMeasuredPromptPrefixIgnoresProvidersWithoutAKnownBreakpoint(t *testing.T) {
	usage := conv.ContextUsage{Measured: 41200, SystemPrompt: 5000, Tools: 19000, MCPTools: 3000}

	m := &model{}
	m.env.CachedPrefixTokens = 26000 // plausible, but not a prompt-prefix measurement
	m.env.LLMProvider = automaticCachingProvider{}

	if got := m.measuredPromptPrefix(usage); got != 0 {
		t.Errorf("measuredPromptPrefix = %d, want 0 for a provider with no declared breakpoint", got)
	}
}
