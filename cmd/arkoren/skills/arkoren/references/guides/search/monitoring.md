---
title: Search Monitoring
nav: Monitoring
description: Observe search operations with shared telemetry and bounded deployment events while preserving data and lifecycle boundaries.
order: 60
summary:
  - Attach shared telemetry and stable engine labels to search operations.
  - Observe returned deployment outcomes with bounded in-process event payloads.
  - Separate signal privacy from application logging, durability, and consistency guarantees.
---

# Search Monitoring

Search requests and index deployment need different signals. Telemetry measures
every engine operation; optional events report returned `Sync` and `Drop`
outcomes. Neither signal proves that the index contains every source record or
that a multi-step change committed atomically.

## Shared Telemetry

Use your application's [shared telemetry handle](../observability/telemetry.md#shared-handle)
when constructing the engine. With an existing `driver`, the
[`Products` definition](indexing-documents.md#document-shape), and `search`
imported from `arkoren.dev/core/service/search`:

```go
engine := search.New(driver, func(settings *search.Builder) {
	settings.Indexes(Products)
	settings.Telemetry(app.Telemetry())
	settings.Name("catalog")
})
```

`Telemetry(nil)`, including the omitted setting, resolves `telemetry.Default()`
per operation rather than freezing the default at construction. Disabled
telemetry emits no signals. `Name` is a stable engine label, not an index rename;
empty uses `search.DefaultName`, which is `default`.

## Operation Signals

`Sync`, `Check`, `Put`, `Delete`, `Drop`, and `Search` each emit one client span
named `search {operation}`, the `arkoren.search.operations` counter, and the
`arkoren.search.operation.duration` histogram in seconds. A logical batch is
one facade operation even if a provider sends several requests.

Attributes identify operation, engine, and index; `Sync` and `Check` operate
across registrations and use an empty index label. Search records the number
of returned hits on its span, not the entire capped match count. Returned errors
use classified `error.type` values instead of recording native error messages.
See [instrumented components](../observability/instrumented-components.md#search)
for exact attribute names and instrument details.

Validation failures still belong to engine operation telemetry. `Driver()` and
`DriverAs` emit nothing, and calls made through their returned shared driver
bypass facade instrumentation, validation, JSON conversion, and notifications.

## Deployment Events

For an in-process release observer, opt in to an event bus at construction.
Import `event` from `arkoren.dev/core/event`:

```go
events := event.New()
engine := search.New(driver, func(settings *search.Builder) {
	settings.Indexes(Products)
	settings.Events(events)
})
```

These are alternative engine constructions; combine settings in one callback
if you need both telemetry and events. Omitted or nil `Events` disables
notifications independently of telemetry.

Listen for administrative failures using the typed payload. Here `log` is the
standard-library package and `context` supplies the listener context:

```go
_, err := events.Listen(search.Failed, func(ctx context.Context, outcome search.MutationEvent) error {
	log.Print(outcome.Operation, outcome.ErrorClass, outcome.Duration)
	return nil
})
```

Check listener registration before running the release action. `search.Synced`
reports returned sync success; `search.Dropped` reports returned drop success,
including an absent index; `search.Failed` reports a returned driver error from
either operation. The payload contains only `Operation` (`sync` or `drop`),
driver-call `Duration`, and bounded `ErrorClass`. Success has an empty error
class. Context failures use `canceled` or `deadline_exceeded`; search sentinels
use their stable names; unknown errors use `error`.

These synchronous, best-effort notifications are not a durable audit log.
Listener errors and panics cannot replace the operation result. Driver panics
propagate without an outcome event. Invalid registrations never reach the
driver and emit no administrative event; `Check`, document operations, and
queries emit none either.

A successful sync can change nothing. A failed sync can follow partial changes;
an event is not rollback evidence or proof of index consistency. Duration excludes
listener execution. Keep handling the returned operation error and follow the
[deployment recovery path](deploying.md#release-deployment) before continuing
a rollout.

## Privacy Boundaries

Automatic telemetry excludes query text, document bodies, document IDs, and
native error messages. Event payloads additionally exclude index and engine
names. Telemetry does contain the configured engine and index labels, so keep
those names stable and nonsensitive rather than deriving them from request text
or private identifiers.

Application logging and native SDK instrumentation have their own policies.
Do not log entire queries, documents, or raw backend errors merely to add context
to these bounded signals. Apply your [telemetry privacy policy](../observability/telemetry-privacy.md)
to exports and access controls, and verify native instrumentation separately.

For every function and option, see the [search API reference](https://pkg.go.dev/arkoren.dev/core/service/search).
