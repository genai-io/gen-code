package core

import "github.com/genai-io/sdk-go/pkg/ai"

// Translating between San's conversation types and the SDK's.
//
// The two disagree on one thing only, and it is the whole of this file: San
// holds a turn as parallel fields — Content beside Thinking beside ToolCalls —
// while the SDK holds it as the ordered block sequence the provider produced.
// Going out, the fields are laid down in the order every protocol wants them
// replayed: reasoning first, then the answer, then the calls it asked for.
// Coming back, the blocks are projected onto the fields San's UI reads.
//
// Exported because the packages above core build calls of their own — llm's
// one-shot Complete, print mode. One copy, in the foundation layer, which llm
// imports rather than the other way round.

// ToAIMessages converts San's history into the SDK's, for the model it is about
// to be sent to.
//
// The model is a parameter because one thing in a turn is not portable: a
// model's own thinking goes back only where that endpoint takes it back, and
// San's history is cross-provider — a session can switch models mid-run, so
// the thinking in it may have come from somewhere else entirely.
//
// Tool-call/result pairing is not repaired here: ai.Client.prepare runs
// RepairHistory on every request, which is the same repair San's providers do
// themselves today.
func ToAIMessages(msgs []Message, model ai.Model) []ai.Message {
	out := make([]ai.Message, 0, len(msgs))
	for _, msg := range msgs {
		switch {
		case msg.ToolResult != nil:
			out = append(out, ai.ToolResultsMessage(ai.ToolResult{
				ToolCallID: msg.ToolResult.ToolCallID,
				ToolName:   msg.ToolResult.ToolName,
				Content:    msg.ToolResult.Content,
				IsError:    msg.ToolResult.IsError,
			}))
		case msg.Role == RoleUser:
			if content := userContent(msg); len(content) > 0 {
				out = append(out, ai.Message{Role: ai.RoleUser, Content: content})
			}
		case msg.Role == RoleAssistant:
			if content := assistantContent(msg, model); len(content) > 0 {
				out = append(out, ai.Message{Role: ai.RoleAssistant, Content: content})
			}
		}
	}
	return out
}

// userContent lays out a user turn, keeping text and images in the order the
// user typed them where the message records one.
func userContent(msg Message) ai.Content {
	parts := InterleavedContentParts(msg)
	if parts == nil {
		content := ai.TextContent(msg.Content)
		for _, img := range msg.Images {
			content = append(content, ai.ImageBlock(toImage(img)))
		}
		return content
	}

	content := make(ai.Content, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case ContentPartText:
			if part.Text != "" {
				content = append(content, ai.TextBlock(part.Text))
			}
		case ContentPartImage:
			if part.Image != nil {
				content = append(content, ai.ImageBlock(toImage(*part.Image)))
			}
		}
	}
	return content
}

// assistantContent lays out a model turn in replay order: reasoning state
// first — Anthropic rejects a thinking block that does not lead, and OpenAI's
// stateless backend rejects a call whose reasoning item does not precede it —
// then the answer, then the calls.
func assistantContent(msg Message, model ai.Model) ai.Content {
	content := make(ai.Content, 0, 2+len(msg.Reasoning)+len(msg.ToolCalls))
	if thinking, ok := replayableThinking(msg, model); ok {
		content = append(content, thinking)
	}
	for _, item := range msg.Reasoning {
		content = append(content, ai.ReasoningBlock(ai.ReasoningItem{
			ID:               item.ID,
			EncryptedContent: item.EncryptedContent,
			Summary:          item.Summary,
		}))
	}
	if msg.Content != "" {
		content = append(content, ai.TextBlock(msg.Content))
	}
	for _, call := range msg.ToolCalls {
		content = append(content, ai.ToolCallBlock(ai.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Input:     call.Input,
			Signature: call.ThoughtSignature,
		}))
	}
	return content
}

// replayableThinking returns the thinking block to send back with this turn,
// and whether the endpoint takes one at all.
//
// Each protocol wants its own thing, and none of them tolerates another's.
// Anthropic replays a thinking block only with the signature that proves the
// text was not edited, and rejects one without it. An OpenAI-compatible
// endpoint carries it as reasoning_content, which only some of them accept,
// and every one of them rejects a signature. The Responses protocol replays
// reasoning as the opaque items above instead, and Gemini takes the text as a
// thought part.
//
// Where none of that applies the thinking San recorded is display-only: it was
// shown to the user and stays out of the request. That is what each of San's
// own provider packages does today, decided once here rather than per vendor.
func replayableThinking(msg Message, model ai.Model) (ai.Block, bool) {
	if msg.Thinking == "" {
		return ai.Block{}, false
	}
	switch model.API {
	case ai.APIAnthropicMessages, ai.APIAnthropicVertex:
		if msg.ThinkingSignature == "" {
			return ai.Block{}, false
		}
		return ai.ThinkingBlock(msg.Thinking, msg.ThinkingSignature), true
	case ai.APIOpenAIChat:
		if !ai.CompatOf[ai.OpenAIChatCompat](model).ReasoningContent {
			return ai.Block{}, false
		}
		return ai.ThinkingBlock(msg.Thinking, ""), true
	case ai.APIGoogleGenAI:
		return ai.ThinkingBlock(msg.Thinking, ""), true
	default:
		// The Responses protocol carries reasoning as opaque items, which
		// assistantContent already replays; its readable summary is not a
		// second copy to send back.
		return ai.Block{}, false
	}
}

func toImage(img Image) ai.Image {
	return ai.Image{MediaType: img.MediaType, Data: img.Data, FileName: img.FileName}
}

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
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:               call.ID,
			Name:             call.Name,
			Input:            call.Input,
			ThoughtSignature: call.Signature,
		})
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
