# Arkoren Search OpenSearch Driver

`arkoren.dev/driver/opensearch/opensearchsearch` implements `search.Driver` on
top of [OpenSearch](https://opensearch.org) with the official client,
`github.com/opensearch-project/opensearch-go/v5` (OpenSearch 2.19 and 3.x). Each
search index is one OpenSearch index with an explicit mapping, and `Sync`
creates it or updates its mapping without losing documents.

Run `go get arkoren.dev/driver/opensearch` in your application's Go module.
With an existing `app` and the `Products` definition from
[Search Overview](../../cmd/arkoren/skills/arkoren/references/guides/search/overview.md),
connect and register lifecycle hooks:

```go
import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/opensearch/opensearchsearch"
)

err := app.Run(ctx, func(compose *application.Composer) {
	driver := compose.Must(opensearchsearch.New("http://127.0.0.1:19200"))
	compose.Cleanup(driver.Stop)
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(Products)
	})
	compose.Startup(driver.Start)
	compose.Startup(engine.Sync)
	compose.Check("search", driver.Ping)
})
```

The plaintext address is for the loopback-only development service in the
[OpenSearch guide](../../cmd/arkoren/skills/arkoren/references/guides/search/drivers.md#opensearch).
Use TLS and authentication for a remote cluster. After startup, use the
overview's indexing and search tasks to verify matching documents. In
production, run `Sync` once from a release command and `Check` at startup.

`New(address, configure...)` publishes configuration without constructing the
native SDK client or doing lifecycle I/O. `Start` creates the client and checks
that the cluster answers; `Stop` closes it and cancels native background work.
`Client()` returns nil before native creation and after `Stop`. Move native
access after `Start`, and handle native URL/certificate validation errors there
rather than at `New`. Empty addresses and invalid prefixes still fail at `New`.
`NewFrom(client, configure...)` wraps an `*opensearchapi.Client` you already
built: native access remains immediate and `Stop` leaves it open.

Register cleanup even if startup might fail: a failed connectivity check retains
the created native client until `Stop`. Repeated `Start` checks that same client;
after `Stop`, a later `Start` can create another one. Repeated `Stop` and
`Stop` before `Start` do nothing. Concurrent owned lifecycle calls serialize;
`Stop` waits for native construction and SDK cleanup, even with an expired
context, but does not drain your requests. Connectivity requests use the startup
context; it cannot forcibly interrupt native construction or lifecycle
serialization. An already canceled startup context prevents acquisition. Request
methods never implicitly create a client; before startup, requests fail as not started.

`Builder.IndexPrefix` lets several applications share a cluster. Policies run
directly once in order on one fresh draft before validation; nil entries panic
and expanded nil or empty slices omit edits. Owned node slices are copied on
input and publication; retained drafts cannot change the driver. `New` invokes
the native `Client` hook directly once after base settings and basic validation,
before publication; `Start` never replays it. Native configuration containers
and scalar pointers are copied, while native objects and functions remain
shared. Keep hooks free of lifecycle I/O and configure shared objects before
concurrent use. `NewFrom` applies only `IndexPrefix` and never invokes that hook.
See the package documentation for the mapping, query compilation, and caveats:
a keyword field cannot become searchable without dropping the index. Nonphrase
term words can match across fields; phrases stay within one field. Native search
timeouts and shard failures are errors, not successful partial pages.

Mapping edits and reindexing are not atomic. Failed reindexing leaves the prior
completed schema checkpoint so `Sync` can retry. Serialize deployments and
native index administration. String, numeric, and boolean values use separate
physical containers, so comparisons and memberships do not coerce between kinds.
Missing, null, arrays, and objects match no scalar comparison, including
negations. Mixed-kind `NOT IN` matches no scalar, as core requires.

Native `_source` wraps the original JSON as an unindexed `source` string beside
`strings`, `numbers`, and `booleans` objects. Scalar values with nonempty,
dot-free keys are captured, but only declared fields are indexed, so `Sync` can
reindex existing documents when fields gain roles. `Driver.Search` restores the original bytes
and external ID without reserving application field names. Empty/dotted keys
remain in the original JSON but cannot be declared index fields. Native SDK
document calls see the wrapper; write documents through `Driver.Put`. Explicitly recreate
local indexes created with the former flat representation if needed. The driver
does not automatically convert, rebuild, or drop existing indexes.

The existing ID tie-break uses native `_id` fielddata, which must remain enabled
through `indices.id_field_data.enabled` and can consume substantial heap.

Dropping an index deletes every indexed document. Keep rebuildable source
data and plan downtime before a field-type rebuild. See the
[driver API reference](https://pkg.go.dev/arkoren.dev/driver/opensearch/opensearchsearch)
for mappings, native client access, and limitations.
