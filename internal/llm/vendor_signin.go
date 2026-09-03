package llm

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"net/http"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/auth"
	"github.com/genai-io/sdk-go/pkg/ai/auth/oauth"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	sdkprovider "github.com/genai-io/sdk-go/pkg/ai/provider"

	"github.com/genai-io/san/internal/core"
)

// Signing in to the two vendors that authenticate a person rather than a
// service, and keeping the result where San already keeps it.
//
// The SDK runs both grants and renews both tokens; what stays here is where
// the credential lives. It lives in San's secret store, under the keys San has
// always used and in the shape it has always written — so somebody who signed
// in before this switch is still signed in after it.

const (
	copilotVendor = "copilot"
	codexVendor   = "openai-codex"

	// requestTimeout bounds one inference request. A turn can legitimately run
	// for minutes, so this is a ceiling on a wedged connection, not on
	// thinking.
	requestTimeout = 10 * time.Minute
)

// signIns maps a catalog vendor that signs in interactively to the San
// provider/auth pair that offers it in the UI.
var signIns = map[string]Meta{
	copilotVendor: {Provider: Copilot, AuthMethod: AuthSubscription},
	codexVendor:   {Provider: OpenAI, AuthMethod: AuthSubscription},
}

// configureSignIn points an endpoint at a stored interactive credential. The
// token itself is minted per request by the SDK's transport, so nothing here
// goes stale mid-session.
func configureSignIn(vendor catalog.Vendor, cfg *sdkprovider.Config) error {
	credential, found, err := credentials{}.Load(vendor.ID)
	if err != nil {
		return err
	}
	if !found || credential.Access == "" {
		return fmt.Errorf("llm: not signed in to %s", vendor.DisplayName)
	}

	client, err := auth.HTTPClient(vendor.ID, credential, credentials{}, &http.Client{Timeout: requestTimeout})
	if err != nil {
		return err
	}

	cfg.APIKey = ""
	cfg.HTTPClient = client
	// Copilot reveals the host the account actually talks to only at sign-in,
	// and an enterprise account's is not the published one.
	if credential.Endpoint != "" {
		cfg.BaseURL = credential.Endpoint
	}

	// The subscription backend publishes its lineup at its own catalog
	// endpoint, not through the protocol's model listing.
	if vendor.ID == codexVendor {
		cfg.Fetch = codexModels
	}

	cfg.Headers = maps.Clone(vendor.Headers)
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	maps.Copy(cfg.Headers, identityHeaders(vendor.ID))
	return nil
}

// identityHeaders are what each backend requires beyond a token: both serve an
// editor integration rather than the public API, and refuse a caller that does
// not present itself as one.
func identityHeaders(vendorID string) map[string]string {
	switch vendorID {
	case copilotVendor:
		return map[string]string{
			"Openai-Intent":        "conversation-panel",
			"X-GitHub-Api-Version": "2025-04-01",
			"X-Request-Id":         requestID(),
			"User-Agent":           "GitHubCopilotChat/0.26.7",
		}
	case codexVendor:
		headers := map[string]string{
			"OpenAI-Beta": "responses=experimental",
			"originator":  codexOriginator,
			"User-Agent":  codexOriginator,
			"session_id":  requestID(),
		}
		// The account the subscription belongs to. The backend rejects a
		// request that does not name it.
		if account := codexAccountID(); account != "" {
			headers["chatgpt-account-id"] = account
		}
		return headers
	}
	return nil
}

// codexOriginator identifies the caller to the ChatGPT backend, which serves
// the Codex lineup only to the Codex CLI.
const codexOriginator = "codex_cli_rs"

// requestID returns a random UUIDv4 for the per-request and per-session
// identifiers these two backends expect in a header — Copilot's X-Request-Id,
// the ChatGPT backend's session_id.
func requestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Interactive sign-in, as the registry above expects it.

// authenticator runs one vendor's sign-in through the SDK.
type authenticator struct{ vendorID string }

func (a authenticator) Login(ctx context.Context, onPrompt func(LoginPrompt)) error {
	interaction := oauth.InteractionFunc(func(_ context.Context, p oauth.Prompt) error {
		if onPrompt != nil {
			onPrompt(LoginPrompt{URL: p.URL, UserCode: p.UserCode})
		}
		return nil
	})
	_, err := auth.Login(ctx, a.vendorID, auth.LoginOptions{
		Store:       credentials{},
		Interaction: interaction,
	})
	return err
}

func (a authenticator) Logout() error { return auth.Logout(a.vendorID, credentials{}) }

func (a authenticator) HasCredentials() bool {
	_, found, err := credentials{}.Load(a.vendorID)
	return err == nil && found
}

var (
	_ Authenticator                 = authenticator{}
	_ StoredCredentialAuthenticator = authenticator{}
)

func registerAuthenticators() {
	for vendorID, meta := range signIns {
		RegisterAuthenticator(meta.Provider, meta.AuthMethod, authenticator{vendorID: vendorID})
	}
}

// turnHeadersFor returns the headers one endpoint needs that depend on what
// the turn actually sends. Copilot is the only one: it meters an agent's
// follow-up differently from a turn the user typed, and it rejects image
// content unless the request opts into vision.
func turnHeadersFor(vendorID string) func([]core.Message) map[string]string {
	if vendorID != copilotVendor {
		return nil
	}
	return func(msgs []core.Message) map[string]string {
		// Anything past the opening user message means the loop is driving.
		// This is an approximation San has always made: a resumed session's
		// first user turn still reads as "agent".
		initiator := "user"
		vision := false
		for _, msg := range msgs {
			if msg.Role == ai.RoleAssistant || len(msg.ToolResults()) > 0 {
				initiator = "agent"
			}
			if msg.Content.HasImages() {
				vision = true
			}
		}
		headers := map[string]string{"X-Initiator": initiator}
		if vision {
			headers["Copilot-Vision-Request"] = "true"
		}
		return headers
	}
}
