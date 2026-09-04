package core

import (
	"context"
	"strings"

	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// Shortening a conversation that has outgrown its window, and the vocabulary
// for what that leaves behind.
//
// It is not the exchange's, which is why it is not in exchange.go: the loop
// asks for it at a step boundary and again when a provider calls the prompt
// too long, and the person asks for it through the inbox with /compact. The
// two automatic paths hand the replacement to the loop; the manual one sets it
// in place. All three collapse to the same one message, announced the same way.

// preStep shortens the conversation before it outgrows the window.
//
// The figure it measures is the SDK's, taken fresh at every boundary — the
// whole prompt, tool schemas included. San used to measure the previous
// response's total input instead, which had to be cleared by hand after a
// compaction or the stale figure would still read "full" and compact again on
// the very next step, forever. There is nothing to clear now.
func (a *agent) preStep(ctx context.Context, c sdkagent.PreStepContext) ([]Message, error) {
	if a.compactFunc == nil || len(c.Messages) < 3 {
		return nil, nil
	}
	limit := a.promptBudget()
	if limit <= 0 || !NeedsCompaction(c.Tokens, limit) {
		return nil, nil
	}
	return a.summarise(ctx, c.Messages, "auto")
}

// onInferError answers the one failure the loop cannot: a prompt the provider
// called too long. Replaying it unchanged fails the same way, so the
// conversation is shortened and the step taken again.
//
// Attempt is the budget: two goes, because a summary that is still too long is
// a summariser problem and a third attempt will not fix it.
func (a *agent) onInferError(ctx context.Context, c sdkagent.InferErrorContext) (*sdkagent.Retry, error) {
	if a.compactFunc == nil || !ai.IsContextExceeded(c.Err) || c.Attempt > 2 {
		return nil, nil
	}
	if len(c.Messages) < 3 {
		return nil, nil
	}
	shorter, err := a.summarise(ctx, c.Messages, "auto")
	if err != nil || shorter == nil {
		// Nothing left to shorten: give up as the loop would, on the model's
		// own failure rather than on anything invented here.
		return nil, nil
	}
	return &sdkagent.Retry{Messages: shorter}, nil
}

// summarise is the compaction both hooks share: announce the wait, call the
// application's summariser, and hand back the conversation that replaces this
// one. Nil means it could not, which both callers read as "leave it alone".
func (a *agent) summarise(ctx context.Context, msgs []Message, trigger string) ([]Message, error) {
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
// the compaction itself, so both paths record identically: a stable ID the
// transcript can reference, and a CompactEvent carrying it as the boundary
// replay truncates at.
func (a *agent) summaryMessage(ctx context.Context, summary string, originalCount int, trigger string) Message {
	msg := UserMessage(FormatCompactSummary(summary), nil)
	msg.ID = NewMessageID()
	// The summary is announced as an appended message before the boundary that
	// truncates at it, so transcript replay can resolve the ID the next
	// inference references. A compaction always collapses to exactly this one
	// message, which is why one append says all of it.
	a.emit(ctx, AppendEvent(a.id, msg))
	a.emit(ctx, CompactEvent(a.id, CompactInfo{
		Summary:          summary,
		OriginalCount:    originalCount,
		SummaryMessageID: msg.ID,
		Trigger:          trigger,
	}))
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

// CompactSummaryPrefix marks a user message as the post-compaction summary.
// The UI uses it to render that message as a system notice rather than a normal
// user turn, while the model and session store keep the full text.
const CompactSummaryPrefix = "Previous context:\n"

// FormatCompactSummary formats a compaction summary for injection as a user message.
func FormatCompactSummary(summary string) string {
	return CompactSummaryPrefix + summary
}

// IsCompactSummary reports whether content is a post-compaction summary message
// (produced by FormatCompactSummary).
func IsCompactSummary(content string) bool {
	return strings.HasPrefix(content, CompactSummaryPrefix)
}

// --- context keys ---
