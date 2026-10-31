package postgressearch_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/driver/postgres/postgresdatabase"
	"arkoren.dev/driver/postgres/postgressearch"
	"arkoren.dev/foundation/query"
)

// ansiDriver is a database driver that only reports a dialect other than
// PostgreSQL; NewFrom must reject it before any call.
type ansiDriver struct {
	// Driver supplies unused methods; construction must reject the dialect
	// before the test's deliberately absent implementation can be invoked.
	database.Driver
}

// Dialect reports the ANSI dialect.
func (ansiDriver) Dialect() query.Dialect {
	return query.ANSI
}

// TestBuilderPoliciesRunOnceInOrder verifies instance edits share one draft and
// publication fixes migration identity independently of retained draft changes.
func TestBuilderPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	db := openDatabase(t)
	var retained *postgressearch.Builder
	calls := 0

	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		calls++
		retained = engine

		if engine.Instance("first") != engine {
			t.Fatal("Instance did not return the draft")
		}
	}, func(engine *postgressearch.Builder) {
		calls++

		if engine != retained {
			t.Fatal("callbacks received different drafts")
		}

		engine.Instance("catalog")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Instance("changed")

	if calls != 2 || driver.Database() != db {
		t.Fatalf("callbacks=%d or database changed", calls)
	}

	for _, migration := range driver.Migrations() {
		if migration.Group() != "arkoren.dev:driver/postgres:postgressearch:catalog" {
			t.Fatalf("retained draft changed group: %q", migration.Group())
		}
	}
}

// TestBuilderPoliciesRunBeforeDatabaseValidation checks a nil database remains
// a domain error, after supplied callbacks have run once.
func TestBuilderPoliciesRunBeforeDatabaseValidation(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := postgressearch.NewFrom(nil, func(engine *postgressearch.Builder) {
		calls++
		engine.Instance("catalog")
	})

	if !errors.Is(err, postgressearch.ErrInvalidConfig) || calls != 1 {
		t.Fatalf("callbacks=%d error=%v", calls, err)
	}
}

// TestBuilderNilPolicyPanics checks nil callbacks are not filtered or converted
// to ordinary database validation errors.
func TestBuilderNilPolicyPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = postgressearch.NewFrom(nil, nil)
}

// TestBuilderPanicPropagates checks user panics remain construction misuse,
// not a wrapped database error, and later policies are not invoked.
func TestBuilderPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := new(int)

	defer func() {
		if recover() != marker {
			t.Fatal("constructor changed the callback panic")
		}
	}()

	_, _ = postgressearch.NewFrom(nil, func(*postgressearch.Builder) {
		panic(marker)
	}, func(*postgressearch.Builder) {
		t.Fatal("callback ran after panic")
	})
}

// TestBuilderEmptyPoliciesKeepDefaultGroup checks nil and empty expansions
// select the unchanged literal migration group without an instance suffix.
func TestBuilderEmptyPoliciesKeepDefaultGroup(t *testing.T) {
	t.Parallel()

	db := openDatabase(t)

	for _, configure := range [][]func(*postgressearch.Builder){nil, {}} {
		driver, err := postgressearch.NewFrom(db, configure...)

		if err != nil {
			t.Fatal(err)
		}

		if driver.Migrations()[0].Group() != "arkoren.dev:driver/postgres:postgressearch" {
			t.Fatal("default group changed")
		}
	}
}

// TestNewFromRejectsNilDatabase checks the required database dependency guard.
// Construction must return ErrInvalidConfig instead of publishing a nil-backed driver.
func TestNewFromRejectsNilDatabase(t *testing.T) {
	t.Parallel()

	if _, err := postgressearch.NewFrom(nil); !errors.Is(err, postgressearch.ErrInvalidConfig) {
		t.Fatalf("NewFrom(nil) error = %v, want ErrInvalidConfig", err)
	}
}

// TestNewFromRejectsInvalidInstances checks the identifier-safe instance grammar.
// Invalid characters and names over the length limit must return ErrInvalidConfig.
func TestNewFromRejectsInvalidInstances(t *testing.T) {
	t.Parallel()

	db := openDatabase(t)

	for _, name := range []string{"Upper", "has-dash", "with space", "dot.name", "café", strings.Repeat("a", 41)} {
		if _, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
			engine.Instance(name)
		}); !errors.Is(err, postgressearch.ErrInvalidConfig) {
			t.Errorf("NewFrom(instance %q) error = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// TestNewFromAcceptsValidInstances checks the accepted instance-name boundaries.
// The default name, digits, underscores, and the maximum length remain valid.
func TestNewFromAcceptsValidInstances(t *testing.T) {
	t.Parallel()

	db := openDatabase(t)

	for _, name := range []string{"", "a", "catalog_2", "0", strings.Repeat("z", 40)} {
		if _, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
			engine.Instance(name)
		}); err != nil {
			t.Errorf("NewFrom(instance %q) error = %v", name, err)
		}
	}
}

