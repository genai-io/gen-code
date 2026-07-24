package input

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/tool"
)

// TokenLimitDeps holds the state needed by token limit commands.
type TokenLimitDeps struct {
	CurrentModel *llm.CurrentModelInfo
	Provider     llm.Provider
	Store        *llm.Store
	InputTokens  int
	Cwd          string
	SpinnerTick  tea.Cmd
	ToolSvc      *tool.Registry
}

// HandleTokenLimitCommand processes the /tokenlimit slash command.
func HandleTokenLimitCommand(deps TokenLimitDeps, args string) (string, tea.Cmd, error) {
	if deps.CurrentModel == nil {
		return "No model selected. Use /models to select a model first.", nil, nil
	}

	modelID := deps.CurrentModel.ModelID
	args = strings.TrimSpace(args)

	if args != "" {
		return setTokenLimits(deps, modelID, args)
	}

	return showOrFetchTokenLimits(deps, modelID)
}

func setTokenLimits(deps TokenLimitDeps, modelID, args string) (string, tea.Cmd, error) {
	var inputLimit, outputLimit int
	if _, err := fmt.Sscanf(args, "%d %d", &inputLimit, &outputLimit); err != nil {
		return "Usage:\n  /tokenlimit              - Show or auto-fetch limits\n  /tokenlimit <input> <output> - Set custom limits", nil, nil
	}

	if inputLimit <= 0 || outputLimit <= 0 {
		return "Token limits must be positive integers", nil, nil
	}

	if deps.Store != nil {
		if err := deps.Store.SetTokenLimit(modelID, inputLimit, outputLimit); err != nil {
			return "", nil, fmt.Errorf("failed to set token limits: %w", err)
		}
	}

	return fmt.Sprintf("Set token limits for %s:\n  Input:  %s tokens\n  Output: %s tokens",
		modelID, kit.FormatTokenCount(inputLimit), kit.FormatTokenCount(outputLimit)), nil, nil
}

func showOrFetchTokenLimits(deps TokenLimitDeps, modelID string) (string, tea.Cmd, error) {
	if deps.Store != nil {
		if customInput, customOutput, ok := deps.Store.GetTokenLimit(modelID); ok {
			return formatTokenLimitDisplay(modelID, customInput, customOutput, true, deps.InputTokens), nil, nil
		}
	}

	inputLimit, outputLimit := kit.GetModelTokenLimits(deps.Store, deps.CurrentModel)
	if inputLimit > 0 || outputLimit > 0 {
		return formatTokenLimitDisplay(modelID, inputLimit, outputLimit, false, deps.InputTokens), nil, nil
	}

	return "", tea.Batch(deps.SpinnerTick, fetchTokenLimitsCmd(deps)), nil
}

func fetchTokenLimitsCmd(deps TokenLimitDeps) tea.Cmd {
	fetchDeps := autoFetchTokenLimitsDeps{
		LLM:          deps.Provider,
		Store:        deps.Store,
		CurrentModel: deps.CurrentModel,
		Cwd:          deps.Cwd,
		ToolSvc:      deps.ToolSvc,
	}
	return func() tea.Msg {
		result, err := autoFetchTokenLimits(context.Background(), fetchDeps)
		return kit.TokenLimitResultMsg{Result: result, Err: err}
	}
}

// --- Auto-fetch agent ---

type autoFetchTokenLimitsDeps struct {
	LLM          llm.Provider
	Store        *llm.Store
	CurrentModel *llm.CurrentModelInfo
	Cwd          string
	ToolSvc      *tool.Registry
}

func autoFetchTokenLimits(ctx context.Context, deps autoFetchTokenLimitsDeps) (string, error) {
	if deps.LLM == nil {
		return "No provider connected. Use /tokenlimit <input> <output> to set manually.", nil
	}

	modelID := deps.CurrentModel.ModelID
	providerName := string(deps.CurrentModel.Provider)

	if fetcher, ok := deps.LLM.(llm.ModelLimitsFetcher); ok {
		inputLimit, outputLimit, err := fetcher.FetchModelLimits(ctx, modelID)
		if err == nil && (inputLimit > 0 || outputLimit > 0) {
			if deps.Store != nil {
				_ = deps.Store.SetTokenLimit(modelID, inputLimit, outputLimit)
			}
			return formatTokenLimitDisplay(modelID, inputLimit, outputLimit, false, 0), nil
		}
	}

	systemPrompt := buildTokenLimitAgentPrompt(modelID, providerName, string(deps.CurrentModel.AuthMethod))
	messages := []core.Message{
		core.UserMessage(fmt.Sprintf("Find the token limits for model: %s (provider: %s)", modelID, providerName), nil),
	}

	const maxTurns = 5

	for range maxTurns {
		response, err := llm.Complete(ctx, deps.LLM, llm.CompletionOptions{
			Model:        deps.CurrentModel.ModelID,
			SystemPrompt: systemPrompt,
			Messages:     messages,
			Tools:        getTokenLimitAgentTools(),
			MaxTokens:    1024,
		})
		if err != nil {
			return "", fmt.Errorf("agent error: %w", err)
		}

		if len(response.ToolCalls) > 0 {
			messages = appendToolCallMessages(ctx, messages, response.ToolCalls, deps.Cwd, deps.ToolSvc)
			continue
		}

		content := strings.TrimSpace(response.Content)
		if result, done := parseTokenLimitResponse(content, modelID, deps.Store); done {
			return result, nil
		}

		messages = append(messages,
			core.AssistantMessage(content, "", nil),
			core.UserMessage("Please continue searching or respond with FOUND or NOT_FOUND.", nil))
	}

	return tokenLimitNotFoundMessage(modelID), nil
}

