package setting

import "testing"

func TestDefaultModel(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		auth     string
		envModel string // VOLCENGINE_MODEL, only relevant for volcengine
		want     string
	}{
		{name: "anthropic vertex", provider: "anthropic", auth: "vertex", want: "claude-sonnet-4-5@20250929"},
		{name: "anthropic", provider: "anthropic", want: "claude-sonnet-5"},
		{name: "openai", provider: "openai", want: "gpt-4o"},
		{name: "ollama", provider: "ollama", want: "llama4"},
		{name: "mimo", provider: "mimo", want: "xiaomi/mimo-v2.5-pro"},
		{name: "volcengine from env", provider: "volcengine", envModel: "doubao-pro-256k", want: "doubao-pro-256k"},
		{name: "volcengine without env", provider: "volcengine", want: ""},
		{name: "unknown provider", provider: "custom", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VOLCENGINE_MODEL", tt.envModel)
			if got := DefaultModel(tt.provider, tt.auth); got != tt.want {
				t.Fatalf("DefaultModel(%q, %q) = %q, want %q", tt.provider, tt.auth, got, tt.want)
			}
		})
	}
}
