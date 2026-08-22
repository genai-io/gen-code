package llm

import (
	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/core"
)

// Translating between San's conversation types and the SDK's.
//
// The two disagree on one thing only, and it is the whole of this file: San
// holds a turn as parallel fields — Content beside Thinking beside ToolCalls —
// while the SDK holds it as the ordered block sequence the provider produced.
// Going out, the fields are laid down in the order every protocol wants them
// replayed: reasoning first, then the answer, then the calls it asked for.
// Coming back, the blocks are projected onto the fields San's UI reads.

// toMessages converts San's history into the SDK's, for the model it is about
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
func toMessages(msgs []core.Message, model ai.Model) []ai.Message {
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
		case msg.Role == core.RoleUser:
			if content := userContent(msg); len(content) > 0 {
				out = append(out, ai.Message{Role: ai.RoleUser, Content: content})
			}
		case msg.Role == core.RoleAssistant:
			if content := assistantContent(msg, model); len(content) > 0 {
				out = append(out, ai.Message{Role: ai.RoleAssistant, Content: content})
			}
		}
	}
	return out
}

// userContent lays out a user turn, keeping text and images in the order the
// user typed them where the message records one.
func userContent(msg core.Message) ai.Content {
	parts := core.InterleavedContentParts(msg)
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
		case core.ContentPartText:
			if part.Text != "" {
				content = append(content, ai.TextBlock(part.Text))
			}
		case core.ContentPartImage:
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
func assistantContent(msg core.Message, model ai.Model) ai.Content {
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
func replayableThinking(msg core.Message, model ai.Model) (ai.Block, bool) {
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

func toImage(img core.Image) ai.Image {
	return ai.Image{MediaType: img.MediaType, Data: img.Data, FileName: img.FileName}
}

// toTools converts San's tool schemas. Run stays nil: San executes tools
// itself and hands the results back as history, so the SDK is never asked to
// run one.
func toTools(schemas []ToolSchema) []ai.Tool {
	if len(schemas) == 0 {
		return nil
	}
	out := make([]ai.Tool, len(schemas))
	for i, schema := range schemas {
		out[i] = ai.Tool{
			Name:        schema.Name,
			Description: schema.Description,
			Parameters:  schema.Parameters,
		}
	}
	return out
}

// toResponse projects a finished SDK response onto San's response fields.
func toResponse(resp *ai.Response) *CompletionResponse {
	if resp == nil {
		return nil
	}
	out := &CompletionResponse{
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
		out.Reasoning = append(out.Reasoning, core.ReasoningItem{
			ID:               item.ID,
			EncryptedContent: item.EncryptedContent,
			Summary:          item.Summary,
		})
	}
	for _, call := range resp.ToolCalls() {
		out.ToolCalls = append(out.ToolCalls, core.ToolCall{
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
func toStopReason(reason ai.StopReason) core.StopReason {
	switch reason {
	case ai.StopToolUse:
		return core.StopToolUse
	case ai.StopMaxTokens:
		return core.StopMaxTokens
	case ai.StopAborted:
		return core.StopCancelled
	case ai.StopError:
		return core.StopError
	default:
		return core.StopEndTurn
	}
}

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
		Pricing:          toModelPricing(m.Pricing),
		Stage:            toModelStage(m.Stage),
		Replacement:      m.Replacement,
		AcceptsImages:    m.Accepts(ai.ModalityImage),
		RejectsTools:     m.Unsupported.Tools,
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

// toModelPricing carries the vendor's published rate across. A model with no
// card reports nil rather than zeros, because free and unstated are different
// answers and the picker renders them differently.
func toModelPricing(p ai.Pricing) *ModelPricing {
	if p.Input <= 0 && p.Output <= 0 {
		return nil
	}
	currency := Currency(p.Currency)
	if currency == "" {
		currency = CurrencyUSD
	}
	return &ModelPricing{Currency: currency, Input: p.Input, Output: p.Output}
}

// toModelStage maps the catalog's lifecycle onto San's. A retired model never
// reaches here — ListModels drops it — so it has no San equivalent.
func toModelStage(stage ai.Stage) ModelStage {
	switch stage {
	case ai.StagePreview:
		return ModelPreview
	case ai.StageDeprecated:
		return ModelDeprecated
	default:
		return ModelStable
	}
}
