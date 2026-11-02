---
title: Diagnostic Tools
description: Look up diagnostic names, typed inputs, limits, failures, and bounded cursor behavior.
order: 30
summary:
  - Match owner prefixes to protocol names, CLI groups, and typed tool inputs.
  - Interpret condition failures, trace lookup errors, and scheduler execution outcomes.
  - Track bounded cursor pages without confusing lost history or store resets with complete results.
  - Recognize capture limits and the disclosure risks of retained diagnostic evidence.
---

# Diagnostic Tools

Use this reference to interpret the diagnostic tools your application publishes.
The endpoint's current `tools/list` and input schemas determine what the caller
can use; this inventory does not guarantee that every tool is registered or
available. [Capture Setup](capture-setup.md) owns publication and protection;
[Application Diagnosis](application-diagnosis.md) owns the investigation path.

## Owner Prefixes

Factories return definitions over an existing owner's state. They do not mount
HTTP routes, authenticate callers, or start routers, schedulers, or standalone
telemetry stores.

| Factory | Empty Prefix | Explicit Prefix |
| --- | --- | --- |
| `composer.MCPTools` | Rejected before capture; requires a nonempty prefix. | `"app"` produces `app.info`, `app.health`, `app.started`, `app.healthy`, `app.ready`, `app.events`, `app.traces`, `app.trace`, `app.logs`, `app.metrics`; CLI group `app`. |
| `public.MCPTools` on a router | Protocol `routes`; CLI group `router`. | `"public"` produces `public.routes`; CLI group `public`. |
| `scheduler.MCPTools` | Protocol `entries`, `run`; CLI group `schedule`. | `"jobs"` produces `jobs.entries`, `jobs.run`; CLI group `jobs`. |
| `store.MCPTools` on memory telemetry | Protocol `traces`, `trace`, `logs`, `metrics`; CLI group `telemetry`. | `"memory"` produces `memory.traces`, `memory.trace`, `memory.logs`, `memory.metrics`; CLI group `memory`. |

Application capture includes memory telemetry tools under the application prefix
directly, without another `app` or `telemetry` segment. Negative event retention
omits the events tool. Use distinct prefixes for multiple owners.

The CLI form is `GROUP:OPERATION`, such as `app:logs`; exact protocol names are
also callable. Tables below use CLI forms and exact JSON property names.
Properties are optional unless marked required. CLI flags use hyphens where
JSON uses underscores, such as `--min-duration` for `min_duration`; see
[Calling Tools](../cli/calling-tools.md) for input handling. Roles and annotations
describe capabilities but grant no access.

## Application State

These tools use the explicit prefix `app`.

