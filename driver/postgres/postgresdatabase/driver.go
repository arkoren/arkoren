package postgresdatabase

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidConfig identifies invalid constructor arguments and native DSN
// settings. DSN parse diagnostics omit supplied values; the original native
// cause remains available through errors.As for explicit, non-logging inspection.
//
// Example:
//
//	_, err := postgresdatabase.New(" ")
//	if errors.Is(err, postgresdatabase.ErrInvalidConfig) {
//		return errors.New("check the database configuration")
//	}
var ErrInvalidConfig = errors.New("invalid PostgreSQL configuration")

// Driver implements [database.Driver] for PostgreSQL on pgx and pgxpool,
// scanning rows with [query.Targets]. Root operations acquire from the configured
// pool; drivers passed to [Driver.WithSession] and [Driver.WithTransaction] are
// callback-scoped handles using one retained physical connection. The root owns
// the pool and must be stopped after callbacks and cursors finish.
//
// Example:
//
//	driver, err := postgresdatabase.New(dsn)
//	if err != nil {
//		return err
//	}
//	db := database.New(driver)
type Driver struct {
	// db executes SQL on the root pool, retained connection, or transaction.
	db executor

	// pool is always the exact root pool, preserved for native access and
	// acquisition only at root entry points. Root Stop owns its lifecycle.
	pool *pgxpool.Pool

	// scope is nil on the root driver. Callback drivers retain a shared
	// physical session and a separate lifetime guard, never a fresh pool path.
	scope *scope
}

var _ database.Driver = (*Driver)(nil)

// New configures PostgreSQL and returns a [Driver].
//
// dsn is the PostgreSQL data source name, as a URL or in key=value form. It is
// required and has no default; a blank or unparsable dsn returns an error
// matching [ErrInvalidConfig], as does a negative pool limit. DSN parse messages
// omit supplied values while retaining the native error for errors.As. Policies edit
// [Builder] once in order before validation; supplied nil entries panic.
// Completed scalar settings detach from retained drafts. The pool connects lazily: no
// network I/O happens until [Driver.Start] or the first query. Nonzero native
// pool_min_conns and pool_min_idle_conns are rejected because pgxpool would
// connect during construction; pool_health_check_period must be positive. For
// eager minimum connections, create a native pool at application startup and
// transfer it through [NewFrom].
//
// Example:
//
//	driver, err := postgresdatabase.New(dsn, func(connection *postgresdatabase.Builder) {
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

	poolConfig, err := nativePoolConfig(dsn, config)

	if err != nil {
		return nil, err
	}

	// Both native minima were validated as zero; pgxpool's initial background
	// maintenance therefore cannot create connections before the first use.
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)

	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	return NewFrom(pool), nil
}

// NewFrom wraps a pgx pool the caller created, for example with
// [pgxpool.New]. The driver takes ownership: [Driver.Stop] closes the pool.
//
// There are no configuration callbacks: pgxpool fixes its limits when created,
// so set them in [pgxpool.Config] before transferring ownership.
//
// Example:
//
//	driver := postgresdatabase.NewFrom(pool)
//	return driver.Stop(ctx)
func NewFrom(pool *pgxpool.Pool) *Driver {
	return &Driver{
		db:   pool,
		pool: pool,
	}
}

// Dialect reports [query.Postgres] for roots and callback handles alike. Use it
// to compile queries with PostgreSQL placeholders and identifier quoting.
//
// Example:
//
//	statement, err := query.Raw("SELECT ?", 7).Compile(driver.Dialect())
//	if err != nil {
//		return err
//	}
//	_, err = driver.Exec(ctx, statement.SQL(), statement.Args()...)
func (driver *Driver) Dialect() query.Dialect {
	return query.Postgres
}

// Start verifies connectivity so a misconfigured database fails fast. The root
// pool connects lazily; a callback driver instead checks its retained connection
// and lifetime without acquiring another pool slot. It is equivalent to [Driver.Ping].
//
// Example:
//
//	return driver.Start(ctx)
func (driver *Driver) Start(ctx context.Context) error {
	return driver.Ping(ctx)
}

// Stop closes the connection pool. It waits until every connection in use has
// been returned, which requires the caller to have closed its cursors and
// finished its transactions; when ctx ends first, Stop returns ctx.Err() and
// the pool finishes closing in the background as those connections return.
// On a callback driver it only checks the scope and context; it never closes the
// shared pool. Expired callback handles report their session/transaction sentinel.
//
// Example:
//
//	return driver.Stop(ctx)
func (driver *Driver) Stop(ctx context.Context) error {
	if driver.scope != nil {
		return driver.check(ctx)
	}

	closed := make(chan struct{})

	go func() {
		driver.pool.Close()
		close(closed)
	}()

	select {
	case <-closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Ping verifies connectivity through the root pool or this callback's retained
// connection. A callback never uses a replacement connection after loss, and an
// expired callback reports its session or transaction sentinel before any I/O.
//
// Example:
//
//	return driver.Ping(ctx)
func (driver *Driver) Ping(ctx context.Context) error {
	if driver.scope != nil {
		_, err := driver.Exec(ctx, "SELECT 1")

		return err
	}

	return driver.pool.Ping(ctx)
}
