---
title: Drivers
description: Choose a queue backend and configure its routing, resources, connections, and local checks.
order: 20
summary:
  - Compare process-local work with broker-backed delivery, renewal, and failure archives.
  - Configure each provider's trusted routing and manage owned or borrowed clients.
  - Run disposable RabbitMQ, JetStream, and SQS checks without a toolkit checkout.
---

# Drivers

Choose memory for single-process development and tests. For work shared across
processes, use a broker you operate—RabbitMQ or NATS JetStream—or a managed queue,
Amazon SQS or Cloudflare Queues. Decide what must survive worker restarts, how
long handlers run, and whether terminal failures need an archive before choosing.

The examples map the logical `reports` queue from the [overview](overview.md) to
provider resources. After construction succeeds, use the driver with
`queue.NewDispatcher(driver)` and `queue.NewConsumer(driver)`. Neither facade
starts or stops the driver. [Consuming Jobs](consuming-jobs.md#worker-lifetime)
owns runner wiring; the provider sections below own connection setup.

Run installation commands from your application's module directory. Driver
construction callbacks run once in order; nil callbacks panic. Required routing
maps are copied, and retained drafts cannot reconfigure published drivers.
Native clients, loggers, option functions, and their captured state remain shared;
configure them before concurrent use. Native calls bypass facade instrumentation.

Local worker commands below assume your existing application runs its registered
consumer from `./cmd/worker`; substitute your actual worker entry point. They do
not refer to an Arkoren CLI subcommand or create a worker for you.

## Compatibility Matrix

This compares Arkoren's exposed behavior, not every vendor feature. Restart
survival means pending work can outlive the application process; it is conditional
on broker storage, retention, delivery limits, and available infrastructure.
A failure archive is an application-level transfer configured on the driver,
not a native dead-letter policy.

| Driver | Worker Restart | Delayed Retry | Lease Renewal | Failure Archive |
| --- | --- | --- | --- | --- |
| [Memory](#memory) | No | Yes — Process-local pending work. | No | No |
| [RabbitMQ](#rabbitmq) | Yes — Durable queues and persistent publications. | Partial — TTL forwarding and queue expiry can lose work. | No — Uses unacknowledged deliveries. | Partial — Requires a distinct FailureQueue and a confirmed routed transfer. |
| [NATS](#nats) | Partial — Stream storage and retention are operational policy. | Yes | Yes — Renewal stops after the first unexpected heartbeat failure. | Partial — Requires a distinct FailureSubject with separately provisioned storage. |
| [Amazon SQS](#amazon-sqs) | Yes — Subject to queue retention. | Partial — At most 12 hours and limited by remaining visibility allowance. | Yes — Limited to 12 hours from the receive request. | Partial — Requires FailureQueueURL. |
| [Cloudflare](#cloudflare) | Yes — Subject to provider retention and delivery limits. | Yes — At most 24 hours. | No | No |

All shipped drivers report attempts, allowing the facade to enforce `Retry(n)`
as **n total attempts**. Failed settlement and lost leases can duplicate work.
There is no portable exactly-once execution or unconditional at-least-once
guarantee; see [Delivery and Retries](delivery-and-retries.md).

## Memory

`memoryqueue` is bundled in `arkoren.dev/core`. Share one driver instance between
the dispatcher and consumer; separate instances do not share work.

### Connecting

Construction starts no goroutines and needs no server:

```go
import "arkoren.dev/core/service/queue/memoryqueue"

driver := memoryqueue.New()
```

`Start` does nothing on an open driver. There is no `Ping`. Register `Stop` for
cleanup, but cancel and join consumers first: `Stop` discards ready jobs and
delayed retries rather than draining them, even with an expired stop context.
Process exit also loses pending work.

### Retry Timing

Without a job backoff, retries are immediate. If a process-local retry needs a
default pause, use an alternative construction with `time` imported:

```go
driver := memoryqueue.New(func(broker *memoryqueue.Builder) {
	broker.RetryDelay(time.Second)
})
```

Consumers promote delayed work while polling; no background timer worker is
required. Set a finite job attempt limit to avoid an endlessly failing job.
`Logger` selects a shared logger, with `slog.Default()` as the default.

### Processing Work

Use the shared [dispatch example](producing-jobs.md#dispatching-work) and
[handler example](consuming-jobs.md#registering-handlers). Consumers on the same
queue compete. Canceling a consumer preserves work not yet admitted and lets an
active handler finish settlement; stopping the driver instead closes admissions
and discards pending work. `Stop` waits for active consumers only until its
context ends and cannot kill a handler. An admitted delivery can still be
acknowledged after stop, but retrying into the closed driver fails with
`memoryqueue.ErrDriverClosed`. New valid dispatch and consume calls also fail
with that error; an already-canceled context takes precedence.

Terminal failures are logged and dropped; rejection drops the job. For dispatch
assertions without a live consumer, use the [queue fake](overview.md#testing-jobs).

See the [`memoryqueue` API](https://pkg.go.dev/arkoren.dev/core/service/queue/memoryqueue).

## RabbitMQ

Choose RabbitMQ for durable AMQP work queues and confirmed persistent publishes.
You need an AMQP endpoint, credentials, and permission to declare the configured
exchange, queues, bindings, and retry topology. Install from your application:

```sh
go get arkoren.dev/driver/rabbitmq
```

### Connecting

Load `amqpURL` from trusted configuration such as `AMQP_URL`; it contains
credentials, so use `amqps://` outside local development and keep it out of logs. Map logical names
to distinct physical queues and routing keys:

```go
import "arkoren.dev/driver/rabbitmq/rabbitmqqueue"

routes := map[string]rabbitmqqueue.Queue{
	"reports": {Name: "app.reports", RoutingKey: "reports"},
}
driver, err := rabbitmqqueue.New(amqpURL, routes)
```

`New` validates configuration without connecting. Handle `err` before using the
driver. `New` uses a connection without SDK automatic recovery. `Start` connects
and declares a durable direct exchange (by default, `arkoren:jobs`) and work
queues. In your existing composition callback, `composer` is a
`*application.Composer` from `arkoren.dev/core/application`. Register cleanup
before startup:

```go
composer.Cleanup(driver.Stop)
composer.Startup(driver.Start)
composer.Check("queue", driver.Ping)
```

Serialize `Start` and `Stop`; cancel and join
consumers before stopping the driver. An owned `Stop` releases the connection,
bounded by five seconds or its context ending sooner, but does not join handlers.
An expired context still releases the socket. A second stop returns
`ErrDriverClosed`.

### Failure Archives

If terminal failures need inspection, configure a separate archive queue in an
alternative construction. Restrict its access and retention first: failure
envelopes contain job data, error text, and the original raw body even for valid
jobs.

```go
driver, err := rabbitmqqueue.New(amqpURL, routes, func(broker *rabbitmqqueue.Builder) {
	broker.FailureQueue("app.report-failures")
})
```

`Start` declares that queue. A failure transfer must receive a positive confirm
without an unroutable return before the source is acknowledged. Without an
archive, terminal failure rejects without requeue; rejection always does so.
Work queues have no driver-declared dead-letter exchange, so rejected work is
dropped unless you add a broker policy.

### Delivery Policy

Use [Dispatching Work](producing-jobs.md#dispatching-work) after startup. Dispatch
waits for a positive publisher confirm without an unroutable `basic.return`.
That establishes acceptance by at least one queue, not execution or correctness
of the intended binding. Keep exchange administration trusted. An unroutable or
negatively confirmed publish returns `ErrPublishNotConfirmed`. Lost confirmations
can still mean accepted work, and transfer plus source acknowledgement is not
atomic.

`Prefetch` defaults to one and accepts 1–65535; zero restores one. Increasing it
reserves more work but does not run handlers concurrently within one consume
call. `Exchange` selects a different direct exchange. `RetryDelay` supplies a
one-second default when a retry requests zero. `MaxRetryDelay` bounds requested
delays, with zero permitting 4294967295 milliseconds (about 49 days); the default
retry delay must fit that bound. Invalid settings wrap `ErrInvalidConfig`.

If facade settlement fails, the driver logs and returns the error and closes the
consumer channel, releasing unsettled deliveries for possible redelivery.
`Consumer.Run` then cancels its sibling queue consumers and returns after they
stop. This differs from an ordinary handler error whose retry settled successfully.

### Retry Forwarding

Retries use durable per-delay queues with per-message TTL. Positive delays round
up into buckets: seconds through a minute, 15 seconds through an hour, five
minutes through a day, then hours, capped at the TTL maximum. A delay above
`MaxRetryDelay` returns `ErrUnsupportedRetryDelay` without settling the source.
TTL expiry is not exact scheduling; older queued messages can delay forwarding.

> [!WARNING]
> A retry confirmation covers the retry queue, not its later dead-letter
> forwarding to the work exchange. Default forwarding can lose messages if the
> destination is unavailable or unroutable. Provision quorum retry queues and
> protected at-least-once dead-lettering with `reject-publish` overflow, the
> required `stream_queue` feature flag, permissions, bindings, and storage before
> relying on this path. The driver neither selects nor verifies these policies.

Set queue-type defaults before retry queues are created; redeclaration cannot
change an existing type. Even protected forwarding can duplicate work. Retry
queues expire after an unused interval of twice their bucket delay plus one
hour, including while forwarding is blocked. Expiry, deletion, and retention
can therefore still discard work during prolonged outages. See
[RabbitMQ's protected dead-lettering requirements](https://www.rabbitmq.com/docs/4.3/quorum-queues#dead-lettering).

### Borrowed Connections

If your application owns a `*amqp091.Connection` from
`github.com/rabbitmq/amqp091-go`, it must have automatic recovery disabled:

```go
driver, err := rabbitmqqueue.NewFrom(connection, routes)
```

`NewFrom` rejects recovery-enabled connections with `ErrInvalidConfig` without
closing or reconfiguring them. Never enable recovery or reconnect that connection
in place: reused delivery tags could let an old handler settle a different job.
`Start` still declares topology. `Stop` closes the publisher channel, not your
connection; close it yourself after consumers stop. `Connection()` exposes the
native pointer without transferring ownership.

For owned connections, cancellation during startup, readiness, consume setup, or
a blocked SDK write can close the socket, interrupting every channel. Normal
consume cancellation attempts channel-only cleanup after the handler returns,
then aborts an unresponsive owned connection after five seconds. Reconstruct the
driver after transport failure. Borrowed connections are never closed to enforce
cancellation: SDK channel/topology/close RPCs need a responsive peer or
caller-managed socket deadlines/close and can outlast a driver context.

### Running Locally

With Docker installed, start a disposable server:

```sh
docker run --detach --name report-rabbit --publish 127.0.0.1:5672:5672 --env RABBITMQ_DEFAULT_USER=local --env RABBITMQ_DEFAULT_PASS=local-only rabbitmq:4-management
docker exec report-rabbit rabbitmq-diagnostics -q ping
```

Wait for the diagnostic command to succeed. With your worker loading `AMQP_URL`
into `amqpURL` and registering the driver hooks and consumer runner, start it:

```sh
AMQP_URL=amqp://local:local-only@localhost:5672/ go run ./cmd/worker
```

Dispatch a report job from your application and verify its stored result; acceptance
alone does not prove handler execution. Stop and join the worker before removing
only this disposable server and its anonymous volumes:

```sh
docker rm --force --volumes report-rabbit
```

See the [`rabbitmqqueue` API](https://pkg.go.dev/arkoren.dev/driver/rabbitmq/rabbitmqqueue).

## NATS

Choose NATS if you operate JetStream streams and durable pull consumers. Core
NATS alone is insufficient. Install the driver in your application:

```sh
go get arkoren.dev/driver/nats
```

### Connecting

Provision a stream and durable consumer with your operational tooling. Map each
logical queue to a distinct literal subject and durable name:

```go
import "arkoren.dev/driver/nats/natsqueue"

routes := map[string]natsqueue.Queue{
	"reports": {Subject: "jobs.reports", Durable: "reports-worker"},
}
driver, err := natsqueue.New("REPORTS", routes)
```

Construction validates settings without connecting. By default `Start` connects
to `nats://127.0.0.1:4222` and checks resources without creating them. Register
`Stop`, `Start`, and `Ping` on the composer as in RabbitMQ. An owned connection
is drained by `Stop`; context cancellation forces close, including with an
already-expired context. Drain also observes native `DrainTimeout`, preserving
`nats.ErrDrainTimeout`. Repeated stop is harmless; a stopped driver cannot restart.
Cancel and join handlers first.

For local development where resource creation is permitted, use this alternative:

```go
driver, err := natsqueue.New("REPORTS", routes, func(broker *natsqueue.Builder) {
	broker.Provision(true)
})
```

It creates missing work resources only, never repairs incompatible resources and
never creates failure-subject storage. Missing or incompatible resources match
`ErrResourcesNotProvisioned`; authorization, connection, and cancellation errors
retain their own identities. Choose stream storage, replication, and retention
operationally; successful startup does not establish all durability policies.
Failed startup closes a newly owned connection, not a borrowed one.

### Resource Policy

The stream must list each work subject explicitly; a covering wildcard alone is
not sufficient. Consumers must be durable pull consumers with bodies, explicit
acknowledgements, and the configured literal filter. Their `AckWait` and
`MaxAckPending` must match the driver. Positive native batch limits must accommodate
the configured batch, and positive pull-expiry limits must allow ten seconds.
An explicit driver `MaxDeliver` must match too. Each consume binding rechecks the
current durable without creating or overwriting it.

`Batch` defaults to one; `MaxAckPending` defaults to the completed batch size and
cannot be smaller. Handlers run sequentially within a batch. `AckWait` defaults
to 30 seconds; zero restores that default and values below two nanoseconds are
invalid. This floor is not a useful production deadline. Pull expiry is separately
ten seconds or the caller's earlier deadline. `MaxDeliver(0)` leaves the native
cap alone; exhausting a native cap does not transfer the message to a failure
subject.

### Authentication Policy

For owned connections, configure server URLs and native TLS/authentication options
before startup. With `github.com/nats-io/nats.go` imported, an authorized user
NKey seed file named by `seedPath` can supply authentication:

```go
authentication, err := nats.NkeyOptionFromSeed(seedPath)
if err != nil {
	return err
}
```

Pass that option to a new driver, using your TLS-protected `serverURL`:

```go
driver, err := natsqueue.New("REPORTS", routes, func(broker *natsqueue.Builder) {
	broker.URLs(serverURL)
	broker.Options(authentication)
})
```

The helper takes a file path, not seed text; passing seed text as a path can expose
it in filesystem errors. For in-memory keys, use native `nats.Nkey` with a public
key and signing callback. Keep credential-bearing URLs and native options out of
logs. Options and their captured state remain application-owned.

### Processing Work

Use [Dispatching Work](producing-jobs.md#dispatching-work); dispatch waits for
JetStream acceptance. Workers sharing the durable compete. Raw dispatch uses a
nonempty `Message.ID` as `Nats-Msg-Id`, enabling deduplication within the stream's
duplicate window. Facade dispatch generates a fresh ID every time, so resubmitting
the same payload is not automatically deduplicated or exactly-once execution.

All fetched messages receive progress heartbeats every half `AckWait`, including
those waiting behind another handler. Renewal continues through active-handler
drain, but the first unexpected renewal error is logged and stops that delivery's
heartbeat worker. Redelivery can then overlap the original handler. Renewal and
settlement are serialized so successful delayed retry is not overwritten by a
later heartbeat.

Retry uses delayed negative acknowledgement; zero is immediate. Rejection uses
JetStream termination, not a dead-letter transfer. To archive terminal failures,
set `FailureSubject("jobs.report-failures")` in the construction callback and
separately provision JetStream storage for that distinct literal subject. Failure
publication must succeed before source acknowledgement; the two are not atomic.
Without a failure subject, terminal failure logs and acknowledges the message.
Protect archive data and error text with access and retention controls.

Settlement errors are logged without automatically acknowledging or ending
consumption; an unsettled job can return after `AckWait`. Empty pulls and pulls
interrupted by reconnect resume polling; other fetch errors end consumption.
Cancellation stops fetching and leaves unhandled batch messages unacknowledged.

### Borrowed Connections

To reuse an existing `*nats.Conn`, supply it before the stream and routing map:

```go
driver, err := natsqueue.NewFrom(connection, "REPORTS", routes)
```

`Start` still checks or provisions resources. `URLs` and `Options` are ignored;
configure the native connection first. Neither failed startup nor `Stop` closes
or drains a borrowed connection. The caller drains/closes it after consumers
stop. `Connection()` exposes it without transferring ownership; native calls
bypass facade telemetry and routing. Delivered message payload and metadata
snapshots are copied, so edits cannot alter subsequent settlement.

### Running Locally

With Docker installed, start JetStream and enable its monitoring endpoint:

```sh
docker run --detach --name report-nats --publish 127.0.0.1:4222:4222 --publish 127.0.0.1:8222:8222 nats:2 -js -m 8222
curl --fail http://localhost:8222/healthz
```

Wait for health to succeed. Configure your worker with the local construction and
`Provision(true)`, then run its consumer:

```sh
go run ./cmd/worker
```

Successful `Start` confirms compatible work resources. Dispatch a report job from
your application and verify its stored result. This setup is disposable,
not a persistent production deployment. Stop and join the worker before removing
the server and its state:

```sh
docker rm --force --volumes report-nats
```

See the [`natsqueue` API](https://pkg.go.dev/arkoren.dev/driver/nats/natsqueue).

## Amazon SQS

Choose SQS for managed AWS queues and long-poll consumers. Provision work queues
before construction. Producers need `sqs:SendMessage`; consumers need
`sqs:ReceiveMessage`, `sqs:DeleteMessage`, and `sqs:ChangeMessageVisibility`.
A failure archive also needs `sqs:SendMessage` on its destination.

```sh
go get arkoren.dev/driver/aws
```

### Connecting

With your provisioned queue URL `reportURL` and configuration-loading context
`ctx`, construct the driver:

```go
import "arkoren.dev/driver/aws/awsqueue"

driver, err := awsqueue.New(ctx, map[string]string{"reports": reportURL})
```

`New` validates routes and optional settings before loading region and credentials
from the AWS default chain. There are no `Start`, `Stop`, or `Ping` hooks; register
only the consumer runner. Use [Dispatching Work](producing-jobs.md#dispatching-work)
and [Registering Handlers](consuming-jobs.md#registering-handlers) unchanged.

### Polling Policy

Long polling defaults to 20 seconds, with `WaitTimeSeconds` accepting 1–20.
`MaxMessages` defaults to one and accepts 1–10. `VisibilityTimeout` defaults to
30 seconds and accepts one second through 12 hours. Zero restores each default;
invalid settings wrap `ErrInvalidConfig`.

Renewal runs every half configured visibility, including for messages waiting
in a batch and during active-handler shutdown. Wire visibility rounds fractional
seconds upward; heartbeat timing uses the unrounded setting. Each renewal SDK
operation gets the lesser of ten seconds and half the configured visibility,
including SDK retries and serialization wait. Renewal errors are logged and
later ticks can retry, but a lost lease can duplicate work. SQS cannot extend
visibility beyond 12 hours from the receive request; split longer work.

Settlement waits for earlier renewal within its own context budget; successful
settlement prevents further renewal. Failed settlement permits another attempt.
Handler completion cancels heartbeat I/O before joining its worker. Native HTTP,
credential, and middleware dependencies must honor contexts or cleanup can wait
past its intended bound. Settlement errors are logged, not automatically
acknowledged; polling continues until cancellation or a receive error.

### Failure Archives

Provision a separate failure queue if terminal jobs need retained reports. With
its URL `failureURL`, use an alternative construction:

```go
driver, err := awsqueue.New(ctx, map[string]string{"reports": reportURL}, func(broker *awsqueue.Builder) {
	broker.FailureQueueURL(failureURL)
})
```

The driver sends a failure envelope before deleting the source; failed publication
withholds deletion. The operations are not atomic and can duplicate work or reports.
Without this setting, terminal failure logs and deletes the source. Archive
contents include job data, error text, and undecodable raw bodies; restrict access,
set retention, and avoid secrets in errors.

Retry changes visibility, rounding positive fractional seconds up. Values above
12 hours return `ErrUnsupportedRetryDelay`; SQS can reject even an otherwise valid
delay if it exceeds the remaining visibility allowance. The driver does not
shorten it. Rejection leaves the message untouched for the queue's redrive policy.
Without redrive, rejection can return repeatedly; prefer `queue.Permanent` unless
you intentionally rely on configured broker redrive.

### FIFO Messages

With a configured native `client` and separately provisioned FIFO work and failure
URLs, wrap both destinations just as you would standard queues:

```go
driver, err := awsqueue.NewFrom(client, map[string]string{"reports": reportFIFOURL},
	func(broker *awsqueue.Builder) {
		broker.FailureQueueURL(failureFIFOURL)
	})
```

For destination URLs ending in `.fifo`, including failure queues, the driver adds
native group and deduplication fields automatically. The group is the logical
queue name (`failed` if missing); deduplication uses the message ID, or a fresh ID
if missing. Provision FIFO queues yourself and pass their URLs through the same
constructors; the JSON job and failure-envelope formats do not change. The
failure destination must be separate from work queues and have the same access
and retention protections described above.

Each facade dispatch still creates a fresh ID. FIFO deduplication therefore does
not make repeated payloads or handler execution exactly once. The consumer also
accepts SNS notification wrappers whose `Message` contains a queue envelope;
this does not turn arbitrary SNS payloads into typed jobs.

### Native Clients

For a custom region, endpoint, credentials, retries, or HTTP transport, configure
a `*sqs.Client` from `github.com/aws/aws-sdk-go-v2/service/sqs` first:

```go
driver, err := awsqueue.NewFrom(client, map[string]string{"reports": reportURL})
```

All queue settings still apply; nil clients are invalid. `NewFrom` makes no SQS
requests. `Client()` exposes the same borrowed client, which the driver never
closes. Native operations bypass facade encoding and telemetry.

### Running Locally

With Docker and the AWS CLI installed, start a disposable emulator:

```sh
docker run --detach --name report-sqs --publish 127.0.0.1:14568:4566 ministackorg/ministack:1.5.8
curl --fail http://localhost:14568/_ministack/health
```

Wait for health to succeed. In the same shell, use emulator-only credentials and
create a queue:

```sh
export AWS_ACCESS_KEY_ID=000000000000
export AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1
reportURL=$(aws --endpoint-url http://localhost:14568 sqs create-queue --queue-name reports --query QueueUrl --output text)
aws --endpoint-url http://localhost:14568 sqs get-queue-attributes --queue-url "$reportURL" --attribute-names QueueArn
```

The final command returns the queue ARN. Supply `reportURL` to your application.
With the AWS SDK packages below imported, configure a client for this emulator:

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
```

Wrap that client with the queue you created:

```go
driver, err := awsqueue.NewFrom(sqs.NewFromConfig(cfg), map[string]string{"reports": reportURL})
```

These credentials and plaintext endpoint are local-only. Make your existing worker
load `REPORT_QUEUE_URL` into `reportURL` and use the emulator client above:

```sh
REPORT_QUEUE_URL="$reportURL" go run ./cmd/worker
```

Dispatch a report job from your application and verify its stored result. Stop
and join the worker before removing the disposable emulator and its state:

```sh
docker rm --force --volumes report-sqs
```

See the [`awsqueue` API](https://pkg.go.dev/arkoren.dev/driver/aws/awsqueue).

## Cloudflare

Choose Cloudflare for Go services outside Workers that send and pull Cloudflare
Queues messages through REST. Create the queues and obtain an account ID, queue
IDs, and an API token with Queues read/write permission. Each consumed queue needs
an HTTP pull consumer, configured in the dashboard or with
`wrangler queues consumer http add <queue>`. Configure native retry limits and
a dead-letter queue there if you need broker retention after rejection.

```sh
go get arkoren.dev/driver/cloudflare
```

### Connecting

Load `token` and `accountID` from trusted configuration; `reportQueueID` is the
provider's queue ID, not its display name:

```go
import "arkoren.dev/driver/cloudflare/cloudflarequeue"

driver, err := cloudflarequeue.New(token, accountID, map[string]string{"reports": reportQueueID})
```

Construction validates the required inputs without a request and copies routing
before callbacks. There are no lifecycle hooks; run the shared consumer and use
[Dispatching Work](producing-jobs.md#dispatching-work). A producer does not need
an HTTP pull consumer, but a worker does. A message is limited to 128 KB including
the envelope, which base64-encodes payload bytes.

### Lease Policy

Cloudflare cannot renew leases. A handler and the earlier handlers in its fetched
batch must finish before visibility expires to avoid concurrent redelivery.
Keep the default batch size of one for slow jobs. To allow more time, use an
alternative construction with `time` imported:

```go
driver, err := cloudflarequeue.New(token, accountID, map[string]string{"reports": reportQueueID},
	func(broker *cloudflarequeue.Builder) {
		broker.VisibilityTimeout(2 * time.Minute)
	})
```

Also give the job a shorter cooperative execution timeout, leaving time for
settlement. Visibility defaults to 30 seconds and accepts one millisecond through
12 hours; zero restores the default. `BatchSize` accepts 1–100, defaulting to one.
Empty-poll waits start at `PollInterval` (one second), double up to
`MaxPollInterval` (ten seconds), and reset after work arrives. Zero restores these
defaults; negative intervals fail construction, and a positive cap below the
initial interval is raised to it.

### Settlement Results

Cloudflare supplies delivery IDs and attempts. Acknowledgement requires exactly
one native acknowledgement count; retry requires exactly one retry count. HTTP
success, warnings alone, or a count for the other operation do not confirm
settlement. Matching-count warnings are logged without invalidating success.
Errors and unconfirmed counts leave settlement open, but do not prove the remote
operation had no effect.

Retry delays round positive fractions up to seconds and reject values above
24 hours rather than shortening them. Rejection settles locally without a remote
request: the lease expires normally, then native retry/dead-letter policy applies.
Without a dead-letter queue, native retry exhaustion discards the message.
Terminal failure logs and requests deletion; it creates no archive. Settlement
errors are logged and consumption continues; a pull failure after SDK retries
ends consumption. Cancellation checks every batch admission and leaves unused
leases to expire; an active handler must return cooperatively.

### Native Clients

For custom HTTP transport, retries, or middleware, configure a
`*cloudflare.Client` from `github.com/cloudflare/cloudflare-go/v7` first:

```go
driver, err := cloudflarequeue.NewFrom(client, accountID, map[string]string{"reports": reportQueueID})
```

The caller owns the client and transport cleanup. `Client()` exposes that same
pointer; configure shared clients and loggers before concurrent use. All builder
settings apply except `Endpoint`, which affects only the client created by `New`.
Native calls bypass facade telemetry and driver settlement checks. Clients,
native error causes, handler errors, failure causes, and logged warnings are not
recursively sanitized; keep credentials and sensitive payloads out of diagnostics
and apply application logger redaction where needed.

There is no local emulator for this driver's remote REST pull API. Use the
[queue fake](overview.md#testing-jobs) locally and a dedicated Cloudflare
development queue for remote checks, never production messages.

See the [`cloudflarequeue` API](https://pkg.go.dev/arkoren.dev/driver/cloudflare/cloudflarequeue).
