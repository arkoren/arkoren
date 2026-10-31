---
title: Payments Overview
nav: Overview
description: Start hosted checkout, observe payment outcomes, issue refunds, and manage provider subscriptions without a local billing engine.
order: 10
---

# Payments Overview

## Introduction

When a customer buys an order or subscribes, your application needs an approval
flow and evidence of what the processor collected. `payment.Client` starts hosted
approval, retrieves provider state, requests refunds, and enrolls customers in
provider-managed subscriptions. Choose a [payment driver](drivers.md) and call each operation
explicitly. The same client works without a webhook endpoint, queue, cache,
background worker, or `application.Application`.

A checkout is the buyer's approval flow, not proof that money moved. A payment
records money movement; a refund tracks the progress of returning funds. A
subscription records a recurring agreement whose active status alone does not
prove payment. Keep these facts separate when deciding whether to fulfill an
order or grant access.

The processor owns payment resources and subscription renewals. Your application
owns account mappings, authorization, entitlements, durable operation records,
and when to retrieve or complete a checkout. Arkoren does not install that wiring
or store a local billing projection.

The portable contract has ten operations for client preparation, hosted approval,
observation, completion, refunds, and subscriptions. It is not a tax engine, invoicing system,
catalog, dispute manager, or entitlement service.

For native iOS or Android payment UI instead of a hosted redirect, follow
[Mobile Payments](mobile-payments.md). `PreparePayment` returns a typed action
for the provider's SDK; client SDK configuration remains provider-specific.

