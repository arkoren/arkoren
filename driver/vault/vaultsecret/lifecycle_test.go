package vaultsecret_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/vault/api"

	"arkoren.dev/driver/vault/vaultsecret"
)

// authMethodFunc supplies a native SDK authentication method through Builder.Auth.
// It substitutes only the documented external authentication boundary.
type authMethodFunc func(context.Context, *api.Client) (*api.Secret, error)

// Login returns the scenario's synthetic token without consulting any credential
// chain. The SDK still validates and installs the returned token normally.
func (authMethodFunc authMethodFunc) Login(ctx context.Context, client *api.Client) (*api.Secret, error) {
	return authMethodFunc(ctx, client)
}

// TestStartIsIdempotentWhileAuthenticated prevents a second lifecycle start from
// replacing credentials or losing an earlier renewal owner.
func TestStartIsIdempotentWhileAuthenticated(t *testing.T) {
	t.Parallel()

	var logins atomic.Int32
	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
		secrets.Address("https://vault.example")
		secrets.Auth(authMethodFunc(func(context.Context, *api.Client) (*api.Secret, error) {
			logins.Add(1)

			return &api.Secret{Auth: &api.SecretAuth{ClientToken: "synthetic-token"}}, nil
		}))
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if logins.Load() != 1 {
		t.Fatalf("Start performed %d logins, want one active owner", logins.Load())
	}
}

// TestStopRetainsOwnershipUntilNativeRenewalReturns uses a real SDK renewal RPC
// stalled at a loopback HTTP server. An expired cleanup budget cannot force that
// SDK call to finish; restart must not overwrite its outstanding worker owner.
func TestStopRetainsOwnershipUntilNativeRenewalReturns(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var released sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		entered <- struct{}{}
		<-release
		writer.Header().Set("Content-Type", "application/json")

		_, err := io.WriteString(writer, `{"auth":{"client_token":"synthetic-token","renewable":false,"lease_duration":0}}`)

		if err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	var logins atomic.Int32
	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
		secrets.Address(server.URL)
		secrets.Auth(authMethodFunc(func(context.Context, *api.Client) (*api.Secret, error) {
			first := logins.Add(1) == 1

			return &api.Secret{Auth: &api.SecretAuth{ClientToken: "synthetic-token", Renewable: first, LeaseDuration: 3600}}, nil
		}))
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		released.Do(func() {
			close(release)
		})

		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_ = driver.Stop(ctx) // This slice checks retained ownership, not select timing.

	if err := driver.Start(t.Context()); err == nil {
		t.Error("Start replaced a renewal worker whose native RPC is still running")
	}

	released.Do(func() {
		close(release)
	})

	if err := driver.Stop(t.Context()); err != nil && !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
}

// TestStartReportsMalformedNativeRenewability checks a documented SDK accessor
// error through a native auth method, rather than silently disabling renewal.
func TestStartReportsMalformedNativeRenewability(t *testing.T) {
	t.Parallel()

	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
		secrets.Address("https://vault.example")
		secrets.Auth(authMethodFunc(func(context.Context, *api.Client) (*api.Secret, error) {
			return &api.Secret{
				Auth: &api.SecretAuth{ClientToken: "synthetic-token"},
				Data: map[string]any{"renewable": "not-a-boolean"},
			}, nil
		}))
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err == nil {
		t.Fatal("Start silently ignored malformed native renewability")
	}
}

// logWriter records complete slog writes at the public logger boundary. Strings
// detach handler buffers, so assertions do not race subsequent log formatting.
type logWriter chan string

// Write captures one formatted record; test channels are buffered for the
// bounded retry sequence and cleanup stops the producer before returning.
func (logWriter logWriter) Write(data []byte) (int, error) {
	logWriter <- string(data)

	return len(data), nil
}

// TestManagedAuthenticationLogsExcludeNativeDiagnostics keeps arbitrary native
// auth failures out of automatic logs while retaining the failed stage/backoff.
func TestManagedAuthenticationLogsExcludeNativeDiagnostics(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		if _, err := io.WriteString(writer, `{"auth":{"client_token":"synthetic-token","renewable":false,"lease_duration":0}}`); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	logs := make(logWriter, 8)
	var logins atomic.Int32
	driver, err := vaultsecret.New(func(secrets *vaultsecret.Builder) {
		secrets.Address(server.URL)
		secrets.Logger(slog.New(slog.NewJSONHandler(logs, nil)))
		secrets.Auth(authMethodFunc(func(context.Context, *api.Client) (*api.Secret, error) {
			if logins.Add(1) > 1 {
				return nil, errors.New("synthetic-sensitive-diagnostic")
			}

			return &api.Secret{Auth: &api.SecretAuth{ClientToken: "synthetic-token", Renewable: true, LeaseDuration: 1}}, nil
		}))
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if record := <-logs; strings.Contains(record, "synthetic-sensitive-diagnostic") {
		t.Fatal("automatic managed-authentication log exposed native error text")
	}
}
