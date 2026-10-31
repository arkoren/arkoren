package oauthauth

import (
	"slices"
	"testing"
)

// TestClientBuilderCopiesInputsAndPublication checks scope and audience input
// ownership separately from retained-root isolation and once-only ordering.
func TestClientBuilderCopiesInputsAndPublication(t *testing.T) {
	t.Parallel()

	scopes := []string{"openid", "email"}
	audiences := []string{"api"}
	var retained *ClientBuilder
	calls := 0
	client, err := New(OIDC("https://issuer.example"), testCipher(t), "client", func(client *ClientBuilder) {
		retained = client
		calls++
		client.Scopes(scopes...)
		client.Audiences(audiences...)
		client.PKCE(false)

		scopes[0] = "input-mutated"
		audiences[0] = "input-mutated"
	}, func(client *ClientBuilder) {
		calls++

		if client != retained || calls != 2 {
			t.Fatal("callbacks were replayed or reordered")
		}
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.config.Scopes[0] = "draft-mutated"
	retained.config.Audiences[0] = "draft-mutated"
	retained.Name("later").PKCE(true)

	if !slices.Equal(client.config.Scopes, []string{"openid", "email"}) {
		t.Fatal("scope storage escaped")
	}

	if !slices.Equal(client.config.Audiences, []string{"api"}) || client.config.PKCE {
		t.Fatal("audiences or explicit false changed after publication")
	}
}

// TestClientBuilderMethodsReturnDraft verifies each setting returns the same
// draft; nil services remain ordinary settings rather than callback misuse.
func TestClientBuilderMethodsReturnDraft(t *testing.T) {
	t.Parallel()

	client := &ClientBuilder{}
	drafts := []*ClientBuilder{
		client.ClientSecret("secret"), client.RedirectURL("https://app.example/callback"),
		client.Scopes("openid"), client.Audiences("api"), client.Name("login"),
		client.AllowInsecure(false), client.PKCE(true), client.Events(nil),
	}

	for _, draft := range drafts {
		if draft != client {
			t.Fatal("method returned another draft")
		}
	}
}

// TestClientCallbacksPrecedeValidation checks invalid required credentials do
// not prevent construction policies from running directly and exactly once.
func TestClientCallbacksPrecedeValidation(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := New(Provider{}, nil, "", func(client *ClientBuilder) {
		calls++
		client.PKCE(false)
	})

	if err == nil || calls != 1 {
		t.Fatal("validation skipped a callback")
	}
}

// TestClientNilCallbackPanics verifies direct-call misuse even when required
// arguments are invalid, rather than masking it with a configuration error.
func TestClientNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = New(Provider{}, nil, "", nil)
}

// TestClientEmptyCallbackSlicesKeepSecureDefaults checks nil and empty slice
// expansion both omit edits and preserve PKCE and secure cookie naming.
func TestClientEmptyCallbackSlicesKeepSecureDefaults(t *testing.T) {
	t.Parallel()

	var configure []func(*ClientBuilder)
	client, err := New(OIDC("https://issuer.example"), testCipher(t), "client", configure...)

	if err != nil {
		t.Fatal(err)
	}

	if !client.config.PKCE || client.prefix != "__Host-arkoren.oauth." {
		t.Fatal("nil expansion changed secure defaults")
	}

	client, err = New(OIDC("https://issuer.example"), testCipher(t), "client", []func(*ClientBuilder){}...)

	if err != nil {
		t.Fatal(err)
	}

	if !client.config.PKCE || client.config.AllowInsecure {
		t.Fatal("empty expansion changed secure defaults")
	}
}

// TestClientExplicitPKCEDisableChangesAuthorization checks false survives the
// complete login path rather than merely surviving draft publication.
func TestClientExplicitPKCEDisableChangesAuthorization(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL), func(client *ClientBuilder) {
		client.PKCE(false)
	})

	query := login(t, client, cookieJar{})

	if query.Get("code_challenge") != "" || query.Get("code_challenge_method") != "" {
		t.Fatal("explicit false still sent a PKCE challenge")
	}

	if query.Get("nonce") == "" || query.Get("state") == "" {
		t.Fatal("disabling PKCE disabled unrelated login defenses")
	}
}

// TestClientApplicationPanicPropagates checks policies are invoked directly,
// without recovery, even when required credentials would fail validation.
func TestClientApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "application panic" {
			t.Fatalf("panic = %v", got)
		}
	}()

	_, _ = New(Provider{}, nil, "", func(client *ClientBuilder) {
		panic("application panic")
	})
}

// TestClientBuilderSettings verifies independent settings and secure defaults.
func TestClientBuilderSettings(t *testing.T) {
	t.Parallel()

	config := configureClient(func(client *ClientBuilder) {
		client.ClientSecret("secret")
		client.RedirectURL("https://app.example/callback")
		client.Scopes("openid")
		client.Audiences("api")
		client.Name("n")
		client.AllowInsecure(true)
		client.PKCE(true)
	})
	want := Config{
		ClientSecret:  "secret",
		RedirectURL:   "https://app.example/callback",
		Scopes:        []string{"openid"},
		Audiences:     []string{"api"},
		Name:          "n",
		AllowInsecure: true,
		PKCE:          true,
	}

	if config.Name != want.Name || !config.AllowInsecure || !config.PKCE ||
		!slices.Equal(config.Scopes, want.Scopes) || !slices.Equal(config.Audiences, want.Audiences) {
		t.Fatalf("config = %#v", config)
	}

	if defaults := configureClient(); defaults.Name != "arkoren.oauth" || !defaults.PKCE {
		t.Errorf("defaults = %#v", defaults)
	}
}
