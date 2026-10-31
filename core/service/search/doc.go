// Package search provides a vendor-neutral full-text search contract and a
// typed facade.
//
// An application describes what it searches with an [Index] per document
// type, registers the indexes with an [Engine], puts documents into them, and
// searches them with a [Query]. Definition callbacks edit one fresh draft;
// published indexes and queries expose no configuration setters. [Query.Derive]
// creates an independent local query variant. For example:
//
//	type Product struct {
//		ID          string    `json:"id"`
//		Title       string    `json:"title"`
//		Description string    `json:"description"`
//		Category    string    `json:"category"`
//		Price       float64   `json:"price"`
//		InStock     bool      `json:"in_stock"`
//		CreatedAt   time.Time `json:"created_at"`
//	}
//
//	var Products = search.NewIndex("products", func(product Product) string {
//		return product.ID
//	}, func(index *search.IndexBuilder) {
//		index.Searchable("title", "description")
//		index.Filterable("category", "price", "in_stock")
//		index.Sortable("price", "created_at")
//	})
//
// Engine Builder callbacks run directly once in order on one fresh draft.
// Nil entries panic; expanded nil/empty slices omit edits. Indexes appends
// copied registration storage; published roots detach from retained drafts.
// Shared index implementations and telemetry services are not cloned.
//
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
//	if err := engine.Sync(ctx); err != nil { // deploy step
//		return err
//	}
//	if err := engine.Put(ctx, Products, products...); err != nil {
//		return err
//	}
//
//	query := search.NewQuery(Products, "red shoes", func(query *search.QueryBuilder) {
//		query.Where("price", "<=", 100)
//		query.OrderBy("price")
//	})
//	page, err := engine.Search(ctx, query, pagination.PageParams{Page: 1, PerPage: 20})
//	if err != nil {
//		return err
//	}
//	_ = page.Items
//
// Callbacks run directly once in order; supplied nil functions panic, while
// expanded nil/empty slices omit edits. Index validation uses completed state;
// query errors are sticky and survive Derive. Publication copies owned field,
// condition, group, membership, and order containers, not closure captures.
// Custom Indexer implementations expose both Schema and Err. Registration
// checks the complete definition, including key-function failures that a raw
// schema cannot represent. Zero Query values are invalid even after Derive.
//
// # Query text
//
// Words are all required. "Double quotes" make a phrase, whose words must be
// adjacent and in order. A leading minus excludes a word or phrase. The last
// plain word of the text is a prefix, so a user still typing finds results.
// Everything else is an ordinary word: no text is an error, an unclosed quote
// runs to the end, and empty text matches every document. [NewQuery] parses
// the text once; drivers receive the terms in [Spec.Text].
//
// # Documents and fields
//
// A document is encoded with encoding/json/v2; index field names are its
// top-level keys. Searchable fields hold strings. Filterable and sortable
// fields hold one string, number, or boolean type each. Text processing is
// language-neutral in v1 (lowercase, split on anything that is not a letter or
// digit, no stemming or typo tolerance), and string filters are
// case-insensitive.
//
// # Ordering and paging
//
// Results are ordered by the query's orders, then by relevance when the query
// has text, then by ascending document ID, so the order is total and stable.
// [Hit.Score] is comparable only within one result. A search can reach the
// first [MaxHits] results: page times per-page above it fails with
// [ErrInvalidQuery], and [pagination.Page.Total] stops at it.
// The returned Items field is an eager collection.Slice of typed hits. Local
// Map, Filter, and GroupBy operations visit only this window, retain normal
// shallow element sharing, and do not search again or recompute server totals.
//
// # Deploying
//
// [Engine.Sync] creates indexes and applies changed definitions without
// losing documents, and may reindex. [Engine.Check] verifies, read-only, that
// the backend matches the code and fails with [ErrSchemaChanged] otherwise.
// Run Sync from a release command (or register it with Composer.Startup in
// development) and register Check with Composer.Startup, never as a health check.
//
// # Telemetry
//
// Engine.Sync, Check, Put, Delete, Drop, and Search emit one client span and the
// arkoren.search.operations and arkoren.search.operation.duration metrics
// when the [Builder.Telemetry] service (default: [telemetry.Default]) is started.
// They record the operation, engine name ([Builder.Name]), index name, and for
// searches the hit count on the span, never query text, documents, or error
// text.
// Driver and DriverAs are uninstrumented accessors; direct driver calls bypass
// facade instrumentation, conversion, validation, and notifications.
//
// Builder.Events opts into best-effort in-process Synced, Dropped, and Failed
// administrative notifications; nil disables them. They observe driver Sync
// and Drop calls, never queries or document writes/deletes. Payloads contain
// only operation, duration, and bounded error classes, not index names or data.
// Invalid registrations emit none. Sync success may be a no-op; failure may
// follow partial changes and implies no rollback. Notifications establish
// neither atomicity nor index consistency and are not a durable audit trail.
package search
