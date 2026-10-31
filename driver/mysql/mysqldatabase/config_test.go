package mysqldatabase_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	mysql "arkoren.dev/driver/mysql/mysqldatabase"
	"arkoren.dev/foundation/query"
)

// TestBuilderPoliciesRunOnceInOrder verifies fresh shared drafts, ordered edits,
// and publication independent of a retained builder, without opening a socket.
func TestBuilderPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *mysql.Builder
	calls := 0

	driver, err := mysql.New("user@tcp(127.0.0.1:1)/db", func(databaseClient *mysql.Builder) {
		calls++
		retained = databaseClient
		databaseClient.MaxOpenConns(1)
	}, func(databaseClient *mysql.Builder) {
		calls++

		if databaseClient != retained {
			t.Fatal("callbacks received different drafts")
		}

		databaseClient.MaxOpenConns(2)
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

	if calls != 2 || driver.SQL().Stats().MaxOpenConnections != 2 {
		t.Fatalf("calls = %d, pool = %+v", calls, driver.SQL().Stats())
	}
}

// TestBuilderCallbacksRunBeforeValidation verifies that invalid required inputs
// still receive each policy once before the normal configuration error.
func TestBuilderCallbacksRunBeforeValidation(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := mysql.New("", func(databaseClient *mysql.Builder) {
		calls++
		databaseClient.MaxOpenConns(2)
	})

	if err == nil || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
}

// TestBuilderNilCallbackPanics distinguishes a supplied nil policy from a nil
// expanded callback slice, which supplies no callbacks.
func TestBuilderNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = mysql.New("", nil)
}

// TestBuilderPanicPropagates checks application panic identity and that later
// policies are not invoked after a failed construction callback.
func TestBuilderPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := new(int)

	defer func() {
		if recover() != marker {
			t.Fatal("constructor changed the callback panic")
		}
	}()

	_, _ = mysql.New("", func(*mysql.Builder) {
		panic(marker)
	}, func(*mysql.Builder) {
		t.Fatal("callback ran after panic")
	})
}

// TestNewFromNilCallbackPanics checks wrapper callbacks are called directly,
// before a nil native pool could be inspected or used.
func TestNewFromNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	mysql.NewFrom(nil, nil)
}

