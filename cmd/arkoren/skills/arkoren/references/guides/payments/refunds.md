---
title: Payment Refunds
description: Request explicit refund amounts and read the refund's own progress without confusing acceptance with completion.
order: 50
summary:
  - Request an explicit positive refund amount with its own durable operation key.
  - Read independent refund progress without treating pending acceptance as returned funds.
  - Preserve provider-specific resource handles and avoid duplicate uncertain refunds.
---

# Payment Refunds

A refund is a separate resource whose progress can outlast the request that
created it. Use a [configured client](drivers.md) named `payments`, caller context
`ctx`, and the original financial payment ID saved against your order. Import
`arkoren.dev/core/service/payment` for the request and status types.

## Choosing Amounts

Authorize the refund against the saved order/account and select its persisted
merchant and test/live environment. Do not accept an arbitrary provider ID from
the caller. Calculate the refund under your server-side policy and keep its
currency equal to the original payment's currency.

For EUR 5.00 of an existing EUR payment, construct an exact positive amount:

```go
refundAmount, err := payment.NewMoney("EUR", 500)
```

A full refund also requires an explicit positive amount. Zero does not mean
“the remainder.” The provider enforces the remaining refundable balance,
including concurrent requests; a locally calculated balance is not a reservation.

## Submitting Refunds

Give this logical refund its own durable operation key, distinct from checkout
and completion keys. Save the immutable request, original payment ID, and
merchant/environment before dispatch:

```go
request := payment.NewRefundRequest(paymentID, refundAmount, "book-184-refund-1")
```

Reuse that key only for the identical request within the provider's documented
scope and retention. A second intended partial refund is a different operation;
it must not become a way to repeat an unresolved first refund.

> [!WARNING]
> A failed refund reply can follow an accepted refund. Keep the original
> request/key and any returned refund references even with an error. A later
> rejection or read failure does not settle that earlier attempt. Follow
> [Payment Recovery](recovery.md) rather than issuing a fresh-key refund.

Submit the saved request:

```go
refund, err := payments.RefundPayment(ctx, request)
```

Record the result before telling the buyer it completed. An accepted
`RefundStatusPending` is normal provider progress, not returned money and not
an outcome-unknown error. The facade does not perform a hidden balance lookup
or post-mutation read; a driver can document a required currency preflight.

## Reading Progress

Save the entire `refund.ID`, not a token extracted from its value. To obtain a
later observation, read the saved `refundID` without resubmitting the refund:

```go
observed, err := payments.GetRefund(ctx, refundID)
```

After a successful read, reconcile the returned identity, expected amount, and
any observed payment relationship against your saved operation. Missing
relationships remain absent rather than being guessed.

| Status | Interpretation |
| --- | --- |
| `RefundStatusSucceeded` | The provider confirms refund success. |
| `RefundStatusPending` | The refund is not finalized. |
| `RefundStatusRequiresAction` | Further action is required; funds are not confirmed returned. |
| `RefundStatusFailed` | A known unsuccessful refund outcome. |
| `RefundStatusCanceled` | Canceled before success. |
| `RefundStatusUnknown` | No mapped state; do not treat it as success. |

Your application chooses when to read and how to notify the buyer. Provider
notifications can repeat or arrive out of order, so persist reconciliation
idempotently. Refunding a payment does not cancel a recurring agreement;
[subscription cancellation](subscriptions.md#stopping-renewals) is separate.

## Provider Limits

Stripe checks the PaymentIntent's currency before submitting an explicit refund.
PayPal refunds Payments v2 captures only; subscription sale lookup/refund returns
`ErrUnsupported`. A sale ID cannot be substituted for a capture ID.

Mollie's opaque compound refund handle carries the parent payment needed for
lookup. Preserve it verbatim. Its queued/pending/processing states remain pending;
only `refunded` succeeds. Mollie caches POST replies for one hour per exact
credential, so credential rotation or replay after expiry can duplicate an
unresolved refund. Its classic callback carries a payment ID, not this compound
handle: read the refund IDs already saved by your application.

Use [Testing](testing.md) to script pending, succeeded, and uncertain replies
without financial effects. For request and result types, see the
[payment API](https://pkg.go.dev/arkoren.dev/core/service/payment).
