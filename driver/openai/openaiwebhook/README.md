# OpenAI inbound webhooks

`arkoren.dev/driver/openai/openaiwebhook` verifies OpenAI notifications with
the official Go SDK. It needs a dashboard endpoint signing secret, not an API
key, and performs no network I/O. Bind `ResponseCompleted`, `ResponseFailed`,
`ResponseCanceled`, or `ResponseIncomplete` through core `webhook.Handler`.
Their payloads contain a response ID and the provider event time, not output.

The SDK enforces a five-minute signature timestamp window in both directions.
`Delivery.ID` uses the signed `webhook-id` header, OpenAI's documented
idempotency key. Applications own deduplication, idempotent effects, routing,
body limits, queue dispatch and explicit response retrieval. Authenticated
unknown events are acknowledged; malformed bound payloads produce 503.

See the [API reference](https://pkg.go.dev/arkoren.dev/driver/openai/openaiwebhook)
for wiring and the [OpenAI webhook guide](https://developers.openai.com/api/docs/guides/webhooks)
for endpoint setup. Install with `go get arkoren.dev/driver/openai`. Register
the public HTTPS URL and subscribed events in your OpenAI project; durably
accept the response reference before acknowledging it. Authentication failures
return 400 by default; acceptance/decode failures return 503 for provider retry.
