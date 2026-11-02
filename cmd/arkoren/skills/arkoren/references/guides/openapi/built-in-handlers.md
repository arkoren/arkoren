---
title: Built-in Handlers
description: Attach shipped application annotations and identify integrations that need an explicit, application-specific contract.
order: 40
summary:
  - Attach matching application callbacks to probes, information endpoints, and welcome pages.
  - Distinguish static readiness and information schemas from live diagnostic values and disclosure risks.
  - Extend annotations for enforced policies and identify integrations that require manual contracts.
---

# Built-in Handlers

Some shipped handlers have matching operation callbacks, so you can describe
their responses without repeating their schemas. Pass the callback when
registering the handler; the exporter does not infer a contract from its type or
package. The callback describes behavior, but neither registers the route nor
changes its access policy.

Use this page to find application annotations and recognize common manual
integration boundaries. It is not a guarantee that every built-in handler or
domain integration has a complete reusable contract.

## Application Annotations

With your existing `app` and `routes`, import `arkoren.dev/core/application`.
Choose the callback that matches the handler you are registering:

| Handler | Callback | Declared Output |
| --- | --- | --- |
| `LiveHandler()` | `LiveOperation` | Plain-text `200` or `503`. |
| `StartupHandler()` | `StartupOperation` | Plain-text `200` or `503`. |
| `ReadyHandler()` | `ReadyOperation` | Plain text or verbose JSON at `200` or `503`. |
| `InfoHandler()` | `InfoOperation` | Application and build information as `200` JSON. |
| `WelcomeHandler()` | `WelcomeOperation` | A welcome page as `200` HTML. |

For probes, pass the callbacks directly to their registrations:

```go
routes.Get(application.LivePath, app.LiveHandler(), application.LiveOperation)
routes.Get(application.StartupPath, app.StartupHandler(), application.StartupOperation)
routes.Get(application.ReadyPath, app.ReadyHandler(), application.ReadyOperation)
```

The path constants are `/livez`, `/startupz`, and `/readyz`. Custom paths and group
prefixes work too: descriptions follow the registered path, not a hard-coded
endpoint. Omit a callback to leave that handler out of OpenAPI without disabling
the route.

All three callbacks describe plain-text `ok\n` at `200` and `unavailable\n` at
`503`, with `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`.
Liveness succeeds while the application is live. Startup records completed
startup, including while stopping and after stop; it is not a readiness check.
Configure the lifecycle and dependency checks through
[health and readiness](../application/health-and-readiness.md).

For the public welcome page, choose your own path:

```go
routes.Get("/", app.WelcomeHandler(), application.WelcomeOperation)
```

`WelcomeOperation` describes HTML and the shared no-store/nosniff headers, without
capturing the configured application name. The page displays that name and
Arkoren resource links, not runtime, configuration, or build diagnostics. Choose
an application name suitable for public display.

## Readiness Details

Readiness reports whether the application is live and its cached health pass is
fresh and passing. Without checks it equals liveness. With checks, it waits for
the first passing pass; a result older than three readiness intervals is stale
and fails readiness. Requests do not run checks themselves.

`ReadyOperation` describes an optional string query parameter named `verbose`.
Its presence selects JSON: `?verbose`, `?verbose=`, and `?verbose=false` all have
the same effect. It is not a boolean opt-out. The callback includes JSON schemas
at both `200` and `503`, alongside the plain-text representations.

The static schema has a required `status` and optional `reason`, `checked_at`,
and `checks`. Each check can report its status and an error string. Export does
not insert current dependency names, check results, or failure messages into the
document.

> [!WARNING]
> The live verbose response reveals dependency names. Failed checks normally
> report `reason withheld`, but `HealthDetails(true)` can also reveal connection
> information in error messages. Restrict the endpoint to operators and the
> platform; omitting its annotation does not restrict access.

## Application Information

If operators need application identity and build details, register the information
handler on an already protected `admin` router:

```go
admin.Get(application.InfoPath, app.InfoHandler(), application.InfoOperation)
```

`InfoPath` is `/infoz`. The callback describes its `200` JSON response and
no-store/nosniff headers. Its schema includes identity, environment, state, log
level, build metadata, and optional startup and uptime fields. Runtime fields
that can be omitted remain optional; current application and binary values are
not embedded in the contract.

The live response can reveal versions and revisions, so expose it only to
operators. Neither the handler nor callback supplies authentication. Use your
existing protected router or a
[restricted listener](../application/health-and-readiness.md), and separately
protect documentation that includes private endpoint names and schemas.

## Extend Annotations

If your protected information route requires bearer authentication, keep the
shipped callback and add your policy description afterward. This fragment also
uses `arkoren.dev/core/http/router`:

```go
admin.Get(application.InfoPath, app.InfoHandler(), application.InfoOperation, func(operation *router.OperationBuilder) {
	operation.Tags("operations")
	operation.Security("operatorBearer")
})
```

Declare `operatorBearer` in the document's
[security configuration](operations.md#declare-security). This fragment assumes
the `admin` router already enforces authentication; `Security` only describes it.
Add middleware failure responses explicitly when they apply.

Callbacks run in order on the same draft, so later scalar settings replace
earlier ones. Shipped application callbacks do not set operation IDs; add unique
IDs if your clients need them. They do not infer middleware errors or security
requirements.

## Manual Contracts

Other handlers depend on provider configuration or implement a protocol that a
single operation cannot describe. Annotate only the behavior you actually serve:

- [OAuth login](../authentication/oauth.md) uses redirects and provider callbacks.
  Describe actual statuses and the `Location` header with `Response` and
  `ResponseHeader`, rather than assuming JSON. The document API does not support
  OAuth-flow or OpenID Connect security schemes.
- [Webhook receivers](../webhooks/receiving-webhooks.md) need provider-specific
  bodies and signature headers. Declare them explicitly; annotations neither
  verify signatures nor discover provider-specific failure responses.
- [Downloads](../file-storage/uploads-and-downloads.md) and
  [Prometheus metrics](../observability/telemetry-drivers.md#prometheus) need their
  actual media types and headers. Use `MediaResponse`, not `JSONResponse`, for
  binary or text output. The [response declarations](operations.md#describe-responses)
  show the pattern.
- Mounted [MCP endpoints](../mcp/first-server.md) remain a separate protocol.
  Export does not traverse their handlers or infer internal operations. Leave
  mounts and catch-all routes unannotated or hidden; visible annotated opaque
  paths fail export.

For every integration, match the body, headers, statuses, and security policy to
the configured handler and middleware. A static annotation or diagnostic route
listing does not discover or maintain that domain contract for you.

For shipped callbacks, see the
[`application` API reference](https://pkg.go.dev/arkoren.dev/core/application).
For manual declarations, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
