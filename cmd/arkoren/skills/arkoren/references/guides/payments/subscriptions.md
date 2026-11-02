---
title: Recurring Subscriptions
description: Enroll buyers in existing provider plans, observe agreement state, and cancel future renewals explicitly.
order: 60
summary:
  - Enroll buyers using existing provider plans and server-owned approval destinations.
  - Read subscription lifecycle without confusing active agreements with paid entitlements.
  - Request immediate cancellation separately from refunds, proration, and access decisions.
---

# Recurring Subscriptions

Provider-managed subscriptions let the provider collect renewals instead of
your application scheduling repeated checkouts. Arkoren starts enrollment,
reads agreement state, and requests immediate cancellation. Your application
still owns billing-account authorization and paid-access policy.

Use a [configured client](drivers.md) named `payments`, caller context `ctx`,
and `arkoren.dev/core/service/payment`. Stripe and PayPal support this flow;
Mollie's driver does not. Memory simulates lifecycle without collecting renewals.

## Selecting Plans

Create the recurring Price or Plan outside Arkoren, then map your application's
offering to that provider reference on the server. Stripe accepts an existing
recurring Price at quantity one; PayPal accepts its account's Plan ID. These are
not interchangeable catalog records.

Authenticate the buyer and authorize enrollment for the saved billing account.
Choose the plan, correlation reference, and return/cancel destinations from
trusted server configuration. A request-supplied plan or arbitrary redirect must
not select the agreement your backend creates.

## Starting Enrollment

Save the immutable enrollment request, a unique operation key, and the selected
merchant/test-live environment before calling the provider. With `planReference`
loaded from your server-owned mapping, build the approval request:

```go
request := payment.NewSubscriptionCheckoutRequest(planReference,
    "https://shop.example/account/billing", "membership-184-enroll")
```

If you need cancellation navigation and correlation, use this alternative
construction before saving the request:

```go
request := payment.NewSubscriptionCheckoutRequest(planReference,
    "https://shop.example/account/billing", "membership-184-enroll",
    func(details *payment.SubscriptionCheckoutRequestBuilder) {
        details.CancelURL("https://shop.example/membership")
        details.Reference("membership-184")
    })
```

Reference is correlation only, not authorization or idempotency. The constructor
does not check plan existence, create a catalog entry, or grant access.

> [!WARNING]
> Enrollment can take effect before its response or local recording fails.
> Preserve the original request/key and any references returned with an error.
> A later same-key rejection does not resolve earlier uncertainty. Use
> [Payment Recovery](recovery.md) instead of creating another agreement.

Submit the saved request:

```go
enrollment, err := payments.CreateSubscriptionCheckout(ctx, request)
```

After checking the result, save `enrollment.ID` against the initiating account
before sending the buyer to its sensitive `ApprovalURL`. Persist any observed
`SubscriptionID` too. Stripe can allocate that ID only after checkout completes;
PayPal can allocate it while buyer approval is pending. Missing IDs stay absent.

## Reading Agreements

On return, authorize the caller and read the saved checkout with `GetCheckout`.
The return URL is not approval evidence. PayPal subscription approval activates
the agreement without a separate activate call; its completion operation observes
approval rather than capturing a one-time Order. No driver polls for approval.

Once a subscription ID has actually been observed, read it with the same
merchant/environment client:

```go
subscription, err := payments.GetSubscription(ctx, subscriptionID)
```

`SubscriptionStatusActive` describes lifecycle, not successful payment or a
paid-through entitlement. The provider's `NextBillingAt`, when present, is a
schedule observation, not proof of the last collection. Stripe leaves that
field absent; PayPal can supply it. Your application decides access and grace
periods using evidence appropriate to its policy.

Optional [provider notifications](drivers.md) can supply lifecycle and collection
observations, but they neither grant access nor fill missing relationships.
Stripe `invoice.paid` can include zero or externally satisfied invoices; correlate
actual invoice/payment references rather than assuming every active agreement
or completed checkout represents newly collected money. PayPal sale observations
do not enable portable sale lookup/refund.

## Stopping Renewals

Authorize cancellation against the saved billing account and its observed
subscription ID. Record the intended cancellation durably before dispatch;
the cancellation request has no caller-supplied idempotency-key field.

```go
request := payment.NewCancelSubscriptionRequest(subscriptionID,
    func(details *payment.CancelSubscriptionRequestBuilder) {
        details.Reason("Membership canceled by owner")
    })
```

This requests immediate cancellation, not an end-of-period change. It neither
refunds prior payments nor requests proration, and it does not decide application
access. Existing liabilities and in-flight payments can remain. Plan changes and
scheduled cancellation are outside the contract.

```go
err := payments.CancelSubscription(ctx, request)
```

Stripe requests no new final invoice; PayPal accepts active/suspended states
and treats already-canceled as success without another mutation. Unsupported
states fail explicitly. A successful cancellation has no mandatory follow-up
read that could obscure that success.

If cancellation returns an uncertain outcome, preserve the intended operation
and read the known subscription to reconcile. Do not assume that a failed reply
means renewals continue or blindly replay an effectful request. See
[Payment Recovery](recovery.md) for the broader error policy.

For subscription types and validation, see the
[payment API](https://pkg.go.dev/arkoren.dev/core/service/payment).
