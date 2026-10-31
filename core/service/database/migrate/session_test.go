package migrate

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// TestMigratorRetainedPolicyCannotChangePublication verifies scalar defaults
// detach at construction without acquiring a session or migration lock.
func TestMigratorRetainedPolicyCannotChangePublication(t *testing.T) {
	t.Parallel()

	var retained *Builder
	migrations := New(nil, func(migrator *Builder) {
		retained = migrator
		migrator.Table("application_history")
		migrator.LockName("application_lock")
		migrator.LockTimeout(-1)
	})
	retained.Table("mutated")
	retained.LockName("mutated")
	retained.LockTimeout(time.Second)

	if migrations.config.Table != "application_history" || migrations.config.LockName != "application_lock" {
		t.Fatalf("published settings changed: %#v", migrations.config)
	}

	if migrations.config.LockTimeout != -1 {
		t.Fatal("published lock budget changed")
	}
}

// errRoutingRoot detects SQL or locking that escaped the pinned callback. The
// fake deliberately has no usable pool executor, including for transactions.
var errRoutingRoot = errors.New("operation escaped migration session")

// errRoutingWork identifies a failed migration independently of cleanup errors.
// Tests use errors.Is to ensure deferred cleanup never replaces this cause.
var errRoutingWork = errors.New("migration work failed")

// errRoutingCleanup identifies failed unlock or session cleanup. It is separate
// from the body failure so tests can verify both survive error joining.
var errRoutingCleanup = errors.New("migration cleanup failed")

// errRoutingSession distinguishes outer session cleanup from explicit unlock.
// Both must remain observable when the migration body has already failed.
var errRoutingSession = errors.New("session cleanup failed")

// routingState scripts session boundaries and records executor routing. All
// access is synchronous or synchronized by synctest; it needs no fake clock.
type routingState struct {
	// calls records boundary and SQL operations in execution order. Executor
	// names distinguish the pool, retained connection, and child transaction.
	calls []string

	// records is the history returned by Select. Tests seed rollback targets
	// directly because this fake tests routing, not SQL parsing or persistence.
	records []record

	// historyErr simulates a failed bookkeeping read after preparation. It is
	// independent of SQL execution failures and must also stop acquisition retry.
	historyErr error

	// sessions counts acquisition attempts, including attempts blocked before
	// callback entry. Retrying after successful acquisition must not increase it.
	sessions int

	// beforeSession optionally blocks pool acquisition or reports a pool error.
	// It runs before a scoped executor is handed to the callback.
	beforeSession func(context.Context) error

	// try optionally controls contention and expiry races. A nil function
	// acquires immediately, without waiting for server-side lock contention.
	try func(context.Context) (bool, error)

	// exec optionally fails or pauses SQL after routing is recorded. It also
	// allows deterministic advancement of time during protected preparation.
	exec func(context.Context, string) error

	// unlock optionally inspects the detached, bounded cleanup context. A nil
	// function succeeds, leaving session cleanup as an independent boundary.
	unlock func(context.Context) error

	// sessionErr is joined after the callback returns, including a refused
	// acquisition. Such cleanup failures must never trigger another attempt.
	sessionErr error

	// transactionErr is returned after a successful transaction body. With
	// ErrAfterCommit it models confirmed commit followed by cleanup failure.
	transactionErr error
}

