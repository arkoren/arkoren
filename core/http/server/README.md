# HTTP Server

`arkoren.dev/core/http/server` binds a listener at startup and serves any
`http.Handler`. Construction opens no listener. With an existing handler named
`handler` and application composer named `compose`, configure and register the
three lifecycle methods:

```go
srv := server.New(handler, func(listener *server.Builder) {
	listener.Addr("127.0.0.1:8080")
})
compose.Cleanup(srv.Stop)
compose.Startup(srv.Start)
compose.Run(srv.Serve)
```

Plain HTTP is the default. `Builder.TLS` selects HTTPS with a shared native
configuration; finish configuring it before `Start`. Startup validates static
credentials without invoking application TLS callbacks.

When its context ends, `Serve` signals cooperative handlers through
`server.ShuttingDown` and drains tracked HTTP connections for 15 seconds by
default. An expired drain closes those connections and returns a deadline error;
it cannot forcibly stop arbitrary application goroutines. `Stop` instead uses
its caller's context as its drain budget, including an already-expired context.

Hijacked connections such as WebSockets are neither closed nor awaited. Their
owners must close them and join associated work before releasing dependencies.
With `Builder.Events`, `Listening` reports binding and `ShutdownTimedOut` reports
an expired drain in `Serve`; neither event proves all handlers have finished.

See the [Server guide](../../../cmd/arkoren/skills/arkoren/references/guides/http-networking/server.md)
for application wiring, TLS, and long-lived handlers.
