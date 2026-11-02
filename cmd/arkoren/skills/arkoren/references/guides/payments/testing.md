---
title: Testing Payments
nav: Testing
description: Simulate payment lifecycles or script uncertain provider replies without network calls or money movement.
order: 90
summary:
  - Simulate approval and settlement separately or script precise provider replies.
  - Test uncertain attempts, client handoffs, and pending refund progress without moving money.
  - Choose conformance coverage deliberately and distinguish local fixtures from sandbox buyer flows.
---

# Testing Payments

Use `memorypayment` when a test needs coherent local payment state. Use
`paymenttest.Fake` when it needs an exact provider reply, including a lost
response or missing relationship. Neither moves money, runs a buyer UI, or
schedules renewals.

The fragments belong inside your application tests with `t *testing.T` and
`ctx := t.Context()`. Import `arkoren.dev/core/service/payment` plus the simulator
or fake subpackage used below. They demonstrate the provider boundary; inject
the resulting `payments` client into your real application helpers to test
authorization, durable recording, and fulfillment policy.

## Simulating Payments

Start a fresh simulator per test. It is bundled with core and requires no account:

```go
import "arkoren.dev/core/service/payment/memorypayment"

simulator := memorypayment.New()
payments := payment.New(simulator)
```

Create an ordinary checkout with an explicit amount and key, as in
[Hosted Checkout](hosted-checkout.md#starting-checkout). With that `checkout`
already created, supply buyer approval and merchant completion separately:

```go
if err := simulator.ApproveCheckout(ctx, checkout.ID); err != nil {
    t.Fatal(err)
}
checkout, err = payments.CompleteCheckout(ctx,
    payment.NewCompleteCheckoutRequest(checkout.ID, "book-184-complete"))
if err != nil {
    t.Fatal(err)
}
if checkout.PaymentStatus != payment.StatusPending {
    t.Fatal("completion must leave collection pending")
}
```

Completion creates a pending payment, not financial success. Resolve it explicitly
before reading through the facade:

```go
if err := simulator.ResolvePayment(ctx, checkout.PaymentID, payment.StatusSucceeded); err != nil {
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

The read reports the state you supplied; it proves no external settlement.
Repeating completion cannot create another payment. Advancing time changes
neither pending settlement nor recurring collection. `ResolveSubscription`
supplies agreement lifecycle separately and creates no invoice/payment.

## Scripting Outcomes

For a policy test that must see a pending payment, script that exact reply.
With a checked `amount` from `payment.NewMoney("EUR", 2500)`:

```go
import "arkoren.dev/core/service/payment/paymenttest"

fake := paymenttest.New()
fake.ReplyGetPayment(payment.Payment{
    ID:     "payment-test",
    Amount: amount,
    Status: payment.StatusPending,
}, nil)
payments := payment.New(fake)
```

Call your application's payment-checking operation with that client and assert
that it does not fulfill. Scripts are independent FIFO queues per operation:
a completion reply never creates a payment-read reply or a ledger entry.
A valid unexpected call returns `paymenttest.ErrUnexpectedCall`, not invented
success.

`Attempted()` and `Accepted()` return copied typed histories. Inspect
`Attempted().ReadPayments` for reads or use mutation assertion helpers with
`testing.TB` and request predicates. Acceptance models what the test declares
accepted, not successful collection.

`FailNext(err)` overrides the next valid operation without consuming its script;
`FailWith(err)` persists until cleared. One-shot failure takes precedence.
Invalid input consumes neither failure. `Reset()` clears scripts and history.
These controls are not a financial ledger or provider replay simulator.

## Testing Uncertainty

To model accepted creation with a lost reply, queue an observed checkout ID with
`ErrOutcomeUnknown`, then separately queue a later retrieval:

```go
fake.ReplyCreateCheckout(payment.Checkout{
    ID: "checkout-test",
}, payment.ErrOutcomeUnknown)
fake.ReplyGetCheckout(payment.Checkout{
    ID:     "checkout-test",
    Status: payment.CheckoutStatusOpen,
}, nil)
```

Invoke your real creation/reconciliation helpers and assert they save the
original request/key, merchant/environment, and returned ID. `Accepted()` records
the modeled effect even with the uncertain error. For accepted cancellation
whose reply was lost, use `ReplyCanceledSubscription(payment.ErrOutcomeUnknown)`;
ordinary `ReplyCancelSubscription(err)` does not declare acceptance with an error.

Also test creation with no returned ID, followed by a known rejection on a later
same-key call:

```go
fake.ReplyCreateCheckout(payment.Checkout{}, payment.ErrOutcomeUnknown)
fake.ReplyCreateCheckout(payment.Checkout{}, payment.ErrUnauthorized)
```

After your application consumes these replies, assert that durable uncertainty
remains, the original request/key stays unchanged, and neither fresh-key dispatch
nor fulfillment is authorized. The later error does not establish the earlier
provider effect. A missing fake `Accepted()` entry reflects the script, not
proof that a real provider did nothing.

With no resource ID, portable lookup by key/Reference is unavailable. Test that
your [recovery policy](recovery.md) respects provider replay scope/retention or
requires investigation instead of manufacturing an ID or another charge.

## Testing Handoffs

For [Mobile Payments](mobile-payments.md), use a fictitious protocol to isolate
backend handling from a native SDK:

```go
fake.ReplyPreparePayment(payment.Preparation{
    CheckoutID: "checkout-test",
    Action:    payment.NewClientOrder("example.native", "client-order-test"),
}, nil)
```

Script `ReplyGetCheckout` with an approved checkout, expected amount, and
`NeedsCompletion: true`; then script `ReplyCompleteCheckout` with a financial
PaymentID. Queue pending and succeeded `ReplyGetPayment` replies independently.
Call the application's actual helpers and assert that approval is checked,
completion uses its saved key, and only the matching succeeded payment permits
one fulfillment commit.

For refund progress, submit a pending reply independently of the later read.
With saved `paymentID` and explicit `refundAmount`:

```go
fake.ReplyRefundPayment(payment.Refund{
    ID:        "refund-test",
    PaymentID: paymentID,
    Amount:    refundAmount,
    Status:    payment.RefundStatusPending,
}, nil)
fake.ReplyGetRefund(payment.Refund{
    ID:        "refund-test",
    PaymentID: paymentID,
    Amount:    refundAmount,
    Status:    payment.RefundStatusSucceeded,
}, nil)
```

Assert that your application reports the first outcome as pending and only the
second as confirmed refund success. Also test mismatched/missing references,
unapproved checkout, protected/no-store JSON, owner-only resume, secret-log
exclusions, recording crashes, and concurrent requests. Fake success does not
prove native approval, persistence, or HTTP security.

## Checking Conformance

If you implement a driver for your application, use `paymenttest.Run` with a
fresh `Harness` per scenario. Its complete Driver and bounded fixture hooks
supply approval, settlement, and subscription activation without waiting for
a real buyer. The factory owns cleanup through `t`.

Full journeys need `PlanReference` and `ActivateSubscription`. `Harness.Amount`
selects a positive supported one-off amount; zero selects 1200 USD, and refunds
use the observed payment currency. If completion exposes no PaymentID, provide
`ResolvePaymentID` for one bounded fixture observation. The suite neither polls
nor invents the relationship.

| Option | Declared limitation |
| --- | --- |
| `WithoutApproval()` | Omits lifecycle journeys, retaining validation and standalone unsupported subscription/refund-lookup checks. Completion/refund-creation checks are omitted with their journey; it does not declare approval unsupported. |
| `WithoutCompletion()` | Requires `ErrUnsupported` and an unchanged approved checkout that does not need completion. |
| `WithoutRefunds()` | Requires unsupported refund creation without changing the original payment. |
| `WithoutRefundLookup()` | Requires unsupported refund reads; creation still runs unless separately disabled. |
| `WithoutSubscriptions()` | Requires unsupported enrollment, lookup, and cancellation; no plan/activation hook is needed. |
| `WithoutPreparation()` | Requires unsupported valid preparation with no handoff; preparation checks remain independent of hosted approval. |

With `factory func(*testing.T) paymenttest.Harness` supplying fresh controlled
Mollie fixtures, supported EUR amounts, and approval/settlement hooks, declare
only the operations actually unsupported:

```go
paymenttest.Run(t, factory,
    paymenttest.WithoutPreparation(),
    paymenttest.WithoutCompletion(),
    paymenttest.WithoutSubscriptions(),
)
```

This still exercises one-off payments and refunds. Do not add WithoutApproval
to hide absent hooks: doing so omits the journey rather than proving it.

## Sandbox Boundaries

Wire fixtures prove serialization, mapping, and attempt counts—not live settlement.
The existing opt-in provider checks also have narrower scopes than a buyer journey:

| Provider | Opt-in coverage | Not established |
| --- | --- | --- |
| Stripe | Intent preparation/read and synthetic test-card confirmation with refund/read. | Native devices, PaymentSheet, wallets, 3DS, or signed delivery. |
| PayPal | Unapproved hosted sandbox Order creation/read. | Native preparation, SDK approval, capture, subscription collections, or signed delivery. |
| Mollie | Read-only profile authentication and optional existing test payment read. | Hosted creation/approval, refunds, or callbacks. |

These describe coverage, not tests run for this guide. Never substitute production
credentials or infer provider retention from a local fixture. With a dedicated
test merchant and your own opt-in configuration, separately exercise hosted
buyer approval, explicit reads/completion, partial refund progress, and supported
subscription approval/cancellation. Stripe/PayPal notification verification needs
real signed deliveries, not just synthetic JSON; Mollie needs the unsigned-hint
route plus authoritative API read.

Test native UI separately with the provider's test-mode SDK/device setup,
including cancellation and additional authentication. PayPal SDK approval still
needs explicit server capture. Client UI success must never bypass backend
ownership and collection checks.

For controls, histories, and harness options, see the
[paymenttest API](https://pkg.go.dev/arkoren.dev/core/service/payment/paymenttest)
and [memorypayment API](https://pkg.go.dev/arkoren.dev/core/service/payment/memorypayment).
