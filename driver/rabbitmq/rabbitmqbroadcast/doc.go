// Package rabbitmqbroadcast provides RabbitMQ (AMQP) publisher and consumer drivers for the broadcast contracts.
//
// [New] owns the connection it creates. [NewFrom] accepts
// a caller-owned connection and does not close it.
// Both constructors apply callbacks once in order on a fresh Builder before
// validation; nil entries panic and expanded nil slices make no edits. Retained
// drafts cannot change the published exchange or logger.
// Empty Exchange selects "arkoren:events"; nil Logger resolves slog.Default()
// at construction. All delivery remains ephemeral and automatically acknowledged.
//
// Consume drops malformed envelopes, invalid concrete names, and envelopes whose
// name differs from the actual AMQP delivery routing key. Raw Subscribe callbacks
// still receive uninterpreted bytes. Routing is not payload authentication: use
// broker permissions and separate exchanges or virtual hosts for untrusted apps.
// On a shared topic exchange, topic permissions must restrict publication keys
// if publishers are not allowed to invoke every broadcast name.
// Raw Subscribe bytes contain the complete JSON broadcast.Message envelope, not
// just its application Payload. Publish succeeds without any active subscriber;
// confirmation is broker acceptance, not retention or processing evidence.
//
// Owned setup and Ping cancellation closes the socket, interrupting all channels
// sharing it. Consume setup follows that rule; normal consumption cancellation
// first closes just its consumer channel, allowing five seconds before aborting
// an unresponsive owned socket. Stop releases an owned connection before waiting
// for publisher-channel close or confirmations. It bounds graceful close to five
// seconds or ctx's earlier end, including forced release for expired contexts.
// Serialize Start and Stop, and reconstruct after an owned transport abort.
//
// NewFrom preserves caller ownership. Its native Connection accessor returns the
// supplied pointer even before Start. The SDK has no in-flight context support
// for channel-open, topology, write, or channel-close RPCs: borrowed operations
// require a responsive peer or caller-managed socket deadlines/connection close.
// The driver cannot interrupt those RPCs by closing a borrowed connection.
//
// Typical setup connects the driver before running a core consumer:
//
//	driver, err := rabbitmqbroadcast.New("amqp://localhost:5672/")
//	if err != nil {
//		return err
//	}
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
//	consumer := broadcast.NewConsumer(driver)
//	return consumer.Run(ctx)
package rabbitmqbroadcast
