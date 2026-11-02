---
title: Hosted Checkout
description: Start provider-hosted approval, complete eligible checkouts explicitly, and verify collection before fulfillment.
order: 30
summary:
  - Create hosted approval from server-owned pricing and persist its operation identity.
  - Separate read-only buyer returns from authorized, approval-gated merchant completion.
  - Verify matching successful collection before fulfilling the saved order once.
---

# Hosted Checkout

Hosted checkout sends the buyer to a provider's approval page. Your backend
creates the flow, saves its identity, and later checks whether the provider
collected the expected payment. Use a [configured client](drivers.md) named
`payments`, a caller context `ctx`, and `arkoren.dev/core/service/payment`.

## Starting Checkout

Authenticate the buyer and authorize their saved order before creating checkout.
Derive the amount, currency, reference, and redirect destinations from server
data, not request-supplied prices or arbitrary URLs. Save the immutable request,
merchant/environment association, and a unique operation key before dispatch.
Use one key for this logical creation, not a new key for each HTTP retry.

For a server-priced book order, construct an explicit EUR 25.00 request:

```go
amount, err := payment.NewMoney("EUR", 2500)
request := payment.NewCheckoutRequest(
    amount, "https://shop.example/payments/return", "book-184-checkout")
```

`NewMoney` uses exact minor units; provider currency limits still apply.
Request constructors perform no I/O. Requests are immutable; `Err()` exposes
construction errors, and facade calls validate before dispatch. To add
correlation or a cancellation destination, construct this alternative before
saving the request:

```go
request := payment.NewCheckoutRequest(amount,
    "https://shop.example/payments/return", "book-184-checkout",
    func(details *payment.CheckoutRequestBuilder) {
        details.Description("Book order")
        details.Reference("book-184")
        details.CancelURL("https://shop.example/cart")
    })
```

Reference is optional correlation, not authorization, idempotency, or a searchable
provider identity. Hosted redirect validation permits HTTPS and loopback HTTP;
it does not decide whether your application trusts a destination.

> [!WARNING]
> Creation can take effect before a response is lost. Retain the original
> request/key and any references returned with an error. A recording failure or
> later rejection does not prove no effect occurred. Reconcile through
> [Payment Recovery](recovery.md), not a fresh-key checkout.

Submit the saved request:

```go
checkout, err := payments.CreateCheckout(ctx, request)
```

After checking the result, durably associate `checkout.ID` with the order,
initiating account, and merchant/environment before redirecting to
`checkout.ApprovalURL`. Treat the approval URL as sensitive and exclude it from
logs. Preserve any returned payment references even when the response is partial;
creation does not perform a follow-up read to fill missing fields.

## Returning Buyers

A browser return is navigation, not payment evidence. Authorize the returning
caller against the saved order and use its persisted `checkoutID`, never a
replacement provider ID supplied by the browser:

```go
checkout, err := payments.GetCheckout(ctx, checkoutID)
```

This read neither captures funds nor activates an agreement. A read-only return
or status endpoint can report the observation without triggering a mutation.
Keep earlier references if a later snapshot omits them; missing relationships
do not authorize casting another resource's ID.

## Explicit Completion

PayPal Orders need merchant capture after buyer approval. Stripe hosted Checkout
collects through its UI; its completion operation only observes. Mollie's
automatic-capture flow reports `NeedsCompletion == false` and explicit completion
returns `ErrUnsupported`.

When completion is required, use an authorized POST or coordinated worker, not
a status GET or browser-return GET. Save a separate `completionKey` and coordinate
it across all completion paths. With the saved `checkoutID`, expected `amount`,
and `errors` imported, first check the retrieved checkout:

```go
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

Only then submit required completion with the persisted key:

```go
if checkout.NeedsCompletion {
    checkout, err = payments.CompleteCheckout(ctx,
        payment.NewCompleteCheckoutRequest(checkoutID, completionKey))
}
```

Record the result and any returned references before continuing, including those
returned with errors. Completion can have an uncertain outcome too: preserve
the original key and reconcile instead of issuing another capture. An approved
PayPal Order can expire without capture if no application path completes it.

## Verifying Collection

Completion and `NeedsCompletion == false` do not prove success. For this paid
order, use the financial `PaymentID` observed and saved from checkout, under its
same merchant/environment. If none has been observed, keep the attempt unresolved;
there is no universal search by operation key or reference.

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

Only a matching succeeded payment satisfies collection. Commit fulfillment once
against the authorized order; pending, unknown, or requires-action states do not
qualify. Checkout `StatusNotRequired` can describe a no-payment-required flow,
not newly collected funds. Your application owns when to retrieve state and
when to fulfill; optional [provider notifications](drivers.md) install no polling
or automatic fulfillment.

To return funds later, use [Payment Refunds](refunds.md). For request types and
status definitions, see the
[payment API](https://pkg.go.dev/arkoren.dev/core/service/payment).
