package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/genai-io/san/internal/proc"
)

const (
	callbackPath = "/auth/callback"

	// primaryPort / fallbackPort are the only localhost ports Codex's OAuth
	// client registers as redirect targets; since we reuse its client id we must
	// bind one of them for the callback to be accepted.
	primaryPort  = 1455
	fallbackPort = 1457

	// Originator identifies the client to OpenAI's OAuth + Codex backend. Codex
	// uses this value; matching it keeps the authorize page and edge (Cloudflare)
	// checks happy. Exported so the Codex transport headers share one source.
	Originator = "codex_cli_rs"

	// loginTimeout bounds how long we wait for the user to complete the browser
	// sign-in before giving up.
	loginTimeout = 5 * time.Minute
)

// Account describes a signed-in ChatGPT subscription account.
type Account struct {
	AccountID string
}

// Login runs the interactive ChatGPT OAuth (PKCE) sign-in. It binds a localhost
// callback, opens the user's browser to OpenAI's authorize page, captures the
// authorization code, exchanges it for tokens, and persists them. It blocks
// until the flow completes, the context is cancelled, or it times out.
//
// onURL, if non-nil, is invoked with the authorize URL right before the browser
// is opened so callers can surface it (useful when auto-open fails, e.g. over
// SSH).
func Login(ctx context.Context, onURL func(string)) (Account, error) {
	verifier, challenge, err := newPKCE()
	if err != nil {
		return Account{}, err
	}
	state, err := randomString(32)
	if err != nil {
		return Account{}, err
	}

	ln, port, err := listen()
	if err != nil {
		return Account{}, err
	}
	defer ln.Close()

	redirectURI := fmt.Sprintf("http://localhost:%d%s", port, callbackPath)

	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("error") != "":
			writePage(w, "Sign-in failed", q.Get("error_description"))
			resultCh <- result{err: fmt.Errorf("authorization error: %s", q.Get("error"))}
		case q.Get("state") != state:
			writePage(w, "Sign-in failed", "The sign-in response could not be verified (state mismatch).")
			resultCh <- result{err: errors.New("oauth state mismatch")}
		case q.Get("code") == "":
			writePage(w, "Sign-in failed", "No authorization code was returned.")
			resultCh <- result{err: errors.New("missing authorization code")}
		default:
			writePage(w, "Signed in to ChatGPT", "You can close this tab and return to san.")
			resultCh <- result{code: q.Get("code")}
		}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck // Serve always returns a non-nil error on close.
	defer srv.Close()

	authURL := authorizeEndpoint(redirectURI, challenge, state)
	if onURL != nil {
		onURL(authURL)
	}
	_ = proc.OpenURL(authURL) // best-effort; onURL lets the caller show it if this fails.

	select {
	case <-ctx.Done():
		return Account{}, fmt.Errorf("sign-in did not complete: %w", ctx.Err())
	case res := <-resultCh:
		if res.err != nil {
			return Account{}, res.err
		}
		return exchange(ctx, res.code, redirectURI, verifier)
	}
}

// exchange trades the authorization code for tokens and persists them.
func exchange(ctx context.Context, code, redirectURI, verifier string) (Account, error) {
	resp, err := postForm(ctx, tokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {ClientID},
		"code_verifier": {verifier},
	})
	if err != nil {
		return Account{}, err
	}

	id := accountID(resp.IDToken, resp.AccessToken)
	if id == "" {
		return Account{}, errors.New("signed in, but no ChatGPT account id was found in the token — this plan may not include Codex access")
	}

	t := Tokens{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		IDToken:      resp.IDToken,
		AccountID:    id,
		ExpiresAt:    expiresAt(resp.AccessToken, resp.ExpiresIn),
	}
	if err := save(t); err != nil {
		return Account{}, fmt.Errorf("save credentials: %w", err)
	}
	return Account{AccountID: id}, nil
}

// listen binds the localhost callback on the primary port, falling back to the
// secondary one if it is busy.
func listen() (net.Listener, int, error) {
	for _, port := range []int{primaryPort, fallbackPort} {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, fmt.Errorf("could not bind the sign-in callback on port %d or %d (already in use?)", primaryPort, fallbackPort)
}

// authorizeEndpoint builds the OpenAI authorize URL for the PKCE flow.
func authorizeEndpoint(redirectURI, challenge, state string) string {
	q := url.Values{
		"response_type":              {"code"},
		"client_id":                  {ClientID},
		"redirect_uri":               {redirectURI},
		"scope":                      {scope},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
		"originator":                 {Originator},
	}
	return authorizeURL + "?" + q.Encode()
}

// newPKCE returns a PKCE verifier and its S256 challenge.
func newPKCE() (verifier, challenge string, err error) {
	verifier, err = randomString(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// randomString returns n random bytes encoded as an unpadded base64url string.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// writePage renders a minimal HTML result page for the browser callback.
func writePage(w http.ResponseWriter, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>%s</title></head>`+
		`<body style="font-family:system-ui,sans-serif;text-align:center;padding-top:4rem">`+
		`<h2>%s</h2><p>%s</p></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
}
