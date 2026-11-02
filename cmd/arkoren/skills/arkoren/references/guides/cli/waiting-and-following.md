---
title: Waiting and Following
description: Bound condition waits and follow new records while accounting for cursor gaps and output failures.
order: 40
summary:
  - Gate work on passive readiness or health observations with a bounded failure policy.
  - Account for discovery time, request deadlines, and the narrow waiting-read retry policy.
  - Follow NDJSON records while detecting lost history, generation changes, and output failures.
---

# Waiting and Following

Deployment checks need an observed condition; live investigations need new
records. A condition wait observes state until it is satisfied. Cursor following
repeatedly reads records after an opaque position. Start with a
[connected CLI](connecting.md) and tools advertising the required inputs; the
two operations use different meanings of `wait`.

## Gate Readiness

If discovery advertises the application group `app`, wait for readiness before
sending test traffic:

```sh
arkoren app:ready --wait --timeout 30s
```

Proceed only after successful completion. An immediate `app:ready` can exit 0
with `ready: false`; it is an observation, not a shell gate. The wait does not
start the application or run dependency checks.

Choose the condition that matches your next action. `app:started --wait` means
startup completed, not that dependencies are healthy. With registered checks,
`app:healthy --wait` requires a recent successful background pass. Without
checks, health can already be `ok`; that says nothing about unregistered
dependencies. Readiness observes whether the application can receive traffic.

## Choose Failure Policy

Healthy and ready waits normally allow failed checks to recover within the
budget. If deployment should stop on a fresh failed background pass, opt into
fail-fast behavior:

```sh
arkoren app:healthy --wait --timeout 30s --fail-fast
```

An already cached failed pass ends this attempt if it is still fresh. Pending
or stale health keeps waiting. `--fail-fast` requires `wait=true` and is not an
input to `started`. Omit it when recovery within the budget is expected.

These are passive observations. If you intend to execute checks now, use the
explicit `app:health --fresh` operation described in
[Application Diagnosis](../diagnostics/application-diagnosis.md).

## Bound Waiting

The CLI grants a condition-wait budget only when the discovered tool is annotated
read-only, declares a boolean `wait` property, and receives JSON `true` in the
final merged input. `timeout` configures the budget only if its advertised type
is string. Omitted or zero duration selects 30s, not an unlimited wait.

One budget counts from before discovery and input processing through connection
retries, execution, and owned session cleanup. The server receives the remaining
duration, not a fresh full timeout. An explicitly non-string JSON `timeout` is
left for server validation, not repaired. Non-string or undeclared timeout
properties otherwise remain ordinary tool data.

The ordinary 10s per-request deadline is replaced by this total budget unless
you explicitly set `--request-timeout`. An explicit setting can impose a shorter
per-request limit; `--request-timeout=0` removes that limit, not the total budget.
Session cleanup has a separate one-second maximum and also honors cancellation
and the remaining budget. Cleanup does not replace a selected result or stdout
failure.

Before discovery, conventional-looking arguments provide only provisional
connection hints. Schema parsing still owns tokens: a string property can
consume a literal `--wait` or `--input` as its value without enabling either
behavior. Only the final parsed input determines execution retry eligibility.

Eligible waiting reads can retry broken transports within the budget. Discovery
backoff starts at 50ms, grows by 1.5, and caps at 500ms; execution retries pause
50ms. HTTP, authentication, certificate-verification, protocol, and tool failures
are terminal. Actions and follow calls are never replayed automatically.

Application condition failures include `condition_timeout`, `condition_stopped`,
and `condition_failed`. Their safe details are defined in
[Diagnostic Tools](../diagnostics/diagnostic-tools.md#condition-failures).
A client deadline can expire before the server's failure response arrives, so
handle ordinary timeout errors too.

## Follow Records

To observe warnings produced while reproducing an issue, start a live tail. This
example assumes discovery advertises `app:logs` with `follow: true`:

```sh
arkoren app:logs --level warn --follow --no-color
```

The first call establishes an empty tail anchor; it does not print existing
history. Subsequent `tools/call` requests send the returned opaque cursor with
an idle wait of at most 25s. Available pages drain immediately. This is long
polling, not a separate diagnostic SSE endpoint.

Follow prints compact newline-delimited JSON (NDJSON), one record per line,
without page envelopes or saved cursor checkpoints. Loss and generation changes
produce notice objects on the same stdout stream. For example, separate polls
might report:

```json
{"dropped":12}
{"instance_changed":true}
```

Consumers must distinguish notices from application records. A notice can also
contain `truncated: true`, and one notice can combine fields. A replaced or reset
store establishes a fresh tail rather than replaying the new generation's
history.

`--request-timeout` does not limit follow requests. Interrupt the process to
stop it. A broken connection ends follow instead of silently reconnecting;
restarting the CLI establishes a new tail and can miss records between runs.

## Retain History

If you need recent context, request a bounded snapshot before reproducing the
problem:

```sh
arkoren app:logs --level warn --limit 100
```

This prints one JSON result, not a live stream. A snapshot followed by a new
`--follow` invocation is not an atomic handoff: records can fall between them.
For controlled continuation, use ordinary calls with `--cursor` and retain the
returned cursor according to the
[cursor contract](../diagnostics/diagnostic-tools.md#cursor-pages).
The CLI's `--follow` mode always chooses a fresh tail rather than resuming your
saved position.

Capture limits, ring eviction, and caller visibility bound the available
evidence. Empty history does not prove an operation never occurred. Protect
captured output like application logs.

## Check Completion

Use a condition wait rather than `status` as a readiness gate. Check the process
exit status even if stdout already contains data. Clean follow cancellation
exits 0; a broken pipe or short write exits 1 and takes precedence over
cancellation or the remote verdict. Output failure does not undo remote effects.

For all command flags and statuses, use [CLI Reference](reference.md).
