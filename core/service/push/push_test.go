package push_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/push"
	"arkoren.dev/core/service/push/pushtest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

func Example() {
	fake := pushtest.New()
	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.TTL(time.Hour)
	})

	message := push.NewMessage(push.Token("device-registration"), "Order ready",
		func(notification *push.MessageBuilder) {
			notification.Body("Collect your order.").Data(map[string]string{"order": "42"})
		})

	receipt, err := sender.Send(context.Background(), message, push.TTL(-1))

	if err != nil {
		fmt.Println("submission failed")

		return
	}

	fmt.Println(receipt.ID)
	fmt.Println(fake.Sent()[0].Config.TTL)
	// Output:
	// fake-1
	// 0s
}

func ExampleCheckSend() {
	message := push.NewMessage(push.Token("native"), "Ready")

	err := push.CheckSend(context.Background(), message, push.SendConfig{})
	fmt.Println(err)
	// Output: <nil>
}

// TestSenderTelemetryKeepsUnknownAcceptanceAheadOfCancellation observes the
// public span, metrics, event, and returned causes for one uncertain submission.
func TestSenderTelemetryKeepsUnknownAcceptanceAheadOfCancellation(t *testing.T) {
	t.Parallel()

	traces := telemetrytest.New()
	bus := event.New()
	var observed push.SubmissionEvent

	stop, err := bus.Listen(push.Failed, func(_ context.Context, payload push.SubmissionEvent) error {
		observed = payload

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stop)

	cause := errors.New("private-provider-diagnostic")
	fake := pushtest.New()
	fake.FailNext(errors.Join(context.Canceled, push.ErrUnavailable, cause, push.ErrOutcomeUnknown))

	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.Telemetry(traces.Telemetry(t))
		delivery.Events(bus)
	})

	receipt, err := sender.Send(t.Context(), push.NewMessage(push.Token("private-token"), "private-title"))

	for _, target := range []error{context.Canceled, push.ErrUnavailable, cause, push.ErrOutcomeUnknown} {
		if !errors.Is(err, target) {
			t.Fatalf("Send lost cause %v", target)
		}
	}

	if receipt != (push.Receipt{}) || observed.ErrorClass != "outcome_unknown" {
		t.Fatalf("receipt = %v, event = %+v", receipt, observed)
	}

	traces.AssertSpan(t, "push send", telemetry.String("error.type", "push.ErrOutcomeUnknown"))
	traces.AssertCounter(t, "arkoren.push.operations", 1, telemetry.String("error.type", "push.ErrOutcomeUnknown"))
	traces.AssertHistogram(t, "arkoren.push.operation.duration", 1, telemetry.String("error.type", "push.ErrOutcomeUnknown"))

	if strings.Contains(fmt.Sprint(traces.Spans(), traces.Metrics(), observed), "private-") {
		t.Fatal("submission diagnostics disclosed private data")
	}
}

// TestSenderTelemetryKeepsUnknownAcceptanceAheadOfDeadline preserves uncertainty
// when a dispatched submission outlives its deadline, without losing either cause.
func TestSenderTelemetryKeepsUnknownAcceptanceAheadOfDeadline(t *testing.T) {
	t.Parallel()

	traces := telemetrytest.New()
	fake := pushtest.New()
	fake.FailNext(errors.Join(context.DeadlineExceeded, push.ErrOutcomeUnknown))

	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.Telemetry(traces.Telemetry(t))
	})

	_, err := sender.Send(t.Context(), push.NewMessage(push.Token("native-token"), "Ready"))

	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, push.ErrOutcomeUnknown) {
		t.Fatalf("Send lost deadline or uncertainty: %v", err)
	}

	traces.AssertSpan(t, "push send", telemetry.String("error.type", "push.ErrOutcomeUnknown"))
	traces.AssertCounter(t, "arkoren.push.operations", 1, telemetry.String("error.type", "push.ErrOutcomeUnknown"))
	traces.AssertHistogram(t, "arkoren.push.operation.duration", 1, telemetry.String("error.type", "push.ErrOutcomeUnknown"))
}

