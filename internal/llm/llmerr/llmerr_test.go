package llmerr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"

	"github.com/genai-io/san/internal/core"
)

// retryInfo reports whether Wrap tagged err as retryable and, if so, its
// Retry-After hint.
func retryInfo(err error) (after time.Duration, retryable bool) {
	var re core.RetryableError
	if errors.As(err, &re) {
		return re.RetryAfter(), true
	}
	return 0, false
}

func dummyReq() *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "https://example.com/v1/x", nil)
	return r
}

func httpResp(code int, hdr http.Header) *http.Response {
	if hdr == nil {
		hdr = http.Header{}
	}
	return &http.Response{StatusCode: code, Header: hdr, Request: dummyReq()}
}

type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "i/o timeout" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

var _ net.Error = fakeTimeout{}

func TestWrapNilIsNil(t *testing.T) {
	if got := Wrap(nil); got != nil {
		t.Fatalf("Wrap(nil) = %v, want nil", got)
	}
}

func TestWrapClassification(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantRetryable bool
		wantAfter     time.Duration
	}{
		{
			name:          "anthropic 529 overloaded is retryable",
			err:           &anthropic.Error{StatusCode: 529, Request: dummyReq(), Response: httpResp(529, nil)},
			wantRetryable: true,
		},
		{
			name:          "anthropic 400 bad request is fatal",
			err:           &anthropic.Error{StatusCode: 400, Request: dummyReq(), Response: httpResp(400, nil)},
			wantRetryable: false,
		},
		{
			name:          "anthropic 401 auth is fatal",
			err:           &anthropic.Error{StatusCode: 401, Request: dummyReq(), Response: httpResp(401, nil)},
			wantRetryable: false,
		},
		{
			name:          "openai 429 honors Retry-After",
			err:           &openai.Error{StatusCode: 429, Request: dummyReq(), Response: httpResp(429, http.Header{"Retry-After": {"8"}})},
			wantRetryable: true,
			wantAfter:     8 * time.Second,
		},
		{
			name:          "openai 429 without header still retryable",
			err:           &openai.Error{StatusCode: 429, Request: dummyReq(), Response: httpResp(429, nil)},
			wantRetryable: true,
		},
		{
			name:          "openai 503 is retryable",
			err:           &openai.Error{StatusCode: 503, Request: dummyReq(), Response: httpResp(503, nil)},
			wantRetryable: true,
		},
		{
			name:          "openai 422 is fatal",
			err:           &openai.Error{StatusCode: 422, Request: dummyReq(), Response: httpResp(422, nil)},
			wantRetryable: false,
		},
		{
			name:          "io.EOF is retryable",
			err:           io.EOF,
			wantRetryable: true,
		},
		{
			name:          "net timeout is retryable",
			err:           fakeTimeout{},
			wantRetryable: true,
		},
		{
			name:          "context canceled is fatal",
			err:           context.Canceled,
			wantRetryable: false,
		},
		{
			name:          "plain error is fatal",
			err:           errors.New("something opaque"),
			wantRetryable: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			after, retryable := retryInfo(Wrap(tc.err))
			if retryable != tc.wantRetryable {
				t.Fatalf("retryable = %v, want %v", retryable, tc.wantRetryable)
			}
			if retryable && after != tc.wantAfter {
				t.Fatalf("RetryAfter = %v, want %v", after, tc.wantAfter)
			}
		})
	}
}

