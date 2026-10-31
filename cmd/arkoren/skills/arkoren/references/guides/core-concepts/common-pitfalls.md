---
title: Common Pitfalls
description: Mistakes that recur when wiring Arkoren services, why each one happens, and the habit that avoids it.
order: 50
---

# Common Pitfalls

## Introduction

Arkoren keeps its behavior explicit, which means most mistakes are visible in
the code once you know what to look for. This page collects the ones that come
up most often, grouped by where they happen. Each entry explains what goes
wrong, why, and the habit that prevents it. Many of them fail loudly at
startup; a few fail quietly, and those are marked.

Wiring snippets assume an existing application named `app`, backend
driver `db`, and HTTP server `srv`. Follow the
[Quickstart](../first-steps/quickstart.md) if you need that initial setup.

## Wiring the application

### Registering cleanup after another fallible operation

Composition can fail before any startup hook runs. Register cleanup as soon as
you own a resource, before constructing its consumers. Cleanup becomes active
immediately and runs even if that resource never starts, so its stop method
must tolerate that state. Inside a composition function, register an existing
database and server like this:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(db.Stop)
	composer.Startup(db.Start)
	composer.Cleanup(srv.Stop)
	composer.Startup(srv.Start)
}
```

[Lifecycle and ownership](../application/lifecycle-and-ownership.md)
explains reverse-order cleanup and resources acquired inside startup callbacks.

### Expecting the constructor to fail on an unreachable server

For drivers with a `Start` method, such as the SQL drivers, construction
validates configuration but does not verify connectivity. If you omit the
start hook, a bad address fails the first query instead of startup. Register
each lifecycle method and readiness check the driver provides. Cloud drivers
that initialize an SDK client at construction can have different requirements;
see [Lifecycle and ownership](../application/lifecycle-and-ownership.md).

### Passing the application to your services

A service that receives the whole `*application.Application` can change the
log level or read runtime state unrelated to its responsibility. Passing the
composer would also permit hidden lifecycle registration. Keep both in bootstrap
code and pass what the service needs instead: the logger from
`app.Logger()` and the event bus from `app.Events()`.

### Registering the same driver lifecycle twice

When a dependency injection container also manages lifecycles, registering a
driver's `Start` and `Stop` with both the container and the application
connects twice and closes twice. Pick one lifecycle per service, as described
in [Application Composition](../application/composition.md).

## Configuration

### Applying a shared policy after its local override

Construction policies run in argument order on one draft. Put shared defaults
first and the local override last. With an existing `cache.Driver` named
`driver` and a shared `cache.BuilderFunc` named `defaults`, import
`arkoren.dev/core/service/cache`:

```go
store := cache.New(driver, defaults, func(store *cache.Builder) {
	store.Name("accounts")
})
```

Reversing them lets `defaults` replace the local name. There is no cache
`Config` input. Configurable vendor drivers also use ordered callbacks rather
than whole-record configuration replacement.

### Forgetting events or telemetry (fails quietly)

Packages publish events only when you give them a bus: `ConsumerBuilder.Events`
for queue/broadcast consumers, `Builder.Events` for HTTP and service facades,
and `ClientBuilder.Events` for OAuth. A
listener registered on the application's bus for a database or queue topic
never fires if the facade was built without a bus. Likewise, a facade
built without explicit telemetry uses the process-wide default telemetry, which
records nothing until you set one. If an event or a metric is missing, check
the constructor first. Add the bus or telemetry service when constructing the
component; changing a retained builder later does not reconfigure it.
[Configuration](../first-steps/configuration.md#the-shape) explains publication
and callback validation. For application diagnostic tools, also check their
[capture and failure behavior](../observability/exposing-diagnostics.md): a
failed declaration does not necessarily undo capture attachment.

## HTTP

### Calling `Use` after registering routes

Router middleware wraps the routes registered after it. A middleware added
after a route would silently skip that route, which is dangerous for
authentication, so `Use` panics when routes were already registered through
the router. Add middleware first, then routes. Import
`arkoren.dev/core/http/router` and `arkoren.dev/core/http/auth`; `authenticator`,
`User`, and `me` are your existing authenticator, user type, and handler:

```go
r := router.New()
r.Use(router.Recover())
r.Use(auth.Middleware(authenticator), auth.Require[User]())
r.Get("/me", me)
```

To apply middleware to only some routes, use a group or `With`, described in
[Routing](../http-fundamentals/routing.md).

### Assuming panics are recovered

`router.New` does not install panic recovery. A panic in a handler without
`router.Recover()` in the middleware chain is caught by Go's HTTP server,
which logs it and closes the connection without a response. Add
`router.Recover()` around the handlers and middleware it must protect so the
client receives a `500` Problem Details response instead. Keep request logging
outside recovery so it sees the converted error; see
[middleware ordering](../http-fundamentals/middleware.md#ordering).

### Writing error responses by hand

Writing an error body with `response.JSON` and returning `nil` works, but the
response then bypasses the router's central error rendering: it is not a
Problem Details document, middleware that inspects the returned error never sees it, and it does not carry
the request's instance path. Return the error instead, and let the router
render it. [Error handling](../http-fundamentals/error-handling.md) shows how to return
errors with a specific status and extra fields.

### Treating zero values as missing in validation (fails quietly)

The validation package considers `0`, `""`, and `false` absent, and rules other
than `Required` skip absent values. An integer schema with only `Min(1)` therefore accepts
`0`. Add `Required()`, or use a pointer field with `validate.Ptr` when zero is a
real value that must be range-checked. See [Validation](../validation/overview.md).

## Data

### Building SQL for the wrong database (fails quietly)

`query.Table(...).Build()` emits ANSI SQL with `?` placeholders. PostgreSQL
expects `$1`, and MySQL quotes identifiers differently. Pass published query values to the
database facade, which compiles them for the configured driver's dialect,
instead of building SQL strings yourself. If you do need the SQL text, use
`BuildFor` with the dialect. See [Query builder](../database/query-builder.md).

### Paginating on a column that is not unique

Cursor pagination needs a total order. If the last `OrderBy` column is not
unique, rows with the same value can be skipped or repeated between pages.
Always end the order with a unique column, typically the primary key. Import
`arkoren.dev/foundation/query` for this query definition:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.OrderByDesc("created_at")
	statement.OrderByDesc("id")
})
```

See [Pagination](../database/pagination.md).

## Production

### Running memory drivers in production

Memory drivers are complete implementations, but their data lives in one
process. With two replicas, each has its own cache and queue, and everything
is lost on restart. They suit development, tests, and intentionally ephemeral
single-process state. Before you deploy, replace drivers whose state must be
durable or shared; see
[Choosing a driver](../drivers/choosing-a-driver.md).

### Exposing operator endpoints publicly

`InfoHandler` and the verbose readiness report reveal versions, build
revisions, and check details. They have no protection of their own. Serve them
on a second server bound to an internal address, as shown in
[Deployment](../first-steps/deploying.md#operator-endpoints).

### Shutting down slower than the platform allows

If the application's shutdown timeout is longer than the platform's grace
period, the platform kills the process before stop hooks finish, and database
connections and in-flight jobs are cut off. Keep the server's drain timeout
inside the application's shutdown timeout, and allow extra platform time for
telemetry cleanup. See the [shutdown budget](../first-steps/deploying.md#graceful-shutdown)
before setting the platform's grace period.

### Long-lived responses that ignore shutdown

Shutdown does not cancel the contexts of requests already in progress. A
server-sent event stream or long poll that only watches the request context
keeps running until the drain timeout cuts it off. Watch
`server.ShuttingDown(ctx)` in such handlers and return when it fires.
