package oauth

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/secret"
)

// stubTransport answers requests with canned JSON, walking `bodies` in order
// and repeating the last one once the script runs out — enough to model both a
// single reply and a poll whose answer changes. It records the last request so
// tests can assert on the headers we send.
type stubTransport struct {
	status int // 0 means 200
	bodies []string
	calls  int
	last   *http.Request
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.last = req
	body := s.bodies[min(s.calls, len(s.bodies)-1)]
	s.calls++
	status := cmp.Or(s.status, http.StatusOK)
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

// serveStub points auth round-trips at the given transport for one test.
func serveStub(t *testing.T, tr http.RoundTripper) {
	t.Helper()
	previous := httpClient
	httpClient = func() *http.Client { return &http.Client{Transport: tr} }
	t.Cleanup(func() { httpClient = previous })
}

// isolateSecrets gives a test its own secret store.
func isolateSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	secret.ResetDefault()
	t.Cleanup(secret.ResetDefault)
}

func TestTokenSourceNotSignedInIsCredentialError(t *testing.T) {
	isolateSecrets(t)

	_, _, err := NewTokenSource().Token(context.Background())
	var credErr *CredentialError
	if !errors.As(err, &credErr) {
		t.Fatalf("Token() with no stored credentials = %T (%v), want *CredentialError", err, err)
	}
}

func TestTokenSourceReusesFreshBearer(t *testing.T) {
	isolateSecrets(t)
	// Any exchange attempt would fail against this transport, so reaching the
	// cached bearer is the only way this test can pass.
	serveStub(t, &stubTransport{status: http.StatusInternalServerError, bodies: []string{`{}`}})

	if err := save(Tokens{
		GitHubToken:  "gho_test",
		CopilotToken: "cached-bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	bearer, endpoint, err := NewTokenSource().Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if bearer != "cached-bearer" {
		t.Errorf("bearer = %q, want the cached one", bearer)
	}
	if endpoint != DefaultAPIEndpoint {
		t.Errorf("endpoint = %q, want %q", endpoint, DefaultAPIEndpoint)
	}
}

func TestTokenSourceMintsAndPersistsStaleBearer(t *testing.T) {
	isolateSecrets(t)
	expiry := time.Now().Add(time.Hour).Unix()
	tr := &stubTransport{bodies: []string{
		`{"token":"fresh-bearer","expires_at":` + strconv.FormatInt(expiry, 10) +
			`,"endpoints":{"api":"https://api.enterprise.githubcopilot.com"}}`,
	}}
	serveStub(t, tr)

	// Expired bearer: the source must exchange the GitHub token for a new one.
	if err := save(Tokens{
		GitHubToken:  "gho_test",
		CopilotToken: "expired-bearer",
		ExpiresAt:    time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	bearer, endpoint, err := NewTokenSource().Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if bearer != "fresh-bearer" {
		t.Errorf("bearer = %q, want fresh-bearer", bearer)
	}
	if endpoint != "https://api.enterprise.githubcopilot.com" {
		t.Errorf("endpoint = %q, want the one the exchange reported", endpoint)
	}
	if got := tr.last.Header.Get("Authorization"); got != "token gho_test" {
		t.Errorf("exchange Authorization = %q, want the GitHub token", got)
	}
	if got := tr.last.Header.Get("Editor-Version"); got != EditorVersion {
		t.Errorf("exchange Editor-Version = %q, want %q", got, EditorVersion)
	}

	// The mint is persisted, so the next process doesn't have to repeat it.
	stored, ok := load()
	if !ok || stored.CopilotToken != "fresh-bearer" {
		t.Errorf("stored bearer = %+v, want the freshly minted one", stored)
	}
}

func TestTokenSourceKeepsValidBearerWhenMintFails(t *testing.T) {
	isolateSecrets(t)
	serveStub(t, &stubTransport{status: http.StatusInternalServerError, bodies: []string{`{}`}})

	// Inside the refresh window but not yet expired: a failed mint must not
	// break a turn that the current bearer can still serve.
	if err := save(Tokens{
		GitHubToken:  "gho_test",
		CopilotToken: "still-valid",
		ExpiresAt:    time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	bearer, _, err := NewTokenSource().Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if bearer != "still-valid" {
		t.Errorf("bearer = %q, want the still-valid cached one", bearer)
	}
}

func TestMintBearerReportsMissingSubscription(t *testing.T) {
	serveStub(t, &stubTransport{status: http.StatusForbidden, bodies: []string{`{}`}})

	_, err := MintBearer(context.Background(), Tokens{GitHubToken: "gho_test"})
	if err == nil || !strings.Contains(err.Error(), "no active Copilot subscription") {
		t.Fatalf("MintBearer on 403 = %v, want a missing-subscription error", err)
	}
}

func TestMintBearerReportsRevokedToken(t *testing.T) {
	serveStub(t, &stubTransport{status: http.StatusUnauthorized, bodies: []string{`{}`}})

	_, err := MintBearer(context.Background(), Tokens{GitHubToken: "gho_test"})
	if err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("MintBearer on 401 = %v, want a sign-in-again error", err)
	}
}

func TestHasCredentialsTracksStoredGitHubToken(t *testing.T) {
	isolateSecrets(t)

	if HasCredentials() {
		t.Fatal("expected no credentials before sign-in")
	}
	if err := save(Tokens{GitHubToken: "gho_test"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !HasCredentials() {
		t.Error("expected credentials once a GitHub token is stored")
	}
	if err := Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if HasCredentials() {
		t.Error("expected no credentials after logout")
	}
}
