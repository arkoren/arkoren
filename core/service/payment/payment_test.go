package payment_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/core/service/payment/paymenttest"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// paymentDriver substitutes only the payment read boundary. Its embedded nil
// Driver deliberately panics if a test accidentally invokes any other operation.
type paymentDriver struct {
	// Driver supplies unexpected operations through nil, exposing accidental I/O.
	payment.Driver

	// get is the one read boundary controlled by this test's scenario.
	get func(context.Context, payment.ID) (payment.Payment, error)
}

// GetPayment delegates to the test's deterministic boundary behavior and does
// not maintain provider state, retries, or a parallel fake implementation.
func (driver *paymentDriver) GetPayment(ctx context.Context, id payment.ID) (payment.Payment, error) {
	return driver.get(ctx, id)
}

// checkoutDriver substitutes only checkout creation to verify validation and
// verbatim forwarding, without an artificial provider lifecycle or data store.
type checkoutDriver struct {
	// Driver supplies unexpected operations through nil, exposing hidden reads.
	payment.Driver

	// create receives the original request and returns one scripted response.
	create func(context.Context, payment.CheckoutRequest) (payment.Checkout, error)
}

// CreateCheckout invokes the test's provider boundary exactly once. All other
// operations remain on the embedded nil Driver to expose accidental extra I/O.
func (driver *checkoutDriver) CreateCheckout(
	ctx context.Context, request payment.CheckoutRequest,
) (payment.Checkout, error) {
	return driver.create(ctx, request)
}

// wrappingDriver forwards the full contract through embedding and exposes its
// backend for DriverAs, matching real tracing or policy wrappers.
type wrappingDriver struct {
	// Driver is the full backend forwarded by this wrapper.
	payment.Driver
}

// Unwrap reveals the wrapped contract without exposing provider internals.
// DriverAs must follow this exact typed Unwrap signature.
func (driver *wrappingDriver) Unwrap() payment.Driver {
	return driver.Driver
}

func TestNewCallbacksShareOneDraftAndRunInOrder(t *testing.T) {
	t.Parallel()

	var seen []*payment.Builder
	var order []int
	client := payment.New(nil, func(checkout *payment.Builder) {
		seen = append(seen, checkout)
		order = append(order, 1)
	}, func(checkout *payment.Builder) {
		seen = append(seen, checkout)
		order = append(order, 2)
	})

	if seen[0] != seen[1] {
		t.Fatal("construction callbacks received different drafts")
	}

	if fmt.Sprint(order) != "[1 2]" {
		t.Fatalf("callback order = %v", order)
	}

	if client.Driver() != nil {
		t.Fatal("construction changed driver")
	}
}

func TestNewPublishesDetachedDraft(t *testing.T) {
	t.Parallel()

	first := telemetrytest.New()
	second := telemetrytest.New()
	firstTelemetry := first.Telemetry(t)
	secondTelemetry := second.Telemetry(t)
	var retained *payment.Builder
	driver := &paymentDriver{get: func(_ context.Context, id payment.ID) (payment.Payment, error) {
		return payment.Payment{ID: id}, nil
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		retained = checkout
		checkout.Telemetry(firstTelemetry)
	})
	retained.Telemetry(secondTelemetry)

	if _, err := client.GetPayment(t.Context(), "pi_example"); err != nil {
		t.Fatal(err)
	}

	if len(first.Spans()) != 1 {
		t.Fatalf("original telemetry spans = %d, want 1", len(first.Spans()))
	}

	if len(second.Spans()) != 0 {
		t.Fatal("retained draft reconfigured published client")
	}
}

func TestNewNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()
	payment.New(nil, nil)
}

func TestNewPropagatesCallbackPanic(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "caller panic" {
			t.Fatalf("panic = %v", got)
		}
	}()
	payment.New(nil, func(checkout *payment.Builder) {
		panic("caller panic")
	})
}

func TestNewAcceptsExpandedNilCallbacks(t *testing.T) {
	t.Parallel()

	var callbacks []payment.BuilderFunc
	client := payment.New(nil, callbacks...)

	if client == nil {
		t.Fatal("nil callback slice failed to construct client")
	}
}

