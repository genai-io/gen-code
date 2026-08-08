package openai

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/llm/openai/oauth"
	"github.com/genai-io/san/internal/llm/openaicompat"
)

// codexBaseURL is the ChatGPT subscription (Codex) Responses endpoint root. The
// trailing slash is required: the SDK resolves the "responses" path against it,
// and without the slash the final "codex" segment would be dropped, pointing the
// request at the wrong endpoint.
const codexBaseURL = "https://chatgpt.com/backend-api/codex/"

// modelsFetchTimeout bounds the live catalog request so a slow/blocked fetch
// doesn't wedge the connect flow before falling back to the static list.
const modelsFetchTimeout = 8 * time.Second

// codexClientVersion is the client version sent to the /models endpoint, which
// requires it and returns the model set for that version. We present a recent
// Codex CLI version (matching the codex originator we already send); the backend
// returns the current lineup for any sufficiently recent version. Bump if the
// list ever goes stale.
const codexClientVersion = "0.144.0"

// staticSubscriptionModels is the fallback catalog used when the live ChatGPT
// Codex model list can't be fetched. Keep this intentionally small so the UI
// makes fallback easy to distinguish from a live account-specific catalog.
var staticSubscriptionModels = []string{
	"gpt-5.4",
	"gpt-5.3-codex-spark",
}

// SubscriptionMeta is the metadata for OpenAI via a ChatGPT subscription (OAuth).
// It has no EnvVars because it authenticates with an OAuth login, not a key.
var SubscriptionMeta = llm.Meta{
	Provider:    llm.OpenAI,
	AuthMethod:  llm.AuthSubscription,
	EnvVars:     nil,
	DisplayName: "ChatGPT Subscription",
}

// NewSubscriptionClient creates an OpenAI client that talks to the ChatGPT Codex
// backend using a subscription OAuth token instead of an API key. The bearer
// token and account id are injected per request from a refreshing TokenSource,
// so a long session survives token expiry transparently.
func NewSubscriptionClient(ctx context.Context) (llm.Provider, error) {
	tokens := oauth.NewTokenSource()

	sdk := openai.NewClient(
		option.WithBaseURL(codexBaseURL),
		option.WithMaxRetries(0),
		option.WithHeader("OpenAI-Beta", "responses=experimental"),
		option.WithHeader("originator", oauth.Originator),
		option.WithHeader("session_id", llm.NewRequestID()),
		option.WithHeader("User-Agent", oauth.Originator),
		option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			access, accountID, err := tokens.Token(req.Context())
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+access)
			req.Header.Set("chatgpt-account-id", accountID)
			return next(req)
		}),
	)

	c := NewClient(sdk, "openai:subscription")
	c.subscription = true
	return c, nil
}

// subscriptionCatalog returns the ChatGPT Codex model catalog the backend
// advertises for this account.
//
// A credential/permission failure (401/403) is propagated, not masked: connect
// verifies the account by listing models, so returning fallback models on a
// bad/expired token or an account without Codex access would falsely mark the
// provider "connected" and only fail on the first real request. Transient,
// offline, or unexpected-shape errors fall back to the static list so a flaky
// catalog endpoint doesn't block an otherwise-usable session.
func (c *Client) subscriptionCatalog(ctx context.Context) ([]llm.ModelInfo, error) {
	var resp codexModelsResponse
	err := c.client.Get(ctx, "models", nil, &resp,
		option.WithRequestTimeout(modelsFetchTimeout),
		option.WithQuery("client_version", codexClientVersion))
	if err != nil {
		// Credential-source failures (no token, or an unrefreshable/revoked token,
		// raised by the auth middleware before the request is sent) and 401/403
		// from the endpoint both mean the connection isn't usable — surface them so
		// connect, which verifies the account by listing models, doesn't record a
		// signed-out/unauthorized account as connected and fail on the first real
		// request. Subscription auth has no API key to check; the fix is to sign in
		// again, so don't route through the API-key-oriented normalizer.
		var credErr *oauth.CredentialError
		if errors.As(err, &credErr) || openaicompat.IsAuthError(err) {
			return nil, fmt.Errorf("ChatGPT subscription sign-in required for Codex access: %w", err)
		}
		// Transient/offline/unexpected-shape errors fall back to the static list
		// so a flaky catalog endpoint doesn't block an otherwise-usable session.
		return staticSubscriptionCatalog(), nil
	}
	if models := resp.toModelInfos(); len(models) > 0 {
		return models, nil
	}
	return staticSubscriptionCatalog(), nil
}

