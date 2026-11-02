---
title: Request Logging
description: Record failed requests with the observed response status and optional trace correlation.
order: 40
summary:
  - Log returned handler errors and observed 5xx responses without logging every request.
  - Place tracing, request logging, and recovery so failures retain trace correlation.
  - Interpret observed statuses, raw error exposure, and connection-hijack limits.
---

# Request Logging

When a request fails, `requestlog` records its method, concrete path, observed
status, and returned error. It is failure logging, not an access log: a handler
error produces a record at any status, and an observed 5xx produces a record
even without a returned error. Other successful responses and directly written
4xx responses produce none.

## Failure Records

The path and raw error can contain personal identifiers, secrets, or native
diagnostics. Choose log access, retention, and any application-owned redaction
before enabling this middleware. If your telemetry driver exports logs, those
same fields can leave the process; span field exclusions do not protect them.
See [Logs](telemetry-privacy.md#logs).

With an existing application `app` and router `r`, import `requestlog` from
`arkoren.dev/core/http/middleware/requestlog` and select the application's logger
before registering routes:

```go
r.Use(requestlog.Middleware(func(log *requestlog.Builder) {
	log.Logger(app.Logger())
}))
```

If a report handler returns an error rendered as HTTP 503, its record contains
these fields (timestamps and other logger attributes are omitted):

```json
{
  "level": "ERROR",
  "msg": "request failed",
  "method": "POST",
  "path": "/reports",
  "status": 503,
  "error": "503 Service Unavailable"
}
```

The actual error string depends on the returned error. Any `*slog.Logger`
works. If `Logger` is nil or omitted, the middleware resolves `slog.Default()`
per request, so a later default change takes effect. Selecting a logger does
not itself enable telemetry or provide trace correlation.

## Middleware Order

For trace IDs and recovered panics, use this alternative registration with the
application's [configured telemetry](telemetry.md#shared-handle). Import
`router` from `arkoren.dev/core/http/router`:

```go
r.Use(
	app.Telemetry().Middleware(),
	requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(app.Logger())
	}),
	router.Recover(),
)
```

Telemetry runs first so logging receives the request's span context. The
application logger's telemetry wrapper adds `trace_id` and `span_id` when that
identity is valid. An unrelated plain slog handler does not acquire correlation
just because tracing middleware ran.

Recovery runs inside logging so recovered panics become returned errors the
logger can observe. Do not register both setup variants on the same route
stack: choose one, or you can produce duplicate failure records.

## Response Boundaries

Logging runs after the router completes response handling, including rendering
returned errors. The observed status is what passed through the response writer,
not evidence the client received those bytes. The middleware relies on Arkoren
router response hooks; exercise it through the router rather than invoking an
isolated wrapped handler.

A returned validation error rendered as 422 produces an error log, while its
[server span](request-tracing.md#recorded-fields) is not marked failed: HTTP
server spans classify 5xx responses as errors. These are intentionally different
signals, not contradictory outcomes.

A successful connection hijack is not a failure. If the handler returns an
error after taking ownership of the connection, it is still logged. Status
`0` means no final HTTP status passed through the writer; it is neither an
inferred 500 nor a status sent to the client. Direct connection writes are
outside the HTTP response hooks.

For configuration declarations, see the
[`requestlog` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/requestlog).
