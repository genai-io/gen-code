package llm

import (
	"context"
	"fmt"
)

// Signing in to a provider that authenticates a person rather than a service.
// The vendor half lives in vendor_signin.go; what is here is the contract
// between the two, so the /models panel can offer a sign-in without knowing
// which vendor it is talking to.

// LoginPrompt is what the user must do to finish a sign-in: the page to open
// and, for device-code flows, the code to type there. A browser-callback (PKCE)
// flow leaves UserCode empty.
type LoginPrompt struct {
	URL      string
	UserCode string
}

// Authenticator performs interactive (non-API-key) sign-in for a provider auth
// method.
type Authenticator interface {
	// Login runs the interactive sign-in, persisting credentials on success.
	// onPrompt, if non-nil, receives the instruction the user must follow —
	// needed when a browser cannot be opened automatically (e.g. over SSH), and
	// always needed for device-code flows, where the code exists nowhere else.
	Login(ctx context.Context, onPrompt func(LoginPrompt)) error
	// Logout clears any stored credentials for the auth method.
	Logout() error
}

// StoredCredentialAuthenticator is an optional extension for authenticators that
// can report whether they already have local credentials worth validating.
type StoredCredentialAuthenticator interface {
	HasCredentials() bool
}

// RegisterAuthenticator records the interactive login handler for a provider
// auth method.
func RegisterAuthenticator(provider ProviderID, authMethod AuthMethod, a Authenticator) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.authenticators[providerKey(provider, authMethod)] = a
}

// SupportsInteractiveLogin reports whether a provider auth method signs in
// interactively (OAuth) rather than via an API key.
func SupportsInteractiveLogin(provider ProviderID, authMethod AuthMethod) bool {
	return lookupAuthenticator(provider, authMethod) != nil
}

// HasInteractiveCredentials reports whether an interactive auth method already
// has stored credentials. Callers should still verify them with the provider,
// because this only checks local presence, not remote validity.
func HasInteractiveCredentials(provider ProviderID, authMethod AuthMethod) bool {
	stored, ok := lookupAuthenticator(provider, authMethod).(StoredCredentialAuthenticator)
	return ok && stored.HasCredentials()
}

// Login runs the interactive sign-in for a provider auth method.
func Login(ctx context.Context, provider ProviderID, authMethod AuthMethod, onPrompt func(LoginPrompt)) error {
	a := lookupAuthenticator(provider, authMethod)
	if a == nil {
		return fmt.Errorf("provider %s:%s does not support interactive login", provider, authMethod)
	}
	return a.Login(ctx, onPrompt)
}

// Logout clears stored credentials for a provider auth method. It is a no-op for
// methods without an interactive authenticator (API-key credentials are cleared
// via the secret store instead).
func Logout(provider ProviderID, authMethod AuthMethod) error {
	a := lookupAuthenticator(provider, authMethod)
	if a == nil {
		return nil
	}
	return a.Logout()
}

// lookupAuthenticator returns the registered Authenticator for a provider auth
// method, or nil when none is registered.
func lookupAuthenticator(provider ProviderID, authMethod AuthMethod) Authenticator {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()
	return globalRegistry.authenticators[providerKey(provider, authMethod)]
}