// TestMigrationPublicationDetachesOwnedSlices exercises both ownership
// boundaries: callers may edit input slices during construction, and retained
// drafts may edit their own slices after a definition has been published.
func TestMigrationPublicationDetachesOwnedSlices(t *testing.T) {
	t.Parallel()

	calls := 0
	step := func(context.Context, *database.Database) error {
		calls++

		return nil
	}

	up := []Func{step}
	down := []Func{step}
	dialects := []query.Dialect{query.Postgres, query.SQLite, query.Postgres}
	var retained *MigrationBuilder
	definition := NewMigration("owned", func(migration *MigrationBuilder) {
		retained = migration
		migration.Up(up...)
		migration.Down(down...)
		migration.Only(dialects...)

		up[0] = nil
		down[0] = nil
		dialects[0] = query.MySQL
	})

	retained.definition.up[0] = nil
	retained.definition.down[0] = nil
	retained.definition.dialects[0] = query.MySQL

	if err := definition.up[0](context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	if err := definition.down[0](context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Fatalf("step calls = %d", calls)
	}

	if !slices.Equal(definition.Dialects(), []query.Dialect{query.Postgres, query.SQLite}) {
		t.Fatalf("Dialects() = %v", definition.Dialects())
	}
}

// routingDriver exposes a root trap and distinct scoped executors. Transactions
// retain the session's state while giving SQL a separately observable route.
type routingDriver struct {
	// state retains the scripted behavior and log shared by every executor.
	// Copies of the driver never copy the execution history.
	state *routingState

	// route is empty for the unusable root, "session" for a retained connection,
	// and "transaction" for SQL inside a per-migration transaction.
	route string
}

// Dialect enables transactional DDL in routing tests. SQLite supplies portable
// SQL compilation without adding a vendor dependency to the core module.
func (*routingDriver) Dialect() query.Dialect {
	return query.SQLite
}

// Exec rejects pool fallback and records SQL on the chosen executor. It honors
// cancellation before invoking the optional SQL failure or timing script.
func (driver *routingDriver) Exec(ctx context.Context, statement string, _ ...any) (int64, error) {
	if driver.route == "" {
		return 0, errRoutingRoot
	}

	driver.state.calls = append(driver.state.calls, driver.route+" exec "+statement)

	if err := ctx.Err(); err != nil {
		return 0, err
	}

	if driver.state.exec != nil {
		return 1, driver.state.exec(ctx, statement)
	}

	return 1, nil
}

// Select returns a copied history only on the retained session. The destination
// is the migrator's private row type, so no SQL parser or reflection is needed.
func (driver *routingDriver) Select(ctx context.Context, _ string, dest any, _ ...any) error {
	if driver.route == "" {
		return errRoutingRoot
	}

	driver.state.calls = append(driver.state.calls, driver.route+" history")

	if err := ctx.Err(); err != nil {
		return err
	}

	if driver.state.historyErr != nil {
		return driver.state.historyErr
	}

	*dest.(*[]record) = slices.Clone(driver.state.records)

	return nil
}

// Query is not used to read migration history. Failing explicitly makes an
// unexpected streaming path visible rather than returning an unusable cursor.
func (*routingDriver) Query(context.Context, string, ...any) (database.Rows, error) {
	return nil, errRoutingRoot
}

// Find is unused by the scripted migrations. An unexpected call is an error,
// preventing a missing fake implementation from silently passing a test.
func (*routingDriver) Find(context.Context, string, any, ...any) error {
	return errRoutingRoot
}

// WithSession models pool acquisition, callback entry, and release separately.
// The release is logged even on panic, and cleanup errors accompany body errors.
func (driver *routingDriver) WithSession(ctx context.Context, fn func(database.Driver) error) error {
	driver.state.sessions++

	if driver.state.beforeSession != nil {
		if err := driver.state.beforeSession(ctx); err != nil {
			return err
		}
	}

	driver.state.calls = append(driver.state.calls, "session enter")
	defer func() {
		driver.state.calls = append(driver.state.calls, "session release")
	}()

	return errors.Join(fn(&routingDriver{state: driver.state, route: "session"}), driver.state.sessionErr)
}

// WithTransaction rejects transaction creation on the root pool. Its child
// records rollback for body failures and commit only after the body succeeds.
func (driver *routingDriver) WithTransaction(ctx context.Context, fn func(database.Driver) error) error {
	if driver.route == "" {
		return errRoutingRoot
	}

	driver.state.calls = append(driver.state.calls, "transaction begin")

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := fn(&routingDriver{state: driver.state, route: "transaction"}); err != nil {
		driver.state.calls = append(driver.state.calls, "transaction rollback")

		return err
	}

	driver.state.calls = append(driver.state.calls, "transaction commit")

	return driver.state.transactionErr
}

// Lock is deliberately unsupported by this fake. The migrator must make one
// nonwaiting TryLock per session attempt, not retain a connection while waiting.
func (*routingDriver) Lock(context.Context, string) (func(context.Context) error, error) {
	return nil, errRoutingRoot
}

// TryLock scripts one attempt on the retained connection. Successful unlock
// records its position before scope release and can inspect cleanup deadlines.
func (driver *routingDriver) TryLock(ctx context.Context, _ string) (func(context.Context) error, bool, error) {
	if driver.route == "" {
		return nil, false, errRoutingRoot
	}

	driver.state.calls = append(driver.state.calls, "session trylock")

	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	if driver.state.try != nil {
		acquired, err := driver.state.try(ctx)

		if err != nil || !acquired {
			return nil, false, err
		}
	}

	return func(ctx context.Context) error {
		driver.state.calls = append(driver.state.calls, "session unlock")

		if driver.state.unlock != nil {
			return driver.state.unlock(ctx)
		}

		return nil
	}, true, nil
}

// TestUpRoutesPreparationHistoryAndEveryMigrationThroughOneSession guards all
// SQL boundaries, including transaction creation and transactionless steps.
func TestUpRoutesPreparationHistoryAndEveryMigrationThroughOneSession(t *testing.T) {
	t.Parallel()

	state := &routingState{}
	migrator := New(database.New(&routingDriver{state: state}))

	if err := migrator.Add(NewMigration("a", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-a")))
	}),
		NewMigration("b", func(migration *MigrationBuilder) {
			migration.Up(Exec(query.Raw("up-b")))
			migration.WithoutTransaction()
		}),
		NewMigration("c", func(migration *MigrationBuilder) {
			migration.Up(Exec(query.Raw("up-c")))
		})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	if state.sessions != 1 || !slices.Equal(state.calls[:2], []string{"session enter", "session trylock"}) {
		t.Fatalf("sessions = %d, calls = %v", state.sessions, state.calls)
	}

	if !strings.HasPrefix(state.calls[2], "session exec CREATE TABLE IF NOT EXISTS") || state.calls[3] != "session history" {
		t.Fatalf("preparation/history routing = %v", state.calls)
	}

	for _, want := range []string{"transaction exec up-a", "session exec up-b", "transaction exec up-c"} {
		if !slices.Contains(state.calls, want) {
			t.Fatalf("missing %q in %v", want, state.calls)
		}
	}

	if !slices.Equal(state.calls[len(state.calls)-2:], []string{"session unlock", "session release"}) {
		t.Fatalf("cleanup order = %v", state.calls)
	}

	inserts := []string{}

	for _, call := range state.calls {
		if strings.Contains(call, " exec INSERT INTO") {
			inserts = append(inserts, strings.SplitN(call, " exec ", 2)[0])
		}
	}

	if !slices.Equal(inserts, []string{"transaction", "session", "transaction"}) {
		t.Fatalf("bookkeeping executors = %v", inserts)
	}
}

// TestRollbackRoutesTargetsAndRecordsThroughOneSession guards the downward
// path independently, including the ledger delete inside each transaction.
func TestRollbackRoutesTargetsAndRecordsThroughOneSession(t *testing.T) {
	t.Parallel()

	state := &routingState{records: []record{{ID: "a", Batch: 1}, {ID: "b", Batch: 1}}}
	migrator := New(database.New(&routingDriver{state: state}))

	if err := migrator.Add(NewMigration("a", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-a")))
		migration.Down(Exec(query.Raw("down-a")))
	}),
		NewMigration("b", func(migration *MigrationBuilder) {
			migration.Up(Exec(query.Raw("up-b")))
			migration.Down(Exec(query.Raw("down-b")))
		})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}

	if state.sessions != 1 || slices.Index(state.calls, "transaction exec down-b") >= slices.Index(state.calls, "transaction exec down-a") {
		t.Fatalf("rollback order = %v", state.calls)
	}

	deletes := 0

	for _, call := range state.calls {
		if strings.HasPrefix(call, "transaction exec DELETE FROM") {
			deletes++
		}
	}

	if deletes != 2 {
		t.Fatalf("ledger deletes = %d, calls = %v", deletes, state.calls)
	}
}