func TestSenderTelemetryRecordsOnlyBoundedOperation(t *testing.T) {
	t.Parallel()

	traces := telemetrytest.New()
	instrumentation := traces.Telemetry(t)
	fake := pushtest.New()
	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.Telemetry(instrumentation)
	})

	_, err := sender.Send(t.Context(), push.NewMessage(browserSubscription(t), "private-title",
		func(notification *push.MessageBuilder) {
			notification.Body("private-body").Data(map[string]string{"private-key": "private-data"})
		}))

	if err != nil {
		t.Fatal(err)
	}

	traces.AssertSpan(t, "push send", telemetry.String("arkoren.push.operation", "send"))
	traces.AssertSpanCount(t, 1)
	traces.AssertCounter(t, "arkoren.push.operations", 1, telemetry.String("arkoren.push.operation", "send"))
	traces.AssertHistogram(t, "arkoren.push.operation.duration", 1, telemetry.String("arkoren.push.operation", "send"))

	span := traces.Spans()[0]

	if span.Kind != telemetry.Client || len(span.Attrs) != 1 || len(span.Events) != 0 {
		t.Fatal("unexpected telemetry shape")
	}

	recorded := fmt.Sprintf("%+v %+v", traces.Spans(), traces.Metrics())
	registration := browserSubscription(t)

	for _, secret := range []string{
		registration.Endpoint, registration.Keys.Auth, registration.Keys.P256DH,
		"private-title", "private-body", "private-key", "private-data", "fake-1",
	} {
		if strings.Contains(recorded, secret) {
			t.Fatal("sensitive content entered telemetry")
		}
	}
}

func TestSenderTelemetryClassifiesWithoutErrorTextOrToken(t *testing.T) {
	t.Parallel()

	traces := telemetrytest.New()
	instrumentation := traces.Telemetry(t)
	fake := pushtest.New()
	cause := errors.New("private-provider-error-text")
	fake.FailNext(errors.Join(push.ErrUnavailable, cause))

	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.Telemetry(instrumentation)
	})

	_, err := sender.Send(t.Context(), push.NewMessage(push.Token("private-native-token"), "private-title"))

	if !errors.Is(err, cause) {
		t.Fatal("driver cause not preserved")
	}

	traces.AssertSpan(t, "push send", telemetry.String("error.type", "push.ErrUnavailable"))
	traces.AssertCounter(t, "arkoren.push.operations", 1,
		telemetry.String("arkoren.push.operation", "send"), telemetry.String("error.type", "push.ErrUnavailable"))

	recorded := fmt.Sprintf("%+v %+v", traces.Spans(), traces.Metrics())

	for _, secret := range []string{"private-provider-error-text", "private-native-token", "private-title"} {
		if strings.Contains(recorded, secret) {
			t.Fatal("error or token entered telemetry")
		}
	}
}

func TestSenderInvalidInputDoesNotEmitTelemetry(t *testing.T) {
	t.Parallel()

	traces := telemetrytest.New()
	instrumentation := traces.Telemetry(t)
	sender := push.New(pushtest.New(), func(delivery *push.Builder) {
		delivery.Telemetry(instrumentation)
	})

	if _, err := sender.Send(t.Context(), push.Message{}); !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatalf("invalid input error = %v", err)
	}

	traces.AssertSpanCount(t, 0)

	if len(traces.Metrics()) != 0 {
		t.Fatal("invalid input allocated telemetry instruments")
	}
}

func TestSenderResolvesTelemetryDefaultOnEveryCall(t *testing.T) {
	previous := telemetry.Default()
	t.Cleanup(func() {
		telemetry.SetDefault(previous)
	})

	telemetry.SetDefault(nil)

	sender := push.New(pushtest.New())
	message := push.NewMessage(push.Token("native"), "Ready")

	if _, err := sender.Send(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	first := telemetrytest.New()
	telemetry.SetDefault(first.Telemetry(t))

	if _, err := sender.Send(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	second := telemetrytest.New()
	telemetry.SetDefault(second.Telemetry(t))

	if _, err := sender.Send(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	telemetry.SetDefault(nil)

	if _, err := sender.Send(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	first.AssertSpanCount(t, 1)
	second.AssertSpanCount(t, 1)
}

func TestSenderExplicitOffOverridesEnabledDefault(t *testing.T) {
	previous := telemetry.Default()
	t.Cleanup(func() {
		telemetry.SetDefault(previous)
	})

	traces := telemetrytest.New()
	telemetry.SetDefault(traces.Telemetry(t))

	sender := push.New(pushtest.New(), func(delivery *push.Builder) {
		delivery.Telemetry(telemetry.New(nil))
	})

	if _, err := sender.Send(t.Context(), push.NewMessage(push.Token("native"), "Ready")); err != nil {
		t.Fatal(err)
	}

	traces.AssertSpanCount(t, 0)
}

func TestSenderUsesDefaultTTL(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()

	if _, err := push.New(fake).Send(t.Context(), push.NewMessage(push.Token("native"), "Ready")); err != nil {
		t.Fatal(err)
	}

	if fake.Sent()[0].Config.TTL != 24*time.Hour {
		t.Fatal("default TTL not applied")
	}
}

func TestSenderZeroTTLInheritsConfiguredDefault(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.TTL(2 * time.Hour)
	})

	if _, err := sender.Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"), push.TTL(0)); err != nil {
		t.Fatal(err)
	}

	if fake.Sent()[0].Config.TTL != 2*time.Hour {
		t.Fatal("per-call zero failed to inherit configured TTL")
	}
}

func TestSenderBuilderZeroResetsDefaultTTL(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.TTL(time.Hour).TTL(0)
	})

	if _, err := sender.Send(t.Context(), push.NewMessage(push.Token("native"), "Ready")); err != nil {
		t.Fatal(err)
	}

	if fake.Sent()[0].Config.TTL != push.DefaultTTL {
		t.Fatal("construction zero failed to restore default")
	}
}

