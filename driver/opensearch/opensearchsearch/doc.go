// Package opensearchsearch provides a search driver for [OpenSearch], built on
// the official opensearch-go client.
//
// Use it when you need a clustered search service for large indexes. Each
// search index is one OpenSearch index with one shard, an explicit mapping
// derived from the definition, and the definition stored in the mapping's
// _meta, which is how [Driver.Check] tells a synced index from a changed one.
//
// # Usage
//
// [New] takes the address of the cluster and publishes configuration without
// constructing a native client or doing lifecycle I/O. [Driver.Start] creates
// the owned client and checks connectivity; [Driver.Stop] closes it and cancels
// native background work, including after failed connectivity checks. Register
// cleanup even when Start can fail. Native URL and certificate validation errors
// are reported by Start; empty addresses and invalid prefixes still fail at New.
// Register the pair with the application lifecycle, and run [search.Engine.Sync]
// where the application deploys:
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		driver := compose.Must(opensearchsearch.New("http://localhost:9200", func(engine *opensearchsearch.Builder) {
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
// [NewFrom] wraps an [opensearchapi.Client] the caller built, for TLS, API
// keys, or request signing; [Driver.Start] still checks the cluster, and
// [Driver.Stop] leaves the client open. [Driver.Client] returns this borrowed
// client immediately; for New it returns nil before native creation and after
// Stop. Move native access after Start. Request methods do not acquire implicitly.
// Owned Start/Stop calls serialize; Stop before Start and repeated Stop do
// nothing. Repeated Start rechecks connectivity; Start after Stop can acquire
// again without replaying configuration hooks. Startup cancellation does not
// forcibly interrupt native construction, native callbacks, or serialization.
//
// # Mapping
//
// Native _source is a small wrapper: source is the original JSON as an
// unindexed string; strings, numbers, and booleans contain only top-level values
// of their respective kinds. Missing, null, arrays, and objects have no scalar
// entry. Separate containers prevent native cross-kind coercion, including for
// inequalities and membership. Numbers normalize to float64, strings compare
// case-insensitively, and timestamp operands compare as UTC RFC 3339 strings.
// Scalar fields with nonempty, dot-free keys are captured before declaration,
// but only declared fields are mapped. Empty/dotted keys cannot be declared and
// stay only in the original JSON, avoiding native object-path collisions.
// Sync can therefore reindex newly declared fields in place.
// Search returns the original JSON bytes, not this wrapper, and reserves no
// application field names. Native SDK document calls see the physical wrapper;
// use Driver.Put for document writes rather than sending flat JSON natively.
// Recreate local indexes made with the former flat representation explicitly;
// the driver never converts or drops an index automatically.
//
// A searchable string is text with an analyzer that lowercases and splits on
// every character that is not a letter or digit. A string that is only
// filterable or sortable is a keyword with a lowercase normalizer; searchable
// strings additionally used in comparisons get a keyword subfield. Numbers and
// booleans use independent double and boolean mappings. Application document IDs
// remain native _id values, independent of any ID field in the original JSON.
//
// # Searching
//
// Every positive text term is a clause that matches in any searchable field
// (a match, a match_phrase, or, for the last word, a match_bool_prefix);
// excluded terms are must_not clauses; filters are in filter context and do
// not score. The sort is the query's orders, then the relevance score when the
// text has a positive term, then the document ID. A text without a positive
// term, an exclusion alone, scores 0. Searches use a fixed preference, so
// equal scores keep their order between calls on a cluster with replicas.
// Relevance is OpenSearch's BM25 over one shard, so scores change as documents
// are added.
// Mixed-kind NOT IN matches no scalar: every operand requires a compatible
// typed inequality. Homogeneous numeric NOT IN requires numeric field presence.
// IN accepts any same-kind equality; NOT IN requires every typed inequality.
//
// # Caveats
//
//   - Nonphrase term words can match different searchable fields. A phrase
//     still requires consecutive words in a single field.
//   - Writes use refresh=wait_for, so a call returns when its documents are
//     searchable, which takes up to the index refresh interval (one second by
//     default).
//   - When a definition changed, Sync updates the mapping and reindexes the
//     existing documents in place (update-by-query), which takes a while on a
//     large index. A keyword field cannot become searchable (OpenSearch
//     rejects the type change); drop the index, sync, and put the documents
//     again.
//     Mapping edits can become visible before completion. A failed reindex
//     leaves the prior completed schema checkpoint so retrying Sync reindexes
//     again. Serialize schema deployments and native administration; this is
//     not an atomic migration or an external-change detector.
//   - A filterable or sortable string over 32,766 bytes is rejected by
//     OpenSearch and fails the write.
//   - Native search timeouts or shard failures return errors instead of partial
//     pages and counts. SDK-native calls bypass these checks.
//   - The existing ID tie-break sorts on native _id fielddata. A cluster that
//     disables indices.id_field_data.enabled rejects these searches. Fielddata
//     loads IDs into heap; assess the cluster's capacity and policy.
//
// # Construction policies
//
// New and NewFrom apply Builder policies directly once in order before
// validation. Nil entries panic; nil or empty slice expansions omit edits.
// Owned node lists detach from inputs and retained drafts. New invokes Client's
// native configuration hook directly once after base settings and basic
// validation, before publication; Start never replays it. The completed native
// configuration's containers and scalar pointers are copied, not native objects,
// functions, or their captures. Keep hooks free of lifecycle I/O. NewFrom only
// applies IndexPrefix and never invokes that hook. A supplied native client remains
// caller-owned, while New owns and closes the client it creates.
//
// [OpenSearch]: https://opensearch.org
package opensearchsearch
