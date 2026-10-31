package postgresdatabase_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"arkoren.dev/core/service/database"
	postgres "arkoren.dev/driver/postgres/postgresdatabase"
	"arkoren.dev/foundation/query"
)

// TestFinalizerCommitReleasesLocksBeforeHooks characterizes the public facade's
// commit boundary: transaction locks are unlocked before hooks, and completed
// transaction contexts cannot reuse the connection after it returns to the pool.
func TestFinalizerCommitReleasesLocksBeforeHooks(t *testing.T) {
	t.Parallel()

	db, wire := newFinalizerDatabase(t,
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'T'},
		finalizerReply{sql: "commit", status: 'I'},
		finalizerReply{sql: "SELECT pg_advisory_unlock(", value: "t", status: 'I'},
	)

	var escaped context.Context
	hooks := 0

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		escaped = ctx

		if _, err := db.Lock(ctx, "finalizer-commit"); err != nil {
			return err
		}

		return db.AfterCommit(ctx, func(context.Context) error {
			hooks++

			wire.mu.Lock()
			defer wire.mu.Unlock()

			if len(wire.replies) != 0 {
				return errors.New("hook ran before transaction lock cleanup")
			}

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	if hooks != 1 || wire.connections.Load() != 1 {
		t.Fatalf("hooks = %d, connections = %d; want one each", hooks, wire.connections.Load())
	}

	if _, err := db.Exec(escaped, query.Raw("SELECT true")); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("escaped transaction Exec = %v, want sql.ErrTxDone", err)
	}
}

// TestFinalizerCallbackMarkerRollsBack verifies an ErrAfterCommit returned by
// application code never masquerades as confirmed commit or executes its hooks.
func TestFinalizerCallbackMarkerRollsBack(t *testing.T) {
	t.Parallel()

	db, _ := newFinalizerDatabase(t,
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'T'},
		finalizerReply{sql: "rollback", status: 'I'},
		finalizerReply{sql: "SELECT pg_advisory_unlock(", value: "t", status: 'I'},
	)

	planned := errors.New("callback requested rollback")
	hooks := 0

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		if _, err := db.Lock(ctx, "finalizer-rollback"); err != nil {
			return err
		}

		if err := db.AfterCommit(ctx, func(context.Context) error {
			hooks++

			return nil
		}); err != nil {
			return err
		}

		return fmt.Errorf("%w: %w", database.ErrAfterCommit, planned)
	})

	if !errors.Is(err, planned) || !errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("callback error = %v, want original marker and cause", err)
	}

	if hooks != 0 {
		t.Fatalf("rollback executed %d hooks", hooks)
	}
}

// TestFinalizerCleanupFailureStillRunsHooks makes native unlock report false
// after a confirmed commit. The public facade must preserve committed semantics
// and hooks while discarding the physical connection with uncertain ownership.
func TestFinalizerCleanupFailureStillRunsHooks(t *testing.T) {
	t.Parallel()

	db, wire := newFinalizerDatabase(t,
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'T'},
		finalizerReply{sql: "commit", status: 'I'},
		finalizerReply{sql: "SELECT pg_advisory_unlock(", value: "f", status: 'I'},
		finalizerReply{sql: "SELECT true", value: "t", status: 'I'},
	)

	hooks := 0

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		if _, err := db.Lock(ctx, "finalizer-cleanup-failure"); err != nil {
			return err
		}

		return db.AfterCommit(ctx, func(context.Context) error {
			hooks++

			return nil
		})
	})

	if !errors.Is(err, database.ErrAfterCommit) || hooks != 1 {
		t.Fatalf("cleanup error = %v, hooks = %d; want ErrAfterCommit and one hook", err, hooks)
	}

	if _, err := db.Exec(t.Context(), query.Raw("SELECT true")); err != nil {
		t.Fatal(err)
	}

	if wire.connections.Load() != 2 {
		t.Fatalf("connections = %d, want replacement for discarded session", wire.connections.Load())
	}
}

