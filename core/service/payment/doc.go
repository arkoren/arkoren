// Package payment defines provider-neutral checkout, payment, refund, and
// subscription operations. Client adds portable validation and telemetry to a
// Driver; applications retain ownership of routes, webhooks, persistence, and
// fulfillment policy.
//
// A checkout is an approval flow, not proof of payment. Pending does not mean
// succeeded, and an active subscription does not establish that an invoice was
// paid. Inspect PaymentStatus and provider observations before fulfillment.
// NativeStatus preserves information outside the portable status vocabulary.
// PreparePayment initializes an SDK authorization, order, or session without
// confirmation or hosted completion. Its required concrete ClientAction selects
// the SDK protocol. Financial and hosted references are optional observations:
// a session-only response need not identify any financial resource. There is no
// universal preparation lookup; reconcile only resources actually observed.
// Action, Preparation, and variant fmt/slog diagnostics redact handoff data;
// their JSON intentionally exposes secrets only for authorized client delivery.
// Applications must authorize callback destinations and protect handoff secrets.
// Get operations are pure reads. Only CompleteCheckout explicitly completes an
// approved flow; reads never hide completion. Successful mutations describe
// their response, not later hydration. A preflight lifecycle read may guard a
// mutation, and a documented no-effect state conflict may allow one bounded
// reconciliation read. Neither permits automatic polling or retry after an
// ambiguous dispatch.
//
// Idempotency keys are caller-owned, provider-scoped, and bounded by each
// provider's retention window. Reuse a key only for the same operation and
// request. ErrOutcomeUnknown means a dispatched mutation may have succeeded;
// reconcile or retry with the same key within provider guarantees, never assume
// it failed. Cancellation is immediate and does not imply a refund or proration.
//
// Example:
//
//	client := payment.New(driver)
//	request := payment.NewCheckoutRequest(payment.Money{Amount: 2500, Currency: "USD"},
//		"https://shop.example/paid", "order-123")
//	checkout, err := client.CreateCheckout(ctx, request)
//	if err != nil {
//		return err
//	}
//	_ = checkout.ApprovalURL
//
// Requests are immutable values published by New*Request constructors. Optional
// builders run once, in order, even for invalid required inputs; nil callback
// entries panic. Err reports sticky first input errors without a context, while
// Validate checks context first. Money inputs/getters are copied. JSON validates
// before encoding and decodes temporary state, replacing only on success.
// NewMoney validates integer minor units without changing Money's record fields.
// Hosted return URLs still require HTTPS or loopback HTTP; preparation callbacks
// additionally allow custom native URI syntax. Applications own the allowlist:
// syntax validation cannot establish native scheme registration or ownership.
//
// Valid calls emit one "payment <operation>" client span and the
// arkoren.payment.operations and arkoren.payment.operation.duration metrics.
// Only operation and error class are recorded: never IDs, amounts, URLs,
// references, native errors, or credentials. Invalid requests and already
// canceled contexts fail before telemetry or driver effects. Telemetry
// reports ErrOutcomeUnknown ahead of any joined transport or context cause;
// the returned error still preserves those causes for errors.Is. Nil telemetry
// follows telemetry.Default per call. Unsupported operations return
// ErrUnsupported rather than using optional capability interfaces.
// Preparation uses "payment prepare_payment" and the same payload-free metrics.
//
// Builder.Events optionally selects an in-process event bus. OperationCompleted
// and OperationFailed report one driver-returned API-call result after telemetry
// ends, including reads, unsupported operations, and idempotent replays. Rejected
// preflight calls emit nothing. Payloads contain only fixed operation/error
// classes; ErrOutcomeUnknown outranks joined causes and never means failed money
// movement. Listener errors and panics cannot change the API result. Listeners
// run synchronously with the operation context; nil disables notifications.
// These best-effort observations are not durable reconciliation or fulfillment
// triggers. Provider webhooks and application-owned state persistence remain
// separate; no event asserts settlement or an authoritative state transition.
//
// Example:
//
//	bus := event.New()
//	client := payment.New(driver, func(checkout *payment.Builder) {
//		checkout.Events(bus)
//	})
//	result, err := client.GetPayment(ctx, payment.ID("pi_example"))
//	if err != nil {
//		return err
//	}
//	_ = result.Status
package payment
