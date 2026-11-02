---
title: Mobile Payments
description: Prepare a protected client SDK handoff and verify its financial outcome on the backend.
order: 40
summary:
  - Prepare a typed client SDK handoff from an authorized server-priced order.
  - Protect handoff credentials and complete approved Orders only when required.
  - Verify financial payment state and preserve unresolved attempts across later errors.
---

# Mobile Payments

Use `PreparePayment` when a native client presents the payment UI instead of a
hosted redirect. Your Go backend prepares a typed action for the client, then
checks provider state before fulfilling the order. The client SDK and its UI
remain your application's responsibility. These provider handoffs do not
implement App Store or Google Play billing.

Start with a [configured client](drivers.md) named `payments`, caller context
`ctx`, and `arkoren.dev/core/service/payment`. Stripe supplies a guest card intent;
PayPal supplies a native Order needing later server capture. Mollie does not
support preparation. A channel names a client platform, not wallet acceptance.

## Preparing Handoffs

Authenticate the caller, authorize the order, and calculate its price on the
server. Persist one immutable request, operation key, and merchant/environment
binding before preparation. A client-supplied price or provider ID must not choose
what gets charged. For an authorized EUR 25.00 order on iOS:

```go
amount, err := payment.NewMoney("EUR", 2500)
request := payment.NewPreparePaymentRequest(amount, "book-184-native",
    func(details *payment.PreparePaymentRequestBuilder) {
        details.Channel(payment.ClientChannelIOS)
        details.Reference("book-184")
    })
```

Use `ClientChannelAndroid` for Android. Reference is optional correlation, not
authorization or idempotency. Leave ReturnURL and DeviceReference unset unless
the [selected profile](drivers.md) supports them. Core callback-URI syntax checks
do not establish application ownership; any configured destination still needs
your server-side scheme/host allowlist.

> [!WARNING]
> Preparation can create a provider resource before the response or local save
> fails. Preserve the original request/key and any returned references, even
> with an error. Do not replace an unresolved attempt with a fresh key; follow
> [Payment Recovery](recovery.md).

Submit the saved request:

```go
prepared, err := payments.PreparePayment(ctx, request)
```

`prepared.Action` is the typed client handoff. `PaymentID`, `CheckoutID`, and
`SessionID` are optional observed references, not interchangeable identities.
Save those actually returned and the protected action before delivering it.
Preparation does not confirm, capture, or prove payment.

## Protecting Actions

Deliver the saved preparation only to the authenticated order owner over HTTPS.
With `encoding/json/v2` imported, encode the typed result directly:

```go
body, err := json.Marshal(prepared)
```

JSON intentionally contains handoff credentials. Send it with
`Cache-Control: no-store`, exclude response bodies from logs/analytics, and apply
access control on every resume request. Formatting/`slog` redaction does not make
JSON safe to log. No backend driver downcast is needed to serialize the action.

For reconnects, return the saved action only while its protocol and lifetime
permit reuse. There is no universal preparation lookup or expiry API. Stripe
returns a sensitive `stripe.payment_intent` authorization; PayPal returns a
`paypal.order` action. Neither an SDK success callback nor a return link proves
provider approval or successful collection.

## Completing Orders

If preparation returned only a financial PaymentID, skip checkout completion.
For a saved CheckoutID, first read provider state under the same persisted
merchant/environment. The following uses saved `checkoutID`, expected `amount`,
and `errors`:

```go
checkout, err := payments.GetCheckout(ctx, checkoutID)
if err != nil {
    return err
}
if checkout.ID != checkoutID || checkout.Amount == nil || *checkout.Amount != amount {
    return errors.New("checkout does not match order")
}
if checkout.NeedsCompletion && checkout.Status != payment.CheckoutStatusApproved {
    return errors.New("checkout is not approved")
}
```

An SDK callback cannot bypass these checks. Run required completion from an
authorized POST or coordinated worker, never a read-only status/return GET.
Use the separate completion key already saved for this attempt:

```go
if checkout.NeedsCompletion {
    checkout, err = payments.CompleteCheckout(ctx,
        payment.NewCompleteCheckoutRequest(checkoutID, completionKey))
}
```

Persist results and any returned payment reference, including with errors.
Preserve previously observed references when a later reply omits them.
Completion errors can be outcome-unknown; keep the same operation/key pending
reconciliation. `NeedsCompletion == false` means no explicit completion is
required, not that money was collected.

## Verifying Payments

Use the saved financial `paymentID` rather than a checkout/session ID. With
the same server-derived `amount`, require an authoritative matching observation:

```go
observed, err := payments.GetPayment(ctx, paymentID)
if err != nil {
    return err
}
if observed.ID != paymentID || observed.Amount != amount {
    return errors.New("payment does not match order")
}
if observed.Status != payment.StatusSucceeded {
    return errors.New("payment is not collected")
}
```

Commit fulfillment once against the saved order. Pending, unknown, failed,
canceled, and requires-action states do not qualify. If no financial ID has
been observed, retain the unresolved attempt; never manufacture one from another
resource ID. A read-only status endpoint reports state without fulfilling.

## Recovering Attempts

Arkoren neither persists attempts nor automatically retries these mutations.
Coordinate concurrent preparation and completion calls in your application.
A timeout, expired claim, or failed local save can follow a provider effect;
preserve context errors and inspect `errors.Is(err, payment.ErrOutcomeUnknown)`.
Keep earlier uncertainty even when a later same-key call is rejected or fails
before dispatch. The latest error does not authorize another charge.

[Payment Recovery](recovery.md) explains bounded reads and provider-scoped replay.
[Payment Refunds](refunds.md) uses the same facade with the saved financial ID;
a pending refund is not returned money. Use [Testing](testing.md) for scripted
preparation, approval, completion, and uncertain replies, and test ownership,
durable recording, protected resume, and one-time fulfillment separately.
Sandbox/device checks are needed for the actual SDK approval boundary.

For typed actions and request options, see the
[payment API](https://pkg.go.dev/arkoren.dev/core/service/payment).
