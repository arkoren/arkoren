---
title: Running the Server
nav: Server
description: Serve a router with safe timeouts, bind the port at startup, drain requests on shutdown, run several servers, and end streams cleanly.
order: 10
---

# Running the Server

## Introduction

Your HTTP listener should fail startup if its port is unavailable and give
in-flight requests time to finish during shutdown. The `server` package serves
any `http.Handler`, usually an Arkoren router, with separate steps for binding,
serving, and cleanup that fit the
[application lifecycle](../application/lifecycle-and-ownership.md).

It wraps Go's `http.Server` and supplies default timeouts to limit how long
slow or malicious clients can hold connections open.

## Serving a router

Import `arkoren.dev/core/http/server`. With your existing router `r` from
[routing](../http-fundamentals/routing.md), create a server with the default
address `:8080`:

```go
srv := server.New(r)
```

The default listens on all interfaces. If you need local-only access or another
port, pass an address callback instead:

```go
srv := server.New(r, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:9000")
})
```

Without TLS configuration the server speaks plain HTTP.
For HTTPS, configure [native TLS below](#serving-https) or place the HTTP
listener behind an appropriately configured TLS-terminating proxy.

With an existing `*application.Application` named `app` from the
[application lifecycle guide](../application/lifecycle-and-ownership.md), register
the server's three lifecycle methods in a composition callback:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(srv.Stop)
	composer.Startup(srv.Start)
	composer.Run(srv.Serve)
}
```

Pass `compose` to `app.Start` or `app.Run` as described in that guide.

`Start` binds the listening socket, so a port already in use fails startup
before serving begins. `Serve` accepts connections; as a runner, it starts only
after every start hook succeeds. Register database connections and other
required setup as start hooks too, so they are ready before the first request
is handled. `Stop` releases the server at the end.

Without an application, use your caller-owned `context.Context` named `ctx` and
call the steps yourself:

```go
err := srv.Start(ctx)
if err != nil {
	return err
}

err = srv.Serve(ctx)
```

Handle a startup error before calling `Serve`. When `ctx` ends, `Serve` drains
requests as described below. Without the application, you also own cleanup:
call `Stop` with a live cleanup context if setup or serving fails.

## Serving HTTPS

If this listener terminates TLS itself, provision its identity before startup.
The [TLS Certificates guide](certificates.md#loading-a-server-identity) shows
loading files and building a native `*tls.Config` named `settings`. With that
configuration and your existing handler `r`, select HTTPS during construction:

```go
srv := server.New(r, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:8443")
	listener.TLS(settings)
})
```

Register the same `Start`, `Serve`, and `Stop` lifecycle hooks shown above.
`Start` checks static TLS credentials before binding or publishing its listening
event. A non-nil configuration requires at least one static certificate or a
native dynamic certificate callback; otherwise startup fails. `TLS(nil)` selects plain HTTP instead.
`Serve` uses native `ServeTLS` for a non-nil configuration.

For the local identity in the certificate guide, use
`https://localhost:8443` from a client that trusts its CA. A successful bind
does not establish client trust or prove that a future TLS handshake succeeds.
`GetCertificate` and `GetConfigForClient` callbacks are supported but are not
invoked during construction or static startup checks; their failures occur
during handshakes.
HTTPS setup failures never fall back to plain HTTP.

Finish configuring `settings`, its certificates, pools, and callbacks before
sharing it, and do not mutate them after `Start`. Native `tls.Config.Clone` is
not a recursive copy. For private-CA clients and mutual TLS settings such as
`ClientCAs` and `ClientAuth`, follow [TLS Certificates](certificates.md).

## Timeouts and limits

A client that sends a request very slowly can hold a connection open and
consume server resources. The defaults limit those waits and the header size:

- request headers must arrive within 10 seconds;
- the whole request, including the body, within 30 seconds;
- the response must be written within 60 seconds;
- idle keep-alive connections are closed after 120 seconds;
- request headers are limited to 1 MiB, plus Go's parser buffer allowance.

Change any of them with the matching option, such as
`server.Builder.ReadTimeout`. Raise them only for the routes that need it: an
upload endpoint might need a longer read timeout, which you could serve from a
separate server.