// TestStatusPreparesAndReadsOnlyAfterSessionLock verifies that Status keeps its
// historical write-on-first-use behavior without escaping the lock session.
func TestStatusPreparesAndReadsOnlyAfterSessionLock(t *testing.T) {
	t.Parallel()

	state := &routingState{}

	if _, err := New(database.New(&routingDriver{state: state})).Status(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(state.calls) != 6 || state.calls[1] != "session trylock" || state.calls[3] != "session history" {
		t.Fatalf("calls = %v", state.calls)
	}
}

// TestLockTimeoutIncludesSessionPoolAcquisition exercises expiry before callback
// entry. Cancel-cause contexts report Canceled through Err, not DeadlineExceeded.
func TestLockTimeoutIncludesSessionPoolAcquisition(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		state := &routingState{beforeSession: func(ctx context.Context) error {
			<-ctx.Done()

			return ctx.Err()
		}}

		err := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
			migrator.LockTimeout(time.Second)
		}).Up(t.Context())

		if !errors.Is(err, ErrLockTimeout) || !errors.Is(err, context.DeadlineExceeded) || len(state.calls) != 0 {
			t.Fatalf("error = %v, calls = %v", err, state.calls)
		}
	})
}

// TestLockTimeoutStopsBeforePreparation advances fake time during the very first
// protected statement. The acquired session must outlive the acquisition budget.
func TestLockTimeoutStopsBeforePreparation(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		state := &routingState{exec: func(ctx context.Context, _ string) error {
			time.Sleep(2 * time.Second)

			return ctx.Err()
		}}

		if err := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
			migrator.LockTimeout(time.Second)
		}).Up(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestLockExpiryRacingSuccessfulAcquisitionDoesNotRunSQL makes acquisition report
// success after expiry. Cleanup must run, but stale work must never start.
func TestLockExpiryRacingSuccessfulAcquisitionDoesNotRunSQL(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		state := &routingState{try: func(context.Context) (bool, error) {
			time.Sleep(time.Second)

			return true, nil
		}}

		err := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
			migrator.LockTimeout(time.Second)
		}).Up(t.Context())

		if !errors.Is(err, ErrLockTimeout) || state.sessions != 1 || !slices.Equal(state.calls,
			[]string{"session enter", "session trylock", "session unlock", "session release"}) {
			t.Fatalf("error = %v, calls = %v", err, state.calls)
		}
	})
}

