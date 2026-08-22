package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/core"
)

// retryInfo reports whether err was tagged retryable and, if so, its hint.
func retryInfo(err error) (after time.Duration, retryable bool) {
	var re core.RetryableError
	if errors.As(err, &re) {
		return re.RetryAfter(), true
	}
	return 0, false
}

// vendorErr is a failure as the SDK's driver hands it over: already classified
// from the provider's own typed error.
func vendorErr(kind ai.ErrorKind, status int, message string) error {
	return &ai.Error{Driver: "test", Kind: kind, Status: status, Message: message}
}

func TestClassifyNilIsNil(t *testing.T) {
	if got := Classify(nil); got != nil {
		t.Fatalf("Classify(nil) = %v, want nil", got)
	}
	if got := ClassifyStream(nil); got != nil {
		t.Fatalf("ClassifyStream(nil) = %v, want nil", got)
	}
}

// The two entry points differ on one thing only: what an unclassifiable
// failure counts as. Everything the SDK did classify must land the same way
// through both, because a completed call and a broken stream disagree about
// nothing else.
func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		// want* are for Classify; wantStreamRetry for ClassifyStream.
		wantRetry       bool
		wantStreamRetry bool
		wantAfter       time.Duration
		wantContext     bool
	}{
		{name: "overloaded", err: vendorErr(ai.KindOverloaded, 503, "unavailable"), wantRetry: true, wantStreamRetry: true},
		{name: "network", err: vendorErr(ai.KindNetwork, 0, "connection reset"), wantRetry: true, wantStreamRetry: true},
		{name: "rate limit", err: vendorErr(ai.KindRateLimit, 429, "slow down"), wantRetry: true, wantStreamRetry: true},
		{name: "auth", err: vendorErr(ai.KindAuth, 401, "unauthorized")},
		{name: "invalid request", err: vendorErr(ai.KindInvalidRequest, 400, "bad request")},
		{name: "unsupported", err: vendorErr(ai.KindUnsupported, 0, "no tools on this model")},
		{name: "cancelled", err: vendorErr(ai.KindCanceled, 0, "cancelled")},
		{name: "context exceeded", err: vendorErr(ai.KindContextExceeded, 400, "prompt is too long"), wantContext: true},

		// Failures that never became a typed provider error. Only a stream
		// gets the benefit of the doubt.
		{name: "opaque", err: errors.New("something opaque"), wantStreamRetry: true},
		{name: "wrapped opaque", err: fmt.Errorf("reading response: %w", errors.New("opaque cause")), wantStreamRetry: true},
		{name: "http2 GOAWAY", err: errors.New(`http2: server sent GOAWAY and closed the connection; LastStreamID=7`), wantStreamRetry: true},
		{name: "EOF", err: io.EOF, wantRetry: true, wantStreamRetry: true},
		{name: "deadline exceeded", err: context.DeadlineExceeded, wantRetry: true, wantStreamRetry: true},
		{name: "context canceled", err: context.Canceled},
		{name: "wrapped context canceled", err: fmt.Errorf("stopped: %w", context.Canceled)},
		{name: "overflow text", err: errors.New("maximum context length exceeded"), wantContext: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, variant := range []struct {
				name      string
				got       error
				wantRetry bool
			}{
				{name: "Classify", got: Classify(tc.err), wantRetry: tc.wantRetry},
				{name: "ClassifyStream", got: ClassifyStream(tc.err), wantRetry: tc.wantStreamRetry},
			} {
				t.Run(variant.name, func(t *testing.T) {
					_, gotRetry := retryInfo(variant.got)
					if gotRetry != variant.wantRetry {
						t.Fatalf("retryable = %v, want %v", gotRetry, variant.wantRetry)
					}
					var exceeded core.ContextExceededError
					if errors.As(variant.got, &exceeded) != tc.wantContext {
						t.Fatalf("context exceeded = %v, want %v", !tc.wantContext, tc.wantContext)
					}
					// The original has to survive: callers match on it with
					// errors.Is and show its message to the user.
					if !errors.Is(variant.got, tc.err) {
						t.Fatal("the original error is not in the chain")
					}
					if variant.got.Error() != tc.err.Error() {
						t.Fatalf("Error() = %q, want %q", variant.got.Error(), tc.err.Error())
					}
				})
			}
		})
	}
}

// A rate limit's own wait hint has to reach the loop, which floors its backoff
// at whatever the provider asked for.
func TestClassifyCarriesTheRateLimitHint(t *testing.T) {
	err := &ai.Error{Driver: "test", Kind: ai.KindRateLimit, Status: 429, RetryAfter: 8 * time.Second}

	after, retryable := retryInfo(Classify(err))
	if !retryable {
		t.Fatal("a rate limit was not marked retryable")
	}
	if after != 8*time.Second {
		t.Fatalf("RetryAfter = %v, want 8s", after)
	}
}

// Retrying an oversized prompt unchanged just fails again — the loop has to
// compact first — so the two tags stay mutually exclusive.
func TestContextExceededIsNotAlsoRetryable(t *testing.T) {
	for _, err := range []error{
		vendorErr(ai.KindContextExceeded, 400, "prompt is too long: 213423 tokens > 200000 maximum"),
		errors.New("This model's maximum context length is 128000 tokens."),
		errors.New("The input token count 1050000 exceeds the maximum number of tokens allowed 1048576."),
	} {
		if _, retryable := retryInfo(Classify(err)); retryable {
			t.Errorf("%v is retryable; want context-exceeded only", err)
		}
	}
}

// A throttle that happens to be worded like an overflow must not be read as
// one: compacting a prompt that was never too long discards history and
// suppresses the retry that would have worked. AWS Bedrock phrases a rate
// limit as "Too many tokens, please wait before trying again."
func TestAThrottleIsNotAnOverflow(t *testing.T) {
	err := errors.New("ThrottlingException: Too many tokens, please wait before trying again.")

	var exceeded core.ContextExceededError
	if errors.As(Classify(err), &exceeded) {
		t.Fatal("a throttle was classified as a context overflow")
	}
}

// Nothing unrelated may be mistaken for an overflow either.
func TestOrdinaryFailuresAreNotOverflows(t *testing.T) {
	for _, msg := range []string{"dial tcp: connection refused", "invalid api key", ""} {
		var exceeded core.ContextExceededError
		if errors.As(Classify(errors.New(msg)), &exceeded) {
			t.Errorf("%q was classified as a context overflow", msg)
		}
	}
}

// Classification runs at the vendor seam and again at the stream boundary.
// The second pass must leave the first pass's answer alone, hint included.
func TestClassifyingTwiceChangesNothing(t *testing.T) {
	once := Classify(&ai.Error{Driver: "test", Kind: ai.KindRateLimit, Status: 429, RetryAfter: 5 * time.Second})
	twice := ClassifyStream(once)

	if twice != once {
		t.Fatalf("re-classifying rewrapped the error: %T", twice)
	}
	if after, _ := retryInfo(twice); after != 5*time.Second {
		t.Fatalf("RetryAfter = %v, want 5s", after)
	}
}
