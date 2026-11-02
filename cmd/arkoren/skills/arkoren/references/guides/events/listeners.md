---
title: Writing Listeners
nav: Listeners
description: Register local observers, own their removal, and keep failure and concurrency behavior explicit.
order: 20
summary:
  - Register typed, exact-name, or pattern listeners for the observations you need.
  - Control failure propagation and remove registrations with their owning scope.
  - Keep synchronous callbacks safe around concurrency, contexts, and transactions.
---

# Writing Listeners

A listener lets your application observe an operation without coupling the
publisher to its logging or monitoring code. It runs inside the publishing call,
not on a worker. Start with the [shared bus](overview.md#choosing-a-bus), register
before publication can begin, and keep the callback short.

## Typed Listeners

To observe uploaded object sizes, listen to `storage.ObjectWritten`. Import
`context`, `arkoren.dev/core/service/storage`, and use your existing bus and
`*slog.Logger`:

```go
unsubscribe, err := bus.Listen(storage.ObjectWritten,
	func(ctx context.Context, notice storage.ObjectWrittenEvent) error {
		logger.DebugContext(ctx, "object stored", "bytes", notice.Object.Size)

		return nil
	})
```

The callback receives `storage.ObjectWrittenEvent` without a type assertion.
`Listen` returns `event.ErrInvalidName` for an invalid topic name and
`event.ErrNilFunc` for a nil callback. Check registration errors during wiring;
do not call the returned removal function after failed registration.

Topics route by name, so a typed listener skips a nonnil payload with the wrong
type. Reuse the publisher's topic variable instead of redeclaring its name.
For a signal such as `application.Started`, the callback's second parameter is
`event.Void`; import those packages from `arkoren.dev/core/application` and
`arkoren.dev/core/event`.

## Name Matching

Use an exact string only when the name comes from configuration or another
boundary where the typed topic is unavailable. `ListenEvent` receives `any`;
inspect a payload's type before reading its fields. A name-only observer needs
no assertion:

```go
unsubscribe, err := bus.ListenEvent("storage.object.written",
	func(ctx context.Context, _ any) error {
		logger.DebugContext(ctx, "storage write observed")

		return nil
	})
```

It has the same name and nil-function validation as typed registration.
Prefer the typed form when your callback needs payload fields.

### Pattern Listeners

To count or log several related topics, use `ListenRegex` with `regexp` from the
standard library. This observer selects storage topics and forwards only names:

```go
unsubscribe, err := bus.ListenRegex(regexp.MustCompile(`^storage\.`),
	func(ctx context.Context, name string, _ any) error {
		logger.DebugContext(ctx, "storage observation", "event", name)

		return nil
	})
```

Patterns use `MatchString`, not whole-name matching. Without `^`, `storage` also
matches `app.storage-check`; add `$` when the end matters. A nil pattern or
callback returns `event.ErrNilFunc`.

Do not log whole payloads merely because a pattern matches. Some built-in events
carry raw errors, identifiers, or panic stacks. Select approved fields explicitly;
payload omissions do not redact the caller's context or returned error chains.

## Failure Ordering

Exact-name callbacks run in registration order before all matching pattern
callbacks, which also run in registration order. The first error stops delivery.
With `Emit`, the bus logs it and returns to the publisher; with `Dispatch`, the
error reaches the publisher. A panic similarly stops delivery: `Emit` recovers
and logs it, while `Dispatch` lets it propagate.

Returning an error is therefore not an isolated failure report: it prevents later
observers from receiving that event. If an optional observer can handle its own
failure, return nil afterward. Avoid retry loops or remote requests in that
callback, since the publisher waits for them. Keep dependent steps together
rather than relying on independently registered observers for correctness.

## Removing Listeners

Every successful registration returns an `event.Unsubscribe`. Call it when the
registration's owning scope ends. For a short-lived test or operation, use your
existing typed `handler` and topic:

```go
unsubscribe, err := bus.Listen(storage.ObjectWritten, handler)
if err != nil {
	return err
}
defer unsubscribe()
```

Repeated removal is safe and removes only that registration, even if the same
function was registered more than once. Delivery uses a snapshot: removal does
not stop a callback already selected, and new registrations do not join the
current snapshot.

For a composition-owned listener, give removal to the composer rather than
deferring it until the composition function returns:

```go
composer.MustDefer(bus.Listen(storage.ObjectWritten, handler))
```

This checks registration and removes the listener on rollback or shutdown, as
described in [subscription ownership](../application/composition.md#own-subscriptions).
Process-owned registrations can remain for their bus's lifetime. Repeated
registration in a request handler without removal accumulates callbacks and
repeats each observation.

## Shared Execution

The same callback can run concurrently when different goroutines publish. A
listener that changes shared state must use its own synchronization. Payload
values are not deeply copied: copy slices, maps, or pointed-to data before
mutating or retaining them when their publisher's contract requires it.

Callbacks receive the publisher's context, including its deadline, cancellation,
and any transaction scope. Respect cancellation, but do not assume that passing
a context forcibly interrupts arbitrary callback code. Do not wait for a lock or
transaction the publisher is holding; the publisher cannot proceed until you return.

A listener using the callback context of a compatible database transaction joins
that transaction. Its writes commit or roll back with the caller's work, but
`Emit` cannot enforce rollback if that listener fails. Required records need
[transactional auditing](auditing.md#required-records). The committed-transaction
event itself runs after commit, outside the finished transaction.

For an external effect that must wait for commit, use
[after-commit hooks](../database/transactions.md#after-commit). These hooks are
not durable. Queueing there still leaves a crash window before enqueueing;
persist pending work in the transaction if loss is unacceptable. Never retain
the transaction's callback context for use after it ends.

The bus holds no registration lock while callbacks run, so a listener can publish
or change registrations. Avoid publishing a topic recursively from its own
listener: that recurses synchronously rather than scheduling later work.

For all functions and options, see the [`event` API reference](https://pkg.go.dev/arkoren.dev/core/event).
