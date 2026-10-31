---
title: Waiting and Following
description: Gate automation on observed conditions and follow bounded event or log records without hiding lost history.
order: 40
---

# Waiting and Following

## Introduction

Start with a [connected CLI](connecting.md) and discovered tools that advertise
the needed inputs. A condition wait observes state until it is satisfied; cursor
following reads new records. Neither starts the application or runs a scheduler
action, and they use different meanings of `wait`.

## Gating work on readiness

When the endpoint publishes the application group `app`, wait for readiness
before sending test traffic:

```sh
arkoren app:ready --wait --timeout 30s
```

Proceed only if the command succeeds. An immediate `app:ready` can successfully
return `ready: false`; it is not a shell gate. `app:started --wait` means startup
completed, not that dependencies are healthy. With registered checks,
`app:healthy --wait` requires a recent successful background health pass.
Without registered checks, health can already be `ok`; this does not verify
unregistered dependencies. None of these calls executes checks.

By default, healthy and ready waits tolerate failed checks while waiting for
recovery within the budget. To end the attempt on a fresh failed background
pass instead, add `--fail-fast`:

```sh
arkoren app:healthy --wait --timeout 30s --fail-fast
```

An already cached failed pass triggers this failure if it is still fresh;
pending or stale health keeps waiting. Omit `--fail-fast` when recovery within
the budget is expected. It is not available on `started`, and requires
`wait=true`. To request an actual check run, use
`app:health --fresh` as described in [Diagnosing an
Application](../observability/diagnosing-an-application.md#checking-health).

## Setting the wait budget

The advertised schema and final merged input determine whether this is a waiting
read: the tool must be annotated read-only, declare boolean `wait`, and receive
the JSON boolean `true`. A string property can consume a literal `--wait` or
`--input` as its value; that text does not opt into waiting or select input.
Before discovery, conventional-looking arguments provide only provisional
connection hints, not authority over tool inputs or execution retries.

For an eligible read, one budget counts elapsed discovery and input-processing
time, connection retries, the condition wait, and owned session cleanup. Omitted
or zero timeout selects 30s. Only a schema-declared string `timeout` configures
this duration; other timeout types remain tool data. The server receives the
remaining budget in that string field, not a new full timeout. An explicitly
supplied non-string JSON value is left for server validation, not silently
repaired. The normal 10s per-request deadline is replaced by the budget; an
explicit `--request-timeout` can still impose a shorter per-request limit.

During discovery, transport failures retry within that budget, including a port
not bound yet. Once discovered, only a read-only tool whose schema declares a
boolean `wait` and whose input requests `wait=true` is eligible for transport
retry. HTTP, authentication, certificate, protocol, and tool failures are not
retried. Actions and follow calls are never replayed automatically.

Owned SDK session cleanup observes caller cancellation and the remaining command
time, with a separate maximum of one second even with `--request-timeout=0`.
This cleanup limit does not shorten the tool call itself. Cleanup cannot replace
an already selected result or a stdout failure.

Condition failures include `condition_timeout`, `condition_stopped`, and
`condition_failed`; the [diagnostic inventory](../observability/diagnostic-tools.md#condition-failures)
defines their safe details. A client deadline can expire before a server failure
response arrives, so automation must also handle ordinary timeout errors.

## Following new logs or events

With a cursor-enabled log tool, start following before reproducing the issue.
If discovery advertises the application group `app` and cursor support on its
`logs` tool, run:

```sh
arkoren app:logs --level warn --follow --no-color
```

The first call establishes an empty tail anchor: existing history is not printed.
Subsequent ordinary `tools/call` requests carry the returned opaque cursor and
an idle wait of at most 25s. Available pages drain immediately. This is long
polling, not a separate diagnostic SSE endpoint. `--request-timeout` does not
limit follow calls; interrupt the process to stop it.

The stream prints one compact JSON record per line. Loss or generation changes
also appear on stdout, for example:

```json
{"dropped":12}
{"instance_changed":true}
```

Consumers must handle these notices separately from records. A reset or replaced
store yields a fresh tail; it does not replay the new generation's history. A
broken connection ends follow rather than reconnecting silently. Starting a new
CLI follow starts at a new tail, so records between runs can be missing.

For historical context, take a bounded snapshot first with
`arkoren app:logs --level warn --limit 100`. This is not an atomic
snapshot-and-follow handoff. To control that handoff or persist continuation
positions, use ordinary calls with `--cursor` and the returned cursor according
to the [cursor contract](../observability/diagnostic-tools.md#cursor-pages).

## Using results in automation

Discover the current catalog rather than saving assumed commands. Prefer
qualified names when metadata provides them. Check the process exit status
before consuming output; `status` is a connection summary, not a readiness gate.
Use `--compact --no-color` for ordinary JSON and protect captured output like
application logs.

Clean follow cancellation exits 0. A stdout failure, including a broken pipe,
exits 1 even if the remote read succeeded. Ring eviction, disabled capture, and
caller visibility all limit evidence: an empty history does not prove that no
operation occurred. For exact syntax and statuses, use [CLI Reference](reference.md).

For the owner factories, see the [application API](https://pkg.go.dev/arkoren.dev/core/application) and
[memory telemetry API](https://pkg.go.dev/arkoren.dev/core/service/telemetry/memorytelemetry).
