package gate_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/auth/gate"
	"arkoren.dev/core/http/router"
)

// listenDenied returns the payloads published on bus so far.
func listenDenied(t *testing.T, bus *event.Bus) *[]gate.DeniedEvent {
	t.Helper()

	var got []gate.DeniedEvent

	if _, err := bus.Listen(gate.Denied, func(_ context.Context, payload gate.DeniedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return &got
}

// allow is a rule that always allows.
func allow(context.Context, user) (bool, error) {
	return true, nil
}

// next is a handler that succeeds.
func next(http.ResponseWriter, *http.Request) error {
	return nil
}

// TestGateRequirePublishesDenied checks that request authorization publishes
// the denied rule and method without the request location or query string.
func TestGateRequirePublishesDenied(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenDenied(t, bus)
	handler := gate.New(func(access *gate.Builder) {
		access.Events(bus)
	}).Require(allow, isAdmin)

	r := httptest.NewRequest(http.MethodDelete, "/admin?token=secret", nil)

	err := handler(router.Handler(next))(httptest.NewRecorder(), r.WithContext(auth.NewContext(r.Context(), user{})))

	if !hasStatus(err, http.StatusForbidden) {
		t.Fatalf("err = %v", err)
	}

	if len(*got) != 1 || (*got)[0] != (gate.DeniedEvent{
		Method: http.MethodDelete,
		Rule:   1,
	}) {
		t.Fatalf("Denied = %+v", *got)
	}
}

// TestGateDeniedEventOmitsRequestIdentity checks the public denial event leaves
// resource identifiers out while retaining the method and denying rule index.
func TestGateDeniedEventOmitsRequestIdentity(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenDenied(t, bus)
	policy := gate.New(func(access *gate.Builder) {
		access.Events(bus)
	})

	routes := router.New()
	routes.Use(auth.Middleware(func(*http.Request) (user, error) {
		return user{}, nil
	}), policy.Require(allow, isAdmin))
	routes.Delete("/users/{identity}", next)

	response := routes.Record(httptest.NewRequest(http.MethodDelete, "/users/private-identity?token=private-token", nil))
	t.Cleanup(func() {
		response.Body.Close()
	})

	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", response.StatusCode)
	}

	if len(*got) != 1 {
		t.Fatalf("events = %d; want one", len(*got))
	}

	if text := fmt.Sprintf("%+v", (*got)[0]); text != "{Method:DELETE Rule:1}" {
		t.Fatalf("event exposed request metadata: %s", text)
	}
}

// TestGateAuthorizePublishesDeniedWithoutRequest checks that direct
// authorization emits a denial without inventing HTTP request metadata.
func TestGateAuthorizePublishesDeniedWithoutRequest(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenDenied(t, bus)
	ctx := auth.NewContext(t.Context(), user{})

	if err := gate.New(func(access *gate.Builder) {
		access.Events(bus)
	}).Authorize(ctx, isAdmin); !errors.Is(err, gate.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}

	if len(*got) != 1 || (*got)[0] != (gate.DeniedEvent{
		Rule: 0,
	}) {
		t.Fatalf("Denied = %+v", *got)
	}
}

// TestGatePublishesNothingWhenAllowed checks that a successful rule evaluation
// produces no denial event.
func TestGatePublishesNothingWhenAllowed(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenDenied(t, bus)
	ctx := auth.NewContext(t.Context(), user{
		Admin: true,
	})

	if err := gate.New(func(access *gate.Builder) {
		access.Events(bus)
	}).Authorize(ctx, isAdmin); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 0 {
		t.Fatalf("Denied = %+v; want none", *got)
	}
}

// TestGatePublishesNothingWithoutUser checks that missing authentication does
// not masquerade as a rule-based authorization denial.
func TestGatePublishesNothingWithoutUser(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenDenied(t, bus)

	if err := gate.New(func(access *gate.Builder) {
		access.Events(bus)
	}).Authorize(t.Context(), isAdmin); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}

	if len(*got) != 0 {
		t.Fatalf("Denied = %+v; want none", *got)
	}
}

// TestGatePublishesNothingWhenRuleFails checks that a rule execution error
// propagates without emitting a normal denial event.
func TestGatePublishesNothingWhenRuleFails(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenDenied(t, bus)
	boom := errors.New("boom")
	failing := func(context.Context, user) (bool, error) {
		return false, boom
	}

	ctx := auth.NewContext(t.Context(), user{})

	if err := gate.New(func(access *gate.Builder) {
		access.Events(bus)
	}).Authorize(ctx, failing); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	if len(*got) != 0 {
		t.Fatalf("Denied = %+v; want none", *got)
	}
}

// TestGateWithoutBusPublishesNothing checks that authorization without an
// explicit event bus leaves an unrelated bus untouched.
func TestGateWithoutBusPublishesNothing(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenDenied(t, bus)
	ctx := auth.NewContext(t.Context(), user{})

	if err := gate.New().Authorize(ctx, isAdmin); !errors.Is(err, gate.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}

	if len(*got) != 0 {
		t.Fatalf("Denied = %+v; want none", *got)
	}
}

// TestGateRequireWithoutRulesPanics checks that middleware rejects an empty
// rule list at construction rather than allowing requests silently.
func TestGateRequireWithoutRulesPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("Require without rules did not panic")
		}
	}()

	gate.New().Require[user]()
}
