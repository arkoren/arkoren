package secret_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/memorysecret"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestBuilderPublicationDetachesDraft verifies ordered once-only policies,
// shared services, and isolation from later edits of a retained draft.
func TestBuilderPublicationDetachesDraft(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	bus := event.New()
	var retained *secret.Builder
	calls := 0
	notifications := 0
	bus.Listen(secret.MutationSucceeded, func(context.Context, secret.MutationEvent) error {
		notifications++

		return errors.New("listener failure cannot replace result")
	})

	store := secret.New(memorysecret.New(), func(secrets *secret.Builder) {
		calls++
		retained = secrets
		secrets.Telemetry(service).Events(bus)
	}, func(secrets *secret.Builder) {
		calls++
	})
	retained.Telemetry(nil).Events(nil)

	if _, err := store.Put(t.Context(), "private", nil); err != nil {
		t.Fatal(err)
	}

	if calls != 2 || notifications != 1 || len(fake.Spans()) != 1 {
		t.Fatalf("calls=%d notifications=%d spans=%d", calls, notifications, len(fake.Spans()))
	}
}

// TestNilConstructionCallbackPanics preserves direct callback misuse semantics
// instead of silently discarding a supplied nil policy.
func TestNilConstructionCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback must panic")
		}
	}()
	secret.New(memorysecret.New(), nil)
}
