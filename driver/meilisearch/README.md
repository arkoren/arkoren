# Arkoren Search Meilisearch Driver

`arkoren.dev/driver/meilisearch/meilisearchsearch` implements `search.Driver`
on a [Meilisearch](https://www.meilisearch.com) server, version 1.15 or later.
It talks to the REST API with `net/http` and has no third-party dependency.

Run `go get arkoren.dev/driver/meilisearch` in your application's Go module.
The [Meilisearch guide](../../cmd/arkoren/skills/arkoren/references/guides/search/drivers.md#meilisearch)
provides a disposable local server, API key, and connectivity check without an
Arkoren checkout. Handle constructor errors in the focused fragments below.

Given your `Products` index definition from the
[search guide](../../cmd/arkoren/skills/arkoren/references/guides/search/overview.md),
connect to the local server and register that index:

```go
import (
	"os"

	"arkoren.dev/core/application"
	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/meilisearch/meilisearchsearch"
)

err := app.Run(ctx, func(compose *application.Composer) {
	driver := compose.Must(meilisearchsearch.New("http://localhost:17700", func(engine *meilisearchsearch.Builder) {
		engine.APIKey(os.Getenv("MEILI_MASTER_KEY"))
	}))
	compose.Cleanup(driver.Stop)
	engine := search.New(driver, func(engine *search.Builder) {
		engine.Indexes(Products)
	})
	compose.Startup(driver.Start)
	compose.Startup(engine.Sync)
	compose.Check("search", driver.Ping)
})
```

The existing `app` completes composition before startup. Startup connects the
driver before synchronizing indexes; cleanup also runs if composition or startup
fails. Pass `engine` to your application services inside the callback.

Index callbacks edit one draft; the published definition is immutable. Field
lists accumulate, and `Schema()` returns sorted, deduplicated copies. Supply
at least one searchable field. `Products.Err()` and engine operations report
invalid definitions before sending a request.

`New(host, configure...)` validates the server URL and does no I/O. Path prefixes
are supported; query strings and fragments are rejected. A native default HTTP
transport is cloned, while application-installed wrappers remain shared.
`NewFrom(client, host, configure...)` sends requests with an `*http.Client` you
configured; both `APIKey` and `IndexPrefix` apply. `Builder.IndexPrefix` puts
indexes of several applications on one server. Policies run directly once in
order on one fresh draft, before validation. Nil entries panic; expanded nil or
empty slices omit edits. Retained drafts cannot change published settings.
`Config` remains a redacted record, not constructor input. `Stop` closes idle
HTTP connections but does not take ownership of a supplied client.

`Sync` creates each index with the primary key `arkoren_id` and sets its
searchable, filterable, and sortable attributes, ranking rules, typo tolerance
(off), and pagination limit. `Check` compares those settings and the required
primary key. Do not change them by hand or race native administration with
deployment. Fields starting with `arkoren_`, and `_rankingScore`, are reserved. Writes
wait for Meilisearch to finish indexing, so they are visible when they
return.

Write batches use an approximate 8-MiB threshold, not a hard payload ceiling:
documents are never split and the server's configured limit still applies.
Nonphrase multiword exclusions such as `-t-shirt` fail with `ErrUnsupported`
instead of silently becoming phrase exclusions. Keep filter operand types
aligned with stored scalar types; backend coercion is not a portable core rule.

After startup, use the [indexing and search tasks](../../cmd/arkoren/skills/arkoren/references/guides/search/overview.md)
to verify matching documents. This `Sync` hook suits a single instance; for
production, run synchronization once in a [release command](../../cmd/arkoren/skills/arkoren/references/guides/search/deploying.md)
and use `engine.Check` at startup. See the
[driver API reference](https://pkg.go.dev/arkoren.dev/driver/meilisearch/meilisearchsearch).
