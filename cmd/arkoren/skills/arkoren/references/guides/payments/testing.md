---
title: Testing Payments
nav: Testing
description: Exercise explicit local lifecycles or script provider outcomes without moving money.
order: 30
---

# Testing Payments

## Introduction

Use `memorypayment` to exercise approval, completion, settlement, and cancellation
as separate local steps. Use `paymenttest.Fake` when your application must react
to particular provider replies or ambiguous failures. Neither integration makes
network calls, schedules renewals, or settles money.

## Simulating a payment

Import `arkoren.dev/core/service/payment` and
`arkoren.dev/core/service/payment/memorypayment`. The fragments below use
`t *testing.T` and `ctx := t.Context()`. Construct `simulator := memorypayment.New()` and
`payments := payment.New(simulator)`. After your test creates `checkout` using
the [ordinary request](overview.md#starting-a-payment), supply approval explicitly:

```go
err = simulator.ApproveCheckout(ctx, checkout.ID)
if err != nil {
    t.Fatal(err)
}
checkout, err = payments.CompleteCheckout(ctx,
    payment.NewCompleteCheckoutRequest(checkout.ID, "order-42-complete"))
if err != nil {
    t.Fatal(err)
}
```

Completion creates a pending payment, not successful collection. Resolve it
explicitly, then retrieve it through the same client your application uses:

```go
err = simulator.ResolvePayment(ctx, checkout.PaymentID, payment.StatusSucceeded)
if err != nil {
    t.Fatal(err)
}
observed, err := payments.GetPayment(ctx, checkout.PaymentID)
if err != nil {
    t.Fatal(err)
}
if observed.Status != payment.StatusSucceeded {
    t.Fatalf("payment status = %q", observed.Status)
}
```

`observed.Status` is now succeeded. Repeating completion cannot create a second
payment. Advancing time does not bill a subscription or settle a pending payment.
Subscription activation uses a separate `ResolveSubscription` control and makes
no claim about invoice payment. See
[the simulator API](https://pkg.go.dev/arkoren.dev/core/service/payment/memorypayment) for state restrictions.

## Scripting application outcomes

With `arkoren.dev/core/service/payment/paymenttest` imported, queue the exact reply
your application should see:

```go
fake := paymenttest.New()
amount, err := payment.NewMoney("USD", 1200)
if err != nil {
    t.Fatal(err)
}
fake.ReplyGetPayment(payment.Payment{
    ID:     "pi_test",
    Status: payment.StatusPending,
    Amount: amount,
}, nil)
payments := payment.New(fake)
```

Inject `payments` into your application and call its operation. A valid
unexpected provider call returns `paymenttest.ErrUnexpectedCall` instead of
fabricating success. Scripts are per operation; a completion reply does not
implicitly create a later payment-read reply.

`Attempted()` and `Accepted()` return copied typed histories. Inspect
`Attempted().ReadPayments`, or use the fake's mutation assertions with
`testing.TB` and request predicates. This separates what was requested from
what the test deliberately models as accepted.

## Testing uncertain mutation outcomes

To model an effect that happened but whose response was lost, script a nonempty
result ID together with `payment.ErrOutcomeUnknown`. For cancellation, use
`ReplyCanceledSubscription(payment.ErrOutcomeUnknown)` explicitly. Queue a
separate retrieval reply for the later reconciliation step.

Assert that your application preserves the original operation key and reconciles
instead of starting another charge with a new key. An entry in `Accepted()`
models provider acceptance, not successful settlement.

Also test an uncertain creation with no returned ID. With `fake` from the
previous section, queue the failure before invoking your application's checkout
operation:

```go
fake.ReplyCreateCheckout(payment.Checkout{}, payment.ErrOutcomeUnknown)
```

The application must retain its saved operation key even though it cannot call
`GetCheckout` without an ID. The contract has no search by reference or key;
assert that your recovery path respects the provider's replay window or asks for
investigation rather than making a fresh charge. A missing `Accepted()` entry
reflects the script, not evidence that a real provider had no effect.

Also script a later call with the same saved key that returns a known rejection,
such as `payment.ErrUnauthorized` or `payment.ErrRateLimited`, after an earlier
`ErrOutcomeUnknown`. Assert that your application's operation record stays
unresolved, preserves the original request/key and merchant/environment binding,
and permits neither a new-key charge nor fulfillment without reconciliation.
The fake's latest reply does not establish what the earlier provider attempt did;
this scenario tests your recovery policy, not a provider-wide replay guarantee.

`FailNext(err)` overrides the next valid operation before consuming its script;
`FailWith(err)` does so until cleared. A one-shot failure takes precedence.
Invalid requests do not consume failures. `Reset()` clears scripts and history.
These are test controls, not a financial ledger or provider replay simulator.

## Testing the native backend journey

The [Go backend lesson](mobile-payments.md) coordinates application persistence,
authorization, completion, and fulfillment. This complete test exercises its
provider boundary: preparation, approved completion, pending/succeeded reads,
and a refund's later progress. `example.native` is a fictitious typed action.
Local variables model authorized saved values, not durable storage/authentication:

```go
package paymentexample_test

import (
    "errors"
    "testing"

    "arkoren.dev/core/service/payment"
    "arkoren.dev/core/service/payment/paymenttest"
)

func TestNativePaymentJourney(t *testing.T) {
    t.Parallel()
    amount, err := payment.NewMoney("USD", 2500)
    if err != nil {
        t.Fatal(err)
    }
    fake := paymenttest.New()
    fake.ReplyPreparePayment(payment.Preparation{
        CheckoutID: "checkout-test",
        Action: payment.NewClientOrder("example.native", "client-order-test"),
    }, nil)
    fake.ReplyGetCheckout(payment.Checkout{
        ID: "checkout-test", Status: payment.CheckoutStatusApproved,
        Amount: &amount, NeedsCompletion: true,
    }, nil)
    fake.ReplyCompleteCheckout(payment.Checkout{
        ID: "checkout-test", Status: payment.CheckoutStatusComplete,
        PaymentID: "payment-test", Amount: &amount,
    }, nil)
    fake.ReplyGetPayment(payment.Payment{
        ID: "payment-test", Status: payment.StatusPending, Amount: amount,
    }, nil)
    fake.ReplyGetPayment(payment.Payment{
        ID: "payment-test", Status: payment.StatusSucceeded, Amount: amount,
    }, nil)
    payments := payment.New(fake)
    prepared, err := payments.PreparePayment(t.Context(),
        payment.NewPreparePaymentRequest(amount, "order-42-native"))
    if err != nil {
        t.Fatal(err)
    }
    checkout, err := payments.GetCheckout(t.Context(), prepared.CheckoutID)
    if err != nil {
        t.Fatal(err)
    }
    if checkout.NeedsCompletion {
        checkout, err = payments.CompleteCheckout(t.Context(),
            payment.NewCompleteCheckoutRequest(checkout.ID, "order-42-complete"))
        if err != nil {
            t.Fatal(err)
        }
    }
    pending, err := payments.GetPayment(t.Context(), checkout.PaymentID)
    if err != nil {
        t.Fatal(err)
    }
    if pending.Status != payment.StatusPending {
        t.Fatal("pending payment must not authorize fulfillment")
    }
    paid := readCollectedPayment(t, payments, checkout.PaymentID, amount)
    assertRefundProgress(t, payments, fake, paid)
}

// readCollectedPayment checks authoritative ID, price, and settlement after
// the caller has established application ownership and merchant binding.
func readCollectedPayment(t *testing.T, payments *payment.Client, id payment.ID, amount payment.Money) payment.Payment {
    t.Helper()
    paid, err := payments.GetPayment(t.Context(), id)
    if err != nil {
        t.Fatal(err)
    }
    if paid.ID != id || paid.Amount != amount {
        t.Fatal("payment does not match the saved order")
    }
    if paid.Status != payment.StatusSucceeded {
        t.Fatal("payment is not collected")
    }
    return paid
}

// assertRefundProgress exercises a full explicit refund and its later read.
// Acceptance alone remains pending; only the scripted later observation succeeds.
func assertRefundProgress(t *testing.T, payments *payment.Client, fake *paymenttest.Fake, paid payment.Payment) {
    t.Helper()
    refundAmount := paid.Amount
    fake.ReplyRefundPayment(payment.Refund{
        ID: "refund-test", PaymentID: paid.ID,
        Amount: refundAmount, Status: payment.RefundStatusPending,
    }, nil)
    refund, err := payments.RefundPayment(t.Context(),
        payment.NewRefundRequest(paid.ID, refundAmount, "order-42-refund-1"))
    if err != nil {
        t.Fatal(err)
    }
    if refund.Status != payment.RefundStatusPending {
        t.Fatal("refund acceptance is not completion")
    }
    fake.ReplyGetRefund(payment.Refund{
        ID: refund.ID, PaymentID: paid.ID,
        Amount: refundAmount, Status: payment.RefundStatusSucceeded,
    }, nil)
    returned, err := payments.GetRefund(t.Context(), refund.ID)
    if err != nil {
        t.Fatal(err)
    }
    if returned.Status != payment.RefundStatusSucceeded {
        t.Fatal("refund is not complete")
    }
}

func TestPreparationOutcomeUnknown(t *testing.T) {
    t.Parallel()
    amount, err := payment.NewMoney("USD", 2500)
    if err != nil {
        t.Fatal(err)
    }
    request := payment.NewPreparePaymentRequest(amount, "order-42-native")
    fake := paymenttest.New()
    fake.ReplyPreparePayment(payment.Preparation{}, payment.ErrOutcomeUnknown)
    _, err = payment.New(fake).PreparePayment(t.Context(), request)
    if !errors.Is(err, payment.ErrOutcomeUnknown) {
        t.Fatal("missing uncertain-outcome classification")
    }
    attempts := fake.Attempted().PreparedPayments
    if len(attempts) != 1 {
        t.Fatal("unexpected preparation dispatch count")
    }
    if attempts[0].IdempotencyKey() != request.IdempotencyKey() {
        t.Fatal("original operation key was not preserved")
    }
}
```

Scripts are independent, not an implicit ledger. Invoke your real helpers in
application tests and assert durable claims, ownership, no-store/protected resume,
one fulfillment commit, missing/mismatched references, unapproved checkout, and
recording crashes. An uncertain reply must preserve the saved key and block fresh
dispatch. These tests establish no native UI, real approval, durability, or money
movement; [sandbox coverage](#verifying-provider-behavior) is separate.

## Checking driver conformance

When implementing a driver for your application, run `paymenttest.Run` with a
fresh `Harness` per scenario. The harness supplies the complete Driver and
test-only approval/settlement hooks. Full runs also need an existing
`PlanReference` and `ActivateSubscription` hook. These controls update a fixture
or simulator, not production Driver methods, and must not wait for a real buyer.

`Harness.Amount` chooses the one-off request's positive minor-unit amount and
currency; zero selects 1200 USD. Refunds use the observed payment currency.
If the approved/completed checkout does not yet expose a payment ID, supply
`ResolvePaymentID(ctx, checkoutID) (payment.ID, error)` to observe it with one
bounded fixture step. Omit that hook when the checkout already includes the ID.
The suite does not poll or manufacture a relationship.

Select limitations only when they describe the actual driver or test fixture:

| Option | Meaning and remaining coverage |
| --- | --- |
| `WithoutApproval()` | Omits lifecycle journeys, retaining validation and declared unsupported subscription/refund-lookup checks. Completion/refund-creation checks are skipped with their journey. It does not declare approval unsupported. |
| `WithoutCompletion()` | Requires valid completion to return `ErrUnsupported`; the approved checkout must not require completion and must remain unchanged. |
| `WithoutRefunds()` | Requires valid refund creation to return `ErrUnsupported` without changing the payment. |
| `WithoutRefundLookup()` | Requires valid refund reads to return `ErrUnsupported`; refund creation still runs unless separately disabled. |
| `WithoutSubscriptions()` | Requires subscription creation, lookup, and cancellation to return `ErrUnsupported`; no plan or activation hook is needed, and one-off journeys still run. |
| `WithoutPreparation()` | Requires a valid preparation to return `ErrUnsupported` with no handoff. Preparation is checked independently of hosted approval journeys, including with `WithoutApproval()`. |

For a controlled Mollie fixture, import `paymenttest` and use its actual
unsupported-operation declarations. With `factory func(*testing.T)
paymenttest.Harness` supplying fresh fixtures, including a supported EUR amount:

```go
paymenttest.Run(t, factory,
    paymenttest.WithoutPreparation(),
    paymenttest.WithoutCompletion(),
    paymenttest.WithoutSubscriptions(),
)
```

This still checks one-off payment/refund journeys. Do not add
`WithoutApproval()` merely to make missing hooks pass: that would omit those
journeys instead of proving them.

## Verifying provider behavior

Vendor wire fixtures prove serialization, mapping, and attempts—not live
settlement. Actual sandbox buyer approval and signed delivery require separate,
dedicated test configuration. Never substitute production credentials or assume
an HTTP fixture establishes a provider retention guarantee.

Opt-in provider tests have narrower scopes than a complete buyer journey:

| Provider | Sandbox coverage when explicitly enabled | Not established |
| --- | --- | --- |
| Stripe | Native intent preparation/read, plus synthetic test-card confirmation and refund/read. Confirmation belongs to test-only SDK code, not `PreparePayment`. | Native device wallets, PaymentSheet UI, 3DS, or signed delivery. |
| PayPal | Unapproved sandbox hosted Order creation and read. | Native preparation against the remote provider, SDK/device approval, capture, subscription collections, or signed delivery. |
| Mollie | Read-only profile authentication and an optional existing test payment read. | Hosted creation/approval, refunds, or callbacks. |

These are coverage descriptions, not a claim that those remote tests were run
or that any live-money flow was verified. Local fixture success does not expand
that coverage.

The opt-in checks require explicit test configuration; short mode skips them.
For the toolkit's sandbox tests, Stripe uses build tag `sandbox` and
`ARKOREN_STRIPE_TEST_KEY` (an `sk_test_` key). PayPal uses `paypal_sandbox`,
`PAYPAL_SANDBOX_TEST=1`, `PAYPAL_CLIENT_ID`, and `PAYPAL_CLIENT_SECRET`. Mollie
uses `integration`, `MOLLIE_INTEGRATION=1`, a test-mode `MOLLIE_API_KEY`, and
optionally `MOLLIE_TEST_PAYMENT_ID` for its existing-payment read. These are not
native device tests, and none of them establishes a complete buyer journey.

For an opt-in sandbox check, use a dedicated merchant account, test credentials,
and a real provider-hosted buyer flow. Save the checkout and payment IDs, confirm
their status with explicit reads, request an explicit partial refund, and read
the refund's own progress. Exercise subscription approval and immediate
cancellation separately on drivers that support them. Verify Stripe/PayPal
notifications through real signed deliveries, not only synthetic JSON. For
Mollie, exercise the unsigned hint route and authoritative API read instead of
expecting a signature. These checks require your own configuration; the examples here
do not claim live provider verification.

For native mobile flows, test your authenticated endpoint's ownership checks,
`no-store` response, and secret-log exclusions independently from SDK UI. Use
Stripe test-mode devices/SDK setup for card, wallet readiness, cancellation,
and additional-authentication paths, and PayPal sandbox SDK approval followed by
explicit server capture. `paymenttest.Fake` scripts the common preparation,
reads, completion, and refunds; it does not simulate either native SDK.
Native UI success must never bypass your backend's fulfillment checks.

For the complete helper surface, see
[the paymenttest API](https://pkg.go.dev/arkoren.dev/core/service/payment/paymenttest).
