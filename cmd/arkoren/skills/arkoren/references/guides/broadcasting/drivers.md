---
title: Broadcast Drivers
nav: Drivers
description: Choose and connect a broadcast transport, including native resources and disposable local brokers.
order: 20
summary:
  - Compare offline retention, replica sharing, and listener-error redelivery.
  - Connect Memory, Redis, NATS, RabbitMQ, MQTT, and SNS with SQS.
  - Keep owned and borrowed resources separate during startup and shutdown.
  - Verify notifications using isolated local brokers and explicit cleanup.
  - Use raw subscriptions and native clients without assuming facade guarantees.
---

# Broadcast Drivers

Choose a transport by what recipients can miss and how service replicas share
notifications. Memory stays within one process. Redis Pub/Sub and Core NATS suit
live notifications; NATS queue groups can share them among replicas. RabbitMQ
uses temporary AMQP subscriptions. MQTT suits MQTT 5 deployments, but its owned
sessions are not persistent. SNS with subscribed SQS queues retains notifications
for offline services, subject to queue policies.

None of these drivers confirms recipient handling to the publisher. A broker
connection is not an active subscription, and protocol acknowledgement is not
proof of a successful application effect. See [Message Delivery](message-delivery.md)
for the consequences of retries and cancellation.

## Compatibility Matrix

This compares Arkoren's exposed behavior, not every feature of each broker.
Replica sharing means competing for one service's notifications, not receiving
independent fan-out copies. Listener retry means redelivery caused by the
consumer's returned error, not native connection retries.

