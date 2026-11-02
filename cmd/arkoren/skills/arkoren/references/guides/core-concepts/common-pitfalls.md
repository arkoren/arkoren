---
title: Common Pitfalls
description: Recognize unsafe assumptions about ownership, configuration, HTTP behavior, data access, and deployment.
order: 50
summary:
  - Avoid late cleanup, unchecked connectivity, and overly broad service dependencies.
  - Diagnose callback precedence and missing event or telemetry configuration.
  - Recognize middleware, recovery, validation, SQL dialect, and pagination mistakes.
  - Protect operator endpoints and align backend state and shutdown behavior with deployment requirements.
---

# Common Pitfalls

When a service starts but behaves differently than expected, first check the
boundary involved: who owns the resource, when configuration takes effect, and
which guarantees the selected backend provides. These mistakes can leave code
that compiles but does not do what its caller assumes.

## Resource Ownership

### Late Cleanup

If composition acquires a resource and then fails while constructing a
consumer, cleanup registered afterward is never reached. Register release as
soon as you own the resource, before the next fallible operation.

Application cleanup becomes active immediately and runs in reverse registration
order, even if startup never runs. A registered stop function must therefore
tolerate an unstarted resource. Keep that ownership in one place using
[Lifecycle and Ownership](../application/lifecycle-and-ownership.md), rather
than duplicating lifecycle wiring in every feature.

### Unchecked Connectivity

A constructed driver is not necessarily connected. For example, Redis cache's
constructor creates its client without network I/O; `Start` checks connectivity.
Omitting startup moves that failure to the first operation instead of rejecting
startup.

Follow the selected driver's setup, including its available startup, cleanup,
and readiness methods. Do not assume every driver has all three: some are
stateless, and some memory drivers need no startup. Register lifecycle work
with either your existing host/container or Arkoren's application, not both.

### Broad Dependencies

Passing `*application.Application` into business services gives them host
control and runtime state they do not need. Passing a composer also permits
hidden lifecycle registration. Keep those objects at the composition boundary;
pass typed services, a logger, or an event bus explicitly.

