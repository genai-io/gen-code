package core

import (
	"context"
	"strings"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// Shortening a conversation that has outgrown its window.
//
// Neither the exchange's nor the mailbox's: the loop asks at a step boundary
// and again when a provider calls the prompt too long, and the person asks
// through the inbox with /compact. All three collapse to the same one message,
// announced the same way.

// MinMessagesToCompact is the shortest conversation worth shortening: an
// opening turn and its reply. Summarising fewer costs a model call to make the
// prompt no shorter.
const MinMessagesToCompact = 3

// preStep shortens the conversation before it outgrows the window. The figure
// is the SDK's, measured fresh at every boundary — the whole prompt, tool
// schemas included. San used to keep the previous response's, which had to be
// cleared by hand after a compaction or it would read "full" forever.
func (a *agent) preStep(ctx context.Context, c sdkagent.PreStepContext) ([]Message, error) {
	if a.compactFunc == nil || len(c.Messages) < MinMessagesToCompact {
		return nil, nil
	}
	limit := a.promptBudget()
	if limit <= 0 || !NeedsCompaction(c.Tokens, limit) {
		return nil, nil
	}
	return a.shorten(ctx, c.Messages, "auto")
}

// onInferError answers the one failure the loop cannot: a prompt the provider
// called too long. Replaying it unchanged fails the same way, so the
// conversation is shortened and the step taken again. Two goes — a summary
// that is still too long is a summarizer problem.
func (a *agent) onInferError(ctx context.Context, c sdkagent.InferErrorContext) (*sdkagent.Retry, error) {
	if a.compactFunc == nil || !ai.IsContextExceeded(c.Err) || c.Attempt > 2 {
		return nil, nil
	}
	if len(c.Messages) < MinMessagesToCompact {
		return nil, nil
	}
	shorter, err := a.shorten(ctx, c.Messages, "auto")
	if err != nil || shorter == nil {
		// Nothing left to shorten: give up as the loop would, on the model's
		// own failure rather than on anything invented here.
		return nil, nil
	}
	return &sdkagent.Retry{Messages: shorter}, nil
}

// shorten is the compaction both hooks share: announce the wait, call the
// application's summarizer, and hand back the conversation that replaces this
// one. Nil means it could not, which both callers read as "leave it alone".
func (a *agent) shorten(ctx context.Context, msgs []Message, trigger string) ([]Message, error) {
	// Shortening is a model call of its own and takes as long as one. The hook
	// says so, because only the code deciding to shorten knows it is about to.
	sdkagent.Compacting(ctx)

	summary, err := a.compactFunc(ctx, msgs)
	if err != nil || strings.TrimSpace(summary) == "" {
		return nil, nil
	}
	return []Message{a.summaryMessage(ctx, summary, len(msgs), trigger)}, nil
}

// applyCompaction replaces the conversation with a summary in place. This is
// the manual /compact path; the automatic one hands its replacement to the loop
// instead, which announces it at the boundary that made it.
func (a *agent) applyCompaction(ctx context.Context, summary string, originalCount int, trigger string) {
	if strings.TrimSpace(summary) == "" {
		return
	}
	a.SetMessages([]Message{a.summaryMessage(ctx, summary, originalCount, trigger)})
}

// summaryMessage builds the message a compaction collapses to and announces
// it, so both paths record identically: a stable ID, and a Compacted carrying
// it as the boundary replay truncates at.
func (a *agent) summaryMessage(ctx context.Context, summary string, originalCount int, trigger string) Message {
	msg := UserMessage(FormatCompactSummary(summary), nil)
	msg.ID = NewMessageID()
	// Announced as an append before the boundary that truncates at it, so
	// replay can resolve the ID the next inference references.
	a.emit(ctx, sdkagent.MessageAdded{Message: msg})
	a.emit(ctx, Compacted{
		Summary:          summary,
		OriginalCount:    originalCount,
		SummaryMessageID: msg.ID,
		Trigger:          trigger,
	})
	return msg
}

// promptBudget is what auto-compaction measures against, or zero when the
// application did not say.
func (a *agent) promptBudget() int {
	if a.inputLimit == nil {
		return 0
	}
	return a.inputLimit()
}

// CompactMaxTokens is the max output tokens for compaction LLM calls.
const CompactMaxTokens = 4096

// CompactSummaryPrefix marks a user message as the post-compaction summary, so
// the UI draws a notice rather than a user turn. The model and the store keep
// the full text.
const CompactSummaryPrefix = "Previous context:\n"

// FormatCompactSummary marks a summary for injection as a user message.
func FormatCompactSummary(summary string) string {
	return CompactSummaryPrefix + summary
}

// IsCompactSummary reports whether content came from FormatCompactSummary.
func IsCompactSummary(content string) bool {
	return strings.HasPrefix(content, CompactSummaryPrefix)
}

// --- context keys ---
