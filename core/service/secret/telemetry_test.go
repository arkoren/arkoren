package secret_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/secret"
	"arkoren.dev/core/service/secret/memorysecret"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// observedStore wires real memory storage to recording telemetry and an event
// bus, leaving each test free to subscribe to precisely the outcomes it needs.
func observedStore(t *testing.T) (*secret.Store, *telemetrytest.Fake, *event.Bus) {
	t.Helper()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	bus := event.New()
	store := secret.New(memorysecret.New(), func(secrets *secret.Builder) {
		secrets.Telemetry(service).Events(bus)
	})

	return store, fake, bus
}

// TestTypedReadsObserveProviderOnce protects the Raw/String/Get delegation
// boundary, including a JSON decode error after a successful provider read.
func TestTypedReadsObserveProviderOnce(t *testing.T) {
	t.Parallel()

	store, fake, _ := observedStore(t)

	if _, err := store.Driver().Put(t.Context(), "sensitive-name", []byte("invalid-json")); err != nil {
		t.Fatal(err)
	}

	if _, err := store.String(t.Context(), "sensitive-name"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(t.Context(), secret.NewSecret[map[string]string]("sensitive-name")); err == nil {
		t.Fatal("expected JSON decode error")
	}

	spans := fake.Spans()

	if len(spans) != 2 {
		t.Fatalf("provider reads = %d, want 2", len(spans))
	}

	for _, span := range spans {
		if span.Name != "secret get" || span.Status == telemetry.Error {
			t.Fatalf("unexpected provider outcome: %+v", span)
		}
	}
}

// TestTypedReadMetricsCountProviderOnce checks both metric instruments count
// the provider call once, independently of the facade's string/JSON decoding.
func TestTypedReadMetricsCountProviderOnce(t *testing.T) {
	t.Parallel()

	store, fake, _ := observedStore(t)
	store.Driver().Put(t.Context(), "private", []byte(`{"password":"fixture"}`))

	if _, err := store.String(t.Context(), "private"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(t.Context(), secret.NewSecret[map[string]string]("private")); err != nil {
		t.Fatal(err)
	}

	if len(fake.Metrics()) != 2 {
		t.Fatalf("expected count and duration: %+v", fake.Metrics())
	}

	for _, metric := range fake.Metrics() {
		point, ok := metric.Point("get", "")

		if !ok || point.Count != 2 {
			t.Fatalf("duplicate or missing provider observations: %+v", metric)
		}
	}
}

// leakingDriver embeds the contract so only Put needs a provider-specific
// substitute. It tests redaction without inventing a new driver capability.
type leakingDriver struct {
	secret.Driver
	err error
}

// Put simulates a provider error containing sensitive native details.
func (driver leakingDriver) Put(context.Context, string, []byte) (secret.Value, error) {
	return secret.Value{}, driver.err
}

// TestMutationRedactsNativeFailure verifies both channels omit names, payloads,
// versions and native error text, while the caller retains exact error identity.
func TestMutationRedactsNativeFailure(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	bus := event.New()
	var outcome secret.MutationEvent
	bus.Listen(secret.MutationFailed, func(_ context.Context, received secret.MutationEvent) error {
		outcome = received

		return nil
	})

	cause := errors.New("native-credential native-version native-payload")
	store := secret.New(leakingDriver{Driver: memorysecret.New(), err: cause}, func(secrets *secret.Builder) {
		secrets.Telemetry(service).Events(bus)
	})

	_, err := store.Put(t.Context(), "native-name", []byte("native-payload"))

	if !errors.Is(err, cause) || outcome.ErrorType != "other" {
		t.Fatalf("error = %v, outcome = %+v", err, outcome)
	}

	recorded := fmt.Sprintf("%+v %+v %+v", fake.Spans(), fake.Metrics(), outcome)

	if strings.Contains(recorded, "native-") {
		t.Fatalf("sensitive observation: %s", recorded)
	}

	if len(fake.Spans()[0].Events) != 0 {
		t.Fatal("native error must not be recorded as an exception")
	}
}

// TestDefaultTelemetryResolvesPerCall must not run in parallel: it changes
// process-global default telemetry and restores the previous service.
func TestDefaultTelemetryResolvesPerCall(t *testing.T) {
	previous := telemetry.Default()
	t.Cleanup(func() { telemetry.SetDefault(previous) })

	telemetry.SetDefault(nil)

	store := secret.New(memorysecret.New())
	store.Raw(t.Context(), "missing")

	first := telemetrytest.New()
	telemetry.SetDefault(first.Telemetry(t))
	store.Raw(t.Context(), "missing")

	second := telemetrytest.New()
	telemetry.SetDefault(second.Telemetry(t))
	store.Raw(t.Context(), "missing")

	if len(first.Spans()) != 1 || len(second.Spans()) != 1 {
		t.Fatalf("default spans = %d, %d", len(first.Spans()), len(second.Spans()))
	}
}

// TestDeadlineMutationPreservesCause distinguishes deadlines from cancellation
// without a real-time sleep or provider write.
func TestDeadlineMutationPreservesCause(t *testing.T) {
	t.Parallel()

	store, fake, _ := observedStore(t)
	ctx, cancel := context.WithDeadline(t.Context(), time.Time{})
	t.Cleanup(cancel)

	if err := store.Delete(ctx, "private"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delete error = %v", err)
	}

	if fake.Spans()[0].StatusDescription != "context.DeadlineExceeded" {
		t.Fatalf("deadline class = %+v", fake.Spans())
	}
}

// TestDriverBypassHasNoObservations makes the facade boundary explicit: even
// version-pinned reads and writes through Driver produce no facade telemetry.
func TestDriverBypassHasNoObservations(t *testing.T) {
	t.Parallel()

	store, fake, bus := observedStore(t)
	bus.Listen(secret.MutationSucceeded, func(context.Context, secret.MutationEvent) error {
		t.Fatal("raw driver calls must not publish facade events")

		return nil
	})

	written, err := store.Driver().Put(t.Context(), "private", []byte("value"))

	if err != nil {
		t.Fatal(err)
	}

	value, err := store.Driver().Get(t.Context(), secret.Reference{Name: "private", Version: written.Version})

	if err != nil || string(value.Data) != "value" {
		t.Fatalf("versioned driver read = %+v, %v", value, err)
	}

	if len(fake.Spans()) != 0 || len(fake.Metrics()) != 0 {
		t.Fatalf("raw driver calls emitted facade telemetry: %+v %+v", fake.Spans(), fake.Metrics())
	}
}