// TestContentionReleasesSessionBeforePolling observes the first refused attempt
// while the retry timer is blocked. No retained connection may span that wait.
func TestContentionReleasesSessionBeforePolling(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		state := &routingState{try: func(context.Context) (bool, error) {
			return false, nil
		}}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)

		go func() {
			done <- New(database.New(&routingDriver{state: state})).Up(ctx)
		}()

		synctest.Wait()

		if !slices.Equal(state.calls, []string{"session enter", "session trylock", "session release"}) {
			t.Fatalf("calls before poll = %v", state.calls)
		}

		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) || errors.Is(err, ErrLockTimeout) {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestRefusedLockWithSessionCleanupFailureIsTerminal prevents cleanup errors
// from being treated as harmless contention and retried on another connection.
func TestRefusedLockWithSessionCleanupFailureIsTerminal(t *testing.T) {
	t.Parallel()

	state := &routingState{try: func(context.Context) (bool, error) {
		return false, nil
	}, sessionErr: errRoutingCleanup}

	err := New(database.New(&routingDriver{state: state})).Up(t.Context())

	if !errors.Is(err, errRoutingCleanup) || state.sessions != 1 {
		t.Fatalf("error = %v, sessions = %d", err, state.sessions)
	}
}

// TestSessionLossStopsBeforeLaterDestructiveMigration verifies that a failure on
// the pinned executor is terminal, even if a fresh acquisition could succeed.
func TestSessionLossStopsBeforeLaterDestructiveMigration(t *testing.T) {
	t.Parallel()

	state := &routingState{
		records: []record{{ID: "a", Batch: 1}, {ID: "b", Batch: 1}},
		exec: func(_ context.Context, statement string) error {
			if statement == "down-b" {
				return database.ErrSessionDone
			}

			return nil
		},
	}

	migrator := New(database.New(&routingDriver{state: state}))

	if err := migrator.Add(NewMigration("a", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-a")))
		migration.Down(Exec(query.Raw("DROP protected")))
	}),
		NewMigration("b", func(migration *MigrationBuilder) {
			migration.Up(Exec(query.Raw("up-b")))
			migration.Down(Exec(query.Raw("down-b")))
		})); err != nil {
		t.Fatal(err)
	}

	err := migrator.Rollback(t.Context())

	if !errors.Is(err, database.ErrSessionDone) || state.sessions != 1 || slices.Contains(state.calls, "transaction exec DROP protected") {
		t.Fatalf("error = %v, calls = %v", err, state.calls)
	}
}

