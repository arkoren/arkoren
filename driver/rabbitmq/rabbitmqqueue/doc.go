// Package rabbitmqqueue provides RabbitMQ work queues with persistent messages,
// mandatory publisher confirms, and manual consumer acknowledgements.
//
// It is intentionally distinct from arkoren.dev/driver/rabbitmq/rabbitmqbroadcast:
// jobs use durable, competing consumers with manual acknowledgement, rather
// than ephemeral event fan-out queues.
// Construction callbacks run once in order on a fresh Builder before validation;
// nil entries panic and expanded nil slices make no edits. Queue maps are copied
// on input and publication, so retained drafts cannot change routing.
// Queue names and routing keys must each be unique. The optional failure archive
// must not also be a work queue. Broker names and deployed wire envelopes are
// used unchanged; changing this topology is an application-owned migration.
//
// Construct a driver with [New] and a required trusted logical queue map,
// and call [Driver.Start] before dispatching or consuming:
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		driver := compose.Must(rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{
//				"emails": {
//					Name:       "app.emails",
//					RoutingKey: "emails",
//				},
//		}))
//		compose.Cleanup(driver.Stop)
//		compose.Startup(driver.Start)
//	})
//	if err != nil {
//		return err
//	}
//
// New uses a connection without SDK automatic recovery. [NewFrom] rejects
// recovery-enabled connections with [ErrInvalidConfig] without closing them or
// changing their configuration: recovered channel objects can reuse delivery
// tags and let an old handler settle a different job. Caller-owned connections
// must remain non-recovering and must not be reconnected in place. After a
// connection fails, cancel and wait for consumers and stop the driver before
// creating a new driver with a fresh connection. Stop never closes a connection
// supplied to NewFrom. Job handlers must tolerate redelivery.
//
// # Publication and retry durability
//
// Dispatch, delayed retry, and failure archiving require a positive publisher
// confirm without a preceding basic.return. An unroutable transfer returns
// ErrPublishNotConfirmed and leaves its source unsettled. A lost confirmation or
// an acknowledgement failure can still duplicate work; handlers must be idempotent.
// Mandatory routing proves that at least one queue accepted the message, not that
// the application's intended binding or broker retention policies are correct.
// Keep the work exchange and bindings under trusted administration.
//
// Delayed retries use durable per-delay queues with per-message TTL and
// dead-letter routing back to the work exchange. This confirms acceptance by the
// retry queue, not subsequent forwarding. RabbitMQ's default dead-lettering can
// lose expired messages when the target is unavailable or unroutable. Stronger
// forwarding requires broker-provisioned quorum retry queues and at-least-once
// dead-lettering with reject-publish overflow policy, working bindings, and
// sufficient storage. This driver does not enable or verify those prerequisites.
// Existing queue types cannot be changed by redeclaration. Retry queues also
// expire after an unused interval of twice their bucket delay plus one hour;
// queue expiry or administrative deletion can discard pending work even with
// protected dead-lettering. There is no unconditional at-least-once guarantee.
// TTL expiry is not an exact scheduler, especially behind older queued messages.
//
// # Lifecycle and cancellation
//
// Start and Ping honor cancellation across owned TLS/AMQP setup and SDK RPCs by
// closing the owned socket. This interrupts every channel sharing that connection;
// reconstruct the driver after transport failure. Consume setup follows the same
// rule; ordinary consumption cancellation closes only its consumer channel when
// the broker responds. Cleanup permits five seconds for that channel close before
// aborting an unresponsive owned socket. Handler or settlement errors end Consume
// and release unsettled jobs through channel closure, rather than stalling prefetch.
// Start and Stop are serialized lifecycle operations, not concurrent calls.
//
// Borrowed connections remain caller-owned. The pinned SDK has no cancelable
// channel-open, topology, or channel-close RPC. For NewFrom, those operations and
// cleanup require a responsive peer or caller-managed socket deadlines/connection
// close; a driver context cannot safely interrupt them without violating ownership.
package rabbitmqqueue
