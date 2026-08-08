package oauth

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/genai-io/san/internal/proc"
)

// loginTimeout bounds how long we wait for the user to authorize the device
// before giving up. GitHub's own device codes expire in 15 minutes.
const loginTimeout = 15 * time.Minute

// UserCode is the device-flow instruction for the user: the page to open and
// the code to type there. GitHub does not offer a pre-filled verification URL,
// so the code has to reach the user through the UI — it exists nowhere else.
type UserCode struct {
	VerificationURI string
	Code            string
}

// Login runs the GitHub device-flow sign-in for Copilot. It requests a device
// code, hands the user code to onCode, opens the verification page, polls until
// the user authorizes, then verifies the account actually has Copilot access
// before persisting anything. It blocks until the flow completes, the context
// is cancelled, or it times out.
func Login(ctx context.Context, onCode func(UserCode)) error {
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	device, err := requestDeviceCode(ctx)
	if err != nil {
		return err
	}
	if onCode != nil {
		onCode(UserCode{VerificationURI: device.VerificationURI, Code: device.UserCode})
	}
	_ = proc.OpenURL(device.VerificationURI) // best-effort; onCode lets the caller show it if this fails.

	githubToken, err := pollAccessToken(ctx, device, pollInterval(device))
	if err != nil {
		return err
	}

	return persistSignIn(ctx, githubToken)
}

// persistSignIn stores what the device flow just earned. It mints a bearer
// first, because a GitHub account without an active Copilot subscription
// authorizes the device fine and would otherwise only fail at the first
// request — minting here turns that into a clear sign-in error and warms the
// token cache in the same round-trip.
//
// Only the account's own verdict (revoked token, no subscription) is worth
// discarding the credential over. A transient exchange failure still keeps the
// GitHub token, so a retry can re-mint instead of sending the user back through
// a fresh 15-minute device flow.
func persistSignIn(ctx context.Context, githubToken string) error {
	tokens, mintErr := MintBearer(ctx, Tokens{GitHubToken: githubToken})
	if mintErr != nil {
		var notEntitled *NotEntitledError
		if errors.As(mintErr, &notEntitled) {
			return mintErr
		}
		tokens = Tokens{GitHubToken: githubToken}
	}
	if err := save(tokens); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return mintErr
}

// deviceCodeResponse is GitHub's reply to the device-code request. The reply
// also carries expires_in, which loginTimeout already covers.
type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
}

// requestDeviceCode starts the device flow.
func requestDeviceCode(ctx context.Context) (deviceCodeResponse, error) {
	var out deviceCodeResponse
	err := postJSON(ctx, deviceCodeURL, map[string]string{
		"client_id": ClientID,
		"scope":     scope,
	}, &out)
	if err != nil {
		return deviceCodeResponse{}, fmt.Errorf("request device code: %w", err)
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return deviceCodeResponse{}, errors.New("GitHub returned no device code")
	}
	if out.VerificationURI == "" {
		out.VerificationURI = githubBaseURL + "/login/device"
	}
	return out, nil
}

// accessTokenResponse is GitHub's reply while polling. A pending or throttled
// poll comes back 200 with an `error` code rather than an HTTP error status.
type accessTokenResponse struct {
	AccessToken      string `json:"access_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// pollInterval is how long to wait between polls: what GitHub asks for, with a
// floor so a missing or over-eager interval can't get us rate-limited.
func pollInterval(device deviceCodeResponse) time.Duration {
	return time.Duration(max(device.Interval, 5)) * time.Second
}

// slowDownBackoff widens the poll interval after GitHub rate-limits us. The
// device-flow spec asks for five extra seconds each time.
func slowDownBackoff(interval time.Duration) time.Duration { return interval + 5*time.Second }

// pollAccessToken polls until the user authorizes the device, honouring the
// interval GitHub asks for and backing off further when told to slow down.
func pollAccessToken(ctx context.Context, device deviceCodeResponse, interval time.Duration) (string, error) {
	// A poll failure is never fatal on its own — the user may still be finishing
	// in the browser — but a permanently failing endpoint (an org policy that
	// bars device flow, a proxy rejecting the call) would otherwise time out
	// with nothing to show for it, so the last one is kept to report at the end.
	var lastErr error

	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", fmt.Errorf("sign-in did not complete: %w", lastErr)
			}
			return "", fmt.Errorf("sign-in did not complete: %w", ctx.Err())
		case <-time.After(interval):
		}

		var out accessTokenResponse
		if err := postJSON(ctx, accessTokenURL, map[string]string{
			"client_id":   ClientID,
			"device_code": device.DeviceCode,
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		}, &out); err != nil {
			lastErr = err
			continue
		}
		lastErr = nil

		switch out.Error {
		case "":
			if out.AccessToken == "" {
				continue
			}
			return out.AccessToken, nil
		case "authorization_pending":
			// The user hasn't finished in the browser yet.
		case "slow_down":
			interval = slowDownBackoff(interval)
		case "expired_token":
			return "", errors.New("the sign-in code expired before it was entered — try again")
		case "access_denied":
			return "", errors.New("sign-in was denied in the browser")
		default:
			return "", fmt.Errorf("sign-in failed: %s", cmp.Or(out.ErrorDescription, out.Error))
		}
	}
}

// postJSON posts a JSON body to a GitHub OAuth endpoint and decodes the reply.
func postJSON(ctx context.Context, endpoint string, payload map[string]string, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %s", endpoint, resp.Status)
	}
	return json.Unmarshal(raw, out)
}
