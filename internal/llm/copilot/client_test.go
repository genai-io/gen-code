package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/llm/copilot/oauth"
)

// recordingTransport captures the request and replies with a canned body, so
// tests can assert on the URL and headers the provider actually sends.
type recordingTransport struct {
	status int
	body   string
	last   *http.Request
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.last = req
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Request:    req,
	}, nil
}

func newTestClient(tr http.RoundTripper) *Client {
	sdk := openai.NewClient(
		option.WithBaseURL("https://api.githubcopilot.com/"),
		option.WithAPIKey("test-bearer"),
		option.WithMaxRetries(0),
		option.WithHTTPClient(&http.Client{Transport: tr}),
	)
	return NewClient(sdk, "copilot:subscription")
}

func TestListModelsRequestsTheCatalogEndpoint(t *testing.T) {
	tr := &recordingTransport{body: copilotCatalogJSON}

	models, err := newTestClient(tr).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected the catalog to yield models")
	}
	if got := tr.last.URL.String(); got != "https://api.githubcopilot.com/models" {
		t.Errorf("catalog URL = %q, want the Copilot /models endpoint", got)
	}
}

func TestListModelsFailsWhenNoChatModelsAreEntitled(t *testing.T) {
	// An account with only embedding entitlements can't run a turn, so connect
	// must fail rather than record a provider with nothing to select.
	tr := &recordingTransport{body: `{"data":[{"id":"text-embedding-3-small","model_picker_enabled":true,
	  "capabilities":{"type":"embeddings","supports":{"tool_calls":false}}}]}`}

	if _, err := newTestClient(tr).ListModels(context.Background()); err == nil {
		t.Fatal("expected ListModels to fail when no chat model is available")
	}
}

// TestListModelsAsksForSignInOnEndpointAuthFailure covers the 401/403 the
// endpoint itself returns. Subscription auth has no API key to check, so the
// advice must be "sign in", not the normalizer's "check your key".
func TestListModelsAsksForSignInOnEndpointAuthFailure(t *testing.T) {
	tr := &recordingTransport{status: http.StatusUnauthorized, body: `{"error":{"message":"bad token"}}`}

	_, err := newTestClient(tr).ListModels(context.Background())
	if err == nil {
		t.Fatal("expected ListModels to surface a 401 rather than fall back")
	}
	if !strings.Contains(err.Error(), "sign-in required") {
		t.Errorf("ListModels error = %q, want it to say sign-in is required", err)
	}
}

// TestListModelsReportsSignInWhenCredentialIsMissing covers the other half: a
// credential failure raised by the token source before any request goes out.
func TestListModelsReportsSignInWhenCredentialIsMissing(t *testing.T) {
	sdk := openai.NewClient(
		option.WithBaseURL("https://api.githubcopilot.com/"),
		option.WithAPIKey("test-bearer"),
		option.WithMaxRetries(0),
		option.WithMiddleware(func(*http.Request, option.MiddlewareNext) (*http.Response, error) {
			return nil, &oauth.CredentialError{Err: errors.New("not signed in")}
		}),
	)

	_, err := NewClient(sdk, "copilot:subscription").ListModels(context.Background())
	var credErr *oauth.CredentialError
	if !errors.As(err, &credErr) {
		t.Fatalf("ListModels error = %T (%v), want the credential error preserved", err, err)
	}
	if !strings.Contains(err.Error(), "sign-in required") {
		t.Errorf("ListModels error = %q, want it to say sign-in is required", err)
	}
}

// streamHeaders runs one turn through the client and returns the headers that
// reached the wire — the per-turn headers are only observable there, since the
// SDK's request options are opaque until applied.
func streamHeaders(t *testing.T, messages []core.Message) http.Header {
	t.Helper()
	tr := &recordingTransport{body: "data: [DONE]\n\n"}

	stream := newTestClient(tr).Stream(context.Background(), llm.CompletionOptions{
		Model:    "gpt-5",
		Messages: messages,
	})
	for range stream { //nolint:revive // draining the stream is what completes the request
	}

	if tr.last == nil {
		t.Fatal("no request was sent")
	}
	return tr.last.Header
}