func TestSenderNegativeBuilderTTLDisablesRetention(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.TTL(-time.Hour)
	})

	if _, err := sender.Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"), push.TTL(0)); err != nil {
		t.Fatal(err)
	}

	if fake.Sent()[0].Config.TTL != 0 {
		t.Fatal("negative default did not resolve to no-store")
	}
}

func TestSenderPositiveOptionOverridesNoStoreDefault(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.TTL(-1)
	})

	_, err := sender.Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"), push.TTL(time.Hour))

	if err != nil {
		t.Fatal(err)
	}

	if fake.Sent()[0].Config.TTL != time.Hour {
		t.Fatal("positive override ignored")
	}
}

func TestSenderNegativeOptionResolvesNoStore(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()

	_, err := push.New(fake).Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"), push.TTL(-1))

	if err != nil {
		t.Fatal(err)
	}

	if fake.Sent()[0].Config.TTL != 0 {
		t.Fatal("negative option did not resolve to no-store")
	}
}

func TestSenderLastOptionWinsWithoutGlobalTTLRestrictions(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	ttl := 40*24*time.Hour + time.Nanosecond

	_, err := push.New(fake).Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"),
		push.TTL(-1), push.TTL(ttl))

	if err != nil {
		t.Fatal(err)
	}

	if fake.Sent()[0].Config.TTL != ttl {
		t.Fatal("portable policy was clamped or rounded")
	}
}

func TestSenderPublishesDetachedDraftAndRunsCallbacksOnce(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	var retained *push.Builder
	var calls int
	policies := []func(*push.Builder){
		func(delivery *push.Builder) {
			retained = delivery
			calls++
			delivery.TTL(time.Hour)
		},
		func(delivery *push.Builder) {
			calls++

			if retained != delivery {
				t.Fatal("callbacks did not share a draft")
			}

			delivery.TTL(2 * time.Hour)
		},
	}

	sender := push.New(fake, policies...)
	retained.TTL(-1).Telemetry(telemetry.New(nil))

	if _, err := sender.Send(t.Context(), push.NewMessage(push.Token("native"), "Ready")); err != nil {
		t.Fatal(err)
	}

	if calls != 2 || fake.Sent()[0].Config.TTL != 2*time.Hour {
		t.Fatal("published sender followed retained draft or replayed callbacks")
	}
}

func TestSenderConstructorAndMethodsHaveExactAliasTypes(t *testing.T) {
	t.Parallel()

	constructor := (func(push.Driver, ...func(*push.Builder)) *push.Sender)(push.New)
	alias := push.BuilderFunc(func(delivery *push.Builder) {
		retention := (func(time.Duration) *push.Builder)(delivery.TTL)
		instrumentation := (func(*telemetry.Telemetry) *push.Builder)(delivery.Telemetry)

		if retention(time.Hour) != delivery || instrumentation(nil) != delivery {
			t.Fatal("setting did not return same draft")
		}
	})

	policies := ([]func(*push.Builder))([]push.BuilderFunc{alias})

	sender := constructor(pushtest.New(), policies...)
	send := (func(context.Context, push.Message, ...push.SendOption) (push.Receipt, error))(sender.Send)

	if _, err := send(t.Context(), push.NewMessage(push.Token("native"), "Ready")); err != nil {
		t.Fatal(err)
	}

	var omitted []push.BuilderFunc
	_ = constructor(pushtest.New(), omitted...)
}

func TestSenderRejectsNilDriver(t *testing.T) {
	t.Parallel()

	receipt, err := push.New(nil).Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"))

	if !errors.Is(err, push.ErrInvalidMessage) || receipt != (push.Receipt{}) {
		t.Fatalf("nil driver result = %v, %v", receipt, err)
	}
}

