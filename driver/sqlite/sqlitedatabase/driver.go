package sqlitedatabase

import (
	"context"
	"database/sql"
	"errors"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	// Registers the "sqlite" database/sql driver used by New.
	_ "modernc.org/sqlite"
)

// ErrInvalidConfig indicates that the arguments or pool settings given to [New]
// cannot establish a driver. Blank DSNs and negative pool limits fail before
// opening a database; native file/URI errors can still arise on first use.
//
// Example:
//
//	_, err := sqlitedatabase.New(" ")
//	if errors.Is(err, sqlitedatabase.ErrInvalidConfig) {
//		return errors.New("check the database configuration")
//	}
var ErrInvalidConfig = errors.New("invalid SQLite configuration")

// Driver implements [database.Driver] for SQLite on database/sql, scanning
// rows with [query.Targets]. Callback drivers from WithSession and
// WithTransaction use one retained connection and expire when their callback
// returns. Native SQL access deliberately returns the root pool instead.
//
// Example:
//
//	driver, err := sqlitedatabase.New(":memory:")
//	if err != nil {
//		return err
//	}
//	db := database.New(driver)
//	return db.WithSession(ctx, func(ctx context.Context) error {
//		_, err := db.Find[int](ctx, query.Raw("SELECT 1"))
//		return err
//	})
type Driver struct {
	// db executes operations on the pool, retained connection, or transaction.
	db executor

	// pool owns the connections and is closed only by the root Stop.
	pool *sql.DB

	// scope is nil for the root pool and otherwise owns this callback's
	// lifetime, cursors, and locks on a retained physical connection.
	scope *scope
}

var _ database.Driver = (*Driver)(nil)

// New configures SQLite and returns a [Driver]. In-memory DSNs default to one
// connection, including ":memory:" with parameters, "file::memory:", and file
// URIs with mode=memory. An explicit positive MaxOpenConns overrides that default;
// private memory databases then have independent contents per connection. Closing
// the last connection destroys a memory database, including after pool expiry.
//
// dsn is the SQLite data source name. It is required and has no default; a
// blank dsn returns an error wrapping [ErrInvalidConfig] naming the data
// source name, as does a negative pool limit. Policies edit [Builder] directly
// once in order before validation; nil entries panic. No database opens until
// [Driver.Start] or the first operation. Completed settings detach from retained drafts. Use
// [NewFrom] to wrap a pool the caller already opened.
//
// New adds _time_format=sqlite unless the native query parameters already set
// _time_format. Filename text does not select this policy. The default emits
// SQLite timestamp text rather than Go's [time.Time.String] text. Values
// written to columns declared DATE, DATETIME, or TIMESTAMP (the types the query
// builder uses for Date, Timestamp, and TimestampTZ on [query.SQLite]) round-trip
// exactly, offset included, and CURRENT_TIMESTAMP defaults scan too. SQLite has
// no timestamp type and compares these values as text, so ordering and
// comparisons are correct only when every value is written in UTC; convert with
// [time.Time.UTC] before writing.
//
// Example:
//
//	driver, err := sqlitedatabase.New(dsn, func(connection *sqlitedatabase.Builder) {
//		connection.MaxOpenConns(10)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func New(dsn string, configure ...BuilderFunc) (*Driver, error) {
	config := configureSettings(configure)

	if err := validate(dsn, config); err != nil {
		return nil, err
	}

	// Evaluated against the caller's dsn, before withTimeFormat rewrites it.
	if config.MaxOpenConns == 0 && memoryDSN(dsn) {
		config.MaxOpenConns = 1
	}

	pool, err := sql.Open("sqlite", withTimeFormat(dsn))

	if err != nil {
		return nil, err
	}

	return newFrom(pool, config), nil
}

// NewFrom wraps a SQLite pool the caller opened, for example with
// [database/sql.Open] and the "sqlite" driver. The driver takes ownership:
// [Driver.Stop] closes the pool.
//
// Only positive [Builder] pool limits apply, on top of the
// limits already set on pool. New's in-memory single-connection rule and its
// time format are not applied to a pool opened elsewhere.
// Native connections must implement database/sql/driver.ConnBeginTx, as the
// pinned modernc driver does, so BEGIN uses its caller context without an
// independent sql.Tx rollback worker. Other native profiles fail transactions
// with an error matching errors.ErrUnsupported rather than dropping cancellation.
// Policies run once in order on one draft; nil entries panic and expanded nil
// or empty slices omit edits. Retained drafts cannot change the pool later.
//
// Example:
//
//	driver := sqlitedatabase.NewFrom(pool, func(connection *sqlitedatabase.Builder) {
//		connection.MaxOpenConns(10)
//	})
//	return driver.Stop(ctx)
func NewFrom(pool *sql.DB, configure ...BuilderFunc) *Driver {
	return newFrom(pool, configureSettings(configure))
}

// Dialect reports [query.SQLite] for roots and callback handles alike. Use it
// when compiling SQL placeholders and identifiers for a native operation.
//
// Example:
//
//	statement, err := query.Raw("SELECT ?", 7).Compile(driver.Dialect())
//	if err != nil {
//		return err
//	}
//	_, err = driver.Exec(ctx, statement.SQL(), statement.Args()...)
func (driver *Driver) Dialect() query.Dialect {
	return query.SQLite
}

// Start verifies connectivity so a misconfigured database fails fast. The
// pool itself is opened lazily by [New]. On callback drivers, readiness runs
// through the retained executor and rejects an expired callback.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	return driver.Ping(ctx)
}

// Stop closes the owned root connection pool through database/sql, including a
// pool transferred through NewFrom. It ignores ctx, so an expired context does
// not skip release. Finish callbacks and close cursors before stopping. On a
// callback driver Stop is a no-op because only the root owns the pool.
//
// Example:
//
//	if err := driver.Stop(context.Background()); err != nil {
//		return err
//	}
func (driver *Driver) Stop(context.Context) error {
	if driver.scope != nil {
		return nil // Only the root owns the caller-supplied pool.
	}

	return driver.pool.Close()
}

// Ping verifies connectivity through the pool on the root and the current
// retained executor in a callback. It never allocates a second scoped connection.
// An expired callback fails with its session or transaction lifetime sentinel.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	if driver.scope != nil {
		var one int

		return driver.Find(ctx, "SELECT 1", &one)
	}

	return driver.pool.PingContext(ctx)
}
