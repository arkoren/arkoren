---
title: Payment Drivers
nav: Drivers
description: Compare memory, Stripe, PayPal, and Mollie for hosted checkout, native mobile payment initialization, refunds, and subscriptions.
order: 40
---

# Payment Drivers

## Introduction

Choose memory for simulation, Stripe for hosted or native card payments and
recurring Prices, PayPal for hosted/native wallet Orders and subscription Plans, or Mollie for
hosted one-off card payments and refunds. No driver requires a webhook or worker;
replacing drivers does not migrate provider resources or credentials.

Stripe and PayPal webhook adapters report outcomes without a browser return;
Mollie's classic callback is an unsigned hint requiring an authenticated read.
Neither completes payments or grants access automatically.
The examples below are focused fragments, with credentials loaded from your
application's configuration. Import
`arkoren.dev/core/service/payment` for the portable client.

## Compatibility matrix

Compare Arkoren's outbound payment operations and optional webhook companions,
not the providers' full billing products. Memory implements local simulations;
it has no hosted buyer flow, real collection, or renewal scheduler.

| Driver | Hosted buyer approval | Merchant completion | Payment lookup | Refund creation and lookup | Subscription enrollment | Immediate cancellation | Client preparation | Authenticated notifications | Facade call observations |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No — Non-routable handles. | Yes — Simulated. | Yes — Simulated. | Yes — Simulated. | Partial — No renewals. | Yes — Simulated. | Partial — Pending payment and inert token. | No | Yes — Opt-in, in-process only. |
| [Stripe](#stripe) | Yes | No — Observes hosted completion. | Yes | Yes | Partial — Existing recurring Price, quantity one. | Yes | Partial — Guest card intent; no return URL or device reference. | Yes — Separate receiver. | Yes — Opt-in, in-process only. |
| [PayPal](#paypal) | Yes | Yes — Order capture. | Partial — Captures only. | Partial — Captures only. | Partial — Existing Plan. | Partial — Active/suspended; canceled succeeds. | Partial — Native Order; creation-only risk data; no web/backend return URL. | Yes — Separate receiver. | Yes — Opt-in, in-process only. |
| [Mollie](#mollie) | Partial — One-off creditcard, automatic capture only. | No — ErrUnsupported. | Yes | Yes — Compound refund handle required. | No | No | No | No — Unsigned ID hints only. | Yes — Opt-in, in-process only. |

Facade call observations come from `payment.Client`, not the driver or provider.
Enable them with `Builder.Events` for any backend; direct driver calls bypass
them. These best-effort API-call results include reads and replays, never prove
settlement, and do not provide durable reconciliation or fulfillment triggers.
Provider webhooks and persisted state remain application-owned. See
[local operation monitoring](overview.md#optional-notification-handling).

Stripe completion observes hosted collection without charging again; PayPal
Orders require explicit capture after approval; Mollie's supported hosted cards
capture automatically. Approval/subscription status is not payment evidence,
and refund acceptance can precede returned funds. Cancellation is immediate,
without scheduling, proration, or refunds. NextBillingAt is absent for Stripe
and provider-supplied for PayPal, never collection evidence. PayPal sale events
do not enable sale reads/refunds. Preserve keys and
[reconcile uncertainty](overview.md#handling-uncertain-outcomes). Separate
notification receivers never complete payments or grant access automatically.

### Preparing optional notification handlers

For the Stripe and PayPal notification examples, use an application `*router.Router` named `r`
and an application-owned `accept` function of type
`webhook.AcceptFunc[payment.Observation]` that durably stores or queues
observations and returns its error. Import `arkoren.dev/core/http/webhook`,
check construction errors, then configure the public HTTPS route and bound
events in the provider dashboard.

By default, core returns 200 after acceptance (also for unbound authenticated
types), 503 on decode/accept failure, and 400 on rejected authentication. A body
over the configured limit returns 413. Failure responses are rendered and committed
by the receiver before its error returns: 4xx receipt errors match
`webhook.ErrRejected`, and 503 receipt errors match `webhook.ErrUnavailable`, while
retaining their original causes. An application-wide error mapper cannot rewrite
that provider response; do not log unwrapped causes without redaction. A 200
does not mean asynchronous processing finished. The
[receiver guide](../http-capabilities/webhooks.md) covers retries and body limits.

`webhook.Delivery` owns native event identity; `payment.Observation` carries
facts/time. `Event.Map` adapts queue DTOs. Scope deduplication by provider,
account, environment, and consumer; cache deduplication replaces neither
operation keys nor a durable inbox/outbox.

## Memory

Use the memory driver when no financial transaction should occur. It is bundled
with `arkoren.dev/core`; no provider account or separate installation is needed.

### Connecting

Construct a simulator and wrap it in the portable client:

```go
import (
    "arkoren.dev/core/service/payment"
    "arkoren.dev/core/service/payment/memorypayment"
)

simulator := memorypayment.New()
payments := payment.New(simulator)
```

### Configuration

The driver has no optional settings, credential input, or native client.
Every `New()` creates independent state; no lifecycle hooks are needed.

### Simulating checkout

Create a checkout with the [ordinary request](overview.md#starting-a-payment),
then supply approval and settlement explicitly using the
[local simulation example](testing.md#simulating-a-payment). A successful local
payment status confirms the state you supplied, not a financial transaction.

### Behavior and limits

State is isolated and lost on exit. Controls supply outcomes without settlement
or recurring billing. `PreparePayment` creates only a pending payment and inert
`memory.authorization` token, not a checkout or usable SDK credential; explicitly
`ResolvePayment` to supply its outcome. Use the
[scripted fake](mobile-payments.md#testing-the-full-provider-boundary) for uncertain replies.

### API references

For controls and ownership, see
[the memorypayment API reference](https://pkg.go.dev/arkoren.dev/core/service/payment/memorypayment).

## Stripe

Use Stripe-hosted checkout with a Stripe account and test-mode API key. Create a
recurring Price in Stripe before subscription enrollment; the one-time checkout
example does not need a precreated Price.

### Installation

From your application's module directory, install the vendor module:

```sh
go get arkoren.dev/driver/stripe
```

### Connecting

With an API key loaded from your application's configuration, construct the
driver independently of notifications:

```go
import "arkoren.dev/driver/stripe/stripepayment"

driver, err := stripepayment.New(apiKey)
payments := payment.New(driver)
```

`New` performs no network I/O and creates an SDK client without mutation retries.
Checkout Session, PaymentIntent, Refund, and Subscription IDs stay distinct and
unmodified. Enrollment takes an existing recurring Price at quantity one.

The API key must be nonempty and contain no whitespace or control characters.
Invalid keys return `payment.ErrInvalidRequest` before any configuration callback
runs. With a valid key, callbacks run once in order on one draft; nil entries and
callback panics propagate. HTTP settings are validated after those callbacks.

### Configuration

To bound requests, import `net/http` and `time` and supply a client at
construction:

```go
driver, err := stripepayment.New(apiKey, func(connection *stripepayment.Builder) {
    connection.HTTPClient(&http.Client{Timeout: 10 * time.Second})
})
```

Omitted/zero timeout selects 30 seconds; negative fails construction. The client
is copied, redirects refused, and transport stays caller-owned. Transports must
not replay mutations. No lifecycle hooks are required.

`NewFrom` preserves caller SDK/transport policies. `Client()` exposes native
operations outside the contract; the driver closes neither client nor transport.
Do not mutate shared clients during requests. Native safety/retries remain yours.

### Creating a checkout

Use the [hosted checkout request](overview.md#starting-a-payment) with `payments`.
Persist its key and returned ID before redirecting to the sensitive approval URL.
Test-mode checkout uses test payment methods and does not settle live money.
For recurring payments, use the [subscription enrollment example](overview.md#enrolling-a-subscriber)
with an existing recurring Stripe Price ID.

### Behavior and limits

`CompleteCheckout` observes the hosted flow or reports invalid state; it never
charges again. Immediate cancellation requests no proration or new final invoice
and decides neither refunds nor access. `NextBillingAt` stays absent: item period
boundaries can remain after cancellation without proving another collection.
Dispatched mutation errors can wrap `payment.ErrOutcomeUnknown`; preserve the
original key and [reconcile](overview.md#handling-uncertain-outcomes).

### Receiving Stripe notifications

Construct `stripewebhook` with the endpoint's signing secret, separately from
the outbound API key. With import `arkoren.dev/driver/stripe/stripewebhook`, bind
the event your application needs:

```go
source, err := stripewebhook.New(signingSecret)
if err != nil {
    return err
}
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[stripewebhook.Envelope]) {
    receiver.Handle(stripewebhook.CheckoutSessionCompleted, accept)
})
if err != nil {
    return err
}
r.Post("/webhooks/stripe", handler)
```

Verification is network-free, using original bytes and timestamped signatures.
Select endpoint version `stripe.APIVersion` from `github.com/stripe/stripe-go/v87`;
bound snapshot mismatches cause retryable decode failure, not invalid authentication.
Thin-event fetching is unsupported.

`stripewebhook.New` requires a `whsec_` prefix, a nonempty suffix, and no whitespace
or control characters before running configuration callbacks. Invalid secrets skip
callbacks; valid secrets retain once-only callback and panic behavior. Tolerance is
validated afterwards.

For a custom envelope decoder, replace the removed `Envelope.Created` field with
`Envelope.CreatedAt`, for example `envelope.CreatedAt`. It remains an `int64` count
of Unix seconds, not a `time.Time` or local receipt time; zero means absent.
Stripe's JSON member is still `created`. Bindings using the shipped events still
receive `payment.Observation`; positive timestamps populate its `OccurredAt` field.

Accept decoded events durably before acknowledging. Checkout/subscription
lifecycle is not collection evidence; `invoice.paid` includes zero or externally
satisfied invoices. Correlate observed invoice references yourself; missing
relationships are not fetched or guessed. Refunds can remain pending; events
can repeat or arrive out of order.

The signature age defaults to five minutes; `stripewebhook.Builder.Tolerance`
changes it. Freshness is not replay prevention. Supported definitions include
Checkout completion, PaymentIntent success/failure, refund updates, subscription
creation/update/deletion, and invoice paid/payment-failed/invoice-payment-paid.

### Preparing native mobile payments

Use the same facade for an authorized native attempt. With server-derived
`amount`, persisted `key`, and caller context `ctx`, prepare a guest card intent:

```go
prepared, err := payments.PreparePayment(ctx, payment.NewPreparePaymentRequest(amount, key))
```

Persist `prepared.PaymentID`; deliver its sensitive `stripe.payment_intent`
authorization only to the authenticated owner. The driver creates an unconfirmed
card-only intent with automatic confirmation/capture settings, not a Customer,
CustomerSession, ephemeral key, or saved card. Empty, web, iOS, and Android channels
use the same profile. Nonempty ReturnURL/DeviceReference is unsupported; configure return
routing in the client, not the backend request. Follow the
[shared mobile backend](mobile-payments.md) for authorization, protected JSON,
verification, telemetry, and recovery. Client completion is not fulfillment
evidence. Native UI integration remains application-owned; Stripe's
[official mobile reference](https://docs.stripe.com/payments/accept-a-payment?payment-ui=mobile&platform=android)
is optional background, not part of the portable backend contract.

### API references

See the [Stripe payment API](https://pkg.go.dev/arkoren.dev/driver/stripe/stripepayment) and
[Stripe webhook API](https://pkg.go.dev/arkoren.dev/driver/stripe/stripewebhook).

## PayPal

For PayPal wallet checkout, create a developer application and start with sandbox
merchant credentials. Recurring enrollment also needs an existing account Plan.

### Installation

From your application's module directory, install the module:

```sh
go get arkoren.dev/driver/paypal
```

### Connecting

With client credentials from your application configuration, construct the
sandbox-default driver:

```go
import "arkoren.dev/driver/paypal/paypalpayment"

driver, err := paypalpayment.New(clientID, clientSecret)
payments := payment.New(driver)
```

### Configuration

Sandbox is the default. Only after verifying your application with sandbox buyer
approval, switch to production credentials and enable live operation. With imports
`net/http` and `time`, configure the environment and request timeout explicitly:

```go
driver, err := paypalpayment.New(clientID, clientSecret, func(merchant *paypalpayment.Builder) {
    merchant.Live(true)
    merchant.HTTPClient(&http.Client{Timeout: 10 * time.Second})
})
```

This can move real money; credentials and endpoints must belong to the same
environment. `merchant.HTTPClient(client)` shares its transport but copies client
settings, refuses redirects, and replaces nonpositive timeouts with thirty
seconds. A positive timeout is preserved. The driver never closes that transport.
The driver uses fixed trusted API origins and a private token cache;
it starts no refresh worker and does not replay failed mutations automatically.
No lifecycle hooks are required. `driver.HTTPClient()` exposes the actual client,
not an SDK; treat it as read-only during use. Custom transports must not replay
mutations.

### Creating and completing an Order

Follow the [hosted checkout example](overview.md#starting-a-payment), save its
key/ID, and redirect to its approval URL. Authorize the returning caller before
[explicit completion](overview.md#completing-and-observing-checkout); approval is
not collection. For [subscription enrollment](overview.md#enrolling-a-subscriber),
use your PayPal Plan ID rather than the sample Stripe Price ID.

### Preparing a native PayPal Order

Use the [shared preparation handler](mobile-payments.md#preparing-and-recording-the-handoff)
with the same `payments` facade. Persist `CheckoutID` and deliver its sensitive
`paypal.order` action only to the authenticated owner; the native Order ID is not
a financial PaymentID. No backend return URLs or approval link are required.
Empty, iOS, and Android channels work; web/nonempty ReturnURL is unsupported.
Optional DeviceReference forwards at most 255 visible ASCII bytes of risk
correlation as `PayPal-Client-Metadata-Id` on creation only, never authorization.

The [shared backend](mobile-payments.md) owns authorization, protected JSON,
approval checks, explicit completion, verification, telemetry, and recovery.
An SDK callback is not payment evidence. Native UI integration remains
application-owned; PayPal's [official mobile reference](https://developer.paypal.com/sdk/android/add-payment-methods/paypal-checkout.md)
is optional background, not a portable backend requirement.

### Behavior and limits

An approved Order can expire without capture if you omit `CompleteCheckout`.
Coordinate one stable completion key across the return route, optional approval
notification, and reconciliation; the driver installs none of those paths.

Preserve private resource-family prefixes in returned handles. Subscription-sale
observations are not Payments v2 captures. This driver deliberately returns
`ErrUnsupported` for sale lookup/refund rather than guessing compatibility with
deprecated APIs. One-time captures support payment lookup and explicit refunds.

PayPal accepts a subset of core currencies. HUF requests must represent whole
forints in canonical two-decimal minor units; fractional forints and unsupported
currencies fail before authentication or payment I/O. See the
[PayPal payment API reference](https://pkg.go.dev/arkoren.dev/driver/paypal/paypalpayment)
for the driver's public surface.

Subscriptions reference existing Plans and activate through buyer approval.
Immediate cancellation supports provider-eligible states; it does not implement
period-end scheduling, plan changes, or proration. Active status and next billing
time are not proof of the latest payment.

Dispatched mutation failures can leave the outcome unknown. Preserve the same
operation key and reconcile instead of starting another charge; see
[uncertain outcomes](overview.md#handling-uncertain-outcomes).

A successful GET reply with malformed JSON returns an error wrapping
`payment.ErrUnavailable`, without `ErrOutcomeUnknown`. Valid JSON that fails
semantic snapshot checks retains its specific validation or capability error;
for example, an unsupported resource shape is not silently reclassified as an
availability failure. Provider HTTP failures retain their status-based categories.

Malformed OAuth JSON or an unusable token response also wraps `ErrUnavailable`,
before the business request is dispatched. That failure does not itself mark the
current mutation outcome unknown, but it does not settle any earlier uncertain
attempt with the same operation key. Successful mutation decoding failures remain
outcome-unknown. `paypalpayment.Error` exposes safe status and machine-token fields;
unwrapped transport or decoding causes still require application redaction.

### Receiving PayPal notifications

Construct `paypalwebhook` with matching environment credentials and the
configured webhook ID. Sandbox is the default; select production separately
with `Builder.Live(true)` only when the webhook configuration and credentials
belong to that environment. With import
`arkoren.dev/driver/paypal/paypalwebhook`, bind order approval if you want a
notification-driven merchant completion path:

```go
source, err := paypalwebhook.New(clientID, clientSecret, webhookID)
if err != nil {
    return err
}
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[paypalwebhook.Envelope]) {
    receiver.Handle(paypalwebhook.CheckoutOrderApproved, accept)
})
if err != nil {
    return err
}
r.Post("/webhooks/paypal", handler)
```

The source remotely verifies original bytes at its trusted PayPal origin with
the request context, never arbitrary certificate URLs or enrichment. Outages are
retryable, not bad signatures. Authentication precedes duplicate suppression,
so repeats can cause verification requests. Delivery identity is the event ID,
not the transmission attempt ID.

The decoder never captures an approved Order; your callback/worker must call
`CompleteCheckout` with the shared completion key. Sale observations do not
enable outbound sale lookup/refund, and missing relationships are not guessed.
Events can repeat or arrive out of order; refund updates can remain pending.

Other definitions cover capture outcomes, sale collection/refund, refund progress,
and subscription lifecycle. Bind only needed events. Verify actual sandbox
deliveries; mock events do not prove signature verification.

### API references

See the [PayPal payment API](https://pkg.go.dev/arkoren.dev/driver/paypal/paypalpayment) and
[PayPal webhook API](https://pkg.go.dev/arkoren.dev/driver/paypal/paypalwebhook).

## Mollie

Use Mollie for hosted one-off card payments, not subscriptions or native SDK
initialization. Create an account and website profile, enable `creditcard`, and
obtain its test API key. Other methods, customer/mandate setup, recurring APIs,
and manual capture are not exposed.

### Installation

From your application's module directory, install the module:

```sh
go get arkoren.dev/driver/mollie
```

### Connecting

With a profile API key named `apiKey` loaded from server configuration, construct
the driver and portable client:

```go
import "arkoren.dev/driver/mollie/molliepayment"

driver, err := molliepayment.New(apiKey)
payments := payment.New(driver)
```

Construction validates a `test_`/`live_` profile key with alphanumeric suffix
without I/O. Its prefix selects mode at the fixed origin; OAuth/organization
credentials are unsupported. Live keys can move money.

### Configuration

Callbacks default to disabled. For status hints, supply a reachable absolute
HTTPS URL without credentials/fragments. With imports `net/http` and `time`:

```go
driver, err := molliepayment.New(apiKey, func(merchant *molliepayment.Builder) {
    merchant.WebhookURL("https://shop.example/payments/mollie")
    merchant.HTTPClient(&http.Client{Timeout: 10 * time.Second})
})
```

Omitted/nonpositive timeouts select 30 seconds. Construction copies settings,
refuses redirects, and borrows transport/jar without closing them. No lifecycle
hooks are needed. `HTTPClient()` exposes the actual client, not an SDK; do not
mutate it during use or weaken redirects. Custom transports must not replay or
log credentials. WebhookURL installs no receiver/authentication.

`fmt` formatting and `slog` logging of `Driver` or `*Driver` produce
`Driver{REDACTED}`, hiding the owned API key and callback destination. This does
not sanitize a borrowed transport or jar, objects returned by `HTTPClient()`,
or explicitly unwrapped error causes. Their ownership and logging policies remain
yours.

### Creating and observing hosted checkout

Use the [ordinary checkout example](overview.md#starting-a-payment) with an
accepted currency/amount. Save IDs against the authorized account before
redirecting to the sensitive ApprovalURL. The driver fixes `method: creditcard`,
`sequenceType: oneoff`, and `captureMode: automatic`, never a broader method set.
Empty description becomes `Payment`; Reference becomes metadata, not idempotency.

On return or a status hint, read the saved ID with `GetCheckout` or `GetPayment`.
Both identify the same `tr_` resource using distinct Go ID types.
`NeedsCompletion` is false; `CompleteCheckout` returns `payment.ErrUnsupported`.
Only `paid` maps to succeeded; `authorized` and `pending` are not collection.

### Refunding and reading refund progress

Authorize the saved order and use the [ordinary refund request](overview.md#refunding-a-payment)
with a positive amount in its original currency. Mollie enforces currency and
remaining balance, without a hidden preflight or post-mutation hydration.

Preserve all of `refund.ID`: its opaque compound handle includes the parent
payment required for lookup, not only Mollie's `re_` token. With `refund` from
the checked refund call, observe progress:

```go
refund, err = payments.GetRefund(ctx, refund.ID)
```

Queued/pending/processing refunds remain pending, not returned funds or an
unknown mutation outcome. Only `refunded` succeeds; unknown native states stay unknown.

### Behavior and limits

Subscription creation, lookup, and cancellation return `payment.ErrUnsupported`
after portable validation without HTTP. Mollie's recurring product is not a
shipped Arkoren subscription capability.

`PreparePayment` also validates, then returns `ErrUnsupported` without HTTP.
This does not disable the supported hosted one-off card flow; Mollie Components
and native wallet SDK initialization are not shipped preparation profiles.

Mollie's [idempotency guarantee](https://docs.mollie.com/reference/api-idempotency)
caches POST responses for **one hour** per exact credential. Persist request,
key, and credential identity before dispatch. Rotating credentials, even within
one account, loses replay scope; after an hour, key reuse creates a new request.
This is not permanent exactly-once processing, especially for partial refunds.

One attempt is submitted without automatic retries. Transport loss, conflicts,
server failures, redirects, or an unusable mutation success can wrap `ErrOutcomeUnknown`;
follow the [recovery limits](overview.md#handling-uncertain-outcomes). Errors omit
provider bodies; unwrapped causes still need application redaction.

Malformed JSON or an unusable snapshot from a successful GET wraps
`payment.ErrUnavailable`, not `ErrOutcomeUnknown`. This includes missing required
amounts and mismatched payment/refund identities. Transport and non-2xx failures
retain their own categories; successful mutation decoding failures remain
outcome-unknown rather than being treated as failed reads.

When `errors.As` finds a `*molliepayment.Error`, its `Status` is a retained non-2xx
status, or zero when none was retained. Zero can follow a successful HTTP response
whose body could not be read or decoded; it does not prove that no reply arrived.
Do not use it to clear an earlier uncertain attempt or authorize another charge.

### Receiving Mollie status hints

Mollie's [classic callback](https://docs.mollie.com/docs/webhooks) sends an
unsigned form POST (`id=tr_example`) to WebhookURL. There is no shipped
`molliewebhook` or authenticated Source. Anyone can forge this hint: rate-limit
the route, bound its body, accept exactly one ID, and check its persisted
merchant/environment association before an authenticated provider read.

Mount a plain application handler, not `webhook.Handler`. With `net/http`
imported, `mux *http.ServeMux`, and an application-owned `handleMollie` that
implements those checks, register its route:

```go
mux.HandleFunc("POST /payments/mollie", handleMollie)
```

The [callback wiring example](mollie-callbacks.md#reading-and-reconciling-the-hint)
shows parsing, ownership checks, authoritative reads, and durable reconciliation.
Require succeeded status, expected amount/currency, and order association before
fulfillment. Return 200 after durable handling; acknowledge unknown IDs without
disclosing order data. Failures allow retries; duplicate hints must not duplicate
fulfillment. Select merchant credentials from persisted mappings, never input.

Refund callbacks carry the payment ID, not the compound refund handle. Retrieve
the refund IDs you already persisted with `GetRefund` to reconcile returned
funds. Next-gen Mollie webhooks and native wallet SDK initialization are outside
this driver's scope.

### API references

See the [Mollie payment API](https://pkg.go.dev/arkoren.dev/driver/mollie/molliepayment) for
configuration, opaque handles, and error behavior.
