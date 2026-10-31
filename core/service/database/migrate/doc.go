// Package migrate applies ordered schema migrations to a [database.Database]
// and records them in a bookkeeping table.
//
//	migrator := migrate.New(db, func(migrator *migrate.Builder) {
//		migrator.OutOfOrder(migrate.OutOfOrderError)
//	})
//
//	err := migrator.Add(
//		migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
//			migration.Up(migrate.Schema(schema.Create("users", func(table *schema.Table) {
//				table.ID()
//				table.String("email", 255).Unique()
//			})))
//			migration.Down(migrate.Schema(schema.Drop("users")))
//		}),
//	)
//	if err != nil {
//		return err
//	}
//	if err := migrator.Up(ctx); err != nil {
//		return err
//	}
//
// To revert separately, migrator.Rollback(ctx, migrate.Steps(1)) reverts the
// most recently applied migration. Check its error before continuing.
//
// # Order and ids
//
// Add order is run order, and the list is append-only: add new migrations
// after existing ones and never reorder or remove them. An id is stable. It is
// the key in the bookkeeping table, so renaming a migration makes it pending
// again and it runs a second time, while the old id shows up as
// [Status.Missing]. Out-of-order detection applies within each group; see
// Packaging.
// Rollback uses newest batch first and reverse Add order within each batch,
// not wall-clock timestamps. Preserve the relative Add order of applied ids.
// Unknown ids in a target batch block rollback before known ids in that batch;
// restore their definitions rather than guessing their original order.
//
// Id matching in Go is byte-exact. MySQL uses VARBINARY(191) NOT NULL with a
// full single-column primary key for new ledgers and verifies actual metadata
// on every operation before user steps or history mutations. Unsupported
// layouts fail with ErrInvalidMigration; unreadable/hidden metadata fails
// closed without assuming that conversion is needed. Existing ledgers are
// never automatically altered. See MySQL ledger compatibility below.
//
// Because order comes from Add order, an id needs no number or date prefix.
// Name a migration for the change it makes, in lowercase words joined by
// hyphens: "create-users", "add-users-email-index", "backfill-users-email".
// Ids are unique across every group. A package writes short ids and its
// [MigrationGroup] prefixes them with the group name and a slash, so
// "create-users" in group "acme.users" is stored as "acme.users/create-users";
// see Packaging.
//
// # Transactions
//
// By default a migration runs in a transaction together with its bookkeeping
// row, when the dialect rolls back DDL ([query.Dialect.TransactionalDDL]):
// PostgreSQL and SQLite. MySQL implicitly commits around DDL, so migrations
// run there without a transaction. A migration marked
// [MigrationBuilder.WithoutTransaction] never starts one. Such a migration must be
// idempotent: a failure can leave it partly applied and unrecorded, and the
// next [Migrator.Up] runs it again.
//
// A step receives the migrator database and a context that carries the retained
// session and, when applicable, its transaction. Its statements join both.
// Calling [database.Database.WithTransaction] inside a transactional migration
// creates a nested transaction, which fails with
// [database.ErrNestedTransaction] when the driver cannot nest.
// There is no operation-wide transaction: earlier migration commits survive a
// later failure. Run deployments with an ordinary context. If ctx already
// carries a transaction, the migrator joins it instead, so that outermost
// transaction controls final commit and the migration lock's release. Even
// WithoutTransaction cannot escape a transaction the caller already started.
//
// # Dialects
//
// A migration runs on every dialect unless [MigrationBuilder.Only] restricts it.
// Use Only for a shipped migration that is meant for one dialect, such as a
// PostgreSQL-only cache table:
//
//	migrate.NewMigration("create-cache", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE UNLOGGED TABLE cache (id INTEGER)")))
//		migration.Down(migrate.Schema(schema.Drop("cache")))
//		migration.Only(query.Postgres)
//	})
//
// [Migrator.Add] rejects such a migration with [ErrInvalidMigration] when the
// database uses another dialect, before any SQL runs. It is never skipped
// silently. A migration whose logic differs per dialect should stay
// unrestricted and switch on [database.Database.Dialect] inside its [Func].
//
// # MySQL ledger compatibility
//
// Up, Rollback, and Status use target-addressed SHOW FULL COLUMNS and SHOW INDEX
// under the retained session and lock. Character columns (including _bin and
// NO PAD collations), fixed BINARY, nullable/generated columns, and composite
// or prefix primary keys are not canonical ID storage. Additional unique
// indexes are rejected rather than allowing narrower
// prefix/expression equality to collide after user steps. SQL inspection failures
// preserve their cause; unavailable metadata does not trigger a repair attempt.
// No per-migration event is emitted for work that never started.
//
// A configured MySQL table is frozen to an actual schema/table before steps:
// unqualified names use the current session database; qualified names retain
// their components. Subsequent USE statements cannot redirect bookkeeping.
// ID insert/delete arguments are the original UTF-8 bytes, not character
// strings subject to connection conversion. Public ID and group values stay
// unchanged. Non-MySQL preparation and bindings retain their existing behavior.
//
// An operator must explicitly authorize conversion of a legacy ledger. First
// quiesce all migrators/writers and conflicting DDL, back up the schema and
// every (HEX(id), batch, ran_at) tuple, and prove the stored bytes match the
// intended UTF-8 identities within 191 bytes without truncation. Non-UTF-8
// storage can require an owner decision: physical bytes and application
// identity cannot both be preserved by blindly changing a charset. Do not
// normalize, trim, rename, or discard history. Inspect dependent constraints,
// triggers, generated values, and column attributes; use strict SQL mode and
// verify all history tuples and canonical metadata after the approved change.
// Reconcile earlier failed nontransactional steps before allowing replay:
// those changes might already be final. The library never runs ALTER for this
// conversion and the migration role needs no new ALTER privilege. Its lock
// coordinates cooperating migrators, not arbitrary administrator DDL.
//
// # Locking
//
// [Migrator.Up], [Migrator.Rollback], and [Migrator.Status] hold the
// database lock named by [Builder.LockName] while they work. Each attempt enters
// [database.Database.WithSession] and tries the lock once. A refused attempt
// ends that scope before waiting to retry, releasing newly acquired pool
// capacity. A successful attempt retains one physical connection for the lock,
// table preparation, history, planning, all migration transactions and SQL, and
// bookkeeping. A lost session fails closed: the operation never reconnects or
// replays its plan through another pool connection. The guarantee depends on
// the dialect:
//
//   - PostgreSQL: session advisory locks span every process and instance that
//     uses the same database. A crashed holder loses the lock when its
//     connection drops. Safe to run on many instances.
//   - MySQL: GET_LOCK spans every process and instance that uses the same
//     server, and ends with the session. Safe to run on many instances, but
//     see Transactions: MySQL DDL is not transactional.
//   - SQLite: an in-process lock covers all drivers in one process. For a
//     file-backed database on Linux, macOS, and the BSDs, an flock on a
//     sibling "<dbfile>.<hash>.lock" file also covers other processes on the
//     same host. In-memory databases, other operating systems, and database
//     files on network file systems get no cross-process lock. SQLite is not
//     a horizontal-scaling database.
//
// PgBouncer in transaction pooling mode breaks PostgreSQL session advisory
// locks: the lock and the unlock can run on different server connections, so
// the lock is not held, or never released. Run migrations over a direct
// connection or a session-pooled one.
//
// Steps must pass their supplied context to every facade call, not use a fresh
// context or the raw [database.Database.Driver]. Those paths bypass the pinned
// session and can wait forever for another connection in a one-connection pool.
// Close streaming cursors before subsequent statements or transaction boundaries.
// Managed named locks are not reentrant: never acquire the migration's lock
// separately and then call the migrator. Other root-level held locks also reserve
// pool connections and can prevent session acquisition when the pool is full.
//
// An existing session or transaction is borrowed, not released between failed
// attempts; its owner still reserves its connection. Prefer an ordinary context
// for deployments so contention can release all newly acquired capacity.
//
// # Lock timeout
//
// [Builder.LockTimeout] bounds session/pool acquisition and lock contention. Its
// timer stops on acquisition, before table preparation, history, or migrations.
// Caller cancellation still applies throughout the work. Zero uses five minutes
// and a negative value waits until the context ends. When acquisition times out,
// the error wraps both
// [ErrLockTimeout] and [context.DeadlineExceeded]. If the caller's own
// context ends first, the error wraps that context's error instead.
// Unlock uses a bounded cleanup context independent of caller cancellation.
// Cleanup errors are joined with the original error; a failed session is never
// retried by the operation. A callback panic is re-raised after cleanup.
//
// # Idempotent migrations
//
// A migration that runs without a transaction, through
// [MigrationBuilder.WithoutTransaction] or on MySQL, can stop half way and run
// again on the next [Migrator.Up]. Write each step so a second run is safe:
// prefer [schema.CreateIfNotExists] and [schema.DropIfExists].
//
//	migrate.NewMigration("create-audit", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Schema(
//			schema.CreateIfNotExists("audit", func(table *schema.Table) {
//				table.ID()
//				table.String("action", 64)
//			}),
//		))
//		migration.Down(migrate.Schema(schema.DropIfExists("audit")))
//		migration.WithoutTransaction()
//	})
//
// # Rolling deploys
//
// During a rolling deploy, old and new code run against the same schema.
// Use expand and contract: first release a migration that only adds
// (tables, nullable columns, indexes) and keeps old code working; deploy the
// code that uses the new schema; and in a later release remove what nothing
// uses any more. Never rename or drop in the same release that stops using
// the old name.
//
// In production, run migrations once per deploy as a Job, an init container,
// or a dedicated command that exits when done, before the new instances take
// traffic. Registering [Migrator.Up] with Composer.Startup makes every instance
// compete for the lock, delays startup, and ties migration failures to
// instance health. The lock keeps that safe, but it is best for development
// and single-instance setups.
//
// # Packaging
//
// Nothing registers itself. A package that needs tables exports
// a function that returns its definitions, and the application adds them in
// the order it chooses:
//
//	func Migrations() []migrate.Definition {
//		return migrate.NewGroup("acme.users").Migrations(
//			migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
//				migration.Up(migrate.Exec(query.Raw("CREATE TABLE users (id INTEGER)")))
//				migration.Down(migrate.Schema(schema.Drop("users")))
//			}),
//		)
//	}
//
//	err := migrator.Add(users.Migrations()...)
//
// A package wraps its definitions in a [NewGroup] and gives them short ids.
// [MigrationGroup.Migrations] prefixes each id with the group name and a slash,
// so the stored id, [Definition.ID], [Status.ID], and [MigrationEvent.ID] are
// "acme.users/create-users". A short id may not contain a slash, which also
// rejects ids a package prefixed by hand, and the combined id must fit in 191
// bytes; both are [ErrInvalidMigration] at [Migrator.Add]. Ungrouped ids may
// contain slashes. Any valid group name works. Arkoren's own packages name
// groups "arkoren.dev:<module>:<package>", plus ":<instance>" when a driver
// can run several instances in one database; that is a repository
// convention, not a rule of this package.
//
// A group is its own append-only sequence: out-of-order
// detection compares a migration only with migrations of the same group, so
// a package release that appends a migration runs it on the next
// [Migrator.Up] without an out-of-order warning or [ErrOutOfOrder], wherever
// the application added the package's definitions. Migrations without a
// group, usually the application's own, form the default group. Add order
// stays run order across groups: add a package's migrations before
// application migrations that refer to its tables.
//
// # Validation
//
// [NewMigration] invokes its callbacks directly once, synchronously in order on
// one [MigrationBuilder]. Settings return that draft. Nil callbacks panic and
// application panics propagate; expanded nil or empty slices supply no edits.
// Callbacks still run when domain validation fails. The published [Definition]
// detaches owned step and dialect slices, so retaining a draft cannot change it.
// Step functions and their closure captures are not copied or run at construction.
// Exec and Schema do copy their own statement and operation slices immediately;
// replacing an entry in the caller's slice cannot replace a published step.
//
// The draft retains the first domain error. Required Up is checked after all
// callbacks; omission is invalid even when no callbacks were supplied.
// [Migrator.Add] reports construction errors as [ErrInvalidMigration].
// An unknown nonempty OutOfOrder policy is rejected with ErrInvalidMigration
// by Up, Rollback, and Status before session acquisition or SQL; empty still
// selects OutOfOrderWarn.
// A [Func] is opaque, so [Exec] with no statements
// or a nil statement, or [Schema] with no operations, is detected when the
// step runs, before it executes anything, not at Add.
//
// # Events
//
// With [Builder.Events], [Migrator.Up] publishes [Applied]
// ("database.migration.applied") after each migration, [Migrator.Rollback]
// publishes [RolledBack] ("database.migration.rolled-back"), and either
// publishes [Failed] ("database.migration.failed") for the migration that
// failed before completion. If a migration transaction commits its schema and
// bookkeeping but cleanup or an after-commit hook then fails, it publishes the
// success event with nil Err and returns the error without running later
// migrations. An error returned by a step remains a failure even when it matches
// [database.ErrAfterCommit]. An outer session cleanup error does not change events
// for migrations that already completed. Events inside a caller-owned outer
// transaction remain provisional until that transaction commits.
//
//	migrator := migrate.New(db, func(migrator *migrate.Builder) {
//		migrator.Events(app.Events())
//	})
//
// New configures the migrator component through Builder policies; declaration
// policies remain the separate MigrationBuilder role. Component policies run
// directly once in order on one draft before defaults are resolved. Nil entries
// panic; expanded nil/empty slices supply no edits. Publication detaches settings,
// not the database, logger or event bus. Construction performs no migration,
// bookkeeping, session or lock I/O.
package migrate
