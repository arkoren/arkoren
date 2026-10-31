---
title: Diagnostic Tools
description: Look up owner-provided tools, prefixes, capture limits, condition failures, and bounded cursor behavior.
order: 100
---

# Diagnostic Tools

## Introduction

This is the canonical inventory of shipped diagnostic MCP tools. Applications
[select and expose them](exposing-diagnostics.md) explicitly; discovery returns
only the tools available to the current caller. The endpoint's current
`tools/list` and CLI metadata remain authoritative for names and inputs; this
inventory does not guarantee that every listed tool is published. Start an
investigation with
[Diagnosing an Application](diagnosing-an-application.md), not this catalog.

## Owners and prefixes

Protocol names and CLI groups are related but distinct. Factories return
definitions over the owner's existing state; they do not register HTTP routes,
authenticate clients, or start the router, scheduler, or standalone store.

| Owner factory | Empty prefix: protocol / CLI group | Prefix example: protocol / CLI group |
| --- | --- | --- |
| `composer.MCPTools` during composition | Rejected; a nonempty prefix is required | `"app"`: `app.info`, `app.logs`, etc. / `app`; `"shop"`: `shop.info`, `shop.logs`, etc. / `shop` |
| `public.MCPTools` (router) | `routes` / `router` | `"public"`: `public.routes` / `public` |
| `scheduler.MCPTools` | `entries`, `run` / `schedule` | `"jobs"`: `jobs.entries`, `jobs.run` / `jobs` |
| `store.MCPTools` (memory telemetry) | `traces`, `trace`, `logs`, `metrics` / `telemetry` | `"memory"`: `memory.traces`, etc. / `memory` |

Application capture includes the memory telemetry tools under the same explicit
prefix as application state and events. It adds no `app` or `telemetry` segment.
Use unique prefixes for multiple owners. The CLI writes
`GROUP:OPERATION`; exact protocol names remain callable. Annotations and roles
are presentation hints, not authorization.

## Application tools

Names below use the explicit application prefix `"app"`.

| Command | Inputs | Result and behavior |
| --- | --- | --- |
| `app:info` | None | Identity/build/lifecycle information and fresh Go runtime statistics. Identity role. |
| `app:health` | `fresh` boolean | Cached `status` (`ok`, `pending`, `failing`, or `stale`) and check `items` by default. `fresh=true` runs all checks now without updating the background cache. Health role. |
| `app:started` | `wait`, `timeout` | `started`, lifecycle `state`, optional `started_at`. Successful startup remains recorded after stopping. |
| `app:healthy` | `wait`, `timeout`, `fail_fast` | Cached background `status`: `ok`, `pending`, `failing`, or `stale`; `checked_at` and `checks`. Does not run checks. |
| `app:ready` | `wait`, `timeout`, `fail_fast` | `ready` and a safe failure `reason`; uses the readiness probe's evaluation. Does not run checks. |
| `app:events` | `name`, `since`, `limit`, `cursor`, `wait` | Oldest-first event metadata page; omitted if event retention is disabled. Cursor-enabled. |

After startup completes, `app:info` includes both `uptime`, a Go duration string
truncated to whole seconds such as `1h2m3s`, and `uptime_seconds`, the elapsed
whole seconds. Both fields are omitted before startup completes.

Cached check records retain their last passing/failing verdict and transition and
check times even when the overall report becomes `stale`. Inspect the overall
status and age before trusting an individual result. Health status does not
include lifecycle liveness; use `app:ready` for traffic readiness.

Condition `wait` is boolean. Omitted or zero `timeout` selects 30s; nonzero
timeout requires `wait=true`. Healthy and ready `fail_fast` also requires waiting
and fails only on a fresh failed background pass, including a cached pass that
is still fresh. Pending or stale health keeps waiting. Without fail-fast, failed
checks can recover within the wait budget. Started has no fail-fast input.
Immediate false observations are successful reads, not failed gates. No checks
means health can be `ok`; it does not prove that unregistered dependencies work.

Event `name` uses Go `path.Match` glob syntax, for example `billing.*`.
`since` is a nonnegative duration relative to the call. `limit` defaults to 100,
with a normalized range of 1–1000. Each record has time, age, event name, payload
type, and an optional trace ID, never the payload value.

### Condition failures

Safe failures travel in result `_meta["arkoren.dev/failure"]`, outside successful
structured content. The CLI reports them as failure, not successful output.

| Code | Meaning and details |
| --- | --- |
| `invalid_input` | Nonzero timeout without wait, or fail-fast without wait; schema validation can reject other invalid values before execution. |
| `condition_timeout` | Server wait expired; details contain the last observation under `result`. |
| `condition_stopped` | Failed, stopping, or stopped lifecycle prevents satisfying the condition; details contain `result`. An already satisfied condition still succeeds. |
| `condition_failed` | Fail-fast saw a fresh failed background health pass; details contain `result` and safe `checks`. |

Cancellation and client deadline expiry can instead end the request without one
of these typed responses. Health failure text stays withheld unless the app
explicitly enables `HealthDetails`; enabling it can reveal dependency details.
Fresh checks are explicit I/O and are not performed by tool discovery or waits.
They use the readiness timeout and caller context but still wait for every check
to return. Cancellation cannot forcibly interrupt an uncooperative callback.
`HealthDetails` also governs verbose HTTP readiness and fail-fast check details;
it is one application-wide disclosure setting, not endpoint authorization.

## Memory telemetry tools

Commands below use the application's explicit `app` group. Durations are
nonnegative Go duration strings; zero removes an age/minimum-duration filter.
Normalized trace/log limits are 1–1000; omission or zero selects their defaults.

