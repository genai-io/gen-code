package llm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/core"
)

// TestLiveTurn runs one real turn against every vendor whose key is in the
// environment. It is opt-in: without SAN_SDK_LIVE it does not spend anything.
func TestLiveTurn(t *testing.T) {
	if os.Getenv("SAN_SDK_LIVE") == "" {
		t.Skip("set SAN_SDK_LIVE=1 to run one real turn per configured vendor")
	}

	cases := []struct {
		provider ProviderID
		auth     AuthMethod
		model    string
		key      string
	}{
		{DeepSeek, AuthAPIKey, "deepseek-v4-flash", "DEEPSEEK_API_KEY"},
		{Moonshot, AuthAPIKey, "kimi-k3", "MOONSHOT_API_KEY"},
		{MinMax, AuthAPIKey, "MiniMax-M2.7", "MINIMAX_API_KEY"},
		{OpenAI, AuthAPIKey, "gpt-5.6-luna", "OPENAI_API_KEY"},
		{BigModel, AuthAPIKey, "glm-5", "BIGMODEL_API_KEY"},
		{Alibaba, AuthAPIKey, "qwen3.8-max", "DASHSCOPE_API_KEY"},
	}

	registerVendors()
	for _, tc := range cases {
		if os.Getenv(tc.key) == "" {
			continue
		}
		t.Run(string(tc.provider), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			p, err := GetProvider(ctx, tc.provider, tc.auth)
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			models, err := p.ListModels(ctx)
			if err != nil {
				t.Errorf("ListModels: %v", err)
			} else {
				t.Logf("%d models, first %q", len(models), models[0].ID)
			}

			var text string
			var resp *CompletionResponse
			for chunk := range legacyStream(ctx, p, CompletionOptions{
				Model:        tc.model,
				SystemPrompt: "Answer with one word.",
				Messages:     []core.Message{{Role: ai.RoleUser, Content: ai.TextContent("What is the capital of France?")}},
				MaxTokens:    64,
			}) {
				switch chunk.Type {
				case ChunkTypeText:
					text += chunk.Text
				case ChunkTypeDone:
					resp = chunk.Response
				case ChunkTypeError:
					// A vendor that has run out of quota is an account
					// problem, not a translation one.
					if ai.IsKind(chunk.Error, ai.KindRateLimit) {
						t.Skipf("vendor declined on quota: %v", chunk.Error)
					}
					t.Fatalf("stream: %v", chunk.Error)
				}
			}
			if resp == nil {
				t.Fatal("no final response")
			}
			t.Logf("text=%q stop=%s usage=%+v", text, resp.StopReason, resp.Usage)
			if resp.Content == "" {
				t.Error("the turn produced no text")
			}
			if resp.TotalInputTokens() == 0 || resp.OutputTokens == 0 {
				t.Error("the turn reported no usage")
			}
		})
	}
}

