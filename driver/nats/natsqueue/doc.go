// Package natsqueue provides a NATS JetStream-backed queue driver with durable,
// at-least-once delivery. It requires JetStream streams and durable pull
// consumers; Core NATS alone is not supported.
//
// [New] creates and owns its NATS connection, which [Driver.Stop] gracefully
// drains. Callers retain ownership of a connection passed to [NewFrom]; its
// Stop method does not drain or close that connection. By default, streams and
// consumers must be provisioned operationally. Set [Builder.Provision] only when
// the application is permitted to create missing JetStream resources.
//
// Reject terminates the delivery (JetStream Term), so it is never
// redelivered; JetStream has no universal dead-letter operation. Fail publishes an application-level failure
// envelope to the [Builder.FailureSubject], when configured, before acknowledging
// the original message. A failure subject is not a JetStream DLQ guarantee.
//
// Construction callbacks run directly once in order on a fresh Builder; nil
// entries panic and expanded nil slices make no edits. URL slices, queue maps
// and SDK-option outer slices are copied on input and publication. Native SDK
// option functions and their captures remain shared. Start passes them to native
// connection creation only when New owns the connection.
//
// Example:
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		driver := compose.Must(natsqueue.New("JOBS", map[string]natsqueue.Queue{
//			"work": {Subject: "jobs.work", Durable: "work"},
//		}))
//		compose.Cleanup(driver.Stop)
//		compose.Startup(driver.Start)
//	})
//	if err != nil {
//		return err
//	}
//
// Stream and queue topology are required positional inputs, captured before
// callbacks. Work and failure subjects must be distinct literals. Optional
// policy defaults are Batch=1, MaxAckPending=Batch, AckWait=30 seconds, no
// provisioning and the server's MaxDeliver default. Nil Logger uses slog.Default.
// Start rejects headers-only consumers and pull expiry limits below ten seconds;
// provisioning sets that pull limit independently of AckWait. It never repairs
// existing resource settings. Heartbeat errors are logged and can permit
// redelivery; progress updates cannot race a successful delayed settlement.
//
// Cancel and wait for consumers before Stop. Owned Stop waits for native drain
// completion subject to ctx and native DrainTimeout; cancellation forces Close.
// Borrowed connections remain caller-owned and open. Delivery.Message returns
// owned copies of payload bytes and metadata maps. Failure envelopes retain
// application content and error text; protect their broker subjects accordingly.
package natsqueue
