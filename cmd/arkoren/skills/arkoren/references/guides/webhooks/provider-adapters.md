---
title: Provider Adapters
description: Choose the correct provider verifier, supply endpoint credentials, and preserve native event keys and delivery identities.
order: 20
summary:
  - Compare body signatures, token verification, Basic Auth, and remote verification.
  - Configure sources for Resend, Mailgun, Postmark, OpenAI, Stripe, and PayPal.
  - Preserve provider event keys and distinguish delivery identity from resource identity.
---

# Provider Adapters

A valid webhook proves only what its provider's protocol authenticates. A body
signature protects different data from Mailgun's signed token or Postmark's Basic
Auth. Select the matching adapter and credentials before treating a notification
as application input.

Each adapter returns a `webhook.Source` and publishes credential-free event
definitions. Bind those definitions using [Receiving Webhooks](receiving-webhooks.md#binding-handlers).
Outbound drivers do not register endpoints, and their API credentials are not
interchangeable with endpoint verification credentials.

Known authentication failures wrap `webhook.ErrRejected` and answer 400 by
default. A notification that passes verification can still fail envelope or
payload decoding with 503. Neither answer guarantees the provider will retry;
use the [acknowledgement rules](delivery-policies.md#acknowledgement-outcomes)
when deciding your endpoint's failure policy.

## Verification Boundaries

This comparison covers the shipped adapters, not every protocol the providers
offer. All except PayPal verify locally without fetching event resources.
`Delivery.ID` is the identity used by optional core deduplication, not necessarily
the identifier of the email, response, or payment affected.

| Adapter | Authentication | `Delivery.ID` |
| --- | --- | --- |
| [Resend](#resend) | Svix v1 HMAC covering ID, timestamp, and exact body | `svix-id` header |
| [Mailgun](#mailgun) | HMAC covering timestamp + token, not `event-data` | UTC event date + `":"` + event ID |
| [Postmark outbound](#postmark) | Endpoint Basic Auth, not a body signature | `X-PM-Webhook-Trace-Id`, or empty when absent |
| [Postmark inbound](#postmark) | Separate inbound source with endpoint Basic Auth | Inbound `MessageID` |
| [OpenAI](#openai) | SDK verification of exact body and webhook headers | `webhook-id` header, not JSON event ID |
| [Stripe](#stripe) | SDK verification of exact body and `Stripe-Signature` | Native event ID |
| [PayPal](#paypal) | Remote verification of original event and signing metadata | Native event ID, not transmission ID |

Use HTTPS at the public endpoint. Timestamp freshness checks do not prevent
replays inside the accepted window; configure [delivery policies](delivery-policies.md)
and make business effects idempotent. Empty IDs disable core duplicate suppression.
Do not substitute resource IDs or attempt IDs: they can collapse distinct events
or fail to recognize redeliveries.

Mollie's classic callback is an unauthenticated status hint, not an authenticated
source. Use the application-owned [Mollie callback](../payments/drivers.md#mollie-callbacks)
and retrieve authoritative state through the payment API.

## Resend

Choose `resendwebhook` for Resend email notifications. Register your public webhook
URL and selected events in Resend, then load that endpoint's `whsec_` secret. No
sending API key is required. From your application's module directory:

```sh
go get arkoren.dev/driver/resend
```

Import `arkoren.dev/driver/resend/resendwebhook` and construct the source:

```go
source, err := resendwebhook.New(signingSecret)
```

The secret must contain nonempty standard base64 after `whsec_`, without line
breaks. Invalid secrets return `webhook.ErrInvalid`. The verifier requires unique
Svix authentication headers and accepts matching v1 signatures within five
minutes of local time in either direction. It verifies before parsing the envelope;
bad signatures wrap both `webhook.ErrRejected` and `resendwebhook.ErrInvalidSignature`.

Bind `EmailSent`, `EmailDelivered`, `EmailBounced`, `EmailComplained`, or
`EmailReceived` to receive typed `Email` metadata. `EmailDelivered` means acceptance
by the recipient's mail server, not inbox placement or reading. `EmailReceived`
does not fetch email bodies or attachments; perform those lookups in application
work after acceptance. See the [mail integration](../mail/drivers.md#receiving-resend-notifications)
and [`resendwebhook` API](https://pkg.go.dev/arkoren.dev/driver/resend/resendwebhook).

## Mailgun

Choose `mailgunwebhook` for Mailgun's JSON event notifications. Configure the
webhook URL and events in Mailgun and load the account's **HTTP webhook signing
key**, not an outbound API key.

> [!WARNING]
> Mailgun signs only timestamp + token, not `event-data`. A successful token check
> does not prove payload integrity. Protect the HTTPS endpoint and account for
> this limitation before allowing a payload to authorize a consequential action.

Install the module:

```sh
go get arkoren.dev/driver/mailgun
```

With import `arkoren.dev/driver/mailgun/mailgunwebhook`, construct the source:

```go
source, err := mailgunwebhook.New(signingKey)
```

Construction rejects empty keys, whitespace, and control characters without
trimming or repairing them. The source checks HMAC and a local timestamp window
of thirty minutes in either direction. There is no token cache: a valid token
can verify more than once. Signature failures wrap `webhook.ErrRejected` and
`mailgunwebhook.ErrInvalidSignature`.

If delayed deliveries require a wider local window, choose that policy explicitly.
With `time` imported, this alternative construction allows one hour:

```go
source, err := mailgunwebhook.New(signingKey, func(signature *mailgunwebhook.Builder) {
	signature.Tolerance(time.Hour)
})
```

Zero restores thirty minutes; a negative tolerance is invalid. A larger window
also accepts older captured tokens. The window measures signature age, not event
age. Delivery identity instead uses the event's UTC date and native ID because
Mailgun event IDs are unique only within a day.

`Accepted`, `Delivered`, `Failed`, `Opened`, `Clicked`, `Unsubscribed`, and
`Complained` decode native Mailgun event payloads. `Failed` retains temporary or
permanent severity rather than treating every delivery failure as terminal.
See [Mailgun notifications](../mail/drivers.md#receiving-mailgun-notifications)
and the [`mailgunwebhook` API](https://pkg.go.dev/arkoren.dev/driver/mailgun/mailgunwebhook).

## Postmark

Choose `postmarkwebhook` for outbound notifications or parsed inbound email.
Configure endpoint-specific Basic Auth credentials on the Postmark webhook URL;
the sending API token is not a receiver credential. Public HTTPS is mandatory,
though TLS can terminate at a trusted proxy. Apply Postmark IP allowlisting at
the trusted edge. Basic Auth neither signs the body nor prevents a credential
holder from submitting or replaying it.

Install the module:

```sh
go get arkoren.dev/driver/postmark
```

Import `arkoren.dev/driver/postmark/postmarkwebhook`. For outbound notifications:

```go
source, err := postmarkwebhook.New(username, password)
```

The constructor rejects empty credentials, control characters, and a colon in
the username. The source rejects missing, incorrect, or duplicate Authorization
headers before parsing the body. Authentication failures wrap `webhook.ErrRejected`;
malformed authenticated JSON answers 503 rather than a credential rejection.

Bind `Delivered`, `Bounced`, `SpamComplaint`, `Opened`, `Clicked`, or
`SubscriptionChanged` for their typed outbound payloads. Core deduplication uses
`X-PM-Webhook-Trace-Id` when present. Do not replace a missing trace ID with an
outbound `MessageID`: different opens or clicks can share the same message ID.

For inbound email, configure a separate URL and construct a separate source:

```go
source, err := postmarkwebhook.NewInbound(username, password)
```

Bind `Inbound` on that receiver. Inbound JSON has no `RecordType`, so the adapter
supplies the exact event key `Inbound`; its delivery ID is the inbound `MessageID`.
The same HTTPS and credential-replay limits apply. Treat email contents as
untrusted input even when endpoint authentication succeeds.

See [outbound notifications](../mail/drivers.md#receiving-postmark-notifications),
[inbound email](../mail/drivers.md#receiving-inbound-email), and the
[`postmarkwebhook` API](https://pkg.go.dev/arkoren.dev/driver/postmark/postmarkwebhook).

## OpenAI

Choose `openaiwebhook` for background response lifecycle notifications. Register
the endpoint and events in OpenAI and load its dashboard endpoint secret, not
your model API key. Install the module:

```sh
go get arkoren.dev/driver/openai
```

With import `arkoren.dev/driver/openai/openaiwebhook`:

```go
source, err := openaiwebhook.New(signingSecret)
```

The secret must use canonical `whsec_`-prefixed base64 with at least 24 decoded
bytes. Invalid secrets wrap `webhook.ErrInvalid`. The OpenAI SDK verifies the
exact body and authentication headers locally before envelope parsing. Signature
failures wrap `webhook.ErrRejected` and `openaiwebhook.ErrInvalidSignature`.
There is no network call or source lifecycle hook.

`ResponseCompleted`, `ResponseFailed`, `ResponseCanceled`, and `ResponseIncomplete`
produce a `Response` reference with an ID and event time, not generated output or
failure details. `ResponseCanceled` retains the native key `response.cancelled`.
Use the response ID for a later API lookup; preserve the distinct `webhook-id`
as `Delivery.ID`. See [response notifications](../llm/drivers.md#response-notifications)
and the [`openaiwebhook` API](https://pkg.go.dev/arkoren.dev/driver/openai/openaiwebhook).

## Stripe

Choose `stripewebhook` for Stripe snapshot events, not thin notifications.
Register the endpoint and event selection in Stripe and load that endpoint's
`whsec_` signing secret separately from the payment API key.

```sh
go get arkoren.dev/driver/stripe
```

Import `arkoren.dev/driver/stripe/stripewebhook`:

```go
source, err := stripewebhook.New(signingSecret)
```

Construction requires a `whsec_` prefix, a nonempty suffix, and no whitespace or
control characters. The Stripe SDK verifies the exact body and `Stripe-Signature`
locally. Signature age defaults to five minutes; an optional construction callback
can set `stripewebhook.Builder.Tolerance`. Zero restores the default, and a
negative value is invalid. Signature failures wrap `webhook.ErrRejected`,
`stripewebhook.ErrInvalidSignature`, and the SDK cause.

Shipped event definitions cover checkout, PaymentIntent, refund, subscription,
invoice, and invoice-payment snapshots and decode to `payment.Observation`.
Bound snapshot decoders require an exact API-version match with the installed
Stripe SDK and the expected resource schema. Mismatches answer 503, not a bad
signature rejection. Authenticated unbound events do not undergo those bound
schema checks.

No Stripe resource is fetched and no payment state is changed by decoding.
Use [Stripe notifications](../payments/drivers.md#stripe-notifications) for
event selection and reconciliation; checkout completion is not itself proof of
collected funds. The full definitions are in the
[`stripewebhook` API](https://pkg.go.dev/arkoren.dev/driver/stripe/stripewebhook).

## PayPal

Choose `paypalwebhook` for PayPal notifications whose original event and signing
metadata can be verified through PayPal's API. You need a client ID, client
secret, and registered webhook ID from the same environment. The source defaults
to sandbox independently of any payment driver.

```sh
go get arkoren.dev/driver/paypal
```

Import `arkoren.dev/driver/paypal/paypalwebhook`. With matching sandbox credentials:

```go
source, err := paypalwebhook.New(clientID, clientSecret, webhookID)
```

Construction makes no network call. Request verification does: PayPal OAuth or
verification outages can prevent acceptance of a genuine notification and answer
503. Missing signing metadata, invalid event JSON, and a confirmed verification
failure wrap `webhook.ErrRejected`. An unknown verification result remains a
processing failure. The `PAYPAL-CERT-URL` header is sent as data to PayPal, not
fetched as an application-selected URL.

For production, explicitly select live mode with matching live credentials and
registration in this alternative construction:

```go
source, err := paypalwebhook.New(clientID, clientSecret, webhookID, func(listener *paypalwebhook.Builder) {
	listener.Live(true)
})
```

To bound verification I/O, `HTTPClient` accepts an application-supplied
`*http.Client`. The source copies it, disables redirects, and uses thirty seconds
when its timeout is nonpositive; transports remain shared and are not closed by
the source. Choose a timeout within the provider's response deadline.

Shipped definitions cover order approval, captures, sales, refunds, and subscription
lifecycle events. They decode to `payment.Observation` without applying business
state. Delivery identity is the event ID, not the retry-specific transmission ID.
Follow [PayPal notifications](../payments/drivers.md#paypal-notifications) for
bindings and reconciliation, and consult the
[`paypalwebhook` API](https://pkg.go.dev/arkoren.dev/driver/paypal/paypalwebhook).

## Boundary Tests

Test with valid provider fixtures, then supply incorrect credentials or required
authentication metadata and confirm rejection before your handler runs. For
body-signing protocols, also confirm that altering the body invalidates the
signature. Do not expect that protection from Mailgun's token signature or
Postmark's Basic Auth; test their stated limits rather than assuming body integrity.
Include authenticated unknown events and bound schema failures: authentication success
does not mean every payload is decodable. For SDK-backed verification, use the
SDK version your application actually ships.

A substitute source can test core outcomes and queue handoff, but it cannot prove
provider authentication, live PayPal verification, or actual provider retries.
Check those boundaries against your configured provider environment separately.
Then choose [Delivery Policies](delivery-policies.md) for receipts, queued work,
and receiver diagnostics; their categories are not provider event keys.
