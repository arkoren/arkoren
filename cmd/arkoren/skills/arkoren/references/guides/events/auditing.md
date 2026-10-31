---
title: Auditing with Events
nav: Auditing
description: Record application facts with listeners and distinguish best-effort trails from required durable audit records.
order: 40
---

# Auditing with Events

## Introduction

An audit trail answers questions after the fact: who changed this invoice, when
did this account last sign in, which requests were turned away. Every entry
records a fact that already happened, the person or system behind it, and the
moment it happened. The same facts often feed analytics too, where they are
counted and sliced instead of read one by one.

Arkoren has no audit package and no audit table. A fact is an
[event](overview.md): you define a topic for it, publish it where it happens,
and write a listener that records it wherever you keep your trail, such as a
log, a table in your own database, ClickHouse, or a collector like Vector.
Where the trail lives and what shape its records take stay your decision, and
the code that does the work never learns that anything is being recorded.

Ordinary `Emit` listeners provide best-effort observations: listener failure or
a process crash can lose a record. If a record is mandatory, persist it in the
same transaction as the change or use an application-owned durable handoff.
The examples below distinguish these requirements before using either path.

Arkoren's own services already publish what they decide and perform, such as a
blocked CSRF request, a denied gate rule, or a deleted object, and
[Built-in Events](built-in-events.md) lists them. Facts that belong to your
application, such as a user signing in, are events you define yourself, and
this page walks through one from start to finish.

## The flow

Every audited fact goes through the same four steps:

1. **Define a topic** for the fact, with a payload that names what happened to
   which subject.
2. **Publish it** from the code that knows the fact happened, after it
   happened.
3. **Listen to it** once, during wiring, with a listener that turns the event
   into a record: who, what, and when.
4. **Store the record** in the listener, or hand it to a queue when the
   destination is slow or far away.

The example below follows these steps for a login. It builds on the handler
from [Session Login](../authentication/session-login.md), keeping only the
authentication decision and the events it publishes.

## Defining the topics

Start with the facts. A login can succeed or fail, and each outcome is its own
event, declared next to the account code as package-level variables:

```go
import (
	"time"

	"arkoren.dev/core/event"
)

// LoggedInEvent is the payload of [LoggedIn].
type LoggedInEvent struct {
	UserID int
	At     time.Time
}

// LoginFailedEvent is the payload of [LoginFailed]. It holds no email
// address or password: only the reason the attempt was refused.
type LoginFailedEvent struct {
	Reason string
	At     time.Time
}
```

In your `account` package, publish typed topics for those payloads:

```go
var (
	LoggedIn    = event.NewTopic[LoggedInEvent]("account.logged-in")
	LoginFailed = event.NewTopic[LoginFailedEvent]("account.login-failed")
)
```

The payload carries the subject of the fact, here the user who signed in, and
the time it happened. Record the time in the payload rather than in the
listener: a listener may hand the event to a queue, and the record should keep
the moment of the login, not the moment a worker got to it.

