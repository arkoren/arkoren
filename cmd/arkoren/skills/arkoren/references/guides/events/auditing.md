---
title: Event Auditing
nav: Auditing
description: Separate optional observations from required audit records that commit with application changes.
order: 40
summary:
  - Define minimal application facts with explicit actor and occurrence time.
  - Record optional observations without confusing them with durable audit guarantees.
  - Require transactional records and persist pending work before forwarding it elsewhere.
---

# Event Auditing

An audit record explains which actor changed which resource and when. Arkoren's
event bus can connect that application fact to a recording function, but Arkoren
does not provide an audit store or make notifications durable. Your application
owns the record format, access policy, retention, and persistence boundary.

Choose the requirement before choosing how to publish. An optional diagnostic
trail can tolerate a lost record and use `Emit`. If every committed change must
have a record, write that record in the same database transaction as the change.
A direct insert is often the clearest dependency; `Dispatch` is an alternative
when your application deliberately expresses that dependency through listeners.

## Defining Facts

For an invoice void, capture the identifiers and occurrence time where the
application authorizes and performs the action. In your invoice package, import
`time` and `arkoren.dev/core/event`:

```go
type VoidedEvent struct {
	InvoiceID string
	ActorID   string
	At        time.Time
}

var Voided = event.NewTopic[VoidedEvent]("invoice.voided")
```

An occurrence time in the payload remains meaningful if processing happens later.
Set `ActorID` from the authenticated, authorized application identity—not an
arbitrary request field. Background work needs an explicit trusted actor too;
it cannot assume the original request's identity remains in a worker context.

Keep secrets and unnecessary personal data out of the payload. Stable identifiers
can still identify people, so restrict audit access and retention. An event name
or typed struct does not establish authorization or sanitize its fields.

## Optional Observations

For an optional local trail, register a callback that selects only approved
fields. Here `invoice` is your package above, `bus` is the shared bus, `context`
is imported, and `logger` is your existing `*slog.Logger`:

```go
_, err := bus.Listen(invoice.Voided, func(ctx context.Context, fact invoice.VoidedEvent) error {
	logger.InfoContext(ctx, "invoice void observed",
		"invoice_id", fact.InvoiceID,
		"actor_id", fact.ActorID,
		"at", fact.At)

	return nil
})
```

After the application establishes that the invoice was voided, publish its fact:

```go
bus.Emit(ctx, invoice.Voided, invoice.VoidedEvent{
	InvoiceID: "invoice-2048",
	ActorID:   actor.ID,
	At:        time.Now(),
})
```

Here `actor.ID` is the already-authorized string identifier. The observer runs
before `Emit` returns. Its error or panic is logged rather than returned, and
later listeners are skipped. A process crash can lose the observation. A log
line also does not prove that an external collector durably stored it.

If publication occurs inside a transaction, the fact is still provisional:
rollback remains possible. Use [after-commit hooks](../database/transactions.md#after-commit)
for optional observations that must not describe rolled-back changes, or use the
required-record pattern below. Do not infer commit merely from the event's name.

## Required Records

A required row must commit or roll back with the invoice change. Assume your
application has an `invoice_audit` table, a configured `*database.Database` named
`db`, and an application-owned `audit.Insert(ctx, fact)` function that inserts
the fact into that table using this database and the supplied context. It must
return any insert failure; it must not use a separate connection or detached context.

Register that insert as the required callback:

```go
_, err := bus.Listen(invoice.Voided, func(ctx context.Context, fact invoice.VoidedEvent) error {
	return audit.Insert(ctx, fact)
})
```

Check registration before accepting writes. Your application-owned
`invoices.Void(ctx, id)` below authorizes and changes the invoice using the same
database; it returns an error if the change cannot be performed. Keep the
failure branches that determine rollback:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := invoices.Void(ctx, "invoice-2048"); err != nil {
		return err
	}

	return bus.Dispatch(ctx, invoice.Voided, invoice.VoidedEvent{
		InvoiceID: "invoice-2048",
		ActorID:   actor.ID,
		At:        time.Now(),
	})
})
```

The listener receives the transaction's callback context, so its compatible
database insert joins the transaction. A failed insert returns through
`Dispatch` and the transaction body, preventing commit. A listener panic
propagates rather than being swallowed. `Emit` cannot enforce this requirement:
it hides callback errors from the publisher. Nor does `Dispatch` verify that a
listener exists—without the required registration it succeeds without a record.
Keep wiring and tests responsible for that invariant, or call the insert directly.

Do not perform remote calls inside the transaction listener: the publisher waits
while holding transaction resources and locks. Do not use its callback context
after the transaction ends. The database's committed event runs outside the
finished transaction and is too late to add an atomic audit row.

The enclosing transaction can return `database.ErrAfterCommit` after a confirmed
commit if cleanup or an after-commit hook fails. Do not retry the transaction on
that result: the change and audit row can already be committed. Transaction
ownership and recovery stay in [Transactions](../database/transactions.md).

## Durable Forwarding

If an audit destination is remote, write a pending record in the same transaction
and have an application-owned worker forward it. This durable pending record is
often called an **outbox**. The worker needs an explicit identifier, occurrence
time, and approved actor/resource fields; it cannot rely on the former request's
context. Make forwarding retry-safe and define deduplication at the destination.

A queue call from an ordinary event listener is only a best-effort handoff. An
after-commit queue call avoids sending rolled-back changes, but a crash between
commit and dispatch can still lose it. Neither gives exactly-once delivery.
[Queues](../queues/overview.md) provide worker transport; atomic record creation,
forwarding policy, and retention remain your application's responsibility.

## Built-in Observations

[Built-in Events](built-in-events.md) can supplement an operational trail but use
best-effort publication and cannot enforce your required audit row. Match the
observation to the claim: payment call completion is not settlement, mail/push
acceptance is not delivery, and secret notifications do not cover console or
other-process changes. Session login changes request-local state before response-time
persistence; it is not evidence that a cookie or stored session reached the client.

Some built-in payloads contain raw diagnostics or identifying data. A broad
[pattern listener](listeners.md#pattern-listeners) should not forward whole
payloads or contexts. Select fields under your privacy policy, and use the
[shared telemetry handle](../observability/telemetry.md#shared-handle) for correlation
without treating a trace as a required business record.

## Checking Guarantees

Use an isolated `event.New()` bus to test publication and listener behavior.
Delivery is synchronous, so a sequential test can assert collected payloads
immediately after the operation returns; no polling is needed. Concurrent tests
must synchronize shared collectors.

For required records, test the application boundary, not only callback execution:
an insert failure must roll back the invoice change, successful commit must retain
both rows, and missing registration must fail your wiring check. Test forwarding
retries and destination deduplication separately. A bus test proves local callback
behavior, not database durability or external delivery.

For dispatch behavior, see the [`event` API reference](https://pkg.go.dev/arkoren.dev/core/event).
