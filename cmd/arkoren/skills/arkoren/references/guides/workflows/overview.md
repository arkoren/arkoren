---
title: Workflows Overview
nav: Overview
description: Coordinate long-running processes with typed workflows and replay-safe steps.
order: 10
summary:
  - Distinguish workflow coordination from steps, signals, queue jobs, and scheduled tasks.
  - Define and register typed work with replay restrictions and a required run limit.
  - Recognize idempotency, retention, and durable-handoff boundaries before choosing a driver.
---

# Workflows Overview

A signup can send a welcome message now and wait days for confirmation. A
**workflow** keeps that process together as a named Go function. Each execution
is a **run**, with your identifier, a status, and a typed result.

With a durable driver, recorded history lets the engine rebuild an interrupted
run on another worker. The memory driver executes the same definitions locally
but loses runs when the process exits. Choose and configure a
[driver](drivers.md) before using the client examples.

## Process Boundaries

A [queue job](../queues/overview.md) is one unit of background work. A
[scheduled task](../scheduled-tasks/overview.md) starts work at a time. A workflow
coordinates several actions and decisions over its lifetime: send a message,
wait for confirmation, then activate an account or let the invitation expire.

A **step** performs an external effect, such as sending mail or updating a
database. A **signal** supplies a typed value from outside the run. The workflow
function coordinates those operations; it does not perform their I/O itself.
Durable drivers can suspend between operations without keeping your application
goroutine alive throughout the wait.

Durability is not an atomic transaction with your database or mail service.
An effect can succeed before its step result is recorded, causing another
attempt after recovery. Make step effects idempotent. Likewise, committing a
database change and starting a workflow are two separate operations unless your
application provides a durable handoff, such as an outbox.

## Defining Work

For onboarding, pass a small payload that both client and worker can encode.
These examples use `arkoren.dev/core/service/workflow`:

```go
type User struct {
	ID    int    `json:"id"`
	Email string `json:"email"`
}
```

Your application supplies `sendWelcome`, with signature
`func(context.Context, User) (string, error)`. It sends through your mail service,
honors cancellation, and tolerates repeated calls for the same user. Declare it
as a step so the driver can record its result:

```go
var SendWelcome = workflow.NewStep("send-welcome", sendWelcome)
```

A workflow can return that result directly. Give every workflow a positive
`MaxDuration`; registration rejects a definition without it.

```go
var Onboard = workflow.New("onboard", func(c *workflow.Context, user User) (string, error) {
	return c.Step(SendWelcome, user)
}, func(definition *workflow.Builder) {
	definition.MaxDuration(96 * time.Hour)
})
```

This baseline ends after sending the welcome message. To keep the run open for
confirmation, replace its body with the version in
[Signals and Waiting](signals-and-waiting.md#awaiting-confirmation). The whole-run
limit must exceed that legitimate wait as well as the steps around it.

Definitions are immutable once built. Builder callbacks run synchronously, once
in order, on a fresh draft; retaining that draft cannot reconfigure the published
definition. A supplied nil callback or a callback panic propagates. Expanded nil
or empty callback slices supply no edits.

Focused snippets in this category show error results but omit routine
propagation. Complete function bodies and examples about recovery keep their
checks; application code must handle the remaining errors.

## Replay Rules

An engine can **replay** the workflow function from the beginning, supplying
recorded operation results to reconstruct its state. Given those same results,
the function must make the same calls in the same order.

- Put network, database, file, and other I/O in [workflow steps](steps.md).
  Do not read mutable global state in coordination code.
- Use `c.Now()` and `c.UUID()` instead of reading the clock or random numbers
  directly. The driver makes their values stable across replay.
- Do not start goroutines or use your own channels in the workflow function.
  Use [durable waits](signals-and-waiting.md) instead. These rules also apply to
  `Select` callbacks and to code using the native context from `c.Raw()`.
- Inputs and outputs must round-trip through the shared codec. The default is
  `encoding/json/v2`; a `time.Duration` field does not marshal, so represent
  payload durations as an integer number of seconds.

A workflow change can invalidate histories still in flight. A new workflow name
keeps old and new coordination separate. An input version works only when you
retain the old branch and route existing runs to compatible workers; changing a
version field alone does not migrate history. Check the
[provider rollout limits](drivers.md) before replacing workers.

`MaxDuration` ends an overlong run with `workflow.ErrTimeout`; it cannot forcibly
terminate arbitrary Go code. `Retention` controls how long finished records stay
available where the provider supports it. Retained records, rather than the
identifier alone, provide duplicate-start protection.

## Registering Definitions

A worker needs the workflow and every step it can execute. Validate them together
before constructing the driver or starting its host:

```go
registry, err := workflow.Handlers(Onboard, SendWelcome)
```

Handle `err` at startup. Workflows and steps share a name namespace: names contain
1–64 lowercase letters, digits, or inner hyphens. Registration reports duplicate
names, nil functions, invalid declarations, and missing run limits. Constructors
retain the first declaration error; `NewSignal` instead panics for an invalid
signal name, so declare signals at startup too.

The registry snapshots definitions and policies, not the services captured by
their functions. Client and worker must agree on names, payload types, and codec.
Custom codecs must produce valid JSON and support concurrent use. Set them on
both `RegistryBuilder.Codec` and `ClientBuilder.Codec`; configuring one role does
not configure the other.

Native drivers register work through a [worker host](shared-worker-host.md);
their construction belongs on [Drivers](drivers.md).

Once the driver is ready, [start and inspect a run](running-workflows.md).
For declarations and options, see the
[`workflow` API reference](https://pkg.go.dev/arkoren.dev/core/service/workflow).