| Command | Inputs | Result |
| --- | --- | --- |
| `app:info` | None. | Identity, build, lifecycle, and fresh Go runtime statistics; identity role. |
| `app:health` | `fresh`: boolean, default false. | Overall `status` and check `items`; health role. Reads the cache unless `fresh=true`. |
| `app:started` | `wait`: boolean; `timeout`: duration. | `started`, lifecycle `state`, optional `started_at`. Successful startup remains recorded after stopping. |
| `app:healthy` | `wait`: boolean; `timeout`: duration; `fail_fast`: boolean. | Cached background `status`, optional `checked_at`, and `checks`; never executes checks. |
| `app:ready` | `wait`: boolean; `timeout`: duration; `fail_fast`: boolean. | `ready` and a safe failure `reason`, using the readiness probe's decision; never executes checks. |
| `app:events [NAME]` | `name`: string glob; `since`: duration; `limit`: integer; `cursor`: string; `wait`: duration. | Oldest-first metadata page; absent when event retention is disabled. See [Cursor Pages](#cursor-pages). |

After successful startup, info includes `uptime`, a Go duration string truncated
to whole seconds, and `uptime_seconds`, the elapsed whole seconds. Both are
omitted before startup completes. Runtime statistics include goroutines, heap
allocation and system memory in bytes and formatted text, GC count, and
`gomaxprocs`.

Health statuses are `ok`, `pending`, `failing`, and `stale`. Cached individual
check records preserve their last verdict, transition time, and check time even
when the overall report becomes stale. Health does not include lifecycle
liveness; use readiness for the traffic decision. With no registered checks,
health can be `ok` without proving dependencies work.

`fresh=true` executes every registered check with the caller context and the
configured readiness timeout. It does not update the background readiness cache
or publish health transitions. The call waits for all callbacks; deadlines
cannot forcibly interrupt application code that ignores cancellation.

Event `name` uses Go `path.Match` syntax, such as `billing.*`, not the event bus's
subscription-pattern syntax. `since` is a nonnegative duration relative to each
call; zero has no age restriction. Event pages default to 100 items and accept
normalized limits of 1–1000. Records contain `time`, `age`, `name`, `payload_type`,
and optional `trace_id`, never payload values.

### Condition Waits

Conditions observe immediately unless `wait=true`. An immediate false condition
is successful structured output, not a failed gate. Omitted or zero `timeout`
selects a 30-second wait budget; a nonzero timeout requires waiting. Durations
are nonnegative Go duration strings.

For example, this is the JSON input for a readiness wait, not a dependency check:

```json
{
  "wait": true,
  "timeout": "30s"
}
```

The tool returns when ready, or fails with the final safe observation. For CLI
gating and transport timeout handling, use [Waiting and Following](../cli/waiting-and-following.md).

Healthy and ready also accept `fail_fast=true`, which requires `wait=true`.
Fail-fast stops on a fresh failed background pass, including an already cached
pass that remains fresh. Pending or stale health continues waiting. Without
fail-fast, checks can recover during the wait budget. Started has no fail-fast
input. An already satisfied condition succeeds even in a terminal lifecycle.

### Condition Failures

Safe tool failures travel in result `_meta["arkoren.dev/failure"]`, not successful
structured content. The CLI treats them as failures.

| Code | Meaning |
| --- | --- |
| `invalid_input` | Nonzero timeout without waiting, or fail-fast without waiting. Schema validation can reject other invalid inputs before execution. |
| `condition_timeout` | The server wait budget expired; details contain the last observation as `result`. |
| `condition_stopped` | Failed, stopping, or stopped lifecycle prevents an unsatisfied condition from completing; details contain `result`. |
| `condition_failed` | Fail-fast observed a fresh failed background health pass; details contain the operation's `result` and safe `checks`. |

Caller cancellation or client deadline expiry can end the request without one
of these typed responses. Health failure text is withheld unless application
`HealthDetails` permits disclosure. That same setting affects verbose HTTP
readiness and fail-fast check details; it is not endpoint authorization.

## Telemetry Records

These examples use application group `app`. A standalone memory store uses its
own prefix or the default `telemetry` group. Durations are nonnegative Go
duration strings; zero removes the age or minimum-duration restriction. Trace
and log limits normalize to 1–1000; omission or zero selects the listed default.

| Command | Inputs | Result And Limits |
| --- | --- | --- |
| `app:traces` | `status`: `error` or `ok`; `route`: string; `name`: string; `min_duration`, `since`: durations; `limit`: integer. | Newest-first summaries in `items`, default 20; `truncated` means matching summaries were omitted. |
| `app:trace ID` | Required `id`: 8–32 hex digits, case-insensitive. | One uniquely matching trace's finished-span tree in `roots`, joined logs, and `orphan_logs`. |
| `app:logs` | `level`: `debug`, `info`, `warn`, or `error`; `trace_id`: 8–32 hex digits; `since`: duration; `limit`: integer; `cursor`: string; `wait`: duration. | Oldest-first cursor page, default 100; level is a minimum severity, trace ID is a case-insensitive prefix filter. |
| `app:metrics [NAME]` | `name`: instrument-name prefix. | Retained aggregates in `items`, including scope, kind, units, label keys, and points; not a query to an external metrics backend. |

For traces, `route` exactly matches the root's `http.route` or span name; `name`
matches a substring of any retained span's name. `min_duration` tests the root's
duration; `since` tests trace start age. A summary reports root duration,
retained span and error counts, status, and optional HTTP status and error type.
Summary status is `error` if any retained span ended in error; otherwise `ok`.

A trace tree can have multiple roots when parents are remote or evicted.
Retained logs whose spans are unfinished or evicted appear as orphans. Log
prefix filtering does not require a unique trace match; trace lookup does.

| Lookup Failure | Meaning And Recovery |
| --- | --- |
| `trace_not_found` | No retained trace matches. Check the full ID and capture history; eviction or missing capture is not proof no work occurred. |
| `ambiguous_trace` | Several retained traces match. Supply a longer prefix or full ID. |

Neither failure discloses candidate IDs. Malformed IDs are rejected by input
validation rather than treated as trace absence.

## Router Tools

With prefix `public`, the router publishes `public:routes [PATH]`. Its string
inputs are `path`, a case-sensitive registered-pattern prefix, and `method`, a
case-insensitive HTTP method. Omit either filter to accept all its values.

For example, this JSON input selects registered GET patterns under `/invoices`:

```json
{
  "method": "GET",
  "path": "/invoices"
}
```

The result contains every match in registration order, with handler identity
and optional source location. `truncated` is always false. This reads the live
registry, including routes added after tool construction; it does not resolve a
concrete URL or execute a handler.

## Scheduler Tools

These commands require prefix `jobs`.

| Command | Inputs | Result |
| --- | --- | --- |
| `jobs:entries` | None. | All entries in registration order: cron spec, description, timeout, overlap setting, and optional next-run time. `truncated` is false; this is configuration, not execution history. |
| `jobs:run NAME` | Required `name`: string identifying an existing entry. | Executes once and waits; success reports name, start time, and duration. |

`run` is non-idempotent application execution, not an inspection read. Publish
and invoke it only with explicit operator authorization, established overlap
safety, and a duplicate-effect policy. Entry timeouts and caller cancellation
apply cooperatively; driver-owned overlap guards do not apply. A callback that
ignores cancellation can keep the call from returning.

Only a failed entry lookup produces safe `not_found`. An existing entry's error
remains an execution failure subject to adapter redaction, including an error
wrapping `schedule.ErrUnknownName` from a nested call. Never automatically retry
after failure or a lost response: neither proves that effects were undone.
Use the [authorized action path](application-diagnosis.md#authorize-actions) to
decide whether execution is appropriate.

## Cursor Pages

Only application events and memory logs advertise cursor following. Their
`wait` input is a duration string, unlike conditions' boolean `wait`.

Without a cursor, the tool returns the newest matching history up to `limit`,
ordered oldest first. Its cursor anchors at the current end of the store, not
at the first page of a historical export. Older matches omitted by that
snapshot cannot be retrieved by following its cursor.

To skip history, supply `cursor: "tail"`. For example, the following input to
`app.logs` immediately anchors an error-log query:

```json
{
  "level": "error",
  "cursor": "tail"
}
```

The result is an empty `items` array and an opaque cursor. Send that returned
cursor unchanged to the same tool with the same filters to read new records.
You can change page size and wait without changing the query. Keep `since`
unchanged too; its relative age cutoff is evaluated anew for each call.

`wait` ranges from `0s` to `25s` and bounds an idle continuation. Initial
snapshots and tail anchors return immediately regardless of that budget.
Nonmatching arrivals do not extend the fixed deadline. All calls remain
cancelable.

| Field | Interpretation |
| --- | --- |
| `items` | Matching records, oldest first; never null. |
| `cursor` | Next opaque position, even on an empty page; not an authorization token. |
| `truncated` | An initial snapshot omitted older matching history; following cannot page backward. |
| `dropped` | Records overwritten since the supplied position, possibly including nonmatches. |
| `has_more` | Retained matching records remain; drain another page without waiting. Nonmatching records also advance the position. |
| `instance_changed` | Reset or replacement detected; this response is empty with a fresh tail cursor, not a mixture of store generations. |

If `instance_changed` is true and you need the new store's history, make a
separate snapshot request. Do not treat it as uninterrupted continuation.
Malformed, wrong-tool, wrong-filter, and future-position cursors return safe
`invalid_cursor`. Invalid limits or waits can yield `invalid_input` or an input
schema failure. Do not silently restart after failure and label the output
complete.

Store generations do not survive reset or replacement, and rings can evict
records between polls. [CLI following](../cli/waiting-and-following.md) reports
loss and restart notices, but does not reconnect or persist cursor checkpoints.

## Capture Limits

Attach application capture once during composition. Application telemetry
recording starts after successful composition; event metadata capture starts
when its listener is attached. New capture defaults are:

| Signal | Retained Capacity |
| --- | --- |
| Spans | 2000 finished spans. |
| Logs | 5000 records. |
| Metrics | 1000 distinct label combinations per instrument. |
| Events | 500 metadata records. |

Zero `CaptureLimits` fields select defaults; negative fields disable retention.
Existing configured memory telemetry retains its own capacities. Standalone
memory stores follow their builder limits and require their own lifecycle;
calling their tool factory does not start them. Capture does not configure an
exporter or itself start the primary telemetry driver.

These are bounded, process-local observations, not a durable audit trail or
export. Tools do not show unfinished spans and cannot recover unrecorded or
evicted evidence. Event
payload values are excluded, but names, types, and trace IDs still disclose
application structure. Route metadata exposes handler identities and source
paths. Log text, telemetry attributes, and approved health details can contain
private data; protect the endpoint and saved output. Follow
[Telemetry Privacy](../observability/telemetry-privacy.md) when instrumenting
application code.

Custom diagnostics use ordinary [MCP tool definitions](../mcp/tools.md), not a
separate inspector extension system. Factory declarations are in the
[application](https://pkg.go.dev/arkoren.dev/core/application),
[memory telemetry](https://pkg.go.dev/arkoren.dev/core/service/telemetry/memorytelemetry),
[router](https://pkg.go.dev/arkoren.dev/core/http/router), and
[schedule](https://pkg.go.dev/arkoren.dev/core/service/schedule) APIs.
