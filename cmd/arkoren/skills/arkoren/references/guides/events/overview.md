---
title: Events Overview
nav: Overview
description: Create the in-process event bus, define typed events, emit and listen to them, and understand ordering, synchronous delivery, and failures.
order: 10
---

# Events Overview

## Introduction

An event says that something already happened: a user signed up, an invoice was
paid, a health check started failing. The code that made it happen announces it
and carries on, and any number of other places react. The announcing code does
not know who listens, so the signup feature can stay free of mail, analytics,
and audit logging, and each of those can be added or removed on its own.

Arkoren's event bus is the smallest tool for this. It lives inside one process,
delivers each event by calling the registered functions directly, and keeps no
state beyond the list of listeners. The application creates one bus for you and
Arkoren's own services publish to it when you ask them to, which makes it the
place to hook into everything from application startup to a failed email. The
bus is not a queue and not a broadcast, and [What events are not](#what-events-are-not)
says where the line is.

Focused examples show the ordinary success path and leave error results visible
without routine checks. Handle those errors in your application; examples about
failure behavior and tests keep the relevant checks.

## The bus

Create a bus directly when you need synchronous in-process notifications:

```go
import "arkoren.dev/core/event"

bus := event.New()
```

If you already use an application, use `bus := app.Events()` instead. It returns
the same nonnil `*event.Bus` for that application's lifetime; pass the bus around
rather than the application. Creating another bus isolates its listeners.
Set `event.Builder.Logger` in a construction callback to
choose the logger that receives listener failures, described in
[Errors and panics](#errors-and-panics); the application's bus logs through the
application's logger.

```go
bus := event.New(func(events *event.Builder) {
	events.Logger(slog.Default())
})
```

Construction callbacks run once, in order. A later `Logger` setting replaces an
earlier one; nil uses `slog.Default()` when a failure is logged. The bus keeps
the shared logger, but retaining and editing the builder cannot reconfigure the
bus. Omitted callbacks make no changes; a supplied nil callback or a callback
panic propagates.

Services publish on a bus only when you hand it to them through their
role-specific builder's `Events` setting.
Without a bus the service publishes nothing and
pays one nil comparison per operation:

```go
files := storage.New(driver, func(store *storage.Builder) {
	store.Events(bus)
})
```

[Built-in events](built-in-events.md) lists what each service publishes and when.

## Defining events

An event has a name and a payload. A **topic** ties the two together:
`event.NewTopic[T](name)` returns a value that carries payloads of type `T`
under that name. Declare topics once, as package-level variables, next to the
payload type, and let everyone else refer to the variable:

```go
package invoice

import "arkoren.dev/core/event"

// PaidEvent is the payload of [Paid].
type PaidEvent struct {
	InvoiceID int
	Cents     int64
}

var Paid = event.NewTopic[PaidEvent]("invoice.paid")
```

Because the topic fixes the payload type, publishing the wrong type or
listening for the wrong one is a compile error rather than a surprise at
runtime. The payload can be any type. A struct that other packages can
import and read is the usual choice, and a struct leaves room to add a field
later without breaking a listener.

Names follow one rule: one or more dot-separated tokens, each made of 1 to 64
lowercase letters, digits, or hyphens that do not start or end with a hyphen,
and at most 255 bytes in total. `invoice.paid` and `user.password-reset` are
valid; `Invoice.Paid`, `invoice..paid` and `invoice_paid` are not. Arkoren's own
names read `<area>.<thing>.<what happened>`, such as `storage.object.written`,
and past tense for something that has happened is a good habit to copy. The name
is checked when you listen or dispatch, and a bad one returns
`event.ErrInvalidName`.

> [!WARNING]
> The name, not the Go type, is what connects a publisher to its listeners. Two
> topics with the same name share their listeners, so give each name one payload
> type. A typed listener skips an event on its name whose payload is not its
> type, without an error, and the mismatch is easy to miss.

### Events without data

Some events need no payload: the application started, a cache was cleared. A
**signal** is a topic without data, declared with `event.NewSignal`:

```go
var CacheCleared = event.NewSignal("cache.cleared")
```

You publish it with `bus.Signal(ctx, CacheCleared)`, and a listener for it takes
an `event.Void`. These signals are unrelated to
[workflow signals](https://pkg.go.dev/arkoren.dev/core/service/workflow), which are messages sent to a
running workflow.

## Emitting events

There are two ways to publish, and they differ in what happens when a listener
fails.

`Emit` is for code that reports what happened, and it is what Arkoren's services
use. It never fails: if a listener returns an error or panics, the problem is
logged with the event name and `Emit` returns nothing. A listener can therefore
never change the outcome of the operation it observes, which is the property you
want from an announcement. A nil bus does nothing, so a component can hold an
optional bus and call `Emit` without checking it.

```go
bus.Emit(ctx, invoice.Paid, invoice.PaidEvent{InvoiceID: 7, Cents: 4900})
```

`Dispatch` is for code that needs the answer. It returns the first listener
error, and it does not recover panics, so a panic reaches the caller. Use it
when a listener is part of the operation, for example an audit insert that
may veto a change. Since that listener can fail the operation, it is closer to a
callback than to an observer, and an ordinary function call is often the
clearer design.

```go
err := bus.Dispatch(ctx, invoice.Paid, paid)
```

Publish after the thing has happened, and publish from the layer that knows it
happened: the service method rather than the HTTP handler, so every caller
announces the same event. Both methods take the context of the operation, and
listeners receive it unchanged.

## Listening

`Listen` registers a function for a topic. The function receives the context
and the typed payload, and returns an error:

```go
unsubscribe, err := bus.Listen(invoice.Paid, func(ctx context.Context, e invoice.PaidEvent) error {
	return receipts.Send(ctx, e.InvoiceID)
})
defer unsubscribe()
```

`Listen` returns an `Unsubscribe` function that removes the registration, and an
error only for an invalid topic name or a nil function. Most applications
register listeners once while wiring and never remove them, which is the right
place for them: after the application is built and before it starts, so no
event is missed. [Listeners](listeners.md) covers registering several
listeners, matching names by pattern, removing them, and writing listeners that
behave well.

## Ordering and delivery

Delivery is synchronous. `Emit` or `Dispatch` calls matching listeners on
the goroutine that called it, one after another, until delivery finishes or a
listener fails.
Three consequences follow, and they are the most important facts about the bus:

- **Listeners run in a fixed order.** Exact-name listeners run first, in the
  order they were registered. Pattern listeners run after them, also in
  registration order. With no matching listener, publishing allocates nothing.
- **A slow listener slows the publisher.** The operation that published waits
  for every listener. A listener that calls a remote service delays the request
  or the job that triggered the event, so keep listeners short and hand slow
  work to a queue.
- **Listeners share the caller's context and transaction.** A listener sees the
  context of the publisher, including its deadline and cancellation. If the
  publisher is inside a database transaction, a listener that uses the same
  context joins it, so its writes commit or roll back together with the
  publisher's. A listener that must wait until the commit, such as one that
  sends an email, should use `db.AfterCommit`, described in
  [Transactions](../database/transactions.md#running-code-after-commit).

Events are not queued, so nothing is delivered later. A listener registered
after an event has been published never sees it.

## Errors and panics

What happens when a listener fails depends on how the event was published.

With `Emit`, an error or a panic is logged at the error level with the event name
and, for a panic, the stack. The logger is the one given to `event.New`, or
`slog.Default()` when none was; the application's bus uses the application's
logger. The listeners after the failing one are skipped for that event, because
delivery stops at the first failure. The publishing code is unaware of any of
it.

With `Dispatch`, the first error is returned and the remaining listeners do not
run. A panic is not recovered.

Because a failure stops delivery, an unimportant listener that fails can hide
an event from the listeners registered after it. Write each listener to handle
its own errors, return an error only when you really want to stop delivery, and
log what you swallow. If one listener must not depend on another, register it
on its own topic or hand its work to a separate queue.

## What events are not

The bus is deliberately small, and it is the wrong tool in several cases:

- **Not durable.** Nothing is stored. If the process stops while a listener is
  running, that work is lost, and an event published while the process is down
  does not exist. Work that must happen belongs on a queue, covered in
  [Queues Overview](../queues/overview.md).
- **Not across processes.** An event reaches only the listeners in the process
  that published it. With several replicas, each sees only its own events. Moving
  messages between processes or to browsers is covered in
  [Broadcasting Overview](../broadcasting/overview.md).
- **Not retried.** A failed listener is not called again.
- **Not a return channel.** Listeners return an error and nothing else. If the
  publisher needs a value back, call a function.

A common pattern combines them: a listener on the bus does the cheap, local part
and enqueues a job for the rest.

To describe the messages your application sends or receives across processes,
use the [AsyncAPI overview](../asyncapi/overview.md). It exports a contract for broadcasts
and jobs; it does not change this bus's synchronous delivery.

## Lifecycle

The bus has no `Start` or `Stop` and holds no resources, so there is nothing to
close. It is safe to use from many goroutines at once, including registering and
removing listeners while events are being published.

Registering during a publish is safe: a listener added by another listener
takes effect for the next event, not the current one. Removing works the same
way, and a listener removed while an event is being delivered may still receive
that event, because delivery works from a snapshot taken when it started.

The application dispatches its own [lifecycle events](../application/lifecycle-and-ownership.md)
on the bus too. After composition succeeds, the application starts configured
telemetry, dispatches `Starting` (`app.starting`), then runs user `Startup`
callbacks. Register listeners before calling `Start` or during synchronous
composition; a listener registered in a `Startup` callback misses `Starting`.
If composition or telemetry startup fails, `Failed` can occur without `Starting`.
Only the application publishes the `app.*` lifecycle topics.

## Testing

A bus needs no fake. Create one with `event.New()` and pass it to your invoice
service. In a test with `t *testing.T`, a `bus`, and that `service`, collect the
payment event and assert what `Pay` publishes:

```go
var got []invoice.PaidEvent
if _, err := bus.Listen(invoice.Paid, func(_ context.Context, e invoice.PaidEvent) error {
	got = append(got, e)

	return nil
}); err != nil {
	t.Fatal(err)
}

if err := service.Pay(t.Context(), 7); err != nil {
	t.Fatal(err)
}

if len(got) != 1 || got[0].InvoiceID != 7 {
	t.Fatalf("events = %v, want one for invoice 7", got)
}
```

Because delivery is synchronous, the event has been delivered by the time the
call returns, so the test needs no waiting or channels. To test a listener on
its own, call it as an ordinary function, or publish to a bus with only that
listener registered. To check that a service configured with an event bus publishes
what it should, pass it a bus like this one; the same approach works for every
service in [Built-in events](built-in-events.md). See also
[Testing](../testing/overview.md).

For every function and option, see [the `event` API reference](https://pkg.go.dev/arkoren.dev/core/event).
