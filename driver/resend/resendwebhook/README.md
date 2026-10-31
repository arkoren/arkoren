# resendwebhook

Authenticate Resend's Svix webhook requests without an API client, then bind
typed email metadata events with `arkoren.dev/core/http/webhook`.

Supply the endpoint's `whsec_` signing secret as one unbroken standard-base64
value, independently of your sending API key. Empty, malformed, or line-broken
secrets fail construction with `webhook.ErrInvalid`; no credential bytes appear
in that error. The source never trims or repairs the secret.

Install with `go get arkoren.dev/driver/resend`. See the
[API reference](https://pkg.go.dev/arkoren.dev/driver/resend/resendwebhook)
for endpoint setup, signature freshness, event identity and deduplication.