// TestFinalizerFailedCommitSkipsHooks distinguishes callback success from a
// confirmed native commit. The native SQLSTATE remains inspectable and no new
// after-commit marker can turn the failed transaction into a successful one.
func TestFinalizerFailedCommitSkipsHooks(t *testing.T) {
	t.Parallel()

	db, _ := newFinalizerDatabase(t,
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "commit", code: "23505", status: 'I'},
	)

	hooks := 0

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		return db.AfterCommit(ctx, func(context.Context) error {
			hooks++

			return nil
		})
	})

	var native *pgconn.PgError

	if !errors.As(err, &native) || native.Code != "23505" {
		t.Fatalf("failed commit = %v, want native 23505", err)
	}

	if errors.Is(err, database.ErrAfterCommit) || hooks != 0 {
		t.Fatalf("failed commit = %v, hooks = %d; want no marker or hooks", err, hooks)
	}
}

// TestFinalizerRollbackFailurePreservesCallbackError checks failed rollback
// joins its native cause with the callback error and never marks a commit. The
// poisoned connection is closed rather than used for advisory unlock SQL.
func TestFinalizerRollbackFailurePreservesCallbackError(t *testing.T) {
	t.Parallel()

	db, _ := newFinalizerDatabase(t,
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'T'},
		finalizerReply{sql: "rollback", code: "XX000", status: 'E'},
	)

	planned := errors.New("callback failure")

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		if _, err := db.Lock(ctx, "finalizer-rollback-failure"); err != nil {
			return err
		}

		return planned
	})

	var native *pgconn.PgError

	if !errors.Is(err, planned) || !errors.As(err, &native) {
		t.Fatalf("rollback failure = %v, want callback and native causes", err)
	}

	if native.Code != "XX000" || errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("rollback failure = %v, want XX000 without ErrAfterCommit", err)
	}
}

// TestFinalizerPanicPreservesIdentityAfterUnlock ensures recovery observes the
// exact application payload only after detached rollback and lock release. This
// covers unwinding rather than returning a callback error to the facade.
func TestFinalizerPanicPreservesIdentityAfterUnlock(t *testing.T) {
	t.Parallel()

	db, wire := newFinalizerDatabase(t,
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'T'},
		finalizerReply{sql: "rollback", status: 'I'},
		finalizerReply{sql: "SELECT pg_advisory_unlock(", value: "t", status: 'I'},
	)

	payload := &struct {
		// value makes payload nonzero-sized for pointer identity comparison.
		value int
	}{value: 1}

	func() {
		defer func() {
			if got := recover(); got != payload {
				t.Errorf("panic = %v, want original payload", got)
			}
		}()

		_ = db.WithTransaction(t.Context(), func(ctx context.Context) error {
			if _, err := db.Lock(ctx, "finalizer-panic"); err != nil {
				return err
			}

			panic(payload)
		})
	}()

	wire.mu.Lock()
	defer wire.mu.Unlock()

	if len(wire.replies) != 0 {
		t.Fatal("panic propagated before cleanup")
	}
}

// TestFinalizerGoexitRollsBackAndUnlocks characterizes a callback that neither
// panics nor returns. Joining its goroutine verifies all deferred cleanup ends
// before the caller proceeds, without real-time sleeps or a PostgreSQL service.
func TestFinalizerGoexitRollsBackAndUnlocks(t *testing.T) {
	t.Parallel()

	db, wire := newFinalizerDatabase(t,
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'T'},
		finalizerReply{sql: "rollback", status: 'I'},
		finalizerReply{sql: "SELECT pg_advisory_unlock(", value: "t", status: 'I'},
	)

	var group sync.WaitGroup

	group.Go(func() {
		if err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
			if _, err := db.Lock(ctx, "finalizer-goexit"); err != nil {
				return err
			}

			runtime.Goexit()

			return nil
		}); err != nil {
			t.Errorf("WithTransaction = %v", err)
		}
	})
	group.Wait()

	wire.mu.Lock()
	defer wire.mu.Unlock()

	if len(wire.replies) != 0 {
		t.Fatal("Goexit did not finish rollback and lock cleanup")
	}
}

// TestFinalizerCancellationRejectsFreshOperationContext preserves the owning
// scope guard: fresh operation contexts cannot resume canceled transactions.
// Finalization itself must still use detached rollback and unlock contexts.
func TestFinalizerCancellationRejectsFreshOperationContext(t *testing.T) {
	t.Parallel()

	db, _ := newFinalizerDatabase(t,
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'T'},
		finalizerReply{sql: "rollback", status: 'I'},
		finalizerReply{sql: "SELECT pg_advisory_unlock(", value: "t", status: 'I'},
	)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	err := db.WithTransaction(ctx, func(ctx context.Context) error {
		if _, err := db.Lock(ctx, "finalizer-cancel"); err != nil {
			return err
		}

		cancel()

		// Retain facade scope values while replacing only cancellation. The
		// driver's stored scope must reject this otherwise-live context.
		_, err := db.Exec(context.WithoutCancel(ctx), query.Raw("SELECT true"))

		if !errors.Is(err, context.Canceled) {
			t.Errorf("fresh-context Exec = %v, want context.Canceled", err)
		}

		return nil
	})

	if !errors.Is(err, context.Canceled) || errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("canceled transaction = %v, want cancellation without commit marker", err)
	}
}

