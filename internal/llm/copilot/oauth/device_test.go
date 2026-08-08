package oauth

import (
	"context"
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

func TestPollAccessTokenGivesUpWhenCancelled(t *testing.T) {
	serveStub(t, &stubTransport{bodies: []string{`{"error":"authorization_pending"}`}})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := pollAccessToken(ctx, deviceCodeResponse{DeviceCode: "dev"}, 5*time.Millisecond); err == nil {
		t.Fatal("expected pollAccessToken to stop when the context is cancelled")
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
