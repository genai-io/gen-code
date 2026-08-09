package oauth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPollAccessTokenWaitsOutAuthorizationPending(t *testing.T) {
	tr := &stubTransport{bodies: []string{
		`{"error":"authorization_pending"}`,
		`{"error":"authorization_pending"}`,
		`{"access_token":"gho_granted"}`,
	}}
	serveStub(t, tr)

	token, err := pollAccessToken(context.Background(), deviceCodeResponse{DeviceCode: "dev"}, time.Millisecond)
	if err != nil {
		t.Fatalf("pollAccessToken: %v", err)
	}
	if token != "gho_granted" {
		t.Errorf("token = %q, want gho_granted", token)
	}
	if tr.calls != 3 {
		t.Errorf("polled %d times, want 3 (two pending, then granted)", tr.calls)
	}
}

func TestPollAccessTokenStopsOnTerminalErrors(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantInErr string
	}{
		{"expired code", `{"error":"expired_token"}`, "expired"},
		{"user declined", `{"error":"access_denied"}`, "denied"},
		{"unknown failure", `{"error":"unsupported_grant_type","error_description":"bad grant"}`, "bad grant"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			serveStub(t, &stubTransport{bodies: []string{tc.body}})

			_, err := pollAccessToken(context.Background(), deviceCodeResponse{DeviceCode: "dev"}, time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("pollAccessToken = %v, want an error mentioning %q", err, tc.wantInErr)
			}
		})
	}
}

func TestSlowDownWidensPollInterval(t *testing.T) {
	if got := slowDownBackoff(5 * time.Second); got != 10*time.Second {
		t.Errorf("slowDownBackoff(5s) = %v, want 10s", got)
	}
}

func TestPollAccessTokenReportsWhyItGaveUp(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantInErr string
	}{
		{
			name:      "still waiting on the browser",
			body:      `{"error":"authorization_pending"}`,
			wantInErr: "context deadline exceeded",
		},
		{
			// An org policy that bars the device flow rejects every poll; the
			// timeout alone would tell the user nothing about why.
			name:      "endpoint rejects every poll",
			status:    http.StatusForbidden,
			body:      `{}`,
			wantInErr: "403",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			serveStub(t, &stubTransport{status: tc.status, bodies: []string{tc.body}})

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()

			_, err := pollAccessToken(ctx, deviceCodeResponse{DeviceCode: "dev"}, 5*time.Millisecond)
			if err == nil {
				t.Fatal("expected pollAccessToken to give up")
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantInErr)
			}
		})
	}
}

func TestRequestDeviceCodeDefaultsVerificationURI(t *testing.T) {
	serveStub(t, &stubTransport{bodies: []string{
		`{"device_code":"dev","user_code":"ABCD-1234","interval":5}`,
	}})

	device, err := requestDeviceCode(context.Background())
	if err != nil {
		t.Fatalf("requestDeviceCode: %v", err)
	}
	if device.UserCode != "ABCD-1234" {
		t.Errorf("user code = %q, want ABCD-1234", device.UserCode)
	}
	if device.VerificationURI != githubBaseURL+"/login/device" {
		t.Errorf("verification URI = %q, want the default device page", device.VerificationURI)
	}
}

func TestPollIntervalHasFloor(t *testing.T) {
	if got := pollInterval(deviceCodeResponse{Interval: 0}); got != 5*time.Second {
		t.Errorf("pollInterval with no interval = %v, want the 5s floor", got)
	}
	if got := pollInterval(deviceCodeResponse{Interval: 12}); got != 12*time.Second {
		t.Errorf("pollInterval = %v, want GitHub's 12s", got)
	}
}

func TestPersistSignInKeepsTokenWhenTheExchangeBlips(t *testing.T) {
	isolateSecrets(t)
	serveStub(t, &stubTransport{status: http.StatusBadGateway, bodies: []string{`{}`}})

	// The browser authorization already succeeded, so the GitHub token is good;
	// only the bearer exchange failed. Throwing it away would cost the user
	// another full device flow.
	err := persistSignIn(context.Background(), "gho_authorized")
	if err == nil {
		t.Fatal("expected persistSignIn to report the failed exchange")
	}
	stored, ok := load()
	if !ok || stored.GitHubToken != "gho_authorized" {
		t.Errorf("stored credentials = %+v, want the authorized GitHub token kept", stored)
	}
}

func TestPersistSignInDiscardsCredentialWithoutEntitlement(t *testing.T) {
	isolateSecrets(t)
	serveStub(t, &stubTransport{status: http.StatusForbidden, bodies: []string{`{}`}})

	// No Copilot subscription is the account's own verdict — retrying with the
	// same token can never work, so nothing should be left behind.
	if err := persistSignIn(context.Background(), "gho_authorized"); err == nil {
		t.Fatal("expected persistSignIn to fail without a subscription")
	}
	if HasCredentials() {
		t.Error("expected no stored credentials when the account has no Copilot access")
	}
}