func TestClientDriverAsFollowsWrappers(t *testing.T) {
	t.Parallel()

	backend := &paymentDriver{}
	wrapper := &wrappingDriver{Driver: backend}
	client := payment.New(wrapper)

	if client.Driver() != wrapper {
		t.Fatal("Driver did not preserve the original wrapper")
	}

	concrete, ok := client.DriverAs[*paymentDriver]()

	if !ok || concrete != backend {
		t.Fatal("DriverAs did not find wrapped backend")
	}
}

func TestClientRejectsInvalidRequestBeforeEffectsAndTelemetry(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := &checkoutDriver{create: func(context.Context, payment.CheckoutRequest) (payment.Checkout, error) {
		t.Fatal("driver called for an invalid request")

		return payment.Checkout{}, nil
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Telemetry(fake.Telemetry(t))
	})

	if _, err := client.CreateCheckout(t.Context(), payment.CheckoutRequest{}); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("CreateCheckout = %v", err)
	}

	if len(fake.Spans()) != 0 || len(fake.Metrics()) != 0 {
		t.Fatal("invalid request emitted telemetry")
	}
}

func TestClientRejectsNilContextBeforeEffectsAndTelemetry(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := &paymentDriver{get: func(context.Context, payment.ID) (payment.Payment, error) {
		t.Fatal("driver called with nil context")

		return payment.Payment{}, nil
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Telemetry(fake.Telemetry(t))
	})

	var ctx context.Context

	if _, err := client.GetPayment(ctx, "pi_example"); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("GetPayment = %v", err)
	}

	if len(fake.Spans()) != 0 || len(fake.Metrics()) != 0 {
		t.Fatal("nil context emitted telemetry")
	}
}

func TestClientPreservesCanceledContextBeforeEffectsAndTelemetry(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := &paymentDriver{get: func(context.Context, payment.ID) (payment.Payment, error) {
		t.Fatal("driver called with canceled context")

		return payment.Payment{}, nil
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Telemetry(fake.Telemetry(t))
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := client.GetPayment(ctx, "pi_example"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPayment = %v", err)
	}

	if len(fake.Spans()) != 0 || len(fake.Metrics()) != 0 {
		t.Fatal("canceled context emitted telemetry")
	}
}

func TestClientRejectsNilDriverBeforeTelemetry(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	client := payment.New(nil, func(checkout *payment.Builder) {
		checkout.Telemetry(fake.Telemetry(t))
	})

	if _, err := client.GetPayment(t.Context(), "pi_example"); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("GetPayment = %v", err)
	}

	if len(fake.Spans()) != 0 || len(fake.Metrics()) != 0 {
		t.Fatal("nil driver emitted telemetry")
	}
}

func TestClientForwardsCheckoutVerbatimAndWithoutHydration(t *testing.T) {
	t.Parallel()

	want := payment.NewCheckoutRequest(payment.Money{Amount: 2500, Currency: "USD"},
		"https://shop.example/paid?checkout={CHECKOUT_SESSION_ID}", "order-123", func(details *payment.CheckoutRequestBuilder) {
			details.CancelURL("https://shop.example/cart").Description("Books").Reference("order-123")
		})

	calls := 0
	driver := &checkoutDriver{create: func(_ context.Context, request payment.CheckoutRequest) (payment.Checkout, error) {
		calls++

		if request != want {
			t.Fatalf("forwarded request = %+v, want %+v", request, want)
		}

		return payment.Checkout{ID: "cs_example", Status: payment.CheckoutStatusOpen}, nil
	}}

	checkout, err := payment.New(driver).CreateCheckout(t.Context(), want)

	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 || checkout.ID != "cs_example" {
		t.Fatalf("calls = %d, checkout = %+v", calls, checkout)
	}
}

func TestClientDoesNotDispatchBackgroundWork(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		calls := 0
		driver := &checkoutDriver{create: func(context.Context, payment.CheckoutRequest) (payment.Checkout, error) {
			calls++

			return payment.Checkout{ID: "cs_example"}, nil
		}}

		client := payment.New(driver)
		synctest.Wait()

		if calls != 0 {
			t.Fatalf("construction dispatched %d operations", calls)
		}

		_, err := client.CreateCheckout(t.Context(), payment.NewCheckoutRequest(
			payment.Money{Amount: 100, Currency: "USD"}, "https://shop.example/paid", "order-123"))

		if err != nil {
			t.Fatal(err)
		}

		synctest.Wait()

		if calls != 1 {
			t.Fatalf("explicit creation dispatched %d operations, want 1", calls)
		}
	})
}

