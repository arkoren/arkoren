---
title: Deferred Tasks
description: Register typed one-off handlers, defer payloads until later, and interpret submission and cancellation outcomes.
order: 40
summary:
  - Define typed task payloads and register bounded handlers before startup.
  - Submit relative or absolute deferrals without confusing acceptance with execution.
  - Cancel by driver identifier and test submitted payloads separately from handler effects.
---

# Deferred Tasks

A reminder created after a user action needs one future execution, not a
recurring calendar entry. A **task** fixes the name and payload type for that
work. Register its handler on a scheduler, then submit individual jobs with
their payloads and due times.

The examples assume a [configured scheduler](drivers.md) named `scheduler`.
Memory loses jobs on restart; Temporal and Restate retain them in engine state.
Choose that durability boundary before accepting work from users.

## Defining Tasks

Keep the reminder's payload and task definition in a package shared by producers
and workers. Import `arkoren.dev/core/service/schedule`:

```go
type Reminder struct {
	AccountID string `json:"account_id"`
}

var RemindAccount = schedule.NewTask[Reminder]("remind-account")
```

The definition does not register a handler or schedule anything. Names share
the entry namespace and contain 1–64 lowercase letters, digits, or hyphens,
without edge hyphens. `Handle` validates the name; definitions using one name
must agree on their payload type.

Payloads round-trip through `encoding/json/v2`. Prefer a small identifier over
a snapshot of account data: the handler can load current state and decide
whether the reminder is still appropriate. Keep pending payloads compatible
with future worker deployments; a decode failure occurs before the handler runs.

## Registering Handlers

Before `scheduler.Run`, register the application function that sends the
reminder. Here, `sendReminder` has type `func(context.Context, string) error`.
Import `context` and `time` to bound its execution:

```go
err := scheduler.Handle(RemindAccount, func(ctx context.Context, reminder Reminder) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return sendReminder(ctx, reminder.AccountID)
})
```

Tasks have no entry-style `Timeout` setting, so bound the handler's work and
pass its context into I/O. This deadline is cooperative, not forced termination.
In particular, memory drains admitted tasks on contexts detached from runner
cancellation and has no driver drain timeout. Keep captured services alive until
handlers finish, even if the application's shutdown wait expires.

`Handle` rejects nil handlers and duplicate names. Registration closes when
`Run` starts. The facade also requires this registration before `At` or `After`,
including on a process that only submits jobs; otherwise it returns
`schedule.ErrUnknownName`. Register compatible names and handlers on all
execution workers. Only the name and JSON payload travel, never the closure.

Make the handler idempotent and recheck current business state before effects.
One-off means one submitted job, not a guarantee that its effects happen once.
[Driver retry and delivery policies](drivers.md#compatibility-matrix) differ.

## Deferring Work

After the application has validated and authorized an account action, defer its
reminder relative to the current time:

```go
id, err := scheduler.After(ctx, RemindAccount, Reminder{AccountID: "acct-42"}, 10*time.Minute)
```

Success returns a driver-chosen job identifier and means accepted for later
execution, not that the handler ran or a message was delivered. Retain the ID
if you might cancel the job. A zero or negative delay makes it due now or in
the past; the driver runs it at its next opportunity, not synchronously.

If the due instant is already known, use `At` with a nonzero `time.Time`
named `due` instead:

```go
id, err := scheduler.At(ctx, RemindAccount, Reminder{AccountID: "acct-42"}, due)
```

These are alternative submissions; calling both creates two jobs. `At` rejects
zero time. Encoding and driver errors retain their causes, and the facade does
not retry failed submissions automatically. A context or transport failure is
not proof that an engine accepted nothing; reconcile uncertain outcomes before
blindly submitting another job when duplicate effects matter.

Deferring a job and committing an application database change are separate
operations. If that boundary must be reliable, use an application-owned durable
handoff or reconciliation design rather than assuming the scheduler joins the
transaction.

Temporal needs a connected host for submission. Restate needs a registered
deployment known to ingress; its initial calendar-publication retry loop does
not apply to deferred submissions. Memory holds pre-run submissions until `Run`
and loses them on process exit. See [Drivers](drivers.md) for those readiness
and shutdown boundaries.

With events configured, successful `At` and `After` calls publish
`schedule.task.deferred` with name, ID, and due time, never the payload. This
is a submission observation, not a delivery receipt.

## Canceling Jobs

When a reminder is no longer needed, pass its returned identifier to `Cancel`:

```go
err = scheduler.Cancel(ctx, id)
```

Unknown and finished IDs succeed. A successful call therefore means the
cancellation request was accepted, not proof that pending work existed or was
removed. The configured `schedule.task.canceled` event has that same boundary.

Memory cannot interrupt an admitted dispatch. Temporal terminates the native
deferred workflow; Restate requests invocation cancellation. Neither can
forcibly stop arbitrary Go code already executing. Recheck business state in
the handler when a stale reminder would cause harm.

## Testing Submissions

To test that application code submits the right reminder without sending mail,
use `scheduletest.New()` from
`arkoren.dev/core/service/schedule/scheduletest`. It records jobs without firing
them. In a test with `t *testing.T`, construct a separate scheduler and register
an inert handler:

```go
fake := scheduletest.New()
scheduler := schedule.New(fake)
err := scheduler.Handle(RemindAccount, func(context.Context, Reminder) error {
	return nil
})
if err != nil {
	t.Fatal(err)
}
```

Invoke the application operation under test with this scheduler. For a focused
submission assertion, call `After` directly:

```go
_, err = scheduler.After(t.Context(), RemindAccount, Reminder{AccountID: "acct-42"}, 10*time.Minute)
if err != nil {
	t.Fatal(err)
}

fake.AssertDeferred(t, RemindAccount, func(reminder Reminder) bool {
	return reminder.AccountID == "acct-42"
})
```

This proves the submitted payload, not handler execution or mail delivery.
Test the handler separately with controlled application dependencies. For real
timer behavior, see [Controlling Time](../testing/time.md).

To execute due fake jobs through the scheduler, start `scheduler.Run` and then
call `fake.Drain(ctx)`. Before `Run` installs dispatch, `Drain` returns
`scheduletest.ErrNotRunning`; inside a `synctest` bubble, wait with
`synctest.Wait()` before draining. Future-due jobs remain pending until due.

For every function and option, see [the `schedule` API reference](https://pkg.go.dev/arkoren.dev/core/service/schedule).
