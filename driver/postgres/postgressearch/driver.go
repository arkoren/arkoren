package postgressearch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/search"
	"arkoren.dev/foundation/query"
)

// ErrInvalidConfig indicates that the arguments or instance setting given to
// [NewFrom] cannot establish a driver: no database, a database that is not
// PostgreSQL, or an invalid instance name.
//
// Example:
//
//	_, err := postgressearch.NewFrom(nil)
//	if errors.Is(err, postgressearch.ErrInvalidConfig) {
//		return err // Supply the application's PostgreSQL database.
//	}
var ErrInvalidConfig = errors.New("invalid PostgreSQL search configuration")

// maxInstanceLength is the longest instance name in bytes. With the longest
// table suffix and index suffix it keeps every generated identifier under
// the 63-byte PostgreSQL limit.
const maxInstanceLength = 40

// Driver implements [search.Driver] on PostgreSQL full-text search. Create it
// with [NewFrom]. It is safe for concurrent use and borrows the database facade;
// the application owns its pool and executes Migrations before startup.
//
// Example:
//
//	driver, err := postgressearch.NewFrom(db)
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx) // After application-owned migrations have run.
type Driver struct {
	// db runs every statement. The caller owns it.
	db *database.Database

	// instance is the validated instance name, "" for the default.
	instance string

	// documents is the name of the documents table, with the instance prefix.
	documents string

	// indexes is the name of the index definitions table, with the instance
	// prefix.
	indexes string
}

var _ search.Driver = (*Driver)(nil)

// NewFrom creates a driver that stores documents through db, which must be
// backed by PostgreSQL (its dialect is [query.Postgres]). It does no I/O: the
// tables are created by the migrations of [Driver.Migrations], which the
// application runs, and [Driver.Start] or [Driver.Ping] verify them.
//
// Only [Builder.Instance] applies. The application owns db, so [Driver.Stop] does
// not close anything. A nil db, another dialect, or an invalid instance name
// returns an error wrapping [ErrInvalidConfig].
// Policies run once in order on one fresh draft before validation; nil entries
// panic, expanded nil/empty slices omit edits, and retained drafts are detached.
// Generated SQL identifiers are quoted, so digit-leading instance names retain
// the same table, index, advisory-lock, and migration identities as other names.
//
// Example:
//
//	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
//		engine.Instance("catalog")
//	})
//	if err != nil {
//		return err
//	}
//	engine := search.New(driver, func(engine *search.Builder) {
//		engine.Indexes(Products)
//	})
func NewFrom(db *database.Database, configure ...BuilderFunc) (*Driver, error) {
	engine := &Builder{}

	for _, configure := range configure {
		configure(engine)
	}

	config := engine.config

	if db == nil {
		return nil, fmt.Errorf("%w: database is nil", ErrInvalidConfig)
	}

	if db.Dialect() != query.Postgres {
		return nil, fmt.Errorf("%w: the database must use the PostgreSQL dialect", ErrInvalidConfig)
	}

	prefix, err := tablePrefix(config.instance)

	if err != nil {
		return nil, err
	}

	return &Driver{
		db:        db,
		instance:  config.instance,
		documents: prefix + "search_documents",
		indexes:   prefix + "search_indexes",
	}, nil
}

// tablePrefix validates an instance identity and returns its table qualifier.
// Empty selects unqualified tables; invalid names keep ErrInvalidConfig context.
func tablePrefix(instance string) (string, error) {
	if len(instance) > maxInstanceLength ||
		strings.Trim(instance, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return "", fmt.Errorf("%w: instance %q must be at most %d bytes of [a-z0-9_]",
			ErrInvalidConfig, instance, maxInstanceLength)
	}

	if instance == "" {
		return "", nil
	}

	return instance + "_", nil
}

// Start verifies the connection and that both tables exist, so a deployment
// that skipped the migrations fails fast. It is [Driver.Ping].
// Startup neither synchronizes logical indexes nor creates missing tables.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	return driver.Ping(ctx)
}

// Stop does nothing and returns nil: the application owns the database and
// stops its driver itself. It ignores ctx and waits for no background work;
// finish application operations before stopping the database owner.
//
// Example:
//
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Stop(context.Context) error {
	return nil
}

// Ping verifies the connection and that both tables exist, read-only. When a
// table is missing it returns an error wrapping [search.ErrNotFound] that
// names [Driver.Migrations]. Use it as a readiness check.
// It resolves table names as single quoted identifiers, including numeric-prefix
// instances, and preserves database execution/cancellation errors in the chain.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	missing, err := driver.db.Select[string](ctx, query.Raw(
		"SELECT name FROM (VALUES (?::text), (?::text)) AS tables(name) WHERE to_regclass(quote_ident(name)) IS NULL ORDER BY name",
		driver.documents, driver.indexes))

	if err != nil {
		return fmt.Errorf("postgres search ping: %w", err)
	}

	if len(missing) > 0 {
		return fmt.Errorf("postgres search: %w: table %s does not exist; add and run Driver.Migrations() with the migrator",
			search.ErrNotFound, strings.Join(missing, ", "))
	}

	return nil
}
