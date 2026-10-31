package auth_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
)

// rejectLookup detects any context access made before definition validation.
// Embedding a real context preserves deadlines and cancellation unchanged.
type rejectLookup struct {
	// Context supplies deadlines and cancellation without overriding behavior.
	context.Context
}

// Value panics on lookup so invalid-definition tests cannot silently pass after
// reading request state. No value is legitimate for these tests.
func (rejectLookup) Value(any) any {
	panic("invalid identity looked up a session")
}

// checkInvalidIdentity checks every operation against both absent and populated
// explicit sessions, proving configuration failure precedes lookup or mutation.
func checkInvalidIdentity(t *testing.T, identity auth.SessionIdentity[int, user]) {
	t.Helper()

	checkInvalidIdentityLookup(t, identity)

	current := session.New(time.Now().Add(time.Hour), map[string]any{"arkoren:auth:user": 7})
	current.MarkAsUnchanged()

	id := current.ID()

	for _, value := range []*session.Session{nil, current} {
		if err := identity.Login(value, 9); !errors.Is(err, auth.ErrInvalidSessionIdentity) {
			t.Fatal(err)
		}

		if err := identity.Logout(value); !errors.Is(err, auth.ErrInvalidSessionIdentity) {
			t.Fatal(err)
		}
	}

	if current.ID() != id || current.HasChanged() || current.All()["arkoren:auth:user"] != 7 {
		t.Fatal("invalid definition mutated state")
	}
}

// checkInvalidIdentityLookup checks read-only operations against a context that
// rejects access, proving invalidity is detected before session resolution.
func checkInvalidIdentityLookup(t *testing.T, identity auth.SessionIdentity[int, user]) {
	t.Helper()

	ctx := rejectLookup{Context: context.Background()}

	if !errors.Is(identity.Err(), auth.ErrInvalidSessionIdentity) {
		t.Fatal("invalid Err result")
	}

	if _, err := identity.Session(ctx); !errors.Is(err, auth.ErrInvalidSessionIdentity) {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)

	if _, err := identity.Authenticator()(r); !errors.Is(err, auth.ErrInvalidSessionIdentity) {
		t.Fatal(err)
	}
}

// TestZeroIdentityRejectsEveryOperation keeps the zero value invalid even with
// populated session state and forbids accidental credential fallback.
func TestZeroIdentityRejectsEveryOperation(t *testing.T) {
	t.Parallel()

	checkInvalidIdentity(t, auth.SessionIdentity[int, user]{})
}

// TestNilLoaderIdentityRejectsEveryOperation verifies constructor invalidity has
// exactly the same fail-closed behavior as the zero definition.
func TestNilLoaderIdentityRejectsEveryOperation(t *testing.T) {
	t.Parallel()

	checkInvalidIdentity(t, auth.NewSessionIdentity[int, user](nil))
}

// TestInvalidIdentityStopsFallback proves a configuration error cannot be
// mistaken for absent credentials by the normal middleware chain.
func TestInvalidIdentityStopsFallback(t *testing.T) {
	t.Parallel()

	fallback := false
	identity := auth.SessionIdentity[int, user]{}
	handler := auth.Middleware(identity.Authenticator(), func(*http.Request) (user, error) {
		fallback = true

		return user{Name: "fallback"}, nil
	})(router.Handler(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("invalid identity reached handler")

		return nil
	}))

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, auth.ErrInvalidSessionIdentity) || fallback {
		t.Fatalf("error = %v, fallback = %v", err, fallback)
	}
}

// TestIdentityNilSessionsAreAbsent verifies valid definitions do not dereference
// nil explicit sessions or treat typed nil context pointers as credentials.
func TestIdentityNilSessionsAreAbsent(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)

	if err := identity.Login(nil, 7); !errors.Is(err, session.ErrNoSession) {
		t.Fatal(err)
	}

	if err := identity.Logout(nil); !errors.Is(err, session.ErrNoSession) {
		t.Fatal(err)
	}

	ctx := session.NewContext(context.Background(), nil)

	if _, err := identity.Session(ctx); !errors.Is(err, session.ErrNoSession) {
		t.Fatal(err)
	}

	if _, err := identity.Authenticator()(sessionRequest(nil)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatal(err)
	}
}

