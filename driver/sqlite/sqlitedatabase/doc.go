// Package sqlitedatabase provides a SQLite [database.Driver] on database/sql.
// Constructors configure a lazily opened pool and never run schema DDL. NewFrom
// takes ownership of the supplied pool, which the root Driver.Stop closes.
// New and NewFrom apply Builder policies directly once in order before
// validation. Nil entries panic; nil or empty slice expansions omit edits.
// Completed settings detach from retained drafts. NewFrom only applies positive
// pool limits, not New's DSN rewriting or in-memory single-connection default.
// Memory DSNs include :memory: with parameters, file::memory:, and file URIs
// whose mode parameter is memory. New defaults them to one connection unless
// a positive MaxOpenConns is supplied; private memory connections do not share
// contents. Closing the last connection destroys the memory database.
// Only native query parameters select _time_format; filename substrings do not.
//
//	driver, err := sqlitedatabase.New(":memory:", func(connection *sqlitedatabase.Builder) {
//		connection.MaxOpenConns(1)
//	})
//	if err != nil {
//		return err
//	}
//	db := database.New(driver)
//
// WithSession retains one physical connection without starting a transaction:
//
//	err := driver.WithSession(ctx, func(session database.Driver) error {
//		return session.WithTransaction(ctx, func(tx database.Driver) error {
//			_, err := tx.Exec(ctx, "UPDATE counters SET value = value + 1")
//			return err
//		})
//	})
//
// Transactions use the retained connection and nested transactions use
// savepoints. Named locks combine process-local exclusion with sibling file
// locks on supported Unix hosts. They are not reentrant. Transaction locks
// survive nested rollback; nontransactional session locks end with their owning
// callback unless explicitly unlocked earlier. Callback drivers and cursors
// expire on return, and cleanup closes outstanding results before releasing the
// connection. Native SQL returns the root pool and deliberately bypasses scopes.
// The driver owns native transaction completion instead of using sql.Tx's
// automatic rollback worker, so cancellation finishes rollback before managed
// locks release. Native BEGIN keeps its caller context. Native Commit/Rollback
// do not take contexts and are not bounded by the savepoint-cleanup timeout.
// Panic and runtime.Goexit both roll back rather than commit incomplete bodies.
//
// Raw Driver.Select replaces previous destination elements; an empty result
// leaves an empty slice. A scanning failure can leave the successfully scanned
// prefix. The Database.Select facade creates a fresh typed result instead.
// Native close errors from Find and Select are preserved alongside scan errors
// and prevent a retained session from committing or being pooled as healthy.
package sqlitedatabase
