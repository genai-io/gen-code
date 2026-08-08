package copilot

import (
	"context"
	"net/http"
	"net/url"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/llm/copilot/oauth"
)

// SubscriptionMeta is the metadata for GitHub Copilot via a paid Copilot plan.
// It has no EnvVars because it authenticates with a GitHub device-flow login,
// not a key.
var SubscriptionMeta = llm.Meta{
	Provider:    llm.Copilot,
	AuthMethod:  llm.AuthSubscription,
	EnvVars:     nil,
	DisplayName: "Copilot Subscription",
}

// NewSubscriptionClient creates a client that talks to the Copilot chat backend
// with a subscription bearer instead of an API key. The bearer is injected per
// request from a re-minting TokenSource, so a long session survives the ~30
// minute token lifetime transparently.
func NewSubscriptionClient(ctx context.Context) (llm.Provider, error) {
	tokens := oauth.NewTokenSource()

	sdk := openai.NewClient(
		// The trailing slash matters: the SDK resolves "chat/completions"
		// against this root, and without it the last path segment is dropped.
		option.WithBaseURL(oauth.DefaultAPIEndpoint+"/"),
		option.WithMaxRetries(0),
		option.WithHeader("Copilot-Integration-Id", oauth.IntegrationID),
		option.WithHeader("Editor-Version", oauth.EditorVersion),
		option.WithHeader("Editor-Plugin-Version", oauth.EditorPluginVersion),
		option.WithHeader("User-Agent", oauth.UserAgent),
		option.WithHeader("Openai-Intent", "conversation-panel"),
		option.WithHeader("X-GitHub-Api-Version", oauth.APIVersion),
		option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			bearer, endpoint, err := tokens.Token(req.Context())
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+bearer)
			req.Header.Set("X-Request-Id", llm.NewRequestID())
			retarget(req, endpoint)
			return next(req)
		}),
	)

	return NewClient(sdk, "copilot:subscription"), nil
}

// retarget points the request at the API root the token exchange reported.
// Individual accounts use the default host, but business and enterprise plans
// are served from their own — and which one applies is only known after the
// first token exchange, too late for the base URL the client was built with.
func retarget(req *http.Request, endpoint string) {
	if endpoint == "" || endpoint == oauth.DefaultAPIEndpoint {
		return
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return
	}
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	req.Host = u.Host
}

// subscriptionAuthenticator adapts the GitHub device flow to llm.Authenticator
// so the app layer can trigger sign-in/out through the llm facade rather than
// importing this provider package directly.
type subscriptionAuthenticator struct{}

func (subscriptionAuthenticator) Login(ctx context.Context, onPrompt func(llm.LoginPrompt)) error {
	return oauth.Login(ctx, func(c oauth.UserCode) {
		if onPrompt != nil {
			onPrompt(llm.LoginPrompt{URL: c.VerificationURI, UserCode: c.Code})
		}
	})
}

func (subscriptionAuthenticator) Logout() error { return oauth.Logout() }

func (subscriptionAuthenticator) HasCredentials() bool { return oauth.HasCredentials() }

func init() {
	llm.RegisterProviderDisplay(llm.Copilot, llm.ProviderDisplay{Name: "GitHub Copilot", Order: 25})
	llm.Register(SubscriptionMeta, NewSubscriptionClient)
	llm.RegisterAuthenticator(llm.Copilot, llm.AuthSubscription, subscriptionAuthenticator{})
}
