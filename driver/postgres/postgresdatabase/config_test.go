package postgresdatabase_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"arkoren.dev/core/service/database"
	postgres "arkoren.dev/driver/postgres/postgresdatabase"
	"arkoren.dev/foundation/query"
)

// TestBuilderPoliciesRunOnceInOrder checks one fresh draft, native pool settings,
// ignored MaxIdleConns symmetry, and isolation from a retained draft.
func TestBuilderPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *postgres.Builder
	calls := 0

	driver, err := postgres.New("postgres://localhost/db", func(databaseClient *postgres.Builder) {
		calls++
		retained = databaseClient
		databaseClient.MaxOpenConns(1)
	}, func(databaseClient *postgres.Builder) {
		calls++

		if databaseClient != retained {
			t.Fatal("callbacks received different drafts")
		}

		databaseClient.MaxOpenConns(2)
		databaseClient.MaxIdleConns(8)
		databaseClient.ConnMaxLifetime(time.Minute)
		databaseClient.ConnMaxIdleTime(time.Second)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	retained.MaxOpenConns(9)

	config := driver.DB().Config()

	if calls != 2 || config.MaxConns != 2 {
		t.Fatalf("calls = %d, MaxConns = %d", calls, config.MaxConns)
	}

	if config.MaxConnLifetime != time.Minute || config.MaxConnIdleTime != time.Second {
		t.Fatalf("native age limits changed: %+v", config)
	}
}

// TestBuilderCallbacksRunBeforeValidation checks callbacks on invalid required
// input without creating a pool or connecting to any database.
func TestBuilderCallbacksRunBeforeValidation(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := postgres.New("", func(databaseClient *postgres.Builder) {
		calls++
		databaseClient.MaxOpenConns(2)
	})

	if err == nil || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
}

// TestBuilderNilCallbackPanics distinguishes nil entries from omission, even
// when required arguments would otherwise produce a configuration error.
func TestBuilderNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = postgres.New("", nil)
}

// TestBuilderPanicPropagates checks callback panics retain their identity and
// stop the remaining policies before any pool can be created.
func TestBuilderPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := new(int)

	defer func() {
		if recover() != marker {
			t.Fatal("constructor changed the callback panic")
		}
	}()

	_, _ = postgres.New("", func(*postgres.Builder) {
		panic(marker)
	}, func(*postgres.Builder) {
		t.Fatal("callback ran after panic")
	})
}