func TestStreamMarksAgentTurns(t *testing.T) {
	tests := []struct {
		name     string
		messages []core.Message
		want     string
	}{
		{
			name:     "first user turn",
			messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			want:     "user",
		},
		{
			name: "turn after an assistant reply",
			messages: []core.Message{
				{Role: core.RoleUser, Content: "hi"},
				{Role: core.RoleAssistant, Content: "hello"},
				{Role: core.RoleUser, Content: "again"},
			},
			want: "agent",
		},
		{
			name: "turn replaying a tool result",
			messages: []core.Message{
				{Role: core.RoleUser, Content: "hi"},
				{Role: core.RoleUser, ToolResult: &core.ToolResult{ToolCallID: "1", Content: "done"}},
			},
			want: "agent",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamHeaders(t, tc.messages).Get("X-Initiator"); got != tc.want {
				t.Errorf("X-Initiator = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStreamOptsIntoVisionOnlyWithImages(t *testing.T) {
	textOnly := []core.Message{{Role: core.RoleUser, Content: "hi"}}
	if got := streamHeaders(t, textOnly).Get("Copilot-Vision-Request"); got != "" {
		t.Errorf("vision header on a text-only turn = %q, want it unset", got)
	}

	withImage := []core.Message{{
		Role:    core.RoleUser,
		Content: "what is this",
		Images:  []core.Image{{MediaType: "image/png", Data: "aGk="}},
	}}
	if got := streamHeaders(t, withImage).Get("Copilot-Vision-Request"); got != "true" {
		t.Errorf("vision header on an image turn = %q, want true", got)
	}
}

func TestSubscriptionMetaUsesInteractiveAuth(t *testing.T) {
	if SubscriptionMeta.Provider != llm.Copilot {
		t.Errorf("provider = %q, want copilot", SubscriptionMeta.Provider)
	}
	if SubscriptionMeta.AuthMethod != llm.AuthSubscription {
		t.Errorf("auth method = %q, want subscription", SubscriptionMeta.AuthMethod)
	}
	// EnvVars must stay empty: a non-empty list would make the registry treat
	// the provider as API-key auth and skip the sign-in path entirely.
	if len(SubscriptionMeta.EnvVars) != 0 {
		t.Errorf("EnvVars = %v, want none for an OAuth provider", SubscriptionMeta.EnvVars)
	}
	if !llm.SupportsInteractiveLogin(llm.Copilot, llm.AuthSubscription) {
		t.Error("copilot:subscription should register an interactive authenticator")
	}
}

func TestRetargetSwitchesToEnterpriseHost(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	retarget(req, "https://api.business.githubcopilot.com")
	if req.URL.Host != "api.business.githubcopilot.com" {
		t.Errorf("host = %q, want the reported enterprise host", req.URL.Host)
	}
	if req.URL.Path != "/chat/completions" {
		t.Errorf("path = %q, want it preserved", req.URL.Path)
	}

	// A malformed endpoint must leave the request alone rather than send it nowhere.
	retarget(req, "::not a url::")
	if req.URL.Host != "api.business.githubcopilot.com" {
		t.Errorf("host = %q, want it unchanged by an unparseable endpoint", req.URL.Host)
	}
}

func TestStreamSendsTheOutputCapAsMaxTokens(t *testing.T) {
	tr := &recordingTransport{body: "data: [DONE]\n\n"}

	stream := newTestClient(tr).Stream(context.Background(), llm.CompletionOptions{
		Model:     "gpt-5",
		MaxTokens: 4096,
		Messages:  []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	for range stream { //nolint:revive // draining the stream is what completes the request
	}

	body, err := io.ReadAll(tr.last.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if got, ok := sent["max_tokens"]; !ok || got != float64(4096) {
		t.Errorf("max_tokens = %v (present=%v), want 4096", got, ok)
	}
	// The two caps are mutually exclusive; sending both is a 400.
	if _, ok := sent["max_completion_tokens"]; ok {
		t.Error("max_completion_tokens was sent alongside max_tokens")
	}
}
