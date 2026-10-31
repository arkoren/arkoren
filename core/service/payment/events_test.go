package payment_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/paymenttest"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// observeOperations captures both operation topics synchronously. The bus does
// not start goroutines; tests own the returned slice and invoke calls serially.
func observeOperations(t *testing.T, bus *event.Bus) *[]payment.OperationEvent {
	t.Helper()

	observed := new([]payment.OperationEvent)

	for _, topic := range []event.Topic[payment.OperationEvent]{payment.OperationCompleted, payment.OperationFailed} {
		stop, err := bus.Listen(topic, func(_ context.Context, result payment.OperationEvent) error {
			*observed = append(*observed, result)

			return nil
		})

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(stop)
	}

	return observed
}

func TestPaymentEventsBuilderPublishesOnceOnlySnapshot(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	unused := event.New()
	other := observeOperations(t, unused)
	fake := paymenttest.New()
	fake.ReplyGetPayment(payment.Payment{Status: payment.StatusPending}, nil)
	var retained *payment.Builder
	calls := 0
	client := payment.New(fake, func(checkout *payment.Builder) {
		calls++
		retained = checkout

		if checkout.Events(unused).Events(bus) != checkout {
			t.Fatal("Events must return the same draft")
		}
	})
	retained.Events(unused)

	result, err := client.GetPayment(t.Context(), "private-id")

	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 || result.Status != payment.StatusPending {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}

	if len(*observed) != 1 || len(*other) != 0 {
		t.Fatalf("published=%v retained=%v", *observed, *other)
	}

	if got := (*observed)[0]; got != (payment.OperationEvent{Operation: "get_payment"}) {
		t.Fatalf("completion must report the read, not settlement: %+v", got)
	}
}

func TestPaymentEventsNilDisablesEarlierBus(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	fake := paymenttest.New()
	fake.ReplyGetPayment(payment.Payment{}, nil)

	client := payment.New(fake, func(checkout *payment.Builder) {
		checkout.Events(bus).Events(nil)
	})

	if _, err := client.GetPayment(t.Context(), "private-id"); err != nil {
		t.Fatal(err)
	}

	if len(*observed) != 0 {
		t.Fatalf("disabled notifications: %v", *observed)
	}
}

func TestPaymentEventsFailurePreservesResultAndEveryUnknownCause(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	cause := errors.New("private-native-error-token")
	want := payment.Payment{ID: "private-id", Status: payment.StatusPending}
	fake := paymenttest.New()
	fake.ReplyGetPayment(want, errors.Join(context.Canceled, payment.ErrUnavailable, cause, payment.ErrOutcomeUnknown))

	client := payment.New(fake, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})

	result, err := client.GetPayment(t.Context(), "private-id")

	for _, sentinel := range []error{cause, context.Canceled, payment.ErrUnavailable, payment.ErrOutcomeUnknown} {
		if !errors.Is(err, sentinel) {
			t.Fatalf("lost cause %v: %v", sentinel, err)
		}
	}

	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result changed: got %+v want %+v", result, want)
	}

	wantEvents := []payment.OperationEvent{{Operation: "get_payment", ErrorType: "payment.ErrOutcomeUnknown"}}

	if !slices.Equal(*observed, wantEvents) {
		t.Fatalf("unknown outcome must outrank joined causes: %v", *observed)
	}
}

func TestPaymentEventsRejectPreflightWithoutNotifications(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	policy := func(checkout *payment.Builder) {
		checkout.Events(bus)
	}

	fake := paymenttest.New()
	client := payment.New(fake, policy)

	if _, err := client.GetPayment(t.Context(), ""); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	// absent exercises the contract's explicit nil-context rejection.
	var absent context.Context

	if _, err := client.GetPayment(absent, "private-id"); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := client.GetPayment(ctx, "private-id"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if _, err := payment.New(nil, policy).GetPayment(t.Context(), "private-id"); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal(err)
	}

	if len(*observed) != 0 || len(fake.Attempted().ReadPayments) != 0 {
		t.Fatalf("rejected calls had effects: %v", *observed)
	}
}

