---
title: Deploying Search
nav: Deploying
description: Create and update search indexes with Sync, verify them with Check, and wire both into single- and multi-instance deploys.
order: 30
---

# Deploying Search

## Introduction

An index definition lives in your code, but the index itself lives in the
backend, and the two must agree: the backend needs to know which fields are
searchable, filterable, and sortable before it can answer queries. Arkoren
gives you two operations for this. `Sync` makes the backend match the code, and
`Check` verifies, without changing anything, that it already does.

Creating and changing indexes is a deploy step rather than something that
happens implicitly on first use. A deploy that changes a definition may have to
rebuild the index, which can take minutes on a large one, and you want that to
happen once, in a place where you can watch it, not in whichever instance
starts first.

For one process that owns its backend, run `Sync` at startup. For several
instances or a production release, run it once in a release command before
starting new instances, then run `Check` at each instance's startup. If the
driver ships table migrations, the application must apply them before either
path uses the search driver; `Sync` does not replace those migrations.

## Sync

Examples use the registered `engine` from [Search Overview](overview.md), your
operation context `ctx`, and the `driver` and lifecycle hooks from
[Search Drivers](drivers.md). Import standard library `errors` and
`arkoren.dev/core/service/search` for error classification.

Registration validates each required `Indexer.Err()` before capturing its schema.
An invalid name, empty searchable-field list, nil key function, or custom definition
error is retained by the core facade. `Sync` and `Check` return that error before
calling the driver; applying migrations or repeating synchronization cannot repair
an invalid declaration. Fix it in code, then construct a new engine.

Before new code can search a newly declared field, the backend needs the
matching index settings. Run `engine.Sync(ctx)` as your deployment's update
step; it applies every index registered with `Builder.Indexes`:

```go
err := engine.Sync(ctx)
```

For an index that does not exist, `Sync` creates it. For one that exists, it
applies the settings that changed and keeps the documents. Changing which
fields are searchable, filterable, or sortable may make the backend reindex
the stored documents, which can be slow for large indexes, and documents stay
searchable under the new definition once `Sync` succeeds. `Sync` is safe to
call repeatedly: it keeps existing documents and reapplies the declared settings
without removing indexes.

`Sync` never removes indexes. An index that you removed from your code stays
in the backend until you call `engine.Drop(ctx, index)` for it. `Drop`, like
every call, needs the index to be registered with the engine, so drop an old
index from a release command that still registers it, then delete its
definition.

Until an index has been synced, `Put`, `Delete`, and `Search` on it fail with
`search.ErrNotFound`, which is the signal that a deploy step is missing.

To observe release-command outcomes without recording index or document data,
configure the engine's optional event bus when constructing it. With `driver`
and the `Products` definition from the overview, import `arkoren.dev/core/event`:

```go
events := event.New()
engine := search.New(driver, func(engine *search.Builder) {
	engine.Indexes(Products).Events(events)
})
```

Listen to `search.Synced`, `search.Dropped`, or `search.Failed` on this bus.
Their `search.MutationEvent` payload contains only the operation (`sync` or
`drop`), driver-call duration, and a bounded error class; it never contains index
names, queries, documents, IDs, or native error text. Nil `Events` disables them.
Invalid registrations, `Check`, `Put`, `Delete`, and queries emit nothing.
A successful sync can be a no-op, and a failed sync may have changed some
indexes. A successful drop includes an already absent index. These synchronous,
best-effort in-process observations prove neither an atomic commit nor index
consistency and are not a durable audit log. Listener errors and panics cannot
replace the operation's returned result; keep handling that result directly.
Driver panics propagate without an outcome event; only returned driver results
produce notifications. Nil `Events` also skips event-related clock reads.

Cancellation is not a rollback signal. A canceled synchronization can leave
partially applied settings or indexing work still running on the server. Do not
continue the rollout on that result: reconcile the backend, repeat `Sync` with
the intended definition, and require a successful `Check` before starting new
instances. The memory driver's cooperative checks do not imply interruptible
locks or a provider-wide atomicity guarantee.

