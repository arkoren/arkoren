---
title: Mobile Payments
nav: Mobile Payments
description: Prepare a client payment handoff, complete an approved checkout when required, and verify payment through the Go facade.
order: 35
---

# Mobile Payments

## Introduction

Use `payment.Client.PreparePayment` when your application needs a client-side
payment handoff rather than a [hosted checkout](overview.md#starting-a-payment).
The Go backend prepares the handoff, optionally completes an approved checkout,
and reads the payment outcome through the same facade.

Start with a [configured `*payment.Client`](overview.md#constructing-the-client)
named `payments` and a request context named `ctx`. Check
[driver support and configuration](drivers.md) before choosing this flow.
Client-side integration belongs to your application; this guide shows the Go API
calls, not an HTTP server, storage implementation, or client SDK.

## Preparing a payment

Authenticate the caller and price the order on the server before preparing it.
Save one immutable request and idempotency key for the logical payment attempt;
do not generate a new key on every HTTP request. The fragments use this import:

```go
import "arkoren.dev/core/service/payment"
```

Construct the amount in minor units and describe the intended client channel:

```go
amount, err := payment.NewMoney("EUR", 2500)
request := payment.NewPreparePaymentRequest(amount, "order-123-prepare",
    func(details *payment.PreparePaymentRequestBuilder) {
        details.Reference("order-123").Channel(payment.ClientChannelIOS)
    })
```

This requests EUR 25.00. Use `ClientChannelAndroid` for an Android client; the
channel describes the client platform, not acceptance of a particular wallet.
`Reference` is optional correlation, not authorization or an idempotency key.
Leave optional callback and device settings unset unless your
[driver configuration](drivers.md) requires or supports them.

### Preparing and recording the handoff

After saving the request, call the facade:

```go
prepared, err := payments.PreparePayment(ctx, request)
```

`prepared.Action` contains the typed client handoff. `PaymentID`, `CheckoutID`,
and `SessionID` are optional references: save only those actually returned,
together with the protected action, before delivering the handoff. Preparation
does not confirm a payment or prove that money moved.

On errors, retain any returned references alongside the error. If dispatch or
local recording was interrupted, keep the attempt unresolved and follow
[uncertain-outcome recovery](overview.md#handling-uncertain-outcomes), rather
than creating another payment.

## Delivering the typed action securely

Return the saved preparation as JSON only to the authenticated owner. With
`encoding/json/v2` imported, encode it directly:

```go
body, err := json.Marshal(prepared)
```

`body` contains the references and typed `action` for your client to consume.
There is no backend vendor switch or driver downcast. JSON intentionally includes
handoff credentials: deliver it over HTTPS with `Cache-Control: no-store`, and
exclude response bodies from logs and analytics. Diagnostic `fmt`/`slog`
redaction does not make JSON safe to log.

For reconnects, reauthorize the caller and return the saved handoff only while
its protocol and lifetime permit reuse. There is no universal `GetPreparation`
or expiry API. A client approval callback or return link is not payment evidence.

## Completing an approved checkout

After client approval, load the saved references under the same configured
account/environment. Do not accept replacement provider IDs from the caller.
If the preparation has no `CheckoutID`, skip this step and read the saved
`PaymentID` instead.

For a saved checkout, use `GetCheckout` to observe approval. The following fragment
assumes `checkoutID`, the server-priced `amount`, and a separate saved
`completionKey`, with `errors` imported for application errors. Completion is an
explicit mutation: call it from an authorized POST or coordinated worker, never
a status GET or return-link handler:

```go
checkout, err := payments.GetCheckout(ctx, checkoutID)
if err != nil {
    return err
}
if checkout.ID != checkoutID || checkout.Amount == nil || *checkout.Amount != amount {
    return errors.New("checkout does not match order")
}
if checkout.NeedsCompletion {
    if checkout.Status != payment.CheckoutStatusApproved {
        return errors.New("checkout is not approved")
    }
    checkout, err = payments.CompleteCheckout(ctx,
        payment.NewCompleteCheckoutRequest(checkoutID, completionKey))
}
```

Save any observed `checkout.PaymentID`, preserving earlier references when a
later response omits
them. Record completion results, including references returned with errors,
before proceeding. `NeedsCompletion == false` means no explicit completion is
required; it does not mean the payment succeeded.

## Verifying the payment

Use the saved financial `paymentID`, not a checkout or session ID. With `errors`
imported and the expected server-priced `amount`, read and check the payment:

```go
observed, err := payments.GetPayment(ctx, paymentID)
if err != nil {
    return err
}
if observed.ID != paymentID || observed.Amount != amount {
    return errors.New("payment does not match order")
}
```

Only `observed.Status == payment.StatusSucceeded` establishes successful
collection. Pending, unknown, requires-action, failed, and canceled states do
not authorize fulfillment. Commit fulfillment once against your saved order;
keep that application transaction separate from a read-only status endpoint.

If no financial ID has been observed, keep the attempt unresolved. Never convert
a checkout/session ID into a payment ID. The contract has no universal session
lookup or lookup by idempotency key. Use bounded current-state reads when a
resource ID is known, and follow the
[recovery limits](overview.md#handling-uncertain-outcomes) otherwise.

## Handling retries and refunds

Arkoren does not automatically retry these mutations or persist attempts.
Coordinate concurrent requests in your application and retain the original
request, keys, configured account/environment, and observed references. A
timeout, expired claim, or recording failure does not prove that no effect
occurred. Check `errors.Is(err, payment.ErrOutcomeUnknown)` and preserve context
errors; replay only under the driver's documented idempotency guarantees.

A later same-key call that is rejected or fails before dispatch does not by itself
resolve an earlier uncertain attempt. Keep the saved uncertainty and original key until
reconciliation supplies the missing evidence; neither the latest error nor an SDK
callback authorizes a fresh-key payment or automatic fulfillment.

Refunds use the same facade and the saved financial payment ID. Follow
[refunding a payment](overview.md#refunding-a-payment) for `NewRefundRequest`,
`RefundPayment`, and `GetRefund`; an accepted pending refund is not returned money.

## Testing the shared backend

### Testing the full provider boundary

Use the [payment fake](testing.md#testing-the-native-backend-journey) to exercise
preparation, explicit completion, pending/succeeded reads, refunds, and uncertain
responses without a provider call. That page contains the complete test; the
fragments here demonstrate individual API operations.

Test your application's authorization, durable recording, concurrent attempts,
and one-time fulfillment separately. Sandbox tests additionally verify the
provider and client approval boundary; fake tests do not exercise that boundary.

For all types and options, see the [payment API](https://pkg.go.dev/arkoren.dev/core/service/payment).
