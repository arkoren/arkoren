---
title: Quickstart
description: Extend the generated HTTP service with a greeting route, a process-local counter, and a checked error response.
order: 30
summary:
  - Run the starter and confirm its welcome page and readiness probe.
  - Connect an error-returning handler to a POST route.
  - Add an atomic cache counter with application-owned startup and cleanup.
  - Return a client-safe problem response and stop the service cleanly.
---

# Quickstart

Build a greeting endpoint, count greetings in memory, and reject an oversized
name with an HTTP error. No database, broker, or credentials are needed.

Start with the `hello` service from [Creating Projects](creating-a-project.md),
including its dependency-resolution step. Run every command below from that
project's root. Keep the generated bootstrap files: the edits extend them rather
than replacing their lifecycle, middleware, or probes.

## First Request

Start the service on loopback:

```sh
HTTP_ADDR=127.0.0.1:8080 go run ./cmd/server
```

The shell examples use POSIX environment assignments. In PowerShell, set
`$env:HTTP_ADDR = "127.0.0.1:8080"` before running `go run ./cmd/server`.
If port 8080 is occupied, choose another port and use it in the requests below.

The development `.env` enables unauthenticated diagnostics on the same listener.
Keep the loopback address; do not expose this development process publicly.
[Starter Defaults](creating-a-project.md#starter-defaults) explains the settings.

In another terminal, request the welcome page and readiness probe:

```sh
curl -i http://127.0.0.1:8080/
curl -i http://127.0.0.1:8080/readyz
```

Both return `200 OK`; `/` contains an HTML welcome page and `/readyz` contains
`ok`. The server's startup log reports its bound address. If binding fails, the
process exits unsuccessfully rather than reporting a successful HTTP start.

Stop with Ctrl+C before editing. Repeat the start command after each edit below;
the starter does not watch or reload source files.

## Greeting Handler

An Arkoren HTTP handler takes the familiar response writer and request, then
returns an error. The router renders returned errors centrally, so handlers can
return failures without writing a second response.

Create `internal/bootstrap/greeting.go`. Start it with the package, imports,
and a typed response:

```go
package bootstrap

import (
	"net/http"

	"arkoren.dev/core/http/response"
)

// Greeting is the JSON returned for an accepted greeting.
type Greeting struct {
	Message string `json:"message"`
}
```

Append this handler to the same file. It reads a route wildcard named `name` and
returns JSON:

```go
// greet responds to a greeting request using the route's name.
func greet(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")

	return response.JSON(w, http.StatusOK, Greeting{
		Message: "Welcome, " + name + "!",
	})
}
```

`response.JSON` sets the content type and encodes the response. Returning its error
keeps encoding or write failures visible to the router.

## Route Registration

In `internal/bootstrap/router.go`, inside the callback returned by `Router()`,
add this line before `return routes`:

```go
routes.Post("/greetings/{name}", greet)
```

Keep the existing middleware, welcome route, and probe routes. Registration maps
the method and path to the handler; it does not open a listener. Use POST because
the next step records a greeting and changes state.

Restart the service, then send a greeting:

```sh
curl -i -X POST http://127.0.0.1:8080/greetings/Mira
```

The response is `200 OK` with this JSON content:

```json
{
  "message": "Welcome, Mira!"
}
```

The starter's CSRF protection allows this command-line request without Origin
or Sec-Fetch-Site headers. That is not authentication or permission to expose
state-changing routes without an application security policy.

## Counting Greetings

Keep counters behind a cache facade so the handler does not depend on how values
are stored. In `internal/bootstrap/bootstrap.go`, add these imports alongside
the existing `application` import:

```go
"arkoren.dev/core/service/cache"
"arkoren.dev/core/service/cache/memorycache"
```

Inside `Compose`, replace only `routes := compose.Use(Router())` with:

```go
driver := memorycache.New()
compose.Cleanup(driver.Stop)
compose.Startup(driver.Start)
visits := cache.New(driver)

routes := compose.Use(Router(visits))
```

Keep the following `compose.UseDebug(MCP(routes))` and
`compose.Use(Server(routes))` calls. Cleanup is registered immediately at the
driver's owner and runs in reverse order, so the server stops before the cache.
Composition finishes before startup starts the cache's expiry janitor and binds
the HTTP listener. Failures during composition or startup also release acquired
resources.

In `internal/bootstrap/router.go`, import `arkoren.dev/core/service/cache` and
change the factory declaration to accept the facade:

```go
func Router(visits *cache.Cache) application.Factory[*router.Router] {
```

Keep the existing factory body and replace the greeting registration with this
closure, which passes the shared facade to the handler:

```go
routes.Post("/greetings/{name}", func(w http.ResponseWriter, r *http.Request) error {
	return greet(w, r, visits)
})
```

Add `net/http` to that file's imports. In `greeting.go`, add the `cache` import
and a `Count int64` field with the tag `json:"count"` to `Greeting`. Replace
the entire `greet` function with:

```go
// greet counts an accepted greeting and returns its current process-local total.
func greet(w http.ResponseWriter, r *http.Request, visits *cache.Cache) error {
	name := r.PathValue("name")
	key := cache.NewKey[int64]("greetings." + name)
	count, err := visits.Increment(r.Context(), key, 1)
	if err != nil {
		return err
	}

	return response.JSON(w, http.StatusOK, Greeting{
		Message: "Welcome, " + name + "!",
		Count:   count,
	})
}
```

The typed key names Mira's counter exactly `greetings.Mira`. `Increment` creates
a missing counter and updates it atomically; it does not use the key's write TTL.
These counters have no expiry, belong only to this process, and reset on restart.
They are demonstration state, not durable business records or an exactly-once
record of delivered responses. A write can succeed even if sending its response
later fails.

Restart, then issue the POST request twice. The second response contains:

```json
{
  "message": "Welcome, Mira!",
  "count": 2
}
```

For shared counters across replicas, follow [Cache Drivers](../cache/drivers.md)
for backend construction and ownership. The handler continues to use the facade.
Use [Key Definitions](../cache/keys.md) and [Counters](../cache/counters.md) when
you need reusable naming or bounded counter windows.

## Expected Errors

Reject an oversized name before changing its counter. Add
`arkoren.dev/foundation/problem` to `greeting.go`'s imports. Immediately after
`name := r.PathValue("name")`, insert:

```go
if len(name) > 32 {
	return problem.UnprocessableContent().Derive(func(details *problem.Builder) {
		details.Detail("The name must be at most 32 bytes.")
	})
}
```

`len` counts bytes, not Unicode characters. Restart and send 33 ASCII characters:

```sh
curl -i -X POST http://127.0.0.1:8080/greetings/abcdefghijklmnopqrstuvwxyzabcdefg
```

The response has status `422 Unprocessable Content` and a Problem Details body:

```json
{
  "type": "about:blank",
  "title": "Unprocessable Content",
  "status": 422,
  "detail": "The name must be at most 32 bytes.",
  "instance": "/greetings/abcdefghijklmnopqrstuvwxyzabcdefg"
}
```

No counter is incremented for this rejected request. The router can render an
error only before the response is committed; it cannot replace headers or body
bytes already sent. See [Error Handling](../http-fundamentals/error-handling.md)
for private internal failures and custom problem responses.

## Stopping Safely

Press Ctrl+C in the service terminal. The generated executable's signal context
also handles `SIGTERM`. The application stops serving, runs registered cleanup,
and returns before the executable exits; clean shutdown exits with status 0,
while lifecycle failures exit with status 1.

The HTTP runner allows in-flight requests up to 15 seconds to drain by default.
After exit, another request fails to connect and the memory counters are gone.
This walkthrough creates no persistent backend or container to remove. Keep or
delete only the `hello` project you created; do not remove a shared parent directory.

You now have a working route, a shared dependency inside one process, and a
checked error path. Continue with [Configuration](configuration.md) to translate
application settings, [Project Layout](project-layout.md) to separate features,
or [Deployment](deploying.md#shutdown-budgets) before running outside development.
