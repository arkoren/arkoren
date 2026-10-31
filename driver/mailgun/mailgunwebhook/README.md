# mailgunwebhook

Receive Mailgun JSON event webhooks with `webhook.Source` and typed native SDK
payloads. Install with `go get arkoren.dev/driver/mailgun`.

See the [API reference](https://pkg.go.dev/arkoren.dev/driver/mailgun/mailgunwebhook).

Mailgun's HMAC covers **timestamp + token only**, not the raw body or event data.
Use HTTPS and protect the receiving transport. The source bounds signature age
but does not cache tokens; event deduplication is application policy. Routes
forms and Alerts are not supported.

Supply the account's HTTP webhook signing key, not its sending API key. Empty
keys and keys containing whitespace or control characters fail construction
with `webhook.ErrInvalid`; the source does not trim or repair credentials.
