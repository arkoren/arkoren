---
title: Quickstart
description: Bootstrap an HTTP service with arkoren new, then add routes, a cache counter, and error responses.
order: 20
---

# Quickstart

## Introduction

This guide creates a small HTTP service with `arkoren new`, then adds a route,
an in-memory cache counter, and error responses. The generated files include
configuration, health probes, lifecycle wiring, and debug diagnostics; you edit ordinary Go
code as the service grows.

You need Go 1.27 or later. Install the Arkoren CLI below before creating a project.
No database or broker is needed, because the service uses an in-memory cache.
The shell commands use POSIX environment-assignment syntax; on PowerShell, set
`$env:HTTP_ADDR = "127.0.0.1:8080"` before `go run ./cmd/server` instead.

Focused snippets throughout the documentation omit routine error checks to keep
the feature visible. Handle those errors in application code; complete programs
and examples about failure handling include the relevant checks. Lifecycle
fragments show composer callbacks; pass them to `app.Run(ctx, compose)` and
keep their downstream service and handler wiring inside that callback. Import
`arkoren.dev/core/application` for `application.Composer`. They are focused
excerpts, not complete standalone programs.

## Installing the CLI

Install the `arkoren` executable with Go:

```sh
go install arkoren.dev/cmd/arkoren@latest
```

Go writes the executable to `GOBIN` if set, otherwise to the `bin` directory
under the first entry in `GOPATH` (usually `$HOME/go/bin`). Add that directory
to your `PATH` so your shell can find `arkoren`, then check the installation:

```sh
arkoren new --help
```

