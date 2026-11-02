---
title: Deploying Search
nav: Deploying
description: Synchronize backend indexes during deployment, verify definitions at startup, and recover safely from schema changes or partial failures.
order: 50
summary:
  - Prepare infrastructure and synchronize logical index definitions before serving requests.
  - Separate release-time changes from startup schema checks and readiness probes.
  - Reconcile failed updates and stage field changes across overlapping application versions.
  - Retire or rebuild indexes explicitly while preserving a source for repopulation.
---

# Deploying Search

An index declaration describes the fields your code expects; the backend must
be prepared to serve them. `Sync` creates missing logical indexes and applies
changed definitions. `Check` verifies those definitions without changing the
backend. Neither happens implicitly during a query or document write.

Use the configured `engine`, registered definitions, and concrete `driver` from
[Search Drivers](drivers.md). Examples use your operation context `ctx` and
import `search` from `arkoren.dev/core/service/search` plus standard-library
`errors` where shown.

## Preparing Indexes

Prepare infrastructure before synchronizing logical indexes. For Postgres,
the application registers `driver.Migrations()` and executes its migrator before
`driver.Start`; the search driver never runs DDL. `Sync` writes definitions and
search vectors, not tables. Follow [Postgres setup](drivers.md#postgres) for
permissions and immutable migration identities.

After starting the provider, synchronize all registered definitions. Stop the
deployment if synchronization fails:

```go
if err := engine.Sync(ctx); err != nil {
	return err
}
```

Successful synchronization keeps existing documents, can reindex them, and is
safe to repeat with the same intended definition. It never prunes backend
indexes removed from registration. Before synchronization, nonempty document
writes and searches needing a missing index fail with `search.ErrNotFound`.

An invalid declaration, nil custom registration, or duplicate name fails before
driver I/O. Applying migrations or repeating synchronization cannot fix that:
correct the declaration and construct a new engine. For valid definitions,
provider-specific changes can still require an explicit rebuild rather than
an in-place sync.

For one process owning a local backend, run `Sync` as a startup hook after
driver startup. Memory has no lifecycle methods; register only
`composer.Startup(engine.Sync)` and repopulate its process-local documents.
Persistent shared backends should use the release path below.

## Startup Checks

An instance should refuse startup if the backend does not match its code. Use
`Check` to classify a missing or changed index:

```go
err := engine.Check(ctx)
switch {
case errors.Is(err, search.ErrNotFound):
	return err // The release must prepare the missing index.
case errors.Is(err, search.ErrSchemaChanged):
	return err // The release must apply the intended definition.
case err != nil:
	return err
}
```

After successful `Sync`, `Check` passes for those same definitions within the
provider's comparison scope. Additional backend indexes are ignored. OpenSearch
compares the last completed schema checkpoint, not every live mapping setting or
document representation; it is not an external-administration detector.

Run this once at startup, not on every readiness probe. During a rolling
deployment, old instances still have the previous definitions, so repeated
`Check` calls would make them unhealthy as soon as synchronization updates the
backend. Use the concrete driver's `Ping` for readiness instead.

## Release Deployment

For a shared persistent backend, use one serialized release command containing
the new release's definitions. Apply shipped table migrations first, start the
driver, run `Sync`, and require a successful `Check` before rolling out instances.
Do not run migrations or synchronization independently on every replica.

The instance path starts the driver and checks schema once. With `application`
imported from `arkoren.dev/core/application`, and an existing composer:

```go
composer.Cleanup(driver.Stop)
composer.Startup(driver.Start)
composer.Startup(engine.Check)
composer.Check("search", driver.Ping)
```

These hooks apply to concrete native drivers, not Memory. Start Postgres's
database owner before its search driver and close that owner after application
operations finish. Register owned OpenSearch cleanup even if `Start` fails:
acquired SDK resources remain owned until `Stop`. See
[native ownership](drivers.md#native-access) for its cancellation and wait limits.

Do not treat a failed or canceled `Sync` as a rollback. Some settings or indexes
can already have changed, and Meilisearch server tasks can continue after the
caller stops waiting. Stop rollout, inspect the backend, reconcile with the
intended definition, retry synchronization, and check again. Keep original source
records available for a rebuild.

Postgres synchronizes under a transaction-scoped advisory lock keyed by its
documents table, so concurrent syncs queue. That does not coordinate your whole
release. Meilisearch metadata reads and tasks are not atomic. OpenSearch mapping
edits can become visible before reindexing finishes; failure leaves the previous
completed checkpoint so a later sync can retry. Serialize deployments and native
administration rather than assuming one cross-provider migration lock.

For release visibility, use [deployment events](monitoring.md#deployment-events)
and telemetry while still checking returned errors directly.

## Changing Fields

Plan definition changes around old and new instances overlapping. To add a
field, first deploy writers that include it in stored JSON. Synchronize the new
definition, then deploy readers that query it. If older documents lack the value,
backfill them from source with [batched `Put`](indexing-documents.md#batch-behavior).
Reindexing can use stored values but cannot invent values never indexed.

To remove a field, first deploy readers that stop querying it, then remove its
role from the declaration and synchronize. Verify the old/new query paths before
rollout; successful schema synchronization alone does not prove application
compatibility.

Field changes can be expensive: Postgres recomputes vectors when searchable
fields change; native providers can run reindexing work. OpenSearch cannot
promote an existing keyword field to searchable text in place. Its former flat
document layout also needs explicit recreation and repopulation for the current
typed wrapper. Schedule those changes as rebuilds, not ordinary startup repairs.

## Retiring Indexes

Removing a declaration from code leaves its backend index intact. To retire it,
run a release command that still registers the exact definition, then explicitly
drop it:

```go
if err := engine.Drop(ctx, Products); err != nil {
	return err
}
```

`Drop` permanently removes the index and every indexed document. It succeeds if
the backend index is already absent, but the engine still requires registration.
Only remove the definition after this step succeeds and no running code uses it.

For a destructive rebuild, plan a maintenance window: drop the index,
synchronize it empty, then repopulate from source in bounded batches. Search is
unavailable or incomplete until repopulation finishes. Neither `Sync` nor
`Check` verifies that every source record has been copied; confirm expected
documents and representative searches before restoring traffic. Never run this
sequence as an automatic response to a request-time error.

For every function and option, see the [search API reference](https://pkg.go.dev/arkoren.dev/core/service/search).
