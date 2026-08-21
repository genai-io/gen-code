package bigmodel

import "strings"

// staticInputLimit returns the known context window for a GLM model ID.
// Used as a fallback when BigModel's /v1/models endpoint omits
// context_length (which it does in practice — the endpoint follows the
// bare OpenAI shape with only id/object/owned_by).
//
// Last checked 2026-08-20 against docs.bigmodel.cn: GLM-5, GLM-4.7 and GLM-4.6
// all state a 200K window and a 128K output cap; GLM-4-Long is the outlier at
// 1M in and 4K out. An unrecognised GLM model returns 0 rather than a blanket
// default — an invented window is acted on silently, and San treats 0 as
// "unknown" and skips proactive compaction rather than compacting against a
// figure nobody checked.
func staticInputLimit(modelID string) int {
	input, _ := staticLimits(modelID)
	return input
}

// staticOutputLimit mirrors staticInputLimit for the output cap.
func staticOutputLimit(modelID string) int {
	_, output := staticLimits(modelID)
	return output
}

func staticLimits(modelID string) (input, output int) {
	m := strings.ToLower(modelID)
	switch {
	case strings.HasPrefix(m, "glm-4-long"):
		return 1_000_000, 4_096
	case strings.HasPrefix(m, "glm-5"), strings.HasPrefix(m, "glm-4.7"), strings.HasPrefix(m, "glm-4.6"):
		return 200_000, 128_000
	}
	return 0, 0
}