An event subscription is also an owned resource. Register its unsubscribe
function for release instead of leaving listeners attached across repeated
composition or tests; [Own Subscriptions](../application/composition.md#own-subscriptions)
shows the focused pattern.

## Construction Policies

### Override Order

Construction callbacks run in argument order on one draft. When a setting
replaces an earlier value, put shared defaults first and the local override
last. With an existing `cache.Driver` named `driver` and a shared
`cache.BuilderFunc` named `defaults`, import `arkoren.dev/core/service/cache`:

```go
store := cache.New(driver, defaults, func(store *cache.Builder) {
	store.Name("reports")
})
```

The final name is `reports`; reversing the callbacks lets `defaults` replace
it. This is construction-time configuration, not a runtime settings API.
Changing a retained draft cannot reconfigure the published facade. See
[Configuration](../first-steps/configuration.md#shared-policies) for reusable
policies and validation boundaries.

### Missing Observations

Registering an event listener does not connect every facade to that bus.
Components that expose an `Events` setting publish only when given a bus; a
database listener on the application's bus sees nothing if the database facade
uses no bus. Application lifecycle events are separate from facade events.

Likewise, a facade with no explicit telemetry follows `telemetry.Default()`;
the initial process default records nothing. Setting a default does not start
its exporters. Check both the component's construction and the selected
telemetry's lifecycle before diagnosing a missing metric. Use
[Observe Outcomes](../application/lifecycle-and-ownership.md#observe-outcomes)
for lifecycle reporting and [Capture Setup](../diagnostics/capture-setup.md)
for diagnostic capture. A failed diagnostic-tool declaration does not
necessarily undo capture attachment.

## HTTP Boundaries

### Middleware Registration

Router middleware is applied when routes are registered. Calling `Use` after
registering a route through that router or one derived from it panics; existing
routes would otherwise silently miss the new policy. Add shared middleware
before routes, and choose a group or `With` for narrower policies as described
in [Choose Scope](../http-fundamentals/middleware.md#choose-scope).

### Explicit Recovery

`router.New` does not install panic recovery. For a buffered handler, install
recovery before registration so an ordinary panic can become a `500` Problem
Details response. Import `arkoren.dev/core/http/router`; `status` below is your
existing `router.Handler`:

```go
routes := router.New()
routes.Use(router.Recover())
routes.Get("/status", status)
```

Recovery protects only the middleware and handlers inside it. Keep request
logging outside recovery so it observes the converted error; [Order Policies](../http-fundamentals/middleware.md#order-policies)
explains the order. Once a response is committed, recovery cannot replace bytes
already sent with a fresh error document.

### Hidden Errors

Writing an error-shaped JSON body and returning `nil` tells outer middleware
that the handler succeeded. It also bypasses central Problem Details rendering
and its request-instance handling. Return the error instead of manually
writing its representation, using [Error Handling](../http-fundamentals/error-handling.md).
Streams have a separate commitment boundary; follow
[Streaming Responses](../http-fundamentals/streaming-responses.md) rather than
trying to render a second response after streaming starts.

### Optional Zero Values

Go-value validation treats scalar `0`, `""`, and `false` as absent. Ordinary
range and format rules skip absent values, so an integer schema with only
`Min(1)` accepts zero. Add `Required` when zero means missing. If zero is a
real supplied value that must be checked, use a pointer schema so a non-nil
pointer is present and its pointee is validated.

Decoding alone does not apply these rules. Follow
[Validate Input](../http-fundamentals/requests.md#validate-input) to run the
schema at the request boundary, and [Validation](../validation/overview.md)
for presence semantics. Exact encoded JSON validation has different presence
rules from validation of Go zero values.

## Data Assumptions

### Dialect Selection

`query.Query.Build()` uses ANSI SQL with `?` placeholders. Sending that SQL
directly to PostgreSQL does not select PostgreSQL's `$1` placeholders; MySQL
also has different identifier quoting. Pass published queries to the database
facade so it compiles for the configured driver's dialect. If you need SQL
text, use `BuildFor` with the intended dialect, as shown in
[Query Builder](../queries/query-builder.md).

### Stable Pagination

Cursor pagination needs a total order. Ordering only by a non-unique timestamp
can skip or repeat rows at page boundaries. For a `reports` table with a unique
`id`, import `arkoren.dev/foundation/query` and end the order with that key:

```go
reports := query.Table("reports", func(statement *query.Builder) {
	statement.OrderByDesc("created_at")
	statement.OrderByDesc("id")
})
```

Rows sharing `created_at` now have a deterministic tie-breaker. Use
[Pagination](../queries/pagination.md) for cursor construction and
[Batching and Streaming](../database/batching-and-streaming.md) for traversing
larger result sets; ordering alone does not choose a traversal strategy.

## Deployment Limits

### Process-Local State

Memory drivers are useful for tests, development, and intentionally ephemeral
state. Separate replicas do not share their contents, and a restart loses
them. Before deploying state that must survive or be shared, select a suitable
backend and verify its persistence configuration using
[Choosing a Driver](../drivers/choosing-a-driver.md).

### Operator Exposure

`InfoHandler`, verbose readiness, and diagnostic tools can reveal internal
metadata. They do not become protected merely because they are mounted on a
second port. Add an actual network or authorization boundary before exposing
them. [Mount Probes](../application/health-and-readiness.md#mount-probes)
distinguishes minimal probes from detailed reports.

### Shutdown Budgets

If the platform kills a process before draining and cleanup finish, connections
and in-flight work can be cut off. Keep the HTTP drain timeout inside the
application budget and allow additional platform time for cleanup and telemetry
finalization. Follow [Shutdown Budgets](../first-steps/deploying.md#shutdown-budgets)
instead of copying one timeout to every layer.

Shutdown does not automatically cancel request contexts already in progress.
A long poll or server-sent event handler must also observe
`server.ShuttingDown(ctx)` and return cooperatively; otherwise it can hold the
drain open until the timeout. [Streaming Responses](../http-fundamentals/streaming-responses.md)
shows that cancellation boundary.