| Command | Inputs | Result and limits |
| --- | --- | --- |
| `app:traces` | `status`, `route`, `name`, `min_duration`, `since`, `limit` | Newest-first summaries in `items`, default 20; `truncated` marks omitted matching summaries. `status` is `error` or `ok`; `route` matches root `http.route` or exact root span name; `name` is a contained span-name substring. |
| `app:trace ID` | Required `id` | Tree of retained finished spans in `roots`, joined logs, and `orphan_logs`. ID is 8–32 hex digits, case-insensitive, and must uniquely match a trace. |
| `app:logs` | `level`, `trace_id`, `since`, `limit`, `cursor`, `wait` | Oldest-first cursor page, default 100. `level` is minimum `debug`, `info`, `warn`, or `error`; trace ID accepts a hex prefix of 8–32 characters. |
| `app:metrics [NAME]` | `name` | Instrument-name prefix filter; returns retained aggregates in `items`, including scope, kind, units, label keys, and points. Not an external metrics-backend query. |

Trace summaries report root duration, retained span/error counts, status, and
optional HTTP status/error type. A trace tree can have multiple roots when
parents are remote or evicted. Logs whose spans are unfinished or evicted appear
as orphans. No candidate IDs are disclosed on lookup failure:

| Code | Recovery |
| --- | --- |
| `trace_not_found` | Check the full ID and capture/eviction; missing history is not evidence no work occurred. |
| `ambiguous_trace` | Supply a longer prefix or full ID. |

## Router and scheduler tools

| Command | Inputs | Behavior |
| --- | --- | --- |
| `public:routes [PATH]` | `path`, `method` | Live complete route registry in registration order. Path is a case-sensitive registered-pattern prefix; method matches case-insensitively. Returns handler identity and optional source location; `truncated` is false. |
| `jobs:entries` | None | All entries in registration order with cron spec, description, timeout, overlap setting, and optional next-run time; `truncated` is false. Configuration, not execution history. |
| `jobs:run NAME` | Required `name` | Executes one registered entry now and waits; reports name, start time, and duration. Only failed entry lookup returns safe `not_found`; errors from executing an existing entry remain execution failures subject to adapter redaction. |

These examples require prefixes `public` and `jobs`. `run` is a non-idempotent
action, not a diagnostic read. Entry cancellation/timeouts apply, but
driver-owned overlap guards do not. Authorize it explicitly, avoid unsafe
concurrent manual runs, and never automatically retry it. A missing or invalid
response does not prove that effects were undone.

An execution error can wrap `schedule.ErrUnknownName`, for example when the entry
calls an unregistered deferred task. That does not turn the existing entry's
failure into safe `not_found`. Entry timeouts are cooperative context deadlines;
the tool waits for the callback and cannot force it to return.

## Cursor pages

Only application events and memory logs advertise cursor following. Their
`wait` input is a duration, unlike condition tools' boolean `wait`:

1. Omit `cursor` for a bounded newest-matching-history snapshot, returned oldest
   first. The cursor anchors at the current end, not the first page of an export.
2. Set `cursor: "tail"` for an immediate empty anchor without history.
3. Pass the returned opaque cursor unchanged to read new records. Keep the tool
   and filters unchanged. Page size and wait may change.

`wait` ranges from `0s` to `25s` and bounds an idle continuation. Snapshots and
tail anchors return immediately. A fixed wait deadline does not extend when
nonmatching records arrive. Calls remain cancelable. Page fields are:

| Field | Meaning |
| --- | --- |
| `items` | Matching records in order; never null. |
| `cursor` | Next opaque continuation, even on an empty page. A position, not an authorization token. |
| `truncated` | A snapshot omitted older matching history; no way to page backward from its tail anchor. |
| `dropped` | Records overwritten since the supplied position; can include nonmatches. |
| `has_more` | Retained matching records remain; drain another page without waiting. Nonmatches still advance the cursor. |
| `instance_changed` | Reset or replacement; returns an empty page and fresh tail instead of mixing generations. Take a separate new snapshot if you need the new store's history. |

Malformed, wrong-tool/filter, and future cursors return safe `invalid_cursor`
failures. Invalid limits or wait budgets can produce `invalid_input` or schema
validation failure. Do not silently restart on error and label the result
complete. Store generations can survive neither reset nor replacement, and
rings can evict between polls. [CLI follow](../cli/waiting-and-following.md)
emits loss/restart notices but does not reconnect or persist cursor checkpoints.

## Retention and disclosure

Application capture must be attached once during composition. Attaching it does
not start a primary driver or configure an exporter. Application telemetry
recording activates only after successful composition; event metadata capture
begins at listener attachment. Defaults for newly attached capture are 2000
finished spans, 5000 logs, 1000 distinct label combinations per metric instrument,
and 500 event records. Zero fields select defaults; negative fields disable
retention.
Existing configured memory telemetry keeps its own capacities. Standalone
memory stores follow their own builder limits and need their own lifecycle.

Capture is bounded process-local evidence, not an audit log or durable export.
Tools cannot recover unrecorded or evicted history. Events exclude payload
values, but names/types and trace IDs still reveal application structure. Route
metadata includes handler identities and source paths. Log text, telemetry
attributes, and approved health details can disclose private data; protect both
the endpoint and saved output. Review [Telemetry Privacy](telemetry-privacy.md)
when adding instrumentation.

Custom owner tools use ordinary [MCP definitions and CLI metadata](../mcp/tools.md),
not a second inspector catalog. For all factory APIs, see
[application](https://pkg.go.dev/arkoren.dev/core/application), [memory telemetry](https://pkg.go.dev/arkoren.dev/core/service/telemetry/memorytelemetry),
[router](https://pkg.go.dev/arkoren.dev/core/http/router), and [schedule](https://pkg.go.dev/arkoren.dev/core/service/schedule).
