package core

import "github.com/genai-io/sdk-go/pkg/ai"

// What is left of translating between San and the SDK.
//
// The conversation no longer needs translating: it is ai.Message on both
// sides. What remains is the two things San holds in its own shape — a tool
// schema, and the response as the UI reads it — and both are one-way.

// ToAITools converts San's tool schemas. Run stays nil: San executes tools
// itself and hands the results back as history, so the SDK is never asked to
// run one.
func ToAITools(schemas []ToolSchema) []ai.Tool {
	if len(schemas) == 0 {
		return nil
	}
	out := make([]ai.Tool, len(schemas))
	for i, schema := range schemas {
		out[i] = ai.Tool{Schema: ai.Schema{
			Name:        schema.Name,
			Description: schema.Description,
			Definition:  schema.Parameters,
		}}
	}
	return out
}

// FromAIResponse projects a finished SDK response onto San's response fields.
func FromAIResponse(resp *ai.Response) *InferResponse {
	if resp == nil {
		return nil
	}
	out := &InferResponse{
		Content:    resp.Text(),
		Thinking:   resp.Thinking(),
		StopReason: toStopReason(resp.StopReason),
		Usage:      toUsage(resp.Usage),
	}
	for _, block := range resp.Content {
		if block.Type == ai.BlockThinking && block.Signature != "" {
			out.ThinkingSignature = block.Signature
			break
		}
	}
	for _, item := range resp.ReasoningItems() {
		out.Reasoning = append(out.Reasoning, ReasoningItem{
			ID:               item.ID,
			EncryptedContent: item.EncryptedContent,
			Summary:          item.Summary,
		})
	}
	for _, call := range resp.ToolCalls() {
		out.ToolCalls = append(out.ToolCalls, call)
	}
	return out
}

// toUsage maps the SDK's token accounting onto San's. The SDK names the two
// cache figures for what happened to the prefix; San names them for the
// Anthropic wire fields they came from, and they mean the same thing.
func toUsage(u ai.Usage) Usage {
	return Usage{
		InputTokens:              u.Input,
		OutputTokens:             u.Output,
		CacheCreationInputTokens: u.CacheWrite,
		CacheReadInputTokens:     u.CacheRead,
	}
}

// toStopReason maps the SDK's finish reasons onto San's. StopSequence and
// StopRefusal have no San equivalent and both end a turn normally, so they
// land on end_turn; StopAborted is a cancelled turn.
func toStopReason(reason ai.StopReason) StopReason {
	switch reason {
	case ai.StopToolUse:
		return StopToolUse
	case ai.StopMaxTokens:
		return StopMaxTokens
	case ai.StopAborted:
		return StopCancelled
	case ai.StopError:
		return StopError
	default:
		return StopEndTurn
	}
}
