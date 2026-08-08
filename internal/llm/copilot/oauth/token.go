// Package oauth implements GitHub Copilot sign-in: the GitHub OAuth 2.0 device
// flow that yields a long-lived GitHub token, plus the exchange of that token
// for the short-lived bearer the Copilot API requires, and the storage and
// refresh around both.
//
// It lets a user with a GitHub Copilot subscription drive san without a metered
// API key, mirroring how the official editor plugins authenticate. The package
// depends only on the stdlib and leaf infrastructure (internal/secret,
// internal/proc) — never on internal/llm — so a headless `san login` command
// could reuse it later.
package oauth

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/genai-io/san/internal/secret"
)

const (
	// ClientID is the GitHub Copilot editor plugin's public OAuth client id. We
	// reuse it on purpose: copilot_internal/v2/token only mints API bearers for
	// GitHub tokens issued to a Copilot-enabled OAuth client, so a client id of
	// our own would be rejected there.
	ClientID = "Iv1.b507a08c87ecfe98"

	// scope is all the device flow needs — the Copilot entitlement rides on the
	// account, not on an extra scope.
	scope = "read:user"

	githubBaseURL   = "https://github.com"
	deviceCodeURL   = githubBaseURL + "/login/device/code"
	accessTokenURL  = githubBaseURL + "/login/oauth/access_token"
	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

	// DefaultAPIEndpoint is the Copilot API root for individual accounts.
	// Business/enterprise accounts are served from their own host, which the
	// token exchange reports back.
	DefaultAPIEndpoint = "https://api.githubcopilot.com"

	// StoreKey is the secret-store key under which the credential blob is persisted.
	StoreKey = "GITHUB_COPILOT_AUTH"

	// refreshWindow is how long before expiry we proactively mint a new Copilot
	// bearer. The bearer lives around 30 minutes, so this still uses most of it.
	refreshWindow = 5 * time.Minute

	// httpTimeout bounds each auth round-trip so a hung endpoint can't wedge a
	// sign-in or an in-flight turn's token refresh.
	httpTimeout = 30 * time.Second
)

// Editor identity sent on every GitHub and Copilot API call. The backend only
// serves clients it recognises as a Copilot chat plugin, so these mirror what
// GitHub's own VS Code extension sends. Exported because the provider client
// puts the same values on its chat requests.
const (
	copilotChatVersion = "0.26.7"

	EditorVersion       = "vscode/1.99.3"
	EditorPluginVersion = "copilot-chat/" + copilotChatVersion
	UserAgent           = "GitHubCopilotChat/" + copilotChatVersion
	APIVersion          = "2025-04-01"
	IntegrationID       = "vscode-chat"
)

