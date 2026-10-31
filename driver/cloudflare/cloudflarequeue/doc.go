// Package cloudflarequeue provides a Cloudflare Queues-backed queue driver.
//
// The driver sends jobs with the Queues REST API and consumes them as an HTTP
// pull consumer, so it works from any host, not only from Cloudflare Workers.
// [New] builds a Cloudflare API client from an API token and [NewFrom] wraps a
// caller-owned one. Both map application queue names to Cloudflare queue IDs.
//
// Cloudflare returns pull requests immediately instead of holding them open,
// so [Driver.Consume] polls and backs off while a queue is empty. It cannot
// extend a lease: a handler must finish within [Builder.VisibilityTimeout].
//
// The queue behind each name needs an HTTP pull consumer. Create it with
// `wrangler queues consumer http add <queue>` or in the dashboard; add a
// dead-letter queue there to keep rejected and exhausted messages.
//
// Both constructors invoke ordered Builder callbacks directly once on one fresh
// draft. Nil entries panic; expanded nil/empty slices omit edits. Publication
// captures routing before callbacks and copies completed settings, not clients
// or loggers. NewFrom ignores
// Endpoint and never closes the supplied client. Retained drafts cannot change
// a constructed driver.
// Both constructors perform no network I/O and need no Start/Stop hook. The
// caller owns Consume's context; cancellation stops remaining handlers in an
// already-pulled batch and leaves their leases to expire for redelivery.
// Defaults are one message per pull, a 30-second lease, and empty-poll waits
// doubling from one second to ten seconds. Lease renewal is unsupported.
// Retry rounds positive delays up to seconds and rejects delays above 24 hours
// without shortening them or settling the delivery. Acknowledge and Retry need
// exactly one matching native success count; warnings with that count may still
// succeed. Unconfirmed settlements remain open for an explicit later attempt.
// Nil loggers resolve slog.Default at construction. Handler-returned errors
// (including settlement failures), Fail causes and confirmed warnings are logged
// as supplied, not recursively sanitized; keep credentials and payloads out of
// those diagnostic values.
//
// Example:
//
//	driver, err := cloudflarequeue.New(token, accountID, queues, func(consumer *cloudflarequeue.Builder) {
//		consumer.BatchSize(5)
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
package cloudflarequeue