func TestClientWrapsDriverErrorAndPreservesIdentity(t *testing.T) {
	t.Parallel()

	driver := &paymentDriver{get: func(context.Context, payment.ID) (payment.Payment, error) {
		return payment.Payment{}, fmt.Errorf("provider failed: %w", payment.ErrUnavailable)
	}}

	_, err := payment.New(driver).GetPayment(t.Context(), "pi_example")

	if !errors.Is(err, payment.ErrUnavailable) {
		t.Fatalf("GetPayment = %v", err)
	}

	if !strings.HasPrefix(err.Error(), "payment get_payment: ") {
		t.Fatalf("missing operation context: %v", err)
	}
}

func TestClientPreservesDispatchedCancellationCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("caller canceled after dispatch")
	ctx, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() {
		cancel(nil)
	})

	driver := &checkoutDriver{create: func(ctx context.Context, _ payment.CheckoutRequest) (payment.Checkout, error) {
		cancel(cause)

		return payment.Checkout{}, fmt.Errorf("%w: %w", payment.ErrOutcomeUnknown, context.Cause(ctx))
	}}

	_, err := payment.New(driver).CreateCheckout(ctx, payment.NewCheckoutRequest(
		payment.Money{Amount: 100, Currency: "USD"}, "https://shop.example/paid", "order-123"))

	if !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("CreateCheckout = %v, want ErrOutcomeUnknown", err)
	}

	if !errors.Is(err, cause) {
		t.Fatalf("CreateCheckout lost driver cause: %v", err)
	}
}

func TestClientTelemetryRecordsOnlyOperationAndErrorClass(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := &paymentDriver{get: func(context.Context, payment.ID) (payment.Payment, error) {
		return payment.Payment{}, fmt.Errorf("provider-secret: %w", payment.ErrOutcomeUnknown)
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Telemetry(fake.Telemetry(t))
	})

	if _, err := client.GetPayment(t.Context(), "private-payment-id"); !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("GetPayment = %v", err)
	}

	spans := fake.Spans()

	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}

	if spans[0].Name != "payment get_payment" || spans[0].StatusDescription != "payment.ErrOutcomeUnknown" {
		t.Fatalf("unexpected span: %+v", spans[0])
	}

	if len(spans[0].Attrs) != 2 || len(spans[0].Events) != 0 {
		t.Fatalf("unexpected span payload: %+v", spans[0])
	}

	snapshot := fmt.Sprint(spans, fake.Metrics())

	for _, secret := range []string{"provider-secret", "private-payment-id"} {
		if strings.Contains(snapshot, secret) {
			t.Fatalf("telemetry disclosed %q", secret)
		}
	}
}

// TestUnknownOutcomeTelemetryPrecedesJoinedCauses keeps ambiguous money movement
// visible without removing the context class, cancellation cause, or provider
// availability class from the error returned to the application.
func TestUnknownOutcomeTelemetryPrecedesJoinedCauses(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	cause := errors.New("caller stopped waiting")
	ctx, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() {
		cancel(nil)
	})

	driver := &checkoutDriver{create: func(ctx context.Context, _ payment.CheckoutRequest) (payment.Checkout, error) {
		cancel(cause)

		return payment.Checkout{}, errors.Join(payment.ErrUnavailable, payment.ErrOutcomeUnknown,
			ctx.Err(), context.Cause(ctx))
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Telemetry(fake.Telemetry(t))
	})

	_, err := client.CreateCheckout(ctx, payment.NewCheckoutRequest(
		payment.Money{Amount: 1200, Currency: "USD"}, "https://shop.example/paid", "order-unknown"))

	for _, target := range []error{payment.ErrOutcomeUnknown, payment.ErrUnavailable, context.Canceled, cause} {
		if !errors.Is(err, target) {
			t.Fatalf("lost error identity %v: %v", target, err)
		}
	}

	spans := fake.Spans()

	if len(spans) != 1 || spans[0].StatusDescription != "payment.ErrOutcomeUnknown" {
		t.Fatalf("unexpected unknown-outcome telemetry: %+v", spans)
	}

	snapshot := fmt.Sprint(fake.Metrics())

	if !strings.Contains(snapshot, "payment.ErrOutcomeUnknown") || strings.Contains(snapshot, "context.Canceled") {
		t.Fatalf("metrics hid unknown outcome: %s", snapshot)
	}
}

