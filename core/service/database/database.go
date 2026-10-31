package database

import (
	"context"
	"errors"

	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/foundation/collection"
	"arkoren.dev/foundation/query"
)

var (
	// ErrNoRows reports that [Driver.Find] matched no row. Drivers also keep
	// the native error, so errors.Is matches [sql.ErrNoRows] as well.
	ErrNoRows = errors.New("no database rows were found")

	// ErrEmptyLockName reports that [Driver.Lock] received an empty name.
	ErrEmptyLockName = errors.New("lock name cannot be empty")

	// ErrNestedTransaction reports that a driver cannot nest transactions:
	// [Driver.WithTransaction] was called on a transaction driver that has no
	// way to start a transaction inside its own. The driver wraps it with the
	// reason, so match it with errors.Is.
	ErrNestedTransaction = errors.New("nested transactions are not supported")

	// ErrAfterCommit marks failed hooks or driver cleanup after a confirmed
	// commit. The data committed: do not retry that transaction. The original
	// errors remain discoverable with errors.Is. An error returned by a
	// transaction body still requests rollback, even if it wraps this marker
	// from work on another database.
	//
	// Example:
	//
	// 	if errors.Is(err, database.ErrAfterCommit) {
	// 		logger.WarnContext(ctx, "post-commit work failed", "error", err)
	// 	}
	ErrAfterCommit = errors.New("after-commit work failed")

	// ErrSessionDone reports use of a session callback's driver or context
	// after that callback returned. No query is sent and no fresh connection
	// is acquired. Transactions retain their sql.ErrTxDone outcome instead.
	//
	// Example:
	//
	// 	if errors.Is(err, database.ErrSessionDone) {
	// 		logger.WarnContext(ctx, "work outlived its database session")
	// 	}
	ErrSessionDone = errors.New("database session has ended")
)

// Rows represents an open database cursor for streaming query results.
type Rows interface {
	// Next advances the cursor to its next row. It returns false when the
	// rows are exhausted, after an error, or after Close.
	Next() bool

	// Scan copies the current row into dest, which must be a non-nil pointer
	// to a struct or to a single-column value. Columns map to struct fields
	// by the rules of [query.Targets].
	Scan(dest any) error

	// Err returns the error that ended iteration, or nil after the rows were
	// exhausted.
	Err() error

	// Close releases cursor resources. It is safe to call more than once.
	// Callers must close a cursor, because it can hold a connection.
	Close() error
}

