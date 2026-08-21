package volcengine

import "testing"

func TestStaticLimits(t *testing.T) {
	cases := []struct {
		model      string
		wantInput  int
		wantOutput int
	}{
		{"doubao-pro-256k", 256_000, 8_000},
		{"doubao-pro-128k", 128_000, 8_000},
		{"doubao-pro-32k", 32_000, 4_000},
		{"doubao-seed-1.6", 1_024_000, 256_000},
		{"unknown-model", 0, 0},
	}
	for _, c := range cases {
		input, output := staticLimits(c.model)
		if input != c.wantInput || output != c.wantOutput {
			t.Errorf("staticLimits(%q) = (%d, %d), want (%d, %d)", c.model, input, output, c.wantInput, c.wantOutput)
		}
	}
}
