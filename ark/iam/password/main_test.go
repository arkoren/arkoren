package password_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	mysql "arkoren.dev/driver/mysql/mysqldatabase"
	postgres "arkoren.dev/driver/postgres/postgresdatabase"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
)

// Connection settings of the services in compose.yaml.
const (
	postgresDSN = "postgres://postgres:arkoren-integration@127.0.0.1:25432/arkoren_access?sslmode=disable"
	mysqlDSN    = "root:arkoren-integration@tcp(127.0.0.1:23306)/arkoren_access?parseTime=true"
)

// testContext returns a context canceled when the test ends.
func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	return ctx
}

// newInstance returns a unique instance name, so scenarios share one server
// without sharing tables.
func newInstance(t *testing.T) string {
	t.Helper()

	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("random instance: %v", err)
	}

	return "t" + hex.EncodeToString(raw[:])
}

// eachDatabase runs scenario once on in-memory SQLite and, outside -short, on
// PostgreSQL and MySQL from compose.yaml. Each run gets a fresh instance name
// and a database that is stopped when the subtest ends.
func eachDatabase(t *testing.T, scenario func(t *testing.T, db *database.Database, instance string)) {
	t.Helper()

	run := func(name string, open func(t *testing.T) database.Driver) {
		t.Run(name, func(t *testing.T) {
			driver := open(t)
			db := database.New(driver)
			scenario(t, db, newInstance(t))
		})
	}
	run("sqlite", func(t *testing.T) database.Driver {
		driver, err := sqlite.New(":memory:")
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		start(t, driver)

		return driver
	})
	if testing.Short() {
		return
	}
	run("postgres", func(t *testing.T) database.Driver {
		driver, err := postgres.New(postgresDSN)
		if err != nil {
			t.Fatalf("postgres: %v", err)
		}
		start(t, driver)

		return driver
	})
	run("mysql", func(t *testing.T) database.Driver {
		driver, err := mysql.New(mysqlDSN)
		if err != nil {
			t.Fatalf("mysql: %v", err)
		}
		start(t, driver)

		return driver
	})
}

// lifecycle is the part of a database driver the tests start and stop.
type lifecycle interface {
	database.Driver
	Start(context.Context) error
	Stop(context.Context) error
}

// start connects driver and stops it when the test ends.
func start(t *testing.T, driver lifecycle) {
	t.Helper()

	if err := driver.Start(testContext(t)); err != nil {
		t.Fatalf("start database: %v", err)
	}
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Errorf("stop database: %v", err)
		}
	})
}

// migrateUp executes definitions on db.
func migrateUp(t *testing.T, db *database.Database, definitions ...migrate.Definition) {
	t.Helper()

	migrator := migrate.New(db)
	if err := migrator.Add(definitions...); err != nil {
		t.Fatalf("add migrations: %v", err)
	}
	if err := migrator.Up(testContext(t)); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
}