// TestIdentitySessionOnlySelectsBridge verifies OAuth's operational bridge does
// not inspect identifiers or invoke loaders, and never falls back to default.
func TestIdentitySessionOnlySelectsBridge(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	identity := auth.NewSessionIdentity(func(context.Context, int) (user, error) {
		t.Fatal("Session invoked loader")

		return user{}, nil
	}, func(authenticator *auth.SessionBuilder) {
		authenticator.Handle(handle)
	})

	current := session.New(time.Now().Add(time.Hour), map[string]any{"arkoren:auth:user": "stale"})
	ctx := session.NewContext(context.Background(), current)

	if _, err := identity.Session(ctx); !errors.Is(err, session.ErrNoSession) {
		t.Fatal(err)
	}

	ctx = session.NewContextFor(ctx, handle, current)

	value, err := identity.Session(ctx)

	if err != nil || value != current {
		t.Fatalf("session = %p, error = %v", value, err)
	}
}

// TestIdentityRejectsRuntimeJSONIdentifier distinguishes application-supplied raw
// JSON from persisted data. Physical legacy decoding is covered through session
// middleware by TestIdentityReadsPhysicalLegacyRecord.
func TestIdentityRejectsRuntimeJSONIdentifier(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(_ context.Context, id int64) (int64, error) {
		t.Fatal("runtime JSON identifier reached loader")

		return id, nil
	})

	current := session.New(time.Now().Add(time.Hour), map[string]any{
		"arkoren:auth:user": jsontext.Value(`9007199254740993`),
	})

	id, err := identity.Authenticator()(sessionRequest(current))

	if !errors.Is(err, auth.ErrNoCredentials) || id != 0 {
		t.Fatalf("identifier = %d, error = %v", id, err)
	}
}

// TestIdentityLoginPreservesLifetimeAndUnrelatedState verifies typed binding
// does not change rotation, session age, expiry or unrelated member semantics.
func TestIdentityLoginPreservesLifetimeAndUnrelatedState(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := session.New(time.Now().Add(time.Hour), map[string]any{"theme": "dark"})
	createdAt, expiresAt, originalID := current.CreatedAt(), current.ExpiresAt(), current.ID()

	if err := identity.Login(current, 7); err != nil {
		t.Fatal(err)
	}

	if current.ID() == originalID || current.OriginalID() != originalID {
		t.Fatal("login rotation changed")
	}

	if current.CreatedAt() != createdAt || current.ExpiresAt() != expiresAt || current.All()["theme"] != "dark" {
		t.Fatal("login changed lifetime or unrelated state")
	}
}

// TestIdentityLogoutPreservesLifetimeAndUnrelatedState verifies removing a
// login never refreshes idle expiry, resets age, or deletes application data.
func TestIdentityLogoutPreservesLifetimeAndUnrelatedState(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := session.New(time.Now().Add(time.Hour), map[string]any{
		"arkoren:auth:user": 7,
		"theme":             "dark",
	})
	createdAt, expiresAt := current.CreatedAt(), current.ExpiresAt()

	if err := identity.Logout(current); err != nil {
		t.Fatal(err)
	}

	if current.CreatedAt() != createdAt || current.ExpiresAt() != expiresAt || current.All()["theme"] != "dark" {
		t.Fatal("logout changed lifetime or unrelated state")
	}
}

// TestIdentityWrongLoginTypeDoesNotCompile verifies the identifier bound during
// construction is enforced at the login call rather than checked at runtime.
func TestIdentityWrongLoginTypeDoesNotCompile(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "go", "test", "./testdata/identity")

	output, err := command.CombinedOutput()

	if err == nil || !strings.Contains(string(output), "cannot use \"wrong\"") {
		t.Fatalf("wrong identifier compilation: %v\n%s", err, output)
	}
}

// TestIdentityLoaderCaptureAndError verifies construction does not invoke the
// loader, function captures stay shared, and loader errors retain their cause.
func TestIdentityLoaderCaptureAndError(t *testing.T) {
	t.Parallel()

	calls := 0
	want := errors.New("user backend unavailable")
	identity := auth.NewSessionIdentity(func(context.Context, int) (user, error) {
		calls++

		return user{}, want
	})

	if calls != 0 {
		t.Fatal("constructor invoked loader")
	}

	current := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := identity.Login(current, 7); err != nil {
		t.Fatal(err)
	}

	if _, err := identity.Authenticator()(sessionRequest(current)); !errors.Is(err, want) || calls != 1 {
		t.Fatalf("loader calls = %d, error = %v", calls, err)
	}
}
