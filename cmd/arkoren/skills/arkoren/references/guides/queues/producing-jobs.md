---
title: Producing Jobs
description: Define jobs with a stable name, typed payload, queue, retries, backoff, and timeout, then dispatch them and know what a dispatch promises.
order: 20
---

# Producing Jobs

## Introduction

After an image upload, you may need thumbnails, but the person uploading it
should not have to wait for every resize. A job lets you hand that work to a
worker and finish the upload request first.

Producing a job has two steps: define the kind of work once, then dispatch an
instance whenever the work arises. The definition, a `queue.Job`, stays in your
application code; it never travels over the wire. The message that travels holds
the job's name, its queue, and the payload as JSON.

The consumer takes retry and timeout settings from its own copy of the job
definition, not from the message. Both sides therefore need the same definition.
Changing a setting does not require touching jobs already in the queue.

To document the jobs your application sends, follow
[Queues and Operations](../asyncapi/queues-and-operations.md). It describes job envelopes and
queue destinations without dispatching work or changing retry policies.

## Defining a job

The resize worker needs to know which image to load and how wide to make it.
Put those values in a payload type, then give the work a stable name with
`queue.NewJob`. Declare the job next to its payload as a package-level variable
and set its worker policy in a `JobBuilder` callback:

```go
import (
	"time"

	"arkoren.dev/core/service/queue"
)

type ResizeImage struct {
	ImageID string `json:"image_id"`
	Width   int    `json:"width"`
}

var ResizeImageJob = queue.NewJob[ResizeImage]("resize-image", func(job *queue.JobBuilder) {
	job.OnQueue("media")
	job.Retry(3)
	job.Timeout(2 * time.Minute)
})
```

This defines the resize job on the `media` queue with three total attempts and a
two-minute timeout per attempt. It does not send a message; dispatching does that.
The policy sections below explain those settings and their limits.

### Keeping the job name valid and stable

The name is the job's identity on the wire: the consumer uses it to find the
handler. Keep it stable once jobs exist. A name must contain 1 to 64 characters
from `a-z`, `0-9`, and `-`, with no hyphen at either end.

An invalid name does not panic. The job remembers the first error, and
`Dispatch` and `Handle` refuse it with `queue.ErrInvalidName`, wrapping the cause.
Always create jobs with `NewJob`: the zero `Job[T]` is invalid and its `Err()` is
non-nil. `Handle` rejects it before registration. `Dispatch` rejects it before
encoding the payload or calling the driver.

### Reusing policies without changing a published job

A job is an immutable value. Builder methods edit and return the same draft;
the constructor publishes an independent value. For variants, reuse a
`queue.JobBuilderFunc` policy and pass a later callback with the local
override to a fresh `NewJob` call. There is no `Job.Derive` or published setter.

Callbacks run directly once, synchronously and in order, on one fresh draft,
even when the name is invalid. Nil entries panic; expanded nil or empty slices
omit configuration. Setters retain the first validation error across callbacks.
The same published job can be used from many goroutines; custom backoff closures
retain their captured state.

`Description` attaches free text for people, such as a note on what the job
does. Nothing in the queue reads it.

## Dispatching

Once the uploaded image is stored, send its ID and the desired width to the
worker with `Dispatch`.

Dispatch only after the work the job depends on is durable. If a request
dispatches a job and then rolls back its database transaction, a fast worker may
run against data that never existed. Commit first, then dispatch, and accept
that a crash between the two loses the job. If losing it is not acceptable,
record the intent in the database and dispatch from there.

