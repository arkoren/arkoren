---
title: Writing Listeners
nav: Listeners
description: Register typed and pattern-matching listeners, remove them, and write listeners that stay short, tolerate failure, and are safe concurrently.
order: 20
---

# Writing Listeners

## Introduction

Deleting an image may also require thumbnail cleanup, without making the storage
code depend on your thumbnail service. A listener is a function the bus calls
when a matching event is published, so that cleanup can live separately.
[Events Overview](overview.md) explains the bus, topics, and delivery; this page
is about the listener side: the three ways to register one, how to remove it,
and what to keep in mind so a listener does not become the slowest or most
fragile part of the operation it observes.

The rule behind every recommendation here is that listeners run synchronously,
in the publisher's goroutine, with the publisher's context. A listener is
therefore part of the publisher's critical path, even though the publisher
cannot see it.

## Typed listeners

When a stored image is deleted, its thumbnails may need to be removed too.
Register that cleanup with `Listen` on the deletion topic. The listener receives
the topic's payload type, so it can use the object's key without a type assertion.
Here `bus` is your application's event bus and `thumbnails` is your cleanup service:

```go
import "arkoren.dev/core/service/storage"

_, err := bus.Listen(storage.ObjectDeleted,
	func(ctx context.Context, e storage.ObjectDeletedEvent) error {
		return thumbnails.Delete(ctx, e.Key)
	})
```

If the payload published under the topic's name is not of the listener's type,
the listener is skipped without an error. That only happens when two parts of a
program disagree about a name, which is why each name should have exactly one
topic variable.

To react to a signal, listen with `event.Void` as the payload type:

```go
_, err := bus.Listen(application.Started, func(ctx context.Context, _ event.Void) error {
	logger.InfoContext(ctx, "application started")

	return nil
})
```

Registration fails with `event.ErrInvalidName` when the topic's name is not
valid, and with `event.ErrNilFunc` when the function is nil. Both are mistakes
in the program rather than runtime conditions. Check the registration error
during wiring, before the application starts.

