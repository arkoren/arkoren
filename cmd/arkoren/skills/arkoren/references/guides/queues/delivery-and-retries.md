---
title: Delivery and Retries
description: Choose retry and terminal outcomes while accounting for uncertain sends, duplicate effects, deadlines, and broker settlement.
order: 50
summary:
  - Distinguish accepted messages, completed effects, and acknowledged deliveries.
  - Choose handler outcomes, total-attempt limits, backoff, and cooperative deadlines.
  - Handle shutdown interruptions, poison messages, and backend-specific terminal records.
---

# Delivery and Retries

A worker can finish a report and still fail to acknowledge its message. Another
worker can then receive the same work. Retry policy controls what the consumer
requests after an attempt; it cannot make the handler's effects and the broker's
state one atomic operation.

This page assumes the shared report job from [Producing Jobs](producing-jobs.md)
and a [running consumer](consuming-jobs.md). A **delivery** is a message reserved
for handling. **Settlement** is the consumer's request to acknowledge, retry,
reject, or terminally fail that delivery. Backend storage, retention, and native
delivery limits still apply; compare [Drivers](drivers.md#compatibility-matrix).

## Acceptance Boundaries

Successful dispatch confirms driver acceptance, not handler execution. A dispatch
error after transmission starts can leave acceptance uncertain: the backend may
have stored the message before its reply was lost. Repeating the facade call
creates a new message ID and can enqueue another copy.

The consumer requests acknowledgement only after the handler returns `nil`.
If an external effect succeeds before the handler returns an error, retry can
repeat it. Even a successful handler can run again if acknowledgement fails or
the reply is uncertain. Losing a lease can allow another worker to start while
the first handler is still running.

There is no unconditional portable at-least-once delivery guarantee and no
exactly-once handler guarantee. Memory loses pending work at stop or process exit.
Broker retention, delivery limits, and retry-forwarding policy can remove work
before completion. RabbitMQ's retry confirmation covers its intermediate TTL
queue, not later forwarding; its [forwarding protections and expiry limit](drivers.md#retry-forwarding)
matter even when the work queue is durable.

## Duplicate Effects

Use a stable business identity such as `BuildReport.ReportID`, not a transport ID,
to make repeats harmless. Typed handlers do not receive the message ID, and the
facade creates a fresh one on every dispatch. JetStream's duplicate window or SQS
FIFO deduplication does not deduplicate repeated facade payloads automatically.

For database-only work, enforce uniqueness and record completion in the same
transaction as the effect. For a remote effect, use the provider's idempotency key
when available and reconcile uncertain outcomes before repeating the operation.
A local flag written before the remote call can suppress work that never happened;
a flag written afterward leaves a crash window for duplicate effects. Neither
placement makes the remote call atomic.

Repeats and ordering are different concerns. Concurrent workers and retries can
reorder effects even when each effect is idempotent. Enforce sequence or version
checks in application state if order matters. A [durable outbox](producing-jobs.md#durable-handoff)
protects dispatch intent across database commits but still needs duplicate-safe
consumption.

## Handler Outcomes

Return the result that matches what the application knows, not what you hope the
broker will do. The consumer converts it into a settlement request:

| Handler Result | Consumer Request |
| --- | --- |
| `nil` | Acknowledge completed work. |
| Ordinary error | Retry with job backoff, or fail terminally at the attempt cap. |
| `queue.RetryAfter(err, delay)` | Retry with this delay instead of backoff, still subject to the cap. |
| `queue.Permanent(err)` | Fail terminally regardless of remaining attempts. |
| `queue.Reject(err)` | Reject without a facade retry; backend rejection policy decides redelivery. |

For an upstream report service that returns a rate-limit delay, your handler can
override backoff. Here `upstream.Build` and `rateLimited` belong to your integration;
the latter recognizes an error with a usable `response.RetryAfter` duration:

```go
response, err := upstream.Build(ctx, payload.ReportID)
if rateLimited(err) {
	return queue.RetryAfter(err, response.RetryAfter)
}
```

Handle the remaining result normally. The explicit delay does not bypass the
attempt limit. Negative delays become zero, selecting driver timing; positive
delays are lower-bound requests that a driver can round up or reject, not silently
shorten. Backend availability and retention still determine whether delivery occurs.

If loading a report shows that its source record no longer exists, retrying cannot
repair that absence. With `errors` imported and your application's
`reports.ErrNotFound` sentinel, classify that branch:

```go
if errors.Is(err, reports.ErrNotFound) {
	return queue.Permanent(err)
}
```

`Permanent` preserves the cause and matches `queue.ErrPermanent` under
`errors.Is`. Check the operation's error before wrapping it: `Permanent(nil)` is
still a non-nil terminal outcome, not success. `RetryAfter` and `Reject` also
accept nil causes while retaining their explicit outcome.

Rejection is deliberately backend-specific. Prefer `Permanent` for irreparable
application failures unless you intentionally configured native rejection/redrive:

| Driver | Accepted Rejection | Accepted Terminal Failure |
| --- | --- | --- |
| Memory | Drops the message. | Logs and drops it. |
| RabbitMQ | Rejects without requeue; drops unless broker dead-letter policy applies. | Transfers to configured failure queue before acknowledging; otherwise rejects without requeue. |
| NATS | Terminates delivery, not a dead-letter transfer. | Publishes to configured failure subject before acknowledging; otherwise logs and acknowledges. |
| Amazon SQS | Leaves the message for visibility expiry and native redrive; can return repeatedly without redrive. | Sends to configured failure queue before deleting; otherwise logs and deletes. |
| Cloudflare | Makes no remote request; lease expiry and native retry/dead-letter policy still apply. | Logs and requests deletion; no failure archive. |

These are successful settlement paths, not unconditional guarantees after an RPC
error. The facade exposes no portable dead-letter inspection or replay API.

## Retry Policy

Set attempts and backoff on a fresh job declaration, using `queue` from
`arkoren.dev/core/service/queue` and `time`. This alternative report definition
allows one initial run and four retries:

```go
var BuildReportJob = queue.NewJob[BuildReport]("build-report", func(job *queue.JobBuilder) {
	job.OnQueue("reports")
	job.Retry(5)
	job.Backoff(2*time.Second, 2, 10*time.Second)
})
```

The four retries request delays of 2, 4, 8, and 10 seconds. `Retry(n)` counts
the first attempt. Zero, the default, imposes no facade cap; native caps and
retention can still end delivery. All shipped drivers report attempt counts.
A custom driver reporting zero cannot support facade attempt-limit enforcement.

`Backoff` takes an initial delay, multiplier, and maximum. A multiplier below one
or NaN becomes one; a zero maximum removes the cap. Negative durations are
invalid. Without backoff, the driver's default timing applies: memory can retry
immediately without a native attempt cap, so always choose a finite policy for
work that may keep failing.

For a custom curve, use an alternative construction with `BackoffFunc`:

```go
var BuildReportJob = queue.NewJob[BuildReport]("build-report", func(job *queue.JobBuilder) {
	job.OnQueue("reports")
	job.Retry(4)
	job.BackoffFunc(func(attempt int) time.Duration {
		return time.Duration(attempt-1) * 5 * time.Second
	})
})
```

The callback receives the attempt about to run, at least two, so these retries
request 5, 10, then 15 seconds. A negative result becomes zero; a nil callback
restores driver timing. It replaces any earlier `Backoff`, and vice versa.
The callback's captured state remains shared and must support concurrent use.

Check [driver bounds](drivers.md) before choosing delays. RabbitMQ buckets delays
and has TTL/forwarding limits; NATS uses delayed negative acknowledgement; SQS
rounds up to seconds and allows at most 12 hours subject to remaining visibility;
Cloudflare rounds up to seconds and allows at most 24 hours. Unsupported-delay
settlement can fail even though the application correctly requested a retry.

## Execution Deadlines

Set `job.Timeout(duration)` in the definition callback to bound one attempt's
context. Zero means no deadline. Execution is synchronous: passing the deadline
does not force a return. If the handler returns an error after the deadline,
normal outcome and attempt policy apply. If it returns `nil`, the consumer still
acknowledges success. Panics are recovered as ordinary errors with stack text and
follow retry/cap policy; avoid secrets in panic values and logs.

Execution timeout is not the broker's reservation deadline. NATS renews every half
`AckWait`; SQS renews every half visibility duration, with a 12-hour limit from
the receive request. Failed renewal can permit concurrent redelivery. RabbitMQ
uses unacknowledged deliveries rather than a renewable lease; connection loss can
release unsettled work. Cloudflare has no renewal: budget for batch waiting,
execution, and settlement within visibility, and make execution honor its shorter
timeout. Increasing batch size does not increase concurrency within one consume
call.

## Shutdown Settlement

When the consumer's context ends, an active handler receives cancellation. If it
returns an error matching that context error, such as `ctx.Err()` or a `%w`
wrapper, the consumer requests retry with zero delay, bypassing the immediate
attempt-cap and backoff checks. A different error follows ordinary policy.
The reported delivery count is **not discounted**: a later ordinary failure can
therefore exhaust the job's cap. Job timeout alone does not select this shutdown
exception.

After decoding and execution finish, settlement gets a fresh 30-second context
detached from consumer cancellation, retaining context values. Unknown names
receive that same fresh budget without execution. Acknowledgement for completed
work can therefore finish after shutdown begins, but the context cannot keep a
closed connection alive or make a non-cooperative SDK interruptible.

Cancel and join consumers before stopping drivers or closing borrowed clients.
Allow shutdown time for both handler completion and settlement. If the application
budget expires first, driver cleanup can interrupt settlement. In memory,
canceling a consumer preserves its unadmitted backlog; stopping the driver instead
discards pending work and prevents a final retry from being enqueued.

An unsuccessful settlement is returned to the driver, not converted into
successful retry or failure. RabbitMQ ends consumption and closes its channel to
release unsettled jobs. NATS, SQS, and Cloudflare log the error and can keep
consuming while unsettled work becomes eligible for redelivery. A settlement
error does not prove that the backend ignored the request.

## Poison Messages

A **poison message** cannot complete with the current handler or envelope. A
payload that fails typed JSON decoding requests terminal failure immediately;
another attempt cannot repair those bytes. Drivers also handle malformed native
envelopes before the typed handler is called, using their own failure path.
Do not expect facade events for a message the facade never receives.

An unknown job name may instead mean an older worker received newly deployed
work. The consumer requests a 30-second retry until the reported delivery count
reaches ten, then requests terminal failure with `queue.ErrUnknownName`.
For a longer deployment window, construct a consumer with a different cap:

```go
consumer := queue.NewConsumer(driver, func(worker *queue.ConsumerBuilder) {
	worker.UnknownNameAttempts(20)
})
```

Zero restores ten; a negative value removes the facade cap, not broker limits.
The cap relies on reported attempts. Native delivery-limit exhaustion, such as
JetStream `MaxDeliver`, does not itself invoke the facade's terminal failure path
or create its failure record. Deploy compatible workers first and investigate
unknown-name failures instead of using unlimited retries as deployment policy.

## Failure Records

Configure archives on [Drivers](drivers.md) before relying on terminal jobs being
retained. RabbitMQ, NATS, and SQS can transfer a failure envelope to a configured
destination before acknowledging/deleting the source. Publication failure leaves
the source unsettled. Transfer and source acknowledgement are not atomic: a lost
reply or crash can duplicate work or failure records. Archive access and retention
must protect job data, raw malformed bodies, and unsanitized error text.

With an event bus configured, `queue.JobFailed` is emitted only after the driver
accepts terminal settlement. It does not prove archive durability, undo remote
effects, or indicate a native dead-letter destination. Rejection emits its own
event rather than `JobFailed`; failed settlement emits no accepted-outcome event.
[Monitoring](metadata-and-telemetry.md#outcome-events) shows event wiring and how
to distinguish handler telemetry from settlement evidence.

See the [`queue` API](https://pkg.go.dev/arkoren.dev/core/service/queue).