This example assumes an existing `dispatcher` of type `*queue.Dispatcher`, a
context `ctx` for the dispatch, and the stored image ID `id`. If you have not
built a dispatcher, follow the [dispatcher setup in Queues Overview](overview.md#jobs-dispatchers-and-consumers)
first. Pass the job definition and a value of its payload type; the compiler
checks that they match:

```go
err := dispatcher.Dispatch(ctx, ResizeImageJob, ResizeImage{ImageID: id, Width: 640})
```

### Understanding success and failure

A successful dispatch means the driver accepted the message. For broker
drivers, this is a confirmation from the broker. It says nothing about the job
having run. Handle the returned `err` according to when the dispatch failed:

- **Before the driver call:** an invalid job fails with `queue.ErrInvalidName`,
  wrapping the cause, or the payload fails JSON encoding. Nothing is queued.
- **At the driver:** dispatch returns whatever error the driver returns, such
  as an unreachable broker, a canceled context, or a queue with no mapping.
  After sending begins, an error can mean the broker accepted the message but
  its acknowledgement was lost. Retrying may enqueue a duplicate.

The dispatcher creates a fresh random ID for every call. Drivers such as
JetStream use message IDs for deduplication, but dispatching the same payload
again through the facade creates a different ID and is not automatically
deduplicated. Put a stable business identifier in the payload for idempotency.
Dispatching has no delay or schedule option: the job is available to consumers
as soon as the driver accepts it. Delays exist only for retries.

## Choosing a queue

Queues separate work with different urgency or resource needs, so a flood of
low-priority thumbnails cannot delay password-reset emails. Every job runs on a
named queue. Without `OnQueue`, it uses the queue called `default`; the resize
definition above instead selects `media`.

A worker can consume all the queues it has handlers for, or only some of them.
[Consuming Jobs](consuming-jobs.md#choosing-which-queues-to-consume) shows how.

A queue name is a logical name. The broker drivers map it to the real thing in
their configuration: a RabbitMQ queue and routing key, a JetStream subject and
durable consumer, an SQS queue URL, a Cloudflare queue ID. A dispatch to a queue
the driver has no mapping for fails, and the driver, never the message, decides
which broker queue receives it. See [Queue Drivers](drivers.md).

## Retries and backoff

If the image store is temporarily unavailable, give the resize job another
chance without retrying forever. `Retry(n)` sets the total number of attempts,
counting the first, so `Retry(5)` allows the original run and four retries. Zero,
the default, leaves the limit to the driver. When the last attempt fails, the
consumer fails the job for good instead of retrying.
The resize job's `Retry(3)` therefore allows three total attempts.
Consumer-shutdown retries can bypass the current attempt-limit check, but the
driver's count is not discounted; a later ordinary failure still uses that count.
[Shutdown handling](consuming-jobs.md#timeouts-panics-and-shutdown) explains when
that exception applies.

> [!WARNING]
> Without an explicit `Retry` limit, the facade imposes no attempt cap. Broker
> retention and redelivery limits, such as SQS redrive policy or JetStream
> `MaxDeliver`, still apply; failed retry settlement can also interrupt that path.
> Memory has no native attempt cap and defaults to zero retry delay, so a live
> consumer can repeatedly retry a failing job in a tight loop. Set a finite
> `Retry` and a suitable backoff rather than relying on indefinite retries.

The attempt limit needs to know which attempt a delivery is. Every driver Arkoren
ships reports it, but a custom driver that does not leaves enforcement to the
broker, as with a job without `Retry`.

### Setting the wait between attempts

`Backoff` shapes the wait before each retry. You give it the delay before the
first retry, a factor that multiplies the delay after each further retry, and a
cap. For an existing account-sync payload type `SyncAccount`, configure eight
attempts:

```go
var SyncAccountJob = queue.NewJob[SyncAccount]("sync-account", func(job *queue.JobBuilder) {
	job.Retry(8)
	job.Backoff(time.Second, 2, time.Minute)
})
```

The seven retries wait 1, 2, 4, 8, 16, and 32 seconds, then a minute because the
next doubling would pass the cap. You can apply the same settings to the image
job above. A factor below one counts as one, giving a constant delay. A cap of
zero removes the limit.

### Choosing a custom delay

If you need a different curve, use `BackoffFunc`. Its function receives the
number of the attempt about to run, always at least two, and returns the delay.
For an existing notification payload type `Notify`, increase the wait by ten
seconds for each retry:

```go
var NotifyJob = queue.NewJob[Notify]("notify", func(job *queue.JobBuilder) {
	job.Retry(4)
	job.BackoffFunc(func(attempt int) time.Duration {
		return time.Duration(attempt) * 10 * time.Second
	})
})
```

This policy waits 20, 30, then 40 seconds before its three retries.

Without `Backoff` or `BackoffFunc`, the delay is the driver's default. A delay is
a lower-bound request: a driver may round it up or reject an unsupported delay,
and cannot always schedule it exactly. A handler can override the job's backoff
for one failure with `queue.RetryAfter`,
described in [Consuming Jobs](consuming-jobs.md#handler-outcomes).

## Timeouts

An export that gets stuck should not occupy a worker indefinitely. Set
`Timeout(d)` to cancel the handler's context when an attempt runs too long.
Zero, the default, means no limit.

Cancellation does not stop the handler by itself: the consumer still waits for
the handler to return. If the handler returns an error, the usual retry or
failure policy applies. If it returns nil, the consumer still acknowledges
success. Pass the context to database and HTTP calls so they can stop at the
deadline.

### Keeping the timeout within the broker lease

The broker's lease on a message is called the visibility timeout or ack wait.
SQS and NATS attempt lease renewal while a handler runs; renewal or connection
failure can still permit concurrent redelivery. RabbitMQ uses unacknowledged
deliveries rather than a renewable visibility lease; losing the connection can
make unsettled work available again.

Cloudflare cannot extend a lease. If a handler outlives it, another worker can
receive the same message while the first handler is still running. Give
Cloudflare jobs a `Timeout` shorter than the lease and make handlers honor the
cancellation; setting a timeout alone does not stop them. If the driver pulls a
batch, time spent waiting behind earlier handlers also uses the lease.
[Queue Drivers](drivers.md) lists each driver's behavior.

For an existing export payload type `Export`, allow five minutes per attempt:

```go
var ExportJob = queue.NewJob[Export]("export", func(job *queue.JobBuilder) {
	job.Retry(3)
	job.Timeout(5 * time.Minute)
})
```

Each of the job's three attempts gets its own five-minute timeout.

## Designing payloads

The payload is the job value as JSON. The consumer decodes it into a fresh
value of the same type, so everything in it must survive `encoding/json`.

Keep the payload small and pass identifiers rather than records. For example,
send the invoice ID and let the handler load the invoice. The job then works
on current data, and the message holds nothing that goes stale or that you
would not want in a broker. Brokers also limit message size; Cloudflare Queues
allow 128 KB per message.

Jobs outlive deployments. A message written by version 41 of your code may be
read by version 42, so change payloads additively: add fields that have a
useful zero value, and do not rename or retype a field while old jobs may still
be queued. A payload that no longer decodes fails the job at once, without
retries, since redelivery cannot fix it.

For every function and option, see [the `queue` API reference](https://pkg.go.dev/arkoren.dev/core/service/queue).