// Driver defines SQL query execution, scanning, and transaction management.
//
// Queries use the placeholder syntax of the database (see [Driver.Dialect]);
// named parameters come from [query.RawNamed], which the [Database] compiles
// before it reaches the driver. Rows are scanned into structs by the column
// rules of [query.Targets]. Every method that takes a ctx fails with an error
// matching ctx.Err() when the context ended before or during the operation.
// Conformance for every driver is checked by package databasetest.
//
// Example:
//
//	err := driver.WithSession(ctx, func(session database.Driver) error {
//		var one int
//		return session.Find(ctx, "SELECT 1", &one)
//	})
type Driver interface {
	// Dialect returns the placeholder and identifier syntax the database
	// accepts. [Database] compiles published queries for it, and migrations
	// switch on it. A wrapping driver must forward the dialect of the driver
	// it wraps; a transaction driver reports the dialect of its parent.
	Dialect() query.Dialect

	// Exec executes a data-modifying query and returns the number of rows it
	// changed.
	Exec(ctx context.Context, statement string, args ...any) (int64, error)

	// Select scans every result row into dest, a pointer to a slice of
	// structs, of pointers to structs, or of single-column values. No rows is
	// not an error and leaves dest empty.
	Select(ctx context.Context, statement string, dest any, args ...any) error

	// Query opens a cursor for query results.
	Query(ctx context.Context, statement string, args ...any) (Rows, error)

	// Find scans a single query result into dest, a pointer to a struct or
	// to a single-column value. It fails with
	// [ErrNoRows] when no row matches and scans the first row when several
	// match.
	Find(ctx context.Context, statement string, dest any, args ...any) error

	// WithSession reserves one physical connection for fn without beginning
	// a transaction. SQL, locks, and transactions through session use that
	// connection; connection loss never reconnects or replays fn. Previously
	// committed statements are not undone when fn returns an error.
	//
	// A nested session borrows the connection, preserves an active transaction,
	// and gives fn a separate callback lifetime. Its new nontransactional locks
	// are released when it returns; enclosing locks remain held. Locks acquired
	// inside a transaction belong to its outermost transaction instead.
	//
	// Drivers close outstanding cursors and release remaining owned locks before
	// returning the connection. Cleanup ignores caller cancellation and discards
	// a connection whose release cannot be established. Errors preserve fn's
	// error and cleanup errors; a panic is re-raised after cleanup. Cancellation
	// fails the scope even if fn returns nil. Arbitrary SQL session state is not
	// reset automatically. Use the session synchronously, from one goroutine.
	// Its driver and cursors expire when fn returns: later I/O fails with
	// [ErrSessionDone], never by accessing a returned or reused connection.
	//
	// Example:
	//
	// 	err := driver.WithSession(ctx, func(session database.Driver) error {
	// 		return session.WithTransaction(ctx, func(tx database.Driver) error {
	// 			_, err := tx.Exec(ctx, "UPDATE counters SET value = value + 1")
	// 			return err
	// 		})
	// 	})
	WithSession(ctx context.Context, fn func(session Driver) error) error

	// WithTransaction executes fn in a transaction started with the driver
	// defaults. It commits when fn returns nil and rolls back when fn returns
	// an error, which WithTransaction returns (matching it with errors.Is),
	// or panics, which WithTransaction re-raises after the rollback. The tx
	// passed to fn runs every operation inside the transaction.
	//
	// Calling WithTransaction on a tx starts a nested transaction: it rolls
	// back only its own work, and that work is final only when the outermost
	// transaction commits. A driver that cannot nest fails with an error
	// matching [ErrNestedTransaction].
	//
	// The callback driver and its cursors expire when fn returns, including a
	// nested callback: later I/O matches [sql.ErrTxDone]. Within a session the
	// transaction uses that same connection. Drivers retain the connection until
	// the transaction ends and its named locks are released. Cleanup failure
	// after a confirmed commit wraps [ErrAfterCommit]; a body error bearing that
	// marker still requests rollback, not commit.
	WithTransaction(ctx context.Context, fn func(tx Driver) error) error

	// Lock blocks until the named lock is held or ctx ends, and fails with
	// [ErrEmptyLockName] for an empty name. Any other name is valid whatever
	// its length. Locks are exclusive and not reentrant. The returned unlock
	// releases the lock and may be called more than once.
	// Inside a transaction the lock is held until the transaction ends,
	// whether it commits or rolls back, and unlock does nothing.
	// Locks first acquired inside a nested transaction survive its rollback or
	// panic until the outermost transaction ends. Session-level locks acquired
	// before the transaction are not released by it. Native lock names/keys may
	// be derived from name; non-reentrancy applies to that derived identity too.
	//
	// Network drivers retain one pool connection per raw held lock. Root waiters
	// release contended connections between nonblocking attempts; a session or
	// transaction already reserves its connection and cannot release it while
	// waiting. Use TryLock and end a contended session when query capacity is
	// needed elsewhere. Raw holders can still exhaust a bounded pool.
	Lock(ctx context.Context, name string) (unlock func(context.Context) error, err error)

	// TryLock takes the named lock without waiting. It returns acquired true
	// with the unlock when the lock is now held, and acquired false with a nil
	// error and a nil unlock when another holder has it. Anything that
	// prevents asking, such as an ended ctx, is an error with acquired false.
	// It does not wait for lock contention; pool acquisition and network I/O
	// can still wait under ctx. A scoped driver never acquires a second connection.
	// The name rules, exclusivity, unlock behavior, and in-transaction
	// behavior are those of [Driver.Lock].
	//
	// Use TryLock when only one process should do a piece of work and the
	// others should skip it rather than queue behind it.
	TryLock(ctx context.Context, name string) (unlock func(context.Context) error, acquired bool, err error)
}

// Database provides typed convenience methods over a driver.
type Database struct {
	// driver executes the queries.
	driver Driver

	// config holds the telemetry settings.
	config settings

	// system is the resolved db.system.name.
	system string

	// instruments caches the telemetry instruments, shared with the
	// transaction databases derived from this one.
	instruments *telemetry.Instruments[instruments]
}