For OpenSearch, mapping edits and reindexing are not atomic. A failed reindex
leaves the prior completed schema checkpoint, so a later `Sync` can retry;
mapping edits can already be visible. Serialize deployments and native index
administration. Local indexes created with the former flat document layout
need explicit recreation and repopulation for the current
[typed wrapper](drivers.md#behavior-and-limits-2). `Sync` does not automatically
convert old documents, rebuild an index, or drop it.

## Check

An application instance may need to verify that the deployment updated its
indexes without trying to change them itself. `engine.Check(ctx)` compares the
backend with the code and reports a mismatch without making changes:

```go
err := engine.Check(ctx)
switch {
case errors.Is(err, search.ErrNotFound):
	// an index was never synced
case errors.Is(err, search.ErrSchemaChanged):
	// an index exists with another definition
}
```

It returns `search.ErrSchemaChanged` when an index exists but its stored
definition differs from the code, which is what you see when new code reaches
a backend that `Sync` has not updated yet, and `search.ErrNotFound` when an
index was never created. After a successful `Sync`, `Check` passes.

OpenSearch compares the last completed schema checkpoint, not every live native
mapping setting or document representation. Treat `Check` as a
deployment check within that scope, not proof that external administration has
left the index unchanged.

## One instance, or development

When one process owns the backend, such as local development, tests, or a
small single-instance service, run `Sync` as a start hook. Start the driver
first, since `Sync` needs its connection:

```go
compose := func(composer *application.Composer) {
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	composer.Startup(engine.Sync)
	composer.Check("search", driver.Ping)
}
```

Start hooks run in registration order, so the driver is connected before
`Sync` runs. Pass `compose` to `app.Run(ctx, compose)`; the registered cleanup
closes the driver on any exit, including composition or startup failure.
The memory driver has no `Start`, `Stop`, or `Ping`; register only
`composer.Startup(engine.Sync)` for it during composition.

For an owned OpenSearch driver, `New` only publishes configuration; `Start`
constructs the SDK client and performs native URL/PEM validation and connectivity
checks. Keep `Stop` registered even if `Start` fails or its connectivity check is
canceled, because an acquired client remains owned until cleanup. An already
canceled context prevents acquisition. See the driver's
[lifecycle and native-access migration](drivers.md#using-the-native-opensearch-client)
before moving native calls around startup.

## Several instances, or production

With several instances, you do not want each one to reindex at startup, and
you do not want a rolling deploy to start the new code before the backend
fits it. Run `Sync` once, from a release command that runs before the new
instances start, and make instances verify rather than change. Build the release
command's engine with that release's index definitions. Unlike the focused call
above, this deployment boundary must stop on failure:

```go
// In your release command, after starting the driver.
if err := engine.Sync(ctx); err != nil {
	return err
}
```

```go
// the service: refuses to start against an out-of-date backend
compose := func(composer *application.Composer) {
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	composer.Startup(engine.Check)
	composer.Check("search", driver.Ping)
}
```

If a driver ships tables (as the Postgres driver does), run its migrations before
`Sync` in the release command; the instances never run either. A new instance
whose code expects a changed definition then fails at startup with
`search.ErrSchemaChanged`, which stops a bad rollout while the old instances
keep serving, instead of failing on the first search.

> [!WARNING]
> Never register `engine.Check` with `composer.Check`, the health check. A
> deploy runs `Sync` while old instances are still running, and from that moment
> their definitions differ from the backend's, so `Check` fails on them. As
> health checks they would turn unhealthy and be restarted for a change that
> they are perfectly able to serve. Run `Check` once, at startup, with
> `composer.Startup` during composition. Use the driver's `Ping` for the health check.

## Changing a definition safely

Because old and new instances run side by side during a rolling deploy,
change a definition in steps that both versions can live with:

1. To add a field, deploy the code that writes it, run `Sync` with the new
   definition, then deploy the code that queries it. `Sync` can reindex values
   already present in stored JSON, but cannot supply missing source values.
   Backfill older documents from your database with `Put` after the `Sync` if
   they lack the new field.
2. To stop using a field, deploy code that no longer queries it, then remove
   it from the definition and run `Sync`.

To rebuild an index from your database during planned downtime, `Drop` it, `Sync` to create it empty,
and `Put` the records in batches. `Drop` deletes all indexed documents, so
search is unavailable or incomplete until the rebuild finishes. Run this in
a release command or job during a planned maintenance window, not in a request.

For every function and option, see [the `search` API
reference](https://pkg.go.dev/arkoren.dev/core/service/search).
