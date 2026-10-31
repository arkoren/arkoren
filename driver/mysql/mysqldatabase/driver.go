package mysqldatabase

import (
	"context"
	"database/sql"
	"errors"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	// Registers the "mysql" database/sql driver used by New.
	_ "github.com/go-sql-driver/mysql"
)

// ErrInvalidConfig identifies invalid constructor arguments or native DSN
// settings, before any connection attempt. DSN parse diagnostics omit supplied
// values; the original native cause remains accessible through errors.Unwrap.
//
// Example:
//
//	_, err := mysqldatabase.New(" ")
//	if errors.Is(err, mysqldatabase.ErrInvalidConfig) {
//		return errors.New("check the database configuration")
//	}
var ErrInvalidConfig = errors.New("invalid MySQL configuration")

// Driver implements [database.Driver] for MySQL on database/sql, scanning
// rows with [query.Targets]. Session and transaction callbacks receive a
// short-lived Driver bound to one physical connection. That connection is
// never replaced after failure, and callback drivers cannot be reused later.
//
// Example:
//
//	driver, err := mysqldatabase.New(dsn)
//	if err != nil {
//		return err
//	}
//	return driver.WithSession(ctx, func(session database.Driver) error {
//		_, err := session.Exec(ctx, "UPDATE counters SET value = value + 1")
//		return err
//	})
type Driver struct {
	// db executes operations on the root pool, retained connection, or active
	// transaction. Scoped operations never fall back to pool.
	db executor

	// pool supplies root queries and retained connections. Only the root
	// driver's Stop closes it, including a pool transferred through NewFrom.
	pool *sql.DB

	// scope guards the callback lifetime and owns its cursors and named locks.
	// It is nil only on the root driver.
	scope *scope
}

var _ database.Driver = (*Driver)(nil)

// New configures MySQL and returns a [Driver].
//
// parseTime=true is added unless the native parameter list sets parseTime, so
// DATETIME columns scan into time.Time. Text inside credentials or the database
// name does not count as a parameter; explicit parseTime=false remains disabled.
//
// dsn is the MySQL data source name. It is required and has no default; a
// blank or malformed dsn returns an error matching [ErrInvalidConfig] without
// printing supplied DSN values, as does a negative pool limit. Policies edit [Builder] directly
// once in order before validation; nil entries panic. No network I/O happens
// until [Driver.Start] or the first operation. Completed settings detach from retained drafts. Use
// [NewFrom] to wrap a pool the caller already opened.
//
// Example:
//
//	driver, err := mysqldatabase.New(dsn, func(connection *mysqldatabase.Builder) {
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

	pool, err := sql.Open("mysql", withParseTime(dsn))

	if err != nil {
		return nil, &invalidDSNError{cause: err}
	}

	return newFrom(pool, config), nil
}

// NewFrom wraps a MySQL pool the caller opened, for example with
// [database/sql.Open] and the "mysql" driver. The driver takes ownership:
// [Driver.Stop] closes the pool.
//
// Only positive [Builder] pool limits apply, on top of the
// limits already set on pool. New's parseTime default is not applied to a
// pool opened elsewhere, so set parseTime=true in its DSN to scan DATETIME
// columns into [time.Time].
// Policies run once in order on one draft; nil entries panic and expanded nil
// or empty slices omit edits. Retained drafts cannot change the pool later.
//
// Example:
//
//	driver := mysqldatabase.NewFrom(pool, func(connection *mysqldatabase.Builder) {
//		connection.MaxOpenConns(10)
//	})
//	return driver.Stop(ctx)
func NewFrom(pool *sql.DB, configure ...BuilderFunc) *Driver {
	return newFrom(pool, configureSettings(configure))
}

// Dialect reports [query.MySQL] for roots and callback handles alike. Use it to
// compile queries with MySQL placeholders and identifier quoting.
//
// Example:
//
//	statement, err := query.Raw("SELECT ?", 7).Compile(driver.Dialect())
//	if err != nil {
//		return err
//	}
//	_, err = driver.Exec(ctx, statement.SQL(), statement.Args()...)
func (driver *Driver) Dialect() query.Dialect {
	return query.MySQL
}

// Start verifies connectivity so a misconfigured database fails fast. The
// pool itself is opened lazily by [New]. Callback handles check their retained
// executor and lifetime instead of borrowing a second connection.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	return driver.Ping(ctx)
}

// Stop closes the owned root pool, including a pool supplied through NewFrom.
// It ignores ctx and does nothing on callback drivers: only their enclosing
// scope may finish a retained session or transaction. sql.DB.Close handles
// outstanding native operations according to database/sql's lifecycle rules.
//
// Example:
//
//	err := driver.Stop(context.Background())
func (driver *Driver) Stop(context.Context) error {
	if driver.scope != nil {
		return nil
	}

	return driver.pool.Close()
}

// Ping verifies connectivity on this driver's executor. A scoped ping never
// borrows another connection, and an escaped callback driver fails before I/O.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	if driver.scope != nil {
		var value int

		return driver.Find(ctx, "SELECT 1", &value)
	}

	return driver.pool.PingContext(ctx)
}
