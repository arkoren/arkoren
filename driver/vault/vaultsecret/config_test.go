package vaultsecret

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/vault/api"

	"arkoren.dev/core/service/secret"
)

// TestNewAppliesCallbacksBeforeValidation verifies that a later address replaces
// an invalid earlier address before SDK validation of the completed settings.
func TestNewAppliesCallbacksBeforeValidation(t *testing.T) {
	t.Parallel()

	driver, err := New(func(secrets *Builder) {
		secrets.Address("://invalid")
	}, func(secrets *Builder) {
		secrets.Address("http://127.0.0.1:8200")
	})

	if err != nil || driver == nil {
		t.Fatalf("New = %v, %v", driver, err)
	}
}

// failingAuth is an auth method whose login always fails.
type failingAuth struct{}

// Login always returns an error.
func (failingAuth) Login(context.Context, *api.Client) (*api.Secret, error) {
	return nil, errors.New("denied")
}

// TestNewWithAuthReturnsNotStartedBeforeStart verifies that a driver with an
// auth method refuses requests until Start logs in.
func TestNewWithAuthReturnsNotStartedBeforeStart(t *testing.T) {
	t.Parallel()

	driver, err := New(func(secrets *Builder) {
		secrets.Auth(failingAuth{})
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Delete(t.Context(), "name"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Delete error = %v, want ErrNotStarted", err)
	}
}

// TestStartFailsWhenLoginFails verifies that Start surfaces login errors.
func TestStartFailsWhenLoginFails(t *testing.T) {
	t.Parallel()

	driver, err := New(func(secrets *Builder) {
		secrets.Auth(failingAuth{})
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err == nil {
		t.Fatal("Start error = nil, want login error")
	}
}

// TestBuilderCallbacksPublishDetachedSettings checks ordered once-only edits
// and uses a local Vault-shaped response to observe the completed mount/token.
func TestBuilderCallbacksPublishDetachedSettings(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/application/data/name" || request.Header.Get("X-Vault-Token") != "original" {
			t.Errorf("request = %s, token = %q", request.URL.Path, request.Header.Get("X-Vault-Token"))
		}

		if _, err := io.WriteString(writer, `{"data":{"data":{"value":"test"},"metadata":{"version":1}}}`); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	var retained *Builder
	calls := 0
	driver, err := New(func(secrets *Builder) {
		calls++
		retained = secrets
		secrets.Address(server.URL)
		secrets.Token("original")
	}, func(secrets *Builder) {
		calls++

		if secrets != retained || secrets.config.Token != "original" {
			t.Fatal("callbacks did not share the ordered draft")
		}

		secrets.Mount("application")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Token("later")
	retained.Mount("later")

	if _, err := driver.Get(t.Context(), secret.Reference{
		Name: "name",
	}); err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Fatalf("callback calls = %d, want 2", calls)
	}
}

// builderAuth records native login use without making a network request.
// Construction must retain the handle without calling Login.
type builderAuth struct {
	// calls counts login invocations on this caller-owned method.
	calls int
}

// Login records one SDK invocation and returns a deterministic failure. The
// failure lets Start exercise error propagation without starting renewal.
func (auth *builderAuth) Login(context.Context, *api.Client) (*api.Secret, error) {
	auth.calls++

	return nil, errors.New("builder login denied")
}

// TestBuilderAuthIsSharedAndDeferred checks that retained root changes cannot
// redirect authentication or logging, while the native auth handle stays live.
func TestBuilderAuthIsSharedAndDeferred(t *testing.T) {
	t.Parallel()

	auth := &builderAuth{}
	logger := slog.New(slog.DiscardHandler)
	var retained *Builder
	driver, err := New(func(secrets *Builder) {
		retained = secrets
		secrets.Auth(auth)
		secrets.Logger(logger)
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Auth(nil)
	retained.Logger(nil)

	if auth.calls != 0 || driver.renewal == nil {
		t.Fatal("construction invoked auth or retained the settings root")
	}

	if err := driver.Start(t.Context()); err == nil {
		t.Fatal("Start lost the configured auth method")
	}

	if auth.calls != 1 {
		t.Fatalf("native auth calls = %d, want 1", auth.calls)
	}
}

// TestBuilderEmptyExpansionsOmitEdits verifies nil and empty slices construct
// independent static-token drivers without an empty native-client builder.
func TestBuilderEmptyExpansionsOmitEdits(t *testing.T) {
	t.Parallel()

	var callbacks []func(*Builder)
	first, err := New(callbacks...)

	if err != nil {
		t.Fatal(err)
	}

	second, err := New([]func(*Builder){}...)

	if err != nil {
		t.Fatal(err)
	}

	if first == second || first.renewal != nil || second.renewal != nil {
		t.Fatal("empty expansions did not publish independent static drivers")
	}
}

// TestBuilderNilCallbackPanics distinguishes an explicit nil entry from an
// omitted callback slice, before SDK configuration validation.
func TestBuilderNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = New(nil)
}

// TestBuilderCallbackPanicPropagates ensures user failures are not recovered or
// converted into SDK validation errors by the constructor.
func TestBuilderCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recovered := recover(); recovered != "policy failed" {
			t.Fatalf("panic = %v, want policy failed", recovered)
		}
	}()

	_, _ = New(func(secrets *Builder) {
		secrets.Mount("unused")
		panic("policy failed")
	})
}

// TestBuilderNilAuthClearsManagedAuthentication checks a nil SDK hook is a
// valid edit rather than callback misuse, and restores static-token readiness.
func TestBuilderNilAuthClearsManagedAuthentication(t *testing.T) {
	t.Parallel()

	driver, err := New(func(secrets *Builder) {
		secrets.Auth(&builderAuth{})
	}, func(secrets *Builder) {
		secrets.Auth(nil)
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.renewal != nil || !driver.started.Load() {
		t.Fatal("nil auth did not restore static-token readiness")
	}
}

// TestBuilderInvalidAddressFailsAfterAllCallbacks preserves SDK validation
// timing while ensuring no callback is skipped because an earlier edit is bad.
func TestBuilderInvalidAddressFailsAfterAllCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := New(func(secrets *Builder) {
		calls++
		secrets.Address("://invalid")
	}, func(secrets *Builder) {
		calls++
		secrets.Mount("application")
	})

	if err == nil || calls != 2 {
		t.Fatalf("New error = %v, callbacks = %d", err, calls)
	}
}

// TestBuilderSettingsReturnReceiver verifies every setting mutates the same
// draft and accepts empty values and nil hooks without eager validation.
func TestBuilderSettingsReturnReceiver(t *testing.T) {
	t.Parallel()

	secrets := &Builder{}
	results := []*Builder{
		secrets.Address(""), secrets.Token(""), secrets.Mount(""),
		secrets.Auth(nil), secrets.Logger(nil),
	}

	for _, result := range results {
		if result != secrets {
			t.Fatal("setting did not return the same receiver")
		}
	}
}
