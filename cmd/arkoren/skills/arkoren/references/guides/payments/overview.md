---
title: Payments Overview
nav: Overview
description: Understand payment resources and read provider evidence before fulfilling an order.
order: 10
summary:
  - Distinguish checkout approval, payment collection, refunds, and subscription lifecycle.
  - Read matching provider payment evidence before committing fulfillment.
  - Construct immutable requests and choose the guide for each payment task.
---

# Payments Overview

A buyer's approval and a provider's collection are different events. Use
`payment.Client` to request payment operations and read their outcomes, while
your application decides whether those outcomes satisfy an authorized order.
Start with a [configured client](drivers.md) named `payments` and a caller context
named `ctx`; the facade needs no application container, worker, or webhook.

## Payment Resources

A **checkout** is an approval flow. Its completion does not necessarily mean
money moved. A **payment** describes an individual money movement; a **refund**
has its own progress toward returning funds. A **subscription** is a recurring
agreement, not evidence that its latest invoice was paid.

The provider owns these resources and subscription renewals. Your application
owns authorization, server-side pricing, merchant and test/live associations,
durable operation records, and fulfillment. Arkoren stores no local billing
projection and supplies no tax, catalog, invoice, dispute, or entitlement engine.

Resource IDs are opaque provider-scoped handles, not authorization. Save them
against the initiating account and order, and preserve their complete values.
Never convert a checkout or session ID into a financial payment ID. Missing
relationships stay absent; successful mutations are not followed by hidden reads
to fill them in.

## Reading Payments

To check an order, first authorize access to its saved payment ID and select the
client for its persisted merchant/environment. With `paymentID` and `amount`
loaded from that order, retrieve the financial resource:

```go
import "arkoren.dev/core/service/payment"

observed, err := payments.GetPayment(ctx, paymentID)
```

`GetPayment` only reads state. Before fulfilling, require a successful read,
the expected identity and amount (including currency), and successful collection.
With `errors` imported, the consequential checks are:

```go
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

Commit fulfillment once against the saved order. Pending, requires-action,
unknown, failed, and canceled payments cannot satisfy that success condition.
A browser return, SDK callback, active subscription, or successful API call
alone cannot replace this evidence.

Amounts use exact integer currency minor units. `NewMoney("EUR", 2500)` represents
EUR 25.00; `NewMoney("JPY", 2500)` represents JPY 2500. `ParseMoney` accepts exact
decimal text without rounding. `CurrencyExponent` lists supported currencies;
unknown currencies return `ErrUnsupported`, and providers can accept fewer.

## Request Values

Construct operation inputs with their typed `New*Request` functions rather than
assigning request fields. Required values are positional; optional settings use
construction callbacks. Published requests expose getters and are immutable.
Callbacks run once synchronously in order, even if required input is invalid;
a supplied nil callback panics, while an expanded empty/nil callback slice does
not supply an edit.

`Err()` preserves the first invalid construction input, even after a later
setting replaces it. Build a new request to correct it. `Validate(ctx)` checks
the context before construction validity, and facade calls validate before
dispatch. Valid request JSON preserves its field names; encoding invalid
requests fails, and decoding validates temporary state before replacing a value.
`Money` remains a plain value, not a builder.

## Choosing Flows

Use [Hosted Checkout](hosted-checkout.md) for a provider-hosted approval page,
or [Mobile Payments](mobile-payments.md) to prepare a typed client SDK handoff.
Both require server-owned pricing, durable operation keys, and provider evidence
before fulfillment. Use [Payment Refunds](refunds.md) to return an explicit amount,
and [Recurring Subscriptions](subscriptions.md) to enroll in an existing provider
plan and stop future renewals.

> [!WARNING]
> An interrupted mutation may already have taken effect. Preserve its original
> request, operation key, merchant/environment, and any returned references.
> A later error does not erase earlier uncertainty. Follow
> [Payment Recovery](recovery.md) rather than submitting a new-key charge.

Focused fragments keep error results visible and omit routine propagation;
production code handles every error. Checks that determine ownership, recovery,
or fulfillment remain explicit. [Testing](testing.md) demonstrates local outcomes
without moving money.

Provider notifications are optional, separate integrations described under
[Drivers](drivers.md). They install no automatic capture or fulfillment path.
Mollie's [unsigned callbacks](mollie-callbacks.md) require an authenticated lookup,
not trust in the incoming request.

For types, constructors, and validation rules, see the
[payment API](https://pkg.go.dev/arkoren.dev/core/service/payment).