When wiring through an application composer, use
`composer.MustDefer(bus.Listen(application.Started, onStarted))` with your existing
typed handler to handle the registration error and own unsubscription together.
It removes the listener on rollback or shutdown, not when composition returns;
see [subscription ownership](../application/composition.md#own-subscriptions-and-infallible-releases).

## Listening by name

Sometimes you know an event's name but cannot import its topic variable—for
example, when the name comes from configuration. `ListenEvent` lets you register
by that exact string. You give up the typed callback: its payload is `any`, so
check the type before using it:

```go
_, err := bus.ListenEvent("invoice.paid", func(ctx context.Context, payload any) error {
	if e, ok := payload.(invoice.PaidEvent); ok {
		return receipts.Send(ctx, e.InvoiceID)
	}

	return nil
})
```

Prefer `Listen` whenever you can import the topic. It checks the type at
compile time and skips mismatched payloads for you.

## Listening by pattern

An audit log may need every storage event, rather than one listener for each
operation. `ListenRegex` lets one function receive all events whose names match
a regular expression. It receives the event name as well as the payload, so it
can tell those operations apart:

```go
_, err := bus.ListenRegex(regexp.MustCompile(`^storage\.`),
	func(ctx context.Context, name string, payload any) error {
		slog.InfoContext(ctx, "storage event", "event", name)

		return nil
	})
```

The pattern is matched against the event name with `MatchString`, so it is not
anchored: `storage` also matches `app.storage-check`. Anchor it with `^` and,
where needed, `$`. A nil pattern or function returns `event.ErrNilFunc`.

Pattern listeners receive their payload as `any` and never see the payload type
at compile time, so keep them to concerns that do not depend on it: logging,
counting, forwarding. Logging a payload verbatim is only as safe as the
publisher makes it. Arkoren's built-in fields omit tokens and request bodies,
but error values can quote provider responses or application data. Redact those
errors before forwarding them, and keep your own event payloads free of secrets.

## Order and failure

A listener is not alone on a name, so its position matters. Exact-name listeners
run in the order they were registered, and pattern listeners run after all of
them, also in registration order. If a listener returns an error, the listeners
after it do not run for that event. With `Emit` the error is logged and the
publisher continues; with `Dispatch` it is returned to the publisher. A panic
is logged by `Emit` the same way, and propagates through `Dispatch`.

So a listener that fails hides the event from later ones. Make each listener
responsible for its own failures: log, retry, or hand the work to a queue, and
return an error only when stopping delivery is the point. A listener whose job
is best-effort, such as sending a metric, should swallow its error:

```go
_, err := bus.Listen(invoice.Paid, func(ctx context.Context, e invoice.PaidEvent) error {
	if err := metrics.Count(ctx, "invoice.paid", e.Cents); err != nil {
		slog.WarnContext(ctx, "count payment", "error", err)
	}

	return nil
})
```

Do not depend on the order between your listeners for correctness. The order is
stable, but a listener added later by another package changes it, and the code
that relies on it is hard to see. If one step must follow another, put both in
one listener.

## Removing a listener

Every registration returns an `Unsubscribe` function. Calling it removes that
registration, and calling it more than once is safe. A listener tied to the life
of something shorter than the application, such as one request or one test,
should always remove itself:

```go
unsubscribe, err := bus.Listen(invoice.Paid, handler)
if err != nil {
	return err
}
defer unsubscribe()
```

Listeners tied strictly to a process-owned bus can remain for that bus's lifetime.
Use `MustDefer` for composition-owned subscriptions that must be removed on
rollback or shutdown, especially on a shared bus. A listener registered in a loop or a handler without a matching
removal leaks one registration per call, and every event then runs all of them.

Removal takes effect for the next event. An event that is being delivered at the
moment already has its list of listeners, so a listener removed from another
goroutine, or by an earlier listener, can still receive that one event.

## Keeping listeners short

Because the publisher waits, a listener is a delay in someone else's operation.
A request that stores a file waits for every `storage.object.written` listener
before it returns. Three habits keep this cheap:

- Do the minimum in the listener and move the rest to a queue: enqueue a job
  with the identifiers it needs, and let a worker do the slow call, with
  retries. The publisher still waits for dispatch, but not for the worker's
  remote call.
- Respect the context. The listener receives the context of the publisher, so a
  listener can observe a canceled request and stop work that respects cancellation.
  Passing a context does not forcibly stop arbitrary listener code.
  Work that must outlive the caller needs
  its own context and, usually, a queue.
- Never block on something the publisher holds. A listener that waits for a
  lock or a transaction the publisher is still holding will wait forever.

### Inside a transaction

When the publisher is running inside a database transaction and passes its
context on, a listener that uses that context joins the transaction. Its writes
commit or roll back together with the publisher's, which is what you want when
the listener maintains related data:

```go
_, err := bus.Listen(invoice.Paid, func(ctx context.Context, e invoice.PaidEvent) error {
	return ledger.Record(ctx, e.InvoiceID, e.Cents)
})
```

A goroutine started by the listener must not use that context after the
transaction ends: its queries fail with `sql.ErrTxDone`. For effects that must
not happen if the transaction rolls back, such as sending an email, register
them with `db.AfterCommit`, described in
[Transactions](../database/transactions.md#running-code-after-commit). The
`database.transaction.committed` event is published after the commit, so its
listeners run outside the finished transaction.

## Concurrency

The bus is safe for concurrent use, and the same listener can run on several
goroutines at once when events are published concurrently, as they are in an
HTTP server. A listener that updates shared state needs its own
synchronization:

```go
type counter struct {
	mu    sync.Mutex
	count int
}

func (counter *counter) onPaid(_ context.Context, _ invoice.PaidEvent) error {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	counter.count++

	return nil
}
```

Listeners may publish further events and register or remove listeners, since
delivery works from a snapshot and holds no lock while a listener runs. Avoid
chains where a listener for an event publishes the same event, which recurses
until the stack is exhausted.

For every function and option, see [the `event` API reference](https://pkg.go.dev/arkoren.dev/core/event).
