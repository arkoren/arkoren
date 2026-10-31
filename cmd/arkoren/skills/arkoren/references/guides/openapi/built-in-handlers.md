---
title: Documenting Built-in Handlers
nav: Built-in Handlers
description: Annotate application probes and information endpoints, and describe integrations explicitly where no contract can be inferred.
order: 40
---

# Documenting Built-in Handlers

## Introduction

To include health probes and application information in your HTTP contract, pass
their reusable operation callbacks when you register the handlers. These callbacks
describe shipped behavior without sampling a running application; they do not
register routes or protect operator endpoints.

Other integrations, such as webhooks and downloads, need declarations that match
your actual provider and handler configuration; the exporter does not infer those
contracts.

## Annotating application probes

With your existing `app` and `routes`, import `arkoren.dev/core/application` and
pass the matching operation callback directly to each probe registration:

```go
routes.Get(application.LivePath, app.LiveHandler(), application.LiveOperation)
routes.Get(application.StartupPath, app.StartupHandler(), application.StartupOperation)
routes.Get(application.ReadyPath, app.ReadyHandler(), application.ReadyOperation)
```

These callbacks describe the handler on the path you choose. The constants use
`/livez`, `/startupz`, and `/readyz`, but a custom path or group prefix works too.
They do not register routes, run probes, capture current health values, or change
access policy. Omit a callback if you do not want that route in OpenAPI; the
handler continues to work.

All three describe plain-text `200` (`ok\n`) and `503` (`unavailable\n`) responses,
with `Cache-Control: no-store` and `X-Content-Type-Options: nosniff` headers.
Liveness succeeds while the application is live. Startup succeeds once startup
completed, including while stopping and after stop; it is not a substitute for
readiness. Follow the [health and readiness guide](../application/health-and-readiness.md) to
configure the underlying lifecycle and checks.

### Describing verbose readiness

Readiness succeeds when the application is live and its cached health pass is
fresh and passing. Without registered checks, readiness equals liveness. Requests
never run checks themselves. With checks, readiness waits for the first passing
pass, and a result older than three readiness intervals counts as failed.

`application.ReadyOperation` also describes the optional `verbose` query parameter and
JSON responses at both `200` and `503`. This is a presence-based parameter:
`?verbose`, `?verbose=`, `?verbose=false`, and any other value all select JSON
instead of plain text. It is described as a string, not a boolean switch.

The static output schema includes `status` and optional `reason`, `checked_at`,
and `checks`. Each check can contain a status and an error string. It does not
embed actual dependency names or failure messages into the OpenAPI document.
Enabling documentation does not enable detailed failures: failed checks report
`reason withheld` unless `HealthDetails(true)` was configured on the application.

> [!WARNING]
> Verbose readiness reveals dependency names even when failure details are
> withheld. Enabling `HealthDetails` can also expose connection information from
> errors. Restrict the live probe endpoint to operators and the platform; hiding
> its OpenAPI annotation is not access control.

## Annotating application information

If operators use the information endpoint, describe it with its shipped
callback on your existing protected `admin` router:

```go
admin.Get(application.InfoPath, app.InfoHandler(), application.InfoOperation)
```

`application.InfoPath` is `/infoz`. `InfoOperation` describes the `200` JSON response and
the same no-store and nosniff headers as the probes. Its static schema describes
application identity, environment, state, log level, build metadata, and optional
startup and uptime fields without embedding current application or binary values.
Omitted runtime fields remain optional in the schema.

This is an operator-only endpoint: version, revision, and build information can
reveal deployment details. Neither `InfoHandler` nor its annotation installs or
infers authentication. Use a protected router or a
[restricted listener](../application/health-and-readiness.md), and
protect any document that includes private endpoint names and schemas separately.

## Extending a built-in description

If a protected probe has additional middleware failures or a security
requirement, keep the shipped callback and add your own trailing callback:

```go
admin.Get(application.InfoPath, app.InfoHandler(), application.InfoOperation, func(operation *router.OperationBuilder) {
	operation.Tags("operations")
	operation.Security("operatorBearer")
})
```

This fragment also uses `arkoren.dev/core/http/router`. Declare `operatorBearer`
on the document as explained in [Describing Operations](operations.md#describing-authentication),
and configure the actual authentication separately. Callbacks run in order on
the same draft, so later scalar settings replace earlier ones. No operation ID
is supplied by the shipped callbacks; add unique IDs yourself if needed.

## Describing integrations manually

A handler's package or return type does not establish its full wire contract.
For these integrations, annotate only the behavior your application actually
serves:

- [OAuth login](../authentication/oauth.md) uses redirects and provider
  callbacks. Describe the real statuses and `Location` response header with
  `Response` and `ResponseHeader`, not a guessed JSON response. Document-level
  security schemes do not support OAuth flows or OpenID Connect.
- [Webhooks](../http-capabilities/webhooks.md) use provider-specific payloads and signature
  headers. Declare body schemas and header parameters explicitly. An annotation
  does not verify a signature or discover provider-specific failures.
- [Downloads](../file-storage/uploads-and-downloads.md) and
  [metrics](../observability/telemetry-drivers.md#prometheus) need their actual
  media types, such as `application/octet-stream` or the configured text
  representation, rather than `JSONResponse`. Describe response headers
  separately; [response declarations](operations.md#describing-response-bodies-and-headers)
  show the pattern.
- A mounted [MCP endpoint](../mcp/first-server.md) remains a separate protocol.
  The exporter does not traverse mounted handlers or infer their internal
  operations. Leave mounts and catch-all routes unannotated or hidden; visible
  annotated opaque paths fail export.

Global middleware and authentication do not automatically become error responses
or security requirements for any of these handlers. Add declarations only when
they match the policy and response format actually enforced at that route.

For the probe and information callbacks, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application). For manual declarations, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
