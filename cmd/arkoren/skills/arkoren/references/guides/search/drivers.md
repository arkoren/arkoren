---
title: Search Drivers
nav: Drivers
description: The memory and Postgres search drivers, how to wire and test with them, the OpenSearch driver, and the Meilisearch driver.
order: 40
---

# Search Drivers

## Introduction

Start with [Memory](#memory) for tests and local development: each process owns
a rebuildable index. For persistent storage, [Postgres](#postgres) reuses your
database and transactions but scans JSON for filters and sorting. If those
scans are too costly, consider an [OpenSearch cluster](#opensearch) or
[Meilisearch server](#meilisearch). Benchmark your data and query load, not just
document count; matching and operational limits differ despite the shared facade.

Follow [Search Overview](overview.md) for engine use and [Deploying Search](deploying.md)
for `Sync` and `Check`. [Choosing a Driver](../drivers/choosing-a-driver.md)
compares every service's drivers.

Run installation commands in your application's module. Examples use `search`
from `arkoren.dev/core/service/search`, the overview's `Products`, your context
`ctx`, and an existing [application](../application/overview.md).
Focused snippets omit routine checks: handle constructor errors before wiring
hooks, and stop deployments on migration or synchronization failure.

## Compatibility matrix

Compare the search operations Arkoren exposes, not each search service's full
feature catalog. Shared state means application processes use the same backend
and index namespace; separate memory drivers never share documents.

| Driver | Shared state | Text queries | Filters and sorting | `Sync` and `Check` | Searchable after `Put` | Database transactions | Administrative events through engine |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No | Yes | Yes | Yes | Yes | No | Yes |
| [Postgres](#postgres) | Yes | Yes | Yes | Yes | Partial — An enclosing database transaction must commit before other transactions see the write. | Yes | Yes |
| [OpenSearch](#opensearch) | Yes | Yes | Partial — Typed filters are supported; the ID tie-break requires native ID fielddata and long string keywords can fail writes. | Partial — Keyword-to-text changes require rebuilding; Check compares a completed schema checkpoint. | Yes | No | Yes |
| [Meilisearch](#meilisearch) | Yes | Partial — At most ten positive words and phrases; multiword nonphrase exclusions are unsupported. | Partial — Native coercion, diacritic folding, and string/ID length limits differ from typed comparisons. | Yes | Yes | No | Yes |

All engines support opt-in [administrative observations](deploying.md#sync) of
returned `Sync` and `Drop` outcomes, never query/document operations or `Check`.
Direct driver calls emit nothing. These events establish neither atomicity nor
index consistency and are not durable audit records; see the linked setup for
payload exclusions, no-op success, and partial-failure semantics.

Provider sections explain [Postgres migrations and transaction visibility](#postgres),
[OpenSearch mapping and matching limits](#behavior-and-limits-2), and
[Meilisearch matching and task-completion limits](#behavior-and-limits-3).
Searchability after a successful `Put` implies neither atomic multi-document
writes nor durability; cancellation can leave a Meilisearch write running.
The matrix describes supported operations and known implementation limits, not
proof that every native backend passes conformance. OpenSearch uses separate
scalar containers to enforce typed comparisons; its remaining filter/sort limits
are native keyword length and ID fielddata requirements. Meilisearch's native
filter coercion still differs from the typed contract.

## Memory

The memory driver is included in `arkoren.dev/core`; no installation is needed.
It keeps documents and decoded fields in process memory, suitable for an index
you can rebuild at startup.

### Connecting

`New` takes no options, never fails, and starts nothing. It has no `Start`,
`Stop`, or `Ping`, because there is no connection:

```go
import "arkoren.dev/core/service/search/memorysearch"

driver := memorysearch.New()
engine := search.New(driver, func(engine *search.Builder) {
	engine.Indexes(Products)
})
```

Call `engine.Sync(ctx)` before indexing or searching. For application startup,
register only `engine.Sync` as a startup hook, as shown in
[Deploying Search](deploying.md#one-instance-or-development).

### Configuration

There are no settings or native clients; register indexes on the engine.

### Indexing and searching

Follow the shared [put](overview.md#putting-documents-in) and
[search](overview.md#searching) examples after synchronization. Changes are
visible immediately within this driver; a second driver has independent data.

### Behavior and limits

Indexes disappear on exit and are independent per replica. Repopulate at startup
if needed. Searches scan every document; benchmark your production data and load.

Text is lowercased and split on every character that is not a letter or
digit. Because all positive terms are required, every hit has the same score:
1 when the query has a positive term, and 0 for empty or exclusion-only text.
After any explicit sort keys, ties fall back to the document ID.
`Sync` records a definition and keeps the
documents of an existing index. `Check` reports a missing or changed definition.
Field settings apply immediately because searches inspect the stored documents.

`Sync` copies schema field lists, and stored and returned document bytes have
independent containers. The driver checks cancellation after acquiring its lock,
between schemas and document iterations, while scanning/copying results, and after
sorting. It does not interrupt mutex waits, sorting, or an individual JSON decode.
A canceled write or sync may have applied partial changes; these checks are
memory-specific behavior, not stronger portable cancellation or atomicity promises.

For every function and option, see [the `memorysearch` API
reference](https://pkg.go.dev/arkoren.dev/core/service/search/memorysearch).

## Postgres

The Postgres driver adds search to your PostgreSQL database using two tables,
`tsvector`, and a GIN index, without another service.

Use an existing database. Grant the migration identity table/index creation
permissions and runtime identities read/write access to those tables. Keep DDL
in the release process, not unrestricted application-instance privileges.

### Installation

```sh
go get arkoren.dev/driver/postgres
```

### Connecting

`NewFrom` wraps the `*database.Database` you already use, so it shares the
connection pool and joins transactions that carry its context. Construction does
no I/O. This driver never creates its tables itself: add its migrations to your
migrator and run them before the search driver's `Start`. For local development,
register these hooks after the database driver's start and stop hooks:

```go
import "arkoren.dev/driver/postgres/postgressearch"

driver, err := postgressearch.NewFrom(db)

if err := migrator.Add(driver.Migrations()...); err != nil {
	return err
}

engine := search.New(driver, func(engine *search.Builder) {
	engine.Indexes(Products)
})
compose := func(composer *application.Composer) {
	composer.Startup(migrator.Up)
	composer.Startup(driver.Start)
	composer.Startup(engine.Sync)
	composer.Check("search", driver.Ping)
}
```

`Start` and `Ping` check connectivity and both tables; a missing table returns
`search.ErrNotFound` naming `Migrations()`. `Stop` does nothing: you stop the
database driver. In production, run migrations then `Sync` in a
[release command](deploying.md#several-instances-or-production), not each instance.

### Configuration

`Builder.Instance(name)` is the only setting. It gives independent backends
their own tables and migration groups; use distinct names within one database:

```go
catalog, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
	engine.Instance("catalog")
})
```

Settings are fixed at construction; the database remains application-owned.

The default instance uses `search_documents` and `search_indexes`; the instance
`catalog` uses `catalog_search_documents` and `catalog_search_indexes`. Names
are 1–40 bytes of `a-z`, `0-9`, and `_`; empty selects the default instance.
Digit-leading names are accepted: `Instance("0")` uses `0_search_documents` and
`0_search_indexes`. Both migration directions and data statements quote generated
identifiers, and readiness checks resolve quoted names. Digit-leading instances
support Up, Rollback, and a subsequent Up without changing physical names. Keep
the same instance name on an existing deployment: quoting does not rename tables
or indexes, change migration groups, or change advisory-lock identities. Table
names are not configurable and renaming is not supported.

### Indexing and searching

After migrations, startup, and synchronization succeed, use the shared
[put](overview.md#putting-documents-in) and [search](overview.md#searching)
tasks. Writes participate in a database transaction carried by `ctx`; inside
one, other transactions see them only after commit.

### Behavior and limits

`Sync` changes data, not schema. It records definitions and recomputes every
document's search vector when searchable fields change; this can be slow.
Concurrent calls queue behind an advisory lock keyed by the unquoted documents
table name, unchanged for digit-leading instances. Without a surrounding
transaction, `Put` commits before returning.

Text uses PostgreSQL's `simple` configuration: no stemming, stop words, or
language. Punctuation splits `red-shoes` into `red` and `shoes`; relevance is
`ts_rank`. Filters and sorting scan JSON for text-matched documents; prefer a
dedicated search service for very large indexes.

Documents are stored as PostgreSQL `jsonb`, which normalizes whitespace and
object-key order rather than preserving the original JSON formatting. JSON
containing the `\u0000` escape cannot be stored. These are
[PostgreSQL JSONB restrictions](https://www.postgresql.org/docs/current/datatype-json.html),
not portable guarantees of the search contract.

### Applying shipped migrations

The driver ships table migrations and exposes its borrowed facade through
`driver.Database()`. With an existing PostgreSQL facade `db`, create the
migrator from `arkoren.dev/core/service/database/migrate` before the connecting
example above:

```go
import "arkoren.dev/core/service/database/migrate"

migrator := migrate.New(db)
```

The migration group remains `arkoren.dev:driver/postgres:postgressearch`, with
`:<instance>` appended for a named instance. Its migration IDs remain
`documents-create-table` and `indexes-create-table`; identifier quoting does not
require a new group or a destructive remigration.

Adding `driver.Migrations()` registers declarations; only `migrator.Up(ctx)`
creates tables. Run it before `driver.Start` and `engine.Sync`. Rolling these
migrations back deletes all indexed documents; rebuild from your source data
after recreating the tables. The database connection and pool remain yours.

### Running locally

Install Docker with Compose. Save this `compose.yaml` in a new empty development directory;
the example password and disabled TLS are for a loopback-only test database:

```yaml
services:
  database:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: local
      POSTGRES_PASSWORD: local-password
      POSTGRES_DB: catalog
    ports:
      - "127.0.0.1:15432:5432"
```

Start it and check readiness (retry the check while it starts):

```sh
docker compose up -d
docker compose exec database pg_isready -U local -d catalog
```

The check reports `accepting connections`. Build your application database
using `database` from `arkoren.dev/core/service/database` and `postgresdatabase`
from `arkoren.dev/driver/postgres/postgresdatabase`:

```go
connection, err := postgresdatabase.New("postgres://local:local-password@127.0.0.1:15432/catalog?sslmode=disable")
db := database.New(connection)
composeDatabase := func(composer *application.Composer) {
	composer.Cleanup(connection.Stop)
	composer.Startup(connection.Start)
}
```

Create `migrator := migrate.New(db)` and follow Connecting to register its
migrations and search hooks. After startup, put and search a product to verify
the index. Run `docker compose down` when finished; the container configuration
does not preserve data after removal. For database pool settings, see the
[database drivers guide](../database/drivers.md).

For every function and option, see [the `postgressearch` API
reference](https://pkg.go.dev/arkoren.dev/driver/postgres/postgressearch).

## OpenSearch

The official client maps indexes and queries to an [OpenSearch](https://opensearch.org)
cluster. Choose it for large indexes or an existing cluster; OpenSearch 2.19 and
3.x are supported.

### Installation

```sh
go get arkoren.dev/driver/opensearch
```

### Connecting

You need a reachable cluster and credentials that permit the operations you
use. Synchronization needs index creation, mapping updates, and reindexing;
runtime use needs search and document read/write/delete access. Restrict access
to your configured index prefix using the cluster's security configuration.

`New` takes the cluster address and publishes configuration without constructing
the native SDK client or starting network or background work. `Start` creates
the owned client and checks that a node answers; `Stop` closes it and cancels
native background work. An empty address or invalid index prefix still fails
at `New`. Native configuration validation, including malformed URLs and CA PEM,
now happens at `Start`; those errors retain their SDK identities for
`errors.Is` and `errors.As`. Register cleanup before startup, even when startup
can fail:

```go
import "arkoren.dev/driver/opensearch/opensearchsearch"

driver, err := opensearchsearch.New("http://localhost:9200")

engine := search.New(driver, func(engine *search.Builder) {
	engine.Indexes(Products)
})
compose := func(composer *application.Composer) {
	composer.Cleanup(driver.Stop)
	composer.Startup(driver.Start)
	composer.Startup(engine.Sync)
	composer.Check("search", driver.Ping)
}
```

An already canceled startup context prevents owned acquisition. If `Start`
creates a client but its connectivity check fails or is canceled, the client
and native background work remain owned until `Stop`; do not skip cleanup on
that error. Repeated `Start` rechecks the same client. `Stop` before `Start` and
repeated `Stop` do nothing; a later `Start` can create a fresh client.

Owned lifecycle calls serialize. `Stop` waits for concurrent lifecycle work and
SDK cleanup even if its context has expired; it does not drain your requests.
The startup context controls connectivity requests, not the native lifetime.
Neither startup cancellation nor a stop deadline forcibly interrupts SDK
construction, lifecycle serialization, or uncooperative native callbacks.

`Ping` verifies a node answers, not cluster health or index existence. Before
owned startup and after `Stop`, request-bearing operations fail as not started
without creating a client. Empty `Sync` and `Check` make no requests; they still
reject a canceled context. Use the
[production deployment pattern](deploying.md#several-instances-or-production):
`Sync` in the release command, `Check` at application startup.

### Configuration

`Builder.IndexPrefix("shop_")` maps `products` to `shop_products` for sharing a
cluster across environments. A nonempty prefix is 1–64 bytes of `a-z`, `0-9`,
and `_`; empty keeps bare index names.
Use `Username` and `Password` for credentials, `Nodes` for additional nodes,
and `Client` for native CA, API-key, or signing configuration:

```go
driver, err := opensearchsearch.New("https://search.internal:9200",
	func(engine *opensearchsearch.Builder) {
		engine.IndexPrefix("shop_")
		engine.Username("search")
		engine.Password(os.Getenv("OPENSEARCH_PASSWORD"))
	},
)
```

Settings are fixed at construction. `New` invokes the native `Client` hook
directly once after basic address/prefix validation and base settings, before
publishing configuration. `Start` never replays it. Keep that hook free of
lifecycle I/O. Owned node lists and completed native configuration containers
and scalar pointers are copied; retained drafts cannot change them. Native
objects, functions, and their captures remain shared, so configure them before
concurrent use. Native background work follows the configured native context
(or a background context), with additional cancellation owned by `Stop`, not
the startup context.

To share a client you already built, pass it to `NewFrom(client, configure...)`;
this borrowed-client path is unchanged. `Client()` returns that supplied client
immediately, `Start` only checks connectivity, and `Stop` leaves it open. Only
`IndexPrefix` applies; `NewFrom` never invokes the `Client` configuration hook.

Use contexts with deadlines for cluster operations. Transport timeouts and
retries come from the native client; adjust them through `Builder.Client` or
configure your client before `NewFrom`.

### Indexing and searching

Use the shared [put](overview.md#putting-documents-in) and
[search](overview.md#searching) tasks after startup and synchronization.
Successful writes wait until their documents are searchable.

### Behavior and limits

`Sync` creates each index with one shard and an explicit mapping, and stores
the logical definition in the mapping so `Check` can compare it. Native
`_source` wraps the original JSON as an unindexed `source` string beside
`strings`, `numbers`, and `booleans` objects. Each container holds only that
scalar kind. Only declared fields are indexed; scalar values with nonempty,
dot-free keys are captured before declaration so later synchronization can
index newly declared fields. Empty/dotted keys cannot be declared fields and
stay only in the original JSON, avoiding native object-path collisions.

Comparisons preserve scalar kinds: the string `"15"` does not match a number,
and `true` does not match the string `"true"`. Missing, null, array, and object
values match no scalar condition, including negations. `IN` accepts any typed
equality; `NOT IN` requires every typed inequality, so a mixed-kind `NOT IN`
list matches no scalar regardless of operand order.

Searchable strings are text, split on every character that is not a letter or
digit without stemming. String filters and sorts use lowercase keywords;
numbers use double fields and booleans use boolean fields. Filterable or sortable
strings over 32,766 bytes fail writes. Numeric comparisons use the core's
float64 normalization and its integer-precision limits.

`Driver.Search` restores the original JSON bytes and external document ID;
application fields do not need reserved prefixes or names. Native SDK document
calls see the wrapper instead. If a local index was created with the former
flat representation, explicitly recreate and repopulate it. The driver never
automatically converts, rebuilds, or drops an existing index.

Relevance is OpenSearch's BM25, so scores change as documents
are added, and a text with no positive word, such as only an exclusion, scores
0.

Changed definitions update mappings and reindex existing documents, including
new filterable/sortable fields. Mapping changes and reindexing are not atomic;
a failure can leave mapping edits visible. The stored schema checkpoint is
published only after reindexing succeeds, so `Check` compares the last completed
checkpoint, not every native setting. Serialize deployments and external
administration. Large indexes take time. A keyword field cannot
become searchable: rebuild from source during planned maintenance. Dropping the
index deletes all documents; search stays unavailable or incomplete until
repopulation finishes.

Writes wait for OpenSearch's refresh, so `Put` and `Delete` return when the
change is searchable, which takes up to a second by default. Nonphrase words,
including those split from `red-shoes`, can match different searchable fields;
phrases still require consecutive words in one field. Native timeouts and failed
shards return errors rather than successful partial result pages.

The built-in document-ID tie-break uses native `_id` fielddata. If the cluster
disables `indices.id_field_data.enabled`, those searches fail. Fielddata loads
IDs into heap; assess the cluster's policy and capacity rather than assuming
that enabling it is safe for your production workload.

### Using the native OpenSearch client

For cluster operations outside the portable contract, use `driver.Client()`.

> [!WARNING]
> Compatibility change for drivers built with `New`: `Client()` is now nil
> before native creation in `Start` and after `Stop`; dereferencing it then
> panics. Move native access after a successful `Start`, and handle native
> URL/PEM validation errors there instead of at `New`. A failed connectivity
> check can still leave a non-nil client that
> you must release with `Stop`. `NewFrom` retains immediate native access and
> caller ownership.

With `Stop` already registered as in Connecting and `net/http` imported, start
the driver before reading the cluster description:

```go
if err := driver.Start(ctx); err != nil {
	return err // The lifecycle owner must still call Stop.
}
client := driver.Client()
request, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
reply, err := client.Client.Request(request)
if err != nil {
	return err
}
defer reply.Body.Close()
```

Check `reply.StatusCode` and body: transport success alone does not mean success.
Native calls bypass validation, telemetry, and prefix mapping; do not alter
`Sync`-managed mappings. Configure TLS, retries, and authentication through
`Builder.Client`, or own a preconfigured client supplied to `NewFrom`.
The accessor is synchronized, but retaining its result does not prevent `Stop`
from closing an owned client; coordinate native requests with shutdown.
Native document responses expose the `source`/`strings`/`numbers`/`booleans`
wrapper, unlike `Driver.Search`. Write documents through `Driver.Put` so the
original JSON and typed index fields stay consistent.

### Running locally

Install Docker with Compose and allow enough memory for OpenSearch. Save this
`compose.yaml` in a new empty development directory. Security is disabled, so bind
only to loopback and never use this configuration for real user data:

```yaml
services:
  search:
    image: opensearchproject/opensearch:3.9.0
    environment:
      discovery.type: single-node
      DISABLE_SECURITY_PLUGIN: "true"
      DISABLE_INSTALL_DEMO_CONFIG: "true"
      OPENSEARCH_JAVA_OPTS: -Xms512m -Xmx512m
    ports:
      - "127.0.0.1:19200:9200"
```

```sh
docker compose up -d
curl --fail http://127.0.0.1:19200/
```

Retry the check while the node starts; success returns a JSON cluster description.
If the node exits, inspect `docker compose logs search` and check memory and
Docker-host requirements in the [OpenSearch Docker instructions](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/docker/).
Use `opensearchsearch.New("http://127.0.0.1:19200")` with the lifecycle hooks
above. After `Start` succeeds, synchronize, put, and search using the shared
tasks. Run `docker compose down` when finished; this disposable configuration
does not preserve indexes after container removal.

For every function and option, see [the `opensearchsearch` API
reference](https://pkg.go.dev/arkoren.dev/driver/opensearch/opensearchsearch).

## Meilisearch

Choose [Meilisearch](https://www.meilisearch.com) for dedicated single-server
search with prefix matching as users type. Version 1.15+ is required for string
filter comparisons, which Arkoren uses for timestamps.

### Installation

```sh
go get arkoren.dev/driver/meilisearch
```

### Connecting

`New` validates options and an absolute `http` or `https` server URL without I/O.
Invalid URLs or index prefixes return errors. With standard-library `os` and
your configured server address, create the driver and engine:

```go
import "arkoren.dev/driver/meilisearch/meilisearchsearch"

driver, err := meilisearchsearch.New("http://search:7700",
	func(engine *meilisearchsearch.Builder) {
		engine.APIKey(os.Getenv("MEILI_MASTER_KEY"))
	},
)
engine := search.New(driver, func(engine *search.Builder) {
	engine.Indexes(Products)
})
```

Check the construction error before creating the engine. Register `Stop` as
cleanup, `Start` as startup, and `Ping` as the health check using the
[application lifecycle](../application/lifecycle-and-ownership.md).
Run `Sync` only after `Start` succeeds. `Start` checks the server version;
a wrong address, key, or old version fails
startup. `Stop` closes idle connections. For production, use the
[release-command deployment pattern](deploying.md#several-instances-or-production).

### Configuration

`Builder.APIKey` supplies each request's bearer token. The key must permit
managing indexes, documents, settings, and tasks; the master key does. Omit it
only if the server has no master key. Load it from configuration, not source;
the driver's configuration prints it as `[REDACTED]` through `fmt` and `slog`.

`Builder.IndexPrefix` is added to every index name to form the Meilisearch index
name, so several applications or environments can share one server:

```go
driver, err := meilisearchsearch.New(url, func(engine *meilisearchsearch.Builder) {
	engine.IndexPrefix("shop_staging_")
})
```

A prefix holds at most 100 letters, digits, underscores, and hyphens. Settings
are fixed at construction. For custom transport settings, use
[`NewFrom` below](#using-a-custom-http-client); both APIKey and IndexPrefix apply.

The default HTTP client has no overall request timeout. Bound operations with
context deadlines or supply a client timeout. The driver does not retry failed
HTTP statuses; polling a pending indexing task is completion tracking, not a retry.

### Indexing and searching

Use the shared [put](overview.md#putting-documents-in) and
[search](overview.md#searching) tasks after startup and synchronization.
Both writes and synchronization wait for Meilisearch's task completion.

### Behavior and limits

Each index uses primary key `arkoren_id`. `Sync` sets field roles, ranking rules,
pagination limits, and disables typo tolerance for whole-word matching. It sends
only changed settings and waits for reindexing tasks. `Check` compares the same
settings and the primary key; change managed settings only through the definition.
`Sync` refuses an existing index with a different primary key. Serialize schema
deployments and native administration: metadata reads and asynchronous tasks are
not an atomic observation.

Writes are asynchronous in Meilisearch. The driver waits for each task, so a
document is searchable when `Put` returns, and a task that fails becomes the
returned error. If your context ends while waiting, the call returns early
and the server may still apply the change, so repeat a `Put` or `Delete` you
canceled; both are safe to repeat.

The driver adds `arkoren_id` and `arkoren_order`, removing them from returned
hits. `Put` rejects document fields starting with `arkoren_` and the native
`_rankingScore` field with `search.ErrUnsupported`, checking every document before
the first request. `Put` and `Delete` verify index existence first, returning
`search.ErrNotFound` rather than implicitly creating an unsynced index.

Meilisearch differs from the contract in a few documented places:

- It folds diacritics along with case, so `cafe` matches `café` in text and in
  string filters.
- Queries permit at most ten positive words and phrases; exclusions do not count.
  Longer queries fail with `search.ErrUnsupported` rather than letting
  Meilisearch ignore excess terms and match too much.
- An excluded word that splits into several words, such as `-t-shirt`,
  fails with `search.ErrUnsupported` before a request. A phrase exclusion such as
  `-"t shirt"` is supported but means something different; do not substitute it
  unless excluding that phrase is your intent.
- Native filters can coerce a stored value whose scalar kind differs from the
  operand, rather than enforcing the contract's typed comparisons. Keep operand
  types aligned with stored fields, including membership lists; do not rely on
  coercion for portable results.
- String values of more than 500 bytes are not indexed for filtering and
  sorting: they match no filter and sort last. A document ID over 250 bytes
  loses its place in the ID tie-break for the same reason.

Documents with equal sort keys come in order of relevance, then ID in byte
order, as the contract says. The score is the Meilisearch ranking score
between 0 and 1, or 0 for empty and exclusion-only text.

### Using a custom HTTP client

For proxy, TLS, or timeout requirements, supply your `*http.Client` with
`NewFrom`. For example, with standard library `net/http` and `time` imports:

```go
client := &http.Client{Timeout: time.Minute}
driver, err := meilisearchsearch.NewFrom(client, "https://search.internal:7700",
	func(engine *meilisearchsearch.Builder) {
		engine.APIKey(os.Getenv("MEILI_MASTER_KEY"))
	})
```

You retain the client; `Stop` closes its idle connections only. `driver.Client()`
returns that same client. It does not automatically add a server address or
authorization to requests you make yourself. Arkoren ships no native Meilisearch
SDK or companion administration adapter; keep driver-managed settings under `Sync`.

### Running locally

Install Docker with Compose. Save this `compose.yaml` in a new empty development directory.
The public example master key grants administration access; keep this service
on loopback and never use this key or configuration in production:

```yaml
services:
  search:
    image: getmeili/meilisearch:v1.55.0
    environment:
      MEILI_MASTER_KEY: local-search-master-key-0123456789
      MEILI_NO_ANALYTICS: "true"
    ports:
      - "127.0.0.1:17700:7700"
```

```sh
docker compose up -d
curl --fail http://127.0.0.1:17700/health
```

Retry the check while the server starts; success reports `"status":"available"`.
Point `New` at `http://127.0.0.1:17700` and set `Builder.APIKey` to the key above
(or read it from your local configuration). Synchronize, put, and search a
product using the shared tasks. Run `docker compose down` when finished;
this disposable configuration does not preserve indexes after removal.

For every function and option, see [the `meilisearchsearch` API
reference](https://pkg.go.dev/arkoren.dev/driver/meilisearch/meilisearchsearch).

## Testing a driver

If you write a driver for your application, `searchtest.Run` checks portable
matching, filters, ordering, pagination, documents, and index lifecycle through
the engine. It does not require a particular relevance ranking:

```go
import "arkoren.dev/core/service/search/searchtest"

func TestConformance(t *testing.T) {
	searchtest.Run(t, func(*testing.T) search.Driver {
		return memorysearch.New()
	})
}
```

A driver that needs a running server calls `Run` only when `testing.Short()` is
false, and each subtest creates indexes with unique names and drops them, so one
server can serve the whole suite.

For every function and option, see the [search API reference](https://pkg.go.dev/arkoren.dev/core/service/search).
