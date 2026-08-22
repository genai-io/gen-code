package sdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/auth"
	"github.com/genai-io/sdk-go/pkg/ai/auth/oauth"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	sdkprovider "github.com/genai-io/sdk-go/pkg/ai/provider"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/secret"
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
var signIns = map[string]llm.Meta{
	copilotVendor: {Provider: llm.Copilot, AuthMethod: llm.AuthSubscription},
	codexVendor:   {Provider: llm.OpenAI, AuthMethod: llm.AuthSubscription},
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
		return fmt.Errorf("sdk: not signed in to %s", vendor.DisplayName)
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
			"X-Request-Id":         llm.NewRequestID(),
			"User-Agent":           "GitHubCopilotChat/0.26.7",
		}
	case codexVendor:
		headers := map[string]string{
			"OpenAI-Beta": "responses=experimental",
			"originator":  codexOriginator,
			"User-Agent":  codexOriginator,
			"session_id":  llm.NewRequestID(),
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

// ---------------------------------------------------------------------------
// The credential store
// ---------------------------------------------------------------------------

// credentials is San's secret store, presented as the SDK's credential store.
//
// The two disagree on shape, and the translation is the point: San's blobs
// carry fields the SDK's Credential has no room for — the ChatGPT account id,
// the cached Copilot bearer — and a save that dropped them would sign the user
// out on the next run. Save reads the stored blob back and writes only what
// the SDK actually rotated.
type credentials struct{}

// storeKey is where a vendor's credential lives in San's secret store.
func storeKey(vendorID string) (string, bool) {
	switch vendorID {
	case copilotVendor:
		return "GITHUB_COPILOT_AUTH", true
	case codexVendor:
		return "OPENAI_CHATGPT_AUTH", true
	}
	return "", false
}

// copilotBlob is San's stored Copilot credential. The GitHub token is the
// durable half; the rest caches the last exchange.
type copilotBlob struct {
	GitHubToken  string    `json:"github_token"`
	CopilotToken string    `json:"copilot_token,omitempty"`
	APIEndpoint  string    `json:"api_endpoint,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

// codexBlob is San's stored ChatGPT credential.
type codexBlob struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

func (credentials) Load(vendorID string) (auth.Credential, bool, error) {
	key, ok := storeKey(vendorID)
	if !ok {
		return auth.Credential{}, false, nil
	}
	raw := secret.Resolve(key)
	if raw == "" {
		return auth.Credential{}, false, nil
	}

	switch vendorID {
	case copilotVendor:
		var blob copilotBlob
		if err := json.Unmarshal([]byte(raw), &blob); err != nil || blob.GitHubToken == "" {
			return auth.Credential{}, false, nil
		}
		return auth.Credential{
			Vendor:   vendorID,
			Access:   blob.GitHubToken,
			Endpoint: blob.APIEndpoint,
		}, true, nil
	default:
		var blob codexBlob
		if err := json.Unmarshal([]byte(raw), &blob); err != nil || blob.AccessToken == "" {
			return auth.Credential{}, false, nil
		}
		return auth.Credential{
			Vendor:    vendorID,
			Access:    blob.AccessToken,
			Refresh:   blob.RefreshToken,
			ExpiresAt: blob.ExpiresAt,
		}, true, nil
	}
}

func (c credentials) Save(credential auth.Credential) error {
	key, ok := storeKey(credential.Vendor)
	if !ok {
		return fmt.Errorf("sdk: %q keeps no interactive credential", credential.Vendor)
	}
	store := secret.Default()
	if store == nil {
		return errors.New("sdk: secret store unavailable")
	}

	var raw []byte
	var err error
	switch credential.Vendor {
	case copilotVendor:
		raw, err = json.Marshal(copilotBlob{
			GitHubToken: credential.Access,
			APIEndpoint: credential.Endpoint,
		})
	default:
		// Keep the fields the SDK does not model: the account id is what the
		// backend routes on, and a refresh must not lose it.
		var blob codexBlob
		_ = json.Unmarshal([]byte(secret.Resolve(key)), &blob)
		blob.AccessToken = credential.Access
		blob.RefreshToken = credential.Refresh
		blob.ExpiresAt = credential.ExpiresAt
		if blob.AccountID == "" {
			blob.AccountID = accountFromToken(blob.IDToken, blob.AccessToken)
		}
		raw, err = json.Marshal(blob)
	}
	if err != nil {
		return err
	}
	return store.Set(key, string(raw))
}

func (credentials) Delete(vendorID string) error {
	key, ok := storeKey(vendorID)
	if !ok {
		return nil
	}
	store := secret.Default()
	if store == nil {
		return nil
	}
	return store.Delete(key)
}

func (c credentials) List() ([]string, error) {
	var out []string
	for vendorID := range signIns {
		if _, found, err := c.Load(vendorID); err == nil && found {
			out = append(out, vendorID)
		}
	}
	return out, nil
}

var _ auth.Store = credentials{}

// codexAccountID reads the stored ChatGPT account id.
func codexAccountID() string {
	var blob codexBlob
	if err := json.Unmarshal([]byte(secret.Resolve("OPENAI_CHATGPT_AUTH")), &blob); err != nil {
		return ""
	}
	if blob.AccountID != "" {
		return blob.AccountID
	}
	return accountFromToken(blob.IDToken, blob.AccessToken)
}

// ---------------------------------------------------------------------------
// Interactive sign-in, as San's registry expects it
// ---------------------------------------------------------------------------

// authenticator runs one vendor's sign-in through the SDK.
type authenticator struct{ vendorID string }

func (a authenticator) Login(ctx context.Context, onPrompt func(llm.LoginPrompt)) error {
	interaction := oauth.InteractionFunc(func(_ context.Context, p oauth.Prompt) error {
		if onPrompt != nil {
			onPrompt(llm.LoginPrompt{URL: p.URL, UserCode: p.UserCode})
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
	_ llm.Authenticator                 = authenticator{}
	_ llm.StoredCredentialAuthenticator = authenticator{}
)

func registerAuthenticators() {
	for vendorID, meta := range signIns {
		llm.RegisterAuthenticator(meta.Provider, meta.AuthMethod, authenticator{vendorID: vendorID})
	}
}

// ---------------------------------------------------------------------------
// The user-defined endpoint
// ---------------------------------------------------------------------------

// customVendor builds a catalog row for the OpenAI-compatible endpoint the
// user configured in the app. It exists in no catalog, so San supplies what a
// vendor entry would have said: the protocol, the host, and nothing else.
func customVendor() (catalog.Vendor, error) {
	store, err := llm.NewStore()
	if err != nil {
		return catalog.Vendor{}, fmt.Errorf("sdk: loading the provider store: %w", err)
	}
	cfg := store.CustomProvider()
	if cfg == nil || cfg.BaseURL == "" {
		return catalog.Vendor{}, fmt.Errorf("custom provider not configured: set a base URL under /models > Providers > Custom")
	}
	return catalog.Vendor{
		ID:          string(customProvider),
		DisplayName: "Custom",
		API:         ai.APIOpenAIChat,
		BaseURL:     cfg.BaseURL,
		KeyEnv:      []string{customAPIKeyEnvVar},
		Input:       []ai.Modality{ai.ModalityText, ai.ModalityImage},
		Compat:      ai.OpenAIChatCompat{},
	}, nil
}

// configureCustom is a no-op beyond what the vendor row already carries: the
// host came from the store and the key from the secret store.
func configureCustom(catalog.Vendor, *sdkprovider.Config) error { return nil }

// accountFromToken reads the ChatGPT account id out of a signed token.
//
// The id is a claim rather than a field: OpenAI puts it in the token it issues,
// and the Codex backend routes on it. Both the id token and the access token
// carry it, which is why more than one is tried — a sign-in that returned only
// an access token still yields the id.
func accountFromToken(tokens ...string) string {
	for _, token := range tokens {
		claims, ok := jwtClaims(token)
		if !ok {
			continue
		}
		scope, ok := claims["https://api.openai.com/auth"].(map[string]any)
		if !ok {
			continue
		}
		if id, ok := scope["chatgpt_account_id"].(string); ok && id != "" {
			return id
		}
	}
	return ""
}

// jwtClaims decodes a JWT's payload without verifying it. Nothing here trusts
// the token: it was just issued to us over TLS, and the only thing read from it
// is which account to name in a header.
func jwtClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
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
			if msg.Role == core.RoleAssistant || msg.ToolResult != nil {
				initiator = "agent"
			}
			if len(msg.Images) > 0 {
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