// TestMigrationFailureKeepsUnlockAndSessionErrors checks that bounded cleanup
// executes inside the live scope without replacing the original SQL error.
func TestMigrationFailureKeepsUnlockAndSessionErrors(t *testing.T) {
	t.Parallel()

	state := &routingState{exec: func(context.Context, string) error {
		return errRoutingWork
	},
		sessionErr: errRoutingSession, unlock: func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Errorf("cleanup context ended: %v", ctx.Err())
			}

			if _, ok := ctx.Deadline(); !ok {
				t.Error("cleanup has no deadline")
			}

			return errRoutingCleanup
		}}

	err := New(database.New(&routingDriver{state: state})).Up(t.Context())

	if !errors.Is(err, errRoutingWork) || !errors.Is(err, errRoutingCleanup) || !errors.Is(err, errRoutingSession) {
		t.Fatalf("error = %v", err)
	}
}

// TestCallerCancellationDuringPoolWaitIsNotLockTimeout preserves caller-owned
// cancellation independently of the migrator's longer acquisition watchdog.
func TestCallerCancellationDuringPoolWaitIsNotLockTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		state := &routingState{beforeSession: func(ctx context.Context) error {
			cancel()
			<-ctx.Done()

			return ctx.Err()
		}}

		err := New(database.New(&routingDriver{state: state})).Up(ctx)

		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrLockTimeout) {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestNegativeLockTimeoutWaitsForCaller advances fake time far beyond the default
// budget, then cancels explicitly. Negative configuration must not add a timer.
func TestNegativeLockTimeoutWaitsForCaller(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		state := &routingState{beforeSession: func(ctx context.Context) error {
			time.Sleep(time.Hour)
			cancel()

			return ctx.Err()
		}}

		err := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
			migrator.LockTimeout(-1)
		}).Up(ctx)

		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrLockTimeout) {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestMigrationPanicUnlocksBeforeSessionRelease preserves the caller's panic
// while ensuring the lock is cleaned up before its session callback ends.
func TestMigrationPanicUnlocksBeforeSessionRelease(t *testing.T) {
	t.Parallel()

	state := &routingState{exec: func(context.Context, string) error {
		panic(errRoutingWork)
	}}
	func() {
		defer func() {
			got := recover()

			err, ok := got.(error)

			if !ok || !errors.Is(err, errRoutingWork) {
				t.Fatalf("panic = %v", got)
			}
		}()
		_ = New(database.New(&routingDriver{state: state})).Up(t.Context())
	}()

	if len(state.calls) < 2 || !slices.Equal(state.calls[len(state.calls)-2:], []string{"session unlock", "session release"}) {
		t.Fatalf("calls = %v", state.calls)
	}
}

// TestCommittedMigrationCleanupFailurePublishesApplied covers the distinction
// between a committed ledger write and a failed post-commit cleanup operation.
func TestCommittedMigrationCleanupFailurePublishesApplied(t *testing.T) {
	t.Parallel()

	state := &routingState{transactionErr: errors.Join(database.ErrAfterCommit, errRoutingCleanup)}
	bus := event.New()
	var seen []string

	for _, topic := range []event.Topic[MigrationEvent]{Applied, Failed} {
		if _, err := bus.Listen(topic, func(_ context.Context, payload MigrationEvent) error {
			seen = append(seen, topic.Name()+" "+payload.ID)

			if topic == Applied && payload.Err != nil {
				t.Errorf("applied error = %v", payload.Err)
			}

			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	migrator := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.Events(bus)
	})

	if err := migrator.Add(NewMigration("a", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-a")))
	}), NewMigration("b", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-b")))
	})); err != nil {
		t.Fatal(err)
	}

	err := migrator.Up(t.Context())

	if !errors.Is(err, database.ErrAfterCommit) || !slices.Equal(seen, []string{"database.migration.applied a"}) || slices.Contains(state.calls, "transaction exec up-b") {
		t.Fatalf("error = %v, events = %v, calls = %v", err, seen, state.calls)
	}
}

