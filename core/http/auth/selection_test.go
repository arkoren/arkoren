package auth_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/session"
)

// TestSessionCallbacksPublishIndependentHandle checks once-only ordered edits,
// one shared draft, and detachment from a retained construction draft.
func TestSessionCallbacksPublishIndependentHandle(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	var retained *auth.SessionBuilder
	calls := 0
	identity := auth.NewSessionIdentity(loadByID, func(authenticator *auth.SessionBuilder) {
		retained = authenticator
		calls++

		if authenticator.Handle(session.NewHandle()) != authenticator {
			t.Fatal("setting returned another draft")
		}
	}, func(authenticator *auth.SessionBuilder) {
		if calls != 1 || retained != authenticator {
			t.Fatal("callbacks reordered or used different drafts")
		}

		calls++
		authenticator.Handle(handle)
	})
	retained.Handle(session.Handle{})

	current := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := identity.Login(current, 7); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(session.NewContextFor(r.Context(), handle, current))

	value, err := identity.Authenticator()(r)

	if err != nil || value.Name != "u" || calls != 2 {
		t.Fatalf("user = %+v, err = %v, callbacks = %d", value, err, calls)
	}
}

// TestSessionZeroHandleRestoresDefault verifies a later zero replaces a custom
// selection without relying on a fallback at request time.
func TestSessionZeroHandleRestoresDefault(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID, func(authenticator *auth.SessionBuilder) {
		authenticator.Handle(session.NewHandle())
		authenticator.Handle(session.Handle{})
	})

	current := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := identity.Login(current, 7); err != nil {
		t.Fatal(err)
	}

	value, err := identity.Authenticator()(sessionRequest(current))

	if err != nil || value.Name != "u" {
		t.Fatalf("user = %+v, err = %v", value, err)
	}
}

// TestSessionNilLoaderRunsCallbacks verifies ordinary invalid configuration
// does not suppress construction callbacks or invoke the missing loader.
func TestSessionNilLoaderRunsCallbacks(t *testing.T) {
	t.Parallel()

	var load func(context.Context, int) (user, error)
	calls := 0
	identity := auth.NewSessionIdentity(load, func(authenticator *auth.SessionBuilder) {
		calls++
	}, func(authenticator *auth.SessionBuilder) {
		calls *= 2
	})

	if calls != 2 || !errors.Is(identity.Err(), auth.ErrInvalidSessionIdentity) {
		t.Fatalf("callbacks = %d, err = %v", calls, identity.Err())
	}
}

// TestSessionConstructionMisusePanics preserves direct nil-entry invocation.
func TestSessionConstructionMisusePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	_ = auth.NewSessionIdentity(loadByID, nil)
}

// TestSessionEmptyCallbackExpansions verifies both expanded empty forms omit
// construction edits and preserve valid default definitions.
func TestSessionEmptyCallbackExpansions(t *testing.T) {
	t.Parallel()

	var configure []auth.SessionBuilderFunc

	if err := auth.NewSessionIdentity(loadByID, configure...).Err(); err != nil {
		t.Fatal(err)
	}

	if err := auth.NewSessionIdentity(loadByID, []auth.SessionBuilderFunc{}...).Err(); err != nil {
		t.Fatal(err)
	}
}

// TestSessionApplicationPanicStopsCallbacks preserves application panic identity
// and prevents a later callback from running after a failed edit.
func TestSessionApplicationPanicStopsCallbacks(t *testing.T) {
	t.Parallel()

	reached := false
	defer func() {
		if value := recover(); value != "session policy" || reached {
			t.Fatalf("panic = %v, later callback = %v", value, reached)
		}
	}()
	_ = auth.NewSessionIdentity(loadByID, func(authenticator *auth.SessionBuilder) {
		panic("session policy")
	}, func(authenticator *auth.SessionBuilder) {
		reached = true
	})
}
