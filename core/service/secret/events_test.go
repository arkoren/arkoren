package secret_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/memorysecret"
	"arkoren.dev/core/service/telemetry"
)

// TestMutationNotificationsCoverFacadeOutcomes checks every facade mutation
// against memory-driver semantics and ensures raw access publishes nothing.
func TestMutationNotificationsCoverFacadeOutcomes(t *testing.T) {
	t.Parallel()

	store, _, bus := observedStore(t)
	var outcomes []secret.MutationEvent
	bus.Listen(secret.MutationSucceeded, func(_ context.Context, outcome secret.MutationEvent) error {
		outcomes = append(outcomes, outcome)

		return nil
	})
	bus.Listen(secret.MutationFailed, func(_ context.Context, outcome secret.MutationEvent) error {
		outcomes = append(outcomes, outcome)

		return nil
	})

	first, err := store.Put(t.Context(), "private", []byte("payload"))

	if err != nil {
		t.Fatal(err)
	}

	second, err := store.Update(t.Context(), "private", []byte("replacement"))

	if err != nil || first.Version == second.Version {
		t.Fatalf("update = %+v, %v", second, err)
	}

	if err := store.Delete(t.Context(), "private"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Update(t.Context(), "private", nil); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("missing update = %v", err)
	}

	if err := store.Delete(t.Context(), "private"); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("missing delete = %v", err)
	}

	store.Driver().Put(t.Context(), "raw-bypass", nil)

	want := []secret.MutationEvent{{Operation: "put"}, {Operation: "update"}, {Operation: "delete"},
		{Operation: "update", ErrorType: "secret.ErrNotFound"}, {Operation: "delete", ErrorType: "secret.ErrNotFound"}}

	if !reflect.DeepEqual(outcomes, want) {
		t.Fatalf("outcomes = %+v, want %+v", outcomes, want)
	}
}

// TestCanceledMutationPreservesCause checks notification delivery with an
// already canceled operation context and the driver's no-change guarantee.
func TestCanceledMutationPreservesCause(t *testing.T) {
	t.Parallel()

	store, fake, bus := observedStore(t)
	var outcome secret.MutationEvent
	bus.Listen(secret.MutationFailed, func(_ context.Context, received secret.MutationEvent) error {
		outcome = received

		return nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := store.Put(ctx, "private", []byte("value")); !errors.Is(err, context.Canceled) {
		t.Fatalf("put error = %v", err)
	}

	if outcome.ErrorType != "context.Canceled" {
		t.Fatalf("outcome = %+v", outcome)
	}

	if _, err := store.Driver().Get(t.Context(), secret.Reference{Name: "private"}); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("canceled put changed state: %v", err)
	}

	if fake.Spans()[0].Status != telemetry.Error {
		t.Fatal("canceled mutation must record failure")
	}
}

// TestNilEventSettingDisablesNotifications checks last-setting-wins behavior
// without relying on the process-wide default telemetry service.
func TestNilEventSettingDisablesNotifications(t *testing.T) {
	t.Parallel()

	bus := event.New()
	bus.Listen(secret.MutationSucceeded, func(context.Context, secret.MutationEvent) error {
		t.Fatal("nil events must disable notifications")

		return nil
	})

	store := secret.New(memorysecret.New(), func(secrets *secret.Builder) {
		secrets.Events(bus).Events(nil)
	})

	if _, err := store.Put(t.Context(), "private", nil); err != nil {
		t.Fatal(err)
	}
}

// TestReadFailureDoesNotPublishMutation checks that failed reads retain the
// missing-secret sentinel and produce telemetry, not mutation event noise.
func TestReadFailureDoesNotPublishMutation(t *testing.T) {
	t.Parallel()

	store, fake, bus := observedStore(t)
	bus.Listen(secret.MutationFailed, func(context.Context, secret.MutationEvent) error {
		t.Fatal("reads must not publish mutation failures")

		return nil
	})

	if _, err := store.Raw(t.Context(), "private-missing"); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("raw error = %v", err)
	}

	span := fake.Spans()[0]

	if span.StatusDescription != "secret.ErrNotFound" || span.Status != telemetry.Error {
		t.Fatalf("read failure = %+v", span)
	}
}

// TestListenerPanicDoesNotChangeMutationResult protects best-effort outcome
// publication even when application listener code panics.
func TestListenerPanicDoesNotChangeMutationResult(t *testing.T) {
	t.Parallel()

	store, _, bus := observedStore(t)
	bus.Listen(secret.MutationSucceeded, func(context.Context, secret.MutationEvent) error {
		panic("listener panic")
	})

	if _, err := store.Put(t.Context(), "private", []byte("value")); err != nil {
		t.Fatal(err)
	}

	value, err := store.String(t.Context(), "private")

	if err != nil || value != "value" {
		t.Fatalf("mutation result changed: %q, %v", value, err)
	}
}
