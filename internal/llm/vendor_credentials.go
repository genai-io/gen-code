package llm

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai/auth"

	"github.com/genai-io/san/internal/secret"
)

// A vendor's credential, where San already keeps it.
//
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
		return fmt.Errorf("llm: %q keeps no interactive credential", credential.Vendor)
	}
	store := secret.Default()
	if store == nil {
		return errors.New("llm: secret store unavailable")
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
