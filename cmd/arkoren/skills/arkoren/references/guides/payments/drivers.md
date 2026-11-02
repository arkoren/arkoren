---
title: Payment Drivers
nav: Drivers
description: Choose and configure a payment backend, with optional provider notifications and local call monitoring.
order: 20
summary:
  - Compare Memory, Stripe, PayPal, and Mollie against the shipped payment capabilities.
  - Configure credentials, HTTP ownership, client handoffs, and provider-specific limits.
  - Add optional authenticated notifications, unsigned Mollie hints, or local call observations.
---

# Payment Drivers

Choose Memory for local simulation, Stripe for hosted or native card payments,
PayPal for wallet Orders, or Mollie for hosted one-off card payments. Stripe and
PayPal also enroll buyers in existing recurring plans. Changing drivers does not
migrate resources, credentials, or customer agreements.

Install core from your application's module directory:

```sh
go get arkoren.dev/core
```

The fragments use `arkoren.dev/core/service/payment`. Load credentials from
server configuration, check construction errors, and begin with test/sandbox
credentials. None of these drivers needs lifecycle hooks, a worker, or a webhook
to make outbound calls. Construction performs no network I/O.

## Compatibility Matrix

This compares Arkoren's shipped operations and companions, not each vendor's
full product. Memory supplies local state, not a real buyer page or collection.

