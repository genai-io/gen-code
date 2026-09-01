package core

import (
	"context"
	"iter"

	"github.com/genai-io/sdk-go/pkg/ai"
)

// Faking the model now means faking the protocol, which is the seam pkg/ai
// already has and the one five real drivers sit behind. A test says what the
// endpoint sends; nothing in between has to be stubbed.

// deltas renders one answer as the stream a driver would produce.
func deltas(r InferResponse) []ai.Delta {
	var out []ai.Delta
	if r.Thinking != "" {
		out = append(out, ai.Delta{Block: ai.ThinkingBlock(r.Thinking, r.ThinkingSignature)},
			ai.Delta{EndBlock: true})
	}
	if r.Content != "" {
		out = append(out, ai.Delta{Block: ai.TextBlock(r.Content)}, ai.Delta{EndBlock: true})
	}
	for _, c := range r.ToolCalls {
		out = append(out, ai.Delta{Block: ai.ToolCallBlock(ai.ToolCall{
			ID: c.ID, Name: c.Name, Input: c.Input, Signature: c.ThoughtSignature,
		})})
	}
	stop := ai.StopEndTurn
	switch {
	case len(r.ToolCalls) > 0:
		stop = ai.StopToolUse
	case r.StopReason == StopMaxTokens:
		stop = ai.StopMaxTokens
	}
	return append(out, ai.Delta{StopReason: stop, Usage: &ai.Usage{
		Input: r.InputTokens, Output: r.OutputTokens,
		CacheWrite: r.CacheCreationInputTokens, CacheRead: r.CacheReadInputTokens,
	}})
}

// yieldAll is the driver body a scripted double needs: send this, then stop.
func yieldAll(script []ai.Delta) iter.Seq2[ai.Delta, error] {
	return func(yield func(ai.Delta, error) bool) {
		for _, d := range script {
			if !yield(d, nil) {
				return
			}
		}
	}
}

// testClient wraps a driver as the client an agent is built on.
func testClient(d ai.Driver) *ai.Client {
	return ai.NewClientWithDriver(d, ai.Model{ID: "stub", API: "stub", ContextWindow: 200_000})
}

var _ = context.Background
