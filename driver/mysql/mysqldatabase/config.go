package mysqldatabase

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Builder edits optional pool limits during New or NewFrom. Its zero value
// leaves native limits unchanged. Constructors copy completed settings, so
// retaining this draft cannot change the published driver. Callbacks run once
// in order; supplied nil callbacks panic, while nil slice expansion omits edits.
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
type Builder struct {
	// config is the draft's private scalar pool policy.
	config settings
}

// configureSettings applies policies directly to one fresh draft and returns
// an independent value. Application panics and nil callback misuse propagate.
func configureSettings(configure []BuilderFunc) settings {
	connection := &Builder{}

	for _, configure := range configure {
		configure(connection)
	}

	return connection.config
}

// MaxOpenConns replaces the open connection limit and returns this draft.
// Zero leaves the native limit unchanged; New rejects negative limits.
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
func (connection *Builder) MaxOpenConns(value int) *Builder {
	connection.config.MaxOpenConns = value

	return connection
}

// MaxIdleConns replaces the idle connection limit and returns this draft.
// Only positive values apply to a supplied pool; New rejects negative values.
//
// Example:
//
//	driver, err := mysqldatabase.New(dsn, func(connection *mysqldatabase.Builder) {
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

// ConnMaxLifetime replaces the maximum connection age and returns this draft.
// Zero preserves the native limit; New rejects negative durations.
//
// Example:
//
//	driver, err := mysqldatabase.New(dsn, func(connection *mysqldatabase.Builder) {
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
// Only positive values apply to a supplied pool; New rejects negative values.
//
// Example:
//
//	driver, err := mysqldatabase.New(dsn, func(connection *mysqldatabase.Builder) {
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
//	policy := mysqldatabase.BuilderFunc(func(connection *mysqldatabase.Builder) {
//		connection.MaxOpenConns(10)
//	})
//	driver, err := mysqldatabase.New(dsn, policy)
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
type BuilderFunc = func(*Builder)

// invalidDSNError gives native parse failures a credential-safe message while
// preserving their original cause for deliberate errors.Is/errors.As inspection.
// Unwrapped native errors are not suitable for ordinary diagnostic output.
type invalidDSNError struct {
	// cause is the native parser failure, which can contain supplied DSN values.
	cause error
}

// Error omits native error text because even an invalid bool or duration value
// can contain credentials accidentally placed in the wrong DSN parameter.
func (*invalidDSNError) Error() string {
	return ErrInvalidConfig.Error() + ": invalid data source name"
}

// Unwrap preserves native error identity without including its text in Error.
// Callers inspecting it explicitly must not treat its message as redacted.
func (err *invalidDSNError) Unwrap() error {
	return err.cause
}

// Is adds the package configuration sentinel without dropping the native cause.
// This keeps New's configuration failures distinguishable from runtime failures.
func (*invalidDSNError) Is(target error) bool {
	return target == ErrInvalidConfig
}

// settings holds the optional MySQL pool settings. The required DSN is a
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

// newFrom applies completed pool settings without replaying caller policies.
// It takes ownership of pool; only positive settings replace native limits.
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

// withParseTime adds the timestamp default only when the native parameter list
// omits parseTime. Like the native parser, it finds the database after the final
// slash and parameters after that database's first question mark. Credentials
// and Unix socket addresses may contain either delimiter and are left intact.
func withParseTime(dsn string) string {
	_, parameters, found := strings.Cut(dsn[strings.LastIndexByte(dsn, '/')+1:], "?")

	for parameter := range strings.SplitSeq(parameters, "&") {
		key, _, assigned := strings.Cut(parameter, "=")

		if key == "parseTime" && assigned {
			return dsn
		}
	}

	if found {
		return dsn + "&parseTime=true"
	}

	return dsn + "?parseTime=true"
}