Focused examples use a caller context `ctx` and keep error results visible without
routine propagation checks. Handle those errors in your application. For mutation failures,
retain the original operation record: a timeout does not prove that no effect
occurred. [Uncertain outcomes](#handling-uncertain-outcomes) explains reconciliation.

## Constructing the client

Install core and the vendor module you choose:

```sh
go get arkoren.dev/core
go get arkoren.dev/driver/stripe
```

With a configured `payment.Driver` named `driver`, construct the facade:

```go
import "arkoren.dev/core/service/payment"

payments := payment.New(driver)
```

Construction starts no worker, subscribes no webhook, and performs no payment.
The [driver guide](drivers.md) covers credentials and native-client ownership.

## Starting a payment

Create and persist an operation key before making the request. Reuse that key
only for retries of the same logical request within the provider's supported
retention window.

Derive the amount, reference, and redirect destinations only from authorized
server-side order data. Do not accept a buyer-supplied price or arbitrary return
URL. A failed dispatched request may already have taken effect; preserve its key
instead of retrying with a fresh one.

```go
amount, err := payment.NewMoney("USD", 2500)
request := payment.NewCheckoutRequest(amount, "https://shop.example/payments/return", "order-42-checkout")
checkout, err := payments.CreateCheckout(ctx, request)
```

Store `checkout.ID` against the initiating application account and send the buyer
to `checkout.ApprovalURL`. Treat this URL as sensitive. IDs are provider-scoped
handles, not authorization; preserve them rather than parsing the approval URL.
`Reference` is application correlation, not a substitute for the operation key.

If you need a description, cancellation destination, or correlation reference,
replace the baseline request construction with a callback before submitting it:

```go
request := payment.NewCheckoutRequest(amount, "https://shop.example/payments/return", "order-42-checkout",
    func(details *payment.CheckoutRequestBuilder) {
        details.Description("Book order")
        details.CancelURL("https://shop.example/cart")
        details.Reference("order-42")
    })
```

Amounts are integer currency minor units, not floating-point values. `2500 USD`
means 25 dollars; `2500 JPY` means 2500 yen. Supported currency exponents are
explicitly listed by `payment.CurrencyExponent`; unknown currencies return
`ErrUnsupported` instead of assuming two decimals. Providers may support fewer
currencies than core.

Creation reports the provider's own response, not a subsequently hydrated view.
A driver can perform documented preflight reads to validate currency or state
before a mutation. It does not perform follow-up reads to fill missing fields
after an accepted mutation. Retrieve a resource explicitly when you need a newer
observation; a missing relationship is not permission to substitute another ID.

## Completing and observing checkout

A browser return only brings the buyer back to your application; it is not
payment evidence. Resolve the saved checkout only after authorizing the caller
for the associated account. Retrieve its state explicitly:

```go
checkout, err = payments.GetCheckout(ctx, checkout.ID)
```

If the provider reports that merchant completion is required, call it with a
separate stable operation key:

```go
if checkout.NeedsCompletion {
    checkout, err = payments.CompleteCheckout(ctx,
        payment.NewCompleteCheckoutRequest(checkout.ID, "order-42-complete"))
}
```

PayPal needs this capture step after approval; Stripe's hosted checkout and
Mollie's supported automatic-capture flow collect through their hosted UI.
Mollie returns `ErrUnsupported` for explicit completion, and reports
`NeedsCompletion == false`. A completed approval flow can still have a
pending payment, and `NeedsCompletion == false` does not establish success.
Inspect `checkout.PaymentStatus`, and call `GetPayment` with
the observed `checkout.PaymentID` when further payment reconciliation is needed.
An empty relationship remains unknown; Arkoren does not invent one from a
different resource ID.

Before fulfilling, require a succeeded payment with the expected amount and
currency, linked to the saved authorized order. Confirm that the observation
comes from the correct provider merchant account and test/live environment;
status alone must not let a different payment satisfy the order.

Your application chooses when to make these calls. Optional provider notifications
can trigger them, but drivers do not wait for webhooks or poll in the background.
If nobody completes an approved PayPal Order, it may expire without capture.

## Refunding a payment

Authorize the refund against your application's saved order/account before
calling the provider. Request the exact positive amount in the original payment's
currency, using a saved nonempty `checkout.PaymentID`:

```go
refundAmount, err := payment.NewMoney("USD", 500)
refund, err := payments.RefundPayment(ctx,
    payment.NewRefundRequest(checkout.PaymentID, refundAmount, "order-42-refund-1"))
```

A full refund uses the original refundable amount explicitly; zero does not mean
“refund the remainder.” The provider enforces the remaining balance, including
concurrent refund requests. A pending refund is not money returned. Retrieve
`refund.ID` with `GetRefund` when you need a subsequent observation.

Refunding a payment does not cancel its subscription. PayPal subscription-sale
lookup/refund is unsupported; one-time capture refunds use the current Payments
v2 API. Do not substitute a sale ID for a capture ID.

## Enrolling a subscriber

For recurring billing with Stripe or PayPal, enroll the buyer in a
provider-managed subscription rather than scheduling repeated checkouts
yourself. Mollie's driver does not support subscriptions. Create the provider
Price or Plan outside Arkoren, then start its approval flow. Use authorized
server-side plan and redirect data, and persist this enrollment's operation key
before requesting it:

```go
enrollmentRequest := payment.NewSubscriptionCheckoutRequest("price_monthly", "https://shop.example/account", "signup-42-enrollment",
    func(details *payment.SubscriptionCheckoutRequestBuilder) {
        details.CancelURL("https://shop.example/plans")
        details.Reference("signup-42")
    })
enrollment, err := payments.CreateSubscriptionCheckout(ctx, enrollmentRequest)
```

The example reference is a Stripe recurring Price; PayPal takes its own Plan ID.
These are not interchangeable catalog records. Quantity is one. Direct the buyer
to the approval URL and retrieve the checkout when your application chooses.
Stripe may not allocate a subscription ID until checkout completes; PayPal can
allocate one while approval is still pending.

When `enrollment.SubscriptionID` is available, call `GetSubscription` with that
ID. `Active` and `NextBillingAt` describe provider lifecycle, not proof of the
latest payment or a paid-through entitlement. Providers collect renewals; your
application decides access, grace periods, and billing-account ownership.

## Canceling future renewals

Authorize the subscriber's cancellation request against your saved account and
use a known nonempty `enrollment.SubscriptionID`. Request immediate cancellation:

```go
err = payments.CancelSubscription(ctx, payment.NewCancelSubscriptionRequest(enrollment.SubscriptionID,
    func(details *payment.CancelSubscriptionRequestBuilder) {
        details.Reason("Customer request")
    }))
```

Cancellation does not refund previous payments, request proration, or decide
application access. Existing liabilities and in-flight payments may remain.
Scheduled cancellation and plan changes are outside this contract. A successful
cancellation is not followed by a mandatory read that could hide its success.

## Handling uncertain outcomes

An interrupted mutation may already have taken effect. Check
`errors.Is(err, payment.ErrOutcomeUnknown)` and reconcile the original operation
instead of creating a fresh key. Also preserve caller context errors with
`errors.Is` when cancellation occurred after dispatch.

Keep earlier uncertainty in the durable operation record too. A later same-key
call's rejection or pre-dispatch failure does not by itself establish what happened
earlier. Do not clear the saved uncertainty merely because the latest error does
not match `ErrOutcomeUnknown`. Preserve the original request, key,
merchant/environment association, and references until application reconciliation
resolves that attempt. Neither a later rejection nor an unavailable read authorizes
a new-key charge or fulfillment.

With import `errors`, preserve the current call's uncertain outcome:

```go
if errors.Is(err, payment.ErrOutcomeUnknown) {
    // Keep the original key/references and schedule application reconciliation.
    return err
}
```

`ErrInvalidRequest` and `ErrUnsupported` require changing the input or operation,
not waiting. `ErrUnauthorized`/`ErrForbidden` require fixing credentials or access.
`ErrDeclined` requires an application-owned buyer recovery path. Do not treat an
unknown payment status as succeeded. Rate limits and unavailable services still
need a bounded retry/reconciliation policy, not an unconditional charge retry.

> [!WARNING]
> Retrying an uncertain charge with a new key can create another payment.
> Persist the original key and provider references, observe current state, and
> retry only under that operation's documented provider guarantees.

Driver-owned clients do not automatically replay effectful requests. Supplied
native clients/transports retain the policies documented by their driver.
Idempotency retention and replay responses differ between providers; they do
not establish permanent exactly-once financial effects.

Recovery depends on what you saved. If you have a provider ID, retrieve that
resource and reconcile it with your operation record. If creation timed out
without returning an ID, the contract has no lookup by operation key or
`Reference`, and no list/search operation. Replay the identical request only
within the provider's documented key scope and retention; otherwise use the
provider dashboard or a provider-specific investigation before authorizing a new
attempt. A correlation reference does not make an unknown resource discoverable.

## Migrating existing callers

This is a source-breaking change with no compatibility aliases. Replace request
composite literals with `NewCheckoutRequest`, `NewSubscriptionCheckoutRequest`,
`NewCompleteCheckoutRequest`, `NewRefundRequest`, and
`NewCancelSubscriptionRequest`. Pass required values positionally and optional
settings through their typed construction callbacks. Read private request data
with getters such as `request.Amount()` and `request.IdempotencyKey()`; provider
result records still expose fields.

Requests are immutable after construction. Callbacks run once synchronously in
order, including when required inputs are invalid. A supplied nil callback
panics; an expanded nil or empty callback slice supplies no edits. Retaining a
draft or changing a returned amount copy cannot mutate a published request.
`Err()` retains the first invalid input even if a later setting replaces it.
`Validate(ctx)` checks the context first, then that construction error; facade
calls validate before dispatch. Build a new request to correct invalid input.

Valid request JSON keeps its existing field names and shapes. Encoding invalid
requests fails; decoding validates temporary state before replacing the receiver.
Private fields cannot be assigned to bypass these checks. `Money` remains a
plain value; `NewMoney(currency, minor)` and `ParseMoney(currency, decimal)` return
checked exact values, not builders.

Replace Stripe's removed `CreateMobilePayment` and `MobilePayment*` types with
`payment.Client.PreparePayment` and typed `Preparation.Action` accessors. Custom
drivers and wrappers must implement or forward the new tenth Driver operation;
unsupported implementations validate first, then return `ErrUnsupported`.

## Optional notification handling

To monitor outbound API calls locally, opt in to an in-process event bus. These
notifications report the observed call result, not financial state: a completed
read or idempotent replay does not prove settlement. Do not use these best-effort
events as durable reconciliation or fulfillment triggers; persist operation
state and verify provider evidence through your application-owned paths.

With the configured `driver` from [client construction](#constructing-the-client),
register a short listener and replace the earlier `payments` client before making
calls. This fragment also imports
`context`, `fmt`, `arkoren.dev/core/event`, and `arkoren.dev/core/service/payment`:

```go
bus := event.New()
stop, err := bus.Listen(payment.OperationCompleted, func(ctx context.Context, observed payment.OperationEvent) error {
    fmt.Println(observed.Operation)
    return nil
})
if err != nil {
    return err
}
defer stop()
payments = payment.New(driver, func(checkout *payment.Builder) {
    checkout.Events(bus)
})
```

Each subsequent driver-returned call emits once: `OperationCompleted` for a nil
error or `OperationFailed` for a non-nil error, including provider rejection or
unsupported operations. The payload contains only the fixed operation name and
bounded error class. `ErrorType == "payment.ErrOutcomeUnknown"` takes precedence
over joined causes; it means the provider may have applied the mutation, not
that money movement failed. The returned error still preserves every cause.
Validation failures, missing drivers, and pre-canceled calls emit nothing.

Listeners run synchronously with the operation context after telemetry ends;
their errors and panics cannot change the API result, but slow listeners delay
its return. No IDs, amounts, URLs, references, keys, handoff secrets, or arbitrary
error text enter the payload. Omitted or nil `Events` disables notifications.

Local call observations do not replace provider-to-application notifications.
Choose [webhook integration](drivers.md) only when your application needs
notifications from the provider without waiting for a browser return. A webhook
is a provider-to-application HTTP notification, separate from the buyer's return
route. The Stripe and PayPal adapters authenticate requests and publish credential-free event
definitions; you explicitly bind callbacks or queue dispatch using the existing
[HTTP receiver](../http-capabilities/webhooks.md).

Mollie's [classic callbacks](drivers.md#receiving-mollie-status-hints) are
unsigned ID hints, not authenticated webhook events. Handle them separately
and retrieve authoritative payment state before acting.

The payment client never registers endpoints, captures in an event decoder,
emits financial business events, or requires a provider notification to return
a result.

For all operations, status types, and validation rules, see
[the payment API reference](https://pkg.go.dev/arkoren.dev/core/service/payment).