func TestWrapAndWrapStreamClassification(t *testing.T) {
	issueGoAway := errors.New(`http2: server sent GOAWAY and closed the connection; LastStreamID=7, ErrCode=NO_ERROR, debug=""`)
	issueStream := errors.New("stream error: stream ID 11; INTERNAL_ERROR; received from peer")
	apiErr := func(code int, message string) error {
		return &openai.Error{
			StatusCode: code,
			Message:    message,
			Request:    dummyReq(),
			Response:   httpResp(code, nil),
		}
	}
	contextOverflow := errors.New("maximum context length exceeded")

	tests := []struct {
		name            string
		err             error
		wantWrapRetry   bool
		wantStreamRetry bool
		wantContext     bool
	}{
		{name: "issue GOAWAY text", err: issueGoAway, wantStreamRetry: true},
		{name: "issue stream text", err: issueStream, wantStreamRetry: true},
		{name: "ordinary opaque", err: errors.New("something opaque"), wantStreamRetry: true},
		{name: "wrapped opaque", err: fmt.Errorf("reading response: %w", errors.New("opaque cause")), wantStreamRetry: true},
		{name: "marked non-retryable", err: MarkNonRetryable(errors.New("semantic stream failure"))},
		{name: "marked retryable", err: MarkRetryable(errors.New("transient stream failure")), wantWrapRetry: true, wantStreamRetry: true},
		{name: "provider 400", err: apiErr(400, "bad request"), wantWrapRetry: false, wantStreamRetry: false},
		{name: "provider 401", err: apiErr(401, "unauthorized"), wantWrapRetry: false, wantStreamRetry: false},
		{name: "provider 429", err: apiErr(429, "rate limited"), wantWrapRetry: true, wantStreamRetry: true},
		{name: "provider 503", err: apiErr(503, "unavailable"), wantWrapRetry: true, wantStreamRetry: true},
		{name: "EOF", err: io.EOF, wantWrapRetry: true, wantStreamRetry: true},
		{name: "net timeout", err: fakeTimeout{}, wantWrapRetry: true, wantStreamRetry: true},
		{name: "context canceled", err: context.Canceled},
		{name: "wrapped context canceled", err: fmt.Errorf("stopped: %w", context.Canceled)},
		{name: "deadline exceeded", err: context.DeadlineExceeded, wantWrapRetry: true, wantStreamRetry: true},
		{name: "context exceeded", err: contextOverflow, wantContext: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, variant := range []struct {
				name          string
				got           error
				wantRetryable bool
			}{
				{name: "Wrap", got: Wrap(tc.err), wantRetryable: tc.wantWrapRetry},
				{name: "WrapStream", got: WrapStream(tc.err), wantRetryable: tc.wantStreamRetry},
			} {
				t.Run(variant.name, func(t *testing.T) {
					_, gotRetryable := retryInfo(variant.got)
					if gotRetryable != variant.wantRetryable {
						t.Fatalf("retryable = %v, want %v", gotRetryable, variant.wantRetryable)
					}
					var gotContext core.ContextExceededError
					if errors.As(variant.got, &gotContext) != tc.wantContext {
						t.Fatalf("context exceeded = %v, want %v", gotContext != nil, tc.wantContext)
					}
					if !errors.Is(variant.got, tc.err) {
						t.Fatal("original error is not preserved in chain")
					}
				})
			}
		})
	}

	if WrapStream(nil) != nil {
		t.Fatal("WrapStream(nil) must return nil")
	}
}

// Wrap must preserve the original error in the chain so substring-based
// detection (e.g. isPromptTooLong) and errors.Is keep working.
func TestWrapPreservesOriginalError(t *testing.T) {
	wrapped := Wrap(io.EOF)
	if !errors.Is(wrapped, io.EOF) {
		t.Fatal("wrapped retryable error should still match io.EOF")
	}
	if wrapped.Error() != io.EOF.Error() {
		t.Fatalf("Error() = %q, want %q", wrapped.Error(), io.EOF.Error())
	}
}

func TestRetryAfterParsesHTTPDate(t *testing.T) {
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	resp := httpResp(429, http.Header{"Retry-After": {future}})
	d := retryAfter(resp)
	if d <= 0 || d > 31*time.Second {
		t.Fatalf("retryAfter(http-date) = %v, want ~30s", d)
	}
}

// A prompt that overflowed the window arrives as a 400/422, which classify
// buckets as fatal. Wrap has to tag it so the turn loop compacts and retries
// instead of surfacing the failure. Provider wordings live here, in the layer
// that knows which provider produced them.
func TestWrapTagsContextExceeded(t *testing.T) {
	overflow := []struct{ name, msg string }{
		{"anthropic", "prompt is too long: 213423 tokens > 200000 maximum"},
		{"anthropic type", `{"type":"error","error":{"type":"prompt_too_long"}}`},
		{"openai", "This model's maximum context length is 128000 tokens. However, your messages resulted in 130512 tokens."},
		{"openai code", `{"code":"context_length_exceeded"}`},
		{"gemini", "The input token count 1050000 exceeds the maximum number of tokens allowed 1048576."},
		{"mixed case", "Prompt Is Too Long"},
	}
	for _, c := range overflow {
		t.Run(c.name, func(t *testing.T) {
			var exceeded core.ContextExceededError
			if !errors.As(Wrap(errors.New(c.msg)), &exceeded) {
				t.Fatalf("Wrap(%q) is not tagged ContextExceededError", c.msg)
			}
			// Retrying an oversized prompt unchanged just fails again, so the
			// two tags must stay mutually exclusive.
			var retryable core.RetryableError
			if errors.As(Wrap(errors.New(c.msg)), &retryable) {
				t.Fatalf("Wrap(%q) is also RetryableError; want context-exceeded only", c.msg)
			}
		})
	}

	// Unrelated failures must not be mistaken for overflow: compacting on a
	// network blip would discard history the retry path could have kept.
	for _, msg := range []string{"dial tcp: connection refused", "invalid api key", ""} {
		var exceeded core.ContextExceededError
		if errors.As(Wrap(errors.New(msg)), &exceeded) {
			t.Fatalf("Wrap(%q) tagged as context-exceeded, want not", msg)
		}
	}
}