You should see `new DIR [flags]` and the project-creation options. This command
does not need a running service or endpoint configuration. If your shell reports
that `arkoren` cannot be found, check that Go's binary directory is on `PATH`.
The [CLI installation guide](../cli/overview.md#installing-the-cli) also covers
installing it as a module tool instead of a global executable.

## Creating the project

From the directory that will contain your project, generate the starter:

```sh
arkoren new hello --module example.com/hello
cd hello
```

The command creates `hello` and writes a Go module, `cmd/server/main.go`,
`internal/bootstrap`, `.env`, `.env.example`, and a README,
but no tests. It does not
download dependencies or start the service, and it never overwrites existing
generated files. To preview the files without writing them, add `--dry-run`.
[Creating a Project](creating-a-project.md) covers directory safety and
direct composition.

From the `hello` folder, resolve dependencies and check the code:

```sh
go get arkoren.dev/core arkoren.dev/driver/mcp
go mod tidy
go vet ./...
```

## Your first application

Start the generated service from the project root, binding to loopback for local
development:

```sh
HTTP_ADDR=127.0.0.1:8080 go run ./cmd/server
```

Open `http://127.0.0.1:8080/` in your browser. You should see Arkoren's HTML
welcome page, served by `app.WelcomeHandler()`. Replace this route with your
application's home page when you need one; it is not an operator-info endpoint.

The **router** in `internal/bootstrap/router.go` maps URLs to handlers. An
Arkoren handler looks like a `net/http` handler, except that it returns an
`error`. The **server** in `server.go` serves that router, using the address
policy defined alongside its lifecycle wiring. `app.go` supplies application
settings and `logging.go` supplies the private log handler factory. The executable loads `.env` without overriding
existing environment values. The generated file selects loopback and enables
unauthenticated MCP route diagnostics at `/mcp` on the same listener.
Loading uses `godotenv/autoload`, which ignores loading errors.
Keep `.env` out of commits, bind development listeners to loopback, and disable
debug in production; see [debug defaults and secrets](creating-a-project.md#local-telemetry-diagnostics-and-secrets).
An empty `HTTP_ADDR` uses the server default `:8080` on all interfaces.

`cmd/server/main.go` constructs the **application** and calls
`app.Run(ctx, bootstrap.Compose)`. The composition function in
`internal/bootstrap/bootstrap.go` obtains one shared router with
`routes := compose.Use(Router())`. `Router()`, `MCP(routes)`, and `Server(routes)`
return typed factories; their callbacks construct and return the router,
diagnostic endpoint, and listener respectively. The root selects diagnostics
with `compose.UseDebug` and applies listener setup with `compose.Use`.
Returned components are not yet started. Every child shares the root's
lifecycle and cleanup ownership.
Ctrl+C or `SIGTERM` cancels the supplied context
for a clean shutdown. Keep these generated files; the next steps extend them.

## Adding routes

Routes use the same patterns as Go's `http.ServeMux`, so a path can contain
wildcards in braces. In `internal/bootstrap/router.go`, add this route inside
the callback returned by `Router`, before `return routes`. Keep the existing
welcome page and probes. Add `net/http` and `arkoren.dev/core/http/response`
to this file's imports for the new handler:

```go
routes.Get("/hello/{name}", func(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")

	return response.JSON(w, http.StatusOK, map[string]string{
		"message": "Hello, " + name + "!",
	})
})
```

`r.PathValue` reads the `{name}` wildcard, and `response.JSON` encodes the
value and sets the `Content-Type` header. Stop the
service with Ctrl+C, run `HTTP_ADDR=127.0.0.1:8080 go run ./cmd/server` again,
and try it:

```sh
curl localhost:8080/hello/Ada
```

```json
{
  "message": "Hello, Ada!"
}
```

## Adding a service

Real services store things. Arkoren's services, such as the cache, the
database, and queues, all work the same way: you create a **driver** for the
backend you want and wrap it in a **facade** that your code uses. To count
how often each name has been greeted, add these imports to
`internal/bootstrap/bootstrap.go`:

```go
"arkoren.dev/core/service/cache"
"arkoren.dev/core/service/cache/memorycache"
```

Inside `Compose(compose *application.Composer)`, replace
`routes := compose.Use(Router())` with the following setup. Create the cache
before the router, register its cleanup immediately, and pass it to the factory:

```go
driver := memorycache.New()
compose.Cleanup(driver.Stop)
compose.Startup(driver.Start)
visits := cache.New(driver)

routes := compose.Use(Router(visits))
```

Do not keep the original router call or apply its routes twice. Keep
`compose.UseDebug(MCP(routes))` and `compose.Use(Server(routes))` after this setup.
Cleanup runs in reverse registration order, so the server drains before the
cache closes. Every composer finishes before startup hooks run.

In `internal/bootstrap/router.go`, add the `cache` import and change the
function declaration to
`func Router(visits *cache.Cache) application.Factory[*router.Router]`.
Keep its returned callback's `*router.Router` result, the `routes := router.New()`
inside that callback, and its final `return routes`. The callback captures the
typed cache service; HTTP handlers use that service, not the composer. Keep
`name := r.PathValue("name")` in the `/hello/{name}` handler, and replace its return
statement with a counter increment and the new response:

```go
key := cache.NewKey[int64]("greetings." + name)
count, err := visits.Increment(r.Context(), key, 1)
if err != nil {
	return err
}

return response.JSON(w, http.StatusOK, map[string]any{
	"message": "Hello, " + name + "!",
	"count":   count,
})
```

Restart the service and request `/hello/Ada` twice. The second request returns:

```json
{
  "message": "Hello, Ada!",
  "count": 2
}
```

The counts live only in this process and reset each time you restart it.
`NewKey[int64]` creates a typed counter key with the exact supplied name, so
Ada's counter is `greetings.Ada`. Creating the key does not write a value;
`Increment` updates the stored count atomically.

Later, when the service runs on several machines and the count must be shared,
you replace `memorycache.New()` with a Redis driver. The handler stays the same,
because it only knows the `visits` facade. Install the Redis module, configure its
connection and credentials, and register its lifecycle as shown in
[Cache Drivers](../cache/drivers.md#redis).
[Facades and drivers](../core-concepts/facades-and-drivers.md) explains why.

## Returning errors

When a handler cannot do its job, it returns an error, and the router turns it
into a response. Reject names that are too long by returning a ready-made
problem from `arkoren.dev/foundation/problem`. Add
`"arkoren.dev/foundation/problem"` to the file's import block, then insert
the check immediately after the existing `name := r.PathValue("name")` line,
before incrementing the counter:

```go
if len(name) > 40 {
	return problem.UnprocessableContent().Derive(func(details *problem.Builder) {
		details.Detail("The name is longer than 40 bytes.")
	})
}
```

`len` counts bytes, not Unicode characters. Restart the service and send a
name of 41 ASCII characters:

```sh
curl -i localhost:8080/hello/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```

The client receives a [Problem Details](https://www.rfc-editor.org/rfc/rfc9457)
response, a standard JSON shape for HTTP errors:

```json
{
  "type": "about:blank",
  "title": "Unprocessable Content",
  "detail": "The name is longer than 40 bytes.",
  "status": 422,
  "instance": "/hello/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}
```

With the default renderer, errors returned before the response is committed use
this shape. Once headers or a streaming body have been sent, a handler cannot
replace that response with a problem document.
[Error handling](../http-fundamentals/error-handling.md) shows how to control the
status and the fields.

## Checking health probes

Platforms such as Kubernetes ask a service whether it is alive and ready
before they send it traffic. The generated `Router` factory already registers the
application's probes; leave these lines in place:

```go
routes.Get(application.LivePath, app.LiveHandler())
routes.Get(application.ReadyPath, app.ReadyHandler())
routes.Get(application.StartupPath, app.StartupHandler())
```

Restart the service and request `curl -i localhost:8080/readyz`. Liveness and
readiness answer `200 OK` with `ok` while this application runs. During shutdown they
answer `503 Service Unavailable` while the server can still accept requests;
before the server starts or after it closes, no HTTP response is available.

## Stopping the service

Press Ctrl+C in the terminal that runs the service. The application logs each
step as it stops the server and the cache:

```text
level=INFO msg="app stopping" env=development service=hello
level=INFO msg="app stopped" env=development service=hello
```

The server allows in-flight requests up to 15 seconds to finish by default,
then closes remaining connections. The generated executable also handles
`SIGTERM`. For deployment, align the shutdown budgets as described in
[Deploying to Production](deploying.md#graceful-shutdown).

## Next steps

You have built a complete service. From here:

- [Project Layout](project-layout.md) shows how to organize feature packages
  as the service grows beyond the starter.
- [Application](../application/overview.md) guides explain composition,
  lifecycle ownership, failures, and readiness.
- [Routing](../http-fundamentals/routing.md) and [Validation](../validation/overview.md) cover
  handlers and request input in depth.
- [Database Overview](../database/overview.md) adds a database
   to the service.

For every lifecycle and server option, see the [`application` API reference](https://pkg.go.dev/arkoren.dev/core/application)
and the [`server` API reference](https://pkg.go.dev/arkoren.dev/core/http/server).
