package llm

import (
	"github.com/genai-io/sdk-go/pkg/ai"
)

// toModelInfo projects an SDK model onto the record San's picker and store
// read. A model with no window reports zero, which San already treats as
// "unknown" rather than substituting a guess.
func toModelInfo(m ai.Model) ModelInfo {
	info := ModelInfo{
		ID:               m.ID,
		Name:             m.Name,
		DisplayName:      m.Name,
		InputTokenLimit:  m.ContextWindow,
		OutputTokenLimit: m.MaxOutput,
		Lifecycle:        toModelLifecycle(m.Stage),
		Replacement:      m.Replacement,
		TextOnly:         !m.Accepts(ai.ModalityImage),
	}
	if info.Name == "" {
		info.Name = m.ID
		info.DisplayName = m.ID
	}
	if efforts := m.Efforts(); len(efforts) > 0 {
		labels := make([]string, len(efforts))
		for i, effort := range efforts {
			labels[i] = string(effort)
		}
		var fallback string
		if level, ok := m.DefaultLevel(); ok {
			fallback = string(level.Effort)
		}
		info.Reasoning = NewReasoningCapability(labels, fallback)
	}
	return info
}

// toModelLifecycle maps the catalog's stage onto San's. A retired model never
// reaches here — ListModels drops it — so it has no San equivalent.
func toModelLifecycle(stage ai.Stage) ModelLifecycle {
	switch stage {
	case ai.StagePreview:
		return ModelPreview
	case ai.StageDeprecated:
		return ModelDeprecated
	default:
		return ModelStable
	}
}