// TestCallbackAfterCommitMarkerPublishesFailed rejects sentinel-only outcome
// inference. Returning the marker from a step still rolls back that migration.
func TestCallbackAfterCommitMarkerPublishesFailed(t *testing.T) {
	t.Parallel()

	state := &routingState{}
	bus := event.New()
	failed := false

	if _, err := bus.Listen(Failed, func(_ context.Context, payload MigrationEvent) error {
		failed = errors.Is(payload.Err, database.ErrAfterCommit)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	migrator := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.Events(bus)
	})

	if err := migrator.Add(NewMigration("a", func(migration *MigrationBuilder) {
		migration.Up(func(context.Context, *database.Database) error {
			return database.ErrAfterCommit
		})
	})); err != nil {
		t.Fatal(err)
	}

	err := migrator.Up(t.Context())

	if !errors.Is(err, database.ErrAfterCommit) || !failed || !slices.Contains(state.calls, "transaction rollback") {
		t.Fatalf("error = %v, failed = %t, calls = %v", err, failed, state.calls)
	}
}

// TestContentionRetriesOnAReleasedSessionThenStopsPolling verifies the positive
// polling path, not just cancellation of a waiter. Only the successful scope
// may prepare the ledger and execute the operation.
func TestContentionRetriesOnAReleasedSessionThenStopsPolling(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		state := &routingState{try: func(context.Context) (bool, error) {
			attempts++

			return attempts == 2, nil
		}}

		if err := New(database.New(&routingDriver{state: state})).Up(t.Context()); err != nil {
			t.Fatal(err)
		}

		if state.sessions != 2 || !slices.Equal(state.calls[:5], []string{
			"session enter", "session trylock", "session release", "session enter", "session trylock",
		}) {
			t.Fatalf("sessions = %d, calls = %v", state.sessions, state.calls)
		}
	})
}

// TestHistorySessionLossIsTerminal stops before invoking any migration callback.
// A history failure must not be mistaken for failed lock contention or retried
// through a fresh pool connection with a potentially different history.
func TestHistorySessionLossIsTerminal(t *testing.T) {
	t.Parallel()

	state := &routingState{historyErr: database.ErrSessionDone}
	migrator := New(database.New(&routingDriver{state: state}))

	if err := migrator.Add(NewMigration("a", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-a")))
	})); err != nil {
		t.Fatal(err)
	}

	err := migrator.Up(t.Context())

	if !errors.Is(err, database.ErrSessionDone) || state.sessions != 1 || slices.Contains(state.calls, "transaction begin") {
		t.Fatalf("error = %v, calls = %v", err, state.calls)
	}
}

// TestCanceledMigrationUsesBoundedDetachedUnlock cancels during protected work
// rather than acquisition. The body failure remains a caller cancellation while
// unlock receives a live deadline and still precedes session release.
func TestCanceledMigrationUsesBoundedDetachedUnlock(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	state := &routingState{exec: func(context.Context, string) error {
		cancel()

		return context.Canceled
	},
		unlock: func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Errorf("unlock context error = %v", ctx.Err())
			}

			if _, ok := ctx.Deadline(); !ok {
				t.Error("unlock context has no deadline")
			}

			return errRoutingCleanup
		}}

	err := New(database.New(&routingDriver{state: state})).Up(ctx)

	if !errors.Is(err, context.Canceled) || !errors.Is(err, errRoutingCleanup) || errors.Is(err, ErrLockTimeout) || state.sessions != 1 {
		t.Fatalf("error = %v, sessions = %d", err, state.sessions)
	}
}

