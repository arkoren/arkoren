---
title: Instrumented Components
description: Look up built-in trace and metric names, labels, configuration, and observation boundaries.
order: 20
---

# Instrumented Components

> [!NOTE]
> Hatchet instrumentation references below are retained development context for
> a pending, unpublished integration, not available support. See the
> [workflow comparison](../workflows/drivers.md#compatibility-matrix) for available drivers.

## Introduction

You do not write spans for the parts of your application that Arkoren already
knows how to describe. The database, cache, storage, mail, queue, broadcast,
and schedule components each record a span and metrics for their operations
when telemetry is on, and the [HTTP middleware and client transport](request-tracing.md)
do the same for requests. Hashing and encryption expose aggregate metrics
without spans because their APIs have no request context. Because every span
starts from the context you pass in, the pieces join into one trace: a request
span contains the
database query it ran, the cache read before it, and the mail it sent
afterwards.

Use this catalog to find the span names, metrics, and attributes available for
your dashboards and alerts. To enable them first, follow
[Telemetry](telemetry.md#turning-telemetry-on). Names follow
the OpenTelemetry semantic conventions where one exists. Where it does not,
such as for cache, storage, mail, and scheduling, a span is named
`{component} {operation}` in lowercase (`cache get`, `schedule run`), with any
name or target in an attribute, and metrics and attributes live under the
`arkoren.` prefix.

These rules describe spans and metrics, not log redaction or application events.
Logs retain the content their producer supplies; see
[Telemetry Privacy](telemetry-privacy.md#logs). Events report discrete outcomes
to listeners, whereas telemetry measures and correlates operations; use
[auditing with events](../events/auditing.md) when individual actions need a
durable record.

All instrumented components share the following rules, which the sections below
do not repeat:

- A context-bearing operation's span is a **client** span unless noted, and its
  metric is recorded alongside it, with the same attributes.
- Failures record an `error.type` classification: depending on the component,
  this is a known sentinel, a fixed fallback such as `other` or `error`, or a Go
  type name. Traced operations also set the span status to error; metrics-only
  operations have no span status. Error text is never recorded or passed to
  `RecordError`, because it can contain keys, addresses, or query values.
- An ordinary missing row, cache key, or storage object leaves the span status
  unset. Other services classify their contract errors explicitly; for example,
  a missing secret is reported as `secret.ErrNotFound`.
- With telemetry off the cost is a single check; no span, attribute, or metric
  allocates.

## Giving a component the handle

Each component takes the telemetry handle through its options. Pass
`app.Telemetry()` from your [configured application](telemetry.md#turning-telemetry-on).
Import `database` from `arkoren.dev/core/service/database`, `cache` from
`arkoren.dev/core/service/cache`, `queue` from `arkoren.dev/core/service/queue`,
and `telemetry` from `arkoren.dev/core/service/telemetry`. Here `driver`,
`cacheDriver`, and later `queueDriver` are your configured service drivers:

```go
t := app.Telemetry()

db := database.New(driver, func(connection *database.Builder) {
	connection.Telemetry(t)
})
store := cache.New(cacheDriver, func(store *cache.Builder) {
	store.Telemetry(t)
})
```

Storage, mail, queue dispatchers, and schedulers accept the same
`Telemetry(t)` setting in their own builder callbacks.

If you leave the option out, the component follows `telemetry.Default()` and
looks it up each time it runs. That means you can also build your services
without the handle and call `app.SetDefaults()` once; see
[Telemetry](telemetry.md#the-process-default). Use the explicit option when several
telemetry handles exist in one process, for example in tests.

The scope of each component's tracer and meter is its package path, such as
`arkoren.dev/core/service/cache`. Backends show it as the instrumentation
library, and you can filter on it.

## Database

The database records one span per statement it runs through `Exec`, `Select`,
`Find`, or `Cursor`. A cursor's span covers the traversal, not each row.
`Lock` and `TryLock` each record a lock-acquisition span. The span name
follows the database conventions: the SQL verb and the table when Arkoren can tell
(`SELECT users`, `INSERT tags`), the verb alone for other statements, and the
database system (`postgresql`) when it cannot tell either.

| Attribute | Value |
| --- | --- |
| `db.system.name` | `postgresql`, `mysql`, or `sqlite`, from the driver's dialect; `other_sql` for others |
| `db.namespace` | The name you set with `database.Builder.Namespace`; omitted when empty |
| `db.operation.name` | `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `CREATE`, `ALTER`, `DROP`, `TRUNCATE`, `LOCK`, or `TRY_LOCK`; omitted for any other leading keyword |
| `db.collection.name` | The table, when the statement comes from a query builder over a table |
| `db.query.text` | Compiled SQL verbatim; span only, disabled by default, enabled only with explicit `database.Builder.QueryText(true)` |

SQL text is excluded by default. Enable `database.Builder.QueryText(true)` only
when the statements are safe to export: it records compiled SQL **verbatim**,
including literals and comments. Separately bound argument values are omitted,
but that does not redact values embedded in SQL. The setting provides no SQL
redaction guarantee; keep it disabled for statements that can contain secrets
or personal data.

The metric is the histogram `db.client.operation.duration`, in seconds, with
the keys `db.system.name`, `db.namespace`, `db.operation.name`,
`db.collection.name`, and `error.type`. The query text is never a metric label.

A query that matches no row is a success. Use `database.Builder.System` to report
a system name the dialect does not give, for example when a wrapper hides the
dialect. The [database guide](../database/query-monitoring.md) shows the
spans in context, including how to log slow queries.

## Cache

The cache records a span named `cache {operation}` for each facade operation:
`get`, `put`, `compare_and_put`, `delete`, `add`, `has`, `pull`, `increment`,
`decrement`, `increment_with_ttl`, `ttl`, `lock`, `try_lock`, and `remember`.
Lock spans cover acquisition, including the wait for `lock`;
extending and releasing a lease are not traced.

| Attribute | Value |
| --- | --- |
| `arkoren.cache.operation` | The operation name above |
| `arkoren.cache.store` | The name from `cache.Builder.Name`, or `default` |
| `arkoren.cache.hit` | `true` or `false` when lookup outcome is known; a found value with a decode error is still a hit. Omitted for writes and backend failures with unknown lookup outcome. |
| `error.type` | Set on failure |

Two metrics are recorded: the counter `arkoren.cache.operations` with the keys
`arkoren.cache.operation`, `arkoren.cache.store`, `arkoren.cache.hit`, and
`error.type`, and the histogram `arkoren.cache.operation.duration`, in seconds,
with the same keys except `arkoren.cache.hit`. Divide the `true` count by all
reads to get a hit ratio per store. Give each cache a short, fixed name, such as
`sessions`, so the ratios stay separate.

The key and the value are never recorded. A missing key counts as a miss
(`arkoren.cache.hit` is `false`), not as an error.
`compare_and_put` omits the span's hit attribute and uses an empty hit label on
the counter, as other non-read operations do. A guard mismatch is a successful
comparison result, not a lookup miss or an error. Neither the guard nor the
destination key or value is recorded.

## Feature flags

Each evaluation records a span named `feature evaluate` (internal), plus a
counter and a histogram.

| Attribute | Value |
| --- | --- |
| `feature_flag.key` | The flag key |
| `feature_flag.provider.name` | The name from `feature.Builder.Name`, or `default` |
| `feature_flag.result.reason` | The driver's reason, such as `static` or `targeting_match`; `error` on failure |
| `feature_flag.result.variant` | The variant, on the span only and only when the driver gave one |
| `error.type` | Set on failure |

The counter is `arkoren.feature.evaluations` with the keys `feature_flag.key`,
`feature_flag.provider.name`, `feature_flag.result.reason`, and `error.type`.
The histogram `arkoren.feature.evaluation.duration`, in seconds, has the same
keys except the reason. Subject keys, subject attributes, flag values, and
error text are never recorded.

## Search

The search engine records a client span named `search {operation}` for `sync`,
`check`, `put`, `delete`, `drop`, and `search`, plus a counter and a histogram.

| Attribute | Value |
| --- | --- |
| `arkoren.search.operation` | The operation name above |
| `arkoren.search.name` | The name from `search.Builder.Name`, or `default` |
| `arkoren.search.index` | The index name; empty for `sync` and `check`, which cover every index |
| `arkoren.search.hits` | The hits on the returned page, on a successful `search`; span only |
| `error.type` | Set on failure |

The counter is `arkoren.search.operations` and the histogram
`arkoren.search.operation.duration`, in seconds, both with the operation,
name, index, and `error.type` as keys. Query text, documents, and error text
are never recorded.

## Storage

The storage facade records a span named `storage {operation}` for `open`,
`put`, `stat`, `delete`, `list`, and `sign`.

| Attribute | Value |
| --- | --- |
| `arkoren.storage.operation` | The operation name above |
| `arkoren.storage.store` | The name from `storage.Builder.Name`, or `default` |
| `arkoren.storage.object.size` | Bytes written, on a successful `put`; span only |
| `error.type` | Set on failure |

The counter `arkoren.storage.operations` and the histogram
`arkoren.storage.operation.duration`, in seconds, use the keys
`arkoren.storage.operation`, `arkoren.storage.store`, and `error.type`. Duration
buckets run from a millisecond to a minute, since a local stat and a large
upload differ by orders of magnitude. A missing object is an answer, not a
failure.

Object keys, prefixes, list cursors, and signed URLs are never recorded; a
signed URL is a credential.

## Mail

Each `Mailer.Send` with a nonnil context records one span named `mail send`, with
`arkoren.mail.operation` set to `send`, and two span-only counts:
`arkoren.mail.recipients` (to, cc, and bcc together) and
`arkoren.mail.attachments`. The counter `arkoren.mail.operations` and the
histogram `arkoren.mail.operation.duration` have the keys
`arkoren.mail.operation` and `error.type`; the buckets run from ten
milliseconds to a minute, since sending talks to a relay or a provider API.

Addresses, subjects, bodies, provider diagnostics, and the receipt are never
recorded. Returned errors use `context.Canceled`, `context.DeadlineExceeded`, or
`mail.ErrInvalidMessage`, in that precedence order; other errors use the Go type
of the innermost wrapped error. Validation failures are recorded before driver
submission. The separate failure event's bounded `ErrorClass` uses `error` as its
fallback, not a native type name; see
[mail failure observations](../mail/events-and-telemetry.md#events).

These metrics count attempts, not confirmed acceptance. A driver panic still
ends the mailer's owned span and records the attempt's count and duration,
without ending the caller's parent span, recording panic data, or adding an
error classification for the panic itself. It propagates without a `Sent` or
`Failed` event. A nil context produces neither telemetry nor events.

## Push notifications

After portable validation, the push sender records one client span named `push send`. Its counter is
`arkoren.push.operations`; its duration histogram is
`arkoren.push.operation.duration`, in seconds. Both use the bounded
`arkoren.push.operation` and `error.type` keys. Pass the shared telemetry handle
through `push.Builder.Telemetry`, or leave it unset to follow
`telemetry.Default()` on each call.

If a returned error matches `push.ErrOutcomeUnknown`, the span and both metrics
use `error.type == "push.ErrOutcomeUnknown"`, ahead of wrapped or joined
`context.Canceled`, `context.DeadlineExceeded`, or other provider failures.
Without that uncertainty sentinel, cancellation takes precedence over deadlines,
then known push sentinel classes; an unclassified error falls back to the Go
type of its innermost wrapped error, not native diagnostic text.
This normalization affects only telemetry: `Send` preserves the returned error's
causes, and failure notifications retain
their bounded `ErrorClass`, including `outcome_unknown` for uncertain acceptance.

Recipients, subscription endpoints, token and encryption values, titles, bodies,
application data, and receipt IDs are never recorded. Acceptance telemetry does
not prove that a device received or displayed a notification. See
[Push Notifications](../push/overview.md) for submission and retention behavior.

## Payments

The payment facade records a client span named `payment {operation}` after
portable input validation. The counter `arkoren.payment.operations` and
histogram `arkoren.payment.operation.duration`, in seconds, use
`arkoren.payment.operation` and `error.type` as keys. Neither successful API
acceptance nor its telemetry proves that funds settled.

Keys, IDs, amounts, approval URLs, customer information, and request/response
payloads are not recorded. Pass your handle through `payment.Builder.Telemetry`
from `arkoren.dev/core/service/payment`; the
[payment observations](../events/service-operations.md#payments) describe the boundary.

## Secrets

The secret facade records a client span named `secret {operation}` for `get`,
`put`, `update`, and `delete`. The counter `arkoren.secret.operations` and
histogram `arkoren.secret.operation.duration`, in seconds, use
`arkoren.secret.operation` and `error.type` as keys. Failures use only
`secret.ErrNotFound`, `context.Canceled`, `context.DeadlineExceeded`, or `other`.

`Raw` observes the provider read once; `String` and typed `Get` reuse that
observation. A typed JSON decoding error occurs after retrieval and is not
reported as a provider failure. Facade writes are observed, but direct calls
through `Driver()` or a native client bypass these observations.

Pass the handle through `secret.Builder.Telemetry`, or leave it unset to follow
the dynamic process default. Names, versions, values, sizes, and native errors
are omitted. See [Managing Secrets](../secrets/managing.md).

## Hashing and encryption

Hash and crypto record metrics only: their contextless methods create no spans.
Use `hash.Builder.Telemetry` or `crypto.Builder.Telemetry`, or leave the setting
unset to follow `telemetry.Default()` on each call. The scope remains the
package path, and disabled telemetry avoids timing and metric allocation.

| Component | Counter | Duration histogram (seconds) | Label keys |
| --- | --- | --- | --- |
| Hash | `arkoren.hash.operations` | `arkoren.hash.operation.duration` | `arkoren.hash.operation`, `arkoren.hash.outcome`, `error.type` |
| Crypto | `arkoren.crypto.operations` | `arkoren.crypto.operation.duration` | `arkoren.crypto.operation`, `arkoren.crypto.outcome`, `error.type` |

Hash operation names are `hash`, `check`, and `needs_rehash`. Outcomes distinguish
`ok`, `match`, `mismatch`, `current`, `rehash_needed`, and `error`; a mismatch is
not an error. Crypto operations are `encrypt` and `decrypt`, with `ok`,
`previous_key`, or `error` outcomes. Each metric covers the complete facade call,
including JSON work; decryption records one final outcome across all keys,
not a failure for each key tried.

The labels contain no inputs, outputs, key material, or native error text/types.
Rehash-needed and previous-key outcomes describe observations, not persisted
upgrades. See [Hashing](../hashing/overview.md) and
[Encryption](../encryption/overview.md) for caller-owned persistence.

## Queues and broadcast

The queue and broadcast components share one instrumentation, shaped by the
OpenTelemetry messaging conventions, and they connect the producer and the
consumer into one trace even though the two run in different processes.

When a job is dispatched or a broadcast is published, the component starts a
**producer** span named `send {destination}` and injects the trace context
into the message's metadata. The destination is the queue name for queues and
the broadcast name for broadcasts. When a consumer receives the message, it
extracts that context and starts a **consumer** span named
`process {destination}` as a child of the producer, so a trace that began in an
HTTP request continues through the worker that handled its job. For queues, a
retry is a new consumer span with a higher attempt.

| Attribute | Value |
| --- | --- |
| `messaging.system` | The system the driver reports, such as `rabbitmq`, `aws_sqs`, `nats`, `redis`, or `memory`; `_OTHER` when the driver names none |
| `messaging.operation.name` and `messaging.operation.type` | `send` or `process` (span only for the type) |
| `messaging.destination.name` | The queue or broadcast name |
| `messaging.message.id` | The message ID, when there is one; span only |
| `arkoren.queue.job` | The job name; queues, span only |
| `arkoren.queue.attempt` | The delivery attempt, from 1; queues, span only |
| `error.type` | Set on failure |

Four metrics are recorded, each with the keys `messaging.system`,
`messaging.operation.name`, `messaging.destination.name`, and `error.type`: the
counters `messaging.client.sent.messages` and `messaging.client.consumed.messages`,
and the histograms `messaging.client.operation.duration` (the producer side) and
`messaging.process.duration` (the consumer side), in seconds.

If your driver reports the wrong system name, or none, set it with
`DispatcherBuilder.System` and `ConsumerBuilder.System`, or `broadcast.ConsumerBuilder.System`. Both the dispatching and the
consuming side take the telemetry option, so give it to every dispatcher,
publisher, and consumer you build:

```go
dispatcher := queue.NewDispatcher(queueDriver, func(dispatcher *queue.DispatcherBuilder) {
	dispatcher.Telemetry(t)
})
consumer := queue.NewConsumer(queueDriver, func(consumer *queue.ConsumerBuilder) {
	consumer.Telemetry(t)
})
```

Payloads and metadata values are never recorded. The metadata does hold the
propagated trace context, which is how the trace crosses the broker; see
[Privacy](telemetry-privacy.md) for what that means for your broker's access.

## Scheduling

Every run of a scheduled entry or a deferred task records an **internal** span
named `schedule run`, with the name you gave the entry in `arkoren.schedule.entry`. The
run's context carries the span, so the database queries, cache reads, and mail
that the task performs become its children.

| Attribute | Value |
| --- | --- |
| `arkoren.schedule.entry` | The entry or task name |
| `arkoren.schedule.kind` | `entry` for a recurring entry, `task` for one deferred with `At` or `After` |
| `arkoren.schedule.outcome` | `ok`, `skipped` when the run returned `schedule.ErrSkipped`, or `failed` |
| `error.type` | Set when the run failed; `schedule.panic` when it panicked |

The counter `arkoren.schedule.run.count` and the histogram
`arkoren.schedule.run.duration`, in seconds, carry the entry, kind, outcome, and
`error.type` as keys. The buckets run from a millisecond to an hour. A skipped
run is not an error: it has outcome `skipped` and no error status. Task payloads
and error text are never recorded.

## HTTP

The server middleware and the client transport are documented on the
[HTTP page](request-tracing.md). In short, the middleware records one server span per
request, named `<METHOD> <route>`, and the histogram
`http.server.request.duration` plus the up-down counter
`http.server.active_requests`. The client transport records one client span per
outgoing request, named after the method, and `http.client.request.duration`.
`client.Transport` from the [HTTP client](../http-networking/http-client.md) wraps the
telemetry transport, so each retry attempt is its own span, marked with
`http.request.resend_count`.

## MCP

Managed [MCP calls](../mcp/tools.md#observe-calls-without-payload-capture)
emit internal `mcp call_tool` spans, `arkoren.mcp.calls`, and
`arkoren.mcp.call.duration`, in seconds, in the `arkoren.dev/driver/mcp` scope.
The only labels are `arkoren.mcp.tool` (a managed registered name) and
`arkoren.mcp.outcome` (`success` or `error`). Arguments, results, raw errors,
unknown tool names, and model-inference attributes are omitted. Native SDK
registrations bypass this instrumentation. Configure the endpoint with
`mcpserver.Builder.Telemetry(t)` from `arkoren.dev/driver/mcp/mcpserver`.

## Language models

`Model.Generate` and `Model.Stream` record a `chat {model}` span, and
`Embedder.Embed` an `embeddings {model}` span, with the model, provider, finish
reasons, and token counts as attributes. `Model.Run` records an
internal `invoke_agent {agent}` span that contains a client `chat` span per turn
and an internal `execute_tool {tool}` span per tool call. Two histograms,
`gen_ai.client.operation.duration` and `gen_ai.client.token.usage`, record
latency and tokens. Names follow the OpenTelemetry generative AI conventions.
Prompts, replies, tool arguments, and tool results are never recorded. See
[Language Models Overview](../llm/overview.md#usage-events-and-telemetry).

## The application and workflows

The application records no spans of its own. It owns the telemetry lifecycle,
wraps the logger so records carry `trace_id` and `span_id`, and passes the
service name and version to the driver; see [Telemetry](telemetry.md#turning-telemetry-on).
Successful startup and shutdown are visible as the `app started`, `app stopping`,
and `app stopped` log lines. There is no built-in `app starting` log. The
`application.Starting` event runs after telemetry starts and before user startup
callbacks; a listener can log through `app.Logger()` with telemetry active.

Workflow clients record `workflow start`, `workflow signal`, `workflow cancel`,
`workflow status`, and `workflow result` client spans. Workers record
`workflow step_attempt` spans around actual registered step-handler invocations.
Retries are separate attempts; replayed orchestration and recorded step results
produce no attempt observations. `result` measures the caller's wait, not run
execution time; canceling that wait does not cancel the workflow.

The counter `arkoren.workflow.operation.count` and histogram
`arkoren.workflow.operation.duration`, in seconds, use
`arkoren.workflow.operation`, `arkoren.workflow.outcome`, and `error.type` as
keys. Outcomes are `success`, `error`, or `already_started`; duplicate starts
retain their existing handle/error semantics but are not telemetry failures.
Labels omit workflow/step names, run IDs, inputs, outputs, signals, and metadata.

Configure `ClientBuilder.Telemetry` in client processes and
`RegistryBuilder.Telemetry` through `workflow.NewRegistry` in workers. Each
defaults to its own process's current telemetry service. Export from every pod
to a common backend; a local event bus or memory capture cannot aggregate pods.
The [workflow wiring example](../workflows/overview.md#events-and-telemetry)
shows both settings.

`StartOptions.Trace` carries serialized propagation separately from application
metadata. Memory, Temporal, Restate, and Hatchet forward it to actual attempts;
supported child/spawn operations forward the start parent too. Signals do not
replace that parent. This is explicit transport data, not a transported Go
context, and native cancellation/deadlines remain attached to each attempt.

> [!WARNING]
> Upgrade Temporal workers before enabling traced producers. Older workers
> cannot decode the traced-input envelope. Existing untraced histories retain
> their argument shape; see [Workflow Drivers](../workflows/drivers.md).

Do not add ordinary telemetry side effects to workflow functions, which can
replay. Local action events and polling do not provide an authoritative terminal
feed; use engine history and application-owned durable reconciliation for that
requirement.

## Instrumenting your own component

If your component can be used by several applications or tests, each telemetry
handle needs its own instruments. Rebuilding them on every call wastes work,
and keeping one global set sends data to the wrong handle.
`telemetry.NewInstruments` builds them lazily and reuses the most recent handle's
instruments. Switching to a different handle rebuilds them:

```go
type instruments struct {
	tracer  telemetry.Tracer
	renders telemetry.Counter
}

var renderInstruments = telemetry.NewInstruments(func(handle *telemetry.Telemetry) *instruments {
	return &instruments{
		tracer: handle.Tracer("example.com/reports"),
		renders: handle.Meter("example.com/reports").Counter("reports.renders",
			telemetry.Unit("{render}"), telemetry.Keys("format", "error.type")),
	}
})
```

Resolve those instruments for the current handle before recording a render.
Import standard packages `cmp`, `context`, and `fmt`. Here `doRender` is your
existing `func(context.Context, string) error`, not an Arkoren helper:

```go
func render(ctx context.Context, t *telemetry.Telemetry, format string) error {
	t = cmp.Or(t, telemetry.Default())
	if !t.Enabled() {
		return doRender(ctx, format)
	}

	in := renderInstruments.For(t)
	ctx, span := in.tracer.Start(ctx, "report render", telemetry.Attrs(telemetry.String("format", format)))
	defer span.End()

	err := doRender(ctx, format)
	kind := ""
	if err != nil {
		kind = fmt.Sprintf("%T", err)
		span.SetStatus(telemetry.Error, "render failed")
		span.SetAttributes(telemetry.String("error.type", kind))
	}
	in.renders.Add(ctx, 1, telemetry.String("format", format), telemetry.String("error.type", kind))

	return err
}
```

`Enabled` is false when telemetry is a no-op, not yet started, or shut down.
Checking it first means disabled telemetry costs one comparison, not the
allocation of attributes. Report failures by class, as the built-in components
do, and keep error text out of spans unless you have checked what it can
contain.

For every option and function, see [the `telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry),
[the `database`](https://pkg.go.dev/arkoren.dev/core/service/database), [`cache`](https://pkg.go.dev/arkoren.dev/core/service/cache),
[`storage`](https://pkg.go.dev/arkoren.dev/core/service/storage), [`mail`](https://pkg.go.dev/arkoren.dev/core/service/mail), [`push`](https://pkg.go.dev/arkoren.dev/core/service/push),
[`queue`](https://pkg.go.dev/arkoren.dev/core/service/queue), [`broadcast`](https://pkg.go.dev/arkoren.dev/core/service/broadcast),
and [`schedule`](https://pkg.go.dev/arkoren.dev/core/service/schedule) API references.