// TestLiveToolRoundTrip runs the two-step exchange San's agent loop depends
// on: the model asks for a tool, the result goes back as history, the model
// answers from it. It is the conversion path most likely to break, and the one
// a stub server cannot prove.
func TestLiveToolRoundTrip(t *testing.T) {
	if os.Getenv("SAN_SDK_LIVE") == "" {
		t.Skip("set SAN_SDK_LIVE=1 to run one real tool exchange per configured vendor")
	}

	cases := []struct {
		provider ProviderID
		model    string
		key      string
	}{
		{DeepSeek, "deepseek-v4-flash", "DEEPSEEK_API_KEY"},
		{MinMax, "MiniMax-M3", "MINIMAX_API_KEY"},
		{OpenAI, "gpt-5.6-luna", "OPENAI_API_KEY"},
	}

	weather := []ToolSchema{{
		Name:        "get_weather",
		Description: "Report the current weather in a city.",
		Definition: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"city": map[string]any{"type": "string"}},
			"required":             []string{"city"},
			"additionalProperties": false,
		},
	}}

	registerVendors()
	for _, tc := range cases {
		if os.Getenv(tc.key) == "" {
			continue
		}
		t.Run(string(tc.provider), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			p, err := GetProvider(ctx, tc.provider, AuthAPIKey)
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			turn := func(msgs []core.Message) *CompletionResponse {
				t.Helper()
				var resp *CompletionResponse
				for chunk := range legacyStream(ctx, p, CompletionOptions{
					Model:        tc.model,
					SystemPrompt: "Use the tools you are given. Answer in one short sentence.",
					Messages:     msgs,
					Tools:        weather,
					MaxTokens:    256,
				}) {
					switch chunk.Type {
					case ChunkTypeDone:
						resp = chunk.Response
					case ChunkTypeError:
						t.Fatalf("stream: %v", chunk.Error)
					}
				}
				if resp == nil {
					t.Fatal("no final response")
				}
				return resp
			}

			history := []core.Message{{Role: ai.RoleUser, Content: ai.TextContent("What is the weather in Paris right now?")}}
			asked := turn(history)
			if len(asked.ToolCalls) == 0 {
				// Whether a model reaches for a tool is its own decision, not
				// something this exercise establishes. What is being tested is
				// the round trip once it does.
				t.Skipf("the model answered without calling the tool: %q", asked.Content)
			}
			call := asked.ToolCalls[0]
			t.Logf("call %s(%s)", call.Name, call.Input)

			row := core.ChatMessage{
				Role:              core.ChatAssistant,
				Content:           asked.Content,
				Thinking:          asked.Thinking,
				ThinkingSignature: asked.ThinkingSignature,
				Reasoning:         asked.Reasoning,
				ToolCalls:         asked.ToolCalls,
			}
			replay, _ := row.ToMessage()
			history = append(history, replay, core.ToolResultMessage(core.ToolResult{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content:    "18C and raining",
			}))

			answered := turn(history)
			t.Logf("answer=%q stop=%s", answered.Content, answered.StopReason)
			if answered.Content == "" {
				t.Error("the model produced no answer from the tool result")
			}
		})
	}
}

// TestLiveSubscription runs one turn through each vendor that signs in a
// person rather than a service, using the credential San already stored. It
// proves the two things the API-key path cannot: that an existing sign-in is
// still readable, and that the editor identity each backend demands is sent.
func TestLiveSubscription(t *testing.T) {
	if os.Getenv("SAN_SDK_LIVE") == "" {
		t.Skip("set SAN_SDK_LIVE=1 to run one real turn per signed-in vendor")
	}

	cases := []struct {
		provider ProviderID
		vendorID string
		model    string
	}{
		{OpenAI, codexVendor, "gpt-5.6-luna"},
		{Copilot, copilotVendor, "gpt-4.1"},
	}

	registerVendors()
	for _, tc := range cases {
		t.Run(string(tc.provider), func(t *testing.T) {
			if !(authenticator{vendorID: tc.vendorID}).HasCredentials() {
				t.Skipf("not signed in to %s", tc.vendorID)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			p, err := GetProvider(ctx, tc.provider, AuthSubscription)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			models, err := p.ListModels(ctx)
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			t.Logf("%d models: %q…", len(models), models[0].ID)

			var text string
			for chunk := range legacyStream(ctx, p, CompletionOptions{
				Model:        tc.model,
				SystemPrompt: "Answer with one word.",
				Messages:     []core.Message{core.UserMessage("What is the capital of France?", nil)},
				MaxTokens:    64,
			}) {
				switch chunk.Type {
				case ChunkTypeText:
					text += chunk.Text
				case ChunkTypeError:
					if ai.IsKind(chunk.Error, ai.KindRateLimit) {
						t.Skipf("subscription declined on quota: %v", chunk.Error)
					}
					t.Fatalf("stream: %v", chunk.Error)
				}
			}
			t.Logf("text=%q", text)
			if text == "" {
				t.Error("the turn produced no text")
			}
		})
	}
}