## Shutting down

When its runner context ends, `Serve` stops accepting new connections and asks
Go's HTTP server to drain its tracked connections, for up to 15 seconds by
default. If that drain deadline expires, it closes the remaining tracked HTTP
connections and returns an error matching `context.DeadlineExceeded`; normal
draining returns nil. This gives ordinary requests time to finish, but does not
guarantee client delivery or the completion of arbitrary handler goroutines.
Change the drain time with
`server.Builder.ShutdownTimeout`, and keep it shorter than the application's own
shutdown timeout, as explained in
[Deployment](../first-steps/deploying.md#graceful-shutdown).

`Stop` uses the cleanup context supplied by its caller as its drain budget, not
`Builder.ShutdownTimeout`. Even with an already-expired context it releases the
listener and closes remaining tracked HTTP connections. Neither method forcibly
stops arbitrary application work or takes ownership of hijacked connections.

### Long-lived requests

Beginning shutdown does not cancel active request contexts, so ordinary requests
can finish. A server-sent event stream or long poll that remains under HTTP
ownership can hold the drain until it returns or its connection is closed at the
deadline. Closing a connection does not guarantee that its handler stops;
application work must cooperate with cancellation or the shutdown signal before
you release dependencies it still uses.

Hijacked connections, including WebSockets, are no longer tracked by the HTTP
server. `Serve` and `Stop` neither close them nor wait for their owners to finish.
Their owners must arrange closure and wait for associated work themselves.

Long-lived handlers can watch `server.ShuttingDown` to learn that shutdown began.
In the following fragment, `r` is the incoming `*http.Request` in an existing
`router.Handler`, `messages` is an application-owned notification channel, and
`sendMessage` is your function that writes one message and returns an error:

```go
select {
case <-server.ShuttingDown(r.Context()):
	return nil
case <-r.Context().Done():
	return r.Context().Err()
case message, open := <-messages:
	if !open {
		return nil
	}

	return sendMessage(message)
}
```

The [streaming helpers](../http-fundamentals/responses.md#streaming) in the `response` package
already watch this signal. Their producers still need a way to stop: a sequence
blocked outside `yield` must observe cancellation or shutdown, and a channel
producer must stop rather than block on its next send. Beginning shutdown does
not cancel the request context, so a producer waiting during shutdown must watch
`server.ShuttingDown` or use an application-owned cancellation signal.

To observe an expired drain, set `listener.Events(app.Events())` in the server's
construction callback. If tracked HTTP connections have not drained before the
deadline, `Serve` force-closes those connections and publishes
`http.server.shutdown.timed-out` with the address and configured drain budget.
This event does not prove that every handler has stopped, and it does not cover
hijacked connections. A shutdown that drains in time publishes nothing, and
neither does `Stop`, whose deadline is the caller's. The same setting publishes
`http.server.listening` when `Start` binds;
[HTTP Events](../events/http-events.md#topics) describes both.

## Several servers

A service sometimes needs two listeners, such as a public API and an internal
port for operator endpoints. With your existing operator router `routes`, create
a second server and register its steps on the same application:

```go
admin := server.New(routes, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:9090")
})
composeAdmin := func(composer *application.Composer) {
	composer.Cleanup(admin.Stop)
	composer.Startup(admin.Start)
	composer.Run(admin.Serve)
}
```

Both listeners bind before the application launches the serving runners. On
shutdown, each server drains its own tracked HTTP connections as described above.

## Choosing a port at runtime

In tests it is convenient to let the operating system choose a free port. Pass
the address `127.0.0.1:0`, and after `Start` read the port that was chosen with
`Addr`:

```go
srv := server.New(r, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:0")
})
err := srv.Start(ctx)
if err != nil {
	return err
}
url := "http://" + srv.Addr()
```

The resulting URL contains the assigned port. Call `Stop` with a live cleanup
context after the test, even if `Serve` never ran.

For most tests you do not need a server at all; see
[HTTP tests](../testing/http-tests.md).

For every option, see the [`server` API reference](https://pkg.go.dev/arkoren.dev/core/http/server).
