// Package redisbroadcast provides Redis publisher and consumer drivers for the broadcast contracts.
//
// Redis Pub/Sub fans out JSON envelopes to connected consumers without retention
// or retries. Wrap a Driver with broadcast.NewPublisher and broadcast.NewConsumer
// for typed topics, portable wildcard listeners, and facade telemetry. Start
// verifies connectivity; Stop waits for subscriptions and closes only an owned
// client. NewFrom leaves client ownership with the caller.
// Construction callbacks run directly once in order on a fresh Builder; nil
// entries panic and expanded nil slices make no edits. NewFrom applies only
// Prefix and Logger. Retained drafts cannot change published settings; native
// clients, loggers and TLS objects remain shared.
// New defaults to TCP at localhost:6379, database zero, no credentials, no TLS,
// and the literal prefix "arkoren.". Its native command client honors operation
// deadlines. NewFrom retains its client's settings; configure native
// ContextTimeoutEnabled before wrapping it when deadlines must bound socket I/O.
// Publish carries a JSON envelope, while raw Subscribe returns those envelope
// bytes unchanged. A nil logger selects slog.Default for recovered handler panics.
// Pre-canceled Publish calls return the context error before validating names or
// encoding an envelope and do not send a command.
//
// For example, configure a disjoint namespace before wiring the lifecycle:
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		driver := redisbroadcast.New(func(broker *redisbroadcast.Builder) {
//			broker.Prefix("billing:")
//		})
//		compose.Cleanup(driver.Stop)
//		compose.Startup(driver.Start)
//		consumer := broadcast.NewConsumer(driver)
//		compose.Run(consumer.Run)
//	})
//	if err != nil {
//		return err
//	}
//
// Prefix bytes are literal even when they contain Redis glob metacharacters.
// Consume rejects malformed envelopes and requires their names to equal the
// actual channel suffix before dispatch. Subscribe exposes raw bytes instead
// and cannot distinguish overlapping application namespaces: choose prefixes
// neither of which starts with the other for raw subscribers. Redis Pub/Sub
// ignores database numbers. Prefixes and envelope checks are routing safeguards,
// not authorization; use Redis channel ACLs or separate instances between
// mutually untrusted applications. Overlapping broad subscriptions can still
// transfer foreign payloads to the process before Consume rejects them.
package redisbroadcast
