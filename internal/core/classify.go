package core

import (
	"context"
	"errors"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"
)

// A provider failure, in the vocabulary the agent loop reads: retry it
// (RetryableError), or compact and retry (ContextExceededError).
//
// Deciding which belongs to the SDK, where the provider's typed error still
// exists; this file only translates that answer, so the loop carries no
// provider vocabulary and San keeps no second copy of the SDK's tables. It
// lives in core because the streaming call is core's own — there is no package
// boundary in between for a failure to arrive across untagged.

// Classify tags a provider failure with what the agent loop needs to know
// about it. A failure of no recognized kind is returned unchanged, which
// leaves it fatal — the conservative call for a call that completed.
func Classify(err error) error {
	return tag(err, ai.Classify("", 0, nil, "", "", err))
}

// ClassifyStream is Classify for an error that terminated a stream, where a
// transport routinely loses the typed error. The SDK owns that rule —
// ai.StreamError is ai.Classify plus "an unrecognized terminal failure is a
// transport failure" — so San does not keep a second copy of it.
func ClassifyStream(err error) error {
	return tag(err, ai.StreamError("", 0, nil, "", "", err))
}

// tag translates the SDK's reading of err onto the two interfaces the loop
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
	var retry RetryableError
	var exceeded ContextExceededError
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
	_ RetryableError       = retryable{}
	_ ContextExceededError = contextExceeded{}
)
