package setting

import "github.com/genai-io/san/internal/secret"

const (
	DefaultMaxTokens    = 8192
	DefaultSystemPrompt = "You are a helpful AI coding assistant."
)

// DefaultModel returns the default model ID for a given provider and auth method.
func DefaultModel(providerName string, authMethod string) string {
	if providerName == "anthropic" && authMethod == "vertex" {
		return "claude-sonnet-4-5@20250929"
	}
	switch providerName {
	case "agnesai":
		return "agnes-2.0-flash"
	case "anthropic":
		// Sonnet 4 retired on the Claude API; Sonnet 5 is the same tier.
		return "claude-sonnet-5"
	case "openai":
		return "gpt-4o"
	case "google":
		// Gemini 2.0 is no longer in Google's published model list.
		return "gemini-2.5-flash"
	case "moonshot":
		return "moonshot-v1-auto"
	case "alibaba":
		return "qwen-plus"
	case "minmax":
		return "MiniMax-M2.7"
	case "bigmodel":
		return "glm-5.1"
	case "deepseek":
		return "deepseek-v4-flash"
	case "sensenova":
		return "sensenova-6.7-flash-lite"
	case "ollama":
		return "llama4"
	case "mimo":
		return "xiaomi/mimo-v2.5-pro"
	case "volcengine":
		// Volcengine has no static catalog; the user picks the model via
		// VOLCENGINE_MODEL. Empty means the caller must require an explicit
		// selection.
		return secret.Resolve("VOLCENGINE_MODEL")
	default:
		// Unknown provider (e.g. a user-defined custom provider) — there is no
		// sensible default; the caller must require an explicit selection.
		return ""
	}
}
