---
title: Queue Drivers
nav: Drivers
description: Choose between the memory, RabbitMQ, NATS JetStream, Amazon SQS, and Cloudflare queue drivers, and install and configure each.
order: 50
---

# Queue Drivers

## Introduction

Your jobs and handlers can stay the same when you change backends, but delivery
and failure handling differ. Choose a backend by what must survive restarts,
how it reserves work for a handler, and where it retains terminal failures.

For each driver, create `queue.NewDispatcher(driver)` and
`queue.NewConsumer(driver)`, then register your handlers and run the consumer as
shown in the [overview](overview.md#jobs-dispatchers-and-consumers). Handle a
constructor error before registering hooks or using the driver.
For drivers with lifecycle hooks, register cleanup before startup and cancel and
join consumers before stopping the driver; the
[application wiring](overview.md#wiring-into-the-application) shows those hooks.

Use [memory](#memory) for process-local tests and development. For shared,
restart-safe jobs, prefer the broker you operate: [RabbitMQ](#rabbitmq) offers
confirmed publishes, durable retry queues, and a failure queue; [NATS](#nats)
uses JetStream with dispatch deduplication and lease renewal. [Amazon
SQS](#amazon-sqs) is managed and renews leases. [Cloudflare](#cloudflare) lets
services outside Workers produce and consume Cloudflare Queues jobs over HTTP.

Run the installation commands below from your application's module directory.

| Driver | Durable | Retry delay | Failed for good | Reject |
| --- | --- | --- | --- | --- |
| Memory | No | Timer in the process | Logged and dropped | Dropped |
| RabbitMQ | Partial — Work queues are durable; TTL forwarding is conditional. | Per-delay TTL queues | Failure queue, if set | Rejected without requeue |
| NATS | Partial — Stream storage and retention are operational policy. | NAK with delay | Failure subject, if set | Terminated |
| SQS | Yes | Message visibility, up to 12 hours | Failure queue, if set | Left for the redrive policy |
| Cloudflare | Yes | Lease delay, up to 24 hours | Logged and deleted | Delivered again after the lease |

All drivers report the delivery attempt to the consumer, which enforces each
job's `Retry` setting. The broker drivers take the destination of a job from their
own configuration and never from the message.

The application handler returns a job outcome; the queue facade turns that
outcome into an acknowledgement, retry, rejection, or terminal failure. The
driver receives a delivery-handler error from the facade only if that settlement
fails. The unsettled-message behavior below describes that boundary, not the
ordinary retry of an application error.

## Compatibility matrix

Compare Arkoren behavior under the limits above. App restart means queued work
can outlive the worker process; it does not promise survival of broker failure,
indefinite retention, or successful forwarding through every retry. Archives
require a configured failure destination, not a broker dead-letter policy.

| Driver | App restart | Delayed retry | Lease renewal | Failure archive |
| --- | --- | --- | --- | --- |
| [Memory](#memory) | No | Yes | No | No |
| [RabbitMQ](#rabbitmq) | Yes | Partial — TTL forwarding needs broker protection; queue expiry can still discard work. | No | Partial — Requires a distinct FailureQueue and a routed, confirmed transfer. |
| [NATS](#nats) | Yes | Yes | Yes — Progress at half AckWait; the first unexpected renewal failure ends heartbeats. | Partial — Requires a distinct FailureSubject backed by provisioned JetStream storage. |
| [Amazon SQS](#amazon-sqs) | Yes | Yes | Yes | Partial — Requires FailureQueueURL configuration. |
| [Cloudflare](#cloudflare) | Yes | Yes | No | No |

RabbitMQ uses unacknowledged deliveries, not visibility leases. NATS requires
JetStream. SQS cannot extend visibility past 12 hours from the receive request.
Cloudflare batches must finish within their lease; terminal failures are deleted,
not archived. Lost leases can duplicate work; keep handlers idempotent.

## Memory

Memory is bundled in `arkoren.dev/core` for single-process development and tests
that exercise consumers. For dispatch assertions, use the `queuetest` fake in
[Queues Overview](overview.md#testing).

### Connecting

`New` returns a driver directly and starts no goroutines:

```go
import "arkoren.dev/core/service/queue/memoryqueue"

driver := memoryqueue.New()
dispatcher := queue.NewDispatcher(driver)
consumer := queue.NewConsumer(driver)
```

`Start` is a no-op. Register `Stop` for application cleanup after consumers have
stopped; it discards pending work rather than draining it, as described below.

### Configuration

There are two builder settings. `Builder.RetryDelay` is the delay for a retry that asks for
none, zero by default, which makes a failing job retry immediately. `Builder.Logger`
chooses where delivery failures are logged, `slog.Default()` otherwise:

```go
driver := memoryqueue.New(func(consumer *memoryqueue.Builder) {
	consumer.RetryDelay(time.Second)
})
```

### Dispatching and handling jobs

Use the [welcome job and handler](overview.md#jobs-dispatchers-and-consumers)
with this driver. A successful dispatch stores the job in this process; only
one consumer handles each delivery.

### Behavior and limits

Cancellation is checked before each delivery is reserved, even while the queue
has a backlog. A delivery already admitted still belongs to its handler: the
facade's detached settlement context can acknowledge it or request a retry after
consumer cancellation. Work not yet admitted stays queued for another live
consumer; cancellation does not drain or discard it.

`Stop` is different: it prevents new admissions and discards ready jobs and
delayed retries, even if its context is already expired. It waits for active
consumers and handlers only until its context ends; it cannot terminate a handler
that ignores cancellation. An admitted delivery can still be acknowledged after
`Stop`, but a retry cannot enqueue work into the closed driver. New dispatches
and consumers with otherwise valid arguments return `memoryqueue.ErrDriverClosed`;
an already-canceled context takes precedence. Everything queued is also lost when
the process exits.

Delayed retries are promoted by consumers when they poll, so the driver needs no
background goroutine and works inside `testing/synctest`. A job that fails for
good is logged and dropped, and a rejected job is dropped. Consumers on the same
queue compete: a message goes to one of them.

### API references

See the [`memoryqueue` API](https://pkg.go.dev/arkoren.dev/core/service/queue/memoryqueue).

## RabbitMQ

Choose RabbitMQ for AMQP 0-9-1 work queues with persistent messages, manual
acknowledgement, mandatory routing checks, and an optional durable failure queue.
Durable queues alone do not establish end-to-end at-least-once delivery: delayed
retries depend on the [broker forwarding and retention policies](#retry-forwarding-and-retention)
below.

### Installation

```sh
go get arkoren.dev/driver/rabbitmq
```

### Connecting

The AMQP URL carries credentials. Load it from the environment or a secret manager
and use `amqps://` outside development.

`New` takes that URL and a required trusted queue map. It rejects a blank URL
and validates the map and optional settings without touching the network.
`Start` opens the connection and declares the exchange and queues. If you enable
a failure archive, restrict access and retention: its envelopes contain job
bodies and error text. Construct the driver with the queue mapping:

```go
import (
	"os"

	"arkoren.dev/driver/rabbitmq/rabbitmqqueue"
)

queues := map[string]rabbitmqqueue.Queue{
	"emails": {Name: "app.emails", RoutingKey: "emails"},
}
driver, err := rabbitmqqueue.New(os.Getenv("AMQP_URL"), queues)
```

Register `Stop` for cleanup before `Start` for startup; use `Ping` for readiness.
If you need a terminal-failure archive, add `broker.FailureQueue("app.failed")`
inside a `func(broker *rabbitmqqueue.Builder)` construction callback.

With an owned connection, cancellation during `Start`, `Ping`, consumer setup,
or a blocked SDK write closes the socket to interrupt the operation. That also
interrupts every other channel sharing the connection. A canceled readiness
probe can therefore end active consumers; reconstruct the driver after transport
failure. Normal consumption cancellation attempts a channel-only close first,
as described under [connection cleanup](#cancellation-and-connection-cleanup).

`New` uses a connection without SDK automatic recovery. If you already have an
SDK connection, `NewFrom` accepts it only when automatic recovery is disabled;
otherwise it returns a nil driver and an error wrapping `ErrInvalidConfig`.
Rejection leaves your connection open and its recovery configuration unchanged.
Keep a supplied connection non-recovering, and do not reconnect it in place:
recovered channels can reuse delivery tags and let an old handler settle a
different job.

### Configuration

Pass the nonempty queue map positionally to both `New` and `NewFrom`;
`Builder.Queues` has been removed. Each logical name maps to a physical queue
name and routing key on one direct exchange. All three names must be nonblank;
physical queue names and routing keys must each be unique within the map.
`Builder.FailureQueue`, if set, must not name a work queue. Invalid completed
settings return an error wrapping `rabbitmqqueue.ErrInvalidConfig`.

The default exchange is `arkoren:jobs`. `Builder.Exchange("")` restores it;
whitespace-only exchange names are invalid. The queue map is copied before
callbacks run and again at publication, so later input or draft changes cannot
reroute jobs. Callbacks run once in order; supplied nil callbacks panic. Nil
loggers select `slog.Default()`.

`Builder.Prefetch` bounds unsettled deliveries per `Consume` call. Zero selects
one; negative values and counts above 65535 fail construction rather than being
truncated by AMQP. Increasing prefetch does not make one consumer run handlers
concurrently.

`Builder.RetryDelay` supplies the delay for a retry requesting zero; zero in the
builder selects one second. `Builder.MaxRetryDelay(0)` permits the driver's TTL
maximum of 4294967295 milliseconds, about 49 days. Negative or unrepresentable
retry settings fail construction, and the normalized default retry delay must
fit the configured maximum. For example, a maximum below one second also needs
an explicit shorter `RetryDelay`. The maximum bounds requested delays, not
upward bucketing or the time the broker takes to forward a retry.

### Dispatching durable jobs

Use the [welcome job and handler](overview.md#jobs-dispatchers-and-consumers),
whose logical queue is `emails`, with this driver. `Dispatch` waits for a
positive broker confirmation without an unroutable return, not for the mail
handler to finish. A successful mandatory publish establishes acceptance by at
least one queue, not that an administrator configured the intended binding or
retention policy. Keep the exchange and bindings under trusted administration.

### Behavior and limits

Dispatches, delayed retries, and failure-archive transfers are persistent,
mandatory publications. A negative confirmation or an unroutable `basic.return`
returns `rabbitmqqueue.ErrPublishNotConfirmed`, even if the broker also sends a
positive confirm for the returned message. A retry or failure transfer must
succeed before the original delivery is acknowledged; a failed transfer leaves
the original unsettled. Losing a confirmation, or failing after a successful
transfer but before acknowledging its source, can still duplicate work. Do not
infer non-acceptance from a timeout or connection error.

Publications on one driver serialize through confirmation so a return cannot be
attributed to a different job. Waiting callers retain their own cancellation;
a canceled confirmation is drained before the next publication.

When a delivery handler returns an error, `Consume` logs it, exits, and closes
its consumer channel. Unsettled jobs then become eligible for broker redelivery
instead of occupying prefetch slots indefinitely. Through `queue.Consumer`, this
is a settlement error, not an ordinary application error that was successfully
retried. `Consumer.Run` cancels its other queue consumers and returns the error
after they stop.

A job that fails for good is archived when `FailureQueue` is configured; without
one it is rejected without requeue. `Reject` also rejects without requeue. The
driver declares work queues without a dead-letter exchange, so rejected messages
are dropped unless you add a broker dead-letter policy. Failure envelopes contain
`message` and `error`, plus `raw_message` if the original body is nonempty. The raw
body is retained even when decoding succeeds. Apply the archive's access and
retention controls to all those fields.

#### Retry forwarding and retention

This driver implements delays with durable per-delay queues and per-message TTL.
After expiry, RabbitMQ attempts to republish the message to the work exchange,
used here as the dead-letter exchange (DLX). A retry publish confirmation covers
acceptance by the retry queue, not that later forwarding. Default RabbitMQ
dead-lettering can lose work when the destination is unavailable or unroutable.

For protected forwarding, provision retry queues as quorum queues and configure
at-least-once dead-lettering with `reject-publish` overflow policy. Ensure the
`stream_queue` feature flag is enabled, and provide working target queues and
bindings, the required broker permissions, and sufficient storage. The driver
does not select queue types or enable or verify these prerequisites; see
[RabbitMQ's protected dead-lettering requirements](https://www.rabbitmq.com/docs/4.3/quorum-queues#dead-lettering).
Configure queue-type defaults before retry queues are first created. Existing
queue types cannot be changed by redeclaration; plan an explicit migration rather
than assuming a driver restart changes them.

Even with protected forwarding, queue expiry, deletion, and retention policies
can discard pending work. Retry queues expire after an unused interval of twice
their bucket delay plus one hour, including when work is awaiting forwarding.
Account for this limit during prolonged destination outages. There is no
unconditional at-least-once guarantee for this retry path. Protected forwarding
can also duplicate a message when target acceptance is not fully confirmed.

The driver rounds positive delays upward into buckets: whole seconds through a
minute, 15 seconds through an hour, 5 minutes through a day, and whole hours
beyond, capped at the TTL maximum. TTL expiry is not an exact scheduler, and an
older message at the head can delay newer ones. A request above
`Builder.MaxRetryDelay` returns `ErrUnsupportedRetryDelay` without settling the
original delivery.

#### Cancellation and connection cleanup

Normal consumption cancellation closes its consumer channel after the active
handler returns. Cleanup allows five seconds for a responsive broker to close
that channel; an unresponsive owned connection is then aborted, interrupting its
other channels as well. This timeout does not terminate a blocked application
handler.

Cancel and join consumers before calling `Stop`, and serialize `Start` with
`Stop`. For an owned connection, `Stop` closes the connection first, without
waiting for a publisher-channel close or pending confirmation. Graceful close is
bounded by five seconds or its context ending sooner. An already-expired context
still releases the owned socket and returns the context error. `Stop` does not
wait for job handlers; a second call returns `ErrDriverClosed`.

After a connection fails, cancel and wait for consumers and stop the driver before
creating a new driver with `New` or a fresh SDK connection whose
`amqp091.Config.Recovery` is nil. Do not enable recovery on an existing driver's
connection. Keep handlers idempotent because failed connections can still cause
redelivery.

### Using a caller-owned AMQP connection

If another part of your application owns the AMQP connection, borrow its
`*amqp091.Connection` from `github.com/rabbitmq/amqp091-go`. Given a non-recovering
`connection` and the trusted `queues` map shown in Connecting:

```go
driver, err := rabbitmqqueue.NewFrom(connection, queues, func(broker *rabbitmqqueue.Builder) {
	broker.FailureQueue("app.failed")
})
```

All optional settings still apply, and `Start` still declares the topology.
`Connection()` exposes the supplied pointer even before `Start`; it does not
transfer ownership. `Stop` closes only the driver's publisher channel. Cancel
and join consumers before the caller closes the supplied connection, and keep
automatic recovery disabled as described above.

The pinned SDK cannot interrupt an in-flight channel-open, topology, write, or
channel-close RPC through a context alone. For a borrowed connection, ensure a
responsive peer or manage socket deadlines/connection close yourself. The driver
never closes a borrowed connection to enforce cancellation, so its setup,
readiness, and cleanup RPCs can otherwise wait past their context deadlines.

### Running locally

Use the [local RabbitMQ Compose file](../broadcasting/drivers.md#running-locally-2)
in your application. Run `docker compose up -d` and wait for
`docker compose exec rabbitmq rabbitmq-diagnostics -q ping` to succeed.
Set `AMQP_URL=amqp://local:local-only@localhost:5672/`. `Start` creates the
configured work and failure queues. Dispatch the welcome job and check the mail
handler's effect. `docker compose down --volumes` removes the disposable server and queues.

### API references

See the [`rabbitmqqueue` API](https://pkg.go.dev/arkoren.dev/driver/rabbitmq/rabbitmqqueue).

## NATS

Choose NATS when you run JetStream: jobs use subjects in one stream and durable
pull consumers. Core NATS alone cannot run jobs.

### Installation

```sh
go get arkoren.dev/driver/nats
```

### Connecting

Pass the required stream name and trusted queue map to
`natsqueue.New(stream, queues, configure...)`. Construction validates settings
without connecting; `Start` connects and checks compatible stream and consumer
resources. Provision them operationally for production. The following example
enables creation of missing resources for disposable local development with
`Builder.Provision(true)`; it never overwrites incompatible existing resources:

```go
import "arkoren.dev/driver/nats/natsqueue"

queues := map[string]natsqueue.Queue{
	"emails": {Subject: "jobs.emails", Durable: "emails-worker"},
}
driver, err := natsqueue.New("JOBS", queues, func(consumer *natsqueue.Builder) {
	consumer.Provision(true)
})
```

This uses the default local server URL. Register `Stop` for cleanup before
`Start` for startup, and `Ping` for readiness. For production, omit
`Provision(true)`, provision resources separately, and configure server URLs,
TLS, and authentication as described below.

With provisioning disabled, a missing stream or durable consumer returns an
error matching `natsqueue.ErrResourcesNotProvisioned`. Incompatible existing
resources use that classification too, even with provisioning enabled.
Authorization, connection, and cancellation failures retain their native error
identity; they are not treated as missing resources or a reason to create them.
Resource lookup and provisioning requests honor the startup context. Failed
startup closes a newly owned connection, but not a borrowed one.

### Configuration

The stream and nonempty queue map are positional constructor inputs;
`Builder.Stream` and `Builder.Queues` have been removed. Stream and durable names
must fit native asset-name syntax. Each queue needs a distinct literal subject
and durable name; wildcard work subjects are rejected. A configured
`Builder.FailureSubject` must also be literal and must differ from every work
subject, preventing failure envelopes from returning to work consumers. Names
are used without normalization. Queue maps are copied before callbacks and at
publication; URL and native-option slices are also detached. Option functions,
their captures, and loggers remain shared. Nil loggers select `slog.Default()`.

`Builder.Batch` defaults to one, and zero selects that default. It must be
positive. `Builder.MaxAckPending(0)` selects the completed batch size; an explicit
limit must be at least that size. Handlers within a fetched batch still execute
sequentially. `Builder.MaxDeliver` caps deliveries on newly provisioned consumers;
zero leaves the server default and accepts an existing cap, while negative
values fail construction. Exhausting that native cap is not an automatic
failure-subject transfer.

`Builder.AckWait` is the acknowledgement deadline, 30 seconds by default. Zero
selects the default; explicit values below two nanoseconds are invalid because
the heartbeat interval is half `AckWait`. This is a representability floor, not
a recommended production deadline. Pull expiry is separate: each fetch waits at
most ten seconds, or the caller's earlier deadline. An existing consumer's
positive `MaxRequestExpires` must allow at least ten seconds; zero leaves that
native limit unset. Provisioning sets it to ten seconds independently of
`AckWait`, so a shorter acknowledgement deadline does not shorten the pull limit.

Existing consumers must deliver bodies (`HeadersOnly` is rejected), use explicit
acknowledgements and pull delivery, and match the configured durable, literal
filter subject, `AckWait`, and `MaxAckPending`. A positive native
`MaxRequestBatch` must accommodate `Batch`; an explicit driver `MaxDeliver` must
match too. The stream must list every work subject explicitly; a broad wildcard
alone does not satisfy that check. Startup never repairs these settings.

#### Authenticating with native options

`Builder.URLs()` selects `nats://127.0.0.1:4222`; explicit server URLs are validated
without connecting. Constructor URL errors do not echo credential-bearing input,
but URLs and native options can contain secrets; keep them out of application logs.
Pass TLS and authentication policy through `Builder.Options` before `Start`.
The queue builder has no raw-seed setting.

If your server authorizes a user NKey and `seedPath` names its seed file, not the
seed text, build the native option before constructing the driver. This helper
reads the file; that I/O is separate from `natsqueue.New`. Given your trusted
`queues` mapping and a `serverURL` using TLS outside local development:

```go
import "github.com/nats-io/nats.go"

authentication, err := nats.NkeyOptionFromSeed(seedPath)
if err != nil {
	return err
}
driver, err := natsqueue.New("JOBS", queues, func(consumer *natsqueue.Builder) {
	consumer.URLs(serverURL)
	consumer.Options(authentication)
})
```

`NkeyOptionFromSeed` takes a file path, not seed text. Passing secret seed text
as a filename can expose it in filesystem errors. For seed material held only in
memory, use the [native `nats.Nkey` option](https://pkg.go.dev/github.com/nats-io/nats.go#Nkey),
which accepts a public key and a nonce-signing callback; manage that secret's
lifetime yourself. Native options apply only to connections owned by `New`, not
a connection supplied to `NewFrom`.

### Dispatching JetStream jobs

Use the [welcome job and handler](overview.md#jobs-dispatchers-and-consumers)
with the `emails` mapping above. `Dispatch` waits for JetStream acceptance;
workers sharing the durable consumer compete for the work.

### Behavior and limits

Raw driver dispatch sends a nonempty `Message.ID` as `Nats-Msg-Id`, so reusing that
same ID enables JetStream deduplication within its configured duplicate window.
The facade generates a new ID for each dispatch; resubmitting the same payload
through `queue.Dispatcher` is not automatically deduplicated. Neither case is an
exactly-once handler guarantee. Choose stream storage and retention operationally;
startup compatibility checks do not establish every broker durability policy.
`Delivery.Message()` copies both payload bytes and metadata, so edits to a
returned envelope cannot change later reads or failure settlement.

Before processing a fetched batch, the driver starts progress heartbeats for all
its messages, including those waiting behind earlier handlers. Heartbeats run
every half `AckWait` until settlement or handler completion. Progress publication
and settlement admission are serialized: no new progress is published during
settlement or after it succeeds, preventing a successful delayed negative
acknowledgement from being overwritten by a heartbeat. Failed settlements can
still leave broker acceptance uncertain. The first unexpected heartbeat failure
is logged once for that delivery and stops its renewal worker; it does not
terminate application code. Redelivery can then overlap the original handler,
so keep effects idempotent.

A delivery-handler error is logged; the driver does not automatically acknowledge
the job or stop consumption because of that error alone. Through `queue.Consumer`,
it is a settlement error rather than an ordinary application error already
handled by the retry policy. JetStream can redeliver an unsettled job after its
acknowledgement deadline. Empty pulls and pulls interrupted by reconnect resume
polling; other fetch errors end `Consume`.

Retries use a negative acknowledgement with a delay; zero requests immediate
redelivery, and a negative delay returns `ErrUnsupportedRetryDelay` without
settlement. `Reject` requests JetStream termination, not a dead-letter transfer.
A job that fails for good is published to `FailureSubject` as a JSON failure
envelope before its original delivery is acknowledged. Provision JetStream
storage for that subject yourself: `Provision(true)` creates work resources, not
a failure archive. Failed failure publication leaves the original unacknowledged.
Without a failure subject, the driver logs the error and acknowledges the job.
Protect archive access and retention because envelopes contain job data, error
text, and undecodable raw bodies. Transfer and source acknowledgement are not
atomic, so connection loss can still duplicate work or its failure record.

#### Binding, cancellation, and cleanup

Before each `Consume` binds a pull subscription, it explicitly looks up and
revalidates the current durable using that call's context. The resulting binding
cannot create or overwrite a consumer, even if resources changed after startup.
Cancellation stops fetching and leaves unhandled members of a fetched batch
unacknowledged. The subscription is not tied to cancellation in a way that revokes
already-admitted work; its handler can finish detached settlement first.

Cancel and join consumers before `Stop`. For a live connection owned by `New`, `Stop`
starts native drain and waits for closure, subject to its context and the native
`DrainTimeout`. Cancellation forces close and returns the context error, even
for an already-expired context. Native drain timeout remains identifiable as
`nats.ErrDrainTimeout`. `Stop` does not join application handlers, is a no-op on
repeat calls, and does not permit restarting the stopped driver.

### Using a caller-owned NATS connection

`natsqueue.NewFrom(connection, stream, queues, configure...)` borrows a `*nats.Conn`
from `github.com/nats-io/nats.go`. Given your existing `connection` and trusted
`queues` mapping:

```go
driver, err := natsqueue.NewFrom(connection, "JOBS", queues)
```

`Start` still provisions or checks the resources, and all delivery policy applies.
`Builder.URLs` and `Builder.Options` are ignored and do not validate or reconfigure
the borrowed connection; configure credentials and TLS on the native client first.
`Connection()` exposes its supplied pointer even before startup validation.
Neither failed startup nor `Stop` drains or closes it. The caller drains and
closes it after consumers stop; native calls bypass facade routing and telemetry.

### Running locally

With Docker installed, save this as `compose.yaml` in your application:

```yaml
services:
  nats:
    image: nats:2.14.6
    command: ["-js", "-m", "8222"]
    ports:
      - "127.0.0.1:4222:4222"
      - "127.0.0.1:8222:8222"
```

Run `docker compose up -d`, then `curl --fail http://localhost:8222/healthz`.
Use `nats://localhost:4222` and `Provision(true)` as above. Successful driver
startup confirms that the stream and consumers exist. Dispatch a welcome job
and check the handler's effect. Run `docker compose down --volumes` to remove the server
and its disposable JetStream state.

### API references

See the [`natsqueue` API](https://pkg.go.dev/arkoren.dev/driver/nats/natsqueue).

## Amazon SQS

The SQS driver sends each job as a message to an SQS queue and consumes by
long polling. Choose it on AWS when you want durable queues without operating a
broker. A consumer also accepts SNS notification wrappers, so one SNS topic can fan
a job out to several worker queues.

### Installation

```sh
go get arkoren.dev/driver/aws
```

### Connecting

Create the SQS queues with your infrastructure tooling before building the
driver. `New` takes a map from queue names to SQS queue URLs and loads region and
credentials from the AWS default chain: environment, shared configuration, or the
instance role. It validates the required nonempty map and completed optional
settings before loading AWS configuration. Blank names or URLs return an error
wrapping `awsqueue.ErrInvalidConfig`; construction copies the map. It has no
`Start`, `Stop`, or `Ping`:

```go
import "arkoren.dev/driver/aws/awsqueue"

driver, err := awsqueue.New(ctx, map[string]string{
	"emails": "https://sqs.eu-west-1.amazonaws.com/123456789012/emails",
})
```

Follow [Queues Overview](overview.md#jobs-dispatchers-and-consumers) for facade
wiring. Register only the consumer's runner; there are no driver hooks.

### Configuration

Set optional values in trailing `awsqueue.BuilderFunc` callbacks.
`Builder.FailureQueueURL` names a queue that receives a report of each job that failed
for good; empty disables the archive. Provision that queue separately, restrict
access and retention, and keep it separate from work queues. Reports contain job
data and the supplied error text, plus the raw body for undecodable envelopes.
The driver does not sanitize error text; native error causes remain inspectable
and can also contain sensitive information. Keep secrets out of errors and logs.

`Builder.WaitTimeSeconds` is the long-poll duration, 20 seconds by default, with
an accepted range of 1–20 seconds. `Builder.MaxMessages` is the number of messages
per poll, one by default, with an accepted range of 1–10. Zero selects each
default. `Builder.VisibilityTimeout` defaults to 30 seconds when zero; explicit
values must be from one second through 12 hours inclusive. Invalid completed
settings wrap `ErrInvalidConfig`. Wire requests round fractional visibility up to
whole seconds, while heartbeat timing uses the original configured duration.
For a custom region, endpoint, or credentials, build the client yourself and use
`awsqueue.NewFrom`.

### Dispatching SQS jobs

Use the [welcome job and handler](overview.md#jobs-dispatchers-and-consumers)
with this driver's `emails` mapping. Grant the producer `sqs:SendMessage` and
the worker `sqs:ReceiveMessage`, `sqs:DeleteMessage`, and
`sqs:ChangeMessageVisibility` on that queue. A configured failure queue also
needs `sqs:SendMessage`. A successful dispatch confirms SQS acceptance, not
mail delivery.

#### Dispatching and archiving with FIFO queues

If you use FIFO queues, provision both the work queue and any FIFO failure queue
before constructing the driver. Given an existing native `client`, a work queue
URL `fifoURL` ending in `.fifo`, and a separate failure queue URL `failureURL`:

```go
driver, err := awsqueue.NewFrom(client, map[string]string{"emails": fifoURL}, func(consumer *awsqueue.Builder) {
	consumer.FailureQueueURL(failureURL)
})
```

The driver supplies FIFO group and deduplication fields whenever the destination
URL ends in `.fifo`, including failure publications. The group is the logical
queue name, or `failed` if that name is missing. The deduplication ID is the
message ID, or a fresh ID if it is missing. Failure reporting retains the JSON
archive envelope rather than replacing it with a different FIFO format. Each facade
dispatch still generates a fresh message ID, so repeating a payload is not
automatic deduplication or exactly-once handler execution.

### Behavior and limits

SQS supplies the receive count, which becomes the attempt number, so `Retry` is
enforced. Renewal starts at half the configured visibility duration and attempts
another extension every half duration while the delivery remains unsettled.
For example, a 1500 ms setting sends a two-second visibility on the wire but
first renews after 750 ms. All batch messages receive renewal from receipt through
their own handling, including time waiting for earlier handlers. A renewal error
is logged and later ticks can retry; a lost lease can still permit concurrent
redelivery. SQS limits visibility to 12 hours from the receive request; split
longer work into smaller jobs.

Each complete renewal SDK operation, including SDK retries and serialization
wait, gets the lesser of ten seconds and half the configured visibility duration.
Settlement waits for any earlier renewal before admission, using its own context
budget. Once admitted, settlement excludes new renewal requests; successful
settlement keeps them disabled, so heartbeats cannot overwrite a retry delay.
A failed settlement permits renewal and another settlement attempt.

Consumer cancellation does not abandon an active handler: renewal continues
while it finishes, allowing detached facade settlement. When handling ends, the
driver cancels pending renewal I/O before joining its worker. These are cooperative
bounds: native HTTP clients, credential providers, and middleware must honor
operation contexts. A dependency that ignores them can keep cleanup waiting.
A delivery-handler error is logged without automatically acknowledging the job
or ending consumption; an unsettled message can return when visibility expires.

A retry changes the message's visibility to the requested delay, rounded up to a
whole second. Negative raw retry delays and delays above 12 hours return
`awsqueue.ErrUnsupportedRetryDelay` without settling the delivery or shortening
the request. SQS can also reject an otherwise valid delay when it exceeds the
remaining visibility allowance measured from the receive request; the driver
returns the native cause without shortening the delay. See
[SQS's remaining-visibility limit](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ChangeMessageVisibility.html).
Both validation and native retry failures leave settlement available for another
attempt. After a transport error, broker acceptance can remain uncertain.
`Reject` leaves the message
untouched, so the queue's redrive policy moves it to a dead-letter queue once
`maxReceiveCount` is reached; without a redrive policy a rejected job comes back
again and again, so prefer `queue.Permanent` or configure one. A job that fails for
good is sent to the failure queue when one is configured, and then deleted. If that
send fails, source deletion is withheld. Sending the failure report and deleting
its source are not atomic: a successful send followed by uncertain deletion can
duplicate the report or the work. Without a failure queue, the driver logs the
failure and deletes the message.

### Using a native SQS client

`awsqueue.NewFrom(client, queues, configure...)` borrows a `*sqs.Client` from
`github.com/aws/aws-sdk-go-v2/service/sqs`; all queue builder settings still
apply. A nil client is invalid; validation starts no goroutines and makes no SQS
requests. Configure credentials, endpoint, SDK retries, and the HTTP client before
wrapping it. `driver.Client()` exposes it. Native operations bypass facade
encoding and telemetry; the driver does not close the native client.

### Running locally

Install Docker and the AWS CLI. Save this as `compose.yaml` in your application:

```yaml
services:
  aws:
    image: ministackorg/ministack:1.5.8
    ports:
      - "127.0.0.1:14568:4566"
```

Run `docker compose up -d`, then
`curl --fail http://localhost:14568/_ministack/health` until it succeeds. In
the same shell, configure emulator-only credentials and create a queue:

```sh
export AWS_ACCESS_KEY_ID=000000000000
export AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1
queueURL=$(aws --endpoint-url http://localhost:14568 sqs create-queue --queue-name emails --query QueueUrl --output text)
aws --endpoint-url http://localhost:14568 sqs get-queue-attributes --queue-url "$queueURL" --attribute-names QueueArn
```

The last command returns the queue ARN. Supply the returned `queueURL` to your
Go application. Build the SDK configuration and wrap its client:

```go
import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

cfg := aws.Config{
	Region:       "us-east-1",
	BaseEndpoint: aws.String("http://localhost:14568"),
	Credentials:  credentials.NewStaticCredentialsProvider("000000000000", "test", ""),
}
driver, err := awsqueue.NewFrom(sqs.NewFromConfig(cfg), map[string]string{"emails": queueURL})
```

Dispatch a welcome job and check the handler's effect. These credentials and
plaintext endpoint are for the local emulator only. `docker compose down --volumes`
removes the disposable emulator and all its queues and messages.

### API references

See the [`awsqueue` API](https://pkg.go.dev/arkoren.dev/driver/aws/awsqueue).

## Cloudflare

Choose Cloudflare for services outside Workers that need Cloudflare Queues.
The driver sends jobs through the REST API and uses HTTP pull consumption.

### Installation

```sh
go get arkoren.dev/driver/cloudflare
```

### Connecting

Before consuming, configure each queue with an HTTP pull consumer using
`wrangler queues consumer http add <queue>` or the dashboard. Add a dead-letter
queue there if you need to retain messages after the broker's retry limit.

`New` takes an API token with Queues read and write permission, the account ID, and
a map from queue names to Cloudflare queue IDs. An empty token, blank account ID,
empty map, or blank queue name or ID returns an error wrapping
`cloudflarequeue.ErrInvalidConfig`. Construction makes no request, and the driver
has no lifecycle hooks:

```go
import (
	"os"

	"arkoren.dev/driver/cloudflare/cloudflarequeue"
)

driver, err := cloudflarequeue.New(
	os.Getenv("CLOUDFLARE_API_TOKEN"),
	os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
	map[string]string{"emails": "6b8f3a0c2d1e4f5a8b7c9d0e1f2a3b4c"},
)
```

Build your dispatcher or consumer with this driver; only the consumer needs an
application runner. Both `New` and `NewFrom` copy the routing map before invoking
callbacks: edits to the caller's map during a callback or afterward cannot change
the constructed driver's routes. Do not mutate the input concurrently with that
capture. Callbacks run directly once in order on one fresh draft; supplied nil
callbacks panic, and retained drafts cannot change the published driver.

The token is not part of the queue builder, but that does not make native clients
or errors safe to log. Protection of Arkoren-owned diagnostics does not recursively
sanitize the native client, middleware, or errors. Native error causes remain
inspectable. Handler-returned errors, terminal-failure causes, and confirmed
settlement warnings are logged as supplied; keep credentials and payloads out of
them and apply application-level logger redaction where needed.

### Configuration

Set optional values in trailing `cloudflarequeue.BuilderFunc` callbacks.
`Builder.BatchSize` accepts 1–100 messages per pull; zero selects one.
`Builder.VisibilityTimeout` accepts one millisecond through 12 hours inclusive;
zero selects 30 seconds. After an empty pull the driver waits
`Builder.PollInterval`, one second by default, and doubles the wait on each further
empty pull up to `Builder.MaxPollInterval`, ten seconds by default. Zero selects
those defaults; negative polling intervals fail construction, and a positive cap
below the initial interval is raised to it. Invalid completed settings wrap
`ErrInvalidConfig`.

`Builder.Endpoint` points the client at another API endpoint in `New` only;
`NewFrom` ignores it because the borrowed client already carries connection
settings. `Builder.Logger` retains a shared logger, with nil selecting
`slog.Default()`.

### Dispatching and pulling jobs

Use the [welcome job and handler](overview.md#jobs-dispatchers-and-consumers)
with this driver's `emails` mapping. The producer sends to the configured queue
ID; only the worker needs an HTTP pull consumer. Confirm success through the
handler's effect, not merely a successful dispatch.

### Behavior and limits

Cloudflare cannot extend a lease. A handler, and the handlers before it in the same
batch, must finish within the visibility timeout to avoid concurrent redelivery.
Keep the batch small for slow jobs. Cancellation is checked before every batch
handler, including messages already fetched. Unhandled messages keep their unused
leases, which expire normally; cancellation does not acknowledge them. An active
synchronous handler must still return cooperatively.

Cloudflare supplies the message ID and attempt count. Positive retry delays round
up to whole seconds: 1500 ms requests two seconds, never one. Delays above 24 hours
return an error without a request or settlement; they are not shortened to fit.
Negative raw delays become zero, which delegates timing to the provider.

`Acknowledge` requires exactly one native acknowledgment count; `Retry` requires
exactly one retry count. A count for the other operation cannot confirm success,
and HTTP success or warnings alone are insufficient. A warning for the lease
accompanied by the matching count of one is logged without invalidating success.
Native errors and unconfirmed counts leave the delivery open for another explicit
settlement attempt. An uncertain response does not prove that the remote operation
had no effect.

`Reject` settles locally without a Cloudflare request or a confirmed dead-letter
outcome. The message becomes eligible for redelivery when its lease ends, subject
to the pull consumer's retry limit and dead-letter queue; without a dead-letter
queue it is discarded after that limit. `Fail` logs the cause and requests deletion
under the acknowledgment-count rules above; it does not create a failure archive.
A message is at most 128 KB, and the payload is base64 encoded inside it.
A delivery-handler error is logged without automatically acknowledging the job or
ending consumption. A pull that fails after the SDK's own retries ends `Consume`
with the error, which stops the worker.

### Configuring a native Cloudflare client

For custom HTTP transport, retry behavior, or middleware, build a
`*cloudflare.Client` from `github.com/cloudflare/cloudflare-go/v7`, then pass it
to `cloudflarequeue.NewFrom(client, accountID, queues, configure...)`. The
account ID and logical queue map remain required. `Endpoint` does not apply to
this borrowed client; all other builder settings do. A nil client returns an error
wrapping `ErrInvalidConfig`. `driver.Client()` returns the same pointer supplied
to `NewFrom`, not a copied or sanitized client. The client and logger remain shared;
configure them before concurrent use. The caller retains ownership, including
transport cleanup, and native operations bypass driver routing, settlement checks,
and facade telemetry.

There is no local emulator for this driver's remote REST pull API. Use
`queuetest` for local application tests and a separate Cloudflare development
queue for remote integration checks; do not consume production messages in tests.

### API references

See the [`cloudflarequeue` API](https://pkg.go.dev/arkoren.dev/driver/cloudflare/cloudflarequeue).
