---
title: Consuming Jobs
description: Register handlers, run a consumer, choose its queues, and control retry, reject, and permanent failure, including timeouts and shutdown.
order: 30
---

# Consuming Jobs

## Introduction

Once an upload request has queued a resize, a worker needs to load the image and
produce the thumbnail. The consuming side of a queue is a `*queue.Consumer`.
You register one handler
per job, and `Run` pulls messages from the driver, finds the handler for each,
runs it, and settles the message according to what the handler returned. The
consumer decodes the payload, applies the job's retry limit and backoff to the
attempt count reported by the driver, and recovers handler panics. Its timeout
cancels the handler's context; it does not kill the handler. Settlement waits
until your code returns.

## Registering handlers

The producer has described the work and sent a payload; the worker still needs
to know which application function should do it. Here `driver` is your
[configured queue driver](drivers.md), `ResizeImageJob` is the definition from
[Producing Jobs](producing-jobs.md#defining-a-job), and `images` is your
application's image service. Register its operation with `Handle`, which passes
it the decoded payload:

```go
import "arkoren.dev/core/service/queue"

consumer := queue.NewConsumer(driver)

err := consumer.Handle(ResizeImageJob, func(ctx context.Context, job ResizeImage) error {
	return images.Resize(ctx, job.ImageID, job.Width)
})
```

The payload is a fresh value on every attempt, decoded from the message, so a
handler can modify it freely. The context carries the job's timeout, the trace
of the dispatching request when telemetry is on, and the cancellation of the
consumer. Pass it to every call that can block.

`Handle` returns `queue.ErrInvalidName` for an invalid job and
`queue.ErrDuplicateName` when you register a second handler for one name, since
the name selects the handler. Register all handlers before `Run`: it reads the
queue list once when it starts. A handler added later for a new queue needs
another run; a new handler on an already consumed queue handles subsequent
deliveries for its name.
Registering everything first avoids either case depending on timing.

## Running the consumer

Registering a handler does not start reading the queue. The consumer needs to
keep waiting for messages while your service is running. Register `Run` as an
application runner so startup and shutdown own that lifetime; it blocks until
its context ends or the driver fails. With the application from the
[lifecycle example](overview.md#wiring-into-the-application), pass this callback
to `app.Run(ctx, compose)`:

```go
compose := func(composer *application.Composer) {
	composer.Run(consumer.Run)
}
```

It starts one `Consume` call per queue that has a handler and works through each
queue's messages one at a time. A single process therefore runs one job per
queue at a time. To process more in parallel, run more worker processes: they
compete for the messages of a queue, and each message goes to one of them.
Some drivers fetch ahead, with a prefetch or batch setting, but the consumer still
handles one delivery after the other.

If any queue's `Consume` fails, `Run` cancels the others, waits for them, and
returns the first error. Run by an application, that error stops the service,
which is what you want from a worker that lost its broker. With no handler registered,
`Run` returns `queue.ErrNoQueues`.

### Choosing which queues to consume

By default a consumer reads every queue that has a handler. A worker that should
serve only some queues, such as a dedicated worker for slow media jobs, restricts
itself with `ConsumerBuilder.Queues`:

```go
consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
	consumer.Queues("media")
})
```

Handlers for other queues may stay registered; they are skipped. If the
restriction leaves nothing to read, `Run` returns `queue.ErrNoQueues`. This lets
one binary carry all handlers and be started as a web server, a general worker,
or a media worker through configuration. This facade restriction does not change
the driver's routing map or declare broker queues.

Each `Queues` call replaces the previous restriction rather than appending to
it. `Queues()` removes the restriction, restoring all handled queues.
Callbacks run directly once, synchronously in order on one fresh draft; later
settings replace earlier ones. Supplied nil callbacks panic, while expanded nil
or empty callback slices omit configuration.

`Queues` copies its input, and `NewConsumer` copies it again at publication.
You can reuse a policy callback across consumers; later changes to an input
slice or a retained draft cannot change published queue restrictions.
Do not mutate drafts concurrently with construction. Telemetry and event-bus
pointers remain shared services, not copied objects.

## Handler outcomes

Most handlers return `nil` for completed work or an ordinary error for work that
should be tried again. The consumer translates that result into a settlement
request to the driver:

| Handler returns | The consumer requests |
| --- | --- |
| `nil` | Acknowledgement. |
| An ordinary error | Retry after the job's backoff, or terminal failure once its attempts are used up. |
| `queue.RetryAfter(err, d)` | Retry after `d` instead of the job's backoff, subject to its attempt limit. |
| `queue.Reject(err)` | Rejection without a facade retry; the driver's rejection policy decides whether it returns. |
| `queue.Permanent(err)` | Terminal failure without a facade retry, whatever attempts remain. |

An acknowledgement or retry can itself fail; the message may remain available
for redelivery according to the driver's policy. These are settlement failures,
not a request to run the application's handler again immediately. Terminal
failure and rejection errors are also returned to the driver without losing
their causes; they are not converted into successful outcomes. With RabbitMQ,
such an error ends `Consume` and closes its channel to release unsettled jobs;
`Run` then cancels its other queue consumers and returns after they stop. See
[RabbitMQ's transfer and retry limits](drivers.md#rabbitmq) before
relying on a retry. Keep handlers idempotent even after they return `nil`.

For ordinary errors, `Retry(n)` permits `n` total attempts, including the first.
Zero, the default, sets no facade attempt limit; the driver's redelivery policy
then decides the limit. `RetryAfter` still respects a configured attempt limit.

The other outcomes let the handler choose a more specific policy. `RetryAfter`
suits an upstream that told you when to come back. Here `job` is your invoice
payload with an integer `InvoiceID`, and `client.Charge` and `rateLimited` belong
to your payment integration. The latter recognizes a rate-limit error whose
response includes a retry delay:

```go
resp, err := client.Charge(ctx, job.InvoiceID)
if rateLimited(err) {
	return queue.RetryAfter(err, resp.RetryAfter)
}
```

The attempt limit still applies to it. Negative `RetryAfter` delays become zero,
which selects the driver's default timing. A positive delay is a lower-bound
request: a driver can round it up or reject an unsupported value, not silently
shorten it. Check the [driver's retry bounds](drivers.md) when choosing a delay.
`Permanent` is for errors that no retry fixes, like a payload that
refers to a record that was deleted. It wraps the error and still matches the
original under `errors.Is`, and it also matches `queue.ErrPermanent`:

```go
if errors.Is(err, store.ErrNotFound) {
	return queue.Permanent(fmt.Errorf("invoice %d: %w", job.InvoiceID, err))
}
```

`Permanent(nil)` intentionally returns a non-nil terminal outcome, not success.
Check a fallible operation's error before wrapping it; return `nil` when the
operation succeeds. `RetryAfter` and `Reject` also accept nil causes while still
requesting their respective outcomes.

`Reject` gives the message back as unwanted. How a broker treats a rejection
differs more than anything else between drivers: RabbitMQ drops it unless the
queue has a dead-letter policy, NATS terminates it, SQS leaves it in the queue
for its redrive policy to move after enough receives, and Cloudflare delivers it
again when its lease ends. Prefer `Permanent` unless you have configured the
broker side, and read [Queue Drivers](drivers.md) first.

### When a job fails for good

A job fails for good when a handler returns a permanent error, when it fails
its last attempt, when its payload does not decode, or when no handler takes its
name after the grace period below. The consumer then asks the driver to fail the
message: the broker drivers archive it in a failure queue or on a failure
subject when you configure one, and the memory and Cloudflare drivers log it.

With `consumer.Events(bus)` in its construction callback, the consumer publishes
`queue.JobFailed` only after `Delivery.Fail` returns success. Its `JobFailedEvent`
carries the message ID, job name, queue, attempt count, and error. Accepted retries
and rejections publish their own events, `queue.job.retried` and
`queue.job.rejected`. A failed terminal settlement publishes no `JobFailed`; a
rejection, successful or failed, never publishes it. Alert or record accepted
terminal failure here:

```go
_, err := app.Events().Listen(queue.JobFailed,
	func(ctx context.Context, e queue.JobFailedEvent) error {
		alerts.Notify(ctx, "job failed: "+e.Name, e.Err)

		return nil
	})
```

Listeners run synchronously on the consumer's goroutine, so keep them short.
`JobFailed` confirms the driver's accepted terminal settlement, not an archive's
durability or reversal of the handler's external effects. Settlement failure is
instead returned to the driver, whose error policy determines whether consumption
ends or continues. Neither a missing event nor a settlement error proves that
the broker rejected every part of the operation.
`queue.job.retried` carries the delay the driver was asked to wait, which shows a
job that keeps coming back before it fails for good; it does not guarantee the
next execution time. Shutdown retries and unknown-name retries do not publish
that event. [Built-in events](../events/built-in-events.md#queues) describes all
queue notifications.

## Timeouts, panics, and shutdown

A job's `Timeout` cancels the handler's context when the attempt runs too long.
If the handler returns an error, the attempt follows the retry or failure policy;
if it returns nil, the consumer acknowledges success even after the deadline.
A handler that ignores its context holds the worker for as long as it takes. Passing `ctx` to
every blocking call is what makes a timeout work. With `ConsumerBuilder.Events`, an
executed handler that returns an error after its timeout elapsed publishes
`queue.job.timed-out` with the timeout, unless it takes the shutdown-retry path
below. Successful settlement then publishes the retried, failed, or rejected
outcome. Any settlement error can leave the timeout
without a follow-up event.

A panic in a handler is recovered as an ordinary error, with the stack in the
error text. It follows the same retry and attempt-limit policy rather than
escaping as a worker crash. A settlement failure can still stop `Run`.

Shutdown cancels the context the handler receives. When the handler returns an
error that matches the context's error, for example by returning `ctx.Err()` or
wrapping it with `%w`, the consumer requests a retry without checking the job's
attempt limit or applying its backoff. The requested delay is zero, so the
driver's default timing applies. The driver's delivery count still advances and
is not discounted: an ordinary failure on a later delivery can reach the job's
attempt limit. A handler that hides the cause behind a different error follows the
ordinary attempt-limit and backoff rules instead. The consumer settles
every delivery on a context that is detached from the shutdown, with a 30-second
limit starting after decoding and handler execution finish. Unknown jobs receive
the same fresh budget without execution. Shutdown cancellation alone therefore
does not abort an acknowledgement for work that just finished. Settlement can
still fail.
During normal coordinated shutdown, the application joins runners before cleanup
releases their drivers. If the shutdown budget expires first, cleanup can release
the transport while a handler or settlement is still running. Allow time for
handler completion and settlement; a detached context does not keep a connection
alive or make a borrowed SDK RPC interruptible.

### Canceling an in-memory consumer

With `memoryqueue`, consumer cancellation stops admission of the next delivery,
even when a backlog is ready. It does not revoke the delivery already handed to
the active handler. That handler can finish, and the facade can acknowledge or
retry it using the detached settlement budget above. Remaining jobs, including
a successful retry, stay queued for a live consumer rather than being drained by
the canceled one.

Stopping the memory driver is not a drain operation. `Stop` prevents admissions
and discards both ready jobs and delayed retries, even with an expired stop
context. It waits for active handlers only until that context ends. An admitted
delivery can still be acknowledged afterward, but a retry into the stopped driver
fails with `memoryqueue.ErrDriverClosed`. Cancel and join your consumers before
stopping the driver if their final settlement must complete first. Use a
[broker-backed driver](drivers.md) when pending work must survive process shutdown.

## Unknown jobs and rolling deploys

During a rolling deploy, a message can reach a worker that does not yet have the
handler for it, or a worker running old code can receive a job it never knew. The
consumer requests a retry with a 30-second delay to give other workers a chance
to take it, rather than immediately discarding it. It requests terminal failure
with `queue.ErrUnknownName` once the delivery count reaches 10. Change the limit with
`ConsumerBuilder.UnknownNameAttempts(n)`; zero selects the default of 10, and a
negative value removes the facade limit. The broker may still impose its own
limit. The facade limit relies on the driver reporting attempts, which all
shipped drivers do.

## Writing reliable handlers

Do not assume one execution or guaranteed eventual delivery. A handler can run
again after its effects succeeded but could not be acknowledged; retention and
retry-forwarding policies can also discard work. Check the
[driver guarantees and limits](drivers.md#compatibility-matrix) and make repeats
harmless: use a stable identifier in the payload and enforce uniqueness where the
effect is stored.
For database-only work, record completion in the same transaction as the change.
For a remote API, use the provider's idempotency key when available.

Marking an invoice as sent before sending its email is not enough: a failed send
can leave a marker that suppresses every retry. Marking it afterwards has the
opposite gap: a crash after acceptance but before the marker can send it twice.
Choose a recovery policy for that boundary; a local flag does not make the
remote send atomic. See [mail retry behavior](../mail/overview.md#receipts-and-errors).

Return errors instead of swallowing them, since a swallowed error acknowledges a
job that did not finish. Keep side effects that cannot be undone at the end of
the handler, after the checks that could fail it. Finally, remember the lease.
SQS and NATS attempt renewal while a handler runs, but renewal or connection
failure can permit concurrent redelivery. A Cloudflare handler must finish within
the visibility timeout or the job is delivered again to another worker.
[Queue Drivers](drivers.md) lists the behavior of each driver.

For every function and option, see [the `queue` API reference](https://pkg.go.dev/arkoren.dev/core/service/queue).
