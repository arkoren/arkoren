// Package databasetest provides a conformance suite for [database.Driver]
// implementations.
//
// [Run] executes the documented behavior of the contract against a driver
// the caller supplies: affected-row counts, positional and named queries,
// cursors, [database.ErrNoRows] mapping, sessions, transactions, locks, and
// context cancellation. Session checks require one physical connection across
// transactions, lexical handle/cursor expiration, automatic lock cleanup, and
// non-reentrant ownership that survives nested rollback. Use a pool with enough
// capacity for independent contenders; retained scopes intentionally hold theirs.
// A schema suite also creates a table with every portable
// column type from [schema.Create], writes a Go value through the query
// builder, and requires the same value back. Each subtest gets a fresh driver
// and its own uniquely named table, created and dropped with portable SQL, so
// the suite runs against any database, including the SQLite driver used for development and tests.
//
// Positional placeholders are written as ? and rewritten for the dialect
// the driver reports through [database.Driver].Dialect; a driver whose dialect is
// [query.ANSI] must accept ?.
//
// A typical driver test:
//
//	func TestConformance(t *testing.T) {
//		databasetest.Run(t, func(t *testing.T) database.Driver {
//			driver, err := sqlite.New(":memory:")
//			if err != nil {
//				t.Fatal(err)
//			}
//			t.Cleanup(func() {
//				_ = driver.Stop(context.Background())
//			})
//
//			return driver
//		})
//	}
package databasetest
