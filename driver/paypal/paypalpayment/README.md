# PayPal payments

Use `paypalpayment` for customer-approved one-time payments and existing-plan
subscriptions. It implements `payment.Driver` operations through
[Orders v2](https://developer.paypal.com/docs/api/orders/v2/),
[Payments v2](https://developer.paypal.com/docs/api/payments/v2/), and
[Billing Subscriptions v1](https://developer.paypal.com/docs/api/subscriptions/v1/).

```sh
go get arkoren.dev/driver/paypal
```

## Start in sandbox

Supply your PayPal application's credentials. Construction validates local
settings without contacting PayPal or starting a goroutine.

```go
import (
    "os"

    "arkoren.dev/core/service/payment"
    "arkoren.dev/driver/paypal/paypalpayment"
)

backend, err := paypalpayment.New(os.Getenv("PAYPAL_CLIENT_ID"), os.Getenv("PAYPAL_CLIENT_SECRET"))
client := payment.New(backend)
```

Use the returned client in your request handler. Creation opens approval; it
does not capture money. With your handler's `ctx`, create an order and send the
customer to the returned approval URL. Authorize the saved order and use its
server-side amount/reference, not buyer-supplied pricing. Persist the operation
key before dispatch; an uncertain result is not permission to create a fresh key:

```go
checkout, err := client.CreateCheckout(ctx, payment.NewCheckoutRequest(
    payment.Money{Amount: 1250, Currency: "USD"}, "https://shop.example/paid", "create-order-123",
    func(details *payment.CheckoutRequestBuilder) {
        details.Reference("order-123")
    }))
_ = checkout.ApprovalURL // Redirect the buyer; do not log this sensitive URL.
```

If `CancelURL` is blank, PayPal receives `ReturnURL` for both redirects. Preserve
`checkout.ID`; do not recover it from the approval URL. After the customer
approves, call `CompleteCheckout` with a separate caller-owned idempotency key.
It observes the order first and captures only an approved order. An already
completed order does not cause another capture. A completed order with a pending
capture remains `payment.StatusPending`, not succeeded.

For a native iOS or Android PayPal SDK flow, use `PreparePayment` instead.
It creates a plain one-off CAPTURE order and returns an action whose `Order()`
accessor exposes protocol `paypal.order` and the real native order ID. Save the
returned `CheckoutID` server-side; `PaymentID` remains empty until capture.
The SDK supplies return/cancel URLs, so a nonempty request `ReturnURL` returns
`ErrUnsupported`. The empty, iOS and Android channels use identical orders;
the web channel is unsupported. Optional `DeviceReference` accepts at most
255 visible ASCII bytes and is sent only in `PayPal-Client-Metadata-Id` on order
creation. Never log this risk data. See the official
[mobile checkout sequence](https://developer.paypal.com/sdk/android/add-payment-methods/paypal-checkout.md).
SDK success is not proof of payment: `CompleteCheckout` still reads actual
provider approval and performs a separate merchant capture. Preparation never
captures, fetches or retries the order automatically.

## Production and HTTP ownership

Use `Builder.Live(true)` only with production credentials:

```go
backend, err := paypalpayment.New(clientID, clientSecret, func(merchant *paypalpayment.Builder) {
    merchant.Live(true)
})
```

The only API origins are `https://api-m.sandbox.paypal.com` and
`https://api-m.paypal.com`. `Builder.HTTPClient` supplies a borrowed transport,
jar and timeout; the client value is copied and redirects are disabled. A nonpositive
timeout becomes 30 seconds. `Driver.HTTPClient()` exposes the actual HTTP client,
not an SDK. Treat it as read-only during use. The driver never closes a borrowed
transport and needs no start/stop lifecycle.

Credentials and cached tokens stay private. Token refresh is serialized and
cancelable, with expiry skew and no refresh goroutine. A cold operation makes
one OAuth request and one business request. A warm ordinary operation makes
one business request; completion and cancellation additionally make their
documented state pre-read.

## Identity, money, and subscriptions

- Checkout IDs are `order:<native>` or `subscription:<native>`.
- Capture payment IDs are `capture:<native>`. Native sale observations use
  `sale:<native>`, but sale reads and refunds return `payment.ErrUnsupported`
  before any request. Current v2 capture APIs do not establish sale compatibility;
  no deprecated v1 sale route is guessed.
- V2 refund IDs are `refund:<native>`. Subscription IDs retain native `I-*`.
- Preserve all these IDs verbatim. Wrong resource families fail locally.

Amounts use canonical core minor units and exact decimal strings. The reviewed
PayPal wallet subset is USD, EUR, GBP, AUD, CAD, CHF, CZK, DKK, HKD, HUF, JPY,
MXN, NOK, NZD, PLN, SEK, SGD, BRL, and CNY. BHD, KWD, OMR, KRW, INR, and ZAR
are rejected locally. Country and account restrictions remain PayPal policy.
See PayPal's [currency rules](https://developer.paypal.com/reference/currency-codes/).

Core's HUF scale remains ISO two: `Money{Amount: 1000, Currency: "HUF"}` sends
PayPal `"10"`, and incoming `"10"` becomes 1000 canonical minor units. Fractional
forints such as 1234 canonical units fail before OAuth or business I/O. HUF and
JPY provider decimals are strict; even a fractional suffix containing only zeros
is rejected. No amount is rounded.

`RefundPayment` always sends the positive explicit requested amount and its
caller-owned key. There is no implicit remaining-balance refund or pre-lookup.
PayPal enforces original currency and remaining refundable balance. A refund
lookup accepts a capture relationship only from a parsed `up` link on a fixed
PayPal HTTPS origin; links are never fetched.

The link may use either fixed sandbox or live origin. Authority matching is
exact: userinfo, foreign or differently cased hosts, and any explicit port
(including `443`) are rejected. Query content, a bare `?`, nonempty fragments,
escaped paths, and identities outside 1–255 ASCII letters, digits, underscores
or hyphens leave `PaymentID` empty without failing the refund lookup. A bare `#`
has no fragment content after URL parsing and remains accepted. The driver does
not normalize authorities or follow links to recover a missing relationship.

`CreateSubscriptionCheckout` accepts an existing plan. It exposes the native
subscription ID before approval. `CompleteCheckout` for subscriptions is one
read-only approval observation; it never activates or polls. Lifecycle state and
optional next billing time do not prove settlement or paid-through time.

Cancellation is immediate for ACTIVE or SUSPENDED subscriptions. Already-canceled
subscriptions succeed without another mutation. Pending, expired and unknown
states fail explicitly. The cancel POST sends a supplied reason or `Customer
request`, has no idempotency header, and accepts an empty 204 without a follow-up
GET. It performs no refund, proration or end-of-period scheduling.

## Failure and reconciliation

No transport retries, 401 token refresh/replay, or successful-mutation hydration
occurs. Only `ORDER_ALREADY_CAPTURED` and `SUBSCRIPTION_STATUS_INVALID` 422
conflicts allow one explicit reconciliation read. Other confirmed 4xx errors map
to payment categories such as unauthorized, forbidden, not found, declined,
invalid state/request and rate limited. An instrument decline leaves a fresh
checkout attempt to your application.

Dispatched mutation transport/5xx failures and malformed successful results wrap
`payment.ErrOutcomeUnknown` and preserve their causes. Cancellation after dispatch
also remains discoverable with `errors.Is`. A read failure or OAuth failure before
business dispatch is not an unknown money-moving outcome. Reconcile uncertain
results before deciding whether to reuse the same provider-scoped key; provider
retention limits still apply.

Malformed successful GET JSON and unusable OAuth responses wrap
`payment.ErrUnavailable`, preserving the diagnostic cause without including it
in the safe error text. Neither implies a dispatched financial mutation. This
does not change the outcome-unknown policy for malformed mutation responses.

A 4xx rejection of the current call does not establish that an earlier attempt
with the same operation key failed. Keep any earlier uncertainty and reconcile
it before choosing a fresh key, including after an in-progress conflict. The
driver retains no cross-attempt outcome history.

`errors.As(err, &native)` with `var native *paypalpayment.Error` exposes safe
`Status`, `Code`, `Issue`, and `DebugID`. Error messages never include provider
body text. Underlying transport and decoding causes remain available and can
contain sensitive diagnostics; do not log them without redaction.

Success bodies are limited to 1 MiB; OAuth and error bodies to 64 KiB. Reads
detect overflow with limit+1 and close every response body. Close errors after a
completed bounded read are ignored because they cannot undo the provider result.

Inbound notifications are an independent opt-in
[`paypalwebhook` adapter](../paypalwebhook/README.md). No handler, database,
deduplication policy, fulfillment action or renewal engine is installed for you.

## Testing your integration

Use [memory simulation or scripted replies](../../../cmd/arkoren/skills/arkoren/references/guides/payments/testing.md)
for application tests without financial effects. Verify real buyer approval and
postback notification authentication separately with dedicated sandbox accounts;
mock events do not establish real signature verification or settlement. There is
no bundled PayPal emulator. See
[PayPal setup and tasks](../../../cmd/arkoren/skills/arkoren/references/guides/payments/drivers.md#paypal)
and the [payment API](https://pkg.go.dev/arkoren.dev/driver/paypal/paypalpayment).