// Tokens is the persisted credential blob for Copilot subscription auth. The
// GitHub token is the durable credential; the Copilot bearer and its endpoint
// are a cache of the last exchange, re-minted whenever they go stale.
type Tokens struct {
	GitHubToken  string    `json:"github_token"`
	CopilotToken string    `json:"copilot_token"`
	APIEndpoint  string    `json:"api_endpoint"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (t Tokens) usable() bool { return t.GitHubToken != "" }

// bearerStale reports whether the cached Copilot bearer is missing or within
// refreshWindow of expiry.
func (t Tokens) bearerStale() bool {
	return t.CopilotToken == "" || time.Now().After(t.ExpiresAt.Add(-refreshWindow))
}

// endpoint returns the Copilot API root to call, defaulting to the individual
// one when the exchange didn't report a specific host.
func (t Tokens) endpoint() string {
	return cmp.Or(t.APIEndpoint, DefaultAPIEndpoint)
}

// load reads the stored credential blob. The bool is false when no usable blob exists.
func load() (Tokens, bool) {
	s := secret.Default()
	if s == nil {
		return Tokens{}, false
	}
	raw := s.Get(StoreKey)
	if raw == "" {
		return Tokens{}, false
	}
	var t Tokens
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return Tokens{}, false
	}
	return t, t.usable()
}

// save persists the credential blob to the secret store (0600, via secret.Store).
func save(t Tokens) error {
	s := secret.Default()
	if s == nil {
		return errors.New("secret store unavailable")
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.Set(StoreKey, string(raw))
}

// Logout clears the stored Copilot subscription credentials.
func Logout() error {
	s := secret.Default()
	if s == nil {
		return nil
	}
	return s.Delete(StoreKey)
}

// HasCredentials reports whether a usable credential blob is stored.
func HasCredentials() bool {
	_, ok := load()
	return ok
}

// CredentialError marks a failure to produce a valid Copilot bearer — not
// signed in, or a GitHub token that no longer buys Copilot access (revoked, or
// a lapsed subscription). It signals the connection isn't usable, so callers
// should surface it rather than fall back to a degraded path.
type CredentialError struct{ Err error }

func (e *CredentialError) Error() string { return e.Err.Error() }
func (e *CredentialError) Unwrap() error { return e.Err }

// notEntitledError marks a mint the account itself can never satisfy — a
// revoked GitHub token, or a plan without Copilot — as opposed to a transient
// exchange failure. Sign-in uses it to decide whether the credential is worth
// keeping for a retry.
type notEntitledError struct{ Err error }

func (e *notEntitledError) Error() string { return e.Err.Error() }
func (e *notEntitledError) Unwrap() error { return e.Err }

// TokenSource returns a valid Copilot bearer and the API endpoint to send it
// to, re-minting the bearer when it nears expiry. It is safe for concurrent
// use; mints are serialized so a burst of requests triggers at most one.
type TokenSource struct {
	mu sync.Mutex
}

// NewTokenSource creates a TokenSource backed by the persisted credentials.
func NewTokenSource() *TokenSource { return &TokenSource{} }

// Token returns a currently-valid Copilot bearer and the API root it is scoped
// to. Failures to obtain a credential are returned as *CredentialError.
func (ts *TokenSource) Token(ctx context.Context) (bearer, apiEndpoint string, err error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	t, ok := load()
	if !ok {
		return "", "", &CredentialError{errors.New("not signed in to GitHub Copilot — connect the Copilot Subscription provider first")}
	}
	if !t.bearerStale() {
		return t.CopilotToken, t.endpoint(), nil
	}

	minted, err := MintBearer(ctx, t)
	if err != nil {
		// A transient mint failure shouldn't break an in-flight turn while the
		// cached bearer is still technically valid; only surface it once expired.
		if t.CopilotToken != "" && time.Now().Before(t.ExpiresAt) {
			return t.CopilotToken, t.endpoint(), nil
		}
		return "", "", &CredentialError{fmt.Errorf("Copilot token refresh failed: %w", err)}
	}
	// Persisting is only a warm start for the next process — the bearer in hand
	// is valid either way, so an unwritable store must not fail a live turn.
	_ = save(minted)
	return minted.CopilotToken, minted.endpoint(), nil
}

// copilotTokenResponse is the reply from copilot_internal/v2/token. The bearer
// is short-lived; endpoints.api names the host it is valid against.
type copilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

// MintBearer exchanges the GitHub token for a fresh Copilot bearer, returning
// the credential blob updated with it. It does not persist — the caller decides
// whether the result is worth storing (sign-in verifies entitlement with it
// before saving anything).
func MintBearer(ctx context.Context, t Tokens) (Tokens, error) {
	if t.GitHubToken == "" {
		return Tokens{}, errors.New("no GitHub token available; sign in again")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenURL, nil)
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+t.GitHubToken)
	req.Header.Set("Editor-Version", EditorVersion)
	req.Header.Set("Editor-Plugin-Version", EditorPluginVersion)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("X-GitHub-Api-Version", APIVersion)

	resp, err := httpClient().Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return Tokens{}, &notEntitledError{errors.New("GitHub rejected the stored token — sign in again")}
	case resp.StatusCode == http.StatusForbidden:
		return Tokens{}, &notEntitledError{errors.New("this GitHub account has no active Copilot subscription")}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return Tokens{}, fmt.Errorf("copilot token endpoint returned %s", resp.Status)
	}

	var ctr copilotTokenResponse
	if err := json.Unmarshal(body, &ctr); err != nil {
		return Tokens{}, fmt.Errorf("decode copilot token response: %w", err)
	}
	if ctr.Token == "" {
		return Tokens{}, errors.New("copilot token endpoint returned no token")
	}

	updated := t
	updated.CopilotToken = ctr.Token
	// Keep the endpoint we already know when a reply omits it: dropping back to
	// the individual host would send an enterprise bearer to the wrong API for
	// the rest of the session, and across restarts.
	updated.APIEndpoint = cmp.Or(ctr.Endpoints.API, t.APIEndpoint)
	updated.ExpiresAt = bearerExpiry(ctr.ExpiresAt)
	return updated, nil
}

// bearerExpiry converts the reported unix expiry, falling back to a
// conservative window when the endpoint omits it.
func bearerExpiry(expiresAt int64) time.Time {
	if expiresAt > 0 {
		return time.Unix(expiresAt, 0)
	}
	return time.Now().Add(20 * time.Minute)
}

// httpClient returns the client used for auth round-trips. It is a variable so
// tests can answer the GitHub endpoints from a stub transport.
var httpClient = func() *http.Client { return &http.Client{Timeout: httpTimeout} }
