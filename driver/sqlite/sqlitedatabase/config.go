package sqlitedatabase

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Builder edits optional pool limits during New or NewFrom. Zero leaves native
// limits unchanged, except New's single-connection in-memory default. Completed
// settings are copied; retained drafts cannot change a driver. Callbacks run
// once in order; supplied nil callbacks panic, nil slice expansion omits edits.
//
// Example:
//
//	driver, err := sqlitedatabase.New("app.db", func(connection *sqlitedatabase.Builder) {
//		connection.MaxOpenConns(10)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
type Builder struct {
	// config is the draft's private scalar pool policy.
	config settings
}

// configureSettings applies policies directly once to one fresh draft.
// Its returned scalar settings are independent of retained builder mutations.
func configureSettings(configure []BuilderFunc) settings {
	connection := &Builder{}

	for _, configure := range configure {
		configure(connection)
	}

	return connection.config
}

// MaxOpenConns replaces the open connection limit and returns this draft.
// Zero keeps native defaults; New rejects negative limits.
//
// Example:
//
//	driver, err := sqlitedatabase.New("app.db", func(connection *sqlitedatabase.Builder) {
//		connection.MaxOpenConns(10)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (connection *Builder) MaxOpenConns(value int) *Builder {
	connection.config.MaxOpenConns = value

	return connection
}

// MaxIdleConns replaces the idle connection limit and returns this draft.
// Only positive values apply to supplied pools; New rejects negative limits.
//
// Example:
//
//	driver, err := sqlitedatabase.New("app.db", func(connection *sqlitedatabase.Builder) {
//		connection.MaxIdleConns(2)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (connection *Builder) MaxIdleConns(value int) *Builder {
	connection.config.MaxIdleConns = value

	return connection
}

// ConnMaxLifetime replaces the connection age limit and returns this draft.
// Zero preserves the native limit; New rejects negative durations.
//
// Example:
//
//	driver, err := sqlitedatabase.New("app.db", func(connection *sqlitedatabase.Builder) {
//		connection.ConnMaxLifetime(time.Hour)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (connection *Builder) ConnMaxLifetime(value time.Duration) *Builder {
	connection.config.ConnMaxLifetime = value

	return connection
}

// ConnMaxIdleTime replaces the idle connection age and returns this draft.
// Only positive values apply to supplied pools; New rejects negative durations.
//
// Example:
//
//	driver, err := sqlitedatabase.New("app.db", func(connection *sqlitedatabase.Builder) {
//		connection.ConnMaxIdleTime(time.Minute)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (connection *Builder) ConnMaxIdleTime(value time.Duration) *Builder {
	connection.config.ConnMaxIdleTime = value

	return connection
}

// BuilderFunc edits a fresh construction draft supplied to New or NewFrom.
// Policies run synchronously once in order; nil entries panic, and retaining the
// draft does not let a caller change completed driver settings.
//
// Example:
//
//	policy := sqlitedatabase.BuilderFunc(func(connection *sqlitedatabase.Builder) {
//		connection.MaxOpenConns(1)
//	})
//	driver, err := sqlitedatabase.New(":memory:", policy)
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
type BuilderFunc = func(*Builder)

// settings holds the optional SQLite pool settings. The required DSN is a
// positional argument of [New], not a field here.
type settings struct {
	// MaxOpenConns limits open connections when positive. The default is zero, which
	// leaves the database/sql setting unconfigured.
	MaxOpenConns int

	// MaxIdleConns limits idle connections when positive. The default is zero, which
	// leaves the database/sql setting unconfigured.
	MaxIdleConns int

	// ConnMaxLifetime limits connection reuse when positive. The default is zero, which
	// leaves it unconfigured.
	ConnMaxLifetime time.Duration

	// ConnMaxIdleTime limits connection idle time when positive. The default is zero, which
	// leaves it unconfigured.
	ConnMaxIdleTime time.Duration
}

// memoryDSN recognizes memory databases using native filename and URI parameter
// boundaries, never substrings in ordinary paths. Malformed URI escapes return
// false here; native opening remains responsible for reporting DSN errors.
func memoryDSN(dsn string) bool {
	name, parameters, _ := strings.Cut(dsn, "?")

	if name == ":memory:" {
		return true
	}

	if !strings.HasPrefix(name, "file:") {
		return false
	}

	name, err := url.PathUnescape(strings.TrimPrefix(name, "file:"))

	if err != nil {
		return false
	}

	if name == ":memory:" {
		return true
	}

	values, err := url.ParseQuery(parameters)

	return err == nil && values.Get("mode") == "memory"
}

// validate rejects a blank dsn and negative pool limits.
func validate(dsn string, config settings) error {
	if strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("%w: data source name cannot be blank", ErrInvalidConfig)
	}

	if min(config.MaxOpenConns, config.MaxIdleConns) < 0 {
		return fmt.Errorf("%w: pool limits cannot be negative", ErrInvalidConfig)
	}

	if min(config.ConnMaxLifetime, config.ConnMaxIdleTime) < 0 {
		return fmt.Errorf("%w: pool limits cannot be negative", ErrInvalidConfig)
	}

	return nil
}

// newFrom applies completed settings without replaying caller policies.
// It takes ownership of pool and does not alter its DSN or memory policy.
func newFrom(pool *sql.DB, config settings) *Driver {
	if config.MaxOpenConns > 0 {
		pool.SetMaxOpenConns(config.MaxOpenConns)
	}

	if config.MaxIdleConns > 0 {
		pool.SetMaxIdleConns(config.MaxIdleConns)
	}

	if config.ConnMaxLifetime > 0 {
		pool.SetConnMaxLifetime(config.ConnMaxLifetime)
	}

	if config.ConnMaxIdleTime > 0 {
		pool.SetConnMaxIdleTime(config.ConnMaxIdleTime)
	}

	return &Driver{
		db:   pool,
		pool: pool,
	}
}

// withTimeFormat appends _time_format=sqlite to dsn unless it already sets
// _time_format. modernc.org/sqlite reads the parameter after the first "?",
// for plain paths, ":memory:", and "file:" URIs alike, and ignores it when
// opening the file. A blank dsn is returned unchanged so [New] reports it.
func withTimeFormat(dsn string) string {
	if dsn == "" {
		return dsn
	}

	_, parameters, found := strings.Cut(dsn, "?")

	for parameter := range strings.SplitSeq(parameters, "&") {
		key, _, assigned := strings.Cut(parameter, "=")

		if key == "_time_format" && assigned {
			return dsn
		}
	}

	if found {
		return dsn + "&_time_format=sqlite"
	}

	return dsn + "?_time_format=sqlite"
}
