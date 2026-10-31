package migrate

import (
	"cmp"
	"log/slog"
	"time"

	"arkoren.dev/core/event"
)

// OutOfOrder says what [Migrator.Up] does with a pending migration that was
// added before a migration of the same group that already ran.
// Empty construction input selects OutOfOrderWarn. The string values are
// stable configuration labels, not an ordered set. Other nonempty labels are
// invalid; Up, Rollback, and Status reject them before acquiring a session.
//
//	Example: _ = migrate.New(db, func(migrator *migrate.Builder) {
//		migrator.OutOfOrder(migrate.OutOfOrderError)
//	})
type OutOfOrder string

const (
	// OutOfOrderWarn logs a warning and runs the migration. It is the default.
	//
	// Example: _ = migrate.New(db, func(migrator *migrate.Builder) {
	//	migrator.OutOfOrder(migrate.OutOfOrderWarn)
	// })
	OutOfOrderWarn OutOfOrder = "warn"

	// OutOfOrderAllow runs the migration without a warning.
	//
	// Example: _ = migrate.New(db, func(migrator *migrate.Builder) {
	//	migrator.OutOfOrder(migrate.OutOfOrderAllow)
	// })
	OutOfOrderAllow OutOfOrder = "allow"

	// OutOfOrderError fails with [ErrOutOfOrder] before any migration runs.
	//
	// Example: _ = migrate.New(db, func(migrator *migrate.Builder) {
	//	migrator.OutOfOrder(migrate.OutOfOrderError)
	// })
	OutOfOrderError OutOfOrder = "error"
)

const (
	// defaultTable is the default bookkeeping table.
	defaultTable = "arkoren_migrations"

	// defaultLockName is the default lock name.
	defaultLockName = "arkoren_migrations"

	// defaultLockTimeout is the default time to wait for the lock.
	defaultLockTimeout = 5 * time.Minute
)

// settings configures a [Migrator].
type settings struct {
	// Logger receives migration logs. A nil value uses [slog.Default].
	Logger *slog.Logger

	// Table is the bookkeeping table. An empty value uses "arkoren_migrations".
	Table string

	// LockName is the lock that serializes migrators. An empty value uses
	// "arkoren_migrations".
	LockName string

	// LockTimeout bounds session/pool acquisition and lock contention for
	// [Migrator.Up], [Migrator.Rollback], and [Migrator.Status]. The timer stops
	// before protected preparation or migration work; caller cancellation still
	// applies throughout. Zero uses five minutes and a negative value waits
	// until the caller context ends. Expiry wraps both [ErrLockTimeout] and
	// [context.DeadlineExceeded]; caller cancellation is not a lock timeout.
	LockTimeout time.Duration

	// OutOfOrder sets how out-of-order pending migrations are handled. The
	// empty value selects [OutOfOrderWarn].
	OutOfOrder OutOfOrder

	// Events receives [Applied], [RolledBack], and [Failed]. Nil, the
	// default, publishes nothing.
	Events *event.Bus
}

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	migrate.New(database, func(migrator *migrate.Builder) {
//		migrator.Table("application_migrations")
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...BuilderFunc) settings {
	migrator := &Builder{}

	for _, configure := range configure {
		configure(migrator)
	}

	config := migrator.settings

	config.Logger = cmp.Or(config.Logger, slog.Default())
	config.Table = cmp.Or(config.Table, defaultTable)
	config.LockName = cmp.Or(config.LockName, defaultLockName)
	config.LockTimeout = cmp.Or(config.LockTimeout, defaultLockTimeout)
	config.OutOfOrder = cmp.Or(config.OutOfOrder, OutOfOrderWarn)

	return config
}

// Logger selects the shared migration log destination. Nil resolves
// slog.Default after all construction callbacks; no logs are emitted here.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	migrate.New(database, func(migrator *migrate.Builder) {
//		migrator.Logger(nil)
//	})
func (migrator *Builder) Logger(logger *slog.Logger) *Builder {
	migrator.settings.Logger = logger

	return migrator
}

// Events selects the shared bus receiving Applied, RolledBack and Failed after
// each migration outcome. Nil publishes nothing; listeners cannot change the
// outcome or take ownership of migration execution.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	migrate.New(database, func(migrator *migrate.Builder) {
//		migrator.Events(nil)
//	})
func (migrator *Builder) Events(bus *event.Bus) *Builder {
	migrator.settings.Events = bus

	return migrator
}

// Table selects the bookkeeping table. Empty resolves to arkoren_migrations
// after all callbacks. Construction neither creates nor inspects that table;
// the existing migration execution path owns bookkeeping I/O.
// On MySQL, an unqualified name is resolved against the retained session's
// current database before steps; qualified names preserve their components.
// The operation then uses that frozen target for metadata and all history SQL.
// Existing MySQL storage must pass the package's binary-ledger verification;
// Table does not authorize conversion or alter an existing ledger.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	migrate.New(database, func(migrator *migrate.Builder) {
//		migrator.Table("application_migrations")
//	})
func (migrator *Builder) Table(table string) *Builder {
	migrator.settings.Table = table

	return migrator
}

// LockName selects the database lock that serializes migration work. Empty
// resolves to arkoren_migrations after all callbacks. Construction acquires no
// lock and does not change session ownership or release timing.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	migrate.New(database, func(migrator *migrate.Builder) {
//		migrator.LockName("application_migrations")
//	})
func (migrator *Builder) LockName(name string) *Builder {
	migrator.settings.LockName = name

	return migrator
}

// LockTimeout sets the combined session-acquisition and lock-wait budget.
// Once the lock is acquired the budget no longer limits preparation or work;
// the caller's context still does. Zero uses five minutes, and a negative
// duration disables this budget without disabling caller cancellation.
//
// Example:
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	migrate.New(database, func(migrator *migrate.Builder) {
//		migrator.LockTimeout(time.Minute)
//	})
func (migrator *Builder) LockTimeout(timeout time.Duration) *Builder {
	migrator.settings.LockTimeout = timeout

	return migrator
}

// OutOfOrder selects how Up handles pending migrations older than completed
// migrations in the same group. The zero value warns and runs them; allow and
// error modes retain their existing execution-time behavior.
// Unknown nonempty labels remain on the draft but Up, Rollback, and Status
// reject them with ErrInvalidMigration before session acquisition or SQL.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	migrate.New(database, func(migrator *migrate.Builder) {
//		migrator.OutOfOrder(migrate.OutOfOrderError)
//	})
func (migrator *Builder) OutOfOrder(mode OutOfOrder) *Builder {
	migrator.settings.OutOfOrder = mode

	return migrator
}

// BuilderFunc configures a Builder.
type BuilderFunc = func(*Builder)

// MigrationBuilderFunc configures a MigrationBuilder.
type MigrationBuilderFunc = func(*MigrationBuilder)
