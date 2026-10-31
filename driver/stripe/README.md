# Stripe

`stripepayment` implements all ten `payment.Driver` operations with hosted
Checkout, PaymentIntents, refunds, and existing recurring Prices.
`stripewebhook` is an independent snapshot-event adapter: it needs an endpoint
signing secret, not a payment driver or API key.

## Create a checkout

From your application's module directory, install the provider:

```sh
go get arkoren.dev/driver/stripe
```

Use a test-mode API key first. With `apiKey` loaded from configuration and `ctx`
supplied by your request handler, create a checkout. Derive its amount/reference
from an authorized server-side order, persist its operation key before dispatch,
and preserve the original key on uncertainty rather than starting another charge:

```go
import (
    "arkoren.dev/core/service/payment"
    "arkoren.dev/driver/stripe/stripepayment"
)

backend, err := stripepayment.New(apiKey)
client := payment.New(backend)
amount, err := payment.NewMoney("USD", 2500)
checkout, err := client.CreateCheckout(ctx, payment.NewCheckoutRequest(
    amount, "https://shop.example/paid", "order-123",
    func(details *payment.CheckoutRequestBuilder) {
        details.Reference("order-123")
    }))
_ = checkout.ApprovalURL // Customer redirect; do not log this sensitive URL.
```

Construction is network-free. No worker or lifecycle methods are required.
Checkout uses one inline price, quantity one, and lowercase Stripe currency
codes. An empty description displays **Payment**. Subscription checkout instead
takes an existing `price_` ID, quantity one; your application owns its catalog.
References become session metadata/client references and payment or subscription
metadata as applicable.

IDs are returned unchanged: `cs_` for checkout, `pi_` for payments/refunds'
original payments, `re_` for refunds, and `sub_` for subscriptions. Do not pass
checkout or charge IDs to `GetPayment` or `RefundPayment`.

`GetCheckout` only reads. `CompleteCheckout` also only reads: it returns
`ErrInvalidState` until Stripe reports complete, and never charges again.
Completion and payment collection remain independent. A proven zero-total trial
flow reports `StatusNotRequired` even when Stripe calls the session `paid`;
an absent total is not assumed to be zero. Absent relationships stay absent.

Refunds require a positive explicit amount. Because Stripe's refund endpoint
has no currency parameter, the driver first reads the PaymentIntent and refuses
a currency mismatch before submitting a refund. It never reads after a successful
mutation. Cancellation likewise reads first, succeeds if already canceled, and
otherwise cancels immediately with `invoice_now=false` and `prorate=false`.
It does not refund or schedule end-of-period cancellation.

## Prepare a native payment

For guest card-backed SDK payment UI, call the same facade's `PreparePayment`
with an authorized server price and a separately persisted operation key:

```go
prepared, err := client.PreparePayment(ctx, payment.NewPreparePaymentRequest(amount, "order-123-native",
    func(details *payment.PreparePaymentRequestBuilder) {
        details.Reference("order-123")
        details.Channel(payment.ClientChannelIOS)
    }))
```

Save `prepared.PaymentID` against the authorized order. Its required
`stripe.payment_intent` authorization action contains the client secret; deliver
it only to that order's authenticated owner over HTTPS with `no-store` and body
logging disabled. JSON intentionally exposes the token; fmt/slog redact the
whole record, not a token you extract and log separately.

Preparation creates an unconfirmed, card-only PaymentIntent with automatic
confirmation/capture settings, not a Customer, saved card, checkout, or session.
Empty, web, iOS, and Android channels share this profile. Nonempty ReturnURL or
DeviceReference returns `ErrUnsupported` before dispatch. Configure native return
routing, publishable key, and wallet eligibility in the SDK. The common facade
supplies `payment prepare_payment` telemetry without recording handoff data.
After SDK confirmation, retrieve the saved PaymentID and require authoritative
success, exact amount/currency, and order/account/environment association before
idempotent fulfillment. See [Mobile Payments](../../cmd/arkoren/skills/arkoren/references/guides/payments/mobile-payments.md)
for native setup and the separate PayPal approval/capture path.

## Request policies and failures

`New(apiKey, ...BuilderFunc)` supports `Builder.HTTPClient(*http.Client)`. It
copies the HTTP client, keeps a supplied positive timeout, and otherwise uses
30 seconds. Negative timeouts are invalid. SDK retries, telemetry, and logging
are disabled. The copied client refuses all HTTP redirects, overriding its
`CheckRedirect` policy, to prevent forwarding credentials to another origin.
The original client is unchanged. Use a transport that does not replay requests.

`New` validates the API key before invoking configuration callbacks. An invalid
key skips all callbacks, including nil entries. With a valid key, callbacks run
once, synchronously in order; nil entries and application panics propagate.
HTTP settings are validated after the callbacks finish.

