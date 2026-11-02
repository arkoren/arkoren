---
title: Application Diagnosis
description: Investigate request failures using retained traces, logs, and health before authorizing new execution.
order: 20
summary:
  - Identify the process and route before correlating a request's retained spans and logs.
  - Distinguish cached health, explicit dependency checks, and traffic readiness.
  - Require authorization and outcome verification before manually executing scheduled work.
---

# Application Diagnosis

When a request fails, inspect the running process and its retained evidence
before repeating application work. A route identifies the handler; a trace ID
connects finished spans and captured logs. Health then helps distinguish a
request-specific failure from a dependency problem.

You need [published diagnostics](capture-setup.md), an
[authenticated CLI connection](../cli/connecting.md), and operator permission to
inspect this application. The examples use application prefix `app`, router
prefix `public`, and scheduler prefix `jobs`. Discover your endpoint's actual
names with `arkoren tools`; not every application publishes every tool.

## Confirm Target

Check process identity before interpreting evidence from an incident:

```sh
arkoren app:info
```

Confirm the application, build, and lifecycle state match the intended process.
Then find the handler for the affected registered path:

```sh
arkoren public:routes --method GET --path /invoices
```

The returned `items` identify the method, registered pattern, handler, and source
location when available. The path filter matches a registered-pattern prefix;
it does not resolve a concrete request URL. An empty result can mean the wrong
router, method, or prefix, not that no listener serves the path.

## Find Evidence

Prefer an existing request's trace ID. With active
[request tracing](../observability/request-tracing.md) and default response
correlation, a valid request span supplies a `traceresponse` header in the form
`00-<trace-id>-<span-id>-<flags>`. A problem response also carries `trace_id`.
Use the 32-hex trace ID, not the entire header.

If the incident has no ID, search recent retained failures:

```sh
arkoren app:traces --status error --since 5m
```

The newest-first summaries let you select a trace without executing another
request. The error filter means at least one retained span ended in error. HTTP
tracing marks server spans as errors for 5xx responses, not 4xx responses; an
unsuccessful client request can therefore be absent from this filtered result.

If latency is the symptom, narrow by registered route and root duration instead:

```sh
arkoren app:traces --route '/invoices/{id}' --min-duration 500ms
```

Use the registered template, not `/invoices/42`. Filters operate on retained
evidence, so a missing result is not proof that the request never ran.

If reproduction is necessary, first confirm permission, a known invoice ID, and
that the application's existing GET route is read-only. For a local development
listener where that route already exists:

```sh
curl -i http://127.0.0.1:8080/invoices/42
```

Inspect its response correlation. Use your application's normal authentication
when required; this example creates neither the route nor credentials. Do not
substitute a mutating request without an explicit execution decision.

## Correlate Records

Set the shell variable `TRACE_ID` to the selected trace ID, then open its retained
span tree:

```sh
arkoren app:trace "$TRACE_ID"
```

The `roots` contain finished spans, their children, and joined logs. Multiple
roots can reflect remote or evicted parents. `orphan_logs` contains logs whose
spans are unfinished or no longer retained. A trace prefix of at least eight hex
digits works only if it identifies one trace; use a longer prefix after
`ambiguous_trace`.

Read the same trace's recent log window separately when you need an oldest-first
view across spans:

```sh
arkoren app:logs --trace-id "$TRACE_ID" --since 5m
```

This is a bounded snapshot, not a complete export. Log prefix filters can match
several trace IDs, so use the full ID when correlating one request. Protect the
output: application messages and attributes can contain private data.

Only finished retained spans appear. `trace_not_found` can mean eviction,
disabled capture, reset, or a request handled by a different process. Check
[capture setup](capture-setup.md) and tracing middleware before increasing
limits. Neither a missing trace nor an empty log page establishes that no
application effects occurred. To observe an authorized new reproduction, use
[CLI log following](../cli/waiting-and-following.md); its loss notices matter
when records are overwritten or the store changes.

## Compare Health

Read cached health first so the investigation does not trigger dependency I/O:

```sh
arkoren app:health
```

Inspect the overall `status` and each check's timestamps. Overall health can be
`stale` even when individual checks last passed. Error text is withheld unless
the application enables `HealthDetails`; do not require raw dependency errors
in automation. The CLI prints the report and exits 1 for `failing`; pending or
stale health is not that failing verdict.

If you need a current dependency observation and are authorized to run the
registered checks, request a fresh pass:

```sh
arkoren app:health --fresh
```

This executes every registered check. It neither replaces the background
readiness cache nor publishes health-transition events, so a fresh success alone
does not restore traffic. Deadlines and cancellation are cooperative: the call
waits for every callback to return, including one that ignores its context.

Observe the traffic-readiness decision separately:

```sh
arkoren app:ready
```

A successful read can return `ready: false`. Cached stale health keeps readiness
false; health with no registered checks can be `ok` without proving any external
dependency works. For a shell gate, use the CLI's
[condition waits](../cli/waiting-and-following.md), rather than treating `status`
or an immediate read as a readiness gate. Background intervals and probe policy
belong in [Health and Readiness](../application/health-and-readiness.md).

## Authorize Actions

Diagnosis does not itself authorize execution. If the scheduler tools are
published, inspect their configuration and the action's help first:

```sh
arkoren jobs:entries
arkoren jobs:run --help
```

Entries report schedules and next-run times, not execution history. Before a
manual run, obtain explicit operator authorization and confirm the existing
entry, its effects, overlap safety, and duplicate-effect policy with the
application owner. Driver-owned overlap guards do not protect manual runs.

Only after those checks, an authorized operator can request one run of an
existing entry named `refresh-reports`:

```sh
arkoren jobs:run refresh-reports
```

The tool executes application code immediately and waits for completion. Success
reports the name, start time, and duration, not application payloads. Entry
timeouts and caller cancellation cannot forcibly stop an uncooperative callback.
Do not run concurrently when overlap is unsafe, and never automatically retry
after an error or lost response: effects may already have occurred.

A safe `not_found` means the entry was absent during lookup. An existing entry's
execution error remains an execution failure even if it wraps
`schedule.ErrUnknownName` from a nested scheduling call. It does not prove the
entry was absent or its effects were undone. Verify the application-specific
outcome through an authorized read before considering any further action.

For exact filters, limits, failures, and cursor semantics, use
[Diagnostic Tools](diagnostic-tools.md).
