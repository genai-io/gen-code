// Package oauth implements the OpenAI ChatGPT subscription sign-in flow: an
// OAuth 2.0 authorization-code grant with PKCE against auth.openai.com, plus
// the token storage and refresh needed to call the ChatGPT Codex backend.
//
// It lets a user with a ChatGPT Plus/Pro/Business plan drive san's OpenAI
// provider without a metered API key, mirroring how OpenAI's own Codex CLI
// authenticates. The package depends only on the stdlib and leaf infrastructure
// (internal/secret, internal/proc) — never on internal/llm — so a headless
// `san login` command could reuse it later.
package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/genai-io/san/internal/secret"
)

const (
	// ClientID is OpenAI Codex's public OAuth client id. We reuse it on purpose:
	// the ChatGPT Codex endpoint only accepts access tokens minted for this
	// client, so a bespoke client_id would be rejected with 401.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	issuer       = "https://auth.openai.com"
	authorizeURL = issuer + "/oauth/authorize"
	tokenURL     = issuer + "/oauth/token"
	scope        = "openid profile email offline_access"

	// StoreKey is the secret-store key under which the token blob is persisted.
	StoreKey = "OPENAI_CHATGPT_AUTH"

	// refreshWindow is how long before expiry we proactively refresh the token.
	refreshWindow = 5 * time.Minute
)

// Tokens is the persisted credential blob for ChatGPT subscription auth.
type Tokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token"`
	AccountID    string    `json:"account_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (t Tokens) usable() bool { return t.AccessToken != "" && t.AccountID != "" }

// stale reports whether the token is within refreshWindow of expiry.
func (t Tokens) stale() bool { return time.Now().After(t.ExpiresAt.Add(-refreshWindow)) }

// load reads the stored token blob. The bool is false when no usable blob exists.
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

// save persists the token blob to the secret store (0600, via secret.Store).
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

// Logout clears the stored ChatGPT subscription credentials.
func Logout() error {
	s := secret.Default()
	if s == nil {
		return nil
	}
	return s.Delete(StoreKey)
}

// HasCredentials reports whether a usable token blob is stored.
func HasCredentials() bool {
	_, ok := load()
	return ok
}

// CredentialError marks a failure to produce a valid subscription access token
// — not signed in, or a stored token that can't be refreshed (revoked/expired
// refresh token). It signals the connection isn't usable, so callers should
// surface it rather than fall back to a degraded/offline path.
type CredentialError struct{ Err error }

func (e *CredentialError) Error() string { return e.Err.Error() }
func (e *CredentialError) Unwrap() error { return e.Err }

// TokenSource returns a valid ChatGPT access token and account id, refreshing
// the token when it is near expiry. It is safe for concurrent use; refreshes
// are serialized so a burst of requests triggers at most one refresh.
type TokenSource struct {
	mu sync.Mutex
}

// NewTokenSource creates a TokenSource backed by the persisted credentials.
func NewTokenSource() *TokenSource { return &TokenSource{} }

// Token returns a currently-valid access token and the ChatGPT account id.
// It refreshes transparently when the stored token is stale. Failures to obtain
// a credential are returned as *CredentialError.
func (ts *TokenSource) Token(ctx context.Context) (accessToken, accountID string, err error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	t, ok := load()
	if !ok {
		return "", "", &CredentialError{errors.New("not signed in to ChatGPT — connect the ChatGPT Subscription provider first")}
	}
	if !t.stale() {
		return t.AccessToken, t.AccountID, nil
	}

	refreshed, err := refresh(ctx, t)
	if err != nil {
		// A transient refresh failure shouldn't break an in-flight turn while the
		// current token is still technically valid; only surface it once expired.
		if time.Now().Before(t.ExpiresAt) {
			return t.AccessToken, t.AccountID, nil
		}
		return "", "", &CredentialError{fmt.Errorf("ChatGPT token refresh failed: %w", err)}
	}
	return refreshed.AccessToken, refreshed.AccountID, nil
}

// tokenResponse is the JSON returned by the OAuth token endpoint for both the
// authorization-code exchange and the refresh grant.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// refresh exchanges the refresh token for a fresh access token and persists it.
func refresh(ctx context.Context, t Tokens) (Tokens, error) {
	if t.RefreshToken == "" {
		return Tokens{}, errors.New("no refresh token available; sign in again")
	}
	resp, err := postForm(ctx, tokenURL, url.Values{
		"client_id":     {ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"scope":         {scope},
	})
	if err != nil {
		return Tokens{}, err
	}

	updated := t
	updated.AccessToken = resp.AccessToken
	if resp.RefreshToken != "" {
		updated.RefreshToken = resp.RefreshToken
	}
	if resp.IDToken != "" {
		updated.IDToken = resp.IDToken
	}
	updated.ExpiresAt = expiresAt(resp.AccessToken, resp.ExpiresIn)
	if id := accountID(updated.IDToken, updated.AccessToken); id != "" {
		updated.AccountID = id
	}
	if err := save(updated); err != nil {
		return Tokens{}, err
	}
	return updated, nil
}

// postForm POSTs a urlencoded form to an OAuth endpoint and decodes the token
// response, turning non-2xx replies into an error carrying the server message.
func postForm(ctx context.Context, endpoint string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}
	return tr, nil
}

// expiresAt derives the token expiry, preferring the access token's own `exp`
// JWT claim and falling back to the `expires_in` hint, then to a safe default.
func expiresAt(accessToken string, expiresIn int) time.Time {
	if exp, ok := jwtExpiry(accessToken); ok {
		return exp
	}
	if expiresIn > 0 {
		return time.Now().Add(time.Duration(expiresIn) * time.Second)
	}
	return time.Now().Add(time.Hour)
}

// jwtPayload decodes the (unverified) claims from a JWT's payload segment. The
// tokens come straight from the OAuth endpoint over TLS, so we read the claims
// without signature verification — we only need the account id and expiry.
func jwtPayload(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, errors.New("malformed jwt")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// jwtExpiry reads the standard `exp` claim as a time.
func jwtExpiry(token string) (time.Time, bool) {
	claims, err := jwtPayload(token)
	if err != nil {
		return time.Time{}, false
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0), true
}

// accountID extracts the ChatGPT account id from the first token that carries
// it. OpenAI nests the value under the "https://api.openai.com/auth" claim as
// "chatgpt_account_id"; the id token holds it, and the access token usually
// mirrors it.
func accountID(tokens ...string) string {
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		claims, err := jwtPayload(tok)
		if err != nil {
			continue
		}
		auth, ok := claims["https://api.openai.com/auth"].(map[string]any)
		if !ok {
			continue
		}
		if id, ok := auth["chatgpt_account_id"].(string); ok && id != "" {
			return id
		}
	}
	return ""
}