// TestNewFromRejectsOtherDialects checks the PostgreSQL-only database requirement.
// A database reporting ANSI SQL must fail construction with ErrInvalidConfig.
func TestNewFromRejectsOtherDialects(t *testing.T) {
	t.Parallel()

	if _, err := postgressearch.NewFrom(database.New(ansiDriver{})); !errors.Is(err, postgressearch.ErrInvalidConfig) {
		t.Fatalf("NewFrom(ANSI database) error = %v, want ErrInvalidConfig", err)
	}
}

// TestMigrationsAreGroupedPerInstance checks stable bookkeeping namespaces.
// Default and named instances must publish valid migrations in distinct groups.
func TestMigrationsAreGroupedPerInstance(t *testing.T) {
	t.Parallel()

	db := openDatabase(t)

	def, err := postgressearch.NewFrom(db)

	if err != nil {
		t.Fatal(err)
	}

	named, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance("catalog")
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, migration := range def.Migrations() {
		if migration.Err() != nil || migration.Group() != "arkoren.dev:driver/postgres:postgressearch" {
			t.Errorf("default migration %s group %q error %v", migration.ID(), migration.Group(), migration.Err())
		}
	}

	for _, migration := range named.Migrations() {
		if migration.Err() != nil || migration.Group() != "arkoren.dev:driver/postgres:postgressearch:catalog" {
			t.Errorf("instance migration %s group %q error %v", migration.ID(), migration.Group(), migration.Err())
		}
	}

	if got := len(def.Migrations()); got != 2 {
		t.Fatalf("migrations = %d, want one per table", got)
	}
}

// dsn is the PostgreSQL service of driver/postgres/compose.yaml.
const dsn = "postgres://postgres:arkoren-integration@127.0.0.1:15432/arkoren_integration?sslmode=disable"

// openDatabase returns a database over a lazily connecting pool that is
// stopped when the test ends. The pool connects on first use, so it also
// serves tests that never touch the server.
func openDatabase(t *testing.T) *database.Database {
	t.Helper()

	driver, err := postgresdatabase.New(dsn)

	if err != nil {
		t.Fatalf("new PostgreSQL driver: %v", err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Errorf("stop PostgreSQL driver: %v", err)
		}
	})

	return database.New(driver)
}

// integration skips the test in short mode and returns a database.
func integration(t *testing.T) *database.Database {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test")
	}

	return openDatabase(t)
}

// uniqueInstance returns a valid instance name unique to the call.
func uniqueInstance() string {
	return "t_" + strings.ToLower(rand.Text())[:12]
}

// newMigrator returns a migrator over db with its own bookkeeping table and
// lock, holding the migrations of the drivers, and drops the bookkeeping
// table when the test ends.
func newMigrator(t *testing.T, db *database.Database, drivers ...*postgressearch.Driver) *migrate.Migrator {
	t.Helper()

	name := uniqueInstance()
	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.Table(name + "_migrations")
	}, func(migrator *migrate.Builder) {
		migrator.LockName(name)
	})

	for _, driver := range drivers {
		if err := migrator.Add(driver.Migrations()...); err != nil {
			t.Fatalf("Add migrations: %v", err)
		}
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if _, err := db.Exec(ctx, query.Raw("DROP TABLE IF EXISTS "+name+"_migrations")); err != nil {
			t.Errorf("drop bookkeeping table: %v", err)
		}
	})

	return migrator
}

// migratedDriver returns a driver for a unique instance whose tables exist
// until the test ends.
func migratedDriver(t *testing.T, db *database.Database) *postgressearch.Driver {
	t.Helper()

	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance(uniqueInstance())
	})

	if err != nil {
		t.Fatalf("NewFrom: %v", err)
	}

	migrator := newMigrator(t, db, driver)

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	t.Cleanup(func() {
		if err := migrator.Rollback(context.Background()); err != nil {
			t.Errorf("migrate down: %v", err)
		}
	})

	return driver
}
