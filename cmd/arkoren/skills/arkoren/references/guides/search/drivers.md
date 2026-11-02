---
title: Search Drivers
nav: Drivers
description: Choose a search backend, configure its engine, and account for native ownership, deployment requirements, and matching limits.
order: 20
summary:
  - Compare shared state, matching support, schema operations, and transaction visibility across four backends.
  - Configure each driver with explicit credentials, lifecycle ownership, and provider limits.
  - Apply application-owned Postgres migrations without changing deployed instance identities.
  - Use native clients deliberately and run disposable local backend services.
---

# Search Drivers

Use Memory for isolated tests or a small rebuildable local index. Postgres
reuses an existing database and its transactions. Meilisearch provides dedicated
single-server search; OpenSearch provides a clustered backend. Benchmark your
documents and query load: sharing a facade does not make capacity, ranking, or
operational costs interchangeable.

## Compatibility Matrix

This compares Arkoren's exposed search behavior, not the vendors' full products.
Shared state means processes use the same backend namespace. Searchability after
`Put` does not guarantee durable storage or atomic batches.

| Driver | Shared State | Text Matching | Typed Filters | Explicit Sorting | Schema Sync/Check | Searchable After Put | Database Transactions |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | No | Yes | Yes | Yes | Yes | Yes | No |
| [Postgres](#postgres) | Yes | Yes | Yes | Yes | Yes | Partial — Other transactions must wait for an enclosing transaction to commit. | Yes |
| [Meilisearch](#meilisearch) | Yes | Partial — Ten positive words and phrases; multiword nonphrase exclusions are rejected; diacritics fold. | Partial — Native coercion and string length limits differ. | Partial — Long strings and IDs lose their expected ordering. | Yes | Yes | No |
| [OpenSearch](#opensearch) | Yes | Yes | Partial — Long filterable strings fail writes. | Partial — Requires native ID fielddata; long keywords fail writes. | Partial — Some mapping changes require a rebuild; Check compares a completed checkpoint. | Yes | No |

Memory loses all state on process exit. Persistent backends still need
application-owned storage, backup, replication, and recovery policies; a successful
search call is not proof of those policies. Every engine can emit optional
[administrative events](monitoring.md#deployment-events), independently of provider.

Examples import `search` from `arkoren.dev/core/service/search` and use the
[`Products` declaration](indexing-documents.md#document-shape). Run `go get`
commands in your application's module. Check constructor errors before using a
driver. Native providers require the [application lifecycle](../application/lifecycle-and-ownership.md)
and [deployment ordering](deploying.md); engine construction alone starts nothing.

## Memory

Memory is included in `arkoren.dev/core` and needs no service or credentials.
Each constructed driver owns independent process-local state.

### Engine Setup

Create the driver and register your application's indexes:

```go
import "arkoren.dev/core/service/search/memorysearch"

driver := memorysearch.New()
engine := search.New(driver, func(settings *search.Builder) {
	settings.Indexes(Products)
})
```

`memorysearch.New` has no options and cannot fail. There is no native client,
`Start`, `Stop`, or `Ping`. Run `engine.Sync(ctx)` before
[putting documents](indexing-documents.md#putting-documents) or
[searching](overview.md#finding-products). For application startup, register only
`composer.Startup(engine.Sync)`.

### Matching Limits

Search scans every document under a read lock. Text is lowercased and split on
nonletters and nondigits, without stemming or typo tolerance. All matching
documents score 1 when positive text exists, otherwise 0; explicit orders and
the ID tie-break determine their order.

`Sync` keeps documents and records the new field policy immediately; searches
read fields from stored documents. Stored and returned byte containers are
independent. Cancellation is checked between schemas, documents, and result
copies, after lock acquisition, and after sorting. Mutex waits, sorting, and
individual JSON decodes are not interrupted; canceled mutations can be partial.

For every function and option, see the [memorysearch API reference](https://pkg.go.dev/arkoren.dev/core/service/search/memorysearch).

## Postgres

Choose Postgres when you already operate PostgreSQL and want search writes to
join your database transactions. Text uses `tsvector` and a GIN index; filters
and sorting read JSON from text-matched rows rather than dedicated field indexes.
Measure those scans before choosing this backend for a large catalog.

### Engine Setup

Install the driver, then use an existing PostgreSQL-backed `*database.Database`
named `db` from your [database setup](../database/drivers.md#postgresql):

```sh
go get arkoren.dev/driver/postgres
```

```go
import "arkoren.dev/driver/postgres/postgressearch"

driver, err := postgressearch.NewFrom(db)
```

`NewFrom` performs no I/O. A nil database, another SQL dialect, or an invalid
instance name returns `postgressearch.ErrInvalidConfig`. The driver borrows
`db`; `Stop` does nothing, and your application closes the database owner.
`driver.Database()` exposes that same borrowed facade.

The driver never runs DDL. Register its table declarations with your existing
application migrator before deploying:

```go
if err := migrator.Add(driver.Migrations()...); err != nil {
	return err
}
```

Create that migrator with `migrate.New(db)` from
`arkoren.dev/core/service/database/migrate` if needed. Registration creates no
tables. Run `migrator.Up(ctx)` in the release process, then start the search
driver and synchronize the logical indexes. Grant DDL privileges to the migration
identity and table read/write privileges to runtime identities.

Once construction succeeds, register the typed definition:

```go
engine := search.New(driver, func(settings *search.Builder) {
	settings.Indexes(Products)
})
```

`Start` and `Ping` verify connectivity and both tables. Missing tables return
`search.ErrNotFound` with migration guidance. Start the database owner first;
then use `driver.Start` and the release/startup policy in
[Deploying Search](deploying.md#release-deployment). Follow the shared
[indexing](indexing-documents.md#putting-documents) and
[query](queries.md#catalog-filters) tasks after synchronization.

### Instance Identity

For independent search stores in one database, choose a stable instance at
construction:

```go
driver, err := postgressearch.NewFrom(db, func(settings *postgressearch.Builder) {
	settings.Instance("catalog")
})
```

The default uses `search_documents` and `search_indexes`; `catalog` uses
`catalog_search_documents` and `catalog_search_indexes`. An instance is at most
40 bytes of lowercase letters, digits, and underscores; empty selects the
default. Digit-leading names are supported because generated identifiers are
quoted. There is no table-name setting or supported instance rename.

Keep deployed identities unchanged. The migration group is
`arkoren.dev:driver/postgres:postgressearch`, with `:<instance>` for a named
instance. Its IDs are `documents-create-table` and `indexes-create-table`.
Identifier quoting changes none of these identities or their advisory-lock keys.
Rolling these migrations back deletes indexed documents; recreate and repopulate
from source rather than treating rollback as a harmless configuration reset.

### Matching Limits

Text uses PostgreSQL's `simple` configuration without stemming or stop words;
punctuation splits words and relevance uses `ts_rank`. Filters preserve scalar
kinds and compare strings case-insensitively in byte order. JSONB storage
normalizes formatting and object-key order and rejects `\u0000`; see
[PostgreSQL JSON restrictions](https://www.postgresql.org/docs/current/datatype-json.html).

Without an enclosing transaction, writes commit before returning. With a
transaction in `ctx`, other transactions see changes after commit. Search reads
its window and capped count separately, so concurrent writes can make those
reads disagree; it does not create a shared snapshot for you.

`Sync` changes data, not SQL schema. It recomputes every stored search vector
when searchable fields change and uses a transaction-scoped advisory lock keyed
by the documents table, so concurrent synchronizations queue. Large batches of
`Put` use chunks of 1,000 documents; without your enclosing transaction they
are not one atomic write.

### Local Server

For a disposable development database, install Docker with Compose and save this
`compose.yaml` in an empty development directory. These credentials and disabled
TLS are for loopback-only use, not production:

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

Start the database, then retry the readiness command until it accepts connections:

```sh
docker compose up -d
docker compose exec database pg_isready -U local -d catalog
```

Use `postgres://local:local-password@127.0.0.1:15432/catalog?sslmode=disable`
with the [Postgres database constructor](../database/drivers.md#postgresql), then
apply the search migrations and startup ordering above. Put a product and run
the shared search example to verify the logical index, not only connectivity.
Run `docker compose down --volumes` afterward to remove this disposable stack
and its volumes. This cleanup discards its indexed data.

For every function and option, see the [postgressearch API reference](https://pkg.go.dev/arkoren.dev/driver/postgres/postgressearch).

## Meilisearch

Use Meilisearch for a dedicated search server with native prefix matching.
Version 1.15 or later is required for string comparisons. Provision a reachable
server and a key authorized for the index, document, settings, task, and version
operations your application uses. Load credentials from configuration; do not
publish a master key in source or expose an unprotected server.

### Engine Setup

Install the module and construct the HTTP-backed driver. Here `address` and
`apiKey` are your configured server URL and secret:

```sh
go get arkoren.dev/driver/meilisearch
```

```go
import "arkoren.dev/driver/meilisearch/meilisearchsearch"

driver, err := meilisearchsearch.New(address, func(settings *meilisearchsearch.Builder) {
	settings.APIKey(apiKey)
})
```

Construction validates an absolute HTTP(S) URL and options without I/O. URL path
prefixes are supported; queries and fragments are rejected. The default client
has no overall request timeout, so give operations context deadlines.

After checking construction, register your indexes:

```go
engine := search.New(driver, func(settings *search.Builder) {
	settings.Indexes(Products)
})
```

Register `driver.Stop` as cleanup before `driver.Start` as startup, and use
`driver.Ping` for readiness. `Start` checks connectivity, the key, and the minimum
version; `Ping` checks the version endpoint but does not enforce that minimum.
Use [release synchronization](deploying.md#release-deployment) before the shared
[document](indexing-documents.md#putting-documents) and [query](queries.md#catalog-filters)
operations. `Stop` closes idle connections without canceling or draining in-flight
requests.

### Client Settings

To isolate environments, set `IndexPrefix` on construction; it prefixes every
logical index name and defaults to empty. A prefix holds at most 100 ASCII
letters, digits, underscores, and hyphens. Settings are fixed after construction;
API keys are redacted by the driver's configuration formatting.

If you need a proxy, custom TLS, or an overall timeout, supply your own client.
With `net/http` and `time` imported:

```go
client := &http.Client{Timeout: time.Minute}
driver, err := meilisearchsearch.NewFrom(client, address,
	func(settings *meilisearchsearch.Builder) {
		settings.APIKey(apiKey)
		settings.IndexPrefix("shop_staging_")
	})
```

Both settings apply to `NewFrom`. The HTTP client remains yours;
`driver.Client()` returns it and `Stop` still closes its idle connections.
Native requests do not automatically gain the server URL or authorization.
There is no shipped native Meilisearch SDK adapter. Keep managed settings under
`Sync`, rather than changing them through custom requests.

### Matching Limits

`Sync` manages primary key `arkoren_id`, field roles, ranking rules, the
1,000-hit pagination limit, and disabled typo tolerance. It waits for changed
settings' tasks and refuses an existing index with another primary key. `Check`
compares those settings and the primary key. Serialize deployments and native
administration: metadata reads and asynchronous tasks are not atomic.

`Put` and `Delete` wait for task completion; successful returns are searchable.
If a context expires while polling, the server can still apply the change.
Reconcile or repeat the idempotent write. HTTP status failures are not retried;
polling a pending task is completion tracking, not request retry.

The driver reserves top-level names starting with `arkoren_` and
`_rankingScore`, rejecting collisions with `search.ErrUnsupported` before its
first write request. It adds internal ID/order fields and removes them from hits.
Missing indexes return `search.ErrNotFound` rather than being created by writes.

Meilisearch folds diacritics as well as case, so `cafe` matches `café`. It accepts
at most ten positive words and phrases; excess text returns
`search.ErrUnsupported`. Nonphrase exclusions splitting into multiple words,
such as `-t-shirt`, also return that error. A phrase exclusion is not an
equivalent replacement.

Native filters can coerce different scalar kinds. Keep operands and stored
fields consistently typed, especially membership lists. Strings over 500 bytes
are not indexed for filtering/sorting and sort last; IDs over 250 bytes lose
their expected position in the byte-order tie-break. Ranking scores range from
0 to 1, but remain comparable only within one result; empty or exclusion-only
text scores zero.

### Local Server

With Docker and Compose installed, save this `compose.yaml` in an empty
development directory. The example key grants administration access; never use
it with real data or expose this service beyond loopback:

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

Start it and retry the health request while the server initializes:

```sh
docker compose up -d
curl --fail http://127.0.0.1:17700/health
```

A successful response reports status `available`. Construct the driver with
`http://127.0.0.1:17700` and the example API key, then start, synchronize, put,
and search a product using the shared tasks. Run `docker compose down --volumes`
afterward to remove this disposable stack and its volumes, discarding indexed data.

For every function and option, see the [meilisearchsearch API reference](https://pkg.go.dev/arkoren.dev/driver/meilisearch/meilisearchsearch).

## OpenSearch

Choose OpenSearch for an existing cluster or a workload needing clustered search.
The client supports OpenSearch 2.19 and 3.x. Synchronization needs index creation,
mapping updates, and reindexing permissions; runtime operations need document
and search permissions. Use TLS and restrict credentials to the intended index
namespace.

### Engine Setup

Install the module, then construct an owned driver with a configured cluster
`address`:

```sh
go get arkoren.dev/driver/opensearch
```

```go
import "arkoren.dev/driver/opensearch/opensearchsearch"

driver, err := opensearchsearch.New(address)
```

`New` publishes configuration without constructing the SDK client or doing I/O.
An empty address or invalid prefix fails here; native URL and certificate
validation occurs in `Start` and retains SDK error identities.

After checking construction, register the definition and lifecycle hooks with
your `*application.Composer` named `composer`:

```go
engine := search.New(driver, func(settings *search.Builder) {
	settings.Indexes(Products)
})
composer.Cleanup(driver.Stop)
composer.Startup(driver.Start)
composer.Startup(engine.Check)
composer.Check("search", driver.Ping)
```

This is the production instance path: [synchronize in the release command](deploying.md#release-deployment)
before starting instances. Register cleanup even if startup fails: a failed or
canceled connectivity check can leave an acquired client and background work
owned until `Stop`. An already canceled startup context prevents acquisition.

### Client Settings

Set credentials and an environment prefix at construction. Here `password`
is loaded from your secret configuration:

```go
driver, err := opensearchsearch.New(address, func(settings *opensearchsearch.Builder) {
	settings.IndexPrefix("shop_")
	settings.Username("search")
	settings.Password(password)
})
```

The prefix defaults to empty and accepts at most 64 bytes of lowercase letters,
digits, and underscores. The resulting native index name cannot start with an
underscore. `Nodes` supplies additional addresses. `Client` adjusts native
configuration for CA certificates, API keys, signing, retry, timeout, or discovery
settings; operations still need context deadlines.

The `Client` configuration hook runs once during `New`, after base validation
and settings, never again during `Start`. Keep it free of lifecycle I/O. Owned
containers and scalar pointers detach from retained drafts; native objects,
functions, and captures remain shared. Finish configuring those before concurrent
use.

`NewFrom(client, configure...)` borrows an existing
`*opensearchapi.Client` from the official v5 SDK. Only `IndexPrefix` applies;
the native hook is not invoked. `Start` checks connectivity, `Stop` leaves the
client open, and `Client()` returns it immediately. You own its lifetime.

### Native Access

For an owned driver, `Client()` is nil before native creation and after `Stop`.
Use it only after successful startup, with cleanup already registered. For
example, with `net/http` imported, read cluster information:

```go
request, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
reply, err := driver.Client().Client.Request(request)
if err != nil {
	return err
}
defer reply.Body.Close()
```

Check the HTTP status and body as well as the transport error. Native requests
bypass facade validation, telemetry, and prefix mapping. Coordinate them with
shutdown: retaining a client pointer does not stop `Stop` from closing it.

Owned lifecycle calls serialize. Repeated `Start` rechecks the same client;
`Stop` before startup and repeated stops are no-ops; startup after stop can
acquire again. `Stop` waits for lifecycle serialization and SDK cleanup even
with an expired context, and does not drain application requests. Startup
cancellation does not forcibly interrupt SDK construction or native callbacks.
Native background work uses the configured native lifetime, not the startup
context. Before startup or after stop, request-bearing methods fail without
implicitly acquiring a client. `Ping` checks a node response, not cluster health
or index existence.

### Matching Limits

After startup and synchronization, use the shared
[document](indexing-documents.md#putting-documents) and [query](queries.md#catalog-filters)
operations. Writes use `refresh=wait_for` and return when searchable; the default
refresh interval is one second. Native timeouts or failed search shards return
errors, not successful partial pages. Relevance uses BM25 over one shard and
can change as documents are added.

Native `_source` wraps original JSON in an unindexed `source` string with
separate `strings`, `numbers`, and `booleans` containers. This preserves scalar
kinds for comparisons; missing, null, arrays, and objects have no scalar entry.
Nonempty, dot-free scalar keys are captured before declaration so later `Sync`
can index newly declared fields. Only declared fields are mapped. Search restores
the original JSON and external ID; no application field names are reserved.

Write through the driver, not flat JSON through the SDK, so source and indexed
containers stay consistent. Former flat-layout indexes require explicit recreation
and repopulation; `Sync` never converts or drops them automatically.

Strings use lowercase keywords for filters and sorts; values over 32,766 bytes
fail writes. Numbers normalize to float64. A keyword field cannot become
searchable in place; plan a [destructive rebuild](deploying.md#retiring-indexes).
Mapping changes and reindexing are not atomic, and `Check` compares only the
last completed schema checkpoint. Serialize deployments and native administration.

The ID tie-break requires `indices.id_field_data.enabled`. A cluster disabling
it rejects those searches. Enabling ID fielddata consumes heap; assess cluster
policy and capacity instead of treating it as a free compatibility switch.

### Local Server

With Docker and Compose installed and enough memory for OpenSearch, save this
`compose.yaml` in an empty development directory. Security is disabled: bind
only to loopback and never put real user data in this configuration.

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

Start the node and retry the request until it returns a cluster description:

```sh
docker compose up -d
curl --fail http://127.0.0.1:19200/
```

If startup fails, inspect `docker compose logs search` and the host/memory
requirements in the [OpenSearch Docker instructions](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/docker/).
Use `http://127.0.0.1:19200` with the lifecycle above, synchronize, and verify a
product put/search. Check the ID-fielddata policy if search fails despite
connectivity. Run `docker compose down --volumes` afterward to remove this
disposable stack and its volumes, discarding indexed data.

For every function and option, see the [opensearchsearch API reference](https://pkg.go.dev/arkoren.dev/driver/opensearch/opensearchsearch).
