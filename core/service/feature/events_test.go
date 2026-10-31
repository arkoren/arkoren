package feature_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/memoryfeature"
)

// listenEvaluationFailed returns the payloads published on bus so far.
func listenEvaluationFailed(t *testing.T, bus *event.Bus) *[]feature.EvaluationFailedEvent {
	t.Helper()

	var got []feature.EvaluationFailedEvent

	if _, err := bus.Listen(feature.EvaluationFailed, func(_ context.Context, payload feature.EvaluationFailedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return &got
}

// TestValuePublishesEvaluationFailed checks driver failures emit a redacted outcome event.
func TestValuePublishesEvaluationFailed(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenEvaluationFailed(t, bus)
	flags := feature.New(memoryfeature.New(), func(flags *feature.Builder) {
		flags.Events(bus)
	})

	value, err := flags.Value(t.Context(), feature.NewFlag("missing", "fallback"), feature.Subject{Key: "user-1"})

	if !errors.Is(err, feature.ErrNotFound) || value != "fallback" {
		t.Fatalf("Value = %q, %v", value, err)
	}

	if len(*got) != 1 || (*got)[0].Key != "missing" || !errors.Is((*got)[0].Err, feature.ErrNotFound) {
		t.Fatalf("EvaluationFailed = %+v", *got)
	}
}

// TestValuePublishesNothingOnSuccess verifies successful evaluations emit no failure event.
func TestValuePublishesNothingOnSuccess(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenEvaluationFailed(t, bus)
	driver := memoryfeature.New()
	definition := feature.NewFlag("flag", false)

	if err := driver.Set(definition, true); err != nil {
		t.Fatal(err)
	}

	flags := feature.New(driver, func(flags *feature.Builder) {
		flags.Events(bus)
	})

	if value, err := flags.Value(t.Context(), definition, feature.Subject{}); err != nil || !value {
		t.Fatalf("Value = %v, %v", value, err)
	}

	if len(*got) != 0 {
		t.Fatalf("EvaluationFailed = %+v; want none", *got)
	}
}

// TestValuePublishesNothingForInvalidSubject checks pre-driver validation emits no outcome event.
func TestValuePublishesNothingForInvalidSubject(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenEvaluationFailed(t, bus)
	flags := feature.New(memoryfeature.New(), func(flags *feature.Builder) {
		flags.Events(bus)
	})

	subject := feature.Subject{Attributes: map[string]any{"bad": struct{}{}}}

	if _, err := flags.Value(t.Context(), feature.NewFlag("flag", false), subject); !errors.Is(err, feature.ErrInvalidSubject) {
		t.Fatalf("Value error = %v; want ErrInvalidSubject", err)
	}

	if len(*got) != 0 {
		t.Fatalf("EvaluationFailed = %+v; want none", *got)
	}
}

// TestValueWithoutBusPublishesNothing checks evaluation errors are safe without event wiring.
func TestValueWithoutBusPublishesNothing(t *testing.T) {
	t.Parallel()

	got := listenEvaluationFailed(t, event.New())
	flags := feature.New(memoryfeature.New())

	if _, err := flags.Value(t.Context(), feature.NewFlag("missing", false), feature.Subject{}); !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf("Value error = %v", err)
	}

	if len(*got) != 0 {
		t.Fatalf("EvaluationFailed = %+v; want none", *got)
	}
}

// TestInvalidFlagPublishesNothing checks invalid application definitions do
// not emit provider-failure events because the driver was never called.
func TestInvalidFlagPublishesNothing(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := listenEvaluationFailed(t, bus)
	flags := feature.New(memoryfeature.New(), func(flags *feature.Builder) {
		flags.Events(bus)
	})

	if _, err := flags.Value(t.Context(), feature.NewFlag("", false), feature.Subject{}); !errors.Is(err, feature.ErrInvalidFlag) {
		t.Fatalf("Value error = %v; want ErrInvalidFlag", err)
	}

	if len(*got) != 0 {
		t.Fatalf("events = %+v; want none", *got)
	}
}