| Driver | Hosted buyer approval | Merchant completion | Payment lookup | Refund creation and lookup | Subscription enrollment | Immediate cancellation | Client preparation | Authenticated notifications | Facade call observations |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No — Non-routable handles. | Yes — Simulated. | Yes — Simulated. | Yes — Simulated. | Partial — No renewals. | Yes — Simulated. | Partial — Pending payment and inert token. | No | Yes — Opt-in, in-process only. |
| [Stripe](#stripe) | Yes | No — Observes hosted completion. | Yes | Yes | Partial — Existing recurring Price, quantity one. | Yes | Partial — Guest card intent; no return URL or device reference. | Yes — Separate receiver. | Yes — Opt-in, in-process only. |
| [PayPal](#paypal) | Yes | Yes — Order capture. | Partial — Captures only. | Partial — Captures only. | Partial — Existing Plan. | Partial — Active/suspended; canceled succeeds. | Partial — Native Order; creation-only risk data; no web/backend return URL. | Yes — Separate receiver. | Yes — Opt-in, in-process only. |
| [Mollie](#mollie) | Partial — One-off creditcard, automatic capture only. | No — ErrUnsupported. | Yes | Yes — Compound refund handle required. | No | No | No | No — Unsigned ID hints only. | Yes — Opt-in, in-process only. |

Approval is not collection, refund acceptance is not returned funds, and an
active subscription is not paid access. Cancellation is immediate, without
proration or refunds. Retain durable operation keys and respect
[provider-specific recovery limits](recovery.md) before replaying any mutation.

## Memory

Use the bundled simulator when no external financial effect should occur. Each
instance has independent state that disappears on exit; it needs no credentials
or separate module.

```go
import (
    "arkoren.dev/core/service/payment"
    "arkoren.dev/core/service/payment/memorypayment"
)

simulator := memorypayment.New()
payments := payment.New(simulator)
```

There are no optional settings or native client. Use the
[local payment journey](testing.md#simulating-payments) to supply approval and
settlement explicitly. Completion creates a pending payment; advancing time
does not settle it or collect a renewal.

Preparation creates a pending payment and inert `memory.authorization` token,
not a checkout or SDK credential. `ResolvePayment` supplies its result.
`ResolveSubscription` separately supplies subscription lifecycle, not invoice
payment. For ambiguous replies rather than simulated state, use the
[scripted fake](testing.md#scripting-outcomes).

See the [memorypayment API](https://pkg.go.dev/arkoren.dev/core/service/payment/memorypayment).

## Stripe

Choose Stripe for hosted Checkout or guest native card intents. Obtain a Stripe
test-mode API key; recurring enrollment additionally needs an existing recurring
Price. One-time checkout uses an inline amount rather than a precreated Price.

```sh
go get arkoren.dev/driver/stripe
```

### Connecting Stripe

With `stripeKey` loaded from server configuration, create the backend and facade:

```go
import "arkoren.dev/driver/stripe/stripepayment"

backend, err := stripepayment.New(stripeKey)
payments := payment.New(backend)
```

The key must be nonempty without whitespace or controls. Invalid keys return
`payment.ErrInvalidRequest` before callbacks run. Valid-key callbacks run once
in order; nil callbacks and callback panics propagate. HTTP settings are checked
afterwards.

To use a shorter timeout, construct the backend with `net/http` and `time`:

```go
backend, err := stripepayment.New(stripeKey, func(connection *stripepayment.Builder) {
    connection.HTTPClient(&http.Client{Timeout: 8 * time.Second})
})
```

The default/zero timeout is 30 seconds; a negative timeout fails construction.
`New` copies HTTP settings, refuses redirects, and disables SDK retries and
standard HTTP mutation replays, logging, and telemetry. Borrowed transports stay
caller-owned and must not independently replay mutations.

`NewFrom` instead preserves the supplied SDK client's retry, redirect, logging,
and timeout policies. `Client()` exposes the retained native client; direct
native operations and their safety remain yours. The driver closes neither
client nor transport. Do not mutate shared clients during requests.

### Payment Flows

Follow [Hosted Checkout](hosted-checkout.md) for a redirect or
[Recurring Subscriptions](subscriptions.md) with a recurring Price at quantity
one. Stripe's hosted UI collects payment; `CompleteCheckout` only observes
completion and never charges again. Checkout Session, PaymentIntent, Refund,
and Subscription IDs retain their distinct native families.

For [Mobile Payments](mobile-payments.md), `PreparePayment` creates an unconfirmed,
card-only PaymentIntent with automatic confirmation/capture settings. The
sensitive `stripe.payment_intent` authorization goes only to the authenticated
owner. Empty, web, iOS, and Android channels use the same guest profile. ReturnURL
and DeviceReference must be empty; configure client return routing in the SDK.
No Customer, CustomerSession, ephemeral key, or saved-card setup is supplied.

Refunds perform a currency-checking PaymentIntent preflight, then submit the
explicit refund without post-success hydration. Immediate subscription
cancellation requests neither proration nor a new final invoice. `NextBillingAt`
stays absent: item period boundaries do not establish another collection.
Dispatched ambiguous mutations can wrap `ErrOutcomeUnknown`; preserve the
original request/key even if a later call fails differently.

### Stripe Notifications

Notifications are optional and use `stripewebhook`, installed by the same vendor
module, independently of the payment backend. Configure a public HTTPS endpoint
in Stripe with snapshot event version `stripe.APIVersion` from
`github.com/stripe/stripe-go/v87`. Its endpoint signing secret is not the API key.

The fragments assume `r *router.Router` and an application-owned
`accept webhook.AcceptFunc[payment.Observation]` that durably accepts observations
and returns its error. Import `arkoren.dev/core/http/webhook` and construct the
source first:

```go
import "arkoren.dev/driver/stripe/stripewebhook"

source, err := stripewebhook.New(endpointSecret)
if err != nil {
    return err
}
```

Bind only needed events and mount the checked handler:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[stripewebhook.Envelope]) {
    receiver.Handle(stripewebhook.PaymentIntentSucceeded, accept)
})
if err != nil {
    return err
}
r.Post("/payments/stripe/events", handler)
```

Verification uses original bytes and timestamped signatures without network I/O.
The secret needs `whsec_`, a nonempty suffix, and no whitespace/controls before
callbacks run. Signature tolerance defaults to five minutes; change it with
`stripewebhook.Builder.Tolerance`. Zero restores the default; negative is invalid.
Freshness is not durable replay prevention.

Bound snapshot version/schema mismatches fail decoding retryably; they are not
authentication failures. Thin events and fetching missing relationships are not
supported. `Envelope.CreatedAt` is native Unix seconds (zero means absent), not
`time.Time`; JSON still uses `created`. Positive values populate portable
`Observation.OccurredAt`.

Definitions also cover Checkout completion, intent failure, refund updates,
subscription creation/update/deletion, and invoice paid/payment-failed/
invoice-payment-paid. Checkout/subscription lifecycle is not collection evidence;
`invoice.paid` can describe zero or externally satisfied invoices. Correlate
invoice references without guessing missing relationships. Events can repeat or
arrive out of order, and refunds can remain pending.

Apply the [receiver acceptance rules](#notification-acceptance) below. See the
[Stripe payment API](https://pkg.go.dev/arkoren.dev/driver/stripe/stripepayment)
and [Stripe webhook API](https://pkg.go.dev/arkoren.dev/driver/stripe/stripewebhook).

## PayPal

Choose PayPal for hosted or native wallet Orders. Create a developer application
and use sandbox merchant credentials. Subscription enrollment additionally needs
an existing account Plan; a Stripe Price is not interchangeable.

```sh
go get arkoren.dev/driver/paypal
```

### Connecting PayPal

Load the sandbox application's credentials from server configuration:

```go
import "arkoren.dev/driver/paypal/paypalpayment"

backend, err := paypalpayment.New(merchantID, merchantSecret)
payments := payment.New(backend)
```

Callbacks run once in order before nonblank credential validation; nil callbacks
panic. Sandbox is the default. To bound requests, use `net/http` and `time`:

```go
backend, err := paypalpayment.New(merchantID, merchantSecret, func(merchant *paypalpayment.Builder) {
    merchant.HTTPClient(&http.Client{Timeout: 8 * time.Second})
})
```

HTTP settings are copied, redirects refused, and a nonpositive timeout becomes
30 seconds. Transports/jars remain borrowed and are never closed. Fixed trusted
API origins and a private token cache require no refresh worker. Requests,
including failed mutations, are not automatically replayed; custom transports
must preserve that boundary. `HTTPClient()` exposes the actual client, not an
SDK; treat it as read-only during use.

Only after sandbox verification, use production credentials and
`merchant.Live(true)` in construction. That setting selects a live origin and
can move real money; merchant credentials, resources, and environment must match.

### Payment Flows

Use [Hosted Checkout](hosted-checkout.md) and explicitly complete an approved
Order with a separately persisted completion key. An approved Order can expire
without capture; neither a notification decoder nor a return link captures it.
Coordinate the same completion operation across authorized POSTs and workers.

For [Mobile Payments](mobile-payments.md), preparation returns a native
`paypal.order` action and a `CheckoutID`, not a financial PaymentID. Empty, iOS,
and Android channels work; web and nonempty ReturnURL are unsupported. No backend
approval link is required. Optional DeviceReference carries at most 255 visible
ASCII bytes as `PayPal-Client-Metadata-Id` on creation only. It is risk correlation,
never authorization. Client SDK approval must still precede server completion.

Preserve returned resource-family prefixes. Payment reads/refunds support
one-time Payments v2 captures, not subscription sales. Sale observations do not
enable deprecated sale lookup/refund; those operations return `ErrUnsupported`.
PayPal supports a subset of core currencies. HUF amounts must represent whole
forints using canonical two-decimal minor units; unsupported currencies and
fractional forints fail before authentication/payment I/O.

[Subscriptions](subscriptions.md) activate through buyer approval. Cancellation
supports active/suspended subscriptions; already-canceled succeeds without a
mutation. There is no period-end scheduling, plan change, or proration.
Provider-supplied `NextBillingAt` and active status do not establish payment.

Malformed successful GET JSON wraps `ErrUnavailable` without outcome uncertainty;
semantic snapshot failures retain their specific validation/capability errors.
Unusable OAuth responses also wrap `ErrUnavailable` before business dispatch.
Neither resolves an earlier uncertain mutation. Successful mutation decoding
failures remain outcome-unknown. `paypalpayment.Error` exposes safe status and
machine-token fields, but unwrapped causes still require redaction.

### PayPal Notifications

Use the separate `paypalwebhook` package from the same module. Configure a public
HTTPS webhook endpoint and selected event types in the matching sandbox account.
The webhook registration ID and merchant credentials must share its environment.
Use `r *router.Router`, import `arkoren.dev/core/http/webhook`, and supply an
application-owned `accept webhook.AcceptFunc[payment.Observation]` that durably
accepts observations and returns its error:

```go
import "arkoren.dev/driver/paypal/paypalwebhook"

source, err := paypalwebhook.New(merchantID, merchantSecret, registrationID)
if err != nil {
    return err
}
```

Bind capture completion when your application needs collection observations:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[paypalwebhook.Envelope]) {
    receiver.Handle(paypalwebhook.PaymentCaptureCompleted, accept)
})
if err != nil {
    return err
}
r.Post("/payments/paypal/events", handler)
```

Production is selected separately with `paypalwebhook.Builder.Live(true)` and
matching production registration/credentials. Its HTTPClient setting copies
client settings, refuses redirects, and defaults nonpositive timeouts to 30
seconds; its transport stays caller-owned.

The source verifies original bytes remotely at a fixed PayPal origin using the
request context. It never fetches arbitrary certificate URLs or enriches events.
Verifier outages are retryable, not invalid signatures. Authentication precedes
duplicate suppression, so repeated events can cause verification requests.
Delivery identity is the event ID, not a transmission attempt ID.

Other definitions cover Order approval, capture outcomes, sale collection/refund,
refund progress, and subscription lifecycle. Binding `CheckoutOrderApproved`
does not capture: your authorized worker calls `CompleteCheckout` with the saved
completion key. Missing relationships stay absent; events can repeat or arrive
out of order. Mock events do not prove signature verification; validate actual
sandbox deliveries separately.

Apply the [receiver acceptance rules](#notification-acceptance). See the
[PayPal payment API](https://pkg.go.dev/arkoren.dev/driver/paypal/paypalpayment)
and [PayPal webhook API](https://pkg.go.dev/arkoren.dev/driver/paypal/paypalwebhook).

## Mollie

Choose Mollie for hosted, one-off `creditcard` payments with automatic capture
and explicit refunds. Create a website profile, enable that method, and obtain
a test profile API key. Other methods, manual capture, customer/mandate setup,
recurring APIs, and native SDK preparation are not exposed.

```sh
go get arkoren.dev/driver/mollie
```

### Connecting Mollie

With the profile key loaded from server configuration:

```go
import "arkoren.dev/driver/mollie/molliepayment"

backend, err := molliepayment.New(profileKey)
payments := payment.New(backend)
```

Keys need a `test_` or `live_` prefix and alphanumeric suffix. Their prefix selects
mode at the fixed API origin; OAuth/organization credentials are unsupported.
A live key can move money. Callbacks run once in order, even for invalid keys;
nil callbacks panic.

Callbacks are disabled by default. To receive status hints, configure a reachable
absolute HTTPS URL without credentials/fragments, using `net/http` and `time`:

```go
backend, err := molliepayment.New(profileKey, func(merchant *molliepayment.Builder) {
    merchant.WebhookURL("https://shop.example/payments/mollie")
    merchant.HTTPClient(&http.Client{Timeout: 8 * time.Second})
})
```

This installs no receiver or authentication. Nonpositive/omitted timeouts become
30 seconds. HTTP settings are copied, redirects refused, and borrowed
transport/jar objects never closed. `HTTPClient()` exposes the actual client;
do not mutate it during use or weaken redirect policy. Custom transports must
neither replay mutations nor log credentials.

### Payment Flows

Use [Hosted Checkout](hosted-checkout.md). The driver fixes `method: creditcard`,
`sequenceType: oneoff`, and `captureMode: automatic`. An empty description becomes
`Payment`; Reference becomes correlation metadata, not idempotency.
Checkout/payment IDs refer to the same `tr_` resource using different Go ID types.
`NeedsCompletion` is false and explicit completion returns `ErrUnsupported`.
Only native `paid` maps to succeeded; `authorized` and `pending` are not collection.

Use [Payment Refunds](refunds.md) with the original currency. Mollie enforces
currency and remaining balance without hidden preflight or post-mutation reads.
Save the entire compound refund handle: it includes the parent payment needed
for lookup. Queued/pending/processing refunds remain pending; only `refunded`
succeeds, and unfamiliar native states stay unknown.

Subscriptions and preparation validate portable input, then return
`ErrUnsupported` without HTTP. This does not disable hosted one-off checkout.
Mollie Components, native wallets, and Mollie's recurring product are not shipped
capabilities of this driver.

> [!WARNING]
> Mollie caches POST responses for one hour per exact API credential. Save the
> request, key, and credential identity before dispatch. Credential rotation or
> replay after expiry can create another effect, even within the same account.
> See [Payment Recovery](recovery.md#replaying-requests) before replaying.

There is one mutation attempt without automatic retries. Transport loss,
conflicts, server failures, redirects, and unusable mutation successes can wrap
`ErrOutcomeUnknown`. Malformed/unusable successful GET snapshots instead wrap
`ErrUnavailable`, including missing required amounts or mismatched identities;
transport/non-2xx failures retain their categories. None clears earlier uncertainty.

`molliepayment.Error.Status` retains a non-2xx status or zero if none was retained.
Zero can follow a successful HTTP reply that could not be read/decoded; it does
not prove no reply arrived. Errors omit provider bodies. Driver `fmt`/`slog`
formatting yields `Driver{REDACTED}`, hiding owned credentials and callback URL,
but borrowed HTTP objects and explicitly unwrapped causes remain your redaction
responsibility.

### Mollie Callbacks

Classic callbacks are unsigned form POSTs with an `id` hint. Anyone can forge
them; there is no shipped `molliewebhook` or authenticated Source. Rate-limit
and bound the route, accept exactly one ID, check its persisted merchant/test-live
association, and read authoritative provider state with that merchant's client.

With `mux *http.ServeMux` and your application handler, register a plain route:

```go
mux.HandleFunc("POST /payments/mollie", handleMollie)
```

[Mollie Callbacks](mollie-callbacks.md) shows parsing, ownership, lookup, and durable
acceptance. Unknown IDs receive an acknowledgement without order disclosure.
Require succeeded state and matching order amount/currency before fulfillment;
duplicate hints must not duplicate effects. Refund callbacks carry the payment
ID, so reconcile the compound refund handles you already saved. Next-gen
webhooks are outside scope.

See the [Mollie payment API](https://pkg.go.dev/arkoren.dev/driver/mollie/molliepayment).

## Notification Acceptance

Stripe/PayPal receivers use the [HTTP webhook receiver](../webhooks/receiving-webhooks.md).
By default, it answers 200 after acceptance, including unbound authenticated
types; 503 for decode/accept errors; 400 for rejected authentication; and 413
for an oversized body. The receiver commits its failure response before returning
an error. Receipt 4xx errors match `webhook.ErrRejected`; 503 errors match
`webhook.ErrUnavailable`, retaining causes. An outer error mapper cannot rewrite
that response, and unwrapped causes require redaction.

Accept observations durably before acknowledging. A 200 does not establish
asynchronous completion or successful collection. `webhook.Delivery` carries
native event identity; `payment.Observation` carries facts/time. Use `Event.Map`
for application queue DTOs. Scope deduplication by provider, merchant,
environment, and consumer; cache deduplication replaces neither a durable
inbox/outbox nor operation keys.

## Local Monitoring

To observe facade calls locally, supply an in-process event bus at construction.
With `driver payment.Driver` and `bus *event.Bus` already configured:

```go
payments := payment.New(driver, func(client *payment.Builder) {
    client.Events(bus)
})
```

Register listeners on `payment.OperationCompleted` and `OperationFailed` using
the [event bus](../events/overview.md). Their `payment.OperationEvent` contains
only a fixed operation name and bounded `ErrorType`, not IDs, amounts, URLs,
keys, secrets, or arbitrary error text. Omitted/nil Events disables observations;
direct driver calls bypass them.

Each driver-returned call emits once: nil error completes, non-nil error fails.
Validation failures, missing drivers, and pre-canceled calls emit nothing.
`payment.ErrOutcomeUnknown` takes precedence over joined error classes, while
the returned error retains its causes. Listeners run synchronously after
telemetry with the operation context; their errors/panics cannot alter the API
result, but slow listeners delay return.

These best-effort observations include reads and replays. They prove neither
money movement nor durable reconciliation and must not trigger fulfillment.
Provider notifications and application-owned operation records remain separate.
For facade configuration, see the
[payment API](https://pkg.go.dev/arkoren.dev/core/service/payment).