// New creates a Database that delegates operations to driver. Policies
// configure its telemetry: every Exec, Select, Find, Cursor, and Lock emits a
// client span and a db.client.operation.duration measurement when the
// telemetry is enabled, and costs nothing otherwise.
// Query text is disabled by default because literals and SQL comments can
// contain private data; Builder.QueryText enables deliberate verbatim export.
//
// Example:
//
//	db := database.New(driver, func(connection *database.Builder) {
//		connection.Telemetry(app.Telemetry())
//		connection.Namespace("billing")
//	})
//	_ = db.Driver()
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
func New(driver Driver, configure ...BuilderFunc) *Database {
	database := &Database{
		driver:      driver,
		config:      newSettings(configure...),
		instruments: telemetry.NewInstruments(newInstruments),
	}

	database.system = resolveSystem(database)

	return database
}

// Driver returns the underlying root Driver, not the driver selected by a
// session or transaction context. Direct calls bypass both scopes and facade
// telemetry; use the facade with the callback context for protected work.
func (database *Database) Driver() Driver {
	return database.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() Driver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver.
func (database *Database) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](database.Driver())
}

// cursor turns a statement into a lazy typed cursor. Its span covers the whole
// iteration and ends when the cursor is exhausted, fails, or is abandoned by
// the consumer. A build error in err is yielded without a span. The cursor
// is closed before a final error is yielded, joining scan/iteration and cleanup
// failures. After consumer stop, cleanup failures reach telemetry only: calling
// yield again would violate the iterator protocol. Each iteration owns its
// execution error; it never overwrites the captured statement-build result.
func (database *Database) cursor[T any](ctx context.Context, s query.Statementer, statement query.Statement, err error) collection.TryLazySlice[T] {
	return collection.NewTryLazySlice(func(yield func(T, error) bool) {
		var zero T

		if err != nil {
			yield(zero, err)

			return
		}

		ctx, op := database.begin(ctx, s, statement.SQL())

		var err error
		defer func() {
			op.end(ctx, err)
		}()

		driver, err := database.driverFor(ctx)

		if err != nil {
			yield(zero, err)

			return
		}

		rows, err := driver.Query(ctx, statement.SQL(), statement.Args()...)

		if err != nil {
			yield(zero, err)

			return
		}

		var failedItem T
		continued := false
		defer func() {
			err = errors.Join(err, rows.Close())

			if continued && err != nil {
				yield(failedItem, err)
			}
		}()
		failedItem, continued, err = iterateRows(ctx, rows, yield)
	})
}

// iterateRows streams rows until completion, a decoding failure, cancellation,
// or consumer stop. The returned bool records whether a final error can still
// be yielded; false forbids further consumer calls. It does not close rows, so
// its owner can join cleanup failure with the original cause before reporting.
func iterateRows[T any](ctx context.Context, rows Rows, yield func(T, error) bool) (T, bool, error) {
	var zero T

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return zero, true, err
		}

		var item T

		if err := rows.Scan(&item); err != nil {
			return item, true, err
		}

		if !yield(item, nil) {
			return zero, false, nil
		}
	}

	return zero, true, rows.Err()
}

// Lock blocks until the named lock is held or ctx ends. The returned unlock
// function releases the lock.
//
// Acquiring the lock is traced as a LOCK client span; releasing it is not.
func (database *Database) Lock(ctx context.Context, name string) (func(context.Context) error, error) {
	ctx, op := database.beginLock(ctx, lockOperation)

	driver, err := database.driverFor(ctx)

	if err != nil {
		op.end(ctx, err)

		return nil, err
	}

	unlock, err := driver.Lock(ctx, name)
	op.end(ctx, err)

	return unlock, err
}

// TryLock takes the named lock without waiting. It reports acquired false,
// with a nil error, when another holder has the lock; otherwise unlock
// releases the lock it holds.
//
// Taking the lock is traced as a TRY_LOCK client span, whether or not it was
// acquired; releasing it is not.
//
// Example:
//
//	unlock, acquired, err := db.TryLock(ctx, "nightly-report")
//	if err != nil || !acquired {
//		return err // Another instance is running the report.
//	}
//	defer unlock(ctx)
func (database *Database) TryLock(ctx context.Context, name string) (func(context.Context) error, bool, error) {
	ctx, op := database.beginLock(ctx, tryLockOperation)

	driver, err := database.driverFor(ctx)

	if err != nil {
		op.end(ctx, err)

		return nil, false, err
	}

	unlock, acquired, err := driver.TryLock(ctx, name)
	op.end(ctx, err)

	return unlock, acquired, err
}
