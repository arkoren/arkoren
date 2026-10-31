// Package postgressearch provides a PostgreSQL [search.Driver] built on
// full-text search (tsvector, tsquery, and a GIN index) over two tables that
// the application creates with its migrator.
//
// # Setup
//
// The driver never runs DDL. [Driver.Migrations] returns the migrations of
// both tables; add them to the application's migrator, run them, then use the
// driver:
//
//	driver, err := postgressearch.NewFrom(db)
//	if err != nil {
//		return err
//	}
//	migrator := migrate.New(db)
//	err = migrator.Add(driver.Migrations()...) // with the application's own
//	// run migrator.Up in the release command, then:
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
//	err = engine.Sync(ctx) // DML only: it records index definitions
//
// The tables are search_documents (one row per document: index name, ID, the
// JSON source, and the tsvector) and search_indexes (one row per index: its
// stored definition). With [Builder.Instance] the names are prefixed
// "<instance>_", so several independent search backends share one database.
// Table names are not configurable, and renaming an instance is unsupported.
// Instance names may start with a digit. Both migration directions quote SQL
// identifiers, and readiness resolves their literal names, without changing
// existing physical names, migration groups, or advisory-lock identities. Start
// and Ping never create DDL.
//
// # How text and filters work
//
// Text uses the built-in "simple" configuration: no stemming, no stop words.
// Before indexing, every character that is not a letter or digit becomes a
// space, so the words match the contract's rules (red-shoes is two words).
// The searchable fields of a document are concatenated with a gap, so a
// phrase never spans two fields. Queries are built from bound parameters:
// each word a separate tsquery joined with &&, phrases with phraseto_tsquery,
// the prefix word with a :* match, and exclusions with !!. Relevance is
// ts_rank.
//
// Filters and orders read the stored JSON source directly
// (lower(source ->> 'field') and typed casts), so they are not served by an
// index: they are fine for small and medium indexes and scan the rows the
// text query selects. Strings compare case-insensitively and byte-wise
// (COLLATE "C"), numbers as float8, and documents with a missing, null, or
// differently typed value match no comparison and sort last.
//
// # Deploying and caveats
//
// [Driver.Sync] runs under a transaction-scoped advisory lock and changes only
// data: it upserts index definitions and, when an index's searchable fields
// changed, recomputes tsv for every document of the index, which can be slow
// on large indexes. Without an enclosing database transaction, writes commit
// before the call returns. Inside an enclosing transaction, other transactions
// see them only after that transaction commits.
// The driver does not own the *database.Database it wraps: [Driver.Stop] does
// nothing, and the application stops the database driver itself.
//
// # Construction policies
//
// NewFrom applies Builder policies directly once in order before validation.
// Nil entries panic; nil or empty slice expansions omit edits. Retained drafts
// cannot change the published instance, tables, or migration group. The
// application keeps ownership of the database and remains the only DDL owner.
package postgressearch
