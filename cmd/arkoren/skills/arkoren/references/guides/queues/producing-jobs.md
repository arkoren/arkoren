---
title: Producing Jobs
description: Define typed work, route it to a queue, and make the handoff without confusing acceptance with completion.
order: 30
summary:
  - Define stable job names and small typed payloads shared by producers and workers.
  - Dispatch through logical queue routing and recognize uncertain acceptance.
  - Preserve dispatch intent across database commits and evolve queued payloads safely.
---

# Producing Jobs

When someone requests a report, store the request and let a worker generate the
file later. The producer defines the kind of work once, then dispatches a payload
for each report. The definition stays in application code; only the job name,
queue, JSON payload, and transport information travel in the message.

These examples assume a [configured dispatcher](drivers.md) named `dispatcher`
and an operation context `ctx`. Keep the definition in a package that the worker
can also import. Retry, backoff, and timeout policy come from the worker's own
registered definition, not from the producer's message.

## Defining Jobs

Carry the report's stable identifier so the handler can load its current state.
Import `arkoren.dev/core/service/queue` as `queue` and `time` for the policy below:

```go
type BuildReport struct {
	ReportID string `json:"report_id"`
}
```

Declare an immutable job with a stable name and a finite execution policy:

```go
var BuildReportJob = queue.NewJob[BuildReport]("build-report", func(job *queue.JobBuilder) {
	job.OnQueue("reports")
	job.Retry(3)
	job.Backoff(time.Second, 2, time.Minute)
	job.Timeout(45 * time.Second)
})
```

This job allows three total attempts, not three additional retries. Its two
retries request one-second and two-second delays. Each attempt gets its own
45-second context deadline. A timeout is cooperative cancellation, not a hard
kill: the consumer still waits for the handler to return. A handler returning
`nil` is acknowledged even after its deadline.

Without explicit policy, `Retry(0)` leaves the limit to the driver, backoff uses
driver timing, and `Timeout(0)` imposes no execution deadline. Memory has no native
attempt cap and immediate retries by default, so an endlessly failing job can
spin. Prefer finite attempts and deliberate backoff. [Delivery and Retries](delivery-and-retries.md#retry-policy)
owns policy variations, custom backoff, and backend delay limits.

### Stable Names

The name selects the worker's handler. Use 1–64 lowercase ASCII letters, digits,
or hyphens, with no hyphen at either end. Keep it stable while messages bearing
that name can still exist.

Always construct jobs with `NewJob`; a zero `queue.Job[T]` is invalid. Invalid
names or settings retain the first validation error in `job.Err()` rather than
panicking. `Dispatch` and `Handle` reject an invalid job with `queue.ErrInvalidName`
wrapping that cause, before encoding or registering it. Negative attempts,
timeouts, and backoff durations are invalid; an empty queue is invalid too.

Construction callbacks run once, synchronously in order, even for an invalid
name. Supplied nil callbacks panic. Published jobs have no setters or `Derive`;
retaining a draft cannot change them. To share policy, pass a
`queue.JobBuilderFunc` to fresh `NewJob` calls and use later callbacks for local
overrides. Custom backoff closures retain their application-owned captured state.
`Description` adds human-readable text without changing delivery.

## Queue Routing

Use separate queues when reports should not compete with urgent work for the
same consumer. `OnQueue("reports")` chooses a logical destination; without it,
a job uses the literal `default` queue.

The [driver's trusted routing](drivers.md) maps that name to a RabbitMQ queue and
routing key, JetStream subject and durable consumer, SQS URL, or Cloudflare queue
ID. Broker drivers reject a logical name with no configured mapping. They never
take the physical destination from an untrusted message body.

The worker must register the job and consume its logical queue. A
[queue restriction](consuming-jobs.md#selecting-queues) can dedicate a worker to
reports without changing producer routing or creating broker resources.

## Dispatching Work

Dispatch only after the data the handler needs is durable. Publishing before a
database transaction commits lets a fast worker read missing or rolled-back data.
For the already-stored report `rpt-204`, submit its ID:

```go
err := dispatcher.Dispatch(ctx, BuildReportJob, BuildReport{ReportID: "rpt-204"})
```

The compiler checks the payload type. The dispatcher JSON-encodes the payload,
adds a fresh random message ID, and calls the driver. Success means acceptance
by the driver, not execution or a completed report. There is no dispatch delay or
schedule option; delay policy applies to retries, not the initial submission.

An invalid job or JSON encoding failure occurs before the driver call and queues
nothing. Driver errors retain their causes. Once a send begins, a timeout or
connection error can mean that the broker accepted the message but its reply was
lost. Do not infer non-acceptance or blindly promise that retrying is harmless.

Every facade dispatch creates a new ID. Repeating a payload is not automatically
deduplicated, including on JetStream or SQS FIFO. Use the report ID as the business
identity and make the [handler's effects repeat-safe](delivery-and-retries.md#duplicate-effects).

## Durable Handoff

Committing the report request and then dispatching avoids premature execution,
but a crash between those operations can still lose the work. If that gap is
unacceptable, use an application-owned **outbox**: a durable record of work still
to be dispatched.

Write the report request and its outbox intent in the same database transaction.
A separate relay reads committed intents, dispatches them, and marks them sent
only after driver acceptance. Keep unsuccessful or uncertain sends recoverable.
If multiple relays run, claim records safely and recover abandoned claims; do
not let two relays assume exclusive ownership from an unprotected read.

The relay can crash after acceptance but before recording success. It must then
be able to resend, so the outbox still requires idempotent handlers and a stable
business ID carried through every resend. Queue message IDs generated by the
facade do not replace that identity. Define retry, alerting, reconciliation, and
retention for stuck intents instead of deleting them after an arbitrary send
error. Arkoren's queue facade does not supply an atomic database-and-broker
transaction or an outbox relay API.

## Payload Changes

Keep payloads small: send identifiers rather than full records, credentials, or
large file contents. Broker readers can inspect queued data, and provider message
limits include the envelope. Cloudflare's 128 KB limit includes base64-encoded
payload bytes, so a 128 KB source payload does not fit unchanged.

Messages outlive deployments. Add fields with useful zero values; do not rename
or retype fields while older messages can remain queued. Deploy compatible
workers before producers start emitting new names or payload shapes. A payload
that cannot decode fails terminally without ordinary retries; unknown names get
a bounded [deployment grace period](delivery-and-retries.md#poison-messages).
Changing consumer policy affects later deliveries of existing messages because
the message does not contain that policy.

For a machine-readable description of jobs without changing execution, use
[Queues and Operations](../asyncapi/queues-and-operations.md). For every queue
function and option, see the [`queue` API](https://pkg.go.dev/arkoren.dev/core/service/queue).
