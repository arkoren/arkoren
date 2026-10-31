---
title: Diagnosing an Application
description: Find a route, correlate a failed request with traces and logs, check health, and control explicit operator actions.
order: 90
---

# Diagnosing an Application

## Introduction

When a request fails, identify its registered route, then use the request's trace
ID to join retained spans and logs. These reads help distinguish request failures
from dependency health problems without rerunning application work.

Start with a running application whose [selected diagnostics are
published](exposing-diagnostics.md), a [connected CLI](../cli/connecting.md), and
an accepted operator credential. The commands below assume the application
prefix `app` for state and telemetry, a router prefix `public`, and a scheduler prefix `jobs`.
Use `arkoren tools` to discover your endpoint's actual tools and groups.

## Finding the route

Before reproducing a failing request, check which handler owns its path:

```sh
arkoren public:routes --method GET --path /invoices
```

The JSON `items` contain method, registered pattern, handler identity, and source
location when available. `--path` matches a registered-pattern prefix, not a
concrete URL matcher. An empty result can mean the wrong router, method, or
prefix; it does not prove that another listener has no such route.

## Finding the request's trace

Use an existing read-only route and a known invoice ID when reproducing safely:

```sh
curl -i http://127.0.0.1:8080/invoices/42
```

This application-specific route must already exist; do not substitute a mutating
request without considering its effects. With [request tracing](request-tracing.md)
and a valid request span, the default response-correlation settings put
`00-<trace-id>-<span-id>-<flags>` in the `traceresponse` header; an error problem
document also includes `trace_id`. Copy the trace ID, not the entire header.

If you have no ID, find recent failed traces or slow requests:

```sh
arkoren app:traces --status error --since 5m
arkoren app:traces --route '/invoices/{id}' --min-duration 500ms
```

Results are newest-first summaries in `items`. The route is the registered
template, not `/invoices/42`. A 4xx response does not necessarily mark a server
span as error; inspect status and route rather than relying only on the error
filter.

## Joining spans and logs

Set `TRACE_ID` to the copied 32-hex ID, then inspect its tree and log snapshot:

```sh
arkoren app:trace "$TRACE_ID"
arkoren app:logs --trace-id "$TRACE_ID" --since 5m
```

The trace result has `roots` with child spans and joined logs. `orphan_logs`
contains retained logs whose spans are unfinished or no longer retained. The
log query returns an oldest-first bounded page. Both commands accept a hex
prefix of at least eight characters, but trace lookup requires a unique match.
If it returns `ambiguous_trace`, use a longer prefix. `trace_not_found` can mean
eviction, not an invalid request or proof the operation never ran.

Only finished retained spans appear. Capture can be disabled or bounded, and a
reset loses history. If there is no request span, check the tracing middleware
and capture setup before increasing limits. To watch a new reproduction, use
[log following](../cli/waiting-and-following.md#following-new-logs-or-events).
Protect copied output: application log messages and attributes can contain
private data even though Arkoren's built-in instrumentation avoids payloads.

## Checking health

Read the cached report first. If you need a new dependency check, the second
command runs every registered check; the third only observes readiness:

```sh
arkoren app:health
arkoren app:health --fresh
arkoren app:ready
```

The first call observes cached check results; `--fresh` executes every registered
check now. The fresh run does not replace the background readiness cache or
publish health-transition events, so a fresh success alone does not restore
traffic. Checks receive a context deadline, but the call still waits for every
callback to return; it cannot forcibly stop a check that ignores cancellation.

Inspect the overall status as well as each check's status and timestamps. Cached
health can be `stale` even when its individual checks last passed; readiness then
remains false. Health error text is withheld unless the developer enables
`HealthDetails` for the application, including MCP health and fail-fast details;
do not require raw dependency errors in automation.

A direct health-role call prints the report and exits 1 when its `status` is
`failing`; inspect both the output and exit status. Pending or stale health is
not that failing verdict.

An immediate readiness result with `ready: false` is still a successful read.
For a shell gate, use `arkoren app:ready --wait --timeout 30s`; see
[condition waits](../cli/waiting-and-following.md#gating-work-on-readiness).
`status` summarizes the connection and optional cached health but is not a
health/readiness gate. For background intervals, freshness, and probe behavior,
see [Health and Readiness](../application/health-and-readiness.md).

## Reviewing an explicit scheduler action

If scheduler diagnostics are exposed, inspect entries before deciding to run one:

```sh
arkoren jobs:entries
arkoren jobs:run --help
```

The listing reports configuration and next-run times, not execution history.
Confirm the entry, permissions, overlap safety, and duplicate-effect policy with
the application owner. Only then, for an existing entry named `refresh-reports`,
request one manual run:

```sh
arkoren jobs:run refresh-reports
```

This executes application code immediately and waits for completion. Entry
timeouts and cancellation are cooperative: a callback that ignores its context
can keep the call from returning. Driver-owned overlap guards do not apply. Do not
run it concurrently if the task is unsafe to overlap, and do not automatically
retry after failure: work may already have completed. A successful response
reports the entry name, start time, and duration, not application payloads.

A safe `not_found` means the requested entry was absent during lookup. If an
existing entry fails while calling another scheduling operation, that remains an
execution failure, even if its error wraps `schedule.ErrUnknownName`. Do not infer
that the entry was absent or that none of its work ran.

For every tool's filters, limits, cursor semantics, and failures, use
[Diagnostic Tools](diagnostic-tools.md). For factory APIs, see
[router](https://pkg.go.dev/arkoren.dev/core/http/router), [memory telemetry](https://pkg.go.dev/arkoren.dev/core/service/telemetry/memorytelemetry),
[application](https://pkg.go.dev/arkoren.dev/core/application), and [schedule](https://pkg.go.dev/arkoren.dev/core/service/schedule).
