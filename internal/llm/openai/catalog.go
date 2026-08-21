package openai

import (
	"strings"

	"github.com/genai-io/san/internal/llm"
)

var reasoningEfforts = []string{"none", "low", "medium", "high", "xhigh"}
var gpt56ReasoningEfforts = []string{"none", "low", "medium", "high", "xhigh", "max"}
var highOnlyReasoningEfforts = []string{"high"}

func (c *Client) ThinkingEfforts(model string) []string {
	return openAIThinkingEfforts(model)
}

func (c *Client) DefaultThinkingEffort(model string) string {
	switch efforts := openAIThinkingEfforts(model); len(efforts) {
	case 0:
		return ""
	case 1:
		return efforts[0]
	default:
		return "medium"
	}
}

func openAIThinkingEfforts(model string) []string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(normalized, "gpt-5.6"):
		return gpt56ReasoningEfforts
	case strings.HasPrefix(normalized, "gpt-5.5"), strings.HasPrefix(normalized, "gpt-5.4"), strings.HasPrefix(normalized, "gpt-6"):
		return reasoningEfforts
	case strings.HasPrefix(normalized, "gpt-5"), strings.HasPrefix(normalized, "o1"), strings.HasPrefix(normalized, "o3"), strings.HasPrefix(normalized, "o4"), strings.Contains(normalized, "codex"):
		return highOnlyReasoningEfforts
	default:
		return nil
	}
}

// openAIModelInfo builds catalog metadata for a model ID. It deliberately
// leaves Reasoning unset: the standard /v1/models catalog advertises no
// reasoning levels, and freezing our static name-based guesses into the
// on-disk cache would mask a later binary's updated rules, since the cache is
// read ignoring TTL. Only live catalogs that actually advertise levels — the
// ChatGPT subscription /models response — attach Reasoning (see subscription.go);
// everything else falls back to the provider's static ThinkingEffortProvider
// rules at resolution time.
func openAIModelInfo(modelID string) llm.ModelInfo {
	input, output := openAILimits(modelID)
	return llm.ModelInfo{
		ID:               modelID,
		Name:             modelID,
		DisplayName:      modelID,
		InputTokenLimit:  input,
		OutputTokenLimit: output,
	}
}

// openAILimits returns known context/output windows for OpenAI model IDs.
// OpenAI's /v1/models endpoint doesn't include limits, so we rely on the
// published per-model pages. Returns 0,0 for unrecognized IDs.
//
// Last checked 2026-08-20 against developers.openai.com: the whole GPT-5
// family — 5.4 through the 5.6 variants — carries a 1,050,000-token window and
// a 128,000-token output cap.
func openAILimits(modelID string) (input, output int) {
	m := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.HasPrefix(m, "gpt-6"),
		strings.HasPrefix(m, "gpt-5"),
		strings.Contains(m, "codex"):
		return 1_050_000, 128_000
	case strings.HasPrefix(m, "o1"),
		strings.HasPrefix(m, "o3"),
		strings.HasPrefix(m, "o4"):
		return 200_000, 100_000
	case strings.HasPrefix(m, "gpt-4.1"):
		return 1_047_576, 32_768
	case strings.HasPrefix(m, "gpt-4o"):
		return 128_000, 16_384
	}
	return 0, 0
}