Keep payloads free of secrets and of more personal data than the trail needs.
A failed login holds the reason, not the email address that was typed, since
an audit log is read by more people than the users table, and
[pattern listeners](listeners.md#listening-by-pattern) make it easy to send
every event to a log verbatim.

## Publishing the event

Publish from the login handler, after the outcome is known. With credentials
already parsed into `in`, keep failed authentication separate from success.
Here `users` is your user service, `users.identity` is the shared
`auth.NewSessionIdentity(users.Load)` definition used by middleware, and `bus`
is the application's event bus:

```go
user, err := users.authenticate(r.Context(), in.Email, in.Password)
if errors.Is(err, auth.ErrUnauthenticated) {
	bus.Emit(r.Context(), account.LoginFailed,
		account.LoginFailedEvent{Reason: "invalid credentials", At: time.Now()})
}
if err != nil {
	return err
}

if err := users.identity.Login(request.MustSession(r), user.ID); err != nil {
	return err
}
bus.Emit(r.Context(), account.LoggedIn,
	account.LoggedInEvent{UserID: user.ID, At: time.Now()})
```

A successful login decision is published only after `identity.Login` updates the
in-request session. The session middleware persists it when writing the response,
so this event does not prove that session storage or cookie delivery succeeded.
`Emit` never fails: if a listener returns an error or
panics, the bus logs it and the login carries on, so a broken audit log cannot
lock people out. Use `Dispatch` instead only when a fact that is not recorded
must stop the operation, which [Events Overview](overview.md#emitting-events)
describes.

Pass the request's context, as above. The listener receives it unchanged,
which is how it learns the rest of the story: the signed-in user, the trace,
and any transaction the handler is running.

## Listening and logging

Register the listener once, while wiring the application. The simplest trail
is a structured log line per fact:

```go
bus := app.Events()
logger := app.Logger()

_, err := bus.Listen(account.LoggedIn, func(ctx context.Context, e account.LoggedInEvent) error {
	logger.InfoContext(ctx, "audit",
		"event", account.LoggedIn.Name(),
		"user_id", e.UserID,
		"at", e.At,
	)

	return nil
})
```

Each successful login now writes one line such as
`level=INFO msg=audit event=account.logged-in user_id=7 at=2026-10-14T09:12:03Z`.
A log is already enough for many trails: a collector such as Vector or Fluent
Bit can read the lines and forward them to ClickHouse, Loki, or object storage
without the application knowing.

The listener returns `nil` even if it could not record the fact. Returning an
error stops the listeners registered after it from seeing the event, so return
one only when stopping delivery is the point; see
[Order and failure](listeners.md#order-and-failure).

### Who did it

The login event names its subject in the payload because nobody is signed in
yet while the handler runs. Most facts happen later, to a user who is already
signed in, and the listener reads that user from the context:

```go
_, err := bus.Listen(invoice.Voided, func(ctx context.Context, e invoice.VoidedEvent) error {
	actor, _ := auth.FromContext[User](ctx)

	logger.InfoContext(ctx, "audit",
		"event", invoice.Voided.Name(),
		"invoice_id", e.InvoiceID,
		"actor_id", actor.ID,
		"trace_id", telemetry.SpanFromContext(ctx).Context().TraceID.String(),
	)

	return nil
})
```

`auth.FromContext` returns the user the
[auth middleware](../authentication/overview.md) stored for the request, and
`false` when the request is anonymous or the event was published outside a
request. The trace ID links the record to the request's trace when
[telemetry](../observability/telemetry.md) is on, so the full path of a
request, from the handler to every query and job, is one lookup away. Without
telemetry the ID is all zeros and can be left out.

## Storing records in your database

A required audit row needs to commit or roll back with the change it records.
When the publisher runs inside a
[transaction](../database/transactions.md) and passes its context on, a
listener that writes with that context joins the transaction, so the change and
its audit row commit or roll back together. Use `Dispatch` and return its error
from the transaction body to make a failed insert prevent the change from
committing:

```go
_, err := bus.Listen(invoice.Voided, func(ctx context.Context, e invoice.VoidedEvent) error {
	actor, _ := auth.FromContext[User](ctx)

	_, err := db.Exec(ctx, query.Table("invoice_audit", func(statement *query.Builder) {
		statement.Insert(map[string]any{
			"invoice_id": e.InvoiceID,
			"action":     invoice.Voided.Name(),
			"actor_id":   actor.ID,
			"at":         e.At,
		})
	}))

	return err
})
```

The table and its columns are yours: one table for every fact, one per area
such as `invoice_audit` and `account_audit`, or a hypertable in TimescaleDB,
created with the [schema builder](../database/schema-builder.md) and
[migrations](../database/migrations.md) like any other table.

Publish with `Dispatch` when the audit row is part of the change. The insert's
error then reaches the publisher, which returns it, and the transaction rolls
back, so no change is committed without its record. With `Emit`, the bus only
logs the error; the publisher cannot use it to require a rollback. A database
error may still abort the transaction, but `Emit` is not a reliable way to
enforce the audit requirement.

> [!WARNING]
> A listener that joins a transaction holds it open while it runs. Keep the
> insert to one statement, and never call a remote service from the listener:
> a slow analytics store would hold row locks for every audited change.

## Sending records elsewhere

ClickHouse, a collector over HTTP, or a third-party analytics service is too
far away to call while the publisher waits. Hand the record to a
[queue](../queues/overview.md) instead, and let a worker send it with retries:

```go
var RecordAuditJob = queue.NewJob[AuditRecord]("record-audit")

_, err := bus.Listen(account.LoggedIn, func(ctx context.Context, e account.LoggedInEvent) error {
	return dispatcher.Dispatch(ctx, RecordAuditJob, AuditRecord{
		Event:  account.LoggedIn.Name(),
		UserID: e.UserID,
		At:     e.At,
	})
})
```

Here `dispatcher` is your configured queue dispatcher, and `AuditRecord` is
your own payload type with `Event`, `UserID`, and `At` fields. Register its job's
worker handler as described in [Consuming Jobs](../queues/consuming-jobs.md).

The worker's handler receives the `AuditRecord` and writes it to the store, in
batches if the store prefers them. Since the record carries the user and the
time, it no longer depends on the request that produced it.

A queue job does not carry the request's signed-in user, so a fact published
from inside a job cannot read it with `auth.FromContext`. Put the identifiers
the trail needs in the job's payload when you dispatch it, the way
`AuditRecord` carries `UserID` above.

If the publisher runs in a transaction, dispatch from `db.AfterCommit` so a
rolled-back change sends nothing; see
[Running code after commit](../database/transactions.md#running-code-after-commit).
This prevents publishing rolled-back changes, but does not make the handoff
atomic: a crash after commit and before dispatch can still lose the record.
If losing it is unacceptable, store the pending record in the transaction and
have a worker forward it.

## Auditing Arkoren's own events

Arkoren's services report what they do on the same bus once you hand it to
them through their builder's `Events` setting, and [Built-in Events](built-in-events.md) lists each
topic with its payload.

Built-in notifications use best-effort `Emit`: a listener failure or process
crash can leave the trail incomplete. They cannot enforce the transactional
audit requirement described above. Payment call completion does not prove
settlement, push acceptance does not prove delivery, and secret mutation
notifications do not cover provider-console or other-process writes. Record
required business facts at an application-owned durable boundary instead.

A [pattern listener](listeners.md#listening-by-pattern) adds a whole area to
the trail at once, here every request the HTTP layer turned away:

```go
_, err := bus.ListenRegex(regexp.MustCompile(`^http\.(csrf|ratelimit|contenttype|auth|gate)\.`),
	func(ctx context.Context, name string, _ any) error {
		logger.WarnContext(ctx, "audit", "event", name)

		return nil
	})
```

Arkoren's built-in fields describe the operation, such as the method and path of
a blocked request, and omit tokens, session IDs, request bodies, and the user.
Error text can still quote sensitive provider responses; redact it before
forwarding. Read the user from the context, as in [Who did it](#who-did-it), when
the trail needs one. Events for rejected requests fire at whatever rate
clients send them, so keep these listeners cheap and put a
[rate limit](../abuse-prevention/rate-limiting.md) in front of the routes.

## Testing

A test builds its own bus with `event.New()` and gives it to your user service.
With `t *testing.T`, that `bus` and `users`, a login request `req` carrying valid
credentials for user 7, and a response recorder `rec`, collect the event before
calling the login handler. The request needs the session middleware's context,
as described in [testing authentication](../testing/auth-tests.md):

```go
var got []account.LoggedInEvent
_, err := bus.Listen(account.LoggedIn, func(_ context.Context, e account.LoggedInEvent) error {
	got = append(got, e)

	return nil
})
if err != nil {
	t.Fatal(err)
}

if err := users.login(rec, req); err != nil {
	t.Fatal(err)
}

if len(got) != 1 || got[0].UserID != 7 {
	t.Fatalf("LoggedIn events = %+v, want one for user 7", got)
}
```

Listeners run synchronously in the publisher's goroutine, so the event has
been delivered by the time the handler returns, and no waiting is needed.
Test the listener on its own the same way: call it with a context and a
payload, and check the line, row, or job it produced.

For every function and option, see [the `event` API reference](https://pkg.go.dev/arkoren.dev/core/event).