// TestCallerDeadlineIsNotLockTimeout distinguishes an inherited deadline from
// the migrator's watchdog even though both match context.DeadlineExceeded.
func TestCallerDeadlineIsNotLockTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		state := &routingState{beforeSession: func(ctx context.Context) error {
			<-ctx.Done()

			return ctx.Err()
		}}

		err := New(database.New(&routingDriver{state: state})).Up(ctx)

		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrLockTimeout) {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestInvalidTableFailsBeforeSessionAcquisition protects validation ordering.
// A malformed table must not consume pool capacity or acquire a managed lock.
func TestInvalidTableFailsBeforeSessionAcquisition(t *testing.T) {
	t.Parallel()

	state := &routingState{}

	err := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.Table("bad name;")
	}).Up(t.Context())

	if !errors.Is(err, ErrInvalidMigration) || state.sessions != 0 {
		t.Fatalf("error = %v, sessions = %d", err, state.sessions)
	}
}

// TestCommitFailurePublishesFailed requires confirmation of commit, not merely
// a successful callback body. A failed commit must not publish Applied.
func TestCommitFailurePublishesFailed(t *testing.T) {
	t.Parallel()

	state := &routingState{transactionErr: errRoutingCleanup}
	bus := event.New()
	failed := false

	if _, err := bus.Listen(Failed, func(_ context.Context, payload MigrationEvent) error {
		failed = errors.Is(payload.Err, errRoutingCleanup)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	migrator := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.Events(bus)
	})

	if err := migrator.Add(NewMigration("a", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-a")))
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Up(t.Context()); !errors.Is(err, errRoutingCleanup) || !failed {
		t.Fatalf("error = %v, failed = %t", err, failed)
	}
}

// TestSessionCleanupFailureDoesNotReplaceAppliedEvent reports the already
// committed migration once. Failure after the operation's session callback ends
// is returned without inventing a Failed migration event or replaying the body.
func TestSessionCleanupFailureDoesNotReplaceAppliedEvent(t *testing.T) {
	t.Parallel()

	state := &routingState{sessionErr: errRoutingCleanup}
	bus := event.New()
	var seen []string

	for _, topic := range []event.Topic[MigrationEvent]{Applied, Failed} {
		if _, err := bus.Listen(topic, func(context.Context, MigrationEvent) error {
			seen = append(seen, topic.Name())

			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	migrator := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.Events(bus)
	})

	if err := migrator.Add(NewMigration("a", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-a")))
	})); err != nil {
		t.Fatal(err)
	}

	err := migrator.Up(t.Context())

	if !errors.Is(err, errRoutingCleanup) || state.sessions != 1 || !slices.Equal(seen, []string{"database.migration.applied"}) {
		t.Fatalf("error = %v, events = %v, sessions = %d", err, seen, state.sessions)
	}
}

// TestBuilderDefaults preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderDefaults(t *testing.T) {
	t.Parallel()

	config := New(nil).config

	if config.Logger != slog.Default() {
		t.Fatal("Logger is not slog.Default()")
	}

	if config.Table != "arkoren_migrations" {
		t.Fatalf("Table = %q", config.Table)
	}

	if config.LockName != "arkoren_migrations" {
		t.Fatalf("LockName = %q", config.LockName)
	}

	if config.OutOfOrder != OutOfOrderWarn {
		t.Fatalf("OutOfOrder = %v", config.OutOfOrder)
	}
}

// TestBuilderDefaultsLockTimeout preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderDefaultsLockTimeout(t *testing.T) {
	t.Parallel()

	if got := New(nil).config.LockTimeout; got != 5*time.Minute {
		t.Fatalf("LockTimeout = %v, want 5m", got)
	}
}

// TestBuilderKeepsNegativeLockTimeout preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderKeepsNegativeLockTimeout(t *testing.T) {
	t.Parallel()

	if got := New(nil, func(migrator *Builder) {
		migrator.LockTimeout(-1)
	}).config.LockTimeout; got != -1 {
		t.Fatalf("LockTimeout = %v, want -1", got)
	}
}

// TestBuilderLockTimeoutSetsLockTimeout preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderLockTimeoutSetsLockTimeout(t *testing.T) {
	t.Parallel()

	if got := New(nil, func(migrator *Builder) {
		migrator.LockTimeout(time.Second)
	}).config.LockTimeout; got != time.Second {
		t.Fatalf("LockTimeout = %v, want 1s", got)
	}
}

