// Package natsbroadcast provides NATS publisher and consumer drivers for the broadcast contracts.
//
// [Config.MaxReconnects] defaults to [DefaultNATSMaxReconnects] (-1,
// unlimited); zero also selects the default. Use [Builder.NoReconnect] to disable
// reconnection.
// Construction callbacks run once in order on one fresh Builder; nil entries
// panic and expanded nil slices make no edits. Owned URL and root-CA slices are
// copied on input and publication, not native TLS internals. NewFrom borrows its
// connection and applies only Prefix, QueueGroup and Logger.
// NewFrom rejects nil connections and returns an error for invalid routing.
// NKeySeed is encoded user seed material, never a file path; CredentialsFile
// remains the file-based JWT/seed mechanism. Config's String, GoString, and slog
// diagnostics hide URL userinfo and explicit secrets. URL entries that fail
// parsing or lack an authority hostname, including schemeless entries, are hidden
// wholesale without changing native inputs. Format also protects ordinary fmt
// fallback verbs; see Config for the Go format-error paths that bypass methods
// and the safe pattern for externally chosen formats. TLS configurations and
// native clients stay shared.
// Owned Stop waits for draining until ctx or native DrainTimeout closes the
// connection; cancellation forces Close. Borrowed Stop leaves its connection open.
//
// Consume preserves the delivered NATS subject through envelope decoding and
// forwards a message only when that subject equals prefix + "." + message.Name.
// Malformed JSON and mismatches are dropped before core listeners or telemetry
// run. This applies to exact and wildcard deliveries, including queue groups.
// Subscribe remains a raw-byte API and does not validate an envelope.
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		driver := compose.Must(natsbroadcast.New(func(broker *natsbroadcast.Builder) {
//			broker.Prefix("billing")
//		}))
//		compose.Cleanup(driver.Stop)
//		compose.Startup(driver.Start)
//		consumer := broadcast.NewConsumer(driver)
//		compose.Run(consumer.Run)
//	})
//	if err != nil {
//		return err
//	}
//
// A prefix is a routing namespace, not authorization. Enforce broker subject
// permissions for limited-trust publishers. Valid distinct subjects can still
// create many metric destinations; choose a metric driver with bounded series
// retention, independently of tracing or scrape-side limits.
package natsbroadcast
