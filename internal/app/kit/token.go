package kit

import (
	"fmt"

	"github.com/genai-io/san/internal/llm"
)

// TokenLimitResultMsg is sent when a token limit fetch completes.
type TokenLimitResultMsg struct {
	Result string
	Err    error
}

// FormatTokenCount formats a token count for display.
func FormatTokenCount(count int) string {
	switch {
	case count >= 1000000:
		return fmt.Sprintf("%.1fM", float64(count)/1000000)
	case count >= 1000:
		return fmt.Sprintf("%.1fk", float64(count)/1000)
	default:
		return fmt.Sprintf("%d", count)
	}
}

// runeClass groups characters the way a BPE tokenizer's pre-tokenizer splits
// them: it breaks text into runs of letters, digits, punctuation, and
// whitespace before merging anything, so a run never spans two classes.
type runeClass int

const (
	classLetter runeClass = iota
	classDigit
	classPunct
	classSpace
	// classWide is CJK and every other non-ASCII rune. Held apart because
	// these carry roughly a token each rather than merging into long runs.
	classWide
)

func classify(r rune) runeClass {
	switch {
	case r >= 0x80:
		return classWide
	case r == ' ' || r == '\t' || r == '\n' || r == '\r':
		return classSpace
	case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		return classLetter
	case r >= '0' && r <= '9':
		return classDigit
	default:
		return classPunct
	}
}

// runTokens estimates how many tokens a single run of n same-class characters
// becomes. The ratios approximate what BPE merging does within each class:
// words merge aggressively (a five-letter word is one token), digits merge in
// groups of about three, punctuation merges only in short common pairs, and
// every non-ASCII rune stands roughly on its own.
func runTokens(class runeClass, n int) int {
	switch class {
	case classLetter:
		return max((n+2)/4, 1)
	case classDigit, classPunct:
		// Both merge, but only in short groups: tokenizers chunk digits about
		// three at a time, and JSON and code are dominated by short punctuation
		// runs learned as single units — `":"`, `":{"`, `!=`, `:=`, `))`. So
		// each sits well above one-token-per-character and well below prose.
		// (The classes stay separate because they decide where runs break —
		// `12+34` is three runs, not one — only the ratio is shared.)
		return max((n+2)/3, 1)
	case classWide:
		return n
	default: // classSpace
		// A lone space is absorbed into the token that follows it — " the" is
		// one token, not two. Longer runs (indentation, blank lines) do cost.
		if n <= 1 {
			return 0
		}
		return max((n+3)/4, 1)
	}
}

// EstimateTokens approximates what a string costs in tokens without running a
// tokenizer.
//
// It counts by pre-token run rather than by a flat characters-per-token ratio,
// because a flat ratio is wrong in exactly the places /context cares about
// most. The familiar "4 characters per token" holds for English prose, but tool
// schemas and tool results are punctuation-dense JSON and code, where BPE emits
// far more tokens per character — measuring those against a prose ratio
// understates the two largest categories in the breakdown. The same reasoning
// separates CJK, which sits near one token per character; a single ratio reads
// a Chinese AGENTS.md as a quarter of its real size.
//
// The result is an estimate and is always presented as one: only the provider
// reports exact counts, and /context labels which numbers are which.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}

	// runLength == 0 means no run has opened yet, so the seeded class is
	// never used to close one.
	tokens, runLength, runClass := 0, 0, classLetter
	for _, r := range s {
		class := classify(r)
		if runLength > 0 && class != runClass {
			tokens += runTokens(runClass, runLength)
			runLength = 0
		}
		runClass, runLength = class, runLength+1
	}
	tokens += runTokens(runClass, runLength)

	return max(tokens, 1)
}

// GetMaxTokens returns the effective output limit, falling back to defaultMaxTokens.
func GetMaxTokens(store *llm.Store, currentModel *llm.CurrentModelInfo, defaultMaxTokens int) int {
	if limit := getEffectiveOutputLimit(store, currentModel); limit > 0 {
		return limit
	}
	return defaultMaxTokens
}

// GetModelTokenLimits returns the cached context window for the current model.
//
// The same model ID can be cached under several provider/auth keys with
// different windows (gpt-5.5: 400k via Direct API, 272k via ChatGPT
// subscription). Scanning the cache map for the ID picks a random one each
// render — that is what made the status-bar limit flicker. So we resolve in two
// deterministic steps:
//
//  1. this model's own provider+auth cache — the correct window. Ignores the 24h
//     TTL, otherwise an expired cache would fall to step 2 and flicker again.
//  2. else the largest window for the ID across all caches — covers a model an
//     aggregator serves with no window while its native provider knows the real one.
func GetModelTokenLimits(store *llm.Store, currentModel *llm.CurrentModelInfo) (inputLimit, outputLimit int) {
	if store == nil || currentModel == nil {
		return 0, 0
	}
	authMethod := store.ResolveAuthMethod(currentModel)
	if input, output := store.CachedModelLimitsForProvider(currentModel.Provider, authMethod, currentModel.ModelID); input > 0 {
		return input, output
	}
	return store.CachedModelLimits(currentModel.ModelID)
}

// getEffectiveOutputLimit returns the output cap: a custom limit if set,
// otherwise the cached model metadata. The input side has its own resolver
// (llm.Store.EffectiveInputLimit) because it also honors an env override and
// is shared with the agent's compaction check.
func getEffectiveOutputLimit(store *llm.Store, currentModel *llm.CurrentModelInfo) int {
	if currentModel == nil {
		return 0
	}

	if store != nil {
		if _, output, ok := store.GetTokenLimit(currentModel.ModelID); ok {
			return output
		}
	}

	_, output := GetModelTokenLimits(store, currentModel)
	return output
}

// GetEffectiveInputLimit returns the context window for the status bar's
// percentage, or 0 when it is unknown (no model selected, or a model whose
// window San cannot size) — the bar renders that as "--" rather than a
// percentage of a guess.
//
// It delegates to llm.Store.EffectiveInputLimit, the same resolver
// llm.Client.InputLimit uses for the auto-compaction trigger, so the bar can
// never fill against a different window than the one compaction fires on
// (issue #338).
func GetEffectiveInputLimit(store *llm.Store, currentModel *llm.CurrentModelInfo) int {
	if store == nil || currentModel == nil {
		return 0
	}
	auth := store.ResolveAuthMethod(currentModel)
	return store.EffectiveInputLimit(currentModel.Provider, auth, currentModel.ModelID)
}