func buildTokenLimitAgentPrompt(modelID, providerName, authMethod string) string {
	return fmt.Sprintf(`You are a helpful assistant that finds token limits for AI models.

Your task is to find the maximum input tokens (context window) and maximum output tokens for this model:
- Model ID: %s
- Provider: %s
- Auth Method: %s

Use the WebSearch tool to search for this information, then use WebFetch to read relevant documentation pages if needed.

When you find the limits, respond with EXACTLY this format:
FOUND: <input_tokens> <output_tokens>

For example: FOUND: 200000 16000

If you cannot find the information after searching, respond with:
NOT_FOUND

Do not include any other text in your final response.`, modelID, providerName, authMethod)
}

func getTokenLimitAgentTools() []llm.ToolSchema {
	return []llm.ToolSchema{
		{
			Name:        "WebSearch",
			Description: "Search the web for information about model token limits",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "The search query"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "WebFetch",
			Description: "Fetch content from a URL to read documentation",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "The URL to fetch"},
				},
				"required": []string{"url"},
			},
		},
	}
}

func appendToolCallMessages(ctx context.Context, messages []core.Message, toolCalls []core.ToolCall, cwd string, toolSvc *tool.Registry) []core.Message {
	messages = append(messages, core.AssistantMessage("", "", toolCalls))

	for _, tc := range toolCalls {
		var params map[string]any
		if err := json.Unmarshal([]byte(tc.Input), &params); err != nil {
			params = map[string]any{}
		}

		result := toolSvc.Execute(ctx, tc.Name, params, cwd)
		messages = append(messages, core.ToolResultMessage(core.ToolResult{
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
			Content:    result.Output,
			IsError:    !result.Success,
		}))
	}
	return messages
}

func tokenLimitNotFoundMessage(modelID string) string {
	return fmt.Sprintf("Could not find token limits for %s.\n\nSet manually with: /tokenlimit <input> <output>", modelID)
}

func parseTokenLimitResponse(content, modelID string, store *llm.Store) (string, bool) {
	if strings.HasPrefix(content, "FOUND:") {
		var inputLimit, outputLimit int
		if _, err := fmt.Sscanf(content, "FOUND: %d %d", &inputLimit, &outputLimit); err == nil && inputLimit > 0 {
			if store != nil {
				_ = store.SetTokenLimit(modelID, inputLimit, outputLimit)
			}
			return fmt.Sprintf("Found and saved token limits for %s:\n  Input:  %s tokens\n  Output: %s tokens",
				modelID, formatTokenCount(inputLimit), formatTokenCount(outputLimit)), true
		}
	}

	if strings.Contains(content, "NOT_FOUND") {
		return tokenLimitNotFoundMessage(modelID), true
	}

	return "", false
}

func formatTokenLimitDisplay(modelID string, inputLimit, outputLimit int, isCustom bool, currentInputTokens int) string {
	result := fmt.Sprintf("Token Limits for %s:\n\n  Input:  %s tokens\n  Output: %s tokens",
		modelID, formatTokenCount(inputLimit), formatTokenCount(outputLimit))

	if isCustom {
		result += "\n\n(custom override)"
	}

	if currentInputTokens > 0 && inputLimit > 0 {
		percent := float64(currentInputTokens) / float64(inputLimit) * 100
		result += fmt.Sprintf("\n\nCurrent usage: %s tokens (%.1f%%)", formatTokenCount(currentInputTokens), percent)
	}

	return result
}

func formatTokenCount(count int) string {
	switch {
	case count >= 1000000:
		return fmt.Sprintf("%.1fM", float64(count)/1000000)
	case count >= 1000:
		return fmt.Sprintf("%.1fk", float64(count)/1000)
	default:
		return fmt.Sprintf("%d", count)
	}
}