`NewFrom(*stripe.Client)` and `Client()` borrow/expose the same native client.
`NewFrom` preserves caller redirects, retries, timeouts, logging, and telemetry;
it does not give `New`'s one-attempt guarantee. The caller owns native and HTTP resources.
Do not mutate shared clients during concurrent use.

Errors expose safe contract text. Use `errors.Is` for payment sentinels and
`errors.As` for the retained `*stripe.Error`; native diagnostic fields may contain
sensitive data. A mutation's transport error, server error, or malformed success
returns `ErrOutcomeUnknown`. Cancellation after dispatch also retains the context
cause. Reconcile unknown outcomes rather than automatically replaying a mutation.
Idempotency keys are required where core requests specify them; Stripe retention
and account scope apply, not exactly-once execution.

Amounts use core's audited currency set without rounding. Stripe's ISK and UGX
[special representations](https://docs.stripe.com/currencies#special-cases) are
not supported by core and are not silently converted. Account, country, payment
method, and amount limits remain Stripe's responsibility. Multi-item subscription
snapshots do not pretend to have one plan. `NextBillingAt` is always absent:
Stripe item period boundaries do not establish the next collection time, even
for an active subscription.

## Receive optional events

Configure a public HTTPS URL and selected event types in Stripe's dashboard.
Provide that endpoint's separate `whsec_` signing secret and bind events with
`webhook.Handler`. In the following fragment, `accept` has type
`webhook.AcceptFunc[payment.Observation]`; the application owns its durable effects.

```go
import (
    "arkoren.dev/core/http/webhook"
    "arkoren.dev/driver/stripe/stripewebhook"
)

source, err := stripewebhook.New(signingSecret)
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[stripewebhook.Envelope]) {
    receiver.Handle(stripewebhook.PaymentIntentSucceeded, accept)
})
_ = handler // Mount explicitly in the application's router.
```

`Builder.Tolerance(time.Duration)` changes the default 300-second signature age
window; zero restores the default and negative values are invalid. Signatures
are checked before parsing. Invalid/stale signatures wrap `ErrInvalidSignature`
and `webhook.ErrRejected`, retaining the SDK's constant signature-error cause
without exposing signature or secret values. Authenticated malformed envelopes
or bound events with an incompatible schema/API version answer 503, not signature rejection.
Authenticated unbound events are acknowledged with 200 by core webhook.

`stripewebhook.New` validates the signing secret before invoking its callbacks;
invalid secrets skip every callback. With a valid secret, callbacks run once,
synchronously in order, and nil entries or application panics propagate.
Tolerance is validated afterwards. `Envelope.CreatedAt` contains the native
event time in Unix seconds, with zero for absence. It replaces the former Go
field `Created`; Stripe's JSON member remains `created` without a wire migration.

Bound events require `stripe.APIVersion` from the pinned SDK, use small json/v2
snapshot decoders, and never fetch resources. Thin events are unsupported.
Delivery owns the native event ID/type; observations contain only resource facts.
Use `Event.Map` for an application-owned DTO. Configure routes, bindings,
deduplication, body limits, queues, and handlers explicitly in the application.

Available definitions and native keys:

| Definition | Native key |
| --- | --- |
| `CheckoutSessionCompleted` | `checkout.session.completed` |
| `PaymentIntentSucceeded` | `payment_intent.succeeded` |
| `PaymentIntentFailed` | `payment_intent.payment_failed` |
| `RefundUpdated` | `refund.updated` |
| `SubscriptionCreated` | `customer.subscription.created` |
| `SubscriptionUpdated` | `customer.subscription.updated` |
| `SubscriptionDeleted` | `customer.subscription.deleted` |
| `InvoicePaid` | `invoice.paid` |
| `InvoicePaymentFailed` | `invoice.payment_failed` |
| `InvoicePaymentPaid` | `invoice_payment.paid` |

`invoice.paid` can describe a zero or externally settled invoice; it never maps
to `PaymentSucceeded`. Invoice observations retain the invoice ID and parent
subscription details when supplied. `invoice_payment.paid` keeps explicit invoice
and PaymentIntent links only; charge-only settlements do not invent `pi_` IDs.
Subscription activity does not prove invoice payment. Events may be duplicated
or out of order; the application owns fulfillment and reconciliation policy.

## Testing your integration

Use [memory simulation or scripted replies](../../cmd/arkoren/skills/arkoren/references/guides/payments/testing.md)
for application tests without financial effects. Verify hosted buyer approval and
signed webhook delivery separately with a dedicated Stripe test account; local
fixtures do not establish live account acceptance or settlement. Select the
endpoint version matching `stripe.APIVersion` from `github.com/stripe/stripe-go/v87`
for bound snapshots. There is no bundled Stripe emulator.

See [Stripe setup and tasks](../../cmd/arkoren/skills/arkoren/references/guides/payments/drivers.md#stripe),
the [payment API](https://pkg.go.dev/arkoren.dev/driver/stripe/stripepayment),
and the [webhook API](https://pkg.go.dev/arkoren.dev/driver/stripe/stripewebhook).
