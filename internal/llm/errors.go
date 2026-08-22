package llm

import (
	"context"
	"errors"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/core"
)

// A provider failure, in the vocabulary the agent loop reads.
//
// San's loop asks two questions of an error: retry it (core.RetryableError),
// or compact and retry (core.ContextExceededError). Answering them is the
// whole of this file. The classification itself belongs to the SDK, which
// decides from typed provider errors — an HTTP status, a vendor error code —
// the only place that decision is reliable; what is here is the translation
// onto the two interfaces core understands, so core carries no provider error
// vocabulary and San keeps no second copy of the SDK's tables.

// Classify tags a provider failure with what the agent loop needs to know
// about it. A failure of no recognized kind is returned unchanged, which
// leaves it fatal — the conservative call for a call that completed.
func Classify(err error) error {
	return tag(err, ai.Classify("", 0, nil, "", "", err))
}

// classifyStream is Classify for an error that terminated a stream, where a
// transport routinely loses the typed error. The SDK owns that rule —
// ai.StreamError is ai.Classify plus "an unrecognized terminal failure is a
// transport failure" — so San does not keep a second copy of it.
func classifyStream(err error) error {
	return tag(err, ai.StreamError("", 0, nil, "", "", err))
}

// tag translates the SDK's reading of err onto the two interfaces core
// understands, wrapping the original rather than the *ai.Error so the
// provider's own message and error chain stay intact.
//
// Cancellation and an already-tagged error pass through untouched: the first
// is the user's own interrupt, and the second was tagged where more was known
// about it.
func tag(err error, classified *ai.Error) error {
	if err == nil || errors.Is(err, context.Canceled) || tagged(err) {
		return err
	}
	switch classified.Kind {
	case ai.KindContextExceeded:
		return contextExceeded{err}
	case ai.KindRateLimit:
		return retryable{err: err, after: classified.RetryAfter}
	case ai.KindOverloaded, ai.KindNetwork:
		return retryable{err: err}
	default:
		// Auth, invalid request, unsupported, cancelled: retrying cannot help.
		return err
	}
}

// tagged reports whether err already carries one of the two answers.
func tagged(err error) bool {
	var retry core.RetryableError
	var exceeded core.ContextExceededError
	return errors.As(err, &retry) || errors.As(err, &exceeded)
}

// retryable marks a transient failure, carrying the provider's own wait hint
// when it sent one.
type retryable struct {
	err   error
	after time.Duration
}

func (e retryable) Error() string             { return e.err.Error() }
func (e retryable) Unwrap() error             { return e.err }
func (e retryable) RetryAfter() time.Duration { return e.after }

// contextExceeded marks a prompt that outgrew the model's window. It is
// deliberately not also retryable: the loop checks for the overflow first and
// compacts, and leaving it fatal stops the retry budget from being spent
// replaying a prompt that cannot fit.
type contextExceeded struct{ err error }

func (e contextExceeded) Error() string    { return e.err.Error() }
func (e contextExceeded) Unwrap() error    { return e.err }
func (e contextExceeded) ContextExceeded() {}

var (
	_ core.RetryableError       = retryable{}
	_ core.ContextExceededError = contextExceeded{}
)
