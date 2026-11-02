---
title: HTTP Server
nav: Server
description: Bind HTTP listeners during startup, serve prepared handlers, and drain connections with explicit shutdown ownership.
order: 10
summary:
  - Bind a listener during startup and register serving and cleanup with the application lifecycle.
  - Configure HTTPS and connection timeouts without confusing socket limits with application deadlines.
  - Budget draining and coordinate streams, producers, and hijacked connections before releasing dependencies.
  - Distinguish listener events from readiness and use separate or temporary listeners when needed.
---

# HTTP Server

An HTTP listener needs to acquire its port before the application reports a
successful startup. It also needs a bounded shutdown that lets requests finish
without leaving their dependencies in use. The `server` package separates
binding, serving, and cleanup so you can give each step to the appropriate
application lifecycle phase.

The server accepts any `net/http.Handler`. Build an Arkoren router and
[register typed handlers](../http-fundamentals/routing.md#register-handlers)
before constructing the listener; this page manages the network boundary, not
request decoding or response construction.

## Bind Locally

With an existing handler named `routes`, import
`arkoren.dev/core/http/server` and choose an explicit loopback address:

```go
srv := server.New(routes, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:8080")
})
```

Construction opens no socket. Without the callback, the address is `:8080`,
which listens on all interfaces. Use that broader address only when the
deployment's network policy permits access. Without TLS settings the listener
serves plaintext HTTP; use TLS termination at your ingress or configure
[HTTPS](#serving-https) before exposing sensitive traffic.

Configure native fields through `srv.HTTP()` before `Start` if you need a Go
server setting that the builder does not expose. Server and TLS configuration
must not be mutated after startup.

## Register Lifecycle

Inside an existing application composition callback, give the composer the
server's cleanup, startup, and runner methods. The composer type comes from
`arkoren.dev/core/application`:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(srv.Stop)
	composer.Startup(srv.Start)
	composer.Run(srv.Serve)
}
```

Pass this callback to your application's `Start` or `Run`. `Start` acquires the
TCP listener; an occupied port fails startup rather than becoming a later runner
failure. `Serve` starts accepting requests only after all startup hooks succeed.
Register required dependency initialization as startup work too.

The cleanup registration matters even if serving never begins: another startup
hook can fail after this listener has bound. `Stop` also closes a bound listener
that `Serve` never took over. See
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md) for the
application's startup and cleanup ordering, and
[outcome handling](../application/lifecycle-and-ownership.md#observe-outcomes)
for observing a runner failure.

If you do not use an application, you own all three phases. With a caller-owned
context `ctx`, check binding before entering the serving loop:

```go
if err := srv.Start(ctx); err != nil {
	return err
}
err := srv.Serve(ctx)
```

Arrange `Stop` with a live, bounded cleanup context on every exit after a
successful `Start`, including serving failure. Canceling `ctx` initiates
`Serve`'s drain; merely returning from unrelated application code does not.

## Serving HTTPS

Prepare a `*tls.Config` named `settings` by
[loading a server identity](certificates.md#load-identities). Select TLS when
constructing the listener:

```go
srv := server.New(routes, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:8443")
	listener.TLS(settings)
})
```

Use the same lifecycle registrations. `Start` rejects missing credentials and
obviously invalid static certificates before binding. A nonnil configuration
needs a static certificate, `GetCertificate`, or `GetConfigForClient`.
`TLS(nil)` selects plaintext instead; an HTTPS failure never falls back to HTTP.

The native TLS configuration remains shared. Finish preparing its certificates,
trust pools, and callbacks before startup. Callbacks are not invoked by the
startup check, so their failures occur during handshakes. Binding successfully
does not prove that clients trust the identity or that its names and validity
match their requests. Native `ServeTLS` handles handshakes and HTTP/2 negotiation.

## Set Timeouts

The server supplies connection-level limits so a slow client cannot retain a
connection indefinitely under the default configuration:

| Setting | Default | Scope |
| --- | --- | --- |
| `ReadHeaderTimeout` | 10 seconds | Reading request headers. |
| `ReadTimeout` | 30 seconds | Reading the whole request, including its body. |
| `WriteTimeout` | 60 seconds | Writing a response. |
| `IdleTimeout` | 120 seconds | Waiting between keep-alive requests. |
| `MaxHeaderBytes` | 1 MiB | Request headers, with Go's additional parser buffer allowance. |

For an upload listener whose request bodies legitimately take longer, import
`time` and change its read budget during construction:

```go
uploads := server.New(routes, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:8081")
	listener.ReadTimeout(2 * time.Minute)
})
```

These settings apply to the listener, not an individual route. A separate
listener can keep a long upload budget away from ordinary API requests. Zero
timeout values select Arkoren's defaults rather than disabling them.

Socket timeouts are not application-work deadlines. Pass request contexts to
downstream work and make that work honor cancellation. For long responses,
[bound slow writes](../http-fundamentals/streaming-responses.md#bound-slow-writes)
rather than assuming the default write timeout renews after every message.
A negative `WriteTimeout` disables that connection write deadline; use it only
with an explicit streaming write policy.

## Shutting Down

When the runner context ends, `Serve` stops accepting new connections and asks
Go's HTTP server to drain tracked connections. The default drain budget is
15 seconds. Choose a different budget before startup with `time` imported:

```go
srv := server.New(routes, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:8080")
	listener.ShutdownTimeout(10 * time.Second)
})
```

Keep this budget inside the application's and deployment's
[shutdown budgets](../first-steps/deploying.md#shutdown-budgets). Zero selects
15 seconds; a negative value expires the drain deadline immediately. A clean
drain returns nil. An expired drain closes remaining tracked HTTP connections
and returns an error matching `context.DeadlineExceeded`.

`Stop` instead uses its caller's context as the drain budget; it does not use
`ShutdownTimeout`. Even an already-expired cleanup context releases the listener
and closes remaining tracked connections. Repeated calls are safe.

Neither operation proves that arbitrary handler goroutines have finished or that
the client received a complete response. Hijacked connections, including
WebSockets, are outside HTTP draining: their owners must close them and wait
for associated work before releasing resources that work still uses.

## Stop Streams

Beginning shutdown does not cancel active request contexts. This lets ordinary
requests finish, but a long poll or stream needs a separate signal to stop
waiting. `server.ShuttingDown` returns that signal for requests accepted by this
server; without it, the result is a nil channel and its select case is disabled.

Inside a handler, let a wait end for either request cancellation or server
shutdown. Here `incoming` is its `*http.Request`, `messages` is an
application-owned channel, and `sendMessage` writes one message and returns an
error:

```go
select {
case <-server.ShuttingDown(incoming.Context()):
	return nil
case <-incoming.Context().Done():
	return incoming.Context().Err()
case message, open := <-messages:
	if !open {
		return nil
	}
	return sendMessage(message)
}
```

The response streaming helpers watch shutdown too, but cannot stop a producer
blocked outside their yield or receive operation. Arrange cooperative
[iterator cancellation](../http-fundamentals/streaming-responses.md#stop-iterator-work)
and stop channel producers before they block sending their next message.
Connection closure is not a substitute for joining application-owned work.
If bytes are already sent, follow the rules for
[committed failures](../http-fundamentals/streaming-responses.md#committed-failures)
rather than trying to replace the response.

## Observe Outcomes

Set `listener.Events(app.Events())` in the construction callback to publish
listener events on your application's bus. `server.Listening` reports the bound
address; it does not mean serving has begun or an HTTPS handshake succeeded.
`server.ShutdownTimedOut` reports a `Serve` drain deadline that forced tracked
connections closed. It is not emitted for a clean drain or by `Stop`.

Keep subscriptions alive for the observed component's lifetime and
[own their cleanup](../application/composition.md#own-subscriptions).
These synchronous notifications are not readiness probes or proof that all
handlers stopped. Mount the application's
[health probes](../application/health-and-readiness.md#mount-probes) and
[observe readiness transitions](../application/health-and-readiness.md#observe-transitions)
separately.

## Separate Listeners

If operational routes need a different exposure policy, construct another
server with its own handler and address:

```go
admin := server.New(operatorRoutes, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:9090")
})
```

Register `admin.Stop`, `admin.Start`, and `admin.Serve` on the same composer as
the public listener. Both bind before runners begin, and each drains its own
connections. Decide which deployment endpoints reach these listeners in
[Health Probes](../first-steps/deploying.md#health-probes); a second port alone
does not authenticate an operator.

## Test Listeners

Most handler tests need no socket; use [HTTP Tests](../testing/http-tests.md).
When a test needs a real listener, let the OS assign a port:

```go
srv := server.New(routes, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:0")
})
if err := srv.Start(ctx); err != nil {
	return err
}
endpoint := "http://" + srv.Addr()
```

`Addr` reports the assigned port after binding. It does not start serving;
run `Serve` separately and arrange `Stop` with a live cleanup context even if
the test fails before serving begins.

For native configuration and all options, see the
[`server` API reference](https://pkg.go.dev/arkoren.dev/core/http/server).