func TestCheckoutTelemetryOmitsRequestAndResponseSecrets(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := &checkoutDriver{create: func(context.Context, payment.CheckoutRequest) (payment.Checkout, error) {
		return payment.Checkout{
			ID: "private-checkout-id", NativeStatus: "private-native-status",
			ApprovalURL: "https://pay.example/private-approval", PaymentID: "private-payment-id",
		}, nil
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Telemetry(fake.Telemetry(t))
	})

	_, err := client.CreateCheckout(t.Context(), payment.NewCheckoutRequest(
		payment.Money{Amount: 987654321, Currency: "USD"}, "https://shop.example/private-return", "private-key",
		func(details *payment.CheckoutRequestBuilder) {
			details.Description("private-description").Reference("private-reference").CancelURL("https://shop.example/private-cancel")
		}))

	if err != nil {
		t.Fatal(err)
	}

	if len(fake.Spans()) != 1 {
		t.Fatalf("spans = %d, want 1", len(fake.Spans()))
	}

	snapshot := fmt.Sprint(fake.Spans(), fake.Metrics())

	for _, secret := range []string{"private-", "987654321", "USD", "shop.example", "pay.example"} {
		if strings.Contains(snapshot, secret) {
			t.Fatalf("telemetry disclosed %q", secret)
		}
	}
}

func TestClientTelemetryRecordsOperationMetrics(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := &paymentDriver{get: func(_ context.Context, id payment.ID) (payment.Payment, error) {
		return payment.Payment{ID: id}, nil
	}}

	client := payment.New(driver, func(checkout *payment.Builder) {
		checkout.Telemetry(fake.Telemetry(t))
	})

	if _, err := client.GetPayment(t.Context(), "pi_example"); err != nil {
		t.Fatal(err)
	}

	metrics := fake.Metrics()

	if len(metrics) != 2 {
		t.Fatalf("metrics = %d, want 2", len(metrics))
	}

	if metrics[0].Name != "arkoren.payment.operations" {
		t.Fatalf("count name = %q", metrics[0].Name)
	}

	if metrics[1].Name != "arkoren.payment.operation.duration" {
		t.Fatalf("duration name = %q", metrics[1].Name)
	}
}

// TestClientPreparationPreflightHasNoEffectsOrTelemetry exercises invalid and
// canceled preparation requests before the facade's shared dispatch boundary.
func TestClientPreparationPreflightHasNoEffectsOrTelemetry(t *testing.T) {
	t.Parallel()

	backend := paymenttest.New()
	instrumentation := telemetrytest.New()
	client := payment.New(backend, func(checkout *payment.Builder) {
		checkout.Telemetry(instrumentation.Telemetry(t))
	})

	_, err := client.PreparePayment(t.Context(), payment.PreparePaymentRequest{})

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("invalid request dispatched")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = client.PreparePayment(ctx, payment.PreparePaymentRequest{})

	if !errors.Is(err, context.Canceled) {
		t.Fatal("request displaced canceled context")
	}

	if len(backend.Attempted().PreparedPayments) != 0 {
		t.Fatal("preflight dispatched")
	}

	if len(instrumentation.Spans()) != 0 || len(instrumentation.Metrics()) != 0 {
		t.Fatal("preflight emitted telemetry")
	}
}

// TestClientPreparationNilDriverFailsBeforeTelemetry applies the existing facade
// safety boundary equally to the new client initialization operation.
func TestClientPreparationNilDriverFailsBeforeTelemetry(t *testing.T) {
	t.Parallel()

	instrumentation := telemetrytest.New()
	client := payment.New(nil, func(checkout *payment.Builder) {
		checkout.Telemetry(instrumentation.Telemetry(t))
	})

	_, err := client.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1"))

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("nil driver accepted")
	}

	if len(instrumentation.Spans()) != 0 {
		t.Fatal("nil driver emitted telemetry")
	}
}

