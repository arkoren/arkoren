package payment

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/event"
	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

// Client validates portable inputs and instruments a concurrency-safe Driver.
// It owns no lifecycle, webhook handling, retry policy, or fulfillment state.
// Construct it with New; its settings are immutable after publication.
//
// Example:
//
//	client := payment.New(driver)
//	checkout, err := client.GetCheckout(ctx, payment.CheckoutID("cs_example"))
//	if err != nil { return err }
//	_ = checkout.Status
type Client struct {
	// driver performs provider I/O after portable validation succeeds.
	// The caller remains responsible for the driver's lifecycle, if any.
	driver Driver

	// telemetry is shared or dynamically resolved from telemetry.Default if nil.
	// It never receives request or response payloads.
	telemetry *telemetry.Telemetry

	// events receives best-effort API-call observations, never financial state.
	// The bus remains shared; nil disables all notification classification work.
	events *event.Bus

	// instruments caches operation instruments for each selected telemetry service.
	// It contains no application request or provider resource data.
	instruments *telemetry.Instruments[clientop.Instruments]
}

// New creates a Client without I/O. Callbacks run directly once, synchronously
// in order on one fresh draft. Nil entries panic; expanded nil/empty slices omit
// edits. Completed settings are copied; driver, telemetry, and event bus stay shared.
// A nil driver is rejected on use with ErrInvalidRequest before telemetry.
// Optional events report API-call results, not settlement; see [OperationEvent].
//
// Example:
//
//	client := payment.New(driver, func(checkout *payment.Builder) {
//		checkout.Telemetry(nil)
//	})
//	_ = client.Driver()
func New(driver Driver, configure ...BuilderFunc) *Client {
	checkout := &Builder{}

	for _, configure := range configure {
		configure(checkout)
	}

	return &Client{
		driver:      driver,
		telemetry:   checkout.telemetry,
		events:      checkout.events,
		instruments: telemetry.NewInstruments(clientop.Build(operationNames)),
	}
}

// Driver returns the originally supplied backend, including any wrapping driver.
// Using it directly bypasses facade telemetry and operation notifications, not
// the driver's validation duties.
//
// Example:
//
//	client := payment.New(driver)
//	backend := client.Driver()
//	_ = backend
func (client *Client) Driver() Driver {
	return client.driver
}

// DriverAs finds the first backend of type T through Unwrap() Driver wrappers.
// It returns T's zero value and false when no matching backend exists.
//
// Example:
//
//	backend, ok := payment.New(driver).DriverAs[payment.Driver]()
//	if ok { _ = backend }
func (client *Client) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](client.driver)
}

// PreparePayment validates and initializes a client-side payment handoff.
// It never confirms, completes hosted checkout, retries, or hydrates a successful
// response. Action secrets are excluded from telemetry. Optional resource IDs
// remain absent when the provider returns only a session or client order.
//
// Example:
//
//	prepared, err := client.PreparePayment(ctx, payment.NewPreparePaymentRequest(amount, "order-1"))
//	if err != nil { return err }
//	_ = prepared.Action.Kind()
func (client *Client) PreparePayment(ctx context.Context, request PreparePaymentRequest) (Preparation, error) {
	if err := request.Validate(ctx); err != nil {
		return Preparation{}, err
	}

	return invoke(ctx, client, "prepare_payment", func(ctx context.Context) (Preparation, error) {
		return client.driver.PreparePayment(ctx, request)
	})
}

// CreateCheckout validates a positive amount and starts customer approval.
// It never completes checkout, retries mutations, or performs a follow-up read.
// Errors preserve provider sentinel identity under an operation-name wrapper.
//
// Example:
//
//	checkout, err := client.CreateCheckout(ctx, request)
//	if err != nil { return err }
//	_ = checkout.ApprovalURL
func (client *Client) CreateCheckout(ctx context.Context, request CheckoutRequest) (Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return Checkout{}, err
	}

	return invoke(ctx, client, "create_checkout", func(ctx context.Context) (Checkout, error) {
		return client.driver.CreateCheckout(ctx, request)
	})
}

// CreateSubscriptionCheckout validates and starts approval of an existing plan.
// No catalog, invoice, entitlement, or post-mutation read is created implicitly.
// Errors preserve provider sentinel identity under an operation-name wrapper.
//
// Example:
//
//	checkout, err := client.CreateSubscriptionCheckout(ctx, request)
//	if err != nil { return err }
//	_ = checkout.ApprovalURL
func (client *Client) CreateSubscriptionCheckout(
	ctx context.Context, request SubscriptionCheckoutRequest,
) (Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return Checkout{}, err
	}

	return invoke(ctx, client, "create_subscription_checkout", func(ctx context.Context) (Checkout, error) {
		return client.driver.CreateSubscriptionCheckout(ctx, request)
	})
}

// GetCheckout validates the identity and observes approval and payment status.
// It never captures funds, activates a subscription, or completes approval.
//
// Example:
//
//	checkout, err := client.GetCheckout(ctx, payment.CheckoutID("cs_example"))
//	if err != nil { return err }
//	_ = checkout.NeedsCompletion
func (client *Client) GetCheckout(ctx context.Context, id CheckoutID) (Checkout, error) {
	if err := id.Validate(ctx); err != nil {
		return Checkout{}, err
	}

	return invoke(ctx, client, "get_checkout", func(ctx context.Context) (Checkout, error) {
		return client.driver.GetCheckout(ctx, id)
	})
}

