package llm

import "strings"

// How hard a model is asked to think.
//
// Every vendor spells it differently — a budget in tokens, a named rung, a
// boolean — and the SDK's catalog flattens all of it into one ordered ladder
// per model, least effort to most. San's job is to pick a rung off that
// ladder: validate what the user chose against what this model actually
// offers, fall back to the model's own default, and cycle to the next rung
// when the user presses the key.
//
// The ladder is per model, not per provider: two models from one vendor
// routinely disagree about it, and a model that does not reason has none at
// all. So every lookup here goes through the live catalog entry cached in the
// store, and falls back to what the provider says statically only when the
// cache has nothing.

// ThinkingEffortProvider is implemented by providers that expose native thinking
// or reasoning effort values.
type ThinkingEffortProvider interface {
	ThinkingEfforts(model string) []string
	DefaultThinkingEffort(model string) string
}

// ReasoningCapability describes the reasoning-effort values advertised for one
// model. Providers that return this metadata from ListModels let the rest of the
// app follow the live catalog instead of guessing capabilities from model IDs.
type ReasoningCapability struct {
	SupportedEfforts []string `json:"supportedEfforts,omitempty"`
	DefaultEffort    string   `json:"defaultEffort,omitempty"`
}

// NewReasoningCapability normalizes provider-supplied reasoning metadata.
// Unknown effort labels are intentionally preserved so newly introduced
// provider-native levels work without a san release.
func NewReasoningCapability(efforts []string, defaultEffort string) *ReasoningCapability {
	normalized := make([]string, 0, len(efforts))
	seen := make(map[string]struct{}, len(efforts))
	for _, effort := range efforts {
		effort = strings.ToLower(strings.TrimSpace(effort))
		if effort == "" {
			continue
		}
		if _, ok := seen[effort]; ok {
			continue
		}
		seen[effort] = struct{}{}
		normalized = append(normalized, effort)
	}
	if len(normalized) == 0 {
		return nil
	}

	defaultEffort = normalizeThinkingEffort(defaultEffort, normalized)
	return &ReasoningCapability{
		SupportedEfforts: normalized,
		DefaultEffort:    defaultEffort,
	}
}

func ThinkingEfforts(p Provider, model string) []string {
	ep, ok := p.(ThinkingEffortProvider)
	if !ok {
		return nil
	}
	efforts := ep.ThinkingEfforts(model)
	if len(efforts) == 0 {
		return nil
	}
	out := make([]string, len(efforts))
	copy(out, efforts)
	return out
}

func DefaultThinkingEffort(p Provider, model string) string {
	ep, ok := p.(ThinkingEffortProvider)
	if !ok {
		return ""
	}
	return normalizeThinkingEffort(ep.DefaultThinkingEffort(model), ep.ThinkingEfforts(model))
}

// ThinkingEffortsForModel returns live cached model metadata when available,
// falling back to the provider's static ThinkingEffortProvider implementation.
func ThinkingEffortsForModel(p Provider, store *Store, current *CurrentModelInfo) []string {
	capability := reasoningCapabilityForModel(p, store, current)
	if capability == nil {
		return nil
	}
	out := make([]string, len(capability.SupportedEfforts))
	copy(out, capability.SupportedEfforts)
	return out
}

// ResolveThinkingEffortForModel validates a selected effort against the live
// model metadata, then falls back to that model's advertised default.
func ResolveThinkingEffortForModel(p Provider, store *Store, current *CurrentModelInfo, selected string) string {
	capability := reasoningCapabilityForModel(p, store, current)
	if capability == nil {
		return ""
	}
	if effort := normalizeThinkingEffort(selected, capability.SupportedEfforts); effort != "" {
		return effort
	}
	return capability.DefaultEffort
}

// NextThinkingEffortForModel cycles through the live model-specific effort
// values, using the advertised default when no valid current value is set.
func NextThinkingEffortForModel(p Provider, store *Store, current *CurrentModelInfo, selected string) (string, bool) {
	capability := reasoningCapabilityForModel(p, store, current)
	if capability == nil {
		return "", false
	}
	currentEffort := normalizeThinkingEffort(selected, capability.SupportedEfforts)
	if currentEffort == "" {
		currentEffort = capability.DefaultEffort
	}
	for i, effort := range capability.SupportedEfforts {
		if effort == currentEffort {
			return capability.SupportedEfforts[(i+1)%len(capability.SupportedEfforts)], true
		}
	}
	return capability.SupportedEfforts[0], true
}

func reasoningCapabilityForModel(p Provider, store *Store, current *CurrentModelInfo) *ReasoningCapability {
	if current == nil || current.ModelID == "" {
		return nil
	}
	if store != nil {
		authMethod := store.ResolveAuthMethod(current)
		if capability, ok := store.CachedModelReasoningForProvider(current.Provider, authMethod, current.ModelID); ok {
			return capability
		}
	}
	return NewReasoningCapability(
		ThinkingEfforts(p, current.ModelID),
		DefaultThinkingEffort(p, current.ModelID),
	)
}

func normalizeThinkingEffort(effort string, efforts []string) string {
	effort = strings.TrimSpace(strings.ToLower(effort))
	if effort == "" {
		return ""
	}
	for _, allowed := range efforts {
		if strings.EqualFold(effort, allowed) {
			return allowed
		}
	}
	return ""
}
