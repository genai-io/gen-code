package openai

import "testing"

func TestOpenAILimits(t *testing.T) {
	cases := []struct {
		model      string
		wantInput  int
		wantOutput int
	}{
		{"gpt-6", 1_050_000, 128_000},
		{"gpt-5.5", 1_050_000, 128_000},
		{"gpt-5.4-mini", 1_050_000, 128_000},
		{"gpt-5", 1_050_000, 128_000},
		{"o1", 200_000, 100_000},
		{"o3-mini", 200_000, 100_000},
		{"o4", 200_000, 100_000},
		{"codex-latest", 1_050_000, 128_000},
		{"gpt-4.1", 1_047_576, 32_768},
		{"gpt-4o", 128_000, 16_384},
		{"unknown-model", 0, 0},
	}
	for _, c := range cases {
		input, output := openAILimits(c.model)
		if input != c.wantInput || output != c.wantOutput {
			t.Errorf("openAILimits(%q) = (%d, %d), want (%d, %d)", c.model, input, output, c.wantInput, c.wantOutput)
		}
	}
}