// TestFinalizerNestedRollbackRetainsOuterLockAndHooks verifies native savepoint
// rollback does not unlock transaction-owned acquisitions or run nested hooks.
// A later successful nested transaction retains its hook until outer commit.
func TestFinalizerNestedRollbackRetainsOuterLockAndHooks(t *testing.T) {
	t.Parallel()

	db, _ := newFinalizerDatabase(t,
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "SAVEPOINT arkoren_1", status: 'T'},
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'T'},
		finalizerReply{sql: "ROLLBACK TO SAVEPOINT arkoren_1", status: 'T'},
		finalizerReply{sql: "RELEASE SAVEPOINT arkoren_1", status: 'T'},
		finalizerReply{sql: "SAVEPOINT arkoren_1", status: 'T'},
		finalizerReply{sql: "RELEASE SAVEPOINT arkoren_1", status: 'T'},
		finalizerReply{sql: "commit", status: 'I'},
		finalizerReply{sql: "SELECT pg_advisory_unlock(", value: "t", status: 'I'},
	)

	planned := errors.New("nested callback failure")
	var hooks []string

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		err := db.WithTransaction(ctx, func(ctx context.Context) error {
			if _, err := db.Lock(ctx, "finalizer-nested"); err != nil {
				return err
			}

			if err := db.AfterCommit(ctx, func(context.Context) error {
				hooks = append(hooks, "rolled-back")

				return nil
			}); err != nil {
				return err
			}

			return planned
		})

		if !errors.Is(err, planned) {
			return fmt.Errorf("nested rollback, want callback failure: %w", err)
		}

		if _, acquired, err := db.TryLock(ctx, "finalizer-nested"); err != nil || acquired {
			return fmt.Errorf("outer lock retained = %v: %w", !acquired, err)
		}

		return db.WithTransaction(ctx, func(ctx context.Context) error {
			return db.AfterCommit(ctx, func(context.Context) error {
				hooks = append(hooks, "committed")

				return nil
			})
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(hooks, []string{"committed"}) {
		t.Fatalf("hooks = %v, want only successful nested hook", hooks)
	}
}

// TestFinalizerTransactionWithinSessionPreservesSessionLock exercises the
// borrowed physical-owner path. Transaction cleanup releases only its fresh
// locks; the enclosing session remains usable by hooks and expires separately.
func TestFinalizerTransactionWithinSessionPreservesSessionLock(t *testing.T) {
	t.Parallel()

	db, wire := newFinalizerDatabase(t,
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'I'},
		finalizerReply{sql: "begin", status: 'T'},
		finalizerReply{sql: "SELECT pg_try_advisory_lock(", value: "t", status: 'T'},
		finalizerReply{sql: "commit", status: 'I'},
		finalizerReply{sql: "SELECT pg_advisory_unlock(", value: "t", status: 'I'},
		finalizerReply{sql: "SELECT true", value: "t", status: 'I'},
		finalizerReply{sql: "SELECT pg_advisory_unlock(", value: "t", status: 'I'},
	)

	var escaped context.Context

	err := db.WithSession(t.Context(), func(ctx context.Context) error {
		escaped = ctx

		if _, err := db.Lock(ctx, "finalizer-session"); err != nil {
			return err
		}

		if err := db.WithTransaction(ctx, func(ctx context.Context) error {
			if _, err := db.Lock(ctx, "finalizer-transaction"); err != nil {
				return err
			}

			return db.AfterCommit(ctx, func(ctx context.Context) error {
				_, err := db.Exec(ctx, query.Raw("SELECT true"))

				return err
			})
		}); err != nil {
			return err
		}

		if _, acquired, err := db.TryLock(ctx, "finalizer-session"); err != nil || acquired {
			return fmt.Errorf("session lock retained = %v: %w", !acquired, err)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if wire.connections.Load() != 1 {
		t.Fatalf("connections = %d, want one pinned physical session", wire.connections.Load())
	}

	if _, err := db.Exec(escaped, query.Raw("SELECT true")); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped session Exec = %v, want ErrSessionDone", err)
	}
}

// finalizerReply describes one expected native simple-protocol query. The SQL
// is a prefix so bound advisory keys need not duplicate the driver's hash rule.
// An optional value produces one text row; code instead returns a server error.
type finalizerReply struct {
	// sql identifies the next query; unexpected or reordered SQL fails the test.
	sql string

	// status is PostgreSQL's idle, transaction, or failed-transaction indicator.
	status byte

	// value is a bool text result; empty means a command-only reply.
	value string

	// code supplies a deterministic PostgreSQL SQLSTATE for failure scenarios.
	code string
}

// finalizerWire drives real pgx transactions through a local net.Pipe. It
// records connection creation and consumes the expected query sequence across
// disposal/reacquisition, without connecting to any PostgreSQL service.
type finalizerWire struct {
	// mu protects the pending script from test and connection goroutines.
	mu sync.Mutex

	// replies contains only queries that have not yet reached the wire.
	replies []finalizerReply

	// connections counts physical dials, detecting premature pool replacement.
	connections atomic.Int32
}

// newFinalizerDatabase transfers a single-slot native pool to the public driver
// and facade. Cleanup joins all wire goroutines and checks every expected query
// was consumed, including cleanup queries that must precede pool return.
func newFinalizerDatabase(t *testing.T, replies ...finalizerReply) (*database.Database, *finalizerWire) {
	t.Helper()

	wire := &finalizerWire{replies: replies}

	config, err := pgxpool.ParseConfig(
		"postgres://fixture@localhost/db?sslmode=disable&default_query_exec_mode=simple_protocol",
	)

	if err != nil {
		t.Fatal(err)
	}

	config.MaxConns = 1

	var group sync.WaitGroup

	config.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		wire.connections.Add(1)

		group.Go(func() {
			defer server.Close()

			if err := wire.serve(server); err != nil {
				t.Errorf("finalizer wire fixture: %v", err)
			}
		})

		return client, nil
	}

	pool, err := pgxpool.NewWithConfig(t.Context(), config)

	if err != nil {
		t.Fatal(err)
	}

	driver := postgres.NewFrom(pool)
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}

		group.Wait()

		if len(wire.replies) != 0 {
			t.Errorf("unconsumed finalizer replies: %+v", wire.replies)
		}
	})

	return database.New(driver), wire
}