func TestSenderRejectsInvalidMessageBeforeDriver(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()

	receipt, err := push.New(fake).Send(t.Context(), push.Message{})

	if !errors.Is(err, push.ErrInvalidMessage) || receipt != (push.Receipt{}) {
		t.Fatalf("invalid message result = %v, %v", receipt, err)
	}

	fake.AssertNothingSent(t)
}

func TestSenderRejectsNilContext(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	var ctx context.Context

	receipt, err := push.New(fake).Send(ctx, push.NewMessage(push.Token("native"), "Ready"))

	if !errors.Is(err, push.ErrInvalidMessage) || receipt != (push.Receipt{}) {
		t.Fatalf("nil context result = %v, %v", receipt, err)
	}

	fake.AssertNothingSent(t)
}

func TestCheckSendPrioritizesContextOverInvalidDeclaration(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := push.CheckSend(ctx, push.Message{}, push.SendConfig{TTL: -1})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("preflight error = %v", err)
	}
}

func TestSenderPreservesErrorIdentityAndDoesNotRetry(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	cause := errors.New("native failure")
	failure := errors.Join(push.ErrOutcomeUnknown, cause, context.Canceled)
	fake.FailNext(failure)

	receipt, err := push.New(fake).Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"))

	if !errors.Is(err, cause) || !errors.Is(err, push.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error identities lost: %v", err)
	}

	if receipt != (push.Receipt{}) {
		t.Fatal("failure returned nonzero receipt")
	}

	fake.AssertNothingSent(t)
}

// fixedDriver scripts a raw response to verify facade boundary behavior for
// optional IDs and incorrectly nonzero receipts accompanying backend errors.
type fixedDriver struct {
	// receipt is the raw return value, including deliberately invalid replies.
	receipt push.Receipt

	// err is returned unchanged for error-identity checks.
	err error
}

// Send returns scripted values without I/O or mutation; input validation is
// exercised separately against real preflight and the outbound fake.
func (driver fixedDriver) Send(context.Context, push.Message, push.SendConfig) (push.Receipt, error) {
	return driver.receipt, driver.err
}

func TestSenderDiscardsReceiptOnDriverError(t *testing.T) {
	t.Parallel()

	driver := fixedDriver{receipt: push.Receipt{ID: "must-not-escape"}, err: push.ErrUnavailable}

	receipt, err := push.New(driver).Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"))

	if !errors.Is(err, push.ErrUnavailable) || receipt != (push.Receipt{}) {
		t.Fatalf("failed result = %v, %v", receipt, err)
	}
}

func TestSenderAllowsSuccessfulReceiptWithoutID(t *testing.T) {
	t.Parallel()

	receipt, err := push.New(fixedDriver{}).Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"))

	if err != nil || receipt != (push.Receipt{}) {
		t.Fatalf("empty acceptance result = %v, %v", receipt, err)
	}
}

// wrappedDriver forwards the complete contract and exposes the underlying
// backend using the repository's standard typed Unwrap convention.
type wrappedDriver struct {
	push.Driver
}

// Unwrap reveals the wrapped backend without hiding the Driver contract.
func (driver wrappedDriver) Unwrap() push.Driver {
	return driver.Driver
}

func TestSenderDriverLookupTraversesWrappers(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	wrapper := wrappedDriver{Driver: fake}
	sender := push.New(wrapper)

	if sender.Driver() != wrapper {
		t.Fatal("Driver did not preserve original wrapper")
	}

	backend, ok := sender.DriverAs[*pushtest.Fake]()

	if !ok || backend != fake {
		t.Fatal("DriverAs failed to unwrap")
	}

	missing, ok := sender.DriverAs[*push.Sender]()

	if ok || missing != nil {
		t.Fatal("DriverAs returned nonexistent type")
	}
}

func TestSenderConcurrentSend(t *testing.T) {
	t.Parallel()

	fake := pushtest.New()
	sender := push.New(fake)
	message := push.NewMessage(push.Token("native"), "Ready")
	var workers sync.WaitGroup

	for range 8 {
		workers.Go(func() {
			if _, err := sender.Send(t.Context(), message, push.TTL(time.Hour)); err != nil {
				t.Error(err)
			}
		})
	}

	workers.Wait()

	fake.AssertSentCount(t, 8)
}

func TestSenderNilConstructionCallbackPanics(t *testing.T) {
	t.Parallel()

	expectPanic(t, func() {
		push.New(pushtest.New(), nil)
	})
}

func TestSenderNilSendOptionPanics(t *testing.T) {
	t.Parallel()

	expectPanic(t, func() {
		_, _ = push.New(pushtest.New()).Send(t.Context(), push.NewMessage(push.Token("native"), "Ready"), nil)
	})
}