// staticSubscriptionCatalog builds the fallback catalog from the static slugs.
func staticSubscriptionCatalog() []llm.ModelInfo {
	models := make([]llm.ModelInfo, 0, len(staticSubscriptionModels))
	for _, id := range staticSubscriptionModels {
		models = append(models, openAIModelInfo(id))
	}
	slices.SortFunc(models, func(a, b llm.ModelInfo) int { return cmp.Compare(a.ID, b.ID) })
	return models
}

// codexModelsResponse is the ChatGPT Codex /models catalog, keyed under "models".
type codexModelsResponse struct {
	Models []codexModel `json:"models"`
}

// codexModel is one catalog entry. The request id is the "slug"; the backend also
// reports the context window and, via show_in_picker, whether the entry should be
// user-selectable (null means shown).
type codexModel struct {
	Slug                     string                `json:"slug"`
	DisplayName              string                `json:"display_name"`
	Description              string                `json:"description"`
	ContextWindow            int                   `json:"context_window"`
	ShowInPicker             *bool                 `json:"show_in_picker"`
	SupportedReasoningLevels []codexReasoningLevel `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string                `json:"default_reasoning_level"`
}

// codexReasoningLevel is one entry of supported_reasoning_levels. The backend
// also sends a per-level "description" blurb next to each effort, which san
// doesn't surface, so only the effort label is parsed.
type codexReasoningLevel struct {
	Effort string `json:"effort"`
}

// toModelInfos converts the catalog to llm.ModelInfo, dropping picker-hidden and
// duplicate entries and taking token limits from the backend when reported.
func (r codexModelsResponse) toModelInfos() []llm.ModelInfo {
	seen := make(map[string]bool, len(r.Models))
	models := make([]llm.ModelInfo, 0, len(r.Models))
	for _, m := range r.Models {
		if m.Slug == "" || seen[m.Slug] {
			continue
		}
		if m.ShowInPicker != nil && !*m.ShowInPicker {
			continue
		}
		seen[m.Slug] = true

		info := openAIModelInfo(m.Slug)
		if m.DisplayName != "" {
			info.Name = m.DisplayName
			info.DisplayName = m.DisplayName
		}
		if m.ContextWindow > 0 {
			info.InputTokenLimit = m.ContextWindow
		}
		if m.Description != "" {
			info.Description = m.Description
		}
		if len(m.SupportedReasoningLevels) > 0 {
			efforts := make([]string, 0, len(m.SupportedReasoningLevels))
			for _, level := range m.SupportedReasoningLevels {
				efforts = append(efforts, level.Effort)
			}
			if capability := llm.NewReasoningCapability(efforts, m.DefaultReasoningLevel); capability != nil {
				info.Reasoning = capability
			}
		}
		models = append(models, info)
	}
	slices.SortFunc(models, func(a, b llm.ModelInfo) int { return cmp.Compare(a.ID, b.ID) })
	return models
}

// subscriptionAuthenticator adapts the ChatGPT OAuth flow to llm.Authenticator
// so the app layer can trigger sign-in/out through the llm facade rather than
// importing this provider package directly.
type subscriptionAuthenticator struct{}

func (subscriptionAuthenticator) Login(ctx context.Context, onPrompt func(llm.LoginPrompt)) error {
	// The PKCE flow needs no user code — opening the authorize URL is the whole
	// instruction, so the prompt carries just the URL.
	_, err := oauth.Login(ctx, func(u string) {
		if onPrompt != nil {
			onPrompt(llm.LoginPrompt{URL: u})
		}
	})
	return err
}

func (subscriptionAuthenticator) Logout() error { return oauth.Logout() }

func (subscriptionAuthenticator) HasCredentials() bool { return oauth.HasCredentials() }

func init() {
	llm.Register(SubscriptionMeta, NewSubscriptionClient)
	llm.RegisterAuthenticator(llm.OpenAI, llm.AuthSubscription, subscriptionAuthenticator{})
}
