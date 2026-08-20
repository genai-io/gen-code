package minmax

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

// Prices are CNY per million tokens, from
// https://platform.minimaxi.com/docs/guides/pricing-paygo (last checked
// 2026-08-20).
var catalog = []modelCatalogEntry{
	{
		// M3's rate card is tiered by prompt size; these are the <=512k rates,
		// which is the tier a single entry can express. Above 512k MiniMax
		// bills double, so a long-prompt turn costs more than estimated here.
		info: llm.ModelInfo{
			ID:               "MiniMax-M3",
			Name:             "MiniMax M3",
			DisplayName:      "MiniMax M3",
			InputTokenLimit:  1000000,
			OutputTokenLimit: 8192,
		},
		pricing: pricing{inputPerMTokens: 2.1, outputPerMTokens: 8.4, cacheReadPerMTokens: 0.42, cacheWritePerMTokens: 2.625},
	},
	{
		info: llm.ModelInfo{
			ID:               "MiniMax-M2.7",
			Name:             "MiniMax M2.7",
			DisplayName:      "MiniMax M2.7",
			InputTokenLimit:  204800,
			OutputTokenLimit: 8192,
		},
		pricing: pricing{inputPerMTokens: 2.1, outputPerMTokens: 8.4, cacheReadPerMTokens: 0.42, cacheWritePerMTokens: 2.625},
	},
	{
		info: llm.ModelInfo{
			ID:               "MiniMax-M2.7-highspeed",
			Name:             "MiniMax M2.7 Highspeed",
			DisplayName:      "MiniMax M2.7 Highspeed",
			InputTokenLimit:  204800,
			OutputTokenLimit: 8192,
		},
		pricing: pricing{inputPerMTokens: 4.2, outputPerMTokens: 16.8, cacheReadPerMTokens: 0.42, cacheWritePerMTokens: 2.625},
	},
	{
		info: llm.ModelInfo{
			ID:               "MiniMax-M2.5",
			Name:             "MiniMax M2.5",
			DisplayName:      "MiniMax M2.5",
			InputTokenLimit:  204800,
			OutputTokenLimit: 8192,
		},
		pricing: pricing{inputPerMTokens: 2.1, outputPerMTokens: 8.4, cacheReadPerMTokens: 0.21, cacheWritePerMTokens: 2.625},
	},
	{
		info: llm.ModelInfo{
			ID:               "MiniMax-M2.5-highspeed",
			Name:             "MiniMax M2.5 Highspeed",
			DisplayName:      "MiniMax M2.5 Highspeed",
			InputTokenLimit:  204800,
			OutputTokenLimit: 8192,
		},
		pricing: pricing{inputPerMTokens: 4.2, outputPerMTokens: 16.8, cacheReadPerMTokens: 0.21, cacheWritePerMTokens: 2.625},
	},
	{
		info: llm.ModelInfo{
			ID:               "MiniMax-M2.1",
			Name:             "MiniMax M2.1",
			DisplayName:      "MiniMax M2.1",
			InputTokenLimit:  204800,
			OutputTokenLimit: 8192,
		},
		pricing: pricing{inputPerMTokens: 2.1, outputPerMTokens: 8.4, cacheReadPerMTokens: 0.21, cacheWritePerMTokens: 2.625},
	},
	{
		info: llm.ModelInfo{
			ID:               "MiniMax-M2.1-highspeed",
			Name:             "MiniMax M2.1 Highspeed",
			DisplayName:      "MiniMax M2.1 Highspeed",
			InputTokenLimit:  204800,
			OutputTokenLimit: 8192,
		},
		pricing: pricing{inputPerMTokens: 4.2, outputPerMTokens: 16.8, cacheReadPerMTokens: 0.21, cacheWritePerMTokens: 2.625},
	},
	{
		info: llm.ModelInfo{
			ID:               "MiniMax-M2",
			Name:             "MiniMax M2",
			DisplayName:      "MiniMax M2",
			InputTokenLimit:  204800,
			OutputTokenLimit: 8192,
		},
		pricing: pricing{inputPerMTokens: 2.1, outputPerMTokens: 8.4, cacheReadPerMTokens: 0.21, cacheWritePerMTokens: 2.625},
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
		return llm.Money{Amount: cost, Currency: llm.CurrencyCNY}, true
	}
	return llm.Money{}, false
}
