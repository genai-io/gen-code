package anthropic

import (
	"strings"

	"github.com/genai-io/san/internal/llm"
)

const (
	ThinkingOff    = "off"
	ThinkingNormal = "think"
	ThinkingHigh   = "think+"
	ThinkingUltra  = "ultrathink"
)

var thinkingEfforts = []string{ThinkingOff, ThinkingNormal, ThinkingHigh, ThinkingUltra}

// alwaysThinkingEfforts drops "off" for a model that reasons unconditionally.
// Claude Fable 5 rejects an explicit thinking: {"type": "disabled"} with a 400,
// so offering the switch would only produce a failed turn.
var alwaysThinkingEfforts = []string{ThinkingNormal, ThinkingHigh, ThinkingUltra}

// thinkingStyle is how a model wants its reasoning configured. The two are not
// interchangeable: Claude 4.6 introduced adaptive thinking, and from Opus 4.7
// on a budget_tokens request is rejected outright rather than deprecated.
type thinkingStyle int

const (
	// styleNone marks a model that does not reason.
	styleNone thinkingStyle = iota
	// styleBudget is the pre-4.6 shape: thinking: {"type": "enabled",
	// "budget_tokens": N}. Also what the Anthropic-compatible third-party
	// endpoints implement.
	styleBudget
	// styleAdaptive is the 4.6-and-later shape: thinking: {"type":
	// "adaptive"} with the level carried in output_config.effort.
	styleAdaptive
)

type catalogEntry struct {
	match    func(string) bool
	info     llm.ModelInfo
	thinking thinkingStyle
	// alwaysThinks marks a model that cannot be asked not to reason.
	alwaysThinks bool
}

// anthropicCatalog lists the models San offers by default. Figures are from
// Anthropic's published model overview and pricing pages, last checked
// 2026-08-20. Entries are matched by prefix, so a dated snapshot ID
// ("claude-opus-4-5-20251101") resolves through its dateless prefix.
//
// Claude 4.6 and later carry the full 1M-token context window at standard
// pricing, which is why there is no separate long-context variant: the
// "[1m]" suffix that used to select a beta header is meaningless on them.
var anthropicCatalog = []catalogEntry{
	{
		match:        matchAnyPrefix("claude-fable-5"),
		info:         newModelInfo("claude-fable-5", "Claude Fable 5", "Claude Fable 5 (Most Capable)", 1000000, 128000),
		thinking:     styleAdaptive,
		alwaysThinks: true,
	},
	{
		match:    matchAnyPrefix("claude-opus-5"),
		info:     newModelInfo("claude-opus-5", "Claude Opus 5", "Claude Opus 5", 1000000, 128000),
		thinking: styleAdaptive,
	},
	{
		match:    matchAnyPrefix("claude-opus-4-8"),
		info:     newModelInfo("claude-opus-4-8", "Claude Opus 4.8", "Claude Opus 4.8", 1000000, 128000),
		thinking: styleAdaptive,
	},
	{
		match:    matchAnyPrefix("claude-opus-4-7"),
		info:     newModelInfo("claude-opus-4-7", "Claude Opus 4.7", "Claude Opus 4.7", 1000000, 128000),
		thinking: styleAdaptive,
	},
	{
		match:    matchAnyPrefix("claude-opus-4-6"),
		info:     newModelInfo("claude-opus-4-6", "Claude Opus 4.6", "Claude Opus 4.6", 1000000, 128000),
		thinking: styleAdaptive,
	},
	{
		match:    matchAnyPrefix("claude-opus-4-5"),
		info:     newModelInfo("claude-opus-4-5", "Claude Opus 4.5", "Claude Opus 4.5", 200000, 64000),
		thinking: styleBudget,
	},
	{
		match:    matchAnyPrefix("claude-sonnet-5"),
		info:     newModelInfo("claude-sonnet-5", "Claude Sonnet 5", "Claude Sonnet 5", 1000000, 128000),
		thinking: styleAdaptive,
	},
	{
		match:    matchAnyPrefix("claude-sonnet-4-6"),
		info:     newModelInfo("claude-sonnet-4-6", "Claude Sonnet 4.6", "Claude Sonnet 4.6", 1000000, 128000),
		thinking: styleAdaptive,
	},
	{
		match:    matchAnyPrefix("claude-sonnet-4-5"),
		info:     newModelInfo("claude-sonnet-4-5", "Claude Sonnet 4.5", "Claude Sonnet 4.5", 200000, 64000),
		thinking: styleBudget,
	},
	{
		match:    matchAnyPrefix("claude-haiku-4-5"),
		info:     newModelInfo("claude-haiku-4-5", "Claude Haiku 4.5", "Claude Haiku 4.5 (Fast)", 200000, 64000),
		thinking: styleBudget,
	},
}

func (c *Client) ThinkingEfforts(model string) []string {
	entry, ok := lookup(model)
	if !ok || entry.thinking == styleNone {
		return nil
	}
	if entry.alwaysThinks {
		return alwaysThinkingEfforts
	}
	return thinkingEfforts
}

func (c *Client) DefaultThinkingEffort(model string) string {
	entry, ok := lookup(model)
	if !ok || entry.thinking == styleNone {
		return ""
	}
	if entry.alwaysThinks {
		return ThinkingHigh
	}
	return ThinkingOff
}

func CatalogModel(modelID string) (llm.ModelInfo, bool) {
	entry, ok := lookup(modelID)
	if !ok {
		return llm.ModelInfo{}, false
	}
	info := entry.info
	info.ID = modelID
	return info, true
}

func lookup(modelID string) (catalogEntry, bool) {
	normalized := normalizeModelID(modelID)
	if normalized == "" {
		return catalogEntry{}, false
	}
	for _, entry := range anthropicCatalog {
		if entry.match(normalized) {
			return entry, true
		}
	}
	return catalogEntry{}, false
}

func supportsThinkingModel(modelID string) bool {
	entry, ok := lookup(modelID)
	return ok && entry.thinking != styleNone
}

// modelThinkingStyle reports how a model wants reasoning configured. An
// unknown model gets the budget shape: the Anthropic-compatible endpoints
// (MiniMax, Xiaomi MiMo, Volcengine Ark) reuse this client and implement only
// that one, and it is also what every Claude model before 4.6 accepts.
func modelThinkingStyle(modelID string) thinkingStyle {
	if entry, ok := lookup(modelID); ok {
		return entry.thinking
	}
	return styleBudget
}

func StaticModels() []llm.ModelInfo {
	models := make([]llm.ModelInfo, 0, len(anthropicCatalog))
	for _, entry := range anthropicCatalog {
		models = append(models, entry.info)
	}
	return models
}

func newModelInfo(id, name, displayName string, inputLimit, outputLimit int) llm.ModelInfo {
	return llm.ModelInfo{
		ID:               id,
		Name:             name,
		DisplayName:      displayName,
		InputTokenLimit:  inputLimit,
		OutputTokenLimit: outputLimit,
	}
}

func matchAnyPrefix(prefix string) func(string) bool {
	return func(model string) bool {
		return strings.HasPrefix(model, prefix)
	}
}

func normalizeModelID(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
}
