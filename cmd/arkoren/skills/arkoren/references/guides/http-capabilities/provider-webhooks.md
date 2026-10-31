---
title: Provider Webhooks
description: Choose a provider webhook adapter by its verification boundary and stable delivery identity, then use shared receipt policy without rewriting provider keys.
order: 50
---

# Provider Webhooks

## Introduction

A provider notification can prove different things depending on its protocol:
a signature can cover the body, cover only a token, or be absent when the endpoint
uses Basic Auth. Choose the adapter and endpoint credentials for that protocol
before treating a notification as input to your application.

Each adapter supplies a `webhook.Source`; your application still binds handlers
and owns [receiving policy](webhooks.md), queues, and deduplication. Outbound mail,
model, and payment drivers do not register these endpoints or require their use.

## Choose the verification boundary

Use the linked setup guide for installation, credentials, provider registration,
and route mounting. This comparison describes the shipped adapters, not every
webhook protocol a provider offers. API links identify the exact adapter package.
Delivery IDs are the values returned to core for optional deduplication:

| Adapter API | Verification boundary | `Delivery.ID` | Setup |
| --- | --- | --- | --- |
| [Resend](https://pkg.go.dev/arkoren.dev/driver/resend/resendwebhook) | Endpoint `whsec_` secret; local Svix v1 HMAC over `svix-id`, timestamp, and the exact body | `svix-id` header | [Resend notifications](../mail/drivers.md#receiving-resend-notifications) |
| [Mailgun](https://pkg.go.dev/arkoren.dev/driver/mailgun/mailgunwebhook) | Account HTTP webhook signing key; local HMAC over timestamp + token, **not** `event-data` | UTC event date + `":"` + native event ID | [Mailgun notifications](../mail/drivers.md#receiving-mailgun-notifications) |
| [Postmark outbound](https://pkg.go.dev/arkoren.dev/driver/postmark/postmarkwebhook) | Endpoint-specific Basic Auth, not a body signature or sending API token | `X-PM-Webhook-Trace-Id`, or empty if absent | [Postmark notifications](../mail/drivers.md#receiving-postmark-notifications) |
| [Postmark inbound](https://pkg.go.dev/arkoren.dev/driver/postmark/postmarkwebhook) | Separate `NewInbound` source and route with endpoint Basic Auth | Inbound `MessageID` | [Inbound email](../mail/drivers.md#receiving-inbound-email) |
| [OpenAI](https://pkg.go.dev/arkoren.dev/driver/openai/openaiwebhook) | Endpoint `whsec_` secret; exact body and webhook headers passed to the OpenAI SDK verifier, without network verification | `webhook-id` header, not JSON `Envelope.ID` | [Background notifications](../llm/drivers.md#receiving-background-notifications) |
| [Stripe](https://pkg.go.dev/arkoren.dev/driver/stripe/stripewebhook) | Endpoint `whsec_` secret, separate from the payment API key; exact body and `Stripe-Signature` passed to the Stripe SDK verifier | Native event ID | [Stripe notifications](../payments/drivers.md#receiving-stripe-notifications) |
| [PayPal](https://pkg.go.dev/arkoren.dev/driver/paypal/paypalwebhook) | Matching client ID, client secret, and registered webhook ID; remote PayPal verification of the original event and signing metadata | Native event ID, not transmission ID | [PayPal notifications](../payments/drivers.md#receiving-paypal-notifications) |

Mailgun's token check does not establish integrity of `event-data`. Keep the
endpoint on HTTPS and account for that limitation when acting on the payload.
Postmark Basic Auth requires HTTPS at the public endpoint; TLS can terminate at
a trusted proxy. Anyone holding those credentials can submit or replay a body.
Apply IP allowlisting at the trusted edge as described in the provider setup.
Neither protocol gains body-signature or replay guarantees from core.

The local-verification adapters do not fetch remote event resources. PayPal
verification does network I/O during the request; a verification service failure
can therefore prevent acceptance even for a genuine notification. Its source
defaults to sandbox; select live mode separately from the outbound driver with
matching credentials and webhook registration. Construction itself makes no
network call. A `PAYPAL-CERT-URL` header is sent as verification data to PayPal,
not fetched as an application-selected URL.

Mollie's classic callback is an unauthenticated status hint, not one of these
authenticated sources. Follow [Mollie status hints](../payments/drivers.md#receiving-mollie-status-hints)
and mount an application handler rather than treating it as a `webhook.Source`.

## Keep provider keys and receipt identities

Bind the adapter's credential-free event definitions without replacing their
provider keys with Arkoren diagnostic categories. Keys are exact, case-sensitive
strings. For example, PayPal's `PAYMENT.CAPTURE.COMPLETED` is not interchangeable
with an application topic name. Postmark inbound uses the adapter key `Inbound`
because its inbound JSON has no `RecordType`.

Use the returned `Delivery.ID` unchanged. An empty ID disables core suppression;
do not invent an attempt ID or substitute Postmark outbound `MessageID`, which
can be shared by distinct opens or clicks. Keep existing namespace names,
separators, and retention when upgrading; the
[receipt migration rules](webhooks.md#skipping-duplicates) explain how to preserve
stored IDs. A timestamp freshness check alone does not prevent replay inside
its acceptance window.

For application-owned queue payloads, `Event.Map` keeps the provider key, delivery
ID, and type. The original decoder runs before conversion, and a decoder failure
prevents conversion. Keep conversion pure and free of I/O; do remote lookups in
acceptance or a worker, not while decoding. See [queuing events](webhooks.md#queuing-events)
for the durable-payload boundary.

## Interpret receiver categories separately

Receipt categories describe the receiver's processing, not a provider's business
outcome. `RejectedEvent.Reason`, `FailedEvent.Stage`, and `IgnoredEvent.Reason`
use `webhook.RejectionReason`, `webhook.FailureStage`, and `webhook.IgnoreReason`.
Their [constants and string values](webhooks.md#reacting-to-rejections-and-failures)
are unchanged on the wire; empty zero values remain unspecified. Existing
string-only alert APIs need conversions such as `string(failure.Stage)`.

A known rejected source error reports `RejectionReasonVerify`; an unclassified
source error reports `FailureStageVerify`. That failure can mean an unavailable
remote verifier or an envelope-processing error, not necessarily a bad signature.
Bound decoder and `Map` conversion errors report `FailureStageDecode`. Every
acceptance error reports `FailureStageAccept`, including `ErrSkip`; it does not
prove that application work was rolled back.

Core commits failure responses locally. Oversized bodies keep the native 413
body-limit problem before the source runs; a provider adapter does not replace
that representation. An authenticated source/decoder/converter `ErrSkip` answers
200 before acceptance without recording an ID, while an acceptance no-op must
return nil. See [what the provider sees](webhooks.md#what-the-provider-sees).

## Verify the provider boundary

Use provider-specific authentication fixtures and the SDK version your application
ships to check signature handling, timestamps, and payload schemas. A handler test
with a substitute source can prove status selection, queue handoff, category bytes,
and deduplication policy; it does not exercise an SDK's native signature rules,
remote PayPal acceptance, or a provider's actual retry and redelivery behavior.
Validate those boundaries separately against the configured provider environment.
A 503 never promises that the provider will retry.

After choosing an adapter and its setup guide, bind your application action with
[Receiving an Event](webhooks.md#receiving-an-event).
