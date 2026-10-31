# MQTT Broadcast Driver

`arkoren.dev/driver/mqtt/mqttbroadcast` maps portable broadcast names to MQTT 5
topics through Eclipse Paho. For example, `sensors.device1.temperature` uses
`sensors/device1/temperature`.

Install with `go get arkoren.dev/driver/mqtt` and import
`"arkoren.dev/driver/mqtt/mqttbroadcast"`. Register the driver's `Start` and
`Stop` hooks before running its broadcast consumer. Register cleanup immediately
at the acquiring owner, before startup. The
[MQTT broadcast guide](../../../cmd/arkoren/skills/arkoren/references/guides/broadcasting/drivers.md#mqtt)
includes facade wiring, a local Mosquitto server, and caller-owned Paho setup.

## Envelope routing and delivery

`Consume` drops malformed envelopes, invalid concrete names (including wildcards),
and envelopes whose name with dots replaced by slashes does not exactly equal the
actual MQTT topic, before core listeners or telemetry run. Raw `Subscribe`
callbacks receive bytes without envelope validation. The payload is shared
read-only across matching callbacks; copy it before retaining or mutating it.

Routing is not authorization or payload authentication. Configure broker topic
ACLs or separate broker namespaces for mutually untrusted publishers; the name
check preserves topic restrictions only when the broker enforces them.

Up to 1024 delivery callbacks run concurrently. Saturation blocks dispatch and
backpressures Paho's receive loop; this is not a drop policy or an asynchronous
buffer. The publish callback waits for matching handlers. With Paho's default
automatic acknowledgements, QoS 1 acknowledgement follows handler completion,
including errors and recovered panics. Handler errors do not trigger retries;
QoS 2 does not guarantee exactly-once application effects. Publication success
does not confirm listener completion, and timeouts do not prove nondelivery.

Already canceled Publish, Consume, and Subscribe contexts perform no operation.
Later cancellation does not unregister a raw Subscribe callback; call its
idempotent unsubscribe function. Consume releases its subscription when its
context ends. Drivers emit no events; facade telemetry and notifications are
separate core behavior.

## Configuration

Valid QoS values are 0 (at most once), 1 (at least once), and 2 (exactly
once at the protocol level). `New` defaults to QoS 1; `Builder.QoS(0)` selects
at-most-once delivery. Invalid levels fail New; NewFrom reports them when Publish
or Subscribe is called because its constructor has no error result.

KeepAlive uses `time.Duration`, not numeric seconds. Zero selects 30 seconds,
negative disables MQTT keepalive, and positive values must be whole seconds from
1 through 65535, inclusive. The broker can override the CONNECT interval in its
CONNACK response. Existing `KeepAlive(30)` calls must become
`KeepAlive(30 * time.Second)`; `Config.KeepAlive` and `DefaultMQTTKeepAlive` are
durations too. For example:

```go
import (
	"time"

	"arkoren.dev/driver/mqtt/mqttbroadcast"
)

driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, func(broker *mqttbroadcast.Builder) {
	broker.QoS(0)
	broker.KeepAlive(45 * time.Second)
})
```

New validates URLs without network I/O. Supported schemes are `mqtt`, `tcp`,
`mqtts`, `ssl`, `tls`, `mqtt+ssl`, `tcps`, `ws`, and `wss`. TCP/TLS addresses
require a host and explicit port and reject paths and queries. WebSocket URLs
retain paths and queries and may use default ports. Fragments, URL userinfo, and
ports outside 1–65535 are rejected. Use `Builder.Username` and `Builder.Password`
instead of embedded credentials, and TLS outside local development. Password is
applied only with a nonempty username; both default to empty. Nil Logger selects
`slog.Default()` for handler panics and failed reconnect subscriptions, not
Paho's separate native loggers.

Callbacks run once, in order on one draft. Nil entries panic; expanded nil
slices make no changes. Retained drafts cannot mutate a constructed driver.
The redacted Config record is diagnostic data, not constructor input. Formatting
redacts Password but retains Username; direct credential fields remain plaintext.

## Native ownership and shutdown

New creates a clean, nonpersistent session in Start. Stop disconnects that owned
manager; offline messages are lost at every QoS. NewFrom borrows its manager and
applies only QoS and Logger, preserving all native connection/session settings.
Configure both `driver.HandlePublish` and `driver.HandleConnectionUp` before
creating the manager, then attach it with `driver.SetClient(manager)`. The public
connection hook restores active filters on reconnect without blocking autopaho's
connection callback. Preserve other native hooks when composing these callbacks.
See the [NewFrom example](https://pkg.go.dev/arkoren.dev/driver/mqtt/mqttbroadcast#NewFrom).

Stop is terminal. It closes admission, waits for admitted deliveries and reconnect
work until its context ends, clears local registrations, and attempts to release
its filters even after a drain timeout. It leaves a borrowed manager connected;
the native owner disconnects it after the consumer and driver stop. MQTT filters
are client-wide: do not share these filters with unrelated native subscriptions.
Cancel Consume contexts separately; Stop does not end their waiting loops.

Borrowed-client unsubscription and raw unsubscribe use a separate ten-second
acknowledgement timeout even when the original context is expired. Cleanup
serializes with subscription RPCs. Neither context cancellation nor Stop can
forcibly stop arbitrary callbacks or native socket writes. Cleanup errors are
returned, joined with drain failures; the native owner may need to close its
session if broker unsubscription fails. Client returns the shared native pointer
without transferring ownership; its operations bypass envelope checks and facade
telemetry, and its own configuration/errors are not recursively sanitized.
