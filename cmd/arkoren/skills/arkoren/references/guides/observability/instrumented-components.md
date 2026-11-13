---
title: Instrumented Components
description: Look up built-in spans, metrics, labels, and the operation boundaries they measure.
order: 30
summary:
  - Find exact built-in span names, metric names, label keys, and telemetry settings.
  - Distinguish provider calls, stream lifetimes, retries, workflow attempts, and business outcomes.
  - Identify omitted content and recorded names that still require privacy review.
---

# Instrumented Components

Use this catalog when you need the exact instrument behind a slow operation or
an alert. It describes observations made through Arkoren facades, not every
native SDK call. Configure a [telemetry handle](telemetry.md#shared-handle)
first; following a trace across processes also requires propagation and telemetry
in each process.

## Shared Rules

Core service scopes are their package paths, such as
`arkoren.dev/core/service/cache`. Unless an entry says otherwise, spans are
`Client` spans and duration histograms use seconds. Each listed error label is
`error.type`; classifications vary by component between known sentinels, fixed
fallbacks, and Go type names. Automatic spans and metrics omit raw error text
and do not call `RecordError`.

Set the component's `Builder.Telemetry(handle)` to select an explicit handle.
Nil or omission follows the process default at operation time; messaging,
workflow, and LLM use their role-specific builders below. Native driver calls
bypass facade instrumentation. An inactive handle skips observation work;
an enabled handle does not imply every signal has a backend.

The exclusions here are not a redaction filter for logs, custom attributes, or
diagnostic tools. Review [Telemetry Privacy](telemetry-privacy.md) separately.
Counts describe observed calls or attempts, not an exactly-once business feed;
[events and auditing](../events/auditing.md) have different responsibilities.

## Database

`database.Builder.Telemetry` selects the handle. Statements through `Exec`,
`Select`, `Find`, and `Cursor` record spans named by SQL verb and known table,
such as `SELECT reports`; without a known table they use the verb, and without
a known verb they use the database system. Cursor timing covers traversal,
not a separate span per row. `Lock` and `TryLock` observe acquisition as `LOCK`
and `TRY_LOCK`.

| Field | Meaning |
| --- | --- |
| `db.system.name` | Dialect-derived `postgresql`, `mysql`, `sqlite`, or `other_sql`; overridden by `Builder.System` |
| `db.namespace` | `Builder.Namespace`, omitted when empty |
| `db.operation.name` | Recognized leading SQL verb, or `LOCK` / `TRY_LOCK` |
| `db.collection.name` | Table known from a table-based query builder |
| `db.query.text` | Span only; compiled SQL when explicitly enabled |

`db.client.operation.duration` uses `db.system.name`, `db.namespace`,
`db.operation.name`, `db.collection.name`, and `error.type`. Recognized leading
verbs are `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `CREATE`, `ALTER`, `DROP`, and
`TRUNCATE`; this classification does not parse comments or CTEs. Missing rows
are not telemetry failures.

`QueryText` defaults to false. Enabling it exports compiled SQL verbatim,
including literals and comments; bound arguments stay separate but embedded
values are not redacted. Query text never becomes a metric label. See
[database monitoring](../database/query-monitoring.md) before opting in.

## Cache

`cache.Builder.Telemetry` selects spans named `cache {operation}` and these
metrics:

| Instrument | Label Keys |
| --- | --- |
| `arkoren.cache.operations` (counter) | `arkoren.cache.operation`, `arkoren.cache.store`, `arkoren.cache.hit`, `error.type` |
| `arkoren.cache.operation.duration` | `arkoren.cache.operation`, `arkoren.cache.store`, `error.type` |

Operations are `get`, `put`, `compare_and_put`, `delete`, `add`, `has`, `pull`,
`increment`, `decrement`, `increment_with_ttl`, `ttl`, `lock`, `try_lock`, and
`remember`. Store is `Builder.Name`, or `default`. Lock spans cover acquisition,
including the blocking wait, not lease extension or release.

Known reads add a boolean `arkoren.cache.hit` span attribute and a `true` or
`false` counter label. A missing key is a miss, not an error; finding bytes
that fail decoding is still a hit. Unknown lookup outcomes and non-read
operations have no span hit attribute and an empty counter hit label.
`compare_and_put` guard mismatch is a successful comparison, not a miss or
failure. Keys, guards, and values are omitted. For a correctly filtered hit
ratio and nested `remember` observations, see [cache monitoring](../cache/monitoring.md).

## Feature Flags

`feature.Builder.Telemetry` observes each evaluation with an internal
`feature evaluate` span.

| Instrument | Label Keys |
| --- | --- |
| `arkoren.feature.evaluations` (counter) | `feature_flag.key`, `feature_flag.provider.name`, `feature_flag.result.reason`, `error.type` |
| `arkoren.feature.evaluation.duration` | `feature_flag.key`, `feature_flag.provider.name`, `error.type` |

The provider name is `Builder.Name` or `default`. Reason comes from the driver,
such as `static` or `targeting_match`, and becomes `error` on failure. A supplied
`feature_flag.result.variant` is span-only. Subject keys, subject attributes,
and flag values are omitted; flag keys and variants still require your privacy
and cardinality review. See [feature evaluation](../feature-flags/overview.md).

## Search

`search.Builder.Telemetry` observes `sync`, `check`, `put`, `delete`, `drop`, and
`search` as `search {operation}` spans. `arkoren.search.operations` is the
counter; `arkoren.search.operation.duration` is the histogram. Both use
`arkoren.search.operation`, `arkoren.search.name`, `arkoren.search.index`, and
`error.type`.

Name is `Builder.Name` or `default`; index is empty for engine-wide `sync`
and `check`. Successful search spans add `arkoren.search.hits` for the returned
page only. Queries, documents, and raw diagnostics are omitted. Index names can
still reveal tenants; keep them bounded. See [Search](../search/overview.md).

## Storage

`storage.Builder.Telemetry` records `storage {operation}` spans for `open`,
`put`, `stat`, `delete`, `list`, and `sign`. The counter
`arkoren.storage.operations` and histogram `arkoren.storage.operation.duration`
use `arkoren.storage.operation`, `arkoren.storage.store`, and `error.type`.
Store is `Builder.Name` or `default`; successful writes add the span-only
`arkoren.storage.object.size` in bytes. Histogram bounds span 1 ms to 1 minute.

`open` ends when the driver returns the body, not after the caller reads or
closes it. `put` measures reader submission; upload helpers reuse that
observation. `list` measures one page, and `sign` measures URL creation rather
than the later request using it. Missing objects are not failures. Object keys,
prefixes, cursors, and signed bearer URLs are omitted. See
[storage observations](../file-storage/events-and-telemetry.md).

## Mail

`mail.Builder.Telemetry` records `mail send` for `Mailer.Send` with a nonnil
context. `arkoren.mail.operation` is `send`; span-only counts are
`arkoren.mail.recipients` (to, cc, and bcc combined) and
`arkoren.mail.attachments`. The `arkoren.mail.operations` counter and
`arkoren.mail.operation.duration` histogram use operation and `error.type`.
Histogram bounds span 10 ms to 1 minute.

Validation failures are observed before submission. Error precedence is
`context.Canceled`, `context.DeadlineExceeded`, `mail.ErrInvalidMessage`, then
the innermost wrapped Go error type. Addresses, subjects, bodies, provider
diagnostics, and receipts are omitted.

These metrics count attempts, not confirmed delivery. A driver panic closes
the mailer's span and records attempt count/duration without ending the parent
span, retaining panic data, or classifying the panic itself; the panic still
propagates and emits no `Sent` or `Failed` notification. Nil context produces
neither telemetry nor events. Notification failure classes are bounded and
separate from telemetry's native-type fallback; see
[mail observations](../mail/events-and-telemetry.md).

## Push Notifications

`push.Builder.Telemetry` observes calls after portable preflight validation as
`push send`. The counter `arkoren.push.operations` and histogram
`arkoren.push.operation.duration` use `arkoren.push.operation` and `error.type`.
Operation is `send`.

`push.ErrOutcomeUnknown` wins over joined or wrapped cancellation, deadline, and
provider failures in telemetry. Otherwise cancellation precedes deadline, known
push sentinels, and the innermost wrapped Go type. This changes observation
classification only: returned errors preserve their causes and notifications
retain their separate bounded classes.

No recipient, endpoint, token, encryption material, content, application data,
or receipt ID enters these fields. Acceptance does not prove device delivery or
display. See [Push Notifications](../push/overview.md).

## Payments

`payment.Builder.Telemetry` observes provider operations after portable
validation as `payment {operation}`. The counter `arkoren.payment.operations`
and histogram `arkoren.payment.operation.duration` use
`arkoren.payment.operation` and `error.type`.

Keys, IDs, amounts, approval URLs, customer details, and payloads are omitted.
API acceptance is not settlement evidence. For the distinction between these
observations and business reconciliation, see [Payments](../payments/overview.md).

## Secrets

`secret.Builder.Telemetry` observes provider `get`, `put`, `update`, and
`delete` as `secret {operation}`. The `arkoren.secret.operations` counter and
`arkoren.secret.operation.duration` histogram use `arkoren.secret.operation`
and `error.type`. Failure classes are only `context.Canceled`,
`context.DeadlineExceeded`, `secret.ErrNotFound`, or `other`, in that order.
Unlike a cache miss, a missing secret is a failure observation.

`Raw` observes one provider read; `String` and typed `Get` reuse it. A later
JSON decode failure is not a provider failure. Direct `Driver()` or native
calls bypass these observations. Names, versions, values, sizes, and native
diagnostics are omitted. See [Managing Secrets](../secrets/managing.md).

## Hashing And Encryption

`hash.Builder.Telemetry` and `crypto.Builder.Telemetry` record metrics only:
their contextless APIs create no synthetic root spans.

| Component | Counter | Duration Histogram | Label Keys |
| --- | --- | --- | --- |
| Hash | `arkoren.hash.operations` | `arkoren.hash.operation.duration` | `arkoren.hash.operation`, `arkoren.hash.outcome`, `error.type` |
| Crypto | `arkoren.crypto.operations` | `arkoren.crypto.operation.duration` | `arkoren.crypto.operation`, `arkoren.crypto.outcome`, `error.type` |

Hash operations are `hash`, `check`, and `needs_rehash`; outcomes are `ok`,
`match`, `mismatch`, `current`, `rehash_needed`, or `error`. Mismatch is not an
error. Failure classes are `hash.ErrInvalidHash` or `error`. Crypto operations
are `encrypt` and `decrypt`; outcomes are `ok`, `previous_key`, or `error`.
Its classes are `crypto.ErrEncrypterClosed`, `crypto.ErrInvalidCiphertext`, or
`error`.

Metrics cover normally completed facade calls, including JSON work; panics do
not create completed observations. Decryption reports one final outcome across
all keys, not a failure per attempted key. No input, output, key, or native
error text/type is retained. Rehash-needed and previous-key outcomes do not
prove a persisted upgrade; applications own persistence as described in
[Hashing](../hashing/overview.md) and [Encryption](../encryption/overview.md).

## Queues And Broadcast

Set telemetry on every participating `queue.DispatcherBuilder`,
`queue.ConsumerBuilder`, `broadcast.PublisherBuilder`, and
`broadcast.ConsumerBuilder`. Producers record `send {destination}`;
consumers record `process {destination}`. With propagation enabled on both
sides and preserved by the transport, consumer work can continue the producer
trace. Queue retries are separate consumer attempts.

| Field | Meaning |
| --- | --- |
| `messaging.system` | Driver-reported system, or `_OTHER`; `System` overrides it |
| `messaging.operation.name` | `send` or `process` |
| `messaging.destination.name` | Queue or broadcast name |
| `messaging.operation.type` | Span-only `send` or `process` |
| `messaging.message.id` | Span-only message ID when available |
| `arkoren.queue.job` | Span-only queue job name |
| `arkoren.queue.attempt` | Span-only queue delivery attempt, starting at 1 |

Counters are `messaging.client.sent.messages` and
`messaging.client.consumed.messages`; histograms are
`messaging.client.operation.duration` for producers and
`messaging.process.duration` for consumers. All four use `messaging.system`,
`messaging.operation.name`, `messaging.destination.name`, and `error.type`.
Send counts include attempted sends, not only confirmed acceptance.

Payloads and metadata values are not span attributes or labels, but propagation
does write trace context into metadata readable by broker operators. HTTP's
incoming trust policy does not authenticate broker messages. See
[queue telemetry](../queues/metadata-and-telemetry.md) and
[broadcast metadata](../broadcasting/metadata-and-telemetry.md).

## Scheduling

`schedule.Builder.Telemetry` records an internal `schedule run` span for a
recurring entry or deferred task. Its returned execution context lets the
task's dependencies become child spans.

The counter `arkoren.schedule.run.count` and histogram
`arkoren.schedule.run.duration` use `arkoren.schedule.entry`,
`arkoren.schedule.kind`, `arkoren.schedule.outcome`, and `error.type`.
Histogram bounds span 1 ms to 1 hour. Kind is `entry` or `task`; outcome is
`ok`, `skipped`, or `failed`. `schedule.ErrSkipped` is not a failed span;
recovered panics use `schedule.panic` as their error class. Task payloads and
error text are omitted, but developer-selected names are recorded. See
[scheduled failures](../scheduled-tasks/error-handling.md).

## HTTP

Use the handle's `Middleware()` and `Transport()` rather than service-builder
options. Their scope is `arkoren.dev/core/service/telemetry`.

| Boundary | Span | Instruments |
| --- | --- | --- |
| Server | `Server`, named `<METHOD> <route>` | `http.server.request.duration`, `http.server.active_requests` |
| Client | `Client`, named `<METHOD>` | `http.client.request.duration` |

Unknown methods use `HTTP` in span names and `_OTHER` in method attributes;
unmatched server requests omit the route from the name. Server duration uses
method, route, status, scheme, and `error.type`; active requests use method and
scheme. Client duration uses method, destination address/port, status, and
`error.type`. Full keys and outcome rules are in
[Request Tracing](request-tracing.md#recorded-fields).

Server completion includes router error rendering, not proof of delivery to
the client. Client completion includes consuming or closing the response body.
The [retrying HTTP client](../http-networking/http-client.md) already installs
telemetry per attempt and marks resends with `http.request.resend_count`;
wrapping it again duplicates observations.

## MCP

`mcpserver.Builder.Telemetry` selects managed-call observations in scope
`arkoren.dev/driver/mcp`. An internal `mcp call_tool` span, the counter
`arkoren.mcp.calls`, and histogram `arkoren.mcp.call.duration` use only
`arkoren.mcp.tool` (registered name) and `arkoren.mcp.outcome` (`success` or
`error`).

Arguments, results, raw errors, unknown tool names, and model-inference fields
are omitted. Native SDK registrations bypass this instrumentation. This does
not make the tool result itself private; diagnostic tools can return raw logs
and attributes. See [managed MCP observation](../mcp/tools.md#call-telemetry)
and [protected diagnostic registration](../diagnostics/capture-setup.md#protect-registration).

## Language Models

`llm.ModelBuilder.Telemetry` and `llm.EmbedderBuilder.Telemetry` select these
observations in `arkoren.dev/core/service/llm`:

| Operation | Span |
| --- | --- |
| `Model.Generate`, `Model.Stream` | Client `chat {model}` |
| `Embedder.Embed` | Client `embeddings {model}` |
| `Model.Run`, `Model.RunStream` | Internal `invoke_agent {agent}`, containing model turns and internal `execute_tool {tool}` spans |

`gen_ai.client.operation.duration` uses `gen_ai.operation.name`,
`gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.response.model`, and
`error.type`. `gen_ai.client.token.usage` adds `gen_ai.token.type` (`input` or
`output`) when usage is reported. Other span-only fields include response ID,
finish reasons, agent/tool names, token details, agent step count, and embedding
input count. Model, agent, and tool names must remain bounded and nonsensitive.

Prompts, replies, tool arguments/results, and error text are excluded. Stream
traversal and cooperative iterator cleanup affect completion; usage is
provider-reported rather than an invoice. See [Observing Calls](../llm/overview.md#observing-calls) for
the consumption and notification boundaries.

## Application Lifecycle

The application creates no lifecycle spans. It supplies the resource, owns
telemetry startup/shutdown, and wraps its logger for correlation. Built-in log
messages include `app started`, `app stopping`, and `app stopped`; there is no
`app starting` log. `application.Starting` runs after telemetry starts and before
user startup callbacks, so listeners can log through active telemetry. See
[application lifecycle](../application/lifecycle-and-ownership.md).

## Workflow Attempts

Configure `workflow.ClientBuilder.Telemetry` in producers and
`workflow.RegistryBuilder.Telemetry` through `workflow.NewRegistry` in workers.
Each process follows its own dynamic default when omitted. Client spans are
`workflow start`, `workflow signal`, `workflow cancel`, `workflow status`, and
`workflow result`; actual registered handler invocations get internal
`workflow step_attempt` spans.

The counter `arkoren.workflow.operation.count` and histogram
`arkoren.workflow.operation.duration` use `arkoren.workflow.operation`,
`arkoren.workflow.outcome`, and `error.type`. Outcomes are `success`, `error`,
or `already_started`; duplicate-start handling preserves the API result while
not classifying it as a telemetry failure. Names, run IDs, payloads, signals,
and metadata are excluded from these automatic fields.

Retries create separate attempts. Replayed orchestration and recorded results
create none; do not add ordinary observation side effects to replayed workflow
functions. `result` measures the caller's wait, not workflow runtime, and
canceling that wait does not cancel the run. Local events and polling are not
an authoritative terminal feed. Export from each process to a shared backend
for cross-replica inspection; local capture cannot aggregate pods.

Serialized `StartOptions.Trace` is separate from application metadata and is
extracted at supported native attempt boundaries without transporting the
caller's Go context. Signals do not replace the start parent. Upgrade Temporal
workers before traced producers: older workers cannot decode traced envelopes;
untraced histories retain their shape. See the available
[workflow drivers](../workflows/drivers.md) and
[Events And Telemetry](../workflows/running-workflows.md#events-and-telemetry).

For custom observations, follow [Recording Spans](telemetry.md#recording-spans)
and [Recording Metrics](telemetry.md#recording-metrics). Full declarations live
in the [`telemetry` API](https://pkg.go.dev/arkoren.dev/core/service/telemetry), with
adapter-specific MCP settings in the
[`mcpserver` API](https://pkg.go.dev/arkoren.dev/ark/mcp/mcpserver).
