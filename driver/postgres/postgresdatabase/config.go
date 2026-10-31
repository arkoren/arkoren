package postgresdatabase

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Builder edits the pool settings used by New. NewFrom has no callbacks because
// pgxpool limits are fixed when a pool is created. Completed scalar settings
// detach from retained drafts. Callbacks run directly once in order; nil entries
// panic, while expanded nil or empty slices omit edits.
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
type Builder struct {
	// config holds this draft's private scalar pool policy.
	config settings
}

// configureSettings applies callbacks to one fresh draft before validation.
// The returned settings do not share mutable state with a retained draft.
func configureSettings(configure []BuilderFunc) settings {
	connection := &Builder{}

	for _, configure := range configure {
		configure(connection)
	}

	return connection.config
}

// MaxOpenConns replaces pgxpool's MaxConns and returns this draft. Zero keeps
// the native default; New rejects negatives and clamps positives to MaxInt32.
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
func (connection *Builder) MaxOpenConns(value int) *Builder {
	connection.config.MaxOpenConns = value

	return connection
}

// MaxIdleConns records a symmetry-only setting and returns this draft. pgxpool
// has no idle-count limit; use ConnMaxIdleTime. New still rejects negatives.
//
// Example:
//
//	driver, err := postgresdatabase.New(dsn, func(connection *postgresdatabase.Builder) {
//		connection.MaxIdleConns(0)
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func (connection *Builder) MaxIdleConns(value int) *Builder {
	connection.config.MaxIdleConns = value

	return connection
}

// ConnMaxLifetime replaces pgxpool's maximum connection age and returns this
// draft. Zero preserves the native default; New rejects negative durations.
//
// Example:
//
//	driver, err := postgresdatabase.New(dsn, func(connection *postgresdatabase.Builder) {
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

// ConnMaxIdleTime replaces pgxpool's idle age limit and returns this draft.
// Zero keeps the native default; New rejects negative durations.
//
// Example:
//
//	driver, err := postgresdatabase.New(dsn, func(connection *postgresdatabase.Builder) {
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

// BuilderFunc edits a fresh construction draft supplied to New. Policies run
// synchronously once in order; nil entries panic, and retaining the draft does
// not let a caller change completed driver settings.
//
// Example:
//
//	policy := postgresdatabase.BuilderFunc(func(connection *postgresdatabase.Builder) {
//		connection.MaxOpenConns(10)
//	})
//	driver, err := postgresdatabase.New(dsn, policy)
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
type BuilderFunc = func(*Builder)

// invalidDSNError hides native parse text, whose best-effort password redaction
// cannot cover every malformed DSN. The unmodified cause remains discoverable
// for explicit inspection, but its own message is not safe diagnostic output.
type invalidDSNError struct {
	// cause retains the native parser error and its original DSN privately.
	cause error
}

// Error returns a fixed diagnostic rather than copying native DSN fragments.
// Even a malformed password or an accidentally secret option stays out of it.
func (*invalidDSNError) Error() string {
	return ErrInvalidConfig.Error() + ": invalid data source name"
}

// Unwrap preserves the native cause for errors.Is and errors.As inspection.
// Callers must not log unwrapped native messages as if they were redacted.
func (err *invalidDSNError) Unwrap() error {
	return err.cause
}

// Is associates the safe diagnostic with the package configuration sentinel.
// Native error matching continues through Unwrap rather than being replaced.
func (*invalidDSNError) Is(target error) bool {
	return target == ErrInvalidConfig
}

// settings holds the optional PostgreSQL pool settings. The required DSN is a
// positional argument of [New], not a field here.
type settings struct {
	// MaxOpenConns limits open connections when positive; it is the pgxpool
	// MaxConns setting. The default is zero, which keeps the pgxpool default
	// of the larger of four and the number of CPUs.
	MaxOpenConns int

	// MaxIdleConns is accepted for symmetry with the other SQL drivers and
	// has no effect: pgxpool has no idle limit, it closes connections idle for
	// longer than ConnMaxIdleTime instead. The default is zero.
	MaxIdleConns int

	// ConnMaxLifetime limits connection reuse when positive; it is the pgxpool
	// MaxConnLifetime setting. The default is zero, which keeps the pgxpool
	// default of one hour.
	ConnMaxLifetime time.Duration

	// ConnMaxIdleTime limits connection idle time when positive; it is the
	// pgxpool MaxConnIdleTime setting. The default is zero, which keeps the
	// pgxpool default of thirty minutes.
	ConnMaxIdleTime time.Duration
}

// validate rejects missing DSNs and negative limits before native parsing.
// NewFrom deliberately does not validate or modify an already-created pool.
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

// nativePoolConfig parses the DSN and overlays positive completed settings on
// pgx defaults. Eager minima are rejected rather than silently disabled, and the
// maintenance period must support time.NewTicker. MaxIdleConns has no equivalent.
func nativePoolConfig(dsn string, config settings) (*pgxpool.Config, error) {
	poolConfig, err := pgxpool.ParseConfig(dsn)

	if err != nil {
		return nil, &invalidDSNError{cause: err}
	}

	if poolConfig.MinConns != 0 || poolConfig.MinIdleConns != 0 {
		return nil, fmt.Errorf("%w: nonzero pool minima require a startup-created pool and NewFrom", ErrInvalidConfig)
	}

	if poolConfig.HealthCheckPeriod <= 0 {
		return nil, fmt.Errorf("%w: pool_health_check_period must be positive", ErrInvalidConfig)
	}

	if config.MaxOpenConns > 0 {
		poolConfig.MaxConns = int32(min(config.MaxOpenConns, math.MaxInt32))
	}

	if config.ConnMaxLifetime > 0 {
		poolConfig.MaxConnLifetime = config.ConnMaxLifetime
	}

	if config.ConnMaxIdleTime > 0 {
		poolConfig.MaxConnIdleTime = config.ConnMaxIdleTime
	}

	return poolConfig, nil
}
