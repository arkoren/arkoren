# PayPal webhook notifications

Use `paypalwebhook` to authenticate incoming PayPal events and bind only the
native events your application needs. This adapter is independent of the
[payment driver](../paypalpayment/README.md).

```sh
go get arkoren.dev/driver/paypal
```

## Authenticate and bind explicitly

Register a webhook in your PayPal account and supply that registration's webhook
ID with your application credentials. Construction is network-free and defaults
to sandbox. The following endpoint accepts a completed capture observation:

```go
import (
    "arkoren.dev/core/http/webhook"
    "arkoren.dev/core/service/payment"
    "arkoren.dev/driver/paypal/paypalwebhook"
)

source, err := paypalwebhook.New(clientID, clientSecret, webhookID)
endpoint, err := webhook.Handler(source, func(listener *webhook.Builder[paypalwebhook.Envelope]) {
    listener.Handle(paypalwebhook.PaymentCaptureCompleted, accept)
})
```

Mount `endpoint` on your application's POST route. Supply `accept` as your
durable acceptance function. You own deduplication, queues, fulfillment,
authorization and entitlement policy. `accept` has type
`webhook.AcceptFunc[payment.Observation]` and returns the durable handoff error.
Bind at least one event; the core handler
rejects an empty binding configuration. It acknowledges authenticated unbound
native keys without attempting their resource conversion.

Use `Builder.Live(true)` with production credentials and registration IDs.
`Builder.HTTPClient` borrows a transport for testing or application HTTP policy.
The client is copied, redirects are disabled, and a nonpositive timeout becomes 30
seconds. No arbitrary API origin is accepted. Each source owns its own cancelable
token cache; no payment driver or background refresh goroutine is constructed.

## Verification and native events

The source requires PAYPAL-AUTH-ALGO, PAYPAL-CERT-URL,
PAYPAL-TRANSMISSION-ID, PAYPAL-TRANSMISSION-SIG, and PAYPAL-TRANSMISSION-TIME.
It sends those fields, your registered webhook ID and the original event bytes
to PayPal's fixed `/v1/notifications/verify-webhook-signature` endpoint, using the
incoming request's context. The `webhook_event` value retains the exact original
bytes, including whitespace. The adapter never downloads the certificate URL.

Only `SUCCESS` authenticates. `FAILURE` wraps `webhook.ErrRejected`; the core
handler answers 400. Missing signing metadata or invalid JSON also rejects
before I/O. OAuth/verification outages and unknown verification statuses are
ordinary errors; the handler answers 503, not invalid signature. Authentication
always precedes resource decoding and event routing.

`Envelope` preserves native `id`, `event_type`, `create_time`, and `resource`.
Delivery IDs use stable event IDs, never transmission-attempt IDs. Event
definitions contain no credentials and convert purely to `payment.Observation`:

- `CheckoutOrderApproved` → `CHECKOUT.ORDER.APPROVED`.
- `PaymentCaptureCompleted`, `PaymentCaptureDenied`, `PaymentCaptureDeclined`,
  and `PaymentCapturePending` preserve their exact native capture keys.
- `PaymentSaleCompleted` and `PaymentSaleRefunded` preserve native sale events.
  A sale refund keeps only an explicit original `sale_id`; its v1 refund resource
  ID is not mislabeled as a v2 refund or capture ID.
- `PaymentCaptureRefunded`, `PaymentRefundPending`, and `PaymentRefundFailed`
  report native v2 refund resources and only observed capture relationships.
- `SubscriptionCreated`, `SubscriptionActivated`, `SubscriptionUpdated`,
  `SubscriptionCanceled`, `SubscriptionSuspended`, and `SubscriptionExpired`
  preserve the `BILLING.SUBSCRIPTION.*` keys, including PayPal's `CANCELLED` spelling.

Approval and lifecycle changes do not prove payment. Pending captures retain an
unknown portable observation kind and native `PENDING`; they are not reported as
succeeded. Money conversion is exact and shared with the payment driver,
including canonical ISO-scale HUF and strict provider zero-decimal precision.
No conversion makes an API call or infers a paid-through date.

For application-owned payloads, use an event's core `Map` operation. No source,
event or decoder automatically registers a route, invokes fulfillment, installs
a local ledger, or starts a renewal engine.

## Errors, limits, and verification

`paypalwebhook.Error` exposes safe HTTP status, code, issue and debug-ID fields
through `errors.As`. Error text does not include provider bodies or certificate
URLs. Underlying causes remain available; apply redaction before logging them.
The shared HTTP client limits success bodies to 1 MiB and OAuth/error bodies to
64 KiB, closes them after bounded reading, and never retries or replays a 401.
Body-close errors after a completed read do not change the observed result.
Inbound size and acceptance/deduplication policy belong to the core webhook
handler's builder.

The source follows PayPal's [remote verification API](https://developer.paypal.com/docs/api/webhooks/v1/#verify-webhook-signature_post)
and [native webhook event catalog](https://developer.paypal.com/api/rest/webhooks/event-names/).
Local tests exercise actual `webhook.Handler` authentication, exact verification
wire bytes, rejected signatures, outages, unbound events and pure observations.
They do not contact PayPal or verify a live account configuration.
