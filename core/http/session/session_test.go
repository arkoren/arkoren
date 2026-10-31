package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/http/session"
)

// TestFromContextReturnsDefaultSession verifies default session retrieval.
func TestFromContextReturnsDefaultSession(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	result, ok := session.FromContext(session.NewContext(context.Background(), value))

	if !ok || result != value {
		t.Fatal("context did not return its session")
	}
}

// TestFromContextForReturnsSession verifies selected session retrieval.
func TestFromContextForReturnsSession(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	ctx := session.NewContextFor(context.Background(), handle, value)
	result, ok := session.FromContextFor(ctx, handle)

	if !ok || result != value {
		t.Fatal("keyed context did not return its session")
	}
}

// TestNewSetsCreatedAtAndExpiration verifies session initialization.
func TestNewSetsCreatedAtAndExpiration(t *testing.T) {
	t.Parallel()

	before := time.Now()
	expiresAt := before.Add(time.Hour)
	value := session.New(expiresAt, map[string]any{})

	if value.CreatedAt().Before(before) || value.ExpiresAt() != expiresAt || value.ID() != value.OriginalID() || len(value.ID()) != 43 {
		t.Fatalf("session = %#v", value)
	}
}

// TestSessionPutDeleteAndClearMarkChanged verifies mutation tracking.
func TestSessionPutDeleteAndClearMarkChanged(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Hour), map[string]any{
		"a": 1,
		"b": 2,
	})
	value.MarkAsUnchanged()

	if err := value.Put(session.NewField[string]("key"), "value"); err != nil {
		t.Fatal(err)
	}

	if !value.HasChanged() || requireGet[string](t, value, "key") != "value" {
		t.Fatal("put did not update session")
	}

	value.MarkAsUnchanged()

	if err := value.Delete(session.NewField[string]("key")); err != nil {
		t.Fatal(err)
	}

	if !value.HasChanged() {
		t.Fatal("delete did not mark session changed")
	}

	_, err := value.Get(session.NewField[string]("key"))

	if !errors.Is(err, session.ErrKeyNotFound) {
		t.Fatalf("error = %v", err)
	}

	value.Clear()

	_, err = value.Get(session.NewField[int]("a"))

	if !errors.Is(err, session.ErrKeyNotFound) {
		t.Fatalf("error = %v", err)
	}
}

// TestSessionRegeneratePreservesDataAndOriginalID verifies secure ID rotation.
func TestSessionRegeneratePreservesDataAndOriginalID(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Hour), map[string]any{
		"user_id": 42,
	})
	original := value.ID()
	value.MarkAsUnchanged()
	value.Regenerate()

	if original == value.ID() || value.OriginalID() != original || !value.HasRegenerated() || !value.HasChanged() || requireGet[int](t, value, "user_id") != 42 {
		t.Fatal("regenerate did not preserve session state")
	}
}

// TestSessionExpirationState verifies expiration state reporting.
func TestSessionExpirationState(t *testing.T) {
	t.Parallel()

	expired := session.New(time.Now().Add(-time.Hour), map[string]any{})

	if !expired.HasExpired() || expired.ExpiresSoon(time.Hour) {
		t.Fatal("expired session state incorrect")
	}

	soon := session.New(time.Now().Add(time.Minute), map[string]any{})

	if !soon.ExpiresSoon(2 * time.Minute) {
		t.Fatal("soon session state incorrect")
	}
}

// TestSessionAllReturnsCopy verifies data snapshots do not alias storage.
func TestSessionAllReturnsCopy(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Hour), map[string]any{
		"key": "value",
	})

	all := value.All()
	all["key"] = "changed"

	if requireGet[string](t, value, "key") != "value" {
		t.Fatal("all returned an alias of session data")
	}
}

// requireGet returns the typed session value for key, failing the calling test
// if lookup or decoding fails. It keeps successful-value assertions separate
// from error-path tests.
func requireGet[T any](t *testing.T, value *session.Session, key string) T {
	t.Helper()

	result, err := value.Get(session.NewField[T](key))

	if err != nil {
		t.Fatal(err)
	}

	return result
}
