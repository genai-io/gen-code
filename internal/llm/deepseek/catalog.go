package deepseek

import "github.com/genai-io/san/internal/llm"

type pricing struct {
	inputPerMTokens      float64
	outputPerMTokens     float64
	cacheReadPerMTokens  float64
	cacheWritePerMTokens float64
}

type modelCatalogEntry struct {
	info    llm.ModelInfo
	pricing pricing
}

// Prices are USD per million tokens, from
// https://api-docs.deepseek.com/quick_start/pricing (last checked 2026-08-20).
// inputPerMTokens is the cache-miss rate and cacheReadPerMTokens the cache-hit
// rate; DeepSeek does not bill cache writes separately, so the write rate
// mirrors the miss rate.
//
// These are the standard (peak) rates. DeepSeek bills 50% of them off-peak —
// outside 09:00-12:00 and 14:00-18:00 Beijing time — which a single rate card
// cannot express, so a turn's estimate is an upper bound during those hours.
var catalog = []modelCatalogEntry{
	{
		info: llm.ModelInfo{
			ID:               "deepseek-v4-flash",
			Name:             "DeepSeek V4 Flash",
			DisplayName:      "DeepSeek V4 Flash",
			InputTokenLimit:  1_000_000,
			OutputTokenLimit: 384000,
		},
		pricing: pricing{inputPerMTokens: 0.44, outputPerMTokens: 1.32, cacheReadPerMTokens: 0.014, cacheWritePerMTokens: 0.44},
	},
	{
		info: llm.ModelInfo{
			ID:               "deepseek-v4-pro",
			Name:             "DeepSeek V4 Pro",
			DisplayName:      "DeepSeek V4 Pro",
			InputTokenLimit:  1_000_000,
			OutputTokenLimit: 384000,
		},
		pricing: pricing{inputPerMTokens: 1.32, outputPerMTokens: 3.96, cacheReadPerMTokens: 0.044, cacheWritePerMTokens: 1.32},
	},
}

func StaticModels() []llm.ModelInfo {
	models := make([]llm.ModelInfo, len(catalog))
	for i, entry := range catalog {
		models[i] = entry.info
	}
	return models
}

func CatalogModel(modelID string) (llm.ModelInfo, bool) {
	for _, entry := range catalog {
		if entry.info.ID == modelID {
			return entry.info, true
		}
	}
	return llm.ModelInfo{}, false
}

func EstimateCost(modelID string, usage llm.Usage) (llm.Money, bool) {
	for _, entry := range catalog {
		if entry.info.ID != modelID {
			continue
		}
		const perMillion = 1_000_000.0
		cost := (float64(usage.InputTokens) / perMillion) * entry.pricing.inputPerMTokens
		cost += (float64(usage.OutputTokens) / perMillion) * entry.pricing.outputPerMTokens
		cost += (float64(usage.CacheReadInputTokens) / perMillion) * entry.pricing.cacheReadPerMTokens
		cost += (float64(usage.CacheCreationInputTokens) / perMillion) * entry.pricing.cacheWritePerMTokens
		return llm.Money{Amount: cost, Currency: llm.CurrencyUSD}, true
	}
	return llm.Money{}, false
}
