package oauthauth

import (
	"testing"
)

// TestGoogleBuilderPublishesCompletedRestriction checks ordered callback edits
// are copied before the provider captures its verified-domain restriction.
func TestGoogleBuilderPublishesCompletedRestriction(t *testing.T) {
	t.Parallel()

	var retained *GoogleBuilder
	calls := 0
	provider := Google(func(provider *GoogleBuilder) {
		retained = provider
		calls++

		if provider.HostedDomain("first.example") != provider {
			t.Fatal("method returned another draft")
		}
	}, func(provider *GoogleBuilder) {
		calls++

		if provider != retained || calls != 2 {
			t.Fatal("callbacks were replayed or reordered")
		}

		provider.HostedDomain("example.com")
	})

	retained.HostedDomain("attacker.example")

	_, err := provider.identity(Identity{
		Issuer: googleIssuer,
		Claims: map[string]any{"hd": "example.com"},
	})

	if err != nil {
		t.Fatal(err)
	}

	if provider.authParams["hd"] != "example.com" {
		t.Fatal("authorization hint retained the mutable draft")
	}
}

// TestGoogleNilCallbackPanics checks provider construction uses the same direct
// nil-entry behavior as client construction, without filtering callbacks.
func TestGoogleNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_ = Google(nil)
}

// TestGoogleEmptyCallbackSlicesKeepPreset checks nil and empty expansion do not
// add a hosted-domain restriction or alter Google's required default scopes.
func TestGoogleEmptyCallbackSlicesKeepPreset(t *testing.T) {
	t.Parallel()

	var configure []func(*GoogleBuilder)
	provider := Google(configure...)

	if len(provider.authParams) != 0 || len(provider.scopes) != 3 {
		t.Fatal("nil expansion changed the preset")
	}

	provider = Google([]func(*GoogleBuilder){}...)

	if len(provider.authParams) != 0 || len(provider.scopes) != 3 {
		t.Fatal("empty expansion changed the preset")
	}
}

// TestGoogleApplicationPanicPropagates checks Google preset construction keeps
// application panics distinct from identity validation errors at request time.
func TestGoogleApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "application panic" {
			t.Fatalf("panic = %v", got)
		}
	}()

	_ = Google(func(provider *GoogleBuilder) {
		panic("application panic")
	})
}

// requireGoogleVerify verifies a token with the given claims against a Google
// provider built with configure: Verify must fail exactly when wantErr is set, and
// on success must report the test issuer and the wantEmail address.
func requireGoogleVerify(
	t *testing.T,
	idp *testProvider,
	configure []func(*GoogleBuilder),
	claims map[string]any,
	wantErr bool,
	wantEmail string,
) {
	t.Helper()

	client := newClient(t, google(idp.server.URL, configure...))

	identity, err := client.Verify(t.Context(), idp.sign(claims))

	if (err != nil) != wantErr {
		t.Fatalf("Verify() error = %v, wantErr %v", err, wantErr)
	}

	if err == nil && (identity.Issuer != idp.server.URL || identity.Email != wantEmail) {
		t.Errorf("identity = %q %q", identity.Issuer, identity.Email)
	}
}

// TestGoogleFullIssuer verifies the Google full issuer case: issuer normalization, hosted domain and email checks.
func TestGoogleFullIssuer(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireGoogleVerify(t, idp, nil, map[string]any{"email": "a@b.c", "email_verified": true}, false, "a@b.c")
}

// TestGoogleBareIssuer verifies the Google bare issuer case: issuer normalization, hosted domain and email checks.
func TestGoogleBareIssuer(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)
	bare := idp.server.URL[len("http://"):]

	requireGoogleVerify(t, idp, nil, map[string]any{"iss": bare, "email": "a@b.c", "email_verified": true}, false, "a@b.c")
}

// TestGoogleOtherIssuer verifies the Google other issuer case: issuer normalization, hosted domain and email checks.
func TestGoogleOtherIssuer(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireGoogleVerify(t, idp, nil, map[string]any{"iss": "https://evil.test"}, true, "")
}

// TestGoogleUnverifiedEmail verifies the Google unverified email case: issuer normalization, hosted domain and email checks.
func TestGoogleUnverifiedEmail(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireGoogleVerify(t, idp, nil, map[string]any{"email": "a@b.c", "email_verified": false}, false, "")
}

// TestGoogleMissingVerified verifies the Google missing verified case: issuer normalization, hosted domain and email checks.
func TestGoogleMissingVerified(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireGoogleVerify(t, idp, nil, map[string]any{"email": "a@b.c"}, false, "")
}

// TestGoogleHostedDomainMatch verifies the Google hd match case: issuer normalization, hosted domain and email checks.
func TestGoogleHostedDomainMatch(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireGoogleVerify(t, idp, []func(*GoogleBuilder){func(provider *GoogleBuilder) {
		provider.HostedDomain("b.c")
	}}, map[string]any{"hd": "b.c"}, false, "")
}

// TestGoogleHostedDomainMismatch verifies the Google hd mismatch case: issuer normalization, hosted domain and email checks.
func TestGoogleHostedDomainMismatch(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireGoogleVerify(t, idp, []func(*GoogleBuilder){func(provider *GoogleBuilder) {
		provider.HostedDomain("b.c")
	}}, map[string]any{"hd": "x.y"}, true, "")
}

// TestGoogleHostedDomainAbsent verifies the Google hd absent case: issuer normalization, hosted domain and email checks.
func TestGoogleHostedDomainAbsent(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireGoogleVerify(t, idp, []func(*GoogleBuilder){func(provider *GoogleBuilder) {
		provider.HostedDomain("b.c")
	}}, nil, true, "")
}

// TestGoogleHostedDomainParam verifies the hd authorization parameter and defaults.
func TestGoogleHostedDomainParam(t *testing.T) {
	t.Parallel()

	provider := Google(func(provider *GoogleBuilder) {
		provider.HostedDomain("b.c")
	})

	if provider.authParams["hd"] != "b.c" || provider.issuer != "https://accounts.google.com" || len(provider.scopes) != 3 {
		t.Errorf("provider = %+v", provider)
	}

	if params := Google().authParams; len(params) != 0 {
		t.Errorf("authParams = %v", params)
	}
}
