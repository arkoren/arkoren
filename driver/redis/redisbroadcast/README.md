# Redis Event Bus

`arkoren.dev/driver/redis/redisbroadcast` adapts Redis Pub/Sub to the Arkoren
`broadcast.PublisherDriver` and `broadcast.ConsumerDriver` contracts. It carries messages between processes; it is not
the in-process `event.Bus`.

Install with `go get arkoren.dev/driver/redis` and import
`"arkoren.dev/driver/redis/redisbroadcast"`. The
[Redis broadcast guide](../../../cmd/arkoren/skills/arkoren/references/guides/broadcasting/drivers.md#redis)
shows facade wiring and a standalone local Redis server. Pub/Sub ignores database
numbers; use disjoint prefixes and broker ACLs where publishers are untrusted.

Configure `New` with `func(broker *redisbroadcast.Builder)` callbacks. They
run once in order on a fresh draft; nil entries panic and expanded nil slices
omit edits. `NewFrom` applies only Prefix and Logger and borrows the client.
Native TLS configurations remain shared objects. The redacted `Config` record
is diagnostic data, not a constructor input.

`New` defaults to TCP at `localhost:6379`, database zero, no credentials, no TLS,
and the literal prefix `arkoren.`. It honors operation deadlines during native
command socket I/O. `NewFrom` does not change the client's settings; configure
`redis.Options.ContextTimeoutEnabled` before creating a shared native client
when deadlines must bound command reads and writes.

`Publish` JSON-encodes the complete broadcast envelope and sends it to the
prefixed channel. Raw `Subscribe` receives those envelope bytes, not only the
application payload. Handler errors are not retried; recovered handler panics
use the configured shared logger, or `slog.Default()` when none is supplied.
Pre-canceled publishes return the context error before name validation and send
nothing.

Stop consumers and unsubscribe raw listeners before stopping a borrowed-client
driver. Its `Stop` waits for subscriptions but does not close the shared client;
`New`'s driver closes the client it owns.