// TestBuilderTablePreservesDefaults preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderTablePreservesDefaults(t *testing.T) {
	t.Parallel()

	config := New(nil, func(migrator *Builder) {
		migrator.Table("t")
	}).config

	if config.Table != "t" || config.LockName != "arkoren_migrations" {
		t.Fatalf("config = %+v", config)
	}
}

// TestBuilderLaterSettingWins preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderLaterSettingWins(t *testing.T) {
	t.Parallel()

	config := New(nil, func(migrator *Builder) {
		migrator.Table("a")
	}, func(migrator *Builder) {
		migrator.Table("b")
	}).config

	if config.Table != "b" {
		t.Fatalf("Table = %q", config.Table)
	}
}

// TestBuilderLoggerSetsLogger preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderLoggerSetsLogger(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)

	if New(nil, func(migrator *Builder) {
		migrator.Logger(logger)
	}).config.Logger != logger {
		t.Fatal("Logger not set")
	}
}

// TestBuilderTableSetsTable preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderTableSetsTable(t *testing.T) {
	t.Parallel()

	if got := New(nil, func(migrator *Builder) {
		migrator.Table("x")
	}).config.Table; got != "x" {
		t.Fatalf("Table = %q", got)
	}
}

// TestBuilderLockNameSetsLockName preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderLockNameSetsLockName(t *testing.T) {
	t.Parallel()

	if got := New(nil, func(migrator *Builder) {
		migrator.LockName("x")
	}).config.LockName; got != "x" {
		t.Fatalf("LockName = %q", got)
	}
}

// TestBuilderOutOfOrderSetsMode preserves the original setting and default behavior.
// Construction performs no SQL or lock acquisition.
func TestBuilderOutOfOrderSetsMode(t *testing.T) {
	t.Parallel()

	if got := New(nil, func(migrator *Builder) {
		migrator.OutOfOrder(OutOfOrderError)
	}).config.OutOfOrder; got != OutOfOrderError {
		t.Fatalf("OutOfOrder = %v", got)
	}
}

// TestMigratorPoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestMigratorPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	component := New(nil, func(migrator *Builder) {
		calls++
		retained = migrator
	}, func(migrator *Builder) {
		if calls != 1 || retained != migrator {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	New(nil, func(migrator *Builder) {
		if retained == migrator {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestMigratorNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestMigratorNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*Builder)
	empty := []func(*Builder){}

	if New(nil, absent...) == nil || New(nil, empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestMigratorNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestMigratorNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		New(nil, nil, func(migrator *Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestMigratorPolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestMigratorPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		New(nil, func(migrator *Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestMigratorLoggerReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMigratorLoggerReturnsDraft(t *testing.T) {
	t.Parallel()

	migrator := &Builder{}

	if migrator.Logger(nil) != migrator {
		t.Fatal("setting returned a different draft")
	}
}

// TestMigratorEventsReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMigratorEventsReturnsDraft(t *testing.T) {
	t.Parallel()

	migrator := &Builder{}

	if migrator.Events(nil) != migrator {
		t.Fatal("setting returned a different draft")
	}
}

// TestMigratorTableReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMigratorTableReturnsDraft(t *testing.T) {
	t.Parallel()

	migrator := &Builder{}

	if migrator.Table("custom") != migrator {
		t.Fatal("setting returned a different draft")
	}
}

// TestMigratorLockNameReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMigratorLockNameReturnsDraft(t *testing.T) {
	t.Parallel()

	migrator := &Builder{}

	if migrator.LockName("custom") != migrator {
		t.Fatal("setting returned a different draft")
	}
}

// TestMigratorLockTimeoutReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMigratorLockTimeoutReturnsDraft(t *testing.T) {
	t.Parallel()

	migrator := &Builder{}

	if migrator.LockTimeout(-1) != migrator {
		t.Fatal("setting returned a different draft")
	}
}

// TestMigratorOutOfOrderReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMigratorOutOfOrderReturnsDraft(t *testing.T) {
	t.Parallel()

	migrator := &Builder{}

	if migrator.OutOfOrder("") != migrator {
		t.Fatal("setting returned a different draft")
	}
}
