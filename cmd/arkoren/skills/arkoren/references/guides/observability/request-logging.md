---
title: Request Logging
description: Log every failed request with its method, path, status, and error, correlated with traces, using any slog logger.
order: 25
---

# Request Logging

## Introduction

When a request fails, you want a log record that says which request it was and
why. The `requestlog` middleware writes one record for every request that
returns an error or has an observed `5xx` status. Successful requests are not
logged, because metrics and traces describe them better and at a fraction of
the volume.

## Usage

The record includes the concrete request path and raw error, which can contain
identifiers or sensitive values. Use a log destination, access policy, and
retention period appropriate for that data. If your telemetry driver exports
logs, these records can also leave the process; see
[Telemetry Privacy](telemetry-privacy.md#logs).

Import `arkoren.dev/core/http/middleware/requestlog`. With your existing
[application](../application/overview.md) `app` and router
`r`, register middleware before routes using the application's logger:

```go
r.Use(requestlog.Middleware(func(log *requestlog.Builder) {
	log.Logger(app.Logger())
}))
```

A failed request produces a record like this:

```json
{
  "level": "ERROR",
  "msg": "request failed",
  "method": "POST",
  "path": "/invoices",
  "status": 422,
  "error": "422 Validation failed"
}
```

Without `Builder.Logger`, it uses `slog.Default()`. Any `*slog.Logger` works,
including zap and zerolog through their slog handlers.

## What is logged

The record is written when the router finishes response handling. Its `status`
is the final HTTP status observed through the response writer, including errors
rendered by the router; it does not confirm that the client received the response.
A request is logged when its handler returned an error, whatever the status, or
when the observed status is `5xx`. A `4xx` response written directly, without
returning an error, is not logged.

A successful connection hijack is not reported as a failure merely because its
handshake bypassed the HTTP writer. If the handler returns an error after
hijacking, that error is still logged. Status `0` means no final HTTP status was
observed; it is not an inferred 500 or an HTTP status sent to the client. Bytes
written directly to the caller-owned connection are outside the response hooks.

This differs from [request tracing](request-tracing.md#spans), which marks only
`5xx` responses as failed server spans. A returned validation error can therefore
produce an error log while its `4xx` span status stays unset.

## Placement

Two neighbors matter. Put the telemetry middleware before request logging, so
each record carries the `trace_id` and `span_id` of the request and you can
jump from a log line to its trace. Put `router.Recover` after it, so a
recovered panic reaches the logger as an error:

In this alternative setup, `tel` is your started `*telemetry.Telemetry` from
`arkoren.dev/core/service/telemetry`, and `logger` is the application's
telemetry-wrapped logger. A plain unrelated `slog` handler does not add trace
IDs merely because telemetry middleware ran. Import `arkoren.dev/core/http/router`
for recovery:

```go
r.Use(
	tel.Middleware(),
	requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(logger)
	}),
	router.Recover(),
)
```

For every option, see the [`requestlog` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/requestlog).