// CompleteCheckout explicitly completes provider-required approval using its
// own idempotency key. Provider-completed flows are observed without another
// charge; successful mutations are not followed by hydration reads.
//
// Example:
//
//	checkout, err := client.CompleteCheckout(ctx, request)
//	if err != nil { return err }
//	_ = checkout.PaymentStatus
func (client *Client) CompleteCheckout(ctx context.Context, request CompleteCheckoutRequest) (Checkout, error) {
	if err := request.Validate(ctx); err != nil {
		return Checkout{}, err
	}

	return invoke(ctx, client, "complete_checkout", func(ctx context.Context) (Checkout, error) {
		return client.driver.CompleteCheckout(ctx, request)
	})
}

// GetPayment observes an individual payment without initiating money movement.
// Pending never counts as success, and refunds remain separate resources.
//
// Example:
//
//	result, err := client.GetPayment(ctx, payment.ID("pi_example"))
//	if err != nil { return err }
//	_ = result.Status
func (client *Client) GetPayment(ctx context.Context, id ID) (Payment, error) {
	if err := id.Validate(ctx); err != nil {
		return Payment{}, err
	}

	return invoke(ctx, client, "get_payment", func(ctx context.Context) (Payment, error) {
		return client.driver.GetPayment(ctx, id)
	})
}

// RefundPayment requests a positive explicit amount without a hidden balance
// lookup. Submission may produce a pending refund rather than immediate success.
//
// Example:
//
//	refund, err := client.RefundPayment(ctx, request)
//	if err != nil { return err }
//	_ = refund.Status
func (client *Client) RefundPayment(ctx context.Context, request RefundRequest) (Refund, error) {
	if err := request.Validate(ctx); err != nil {
		return Refund{}, err
	}

	return invoke(ctx, client, "refund_payment", func(ctx context.Context) (Refund, error) {
		return client.driver.RefundPayment(ctx, request)
	})
}

// GetRefund reads the refund's own progress without resubmitting it.
// Pending or RequiresAction must not be interpreted as successful returned funds.
//
// Example:
//
//	refund, err := client.GetRefund(ctx, payment.RefundID("re_example"))
//	if err != nil { return err }
//	_ = refund.Status
func (client *Client) GetRefund(ctx context.Context, id RefundID) (Refund, error) {
	if err := id.Validate(ctx); err != nil {
		return Refund{}, err
	}

	return invoke(ctx, client, "get_refund", func(ctx context.Context) (Refund, error) {
		return client.driver.GetRefund(ctx, id)
	})
}

// GetSubscription reads lifecycle without activation or invoice settlement.
// Active and NextBillingAt must not be interpreted as paid-through evidence.
//
// Example:
//
//	subscription, err := client.GetSubscription(ctx, payment.SubscriptionID("sub_example"))
//	if err != nil { return err }
//	_ = subscription.Status
func (client *Client) GetSubscription(ctx context.Context, id SubscriptionID) (Subscription, error) {
	if err := id.Validate(ctx); err != nil {
		return Subscription{}, err
	}

	return invoke(ctx, client, "get_subscription", func(ctx context.Context) (Subscription, error) {
		return client.driver.GetSubscription(ctx, id)
	})
}

// CancelSubscription immediately cancels without a refund or proration.
// It neither schedules a future cancellation nor reads after mutation. An
// uncertain dispatched outcome preserves ErrOutcomeUnknown for reconciliation.
//
// Example:
//
//	if err := client.CancelSubscription(ctx, request); err != nil { return err }
func (client *Client) CancelSubscription(ctx context.Context, request CancelSubscriptionRequest) error {
	if err := request.Validate(ctx); err != nil {
		return err
	}

	_, err := invoke(ctx, client, "cancel_subscription", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, client.driver.CancelSubscription(ctx, request)
	})

	return err
}

// invoke is the facade's shared provider-I/O boundary: reject missing drivers,
// record payload-free telemetry, invoke exactly once, and wrap failures with the
// operation name without losing error identity. Validation happens before it.
// A returned provider result emits one best-effort notification, after telemetry
// ends, using the operation context. Panicking providers emit no result event.
func invoke[T any](
	ctx context.Context, client *Client, operation string, call func(context.Context) (T, error),
) (result T, err error) {
	if client.driver == nil {
		return result, fmt.Errorf("%w: driver cannot be nil", ErrInvalidRequest)
	}

	ctx, op := clientop.Begin(ctx, client.telemetry, client.instruments, telemetry.Client, "payment", operation,
		telemetry.String(semconv.PaymentOperation, operation))
	// returned distinguishes a provider result from a provider panic; only the
	// former is an observed API-call outcome eligible for notification.
	returned := false
	defer func() {
		// Normalize only the telemetry input: ambiguous money movement outranks
		// transport causes. The API error retains every joined cause, and events
		// apply the same precedence independently without copying native text.
		failure := err

		if errors.Is(err, ErrOutcomeUnknown) {
			failure = ErrOutcomeUnknown
		}

		op.End(ctx, failure, errorSentinels...)

		if returned {
			client.emit(ctx, operation, err)
		}
	}()
	result, err = call(ctx)
	returned = true

	if err != nil {
		return result, fmt.Errorf("payment %s: %w", operation, err)
	}

	return result, nil
}