func TestPaymentEventsListenerFailureCannotChangeResultOrContext(t *testing.T) {
	t.Parallel()

	bus := event.New(func(events *event.Builder) {
		events.Logger(slog.New(slog.DiscardHandler))
	})

	// marker is a private context key used to prove metadata reaches listeners.
	type marker struct{}
	ctx := context.WithValue(t.Context(), marker{}, "request-context")
	want := payment.Payment{ID: "private-id", Status: payment.StatusPending}
	driver := &paymentDriver{get: func(ctx context.Context, _ payment.ID) (payment.Payment, error) {
		if ctx.Value(marker{}) != "request-context" {
			t.Fatal("driver lost context")
		}

		return want, nil
	}}

	called := 0

	stop, err := bus.Listen(payment.OperationCompleted, func(ctx context.Context, _ payment.OperationEvent) error {
		called++

		if ctx.Value(marker{}) != "request-context" {
			t.Fatal("listener lost context")
		}

		return errors.New("listener failure")
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stop)

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})

	result, err := client.GetPayment(ctx, "private-id")

	if err != nil || !reflect.DeepEqual(result, want) {
		t.Fatalf("listener changed result: %+v, %v", result, err)
	}

	if called != 1 {
		t.Fatalf("listener calls=%d", called)
	}
}

func TestPaymentEventsListenerPanicCannotChangeError(t *testing.T) {
	t.Parallel()

	bus := event.New(func(events *event.Builder) {
		events.Logger(slog.New(slog.DiscardHandler))
	})

	called := 0

	stop, err := bus.Listen(payment.OperationFailed, func(context.Context, payment.OperationEvent) error {
		called++
		panic("listener panic")
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stop)

	fake := paymenttest.New()
	fake.ReplyGetPayment(payment.Payment{}, payment.ErrDeclined)

	client := payment.New(fake, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})

	if _, err := client.GetPayment(t.Context(), "private-id"); !errors.Is(err, payment.ErrDeclined) {
		t.Fatalf("listener changed API failure: %v", err)
	}

	if called != 1 {
		t.Fatalf("failure listener calls=%d", called)
	}
}

func TestPaymentEventsRedactPreparationSecretsAndNativeErrors(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	fake := paymenttest.New()
	fake.ReplyPreparePayment(payment.Preparation{
		PaymentID: "private-id",
		Action:    payment.NewClientAuthorization(payment.ClientProtocolStripePaymentIntent, "private-action-secret"),
	}, nil)
	fake.ReplyPreparePayment(payment.Preparation{}, errors.New("private-native-error"))

	client := payment.New(fake, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})

	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 123456, Currency: "USD"}, "private-key",
		func(details *payment.PreparePaymentRequestBuilder) {
			details.Description("private-description")
			details.Reference("private-reference")
			details.ReturnURL("https://private.example/callback")
			details.DeviceReference("private-device")
		})

	if _, err := client.PreparePayment(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	if _, err := client.PreparePayment(t.Context(), request); err == nil {
		t.Fatal("expected native failure")
	}

	want := []payment.OperationEvent{
		{Operation: "prepare_payment"},
		{Operation: "prepare_payment", ErrorType: "unknown"},
	}

	if !slices.Equal(*observed, want) {
		t.Fatalf("unexpected observations: %v", *observed)
	}

	if text := fmt.Sprintf("%+v", *observed); strings.Contains(text, "private") || strings.Contains(text, "123456") {
		t.Fatalf("leaked request, result, or error: %s", text)
	}
}

func TestPaymentEventsObserveAllReadOperations(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	fake := paymenttest.New()
	fake.ReplyGetCheckout(payment.Checkout{}, nil)
	fake.ReplyGetPayment(payment.Payment{}, nil)
	fake.ReplyGetRefund(payment.Refund{}, nil)
	fake.ReplyGetSubscription(payment.Subscription{}, nil)

	client := payment.New(fake, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})

	if _, err := client.GetCheckout(t.Context(), "private-checkout"); err != nil {
		t.Fatal(err)
	}

	if _, err := client.GetPayment(t.Context(), "private-payment"); err != nil {
		t.Fatal(err)
	}

	if _, err := client.GetRefund(t.Context(), "private-refund"); err != nil {
		t.Fatal(err)
	}

	if _, err := client.GetSubscription(t.Context(), "private-subscription"); err != nil {
		t.Fatal(err)
	}

	want := []payment.OperationEvent{
		{Operation: "get_checkout"},
		{Operation: "get_payment"},
		{Operation: "get_refund"},
		{Operation: "get_subscription"},
	}

	if !slices.Equal(*observed, want) {
		t.Fatalf("expected one observation per read: %v", *observed)
	}
}

