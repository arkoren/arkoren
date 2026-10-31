---
title: Broadcast Drivers
nav: Drivers
description: Choose between the memory, Redis, NATS, RabbitMQ, MQTT, and SNS broadcast drivers, and install, connect, and run each.
order: 30
---

# Broadcast Drivers

## Introduction

Topics, listeners, and patterns stay portable. Choose by whether offline services
need missed events or can reload current state.

Use memory in-process, Redis for existing Pub/Sub, NATS for lightweight or
queue-group notifications, RabbitMQ for AMQP topics, MQTT for devices, and SNS
with SQS for offline retention.

Memory delivers asynchronously in process. Redis and NATS deliver at most once;
RabbitMQ uses ephemeral, auto-acknowledged subscriptions, and MQTT delivery
depends on QoS. With these default configurations, offline subscribers miss
messages. RabbitMQ publication retries can duplicate a message when its first
broker confirmation is lost.
SNS with SQS provides at-least-once delivery and retains offline messages.

Install from your application's module directory. Wrap each driver in
`broadcast.NewPublisher` and `broadcast.NewConsumer`. Register listeners before
running the consumer, and register driver cleanup before startup as in the
[overview](overview.md#listening). Connection alone does not activate a consumer
subscription; publishing success does not confirm listener processing.

At-most-once delivery can lose messages. [Listener failures](overview.md#delivery)
never reach publishers. Redelivery repeats all matching listeners: make effects idempotent.
Drivers do not emit events; listener failure notifications come from the consumer
facade, not the transport.

## Compatibility matrix

The comparison covers behavior exposed by these drivers, not every broker
feature. Replica sharing means competing consumers rather than independent
fan-out copies; NATS requires a queue group. The MQTT row describes `New`'s owned,
clean session; a caller-owned Paho manager retains its native session policy.

| Driver | Offline retention | Replica sharing |
| --- | --- | --- |
| [Memory](#memory) | No | No |
| [Redis](#redis) | No | No |
| [NATS](#nats) | No | Partial — Requires the same queue group on replicas. |
| [RabbitMQ](#rabbitmq) | No | No |
| [MQTT](#mqtt) | No | No |
| [SNS with SQS](#sns) | Partial — Requires subscribed queues; retention and redrive policies apply. | Yes |
| [Direct SQS](#publishing-directly-to-sqs) | Partial — Requires a provisioned queue; retention and redrive policies apply. | Yes |

SNS fan-out needs subscriptions: use one queue per independent service, shared
among its replicas. Direct SQS has competing consumers; queue policies govern retention.

Redis, NATS, RabbitMQ, and MQTT offer raw subscriptions for non-envelope producers.
With `handleBytes` of type `func([]byte)`, call
`driver.Subscribe(ctx, "order.#", handleBytes)`. Handle setup errors and call the
returned unsubscribe function when finished. This bypasses facade decoding,
listener routing, and telemetry without adding retention or retries. See provider
sections for routing and ownership constraints.

## Memory

Memory is included in `arkoren.dev/core` for single-process development.
For tests that only assert publications, use [the broadcast fake](overview.md#testing).

### Connecting

`New` never fails or starts work. `Start` does nothing; `Stop` shuts the broker down:

```go
import "arkoren.dev/core/service/broadcast/memorybroadcast"

driver := memorybroadcast.New()
publisher := broadcast.NewPublisher(driver)
consumer := broadcast.NewConsumer(driver)
```

Register `Stop` for cleanup after consumers stop. It closes admission and waits
for admitted handlers only until its context ends; those handlers must bound
their own work. See [application wiring](overview.md#wiring-into-the-application).

### Configuration

`memorybroadcast.Builder.MaxConcurrentDeliveries` caps concurrent message deliveries
across consumers (default 1024). `Builder.Logger` logs listener failures (default `slog.Default()`):

```go
driver := memorybroadcast.New(func(broker *memorybroadcast.Builder) {
	broker.MaxConcurrentDeliveries(64)
})
```

### Publishing and receiving notifications

With this `driver`, follow [Publishing](overview.md#publishing) and
[Listening](overview.md#listening) to register the order listener and run the
consumer before publishing; only running consumers receive notifications.

### Behavior and limits

Delivery is asynchronous and only reaches consumers already running. Each
running `Consume` receives its own copy of the payload bytes and metadata map;
this is per consumer, not per listener within a consumer. A full consumer buffer
blocks `Publish` until space becomes available or its context ends.

A message waiting for a shared handler slot is not yet in flight. Canceling
`Consume` interrupts that wait with the context error; shutdown interrupts it
with `memorybroadcast.ErrBrokerClosed`. Buffered messages and messages waiting
for a slot are not drained into handlers after cancellation or shutdown.
Already-admitted handlers keep the consumer context's values, but not its
deadline or cancellation. `Consume` returns without waiting for those handlers.

`Stop` closes admission and signals consumers even when its own context is
already expired. It waits for admitted handlers only until that context ends,
does not cancel their contexts, and does not wait for `Consume` calls to return.
An admitted handler can start or finish after `Stop` returns. A later `Stop` can
wait for the same handlers again. Listener errors are logged without retries.

With valid inputs and a live context, new `Publish` and `Consume` calls after
`Stop` return `memorybroadcast.ErrBrokerClosed`. An already-canceled context
takes precedence, including after `Stop` and before `Consume`'s nil-handler
check.

### API references

See the [`memorybroadcast` API](https://pkg.go.dev/arkoren.dev/core/service/broadcast/memorybroadcast).

## Redis

Use Redis Pub/Sub for cross-process notifications if you run Redis and can lose
messages during subscriber downtime.

### Installation

```sh
go get arkoren.dev/driver/redis
```

### Connecting

Register driver [lifecycle](../application/lifecycle-and-ownership.md) hooks and
wrap it in `broadcast.NewPublisher` and `broadcast.NewConsumer` as in the
[overview](overview.md#publishing).

`New` never fails or performs I/O. `Start` checks connectivity; misconfiguration
fails startup:

```go
import "arkoren.dev/driver/redis/redisbroadcast"

driver := redisbroadcast.New()
```

This uses `localhost:6379` and the `arkoren.` prefix. Register `Stop` for cleanup
before `Start` for startup, and use `Ping` for readiness. On a shared server,
choose a distinct prefix in a construction callback:

```go
driver := redisbroadcast.New(func(broker *redisbroadcast.Builder) {
	broker.Prefix("myapp.")
})
```

### Configuration

Default address: `localhost:6379`. Set `Username`, `Password`, and `TLS` for
authentication/TLS; load secrets from configuration. `Prefix` defaults to literal
`arkoren.`. Pub/Sub ignores database numbers: use disjoint prefixes such as
`billing:` and `shipping:` on shared Redis.

> [!WARNING]
> Prefixes route but do not authorize. For mutually untrusted applications, use
> Redis channel ACLs or separate instances. Database numbers do not isolate Pub/Sub;
> overlapping broad subscriptions can receive foreign bytes before driver rejection.

To share a client with another component, build it yourself and set its namespace:

```go
driver := redisbroadcast.NewFrom(client, func(broker *redisbroadcast.Builder) {
	broker.Prefix("myapp.")
})
```

You own the client; only Prefix and Logger apply.

### Publishing and receiving notifications

Use the [order publisher](overview.md#publishing) and
[order listener](overview.md#listening) with this driver. Connected consumers
each receive a copy; later consumers miss it.

### Behavior and limits

Redis has no persistence, retries, or consumer groups: connected replicas each
receive a copy. `Stop` awaits subscription goroutines and closes only `New`'s
client. Listener panics are recovered and logged.

`Consume` drops malformed envelopes and names that differ from the channel
suffix after literal prefix removal, before invoking listeners or telemetry.
The [API caveats](drivers.md#behavior-and-limits-1) explain
overlapping-prefix behavior and portable wildcard matching.

Raw `Subscribe` skips envelope validation: avoid overlapping application prefixes
and call its unsubscribe function. `Consume` unsubscribes when its context ends.

### Using a shared Redis client

`NewFrom` borrows a `*redis.Client` from `github.com/redis/go-redis/v9`.
`driver.Client()` exposes native operations without automatic envelopes or facade
telemetry. Close the borrowed client after consumers stop.

### Running locally

With Docker installed, save this as `compose.yaml` in your application:

```yaml
services:
  redis:
    image: redis:8-alpine
    ports:
      - "127.0.0.1:6379:6379"
```

Run `docker compose up -d`, then `docker compose exec redis redis-cli ping`;
`PONG` confirms readiness. For host-local Go, set `broker.Addr("localhost:6379")`.
After consumer startup, publish an order and check the listener's effect.
`docker compose down --volumes` removes the server and disposable in-memory state.

### API references

See the [`redisbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/redis/redisbroadcast).

## NATS

Core NATS needs a reachable server and suits lightweight notifications, with
queue groups for replica sharing.

### Installation

```sh
go get arkoren.dev/driver/nats
```

### Connecting

`New` validates settings without network or file I/O; handle its error before
registering hooks. `Start` connects; `Stop` drains and closes the owned connection:

```go
import "arkoren.dev/driver/nats/natsbroadcast"

driver, err := natsbroadcast.New()
```

This selects the local default URL. Register `Stop` for cleanup before `Start`
for startup, and use `Ping` for readiness. Configure TLS and authentication before
connecting to a production server.

### Configuration

`Builder.URLs` accepts failover servers, including schemeless hosts and the
`nats`, `tls`, `ws`, and `wss` schemes. An omitted or empty list selects
`nats://127.0.0.1:4222`; an empty entry, absent host, malformed URL, or unsupported
scheme is invalid. URL validation errors omit the supplied address because it
can contain credentials. Use TLS and load secrets from configuration for
production rather than embedding them in URLs.

`Builder.NKeySeed` takes encoded NATS **user seed material**, not a filename.
`New` validates a nonempty seed without file I/O and retains it for signing
server nonces; temporary signing keys are wiped, not the retained seed string.
An empty seed omits this authentication mechanism. For a JWT/seed file, use
`Builder.CredentialsFile` instead: the SDK reads that path during
`Start`/authentication and may read it again on reconnect. Builder-based basic
authentication applies only when both `Username` and `Password` are nonempty;
`Token` is another authentication option.

`Prefix` defaults to `arkoren`, including when set to empty. It must be a literal
NATS subject without wildcards, spaces, tabs, line breaks, or empty dot-separated
tokens. `QueueGroup` must not contain spaces, tabs, or line breaks.

Reconnect attempts default to unlimited; `MaxReconnects(0)` also selects that
default, a positive value limits attempts, and `NoReconnect(true)` disables
reconnection. `ReconnectWait(0)` restores two seconds. `Timeout(0)` selects the
native two-second connection timeout, not an operation deadline. `New` rejects
negative `ReconnectWait` and `Timeout` values. Nil `Logger` selects `slog.Default()`.

`Config` is diagnostic data, not constructor input. For configuration logs, pass
your `natsbroadcast.Config` record directly to `slog` or call `record.String()`.
These diagnostics hide nonempty `Password`, `Token`, `NKeySeed`, and parsed URL
authority userinfo; `Username` remains visible. URL entries that fail parsing or
lack an authority hostname, including schemeless server entries, are replaced
wholesale. Formatting copies the URL slice without changing the original record
or native connection inputs.

`Config.Format` also protects numeric, boolean, and unknown `fmt` verbs dispatched
to it. Go itself bypasses all formatting methods for invalid `%p` on a `Config`
value and invalid `%w` on `Config` values or pointers; those format-error
representations can expose raw fields. If the format string is externally
chosen, pass the already-redacted string to `fmt`:
`fmt.Sprintf(format, record.String())`, not `fmt.Sprintf(format, record)`.

Diagnostic redaction does not apply to direct field access, serialization of the
record, or method-free type conversions. It does not erase original values or
recursively sanitize native objects or arbitrary errors.

Every subscriber gets every message by default. For replica sharing, use one queue group:

```go
driver, err := natsbroadcast.New(func(broker *natsbroadcast.Builder) {
	broker.QueueGroup("billing")
})
```

### Sharing notifications among replicas

Use the [order publisher](overview.md#publishing) and
[order listener](overview.md#listening) with this driver. Replicas using
`QueueGroup("billing")` share each event; a different group receives its own
copy. A queue group adds load sharing, not persistence.

### Behavior and limits

Core NATS has no persistence. `#` becomes exact plus `>` subscriptions because
NATS `>` excludes zero tokens, preserving portable matching.

`Consume` drops malformed envelopes and subject/name mismatches before invoking
listeners or telemetry. Raw `Subscribe` does not validate envelopes.

Already-canceled `Publish`, `Consume`, and `Subscribe` contexts return their
context error before native access. This is admission checking, not a guarantee
that later cancellation interrupts a native publish or subscription setup.
Raw `Subscribe` uses its context only for setup; call the returned unsubscribe
function to release it. Callback bytes are borrowed: copy bytes you retain or
mutate. `Consume` unsubscribes when its context ends, without waiting for an
application callback already running.

For an open owned connection, `Stop` waits for native subscription and publisher
draining and connection closure. If its context ends, including before the call,
it force-closes the connection and returns the context error. Drain setup failure
also closes the connection. A native drain timeout closes it and remains
identifiable as `nats.ErrDrainTimeout`. Forced close cannot stop arbitrary
application callbacks. Cancel and join `Consume` calls separately: closing the
connection does not end their context-waiting loops.

Canceling `Start` abandons the connection attempt and closes any connection
obtained later; native dialing may finish in the background. Borrowed `Stop`
does nothing, including no subscription cleanup; its owner handles draining and
closure as described below.

Enforce broker subject permissions: prefixes are not authorization. Use
[bounded metric retention](../observability/telemetry-drivers.md#prometheus)
to limit retained process memory when authorized publishers can create many subjects.

### Using an existing NATS connection

For custom native connection settings, borrow an existing nonnil `*nats.Conn`
from `github.com/nats-io/nats.go`. With that connection named `connection`, use
this fragment in a function returning `error`; handle the constructor error
before using the driver:

```go
driver, err := natsbroadcast.NewFrom(connection, func(broker *natsbroadcast.Builder) {
	broker.Prefix("billing")
	broker.QueueGroup("indexers")
})
```

`NewFrom` returns `(*Driver, error)` and rejects nil connections or invalid
routing with `natsbroadcast.ErrInvalidConfig`. Only `Prefix`, `QueueGroup`, and
`Logger` apply; connection/authentication settings are ignored, and it never
dials or reconfigures the supplied connection. With a live context, `Start` is
a no-op. The caller keeps ownership: cancel and join consumers, call raw
unsubscribe functions, then drain or close the native connection. Borrowed
`Stop` is not a substitute for that cleanup.

`driver.Connection()` returns the shared native pointer without transferring
ownership: nil before owned `Start`, or the supplied connection immediately
after `NewFrom`. Do not close an owned connection outside `Stop`; keep a
borrowed connection alive while the driver uses it. Native operations bypass
envelope checks and facade telemetry.

### Running locally

With Docker installed, save this as `compose.yaml` in your application:

```yaml
services:
  nats:
    image: nats:2.14.6
    command: ["-m", "8222"]
    ports:
      - "127.0.0.1:4222:4222"
      - "127.0.0.1:8222:8222"
```

Run `docker compose up -d`, then `curl --fail http://localhost:8222/healthz`.
Use `broker.URLs("nats://localhost:4222")`. After consumer startup, publish an order
and check the listener's effect. `docker compose down --volumes` removes the disposable server.

### API references

See the [`natsbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/nats/natsbroadcast).

## RabbitMQ

Choose RabbitMQ for existing AMQP topic routing. Each consuming process gets an
ephemeral queue. For durable work, use the
[queue driver](../queues/drivers.md#rabbitmq).

### Installation

```sh
go get arkoren.dev/driver/rabbitmq
```

### Connecting

`New` rejects blank AMQP URLs without network I/O. `Start` connects and declares
the exchange. On this owned connection, cancellation during a blocking SDK call
can close the socket and interrupt **every channel** sharing it, including native
work obtained through `Connection()`. Keep unrelated work on another connection:

```go
import (
	"os"

	"arkoren.dev/driver/rabbitmq/rabbitmqbroadcast"
)

driver, err := rabbitmqbroadcast.New(os.Getenv("AMQP_URL"))
```

Register `Stop` for cleanup before `Start` for startup, and `Ping` for readiness.

The URL carries credentials; load it from the environment or a secret manager and use
`amqps://` outside development. Owned TCP/TLS/AMQP negotiation has a 30-second
budget or the earlier end of the startup context. Serialize `Start` and `Stop`.

### Configuration

The only settings are `Builder.Exchange` (topic exchange, default `arkoren:events`)
and `Builder.Logger`. Isolate applications with separate exchanges or virtual hosts.

### Publishing and receiving notifications

Use the [order publisher](overview.md#publishing) and
[order listener](overview.md#listening) with this driver and the same exchange
in both processes. Connected consumers each get a copy, not competing jobs.

`Publish` JSON-encodes the complete `broadcast.Message`, including metadata, in
the AMQP body. Native publishers targeting `Consume` must send that envelope
with its `Name` matching the routing key, not just the application `Payload`.
Payload bytes use the envelope's base64 JSON representation. Raw `Subscribe`
passes through the native body unchanged; driver publications therefore arrive
as complete envelopes, without validation or typed decoding.

### Behavior and limits

Subscriber queues are exclusive and auto-delete; deliveries are auto-acknowledged.
Offline subscribers lose messages. A closed subscription makes `Run` return
`rabbitmqbroadcast.ErrSubscriptionClosed`; an application runner failure lets
your platform restart the service.

Publication waits for broker confirmation and retries once on a non-cancellation
error after reopening the publisher channel. If the broker accepted the first
publication but its confirmation failed, the retry can produce a duplicate.
Do not treat auto-acknowledged consumption as an end-to-end at-most-once guarantee.

Cancellation while waiting for confirmation is separately context-aware; it
does not always close the socket or establish whether the broker accepted the
message. Blocking SDK setup/write calls on owned connections can instead be
interrupted by closing the socket, affecting all channels as described above.

`Consume` drops malformed envelopes and routing-key/name mismatches before
listeners or telemetry run. Dropped messages remain auto-acked. Raw `Subscribe`
does not validate envelopes.

For an owned connection, `Stop` marks the driver closed and releases the
connection directly, before any publisher-channel close handshake or
confirmation wait. It does not wait for application handlers. Graceful close
has at most five seconds or the earlier end of its context; an already-expired
context still interrupts the socket and returns the context error. `Stop` is
terminal, and repeated calls return nil.

`Consume` cancels its subscription and drains unread deliveries during channel
cleanup. Owned channel cleanup has a separate five-second budget and may abort
the entire owned socket to unblock a stalled close. Neither closing the socket
nor canceling a context can forcibly stop application code. Raw unsubscribe
waits for its running callback and a native channel-close RPC, so callbacks must
return for cleanup to finish.

Use broker permissions and separate exchanges or virtual hosts for untrusted
publishers. Shared exchanges also need topic permissions; routing checks do not
authorize publishers.

### Using a shared AMQP connection

`rabbitmqbroadcast.NewFrom(connection)` borrows a `*amqp091.Connection` from
`github.com/rabbitmq/amqp091-go`. Handle errors and register the same hooks.
`Start` still opens the publisher channel and declares the exchange. `Stop`
closes only that channel, never the caller's connection, and does not wait for
handlers or confirmations.

Native setup, publish-write, and channel-close RPCs on borrowed connections are
not forcibly cancelable by the driver. They need a responsive peer or
caller-managed socket deadlines/connection close; a context deadline alone
cannot bound a stuck RPC, including borrowed `Stop` or raw unsubscribe. Normally,
cancel and join consumers and call raw unsubscribe functions before stopping
the driver and closing the connection. If an RPC is unresponsive, its owner may
need to close the connection to unblock that cleanup.

`driver.Connection()` exposes the shared pointer without ownership transfer:
nil before successful owned startup, immediately available for `NewFrom`, and
still available after `Stop`. An owned connection is then closed; a borrowed
connection still belongs to its caller. Native operations bypass envelope and
driver lifecycle protections and facade telemetry.

### Running locally

With Docker installed, save this as `compose.yaml` in your application:

```yaml
services:
  rabbitmq:
    image: rabbitmq:4.3.5-management-alpine
    environment:
      RABBITMQ_DEFAULT_USER: local
      RABBITMQ_DEFAULT_PASS: local-only
    ports:
      - "127.0.0.1:5672:5672"
```

Run `docker compose up -d`, then
`docker compose exec rabbitmq rabbitmq-diagnostics -q ping` until successful.
Set `AMQP_URL=amqp://local:local-only@localhost:5672/`. These credentials and plaintext
transport are local-only. After consumer startup, publish an order and check the
listener's effect. `docker compose down --volumes` removes the server and disposable queues.

### API references

See the [`rabbitmqbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/rabbitmq/rabbitmqbroadcast).

## MQTT

Use MQTT 5 through Eclipse Paho for events on MQTT brokers such as Mosquitto,
EMQX, or HiveMQ. It is experimental in
[Choosing a Driver](../drivers/choosing-a-driver.md).

### Installation

```sh
go get arkoren.dev/driver/mqtt
```

### Connecting

`New` validates one or more broker URLs without network I/O; multiple URLs enable
failover. `Start` creates an owned connection manager and awaits its first
connection:

```go
import "arkoren.dev/driver/mqtt/mqttbroadcast"

driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"})
```

Register `Stop` for cleanup before `Start` for startup so acquired resources are
released even after startup failure; use `Ping` for readiness.

Use `mqtts://` for TLS outside local development. Supported schemes are `mqtt`,
`tcp`, `mqtts`, `ssl`, `tls`, `mqtt+ssl`, `tcps`, `ws`, and `wss`. A host is
required, and explicit ports must be between 1 and 65535. TCP/TLS URLs require a
port and reject paths and queries; WebSocket URLs preserve paths and queries
and may omit the port. Unsupported schemes, malformed URLs, fragments, and URL
userinfo such as `user:password@host` are rejected. Set authentication with the
builder instead of embedding credentials in URLs; constructor errors do not
echo those URL credentials.

### Configuration

`Builder.QoS` selects the publish and subscription quality of service: 0 for at
most once, 1 for at least once, or 2 for exactly once at the MQTT protocol level.
The default is 1; `Builder.QoS(0)` selects 0. `New` rejects other levels after
construction callbacks finish. `NewFrom` has no error result, so its invalid QoS
is reported by `Publish` or `Subscribe` before contacting Paho.

`Builder.KeepAlive` takes a `time.Duration`. Zero restores the 30-second default;
a negative value requests disabled keepalive. Positive values must be whole
seconds from 1 through 65535 seconds, inclusive. `New` rejects other positive
durations instead of truncating or overflowing the MQTT field. The broker can
override the requested interval in its CONNACK response. To request
45 seconds:

```go
import "time"

driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, func(broker *mqttbroadcast.Builder) {
	broker.KeepAlive(45 * time.Second)
})
```

Replace old numeric-second calls such as `KeepAlive(30)` with
`KeepAlive(30 * time.Second)`. `Config.KeepAlive` and `DefaultMQTTKeepAlive` are
durations too; the native Paho configuration still uses its own seconds field.

`Builder.Username` and `Builder.Password` default to empty. Password is applied
only with a nonempty username; load credentials from configuration or a secret
manager and use TLS. Nil `Builder.Logger` selects `slog.Default()` for recovered
handler panics and failed reconnect subscriptions, not Paho's separate native
loggers. Config is diagnostic data, not constructor input; formatting redacts
Password but retains Username. Callbacks run once in order on a fresh draft;
retaining the draft cannot change the driver. For native connection settings,
use the caller-owned Paho setup below.

### Publishing device notifications

Use the [typed publisher](overview.md#publishing) and
[listener](overview.md#listening) with this driver. A name such as
`sensors.device1.temperature` uses MQTT topic `sensors/device1/temperature`.
Native producers must send a `broadcast.Message` JSON envelope, not bare sensor
JSON, for `Consume` to route it. Payload bytes use the envelope's base64 JSON
representation. Metadata travels in that JSON body, not MQTT user properties;
see [Broadcast Metadata and Telemetry](metadata-and-telemetry.md#message-metadata).

At QoS 1 and 2, publication waits for the MQTT protocol result. Broker rejection
is an error, including failed QoS 2 PUBREC or PUBCOMP response codes even when
Paho returns a nil error. Success does not confirm listener processing, and a
timeout does not prove that the message was not delivered.

### Behavior and limits

`New`'s clean, nonpersistent sessions lose offline messages at every QoS. QoS 2
does not guarantee exactly-once application effects. Names replace `.` with `/`,
`*` with `+`, and retain final `#`.

`Consume` drops malformed envelopes and topic/name mismatches before listeners
or telemetry run, through the normal MQTT acknowledgement path. Raw `Subscribe`
does not validate envelopes. A returned listener error does not prevent MQTT
acknowledgement or cause application-level retries; use the consumer's
[failure events](overview.md#delivery) to observe those errors.

With Paho's default automatic acknowledgements, QoS 1 acknowledgement follows
all matching callbacks. A caller-owned manager with manual acknowledgements
retains that policy. Up to 1024 driver delivery callbacks run concurrently;
saturation blocks dispatch and backpressures Paho's receive loop. Raw callback
bytes are shared read-only: copy them before retaining or mutating them.

Already-canceled `Publish`, `Consume`, and `Subscribe` contexts perform no
operation. Later cancellation of a raw subscription's context does not remove
its callback; call the returned idempotent unsubscribe function. `Consume`
releases its subscription when its context ends. Subscription changes and
reconnect restoration serialize so an old unsubscribe cannot remove a replacement.

`Stop` is terminal: it closes admission and waits for admitted deliveries and
reconnect work until its context ends, then clears local registrations and
attempts subscription cleanup even after a drain timeout. It cancels and
disconnects `New`'s owned manager. For `NewFrom`, it attempts to unsubscribe this
driver's filters but leaves the manager connected. Cancel `Consume` contexts
separately; `Stop` does not end their context-waiting loops.

Raw unsubscribe and borrowed-manager cleanup use a separate ten-second
acknowledgement timeout even when the original context is expired. Cleanup
serializes with subscription RPCs; neither cancellation nor `Stop` can forcibly
stop arbitrary callbacks or native socket writes. Drain and cleanup errors are
joined. If broker unsubscription fails, the native owner may need to disconnect
its session to release the remaining filters.

Enforce broker topic ACLs or separate namespaces between untrusted publishers;
routing checks do not authorize publishers or authenticate payloads.

### Using a caller-owned Paho client

For custom TLS, authentication, session, or native logging settings, configure
Paho yourself. MQTT topic filters belong to the whole native client: do not
share this driver's filters with unrelated native subscriptions, because its
cleanup unsubscribes those filters for the client.

With your existing `autopaho.ClientConfig` named `config` containing broker URLs
and native policy, install both driver hooks before creating the manager.
This fragment assumes an operation context `ctx` and a named `(err error)`
function result so deferred cleanup can join its errors with the operation's
result. End and join any consumers before returning from that function:

```go
import (
	"context"
	"errors"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

driver := mqttbroadcast.NewFrom(nil)
config.ClientConfig.OnPublishReceived = append(config.ClientConfig.OnPublishReceived, driver.HandlePublish)
onConnectionUp := config.OnConnectionUp
config.OnConnectionUp = func(manager *autopaho.ConnectionManager, acknowledgement *paho.Connack) {
	if onConnectionUp != nil {
		onConnectionUp(manager, acknowledgement)
	}
	driver.HandleConnectionUp(manager, acknowledgement)
}
connection, err := autopaho.NewConnection(context.WithoutCancel(ctx), config)
if err != nil {
	return err
}
defer func() {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = errors.Join(err, driver.Stop(cleanup), connection.Disconnect(cleanup))
}()
driver.SetClient(connection)
if err := connection.AwaitConnection(ctx); err != nil {
	return err
}
```

The manager outlives the operation context, so its owner must always disconnect
it. The deferred cleanup stops the driver before disconnecting the native
manager, including after a failed connection wait. Its five-second context
bounds draining and the native disconnect wait, not arbitrary handler or socket
execution or the driver's separate unsubscription wait.

Only QoS and Logger apply to `NewFrom`; its `Start` is a no-op. `SetClient`
attaches the manager once before publishing or consuming, without transferring
ownership. `Client()` returns that shared pointer; native calls bypass envelope
checks and facade telemetry, and native configuration/errors are not recursively
sanitized.

`HandleConnectionUp` schedules active-filter restoration without waiting for
acknowledgements in Paho's connection callback. Existing native connection
callbacks must also return promptly. Failed restoration is logged and is not
retried on the same connection. Borrowed managers otherwise retain their native
session and reconnect settings.

### Running locally

With Docker installed, save this as `compose.yaml` in your application:

```yaml
services:
  mqtt:
    image: eclipse-mosquitto:2.0.22
    command: ["mosquitto", "-c", "/mosquitto-no-auth.conf"]
    ports:
      - "127.0.0.1:1883:1883"
```

Run `docker compose up -d` and connect with
`mqttbroadcast.New([]string{"mqtt://localhost:1883"})`. Successful `driver.Start(ctx)`
confirms connection. Keep this anonymous plaintext server bound to loopback.
After consumer startup, publish and check the listener's effect.
`docker compose down --volumes` removes the server.

### API references

See the [`mqttbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/mqtt/mqttbroadcast).

## SNS

`awsbroadcast` publishes to SNS and consumes through SQS. Subscribe one queue per
service for durable fan-out; offline delivery follows SQS retention and redrive policies.

### Installation

```sh
go get arkoren.dev/driver/aws
```

### Connecting

Provision SNS topics, one SQS queue per consuming service, and subscriptions first;
the drivers create none of them.

`SNSDriver` publishes to topics; `SQSDriver` consumes/publishes queues. Neither
has lifecycle hooks: register only the consumer runner. `NewSNS` maps names to
topic ARNs; `NewSQS` takes the consumer queue. Both use the AWS default region/credential chain:

```go
import "arkoren.dev/driver/aws/awsbroadcast"

outbox, err := awsbroadcast.NewSNS(ctx, map[string]string{
	"order.created": "arn:aws:sns:eu-west-1:123456789012:orders-created",
})
publisher := broadcast.NewPublisher(outbox)
```

For a consuming service, configure its subscribed queue and consumer separately:

```go
inbox, err := awsbroadcast.NewSQS(ctx, func(queue *awsbroadcast.SQSBuilder) {
	queue.QueueURL("https://sqs.eu-west-1.amazonaws.com/123456789012/billing-events")
})
consumer := broadcast.NewConsumer(inbox)
```

Register `consumer.Run` as an application runner; there are no driver hooks.

Publish-only services need just SNS; listen-only services need just SQS.
Construction and native-client access do not verify that topics, queues,
subscriptions, or permissions are ready. Verify those resources separately and
start the consumer before using its listener effects as a delivery check; an SDK
client or successful SNS publish is not proof of queue delivery.

### Configuration

SNS has only its required topic map. `NewSNS` and `NewSNSFrom` reject an empty
map, invalid broadcast names, or blank topic ARNs with an error wrapping
`awsbroadcast.ErrInvalidConfig`. They copy the map; later caller mutations do
not change routing. AWS validates nonempty ARN syntax and permissions when you
use the client, not during this local mapping validation.

SQS requires `QueueURL` for consuming or `Queues` for direct publishing, or both.
Publisher mappings require valid broadcast names and nonblank queue URLs and
are copied at capture and publication. `WaitTimeSeconds` defaults to 20; nonzero
values must be 1–20 seconds. `MaxMessages` defaults to one; nonzero values must
be 1–10. Invalid settings wrap `awsbroadcast.ErrInvalidConfig`. Nil `Logger`
selects `slog.Default()`. Configure region, endpoint, and credentials on native
clients passed to `NewSNSFrom` and `NewSQSFrom`; nil clients are rejected.

### Publishing durable notifications

Use the [order publisher](overview.md#publishing)
with `outbox` and the [order listener](overview.md#listening) with `inbox`.
SQS retains notifications delivered by SNS under its retention and redrive
policies. SNS acceptance does not confirm delivery to every queue. Grant the
publisher `sns:Publish` on its topic and the consumer `sqs:ReceiveMessage` and
`sqs:DeleteMessage` on its queue.
The queue policy must allow that SNS topic to send messages.

To select broadcast names per queue, set its SNS subscription's message-attribute
filter policy, for example `{"name":["order.created"]}`.
The SNS driver supplies the string attribute `name`; this filter is broker
configuration, separate from the consumer's local listener patterns.

For an SNS FIFO topic whose ARN ends in `.fifo`, the driver groups publications
by broadcast name. It uses `Message.ID` for deduplication when supplied directly
to the driver, otherwise a fresh random ID. Facade publications supply no ID, so
repeating `publisher.Publish` does not deduplicate an application event. Keep
listener effects idempotent even with FIFO topics.

### Behavior and limits

Publish success means SNS/SQS acceptance, not processing. Both publishers send
the complete JSON `broadcast.Message` envelope, including metadata; the SQS
consumer accepts it directly or inside an SNS notification wrapper. Payload
bytes use the envelope's base64 JSON representation. SNS sends no subject, so
subject rules do not limit names. Unmapped valid names return
`awsbroadcast.ErrUnknownName`.

The consumer deletes successful deliveries, malformed envelopes, and deliveries
whose listener failures are all permanent. Other listener failures leave the
message for redelivery to every matching listener, so listeners must be
idempotent. Unlike the SQS queue driver, this broadcast driver does not extend
visibility while listeners run. Configure the queue's visibility timeout to cover
the entire received batch, or another consumer can receive a message still being
handled. Publishing straight to an SQS queue is point-to-point: one queue, competing
consumers. Use an SNS topic for fan-out.

### Publishing directly to SQS

For one consuming service, bypass SNS with `SQSBuilder.Queues`:

```go
outbox, err := awsbroadcast.NewSQS(ctx, func(broker *awsbroadcast.SQSBuilder) {
	broker.Queues(map[string]string{"order.created": queueURL})
})
```

`queueURL` must identify a provisioned queue. For a URL ending in `.fifo`, the
driver supplies SQS's required `MessageGroupId` from the broadcast name and
`MessageDeduplicationId` from `Message.ID` when supplied directly to the driver,
otherwise a fresh random ID. These native fields do not change the envelope.
As with SNS FIFO, facade publications supply no ID, so repeating a facade
publish does not deduplicate an application event. Wrap `outbox` with
`broadcast.NewPublisher`; replicas compete for messages.

`NewSNSFrom` and `NewSQSFrom` borrow SDK clients, exposed by `Client()` without
ownership transfer. Configure native policy before sharing the clients; native
configuration and transport cleanup remain caller-owned. The drivers have no
client-close lifecycle hooks. Native calls bypass facade encoding and telemetry.

### Running locally

Use the [local SQS emulator setup](../queues/drivers.md#running-locally-2)
(Compose, credentials, SDK imports, readiness check). With its environment set, follow
[local SNS provisioning](#local-sns-provisioning)
to create the topic, queue policy, and subscription with the AWS CLI and Python 3.

Use the returned topic ARN in `NewSNSFrom` and the queue URL in `NewSQSFrom`.
Build both native clients with the emulator configuration:

```go
import (
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

outbox, err := awsbroadcast.NewSNSFrom(sns.NewFromConfig(cfg), map[string]string{
	"order.created": topicARN,
})
inbox, err := awsbroadcast.NewSQSFrom(sqs.NewFromConfig(cfg), func(broker *awsbroadcast.SQSBuilder) {
	broker.QueueURL(queueURL)
})
```

Supply CLI results as `topicARN` and `queueURL`. Publish an order and check the
listener's effect. `docker compose down --volumes` removes the disposable emulator,
topics, subscriptions, and queues.

### Local SNS provisioning

After the linked SQS setup creates `queueURL`, use the AWS CLI and Python 3 to
create a topic and subscribe that queue. These development commands replace the
queue's access policy; do not run them against a production queue:

```sh
topicARN=$(aws --endpoint-url http://localhost:14568 sns create-topic --name orders-created --query TopicArn --output text)
queueARN=$(aws --endpoint-url http://localhost:14568 sqs get-queue-attributes --queue-url "$queueURL" --attribute-names QueueArn --query Attributes.QueueArn --output text)
policy=$(python3 -c 'import json,sys; print(json.dumps({"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":sys.argv[1],"Condition":{"ArnEquals":{"aws:SourceArn":sys.argv[2]}}}]}))' "$queueARN" "$topicARN")
attributes=$(python3 -c 'import json,sys; print(json.dumps({"Policy":sys.argv[1]}))' "$policy")
aws --endpoint-url http://localhost:14568 sqs set-queue-attributes --queue-url "$queueURL" --attributes "$attributes"
aws --endpoint-url http://localhost:14568 sns subscribe --topic-arn "$topicARN" --protocol sqs --notification-endpoint "$queueARN"
```

Use `topicARN` and `queueURL` in the local clients above. Publish an order and
check the listener's effect before cleaning up the emulator.

### API references

See the [`awsbroadcast` API](https://pkg.go.dev/arkoren.dev/driver/aws/awsbroadcast).