// TestBuilderEmptyPoliciesKeepDefaults verifies omitted, nil and empty policies
// all create fresh zero settings rather than carrying previous construction.
func TestBuilderEmptyPoliciesKeepDefaults(t *testing.T) {
	t.Parallel()

	for _, configure := range [][]func(*mysql.Builder){nil, {}} {
		driver, err := mysql.New("user@tcp(127.0.0.1:1)/db", configure...)

		if err != nil {
			t.Fatal(err)
		}

		if driver.SQL().Stats().MaxOpenConnections != 0 {
			t.Error("default open limit changed")
		}

		if err := driver.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNewFromOwnsPoolAndKeepsNonpositiveLimits verifies wrapper applicability:
// negative settings are ignored, existing native limits survive, and Stop closes
// the exact transferred pool even though it has never connected.
func TestNewFromOwnsPoolAndKeepsNonpositiveLimits(t *testing.T) {
	t.Parallel()

	pool, err := sql.Open("mysql", "user@tcp(127.0.0.1:1)/db")

	if err != nil {
		t.Fatal(err)
	}

	pool.SetMaxOpenConns(3)

	driver := mysql.NewFrom(pool, func(databaseClient *mysql.Builder) {
		databaseClient.MaxOpenConns(-1)
		databaseClient.MaxIdleConns(-1)
		databaseClient.ConnMaxLifetime(-1)
		databaseClient.ConnMaxIdleTime(-1)
	})

	if pool.Stats().MaxOpenConnections != 3 {
		t.Error("nonpositive limit changed native pool")
	}

	if err := driver.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := pool.PingContext(t.Context()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("transferred pool remains open: %v", err)
	}
}

// TestNewRejectsBlankDSN verifies New rejects a blank DSN before connecting.
func TestNewRejectsBlankDSN(t *testing.T) {
	t.Parallel()

	if _, err := mysql.New(" \n"); !errors.Is(err, mysql.ErrInvalidConfig) {
		t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsNegativePoolLimits verifies every negative pool limit is
// invalid.
func TestNewRejectsNegativePoolLimits(t *testing.T) {
	t.Parallel()

	for _, configure := range []func(*mysql.Builder){
		func(databaseClient *mysql.Builder) {
			databaseClient.MaxOpenConns(-1)
		},
		func(databaseClient *mysql.Builder) {
			databaseClient.MaxIdleConns(-1)
		},
		func(databaseClient *mysql.Builder) {
			databaseClient.ConnMaxLifetime(-time.Nanosecond)
		},
		func(databaseClient *mysql.Builder) {
			databaseClient.ConnMaxIdleTime(-time.Nanosecond)
		},
	} {
		if _, err := mysql.New("user@tcp(127.0.0.1:1)/db", configure); !errors.Is(err, mysql.ErrInvalidConfig) {
			t.Errorf("New() error = %v, want ErrInvalidConfig", err)
		}
	}
}

// TestNewConnectsLazily verifies New does no network I/O and applies the pool
// limit.
func TestNewConnectsLazily(t *testing.T) {
	t.Parallel()

	driver, err := mysql.New("user@tcp(127.0.0.1:1)/db", func(databaseClient *mysql.Builder) {
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

	if got := driver.SQL().Stats().MaxOpenConnections; got != 3 {
		t.Fatalf("MaxOpenConnections = %d, want 3", got)
	}

	if driver.Dialect() != query.MySQL {
		t.Fatalf("Dialect() = %v, want MySQL", driver.Dialect())
	}
}

// TestNewFromWrapsPool verifies NewFrom uses the caller's pool.
func TestNewFromWrapsPool(t *testing.T) {
	t.Parallel()

	pool, err := sql.Open("mysql", "user@tcp(127.0.0.1:1)/db")

	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	var retained *mysql.Builder

	driver := mysql.NewFrom(pool, func(databaseClient *mysql.Builder) {
		calls++
		retained = databaseClient
		databaseClient.MaxOpenConns(2)
	})

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if driver.SQL() != pool {
		t.Fatal("SQL() is not the pool passed to NewFrom")
	}

	retained.MaxOpenConns(9)

	if calls != 1 {
		t.Fatalf("callbacks = %d, want 1", calls)
	}

	if got := pool.Stats().MaxOpenConnections; got != 2 {
		t.Fatalf("MaxOpenConnections = %d, want 2", got)
	}
}

// TestNewRejectsMalformedDSNWithSafeDiagnostics checks native parsing failures
// have the package's configuration identity without printing DSN parameter data.
// ErrorsAs may still inspect the original native cause for explicit recovery.
func TestNewRejectsMalformedDSNWithSafeDiagnostics(t *testing.T) {
	t.Parallel()

	_, err := mysql.New("user:private-password@tcp(localhost:3306)/db?parseTime=private-option")

	if !errors.Is(err, mysql.ErrInvalidConfig) {
		t.Errorf("New error = %v, want ErrInvalidConfig", err)
	}

	if err == nil {
		t.Fatal("New accepted a malformed native parameter")
	}

	if strings.Contains(err.Error(), "private-") {
		t.Fatalf("configuration diagnostic leaked DSN data: %v", err)
	}
}

// TestParseTimeDefaultIgnoresPasswordText proves policy detection considers
// native DSN parameter boundaries, not a parseTime= substring in a password.
func TestParseTimeDefaultIgnoresPasswordText(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t, "user:parseTime=secret@fixture(fixture)/db", nil)
	var value time.Time

	if err := driver.Find(t.Context(), "SELECT DATETIME", &value); err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, time.October, 30, 12, 34, 56, 0, time.UTC)

	if !value.Equal(want) {
		t.Fatalf("DATETIME = %v, want %v", value, want)
	}
}

// TestParseTimeDefaultIgnoresPasswordQuestionMark checks a question mark before
// the native database-name separator cannot turn the appended query into text
// belonging to the database name instead of a parseTime parameter.
func TestParseTimeDefaultIgnoresPasswordQuestionMark(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t, "user:why?secret@fixture(fixture)/db", nil)
	var value time.Time

	if err := driver.Find(t.Context(), "SELECT DATETIME", &value); err != nil {
		t.Fatal(err)
	}

	if value.Year() != 2026 {
		t.Fatalf("DATETIME = %v, want a parsed timestamp", value)
	}
}

// TestParseTimeExplicitFalseRemainsDisabled checks that the convenience default
// never overrides a native parameter intentionally requesting textual values.
func TestParseTimeExplicitFalseRemainsDisabled(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t, "user@fixture(fixture)/db?parseTime=false", nil)
	var value string

	if err := driver.Find(t.Context(), "SELECT DATETIME", &value); err != nil {
		t.Fatal(err)
	}

	if value != "2026-10-30 12:34:56" {
		t.Fatalf("DATETIME = %q, want native text", value)
	}
}