// TestClientPreparationForwardsSessionOnlyResultWithoutHydration checks verbatim
// forwarding and optional financial identities, without fabricated provider reads.
func TestClientPreparationForwardsSessionOnlyResultWithoutHydration(t *testing.T) {
	t.Parallel()

	backend := paymenttest.New()
	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 100, Currency: "USD"}, "prepare-1",
		func(details *payment.PreparePaymentRequestBuilder) {
			details.Reference("order-1").Channel(payment.ClientChannelAndroid).DeviceReference("device-1")
		})

	want := payment.Preparation{Action: payment.NewClientSession("vendor.session", "session-1", "sdk-data")}
	backend.ReplyPreparePayment(want, nil)

	prepared, err := payment.New(backend).PreparePayment(t.Context(), request)

	if err != nil || prepared != want {
		t.Fatal("facade changed session-only handoff")
	}

	backend.AssertPreparedPayment(t, func(observed payment.PreparePaymentRequest) bool {
		return observed == request
	})

	if len(backend.Attempted().ReadPayments) != 0 || len(backend.Attempted().ReadCheckouts) != 0 {
		t.Fatal("preparation hydrated optional resources")
	}

	if len(backend.Attempted().CompletedCheckouts) != 0 {
		t.Fatal("preparation completed hosted checkout")
	}
}

// TestClientPreparationTelemetryRedactsBothSides verifies the stable operation
// name, ordinary metrics, and absence of request, callback, and action secrets.
func TestClientPreparationTelemetryRedactsBothSides(t *testing.T) {
	t.Parallel()

	backend := paymenttest.New()
	backend.ReplyPreparePayment(payment.Preparation{PaymentID: "private-payment",
		Action: payment.NewClientAuthorization("vendor.authorization", "private-secret")}, nil)

	instrumentation := telemetrytest.New()
	client := payment.New(backend, func(checkout *payment.Builder) {
		checkout.Telemetry(instrumentation.Telemetry(t))
	})

	request := payment.NewPreparePaymentRequest(payment.Money{Amount: 987654321, Currency: "USD"}, "private-key",
		func(details *payment.PreparePaymentRequestBuilder) {
			details.Description("private-description").Reference("private-reference").DeviceReference("private-device")
			details.ReturnURL("com.example.private:/payment").Channel(payment.ClientChannelIOS)
		})

	if _, err := client.PreparePayment(t.Context(), request); err != nil {
		t.Fatal(err)
	}

	spans := instrumentation.Spans()

	if len(spans) != 1 || spans[0].Name != "payment prepare_payment" {
		t.Fatal("preparation span missing")
	}

	if len(instrumentation.Metrics()) != 2 {
		t.Fatal("preparation metrics missing")
	}

	snapshot := fmt.Sprint(spans, instrumentation.Metrics())

	for _, secret := range []string{"private-", "987654321", "USD", "com.example", "vendor.authorization"} {
		if strings.Contains(snapshot, secret) {
			t.Fatal("telemetry disclosed handoff data")
		}
	}
}

// TestClientPreparationPreservesUncertainAcceptance checks that returned action
// data and joined provider/context causes survive the operation wrapper, while
// telemetry classifies uncertainty ahead of ordinary transport cancellation.
func TestClientPreparationPreservesUncertainAcceptance(t *testing.T) {
	t.Parallel()

	backend := paymenttest.New()
	want := payment.Preparation{Action: payment.NewClientOrder("paypal.order", "ORDER-1")}
	backend.ReplyPreparePayment(want, errors.Join(payment.ErrOutcomeUnknown, payment.ErrUnavailable, context.Canceled))

	instrumentation := telemetrytest.New()
	client := payment.New(backend, func(checkout *payment.Builder) {
		checkout.Telemetry(instrumentation.Telemetry(t))
	})

	prepared, err := client.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 100, Currency: "USD"}, "prepare-1"))

	for _, target := range []error{payment.ErrOutcomeUnknown, payment.ErrUnavailable, context.Canceled} {
		if !errors.Is(err, target) {
			t.Fatal("uncertain result lost cause")
		}
	}

	if prepared != want {
		t.Fatal("uncertain acceptance lost action")
	}

	spans := instrumentation.Spans()

	if len(spans) != 1 || spans[0].StatusDescription != "payment.ErrOutcomeUnknown" {
		t.Fatal("uncertainty hidden")
	}

	if !strings.Contains(err.Error(), "payment prepare_payment") {
		t.Fatal("operation context missing")
	}
}
