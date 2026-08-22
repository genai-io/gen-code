package sdk

import (
	"context"
	"errors"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm/llmerr"
)

// Carrying the SDK's classification across to the agent loop.
//
// Both sides already classify; what would be lost without this file is the
// translation. The SDK decides from typed provider errors — an HTTP status, a
// vendor error code — which is the only place that decision is reliable. San's
// loop asks two questions of an error: retry it (core.RetryableError), or
// compact and retry (core.ContextExceededError). llmerr's own classifier reads
// provider SDK error types San no longer sees here, and its context-window
// check reads message text, so a kind that arrived typed would be re-derived
// from a substring. Tagging it here keeps the typed answer.

// classify tags a failure with what the agent loop needs to know about it.
// An error of no recognized kind is returned unchanged, which leaves llmerr's
// stream-boundary handling to make the conservative call.
func classify(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}

	var e *ai.Error
	if !errors.As(err, &e) {
		return err
	}

	switch e.Kind {
	case ai.KindContextExceeded:
		// Non-retryable *and* context-exceeded: the loop checks for the
		// overflow first and compacts, and marking it fatal stops the retry
		// budget from being spent replaying a prompt that cannot fit.
		return llmerr.MarkNonRetryable(contextExceeded{err})
	case ai.KindRateLimit:
		return retryAfter{err: err, after: e.RetryAfter}
	case ai.KindOverloaded, ai.KindNetwork:
		return llmerr.MarkRetryable(err)
	case ai.KindAuth, ai.KindInvalidRequest, ai.KindUnsupported, ai.KindCanceled:
		return llmerr.MarkNonRetryable(err)
	default:
		return err
	}
}

// contextExceeded marks a prompt that outgrew the model's window.
type contextExceeded struct{ err error }

func (e contextExceeded) Error() string    { return e.err.Error() }
func (e contextExceeded) Unwrap() error    { return e.err }
func (e contextExceeded) ContextExceeded() {}

// retryAfter marks a rate limit, carrying the provider's own wait hint.
type retryAfter struct {
	err   error
	after time.Duration
}

func (e retryAfter) Error() string             { return e.err.Error() }
func (e retryAfter) Unwrap() error             { return e.err }
func (e retryAfter) RetryAfter() time.Duration { return e.after }

var (
	_ core.ContextExceededError = contextExceeded{}
	_ core.RetryableError       = retryAfter{}
)
