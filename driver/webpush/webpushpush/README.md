# Web Push Driver

`arkoren.dev/driver/webpush/webpushpush` implements `push.Driver` for encrypted,
standards-based browser notifications. It submits one visible notification at a
time without automatic retries. Your application owns subscription storage,
permissions, and service-worker display.

Install the module from your application's module directory:

```sh
go get arkoren.dev/driver/webpush
```

First load a persistent P-256 private key using the
[VAPID key guide](../../../cmd/arkoren/skills/arkoren/references/guides/push/vapid-keys.md).
With that `key.Signing` named `identity`, an operation context `ctx`, and an
authorized browser subscription named `subscription`, construct and reuse the
driver and sender:

```go
import (
	"arkoren.dev/core/service/push"
	"arkoren.dev/driver/webpush/webpushpush"
)

driver, err := webpushpush.New(identity, "mailto:push@example.com")
sender := push.New(driver)
message := push.NewMessage(subscription, "Order shipped")

_, err = sender.Send(ctx, message)
```

A nil error confirms provider acceptance, not device delivery or display.
The returned `Receipt.ID` is empty rather than a sensitive provider URL. Construction does no
network I/O and requires no lifecycle hooks. Do not generate a new key on every
server startup: subscriptions remain bound to the key used when registering.
Keep the constructor's pointer rather than copying its synchronized cache.
Pointer formatting emits a fixed redaction marker without traversing owned keys
or authorizations. This does not sanitize arbitrary caller objects or explicitly
unwrapped native error causes, which can contain confidential transport data.

The driver enforces its approved HTTPS endpoint policy and public-address dialing,
rejects redirects, and bounds requests and response processing. It sends a flat
`{title, body, data}` JSON payload; the browser's service worker must understand
that shape. Native FCM/APNs token delivery is not implemented by this driver.

See the [browser integration guide](../../../cmd/arkoren/skills/arkoren/references/guides/push/browser-integration.md),
[driver setup and limits](../../../cmd/arkoren/skills/arkoren/references/guides/push/drivers.md#web-push),
and [API reference](https://pkg.go.dev/arkoren.dev/driver/webpush/webpushpush).
