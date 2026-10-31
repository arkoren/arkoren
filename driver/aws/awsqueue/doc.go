// Package awsqueue provides an Amazon SQS-backed queue driver.
//
// [New] loads the default AWS configuration. [NewFrom]
// accepts a caller-owned SDK client.
// Both accept ordered Builder callbacks invoked directly once on one fresh
// draft. Nil entries panic; expanded nil/empty slices omit edits. Publication
// copies the required routing map and completed settings, not clients/loggers.
// Queue mappings and completed settings are validated before New loads AWS
// configuration; NewFrom performs no network I/O. VisibilityTimeout defaults
// to 30 seconds when zero, otherwise it must be one second through 12 hours
// inclusive. Requests round fractional seconds up; renewal ticks use half the
// configured duration. SQS can still reject renewal beyond a message's remaining
// visibility lifetime; configuration validation does not guarantee renewal.
// Retry rejects delays above 12 hours rather than silently shortening them;
// native remaining-time failures leave settlement available for another attempt.
// FIFO dispatch and failure reporting retain the existing JSON envelopes and
// supply native group/deduplication fields. Failure archives contain payloads
// and handler error text and require the same access protection as source jobs.
//
// Renewal operations have a budget of half the visibility or ten seconds,
// whichever is shorter, including SDK retries. They continue during canceled
// handler drain. Handler return cancels in-flight renewal before joining its
// worker; no worker is abandoned. Application settlement waits for prior renewal
// without exceeding its own context budget. Native clients and their dependencies
// must honor operation contexts for cancellation to release I/O. Renewal failures
// are logged, not treated as successful settlement; duplicate delivery remains
// possible. As with other RPC errors, cancellation does not prove SQS ignored
// the request. See [Driver.Consume] for context and settlement wiring.
//
// Example:
//
//	driver, err := awsqueue.New(ctx, map[string]string{"emails": "queue-url"}, func(consumer *awsqueue.Builder) {
//		consumer.MaxMessages(5)
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
package awsqueue