func TestPaymentEventsObserveHostedAndSubscriptionCalls(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	fake := paymenttest.New()
	fake.ReplyCreateCheckout(payment.Checkout{Status: payment.CheckoutStatusOpen}, nil)
	fake.ReplyCreateSubscriptionCheckout(payment.Checkout{Status: payment.CheckoutStatusOpen}, nil)
	fake.ReplyCompleteCheckout(payment.Checkout{PaymentStatus: payment.StatusPending}, nil)
	fake.ReplyCancelSubscription(nil)

	client := payment.New(fake, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})

	amount := payment.Money{Amount: 2500, Currency: "USD"}

	if _, err := client.CreateCheckout(t.Context(), payment.NewCheckoutRequest(amount, "https://shop.example/return", "create-key")); err != nil {
		t.Fatal(err)
	}

	if _, err := client.CreateSubscriptionCheckout(t.Context(), payment.NewSubscriptionCheckoutRequest("private-plan", "https://shop.example/return", "enroll-key")); err != nil {
		t.Fatal(err)
	}

	if _, err := client.CompleteCheckout(t.Context(), payment.NewCompleteCheckoutRequest("private-checkout", "complete-key")); err != nil {
		t.Fatal(err)
	}

	if err := client.CancelSubscription(t.Context(), payment.NewCancelSubscriptionRequest("private-subscription")); err != nil {
		t.Fatal(err)
	}

	want := []payment.OperationEvent{
		{Operation: "create_checkout"},
		{Operation: "create_subscription_checkout"},
		{Operation: "complete_checkout"},
		{Operation: "cancel_subscription"},
	}

	if !slices.Equal(*observed, want) {
		t.Fatalf("expected API completion, not settlement: %v", *observed)
	}
}

func TestPaymentEventsRefundReplayReportsCallsNotReturnedFunds(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	fake := paymenttest.New()
	want := payment.Refund{ID: "private-refund", Status: payment.RefundStatusPending}
	fake.ReplyRefundPayment(want, nil)
	fake.ReplyRefundPayment(want, nil)

	client := payment.New(fake, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})

	request := payment.NewRefundRequest("private-payment", payment.Money{Amount: 500, Currency: "USD"}, "private-key")

	for range 2 {
		result, err := client.RefundPayment(t.Context(), request)

		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(result, want) {
			t.Fatalf("refund result changed: %+v", result)
		}
	}

	wantEvents := []payment.OperationEvent{{Operation: "refund_payment"}, {Operation: "refund_payment"}}

	if !slices.Equal(*observed, wantEvents) {
		t.Fatalf("replayed API calls must each be observed: %v", *observed)
	}
}

func TestPaymentEventsDispatchedCancellationStillNotifies(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	driver := &paymentDriver{get: func(context.Context, payment.ID) (payment.Payment, error) {
		cancel()

		return payment.Payment{}, context.Canceled
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})

	if _, err := client.GetPayment(ctx, "private-id"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	want := []payment.OperationEvent{{Operation: "get_payment", ErrorType: "context.Canceled"}}

	if !slices.Equal(*observed, want) {
		t.Fatalf("dispatched cancellation observation: %v", *observed)
	}
}

func TestPaymentEventsUnsupportedProviderResultIsClassified(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	fake := paymenttest.New()
	fake.ReplyCancelSubscription(payment.ErrUnsupported)

	client := payment.New(fake, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})

	if err := client.CancelSubscription(t.Context(), payment.NewCancelSubscriptionRequest("private-subscription")); !errors.Is(err, payment.ErrUnsupported) {
		t.Fatal(err)
	}

	want := []payment.OperationEvent{{Operation: "cancel_subscription", ErrorType: "payment.ErrUnsupported"}}

	if !slices.Equal(*observed, want) {
		t.Fatalf("unsupported is an API-call failure: %v", *observed)
	}
}

func TestPaymentEventsRunAfterExistingTelemetryEnds(t *testing.T) {
	t.Parallel()

	bus := event.New()
	instrumentation := telemetrytest.New()

	stop, err := bus.Listen(payment.OperationCompleted, func(context.Context, payment.OperationEvent) error {
		if len(instrumentation.Spans()) != 1 || len(instrumentation.Metrics()) == 0 {
			t.Fatal("listener ran before operation telemetry completed")
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stop)

	fake := paymenttest.New()
	fake.ReplyGetPayment(payment.Payment{}, nil)

	client := payment.New(fake, func(checkout *payment.Builder) {
		checkout.Events(bus)
		checkout.Telemetry(instrumentation.Telemetry(t))
	})

	if _, err := client.GetPayment(t.Context(), "private-id"); err != nil {
		t.Fatal(err)
	}
}

func TestPaymentEventsProviderPanicHasNoResultNotification(t *testing.T) {
	t.Parallel()

	bus := event.New()
	observed := observeOperations(t, bus)
	driver := &paymentDriver{get: func(context.Context, payment.ID) (payment.Payment, error) {
		panic("provider panic")
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Events(bus)
	})
	defer func() {
		if recovered := recover(); recovered != "provider panic" {
			t.Fatalf("provider panic changed: %v", recovered)
		}

		if len(*observed) != 0 {
			t.Fatalf("a panic has no returned operation result: %v", *observed)
		}
	}()
	_, _ = client.GetPayment(t.Context(), "private-id")
}
