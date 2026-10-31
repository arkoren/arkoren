# NATS Event Bus

`arkoren.dev/driver/nats/natsbroadcast` adapts NATS subjects to portable Arkoren topics.

Install with `go get arkoren.dev/driver/nats` and import
`"arkoren.dev/driver/nats/natsbroadcast"`. Register `Start` and `Stop` with
your application before running the consumer. The
[NATS broadcast guide](../../../cmd/arkoren/skills/arkoren/references/guides/broadcasting/drivers.md#nats)
covers queue groups, native connections, and a standalone local server.

## Configuration

`New` defaults to unlimited reconnects (`MaxReconnects` of -1).
Set `NoReconnect(true)` in a construction callback to disable reconnection:

```go
driver, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
	broker.URLs("nats://localhost:4222")
	broker.NoReconnect(true)
})
```

Callbacks run once, in order on one draft. Nil callback entries panic; expanded
nil slices make no changes. `NewFrom` borrows its connection and applies only
`Prefix`, `QueueGroup`, and `Logger`. TLS configurations remain shared native
objects. The public redacted `Config` record is diagnostic data, not an input.

`NewFrom` returns `(*Driver, error)` and rejects nil connections or invalid
routing settings. It never creates or closes a borrowed connection. `NKeySeed`
accepts encoded user seed material, not a filename; use `CredentialsFile` for
file-based JWT/seed credentials. New validates seed material without file I/O.

For configuration logs, pass `Config` directly to `slog` or use its `String`
method. These diagnostics hide URL userinfo, including passwordless bearer tokens,
as well as explicit password, token, and seed fields. URL entries with parse errors
or no authority hostname are replaced wholesale, including schemeless entries.
Formatting does not mutate the record or change URLs passed to the native client.

`Config.Format` also protects numeric, boolean, and unknown fmt verbs. However,
Go bypasses all methods for invalid `%p` on a Config value and invalid `%w` on a
value or pointer; those format errors can expose its raw fields. If a format
string comes from elsewhere, pass `record.String()` to fmt instead of `record`.
Direct field access, serialization, method-free type conversions, and arbitrary
native objects do not receive diagnostic redaction.

Owned Stop waits for native subscription and publisher draining. The shutdown
context and native drain timeout bound that wait; cancellation forces the
connection closed and returns the context error. Borrowed Stop leaves its
connection open. Nil Logger uses `slog.Default()`; supply a discard logger to
suppress handler failure records.

See the [package API](https://pkg.go.dev/arkoren.dev/driver/nats/natsbroadcast)
for construction, native access, and lifecycle examples.
