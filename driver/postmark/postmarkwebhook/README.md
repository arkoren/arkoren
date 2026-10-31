# Postmark webhooks

`arkoren.dev/driver/postmark/postmarkwebhook` returns a core `webhook.Source`
authenticated with endpoint-specific HTTP Basic Auth. It needs no mail driver,
API token, SDK, network lookup, or lifecycle hooks.

Install with `go get arkoren.dev/driver/postmark`. Follow the
[API reference](https://pkg.go.dev/arkoren.dev/driver/postmark/postmarkwebhook)
to configure provider endpoints, bind your durable acceptance handler, and mount
application-owned POST routes.

Use `New(username, password)` for outbound notifications and
`NewInbound(username, password)` on a separate route for parsed inbound email.
Bind `Delivered`, `Bounced`, `SpamComplaint`, `Opened`, `Clicked`,
`SubscriptionChanged`, or `Inbound` through core `webhook.Handler`.

The public endpoint **must use HTTPS**. Configure matching Basic Auth credentials
in Postmark's webhook URL. Postmark does not sign webhook bodies: Basic Auth
authenticates possession of the credentials, not payload integrity independently
of TLS, freshness, or Postmark origin. Add Postmark IP allowlisting at your trusted
edge where possible. Do not trust arbitrary forwarded IP headers.

Outbound delivery IDs use `X-PM-Webhook-Trace-Id`, which Postmark documents as
stable across retries. Without it, delivery IDs are empty and core deduplication
is disabled. Never use `MessageID` alone for outbound deduplication: one message
can produce multiple opens, clicks and recipient notifications. Inbound email
uses its received `MessageID`. Use distinct application deduplication scopes for
different endpoints; handlers must remain idempotent.

Authentication failures wrap `webhook.ErrRejected` (core defaults to 400).
Malformed authenticated envelopes or bound field types return a retryable 503.
Unknown outbound `RecordType` values are acknowledged without payload decoding.
Optional or omitted fields retain their zero values. Payloads contain personal
data; inbound HTML and sender claims remain untrusted.

See the [Postmark webhook documentation](https://postmarkapp.com/developer/webhooks/webhooks-overview)
for authentication, trace identity, retry policy, and IP ranges, and the
[inbound webhook documentation](https://postmarkapp.com/developer/webhooks/inbound-webhook)
for the distinct inbound payload and retry policy.