// serve handles startup and one connection's query stream. Native Terminate
// and EOF are normal when cleanup discards a poisoned session; other unexpected
// messages or query ordering fail instead of approximating successful cleanup.
func (wire *finalizerWire) serve(conn net.Conn) error {
	backend := pgproto3.NewBackend(conn, conn)

	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return err
	}

	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})

	if err := backend.Flush(); err != nil {
		return err
	}

	for {
		message, err := backend.Receive()

		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}

		if err != nil {
			return err
		}

		if _, ok := message.(*pgproto3.Terminate); ok {
			return nil
		}

		request, ok := message.(*pgproto3.Query)

		if !ok {
			return fmt.Errorf("unexpected frontend message %T", message)
		}

		if err := wire.respond(backend, request.String); err != nil {
			return err
		}
	}
}

// respond consumes one scripted query and emits native command, row, or error
// messages. Successful bool rows exercise real advisory-lock decoding; failing
// commands exercise pgx's own transaction error and connection disposal paths.
func (wire *finalizerWire) respond(backend *pgproto3.Backend, statement string) error {
	wire.mu.Lock()
	defer wire.mu.Unlock()

	if len(wire.replies) == 0 {
		return fmt.Errorf("unexpected query %q after script completion", statement)
	}

	reply := wire.replies[0]
	wire.replies = wire.replies[1:]

	if !strings.HasPrefix(statement, reply.sql) {
		return fmt.Errorf("query = %q, want prefix %q", statement, reply.sql)
	}

	if reply.code != "" {
		backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: reply.code, Message: "planned failure"})
	} else {
		if reply.value != "" {
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{
				{Name: []byte("value"), DataTypeOID: 16, DataTypeSize: 1},
			}})
			backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(reply.value)}})
		}

		command, _, _ := strings.Cut(statement, " ")
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(strings.ToUpper(command))})
	}

	backend.Send(&pgproto3.ReadyForQuery{TxStatus: reply.status})

	return backend.Flush()
}
