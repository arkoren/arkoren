// Package meilisearchsearch provides a Meilisearch [search.Driver]: documents
// live in a Meilisearch server and are searched through its HTTP API.
//
// Choose it when you want a dedicated search service with fast prefix search
// and little setup. It needs Meilisearch 1.15 or later (the first version
// whose filters compare strings, which the contract uses for timestamps);
// [Driver.Start] checks the version. The driver talks to the REST API with
// net/http instead of the official Go SDK, because the SDK's settings type
// cannot express empty lists, which [Driver.Check] must compare, and the
// whole surface used here is a handful of endpoints.
//
// # Wiring
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		driver := compose.Must(meilisearchsearch.New("http://localhost:7700", func(engine *meilisearchsearch.Builder) {
//			engine.APIKey(os.Getenv("MEILI_MASTER_KEY"))
//			engine.IndexPrefix("shop_")
//		}))
//		compose.Cleanup(driver.Stop)
//		engine := search.New(driver, func(engine *search.Builder) {
//			engine.Indexes(Products)
//		})
//		compose.Startup(driver.Start)
//		compose.Startup(engine.Sync)
//		compose.Check("search", driver.Ping)
//	})
//	if err != nil {
//		return err
//	}
//
// # How the contract maps to Meilisearch
//
// Every index becomes one Meilisearch index whose uid is the index prefix
// plus the index name, with the primary key arkoren_id. [Driver.Sync]
// creates it and sets its searchable, filterable, and sortable attributes,
// the ranking rules, disables typo tolerance, and pins the pagination limit
// to [search.MaxHits]; [Driver.Check] compares exactly those settings.
//
// Fields whose names start with arkoren_ are reserved: the driver adds
// arkoren_id (the document ID) and arkoren_order (the ID in a form that sorts
// in byte order) when it writes a document and removes them when it reads
// one. [Driver.Put] rejects, with [search.ErrUnsupported], a document that
// has a field with that prefix or the native _rankingScore field.
//
// Writes and deletes wait for the Meilisearch task to finish, so they are
// visible when the call returns. If the context ends while waiting, the call
// returns the context's error but the server may still apply the change.
//
// # Differences from the contract
//
// Meilisearch folds case and diacritics, so a search for cafe also matches
// café, and string filters ignore both as well. The driver limits positive query
// words and phrases to ten to avoid native truncation; a longer
// one fails with [search.ErrUnsupported] instead of matching too much. An
// excluded nonphrase term that splits into several words (-t-shirt) also
// returns ErrUnsupported: a native phrase exclusion changes its meaning.
// Meilisearch does not
// index string values of more than 500 bytes for filtering and sorting: such a
// value matches no filter and sorts last, and a document ID of more than 250
// bytes loses its place in the ID tie-break. Filters that compare a field
// holding a value of another type than the query's value can be coerced by the
// backend. Keep operand types aligned; this is not permission from the core's
// type-sensitive comparison contract.
//
// # Construction policies
//
// New and NewFrom apply Builder policies directly once in order before
// validation. Nil entries panic; nil or empty slice expansions omit edits.
// Both APIKey and IndexPrefix apply to a supplied HTTP client; the client stays
// caller-owned, while Stop closes its idle connections. Completed scalar
// settings detach from retained drafts. Config remains a redacted record only.
// New clones a native default HTTP transport but shares application wrappers.
// Base URLs can have path prefixes, not query strings or fragments. Check also
// validates the required primary key; native administration must be serialized
// with deployments because metadata reads and asynchronous tasks are not atomic.
package meilisearchsearch