// TestBuilderEmptyPoliciesKeepDefaults compares nil and empty callback slices
// with native pgx defaults without making a connection.
func TestBuilderEmptyPoliciesKeepDefaults(t *testing.T) {
	t.Parallel()

	for _, configure := range [][]func(*postgres.Builder){nil, {}} {
		driver, err := postgres.New("postgres://localhost/db", configure...)

		if err != nil {
			t.Fatal(err)
		}

		if driver.DB().Config().MaxConns < 4 {
			t.Error("native default open limit changed")
		}

		if err := driver.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNewFromOwnsPool verifies the no-callback wrapper retains the exact pool
// and closes it on Stop, without replacing its immutable native settings.
func TestNewFromOwnsPool(t *testing.T) {
	t.Parallel()

	pool, err := pgxpool.New(t.Context(), "postgres://localhost/db?pool_max_conns=3")

	if err != nil {
		t.Fatal(err)
	}

	driver := postgres.NewFrom(pool)

	if driver.DB() != pool || pool.Config().MaxConns != 3 {
		t.Fatal("NewFrom changed the native pool")
	}

	if err := driver.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Acquire(t.Context()); err == nil {
		t.Fatal("Stop did not close the transferred pool")
	}
}

// TestNewRejectsBlankDSN verifies New rejects a blank DSN before connecting.
func TestNewRejectsBlankDSN(t *testing.T) {
	t.Parallel()

	if _, err := postgres.New(" \n"); !errors.Is(err, postgres.ErrInvalidConfig) {
		t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsUnparsableDSN verifies New reports a DSN pgx cannot parse.
func TestNewRejectsUnparsableDSN(t *testing.T) {
	t.Parallel()

	if _, err := postgres.New("postgres://host:notaport/db"); !errors.Is(err, postgres.ErrInvalidConfig) {
		t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsNegativePoolLimits verifies every negative pool limit is
// invalid.
func TestNewRejectsNegativePoolLimits(t *testing.T) {
	t.Parallel()

	for _, configure := range []func(*postgres.Builder){
		func(databaseClient *postgres.Builder) {
			databaseClient.MaxOpenConns(-1)
		},
		func(databaseClient *postgres.Builder) {
			databaseClient.MaxIdleConns(-1)
		},
		func(databaseClient *postgres.Builder) {
			databaseClient.ConnMaxLifetime(-time.Nanosecond)
		},
		func(databaseClient *postgres.Builder) {
			databaseClient.ConnMaxIdleTime(-time.Nanosecond)
		},
	} {
		if _, err := postgres.New("postgres://localhost/db", configure); !errors.Is(err, postgres.ErrInvalidConfig) {
			t.Errorf("New() error = %v, want ErrInvalidConfig", err)
		}
	}
}

// TestNewConnectsLazily verifies New does no network I/O and applies the pool
// limit.
func TestNewConnectsLazily(t *testing.T) {
	t.Parallel()

	driver, err := postgres.New("postgres://nobody@127.0.0.1:1/db", func(databaseClient *postgres.Builder) {
		databaseClient.MaxOpenConns(3)
	})

	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if got := driver.DB().Config().MaxConns; got != 3 {
		t.Fatalf("MaxConns = %d, want 3", got)
	}

	if driver.Dialect() != query.Postgres {
		t.Fatalf("Dialect() = %v, want Postgres", driver.Dialect())
	}
}

// TestNewFromWrapsPool verifies NewFrom uses the caller's pool.
func TestNewFromWrapsPool(t *testing.T) {
	t.Parallel()

	pool, err := pgxpool.New(t.Context(), postgresConnectionURL(sharedPostgresEndpoint.host, sharedPostgresEndpoint.port))

	if err != nil {
		t.Fatal(err)
	}

	driver := postgres.NewFrom(pool)
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if driver.DB() != pool {
		t.Fatal("DB() is not the pool passed to NewFrom")
	}
}

// TestTransactionUsedAfterEndReportsErrTxDone verifies the driver of a
// finished transaction fails with database/sql.ErrTxDone.
func TestTransactionUsedAfterEndReportsErrTxDone(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	driver, connection := newPostgresDriver(t)
	registerPostgresConnectionCleanup(t, connection)

	ctx := postgresTestContext(t)

	var leaked interface {
		Find(ctx context.Context, statement string, dest any, args ...any) error
	}

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		leaked = tx

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	var one int

	if err := leaked.Find(ctx, "SELECT 1", &one); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("Find() error = %v, want sql.ErrTxDone", err)
	}
}

// TestNewRejectsEagerMinimumConnections protects New's no-lifecycle-I/O promise.
// pgxpool starts minimum-connection creation in its constructor, not at Ping.
func TestNewRejectsEagerMinimumConnections(t *testing.T) {
	t.Parallel()

	driver, err := postgres.New("postgres://fixture@127.0.0.1:1/db?pool_min_conns=1")

	if driver != nil {
		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}

	if !errors.Is(err, postgres.ErrInvalidConfig) {
		t.Fatalf("eager pool constructor = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsEagerMinimumIdleConnections covers pgxpool's independent minimum
// idle policy, which also creates background connections before the first use.
func TestNewRejectsEagerMinimumIdleConnections(t *testing.T) {
	t.Parallel()

	driver, err := postgres.New("postgres://fixture@127.0.0.1:1/db?pool_min_idle_conns=1")

	if driver != nil {
		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}

	if !errors.Is(err, postgres.ErrInvalidConfig) {
		t.Fatalf("eager pool constructor = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRedactsMalformedDSN preserves native parse error identity for explicit
// inspection without exposing escaped password fragments in ordinary messages.
func TestNewRedactsMalformedDSN(t *testing.T) {
	t.Parallel()

	dsn := "user=fixture password='credential-head\\' credential-tail' port=notaport"

	_, err := postgres.New(dsn)

	if !errors.Is(err, postgres.ErrInvalidConfig) {
		t.Fatalf("New error = %v, want ErrInvalidConfig", err)
	}

	var native *pgconn.ParseConfigError

	if !errors.As(err, &native) || native.ConnString != dsn {
		t.Fatalf("New error lost native parse cause: %v", err)
	}

	if strings.Contains(err.Error(), "credential-") {
		t.Fatalf("configuration diagnostic leaked password fragments: %v", err)
	}
}

// TestNewRejectsNonpositiveHealthCheckPeriod prevents a native background ticker
// panic from invalid ordinary configuration. The constructor must return its
// configuration sentinel before starting pgxpool's maintenance goroutine.
func TestNewRejectsNonpositiveHealthCheckPeriod(t *testing.T) {
	t.Parallel()

	for _, period := range []string{"0", "-1s"} {
		driver, err := postgres.New("postgres://fixture@127.0.0.1:1/db?pool_health_check_period=" + period)

		if driver != nil {
			t.Cleanup(func() {
				if err := driver.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			})
		}

		if !errors.Is(err, postgres.ErrInvalidConfig) {
			t.Errorf("health period %q = %v, want ErrInvalidConfig", period, err)
		}
	}
}

// TestBuilderAcceptsConnectionSettings checks all pool setters on a zero draft.
// MaxIdleConns remains accepted for symmetry, without a native pgx setting.
func TestBuilderAcceptsConnectionSettings(t *testing.T) {
	t.Parallel()

	databaseClient := &postgres.Builder{}

	if databaseClient.MaxOpenConns(2) != databaseClient || databaseClient.MaxIdleConns(1) != databaseClient {
		t.Fatal("connection limit setters changed the draft")
	}

	if databaseClient.ConnMaxLifetime(time.Minute) != databaseClient || databaseClient.ConnMaxIdleTime(time.Second) != databaseClient {
		t.Fatal("connection age setters changed the draft")
	}
}