| Driver | Offline Retention | Replica Sharing | Listener Retry |
| --- | --- | --- | --- |
| [Memory](#memory) | No | No | No |
| [Redis](#redis) | No | No | No |
| [NATS](#nats) | No | Partial — Set the same queue group on replicas. | No |
| [RabbitMQ](#rabbitmq) | No | No | No |
| [MQTT](#mqtt) | No | No | No |
| [SNS with SQS](#sns) | Partial — Subscribed queues retain messages under their retention and redrive policies. | Yes | Partial — Nonpermanent failures remain for native redelivery. |
| [Direct SQS](#direct-sqs) | Partial — A provisioned queue retains messages under its policies. | Yes | Partial — Nonpermanent failures remain for native redelivery. |

The MQTT row describes `New`'s clean session. A caller-owned Paho manager keeps
its native session policy; that does not add a portable persistence guarantee.
With SNS, give each independent service its own queue, then share that queue
among the service's replicas. Direct SQS alone is point-to-point, not fan-out.

## Driver Lifecycle

Import `arkoren.dev/core/service/broadcast` for the facades. Install external
driver modules from your application's module directory. These focused fragments
assume an operation context `ctx`; handle constructor errors before registering
or using a returned driver.

For Redis, NATS, RabbitMQ, and MQTT, an existing application composer can own
connection setup, readiness, and the consumer runner:

```go
composer.Cleanup(driver.Stop)
composer.Startup(driver.Start)
composer.Check("broadcast", driver.Ping)
composer.Run(consumer.Run)
```

Register cleanup before startup so acquired resources are released even after
startup failure. Construct the consumer and register its listeners before the
application runs. `Start` establishes connectivity; `consumer.Run` owns the
subscription. For non-retaining transports, do not publish a required test
notification until consumption is active. There is no shared subscription-ready
method. See [Lifecycle and Ownership](../application/lifecycle-and-ownership.md)
for application supervision.

For Memory, register only `Stop` and `consumer.Run`: `Start` is a no-op and there
is no `Ping`. SNS/SQS has no driver lifecycle hooks; register only the consumer
runner. A publish-only service needs no consumer runner.

Cancel and join consumers and release raw subscriptions before resource cleanup.
Driver shutdown cannot forcibly stop arbitrary application callbacks. Borrowed
native resources stay caller-owned; keep them alive until all users finish.

The local examples use dedicated project names and loopback ports. Run each in
a new directory with the shown Compose file. If its name or port is already in
use, choose another disposable name or port; do not stop an existing deployment
to make room. Never reuse these cleanup commands for production resources.

## Memory

Use the bundled memory driver for asynchronous notifications within one process.
Use the [recording fake](overview.md#testing-publications) when a test only needs
to assert publication. No broker or additional module is required:

```go
import "arkoren.dev/core/service/broadcast/memorybroadcast"

driver := memorybroadcast.New()
publisher := broadcast.NewPublisher(driver)
consumer := broadcast.NewConsumer(driver)
```

Construction starts no work. Register the [order listener](overview.md#listening),
run the consumer, then [publish an order](overview.md#publishing-facts) through
the same driver instance. Only already-running consumers receive the message;
each `Consume` gets its own payload bytes and metadata map. Restarting the
process loses all broker state.

### Delivery Limits

If listeners need a lower concurrency bound, change the shared handler admission
limit at construction:

```go
driver := memorybroadcast.New(func(broker *memorybroadcast.Builder) {
	broker.MaxConcurrentDeliveries(64)
})
```

The default is 1024 simultaneous deliveries across consumers; nonpositive values
restore that default. `Logger` defaults to `slog.Default()` and records delivery
failures without retrying. A full consumer buffer blocks publication until space
is available or the publishing context ends. A publication can fail after some
consumers accepted their copies; fan-out is not an atomic transaction.

Cancellation and `Stop` discard buffered messages and interrupt messages waiting
for a handler slot. Admitted handlers preserve consumer context values but lose
its cancellation and deadline; they can outlive `Consume`. Give each handler its
own work deadline. `Stop` closes admission even with an expired context, then
waits for admitted handlers only until its context ends. It neither cancels
those handlers nor waits for `Consume` calls to return. A later `Stop` can wait
for the same handlers again.

After shutdown, valid operations with live contexts return
`memorybroadcast.ErrBrokerClosed`. An already-canceled context takes precedence.

See the [`memorybroadcast` API](https://pkg.go.dev/arkoren.dev/core/service/broadcast/memorybroadcast).

## Redis

Choose Redis Pub/Sub if you already run Redis and recipients can reload state
after missed notifications. Connected consumers each receive a copy; this driver
does not use Streams, retention, consumer groups, or listener retries.

```sh
go get arkoren.dev/driver/redis
```

### Connecting Redis

`New` constructs an owned client without network I/O. `Start` verifies
connectivity; `Stop` closes that client and waits for subscription goroutines:

```go
import "arkoren.dev/driver/redis/redisbroadcast"

driver := redisbroadcast.New()
publisher := broadcast.NewPublisher(driver)
consumer := broadcast.NewConsumer(driver)
```

The defaults are TCP at `localhost:6379`, no credentials or TLS, and literal
prefix `arkoren.`. Apply the [lifecycle hooks](#driver-lifecycle), then use the
[typed order listener](overview.md#listening) and
[publisher](overview.md#publishing-facts).

On a shared server, choose a distinct namespace. Redis Pub/Sub ignores database
numbers, so `DB` cannot isolate applications:

```go
driver := redisbroadcast.New(func(broker *redisbroadcast.Builder) {
	broker.Addr("localhost:6379")
	broker.Prefix("orders:")
})
```

Prefix bytes are literal, including Redis glob metacharacters.
Set `Username`, `Password`, and `TLS` from your deployment configuration for
authenticated, encrypted connections. Prefixes route messages; they do not
authorize publishers. Use Redis channel ACLs or separate instances for untrusted
applications. Overlapping broad subscriptions can transfer foreign bytes into
the process before envelope rejection. For raw subscribers, use prefixes neither
of which starts with the other, such as `orders:` and `billing:`.

### Native Clients

To reuse an existing `*redis.Client` from `github.com/redis/go-redis/v9`, borrow it
with `NewFrom`:

```go
driver := redisbroadcast.NewFrom(client, func(broker *redisbroadcast.Builder) {
	broker.Prefix("orders:")
})
```

Only `Prefix` and `Logger` apply. Configure native `ContextTimeoutEnabled` before
wrapping the client if operation deadlines must bound command socket I/O; `New`
enables it for its owned client. `Client()` exposes the same pointer without
ownership transfer. End consumers and raw subscriptions before `Stop`, then let
the owner close a borrowed client. Native calls bypass facade encoding and
telemetry. Config formatting redacts `Password`, not direct field access or
native configuration.

Owned `Stop` closes its client even with an expired context, then waits for
subscription goroutines only until that context ends.

### Local Redis

Use Docker in a new disposable directory, with this `compose.yaml`. A named
project avoids operating an existing Compose deployment:

```yaml
services:
  redis:
    image: redis:8-alpine
    ports:
      - "127.0.0.1:16379:6379"
```

Start and verify only this project:

```sh
docker compose -p arkoren-broadcast-redis-demo up -d
docker compose -p arkoren-broadcast-redis-demo exec redis redis-cli ping
```

`PONG` confirms connectivity. Use `broker.Addr("localhost:16379")` in the
constructor, start consumption, then publish `o-1042` and check the indexer's
effect. These are separate checks: connectivity does not prove delivery.

```sh
docker compose -p arkoren-broadcast-redis-demo down --volumes
```

This removes only the disposable project's server and data. See the
[`redisbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/redis/redisbroadcast).

## NATS

Use Core NATS for live cross-process notifications. Set a queue group when replicas
of one service should share notifications. This driver does not use JetStream;
queue groups provide load sharing, not offline retention.

```sh
go get arkoren.dev/driver/nats
```

### Connecting NATS

`New` validates settings without network or file I/O. `Start` connects, and `Stop`
drains and closes the owned connection:

```go
import "arkoren.dev/driver/nats/natsbroadcast"

driver, err := natsbroadcast.New()
```

The default URL is `nats://127.0.0.1:4222` and the subject prefix is `arkoren`.
Handle construction errors, apply [lifecycle hooks](#driver-lifecycle), and wrap
the driver in `broadcast.NewPublisher` and `broadcast.NewConsumer` for the
[order notification](overview.md#listening).

To share notifications among indexer replicas, use the same prefix and group:

```go
driver, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
	broker.Prefix("orders")
	broker.QueueGroup("indexers")
})
```

An independent group receives its own copy. An empty group gives every consumer
a copy. Prefixes must be literal NATS subjects without wildcards, whitespace,
or empty tokens; queue groups cannot contain spaces, tabs, or line breaks.
Enforce broker subject permissions: a prefix is not an authorization boundary.

### Connection Policy

`URLs` accepts multiple failover servers, schemeless hosts, or `nats`, `tls`,
`ws`, and `wss` URLs. Empty lists select the default; empty entries, missing hosts,
and unsupported schemes fail validation without echoing the address. Use TLS and
load credentials from configuration rather than hard-coding them in URLs.

`CredentialsFile` is a JWT/seed file path read by the SDK during connection,
authentication, and potentially reconnect. `NKeySeed` instead takes encoded
user seed material, never a path. The seed string stays in memory for signing;
wiping temporary signing keys does not erase it. Basic authentication applies
only when both `Username` and `Password` are nonempty; `Token` is another option.

Reconnect attempts default to unlimited; `MaxReconnects(0)` also selects that
default. Set a positive limit or `NoReconnect(true)` to change it.
`ReconnectWait(0)` restores two seconds. `Timeout(0)` selects the native
two-second connection timeout, not an operation deadline. Negative wait and
timeout values are invalid. `Logger(nil)` selects `slog.Default()`.

Config's `String`, `GoString`, `LogValue`, and dispatched formatting hide Password,
Token, NKeySeed, and URL userinfo; Username remains visible. Unparseable URLs and
entries without an authority hostname are hidden wholesale. This does not redact
direct fields, serialization, native objects, or arbitrary errors. Go bypasses
formatting methods for invalid `%p` on a Config value and invalid `%w` on values
or pointers. For an externally chosen format string, format `record.String()`,
not the record itself, to avoid exposing raw credentials.

### Native Connections

With an existing nonnil `*nats.Conn` from `github.com/nats-io/nats.go`, borrow it
instead of creating an owned connection:

```go
driver, err := natsbroadcast.NewFrom(connection, func(broker *natsbroadcast.Builder) {
	broker.Prefix("orders")
	broker.QueueGroup("indexers")
})
```

Nil connections and invalid routing match `natsbroadcast.ErrInvalidConfig`.
Only Prefix, QueueGroup, and Logger apply; `Start` does not reconfigure or dial
the supplied connection. Borrowed `Stop` does nothing. Cancel and join consumers,
release raw subscriptions, then drain or close the connection yourself.
`Connection()` returns the borrowed pointer immediately, or nil before owned
startup, without transferring ownership. Native operations bypass envelope checks
and facade telemetry.

Owned `Stop` waits for draining and closure. Its context ending forces close and
returns the context error; native drain timeout remains identifiable as
`nats.ErrDrainTimeout`. Forced close cannot stop an application callback.
Connection closure does not end `Consume`'s context wait: cancel and join consumers
separately. Canceling `Start` abandons dialing and closes any connection obtained
later; native dialing can finish in the background.

Already-canceled Publish, Consume, and Subscribe contexts are rejected before
native access. Later cancellation is not a guarantee that a native publish or
subscription setup is interrupted. Portable `#` patterns become exact and `>`
subscriptions where needed because native NATS `>` excludes zero tokens.

### Local NATS

In a new disposable directory, save this `compose.yaml`:

```yaml
services:
  nats:
    image: nats:2-alpine
    command: ["-m", "8222"]
    ports:
      - "127.0.0.1:14222:4222"
      - "127.0.0.1:18222:8222"
```

Start the isolated project and check its local health endpoint:

```sh
docker compose -p arkoren-broadcast-nats-demo up -d
curl --fail http://localhost:18222/healthz
```

A successful response confirms readiness. Configure
`broker.URLs("nats://localhost:14222")`, start the consumer, then publish `o-1042`
and check the indexer. Remove the disposable server afterward:

```sh
docker compose -p arkoren-broadcast-nats-demo down --volumes
```

See the [`natsbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/nats/natsbroadcast).

## RabbitMQ

Choose RabbitMQ if you already use AMQP topic routing. Each consuming process
gets an exclusive, auto-delete queue with automatic acknowledgement. Offline
subscribers miss notifications; for durable work use the
[RabbitMQ queue driver](../queues/drivers.md#rabbitmq).

```sh
go get arkoren.dev/driver/rabbitmq
```

### Connecting RabbitMQ

Load the credential-bearing URL from configuration; use `amqps://` outside
local development. `New` rejects blank URLs without network I/O:

```go
import (
	"os"

	"arkoren.dev/driver/rabbitmq/rabbitmqbroadcast"
)

driver, err := rabbitmqbroadcast.New(os.Getenv("AMQP_URL"))
```

`Start` connects and declares the topic exchange. Register the
[lifecycle hooks](#driver-lifecycle) and wrap the driver in the facades to use
the [order listener](overview.md#listening). Owned negotiation has a 30-second
budget or the startup context's earlier end. Serialize `Start` and `Stop`.

`Exchange` defaults to `arkoren:events`; `Logger` defaults to `slog.Default()`.
Use separate exchanges or virtual hosts to isolate applications. Shared exchanges
also need topic permissions to restrict publication keys. Envelope routing checks
do not authenticate a publisher or payload.

Publication waits for broker confirmation, retrying once after reopening the
publisher channel on a non-cancellation failure. Losing the first confirmation
can therefore duplicate an accepted publication. Automatic acknowledgement does
not make the entire path at-most-once, and confirmation does not establish
retention or processing.

### Connection Ownership

On an owned connection, canceling a blocking SDK setup or write can close the
socket, interrupting every channel sharing it. Keep unrelated native work on a
separate connection. Confirmation waiting is separately context-aware: its
cancellation does not always close the socket or prove nondelivery.

For an existing `*amqp091.Connection` from
`github.com/rabbitmq/amqp091-go`, use `rabbitmqbroadcast.NewFrom(connection)`.
It returns a driver and error; `Start` still opens a publisher channel and declares
the exchange. `Stop` closes that channel but never the caller's connection.
Borrowed SDK channel-open, topology, write, and close RPCs cannot be forcibly
bounded by the driver's context. A responsive peer, native socket deadlines, or
caller-managed connection close must unblock stuck operations.

`Connection()` exposes the shared pointer without transferring ownership: nil
before owned startup, immediately available when borrowed, and still available
after shutdown. Do not close an owned connection outside `Stop`. Native calls
bypass envelope and lifecycle protections and facade telemetry.

Owned `Stop` is terminal and releases the connection before waiting on channel
handshakes or confirmations. Graceful close has at most five seconds or the
earlier end of its context; even expired contexts force socket release. It does
not wait for application handlers. Consumer channel cleanup has its own five-second
budget and can abort an unresponsive owned socket. A closed subscription makes
`Run` return `rabbitmqbroadcast.ErrSubscriptionClosed` unless cancellation supplies
the exit reason.

### Local RabbitMQ

In a new disposable directory, save this `compose.yaml`. These plaintext
credentials are only for this loopback-bound development server:

```yaml
services:
  rabbitmq:
    image: rabbitmq:4-alpine
    environment:
      RABBITMQ_DEFAULT_USER: local
      RABBITMQ_DEFAULT_PASS: local-only
    ports:
      - "127.0.0.1:15672:5672"
```

Start the isolated project and repeat the diagnostic until successful:

```sh
docker compose -p arkoren-broadcast-rabbitmq-demo up -d
docker compose -p arkoren-broadcast-rabbitmq-demo exec rabbitmq rabbitmq-diagnostics -q ping
```

Set `AMQP_URL=amqp://local:local-only@localhost:15672/` for your local application.
After consumption is active, publish `o-1042` and check the indexer. Remove only
the disposable project's resources:

```sh
docker compose -p arkoren-broadcast-rabbitmq-demo down --volumes
```

See the [`rabbitmqbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/rabbitmq/rabbitmqbroadcast).

## MQTT

Use the experimental MQTT 5 driver with brokers such as Mosquitto, EMQX, or
HiveMQ. `New` owns a clean, nonpersistent session: offline subscribers lose
notifications at every QoS. QoS 2 is a protocol guarantee, not exactly-once
application effects.

```sh
go get arkoren.dev/driver/mqtt
```

### Connecting MQTT

`New` validates broker URLs and settings without network I/O. `Start` creates the
owned Paho manager and waits for its first connection:

```go
import "arkoren.dev/driver/mqtt/mqttbroadcast"

driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"})
```

Handle the error, apply [lifecycle hooks](#driver-lifecycle), then use the facades
for [typed notifications](overview.md#publishing-facts). Multiple URLs allow
failover. Supported schemes are `mqtt`, `tcp`, `mqtts`, `ssl`, `tls`, `mqtt+ssl`,
`tcps`, `ws`, and `wss`. All require a host. TCP/TLS URLs require ports 1–65535
and reject paths and queries; WebSocket URLs preserve paths and queries and can
omit the port. Fragments and URL userinfo are rejected without echoing URL
credentials. Use `mqtts://` and builder `Username`/`Password` outside local
development; passwords apply only with a nonempty username.

### Protocol Settings

The default QoS is 1. `QoS(0)` explicitly selects at-most-once protocol delivery;
1 requests at-least-once and 2 requests exactly-once at the protocol level.
Other values fail construction. QoS 1 and 2 publication waits for protocol
completion and treats negative reason codes as errors, even if Paho returns no
error. A timeout does not prove nondelivery. Listener errors do not request
redelivery or prevent normal acknowledgement.

Already-canceled Publish, Consume, and Subscribe contexts install or send
nothing. Later cancellation of raw setup does not remove its subscription;
call the returned unsubscribe function.

With automatic acknowledgement, QoS 1 acknowledgement follows all matching
callbacks, including failures. A borrowed manager retains any manual policy.
Up to 1024 delivery callbacks run concurrently; saturation blocks admission and
backpressures Paho's receive loop rather than dropping work.

If your broker requires a different keepalive interval, use a duration:

```go
import "time"

driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"},
	func(broker *mqttbroadcast.Builder) {
		broker.KeepAlive(45 * time.Second)
	})
```

Zero restores 30 seconds; negative disables keepalive. Positive values must be
whole seconds from 1 through 65535. The broker can override the interval in its
CONNACK. Numeric `KeepAlive(30)` means 30 nanoseconds, not 30 seconds, and is invalid.
`Logger(nil)` uses `slog.Default()` for panics and failed subscription restoration,
not Paho's native loggers. Config formatting redacts Password, not direct fields
or native data.

Names map dots to slashes, so `order.created` uses MQTT topic `order/created`.
Pattern `*` maps to `+`, and final `#` remains `#`. Enforce broker topic ACLs or
separate namespaces for untrusted publishers. Routing checks do not authorize
publishers.

### Native Managers

To customize TLS, native logging, or session policy, borrow a Paho manager. The
driver's filters belong to the whole native client; do not share those filters
with unrelated subscriptions because cleanup unsubscribes them for that client.
Only QoS and Logger apply to `NewFrom`; invalid QoS is reported at use because
that constructor has no error result.

With your existing `autopaho.ClientConfig` named `config`, install both driver
hooks before native construction:

```go
import (
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

driver := mqttbroadcast.NewFrom(nil)
config.ClientConfig.OnPublishReceived = append(config.ClientConfig.OnPublishReceived, driver.HandlePublish)
previous := config.OnConnectionUp
config.OnConnectionUp = func(manager *autopaho.ConnectionManager, reply *paho.Connack) {
	if previous != nil {
		previous(manager, reply)
	}
	driver.HandleConnectionUp(manager, reply)
}
```

Callbacks must return promptly. `HandleConnectionUp` schedules filter restoration
without waiting in the connection callback; failed restoration is logged once,
not retried on the same connection.

Create the manager under a separately owned, long-lived `sessionCtx`. With an
owning application `composer`, register cleanup before waiting for connection:

```go
import (
	"context"
	"errors"
)

manager, err := autopaho.NewConnection(sessionCtx, config)
if err != nil {
	return err
}
driver.SetClient(manager)
composer.Cleanup(func(cleanup context.Context) error {
	return errors.Join(driver.Stop(cleanup), manager.Disconnect(cleanup))
})
err = manager.AwaitConnection(ctx)
```

The native owner must disconnect the manager even if `AwaitConnection` fails.
The cleanup stops the driver before disconnecting, and preserves both errors.
Do not make a request context own a shared connection. `SetClient` attaches once,
installs no hooks, and transfers no ownership. `Client()` exposes the pointer;
native operations bypass facade encoding and telemetry. With a borrowed manager,
`Start` does nothing.

Cancel and join consumers, then call `driver.Stop(cleanup)` before
`manager.Disconnect(cleanup)`, using a fresh bounded cleanup context and handling
both errors. `Stop` is terminal, drains admitted callbacks and reconnect work
until its context ends, clears local registrations, and attempts filter cleanup
even after a drain timeout. It disconnects owned managers only. Borrowed filter
cleanup uses a separate ten-second acknowledgement timeout; native writes and
arbitrary callbacks can outlive context deadlines. A failed unsubscribe may
require the native owner to disconnect the session. `Stop` does not end
`Consume`'s context wait.

### Local MQTT

In a new disposable directory, save this `compose.yaml`. The anonymous plaintext
server is intentionally bound only to loopback:

```yaml
services:
  mqtt:
    image: eclipse-mosquitto:2
    command: ["mosquitto", "-c", "/mosquitto-no-auth.conf"]
    ports:
      - "127.0.0.1:11883:1883"
```

Start only this project:

```sh
docker compose -p arkoren-broadcast-mqtt-demo up -d
```

Connect with `mqttbroadcast.New([]string{"mqtt://localhost:11883"})`.
Successful `Start(ctx)` confirms connectivity. Run the consumer, publish
`o-1042`, and check the indexer to verify envelope delivery, not just connection.

```sh
docker compose -p arkoren-broadcast-mqtt-demo down --volumes
```

See the [`mqttbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/mqtt/mqttbroadcast).

## SNS

Use SNS with SQS when independent services must receive notifications published
while they are offline. Provision an SNS topic, one SQS queue per service, and
subscriptions first; the drivers create none of these resources. Retention,
visibility, and redrive remain queue policies, not facade guarantees.

```sh
go get arkoren.dev/driver/aws
```

### Connecting AWS

The SNS driver only publishes. Map each logical broadcast name to an existing
topic ARN; the default AWS region and credential chain supplies configuration:

```go
import "arkoren.dev/driver/aws/awsbroadcast"

outbox, err := awsbroadcast.NewSNS(ctx, map[string]string{
	"order.created": "arn:aws:sns:eu-west-1:123456789012:orders-created",
})
```

Wrap `outbox` with `broadcast.NewPublisher`. The SQS driver receives from one
service's subscribed queue:

```go
inbox, err := awsbroadcast.NewSQS(ctx, func(broker *awsbroadcast.SQSBuilder) {
	broker.QueueURL("https://sqs.eu-west-1.amazonaws.com/123456789012/order-indexer")
})
consumer := broadcast.NewConsumer(inbox)
```

Register the [order listener](overview.md#listening) and consumer runner; neither
AWS driver has Start, Stop, or Ping hooks. Construction does not verify resource
existence, subscriptions, or permissions. Grant the publisher `sns:Publish`, the
consumer `sqs:ReceiveMessage` and `sqs:DeleteMessage`, and the SNS topic permission
to send to the queue through its queue policy. Use IAM and queue policies for
authorization; local listener patterns are not access controls.

### Queue Policies

The SNS map must be nonempty, with valid broadcast names and nonblank ARNs.
SQS requires a consuming `QueueURL`, publishing `Queues`, or both. Invalid
settings wrap `awsbroadcast.ErrInvalidConfig`; unmapped publication names match
`awsbroadcast.ErrUnknownName`. Constructors copy routing maps. Nonblank ARN and
URL syntax and actual permissions are verified by AWS when used.

SQS polls for 20 seconds and receives one message by default.
`WaitTimeSeconds` accepts 1–20, `MaxMessages` accepts 1–10, and zero restores
their defaults. `Logger(nil)` uses `slog.Default()` for invalid deliveries,
handler errors, and deletion failures. It processes a batch sequentially and does
not extend visibility while listeners run. Set the queue's visibility timeout to
cover the entire batch, or another replica can process a delivery still being
handled.

Successful messages and malformed envelopes are deleted; so are deliveries whose
listener failures are all permanent. Other failures remain for native redelivery
to every matching listener. Even successful handling can repeat if deletion
fails. Make effects idempotent and configure queue redrive for messages that keep
failing. Permanent failures are deleted here, not moved to a dead-letter queue by
the driver. Deletion uses a detached ten-second budget so cancellation after
handling does not by itself prevent acknowledgement; failed deletion is logged.

SNS supplies the message attribute `name`, so a subscription filter policy such
as `{"name":["order.created"]}` can select names before queue delivery. This is
broker configuration, separate from consumer patterns. Full broadcast envelopes
arrive either directly or inside an SNS notification wrapper. Their payload bytes
use the envelope's base64 JSON representation, not a bare order object.

### Direct SQS

If only one service consumes a notification, publish directly to its provisioned
queue instead of adding an SNS topic. With its URL named `queueURL`:

```go
outbox, err := awsbroadcast.NewSQS(ctx, func(broker *awsbroadcast.SQSBuilder) {
	broker.Queues(map[string]string{"order.created": queueURL})
})
publisher := broadcast.NewPublisher(outbox)
```

Replicas on that queue compete for messages. Use SNS subscriptions instead for
independent fan-out copies.

SNS ARNs and SQS queue URLs ending in `.fifo` cause publications to group by
broadcast name. Native deduplication uses `Message.ID` if supplied directly to
the driver, otherwise a fresh random ID. Facade publications supply no ID:
repeating `publisher.Publish` does not deduplicate an application event, even on
FIFO destinations.

### Native Clients

Use `NewSNSFrom` or `NewSQSFrom` with existing clients from the AWS SDK v2
`service/sns` or `service/sqs` packages to customize region, endpoints, credentials,
and native policy. Nil clients are rejected. `Client()` exposes the pointer
without ownership transfer. The drivers have no client-close hooks; native
configuration and transport cleanup remain caller-owned. Native calls bypass
facade encoding and telemetry.

### Local SNS

Use Docker, AWS CLI, and Python 3 in a new disposable directory. Save this
`compose.yaml`; only SNS/SQS run, bound to loopback:

```yaml
services:
  aws:
    image: localstack/localstack:4
    environment:
      SERVICES: sns,sqs
    ports:
      - "127.0.0.1:14568:4566"
```

Start the isolated project and check its health:

```sh
docker compose -p arkoren-broadcast-aws-demo up -d
curl --fail http://localhost:14568/_localstack/health
```

Wait for SNS and SQS to be available. In a disposable shell, disable AWS profiles
and use dummy local credentials. Every provisioning command below explicitly
targets the emulator, never a production account:

```sh
unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=eu-west-1 AWS_EC2_METADATA_DISABLED=true
queueURL=$(aws --endpoint-url http://localhost:14568 sqs create-queue --queue-name order-indexer --query QueueUrl --output text)
topicARN=$(aws --endpoint-url http://localhost:14568 sns create-topic --name orders-created --query TopicArn --output text)
```

These commands return routing values for the local clients. The following policy
replacement is only for this newly created disposable queue. Retrieve its ARN,
then authorize this topic:

```sh
queueARN=$(aws --endpoint-url http://localhost:14568 sqs get-queue-attributes --queue-url "$queueURL" --attribute-names QueueArn --query Attributes.QueueArn --output text)
policy=$(python3 -c 'import json,sys; print(json.dumps({"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":sys.argv[1],"Condition":{"ArnEquals":{"aws:SourceArn":sys.argv[2]}}}]}))' "$queueARN" "$topicARN")
attributes=$(python3 -c 'import json,sys; print(json.dumps({"Policy":sys.argv[1]}))' "$policy")
aws --endpoint-url http://localhost:14568 sqs set-queue-attributes --queue-url "$queueURL" --attributes "$attributes"
```

Subscribe the queue so SNS can route publications into it:

```sh
aws --endpoint-url http://localhost:14568 sns subscribe --topic-arn "$topicARN" --protocol sqs --notification-endpoint "$queueARN"
```

For local Go code, configure the native SDK with static dummy credentials and
explicit endpoints, rather than relying on your normal AWS profile:

```go
import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

cfg := aws.Config{
	Region:      "eu-west-1",
	Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
}
client := sns.NewFromConfig(cfg, func(options *sns.Options) {
	options.BaseEndpoint = aws.String("http://localhost:14568")
})
queue := sqs.NewFromConfig(cfg, func(options *sqs.Options) {
	options.BaseEndpoint = aws.String("http://localhost:14568")
})
```

Supply the CLI results as Go strings `topicARN` and `queueURL`, then construct
the drivers without provisioning anything further:

```go
outbox, err := awsbroadcast.NewSNSFrom(client, map[string]string{
	"order.created": topicARN,
})
inbox, err := awsbroadcast.NewSQSFrom(queue, func(broker *awsbroadcast.SQSBuilder) {
	broker.QueueURL(queueURL)
})
```

Use the facades to publish `o-1042` and check the indexer's effect. A successful
SNS publication alone does not verify subscription delivery. Remove only this
disposable project's emulator, topics, subscriptions, and queues afterward:

```sh
docker compose -p arkoren-broadcast-aws-demo down --volumes
```

See the [`awsbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/aws/awsbroadcast).

## Raw Subscriptions

Redis, NATS, RabbitMQ, and MQTT also expose `Subscribe` for native producers that
do not send Arkoren envelopes. With one of these drivers and
`handleBytes func([]byte)`, own the returned resource explicitly:

```go
unsubscribe, err := driver.Subscribe(ctx, "order.#", handleBytes)
if err != nil {
	return err
}
err = unsubscribe()
```

Keep the subscription alive for the receiving scope; this final call illustrates
its cleanup. The return type is `func() error`, unlike the facade's
`broadcast.Unsubscribe`. Handle cleanup errors. Raw subscriptions bypass envelope
validation, typed listener routing, and facade telemetry. Publications from these
drivers arrive as full JSON `broadcast.Message` envelopes, not bare application
payloads: payload bytes use the envelope's base64 JSON representation and metadata
stays in the body, not protocol headers.

The context governs raw setup; canceling it later is not a substitute for calling
unsubscribe. NATS and MQTT raw callback bytes are borrowed/read-only: copy them
before retaining or mutating. RabbitMQ raw unsubscribe waits for its running
callback and channel-close RPC; callbacks must return, and a borrowed connection
needs owner-managed interruption if the RPC stalls.

MQTT unsubscribe is idempotent, returns its original cleanup result, and removes
only its registration. The broker filter remains until the last matching local
registration leaves. Subscription changes and reconnect restoration serialize so
old cleanup cannot remove a replacement registration.

Normal `Consume` validates envelope/routing identity before invoking facade
listeners. Redis checks the literal channel suffix, NATS the prefixed subject,
RabbitMQ the routing key, and MQTT the mapped topic. Malformed or mismatched
envelopes are dropped before facade processing telemetry. RabbitMQ drops remain
auto-acknowledged; MQTT follows its native acknowledgement path. These checks
prevent routing confusion, not unauthorized publication.

For the portable contracts, see the
[`broadcast` API](https://pkg.go.dev/arkoren.dev/core/service/broadcast).
