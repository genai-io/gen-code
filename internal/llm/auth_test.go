package llm

import (
	"context"
	"testing"
)

type fakeAuthenticator struct {
	loggedIn       bool
	loggedOut      bool
	hasCredentials bool
}

func (f *fakeAuthenticator) Login(_ context.Context, onPrompt func(LoginPrompt)) error {
	if onPrompt != nil {
		onPrompt(LoginPrompt{URL: "https://example.com/authorize", UserCode: "ABCD-1234"})
	}
	f.loggedIn = true
	return nil
}

func (f *fakeAuthenticator) Logout() error {
	f.loggedOut = true
	return nil
}

func (f *fakeAuthenticator) HasCredentials() bool { return f.hasCredentials }

func TestAuthenticatorRegistrationAndDispatch(t *testing.T) {
	const provider, method = Name("fakeprovider"), AuthMethod("fakeauth")

	// Before registration: no interactive login; Login errors, Logout no-ops.
	if SupportsInteractiveLogin(provider, method) {
		t.Fatal("did not expect an authenticator before registration")
	}
	if err := Login(context.Background(), provider, method, nil); err == nil {
		t.Error("Login without a registered authenticator should error")
	}
	if err := Logout(provider, method); err != nil {
		t.Errorf("Logout without an authenticator should be a no-op, got %v", err)
	}

	fa := &fakeAuthenticator{}
	RegisterAuthenticator(provider, method, fa)
	t.Cleanup(func() {
		globalRegistry.mu.Lock()
		delete(globalRegistry.authenticators, makeProviderKey(provider, method))
		globalRegistry.mu.Unlock()
	})

	if !SupportsInteractiveLogin(provider, method) {
		t.Fatal("expected an authenticator after registration")
	}
	if HasInteractiveCredentials(provider, method) {
		t.Fatal("did not expect stored credentials before the fake reports them")
	}

	fa.hasCredentials = true
	if !HasInteractiveCredentials(provider, method) {
		t.Fatal("expected stored credentials after the fake reports them")
	}

	var got LoginPrompt
	if err := Login(context.Background(), provider, method, func(p LoginPrompt) { got = p }); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !fa.loggedIn {
		t.Error("Login was not dispatched to the authenticator")
	}
	if got.URL == "" || got.UserCode == "" {
		t.Errorf("onPrompt callback did not deliver the sign-in instruction: %+v", got)
	}

	if err := Logout(provider, method); err != nil || !fa.loggedOut {
		t.Errorf("Logout not dispatched: err=%v loggedOut=%v", err, fa.loggedOut)
	}
}
