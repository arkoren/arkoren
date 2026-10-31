# Arkoren Queue Cloudflare

`arkoren.dev/driver/cloudflare/cloudflarequeue` provides a Cloudflare Queues
driver that works from any host: it sends jobs with the Queues REST API and
consumes them as an HTTP pull consumer, using the official
`github.com/cloudflare/cloudflare-go/v7` SDK. It sends and receives complete
`queue.Message` envelopes as text messages.

Install with `go get arkoren.dev/driver/cloudflare` and import
`"arkoren.dev/driver/cloudflare/cloudflarequeue"`. Provision the queues and
configure HTTP pull consumers before consuming. The
[Cloudflare queue guide](../../../cmd/arkoren/skills/arkoren/references/guides/queues/drivers.md#cloudflare)
covers token permissions, worker wiring, and lease limits.

`New(apiToken, accountID, queues, configure...)` needs an API token with Queues read
and write permission, the account ID, and a map from application queue names to
Cloudflare queue IDs. `NewFrom(client, accountID, queues, configure...)` wraps a
caller-owned client. Both take trailing `func(*cloudflarequeue.Builder)` callbacks:

```go
driver, err := cloudflarequeue.New(token, accountID, queues, func(consumer *cloudflarequeue.Builder) {
	consumer.BatchSize(5)
})
```

Callbacks run directly once, synchronously in order on one fresh draft before
validation. Nil entries panic; expanded nil/empty slices omit edits. Settings
return the same draft. Both constructors copy the required queue map before
callbacks, so a callback's edits to your original map cannot change routing.
Do not mutate it concurrently with capture. Published settings are detached;
retained drafts cannot change drivers. Native clients and loggers remain shared.
`NewFrom` ignores `Endpoint` and applies all other settings; `Client()` returns
that same pointer without transferring ownership or closing its transport.

Cloudflare answers a pull immediately, so `Consume` polls and backs off while a
queue is empty. It cannot extend a lease, so a handler must finish within
`VisibilityTimeout`. Reject leaves the message to its lease; configure a
dead-letter queue on the pull consumer to keep rejected and exhausted messages.
Defaults are one message per pull, a 30-second lease, and empty-poll waits doubling
from one second to ten seconds. A larger batch is sequential: earlier handlers
consume later messages' lease time too. Cancellation is checked before every
handler, including remaining messages in a pulled batch; their unused leases
expire for redelivery. The active handler must return cooperatively.

Positive retry delays are rounded up to seconds. Delays beyond 24 hours fail
without a vendor request or settlement; the driver never shortens the requested
lower bound. Acknowledge and Retry require exactly one matching native success
count, not merely a successful HTTP response. A warning accompanying that count
can succeed; an absent or zero matching count fails and leaves local settlement
open. Repeated or concurrent settlement returns `ErrDeliverySettled`.

Construction makes no requests and needs no Start/Stop hook. You own the consumer
context and native transport lifecycle. A nil logger selects `slog.Default()` at
construction. Handler-returned errors (including settlement failures), Fail causes
and confirmed warnings are logged as supplied; the driver does not recursively
sanitize native objects or arbitrary caller errors. Keep credentials and payloads
out of them.

See the [API reference](https://pkg.go.dev/arkoren.dev/driver/cloudflare/cloudflarequeue).
