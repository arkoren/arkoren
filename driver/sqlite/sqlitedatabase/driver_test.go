package sqlitedatabase_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/databasetest"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
	"arkoren.dev/foundation/query"
)

// TestBuilderAcceptsConnectionSettings checks all pool setters on a zero draft.
// Construction copies these scalar settings before the draft can be reused.
func TestBuilderAcceptsConnectionSettings(t *testing.T) {
	t.Parallel()

	databaseClient := &sqlite.Builder{}

	if databaseClient.MaxOpenConns(2) != databaseClient || databaseClient.MaxIdleConns(1) != databaseClient {
		t.Fatal("connection limit setters changed the draft")
	}

	if databaseClient.ConnMaxLifetime(time.Minute) != databaseClient || databaseClient.ConnMaxIdleTime(time.Second) != databaseClient {
		t.Fatal("connection age setters changed the draft")
	}
}

const (
	// sqliteTestTimeout bounds SQLite test operations.
	sqliteTestTimeout = 2 * time.Minute

	// sqliteCleanupTimeout bounds SQLite test cleanup.
	sqliteCleanupTimeout = 30 * time.Second
)

// sqliteRecord represents a row used by SQLite integration tests.
type sqliteRecord struct {
	// ID is the test record identifier.
	ID int `db:"id"`

	// Name is the test record name.
	Name string `db:"name"`
}

// sqliteConnection exposes the connection lifecycle methods used by tests.
type sqliteConnection interface {
	// Stop releases the database connection.
	Stop(context.Context) error

	// Ping verifies the database connection.
	Ping(context.Context) error
}

// TestSQLitePing verifies the driver can ping SQLite.
func TestSQLitePing(t *testing.T) {
	t.Parallel()

	_, connection, _ := newSQLiteDatabase(t)

	if err := connection.Ping(sqliteTestContext(t)); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

// TestSQLiteNamedInsertAndQuery verifies named query execution against SQLite.
func TestSQLiteNamedInsertAndQuery(t *testing.T) {
	t.Parallel()

	databaseClient, _, table := newSQLiteDatabase(t)
	operationContext := sqliteTestContext(t)
	record := sqliteRecord{
		ID:   1,
		Name: "named query",
	}

	insertSQLiteRecord(t, databaseClient, operationContext, table, record)

	statement := fmt.Sprintf("SELECT id, name FROM %s WHERE id = :id", table)
	actual, err := databaseClient.Find[sqliteRecord](operationContext, query.RawNamed(statement, query.Values(sqliteRecord{
		ID: record.ID,
	})))

	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}

	if actual != record {
		t.Fatalf("Find() = %#v, want %#v", actual, record)
	}
}

// TestSQLiteQueryBuilderRoundTrip verifies builder statements compile for
// SQLite and execute through the facade.
func TestSQLiteQueryBuilderRoundTrip(t *testing.T) {
	t.Parallel()

	databaseClient, _, table := newSQLiteDatabase(t)
	operationContext := sqliteTestContext(t)

	inserted, err := databaseClient.Exec(operationContext, query.Table(table, func(statement *query.Builder) {
		statement.InsertRows(
			map[string]any{"id": 1, "name": "first"},
			map[string]any{"id": 2, "name": "second"},
		)
	}))

	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	if inserted != 2 {
		t.Fatalf("Exec() = %d, want 2", inserted)
	}

	records, err := databaseClient.Select[sqliteRecord](operationContext, query.Table(table, func(statement *query.Builder) {
		statement.Select("id", "name")
		statement.WhereIn("id", 1, 2)
		statement.OrderByDesc("id")
	}))

	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}

	want := []sqliteRecord{
		{
			ID:   2,
			Name: "second",
		},
		{
			ID:   1,
			Name: "first",
		},
	}

	if got := records.Items(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Select() = %#v, want %#v", got, want)
	}
}

// insertSQLiteRecord inserts record through a named query.
func insertSQLiteRecord(
	t *testing.T,
	databaseClient *database.Database,
	operationContext context.Context,
	table string,
	record sqliteRecord,
) {
	t.Helper()

	statement := fmt.Sprintf("INSERT INTO %s (id, name) VALUES (:id, :name)", table)

	rows, err := databaseClient.Exec(operationContext, query.RawNamed(statement, query.Values(record)))

	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	if rows != 1 {
		t.Fatalf("Exec() rows = %d, want 1", rows)
	}
}

// TestSQLiteNoRowsMapping verifies SQLite no-row errors map to the database sentinel.
func TestSQLiteNoRowsMapping(t *testing.T) {
	t.Parallel()

	databaseClient, _, table := newSQLiteDatabase(t)
	statement := fmt.Sprintf("SELECT id, name FROM %s WHERE id = :id", table)

	_, err := databaseClient.Find[sqliteRecord](sqliteTestContext(t), query.RawNamed(statement, query.Values(sqliteRecord{
		ID: 1,
	})))

	if !errors.Is(err, database.ErrNoRows) {
		t.Fatalf("Find() error = %v, want ErrNoRows", err)
	}
}

// TestSQLiteTransactionCommits verifies successful SQLite transactions commit.
func TestSQLiteTransactionCommits(t *testing.T) {
	t.Parallel()

	databaseClient, _, table := newSQLiteDatabase(t)
	operationContext := sqliteTestContext(t)
	record := sqliteRecord{
		ID:   1,
		Name: "committed",
	}

	err := databaseClient.WithTransaction(operationContext, func(txCtx context.Context) error {
		_, err := databaseClient.Exec(txCtx, query.Table(table, func(statement *query.Builder) {
			statement.Insert(map[string]any{"id": record.ID, "name": record.Name})
		}))

		return err
	})

	if err != nil {
		t.Fatalf("WithTransaction() error = %v", err)
	}

	statement := fmt.Sprintf("SELECT id, name FROM %s WHERE id = :id", table)
	var actual sqliteRecord
	actual, err = databaseClient.Find[sqliteRecord](operationContext, query.RawNamed(statement, query.Values(sqliteRecord{
		ID: record.ID,
	})))

	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}

	if actual != record {
		t.Fatalf("Find() = %#v, want %#v", actual, record)
	}
}

// TestSQLiteTransactionRollsBackReturnedError verifies failed SQLite transactions roll back.
func TestSQLiteTransactionRollsBackReturnedError(t *testing.T) {
	t.Parallel()

	databaseClient, _, table := newSQLiteDatabase(t)
	operationContext := sqliteTestContext(t)
	record := sqliteRecord{
		ID:   1,
		Name: "rolled back",
	}
	rollback := errors.New("rollback transaction")

	err := databaseClient.WithTransaction(operationContext, func(txCtx context.Context) error {
		_, err := databaseClient.Exec(txCtx, query.Table(table, func(statement *query.Builder) {
			statement.Insert(map[string]any{"id": record.ID, "name": record.Name})
		}))

		if err != nil {
			return err
		}

		return rollback
	})

	if !errors.Is(err, rollback) {
		t.Fatalf("WithTransaction() error = %v, want rollback error", err)
	}

	statement := fmt.Sprintf("SELECT id, name FROM %s WHERE id = :id", table)
	_, err = databaseClient.Find[sqliteRecord](operationContext, query.RawNamed(statement, query.Values(sqliteRecord{
		ID: record.ID,
	})))

	if !errors.Is(err, database.ErrNoRows) {
		t.Fatalf("Find() error = %v, want ErrNoRows", err)
	}
}

// newSQLiteDatabase creates an isolated SQLite table and driver for a test.
func newSQLiteDatabase(t *testing.T) (*database.Database, sqliteConnection, string) {
	t.Helper()

	operationContext := sqliteTestContext(t)
	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)

	table := sqliteTableName(t)
	createSQLiteTable(t, driver, operationContext, table)
	registerSQLiteTableCleanup(t, driver, table)

	return database.New(driver), connection, table
}

// newSQLiteDriver creates a driver and exposes its connection lifecycle.
func newSQLiteDriver(t *testing.T) (*sqlite.Driver, sqliteConnection) {
	t.Helper()

	driver, err := sqlite.New(":memory:", func(databaseClient *sqlite.Builder) {
		databaseClient.MaxIdleConns(1)
		databaseClient.MaxOpenConns(1)
	})

	if err != nil {
		t.Fatalf("new SQLite driver: %v", err)
	}

	connection := sqliteConnection(driver)

	return driver, connection
}

// registerSQLiteConnectionCleanup closes connection when the test completes.
func registerSQLiteConnectionCleanup(t *testing.T, connection sqliteConnection) {
	t.Helper()

	t.Cleanup(func() {
		closeSQLiteConnection(t, connection)
	})
}

// closeSQLiteConnection closes connection and reports a cleanup failure.
func closeSQLiteConnection(t *testing.T, connection sqliteConnection) {
	t.Helper()

	if err := connection.Stop(context.Background()); err != nil {
		t.Errorf("close SQLite driver: %v", err)
	}
}

// createSQLiteTable creates the isolated table used by a test.
func createSQLiteTable(
	t *testing.T,
	driver database.Driver,
	operationContext context.Context,
	table string,
) {
	t.Helper()

	statement := fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, name TEXT NOT NULL)", table)

	if _, err := driver.Exec(operationContext, statement); err != nil {
		t.Fatalf("create SQLite table: %v", err)
	}
}

// registerSQLiteTableCleanup drops table when the test completes.
func registerSQLiteTableCleanup(t *testing.T, driver database.Driver, table string) {
	t.Helper()

	t.Cleanup(func() {
		dropSQLiteTable(t, driver, table)
	})
}

// dropSQLiteTable drops table and reports a cleanup failure.
func dropSQLiteTable(t *testing.T, driver database.Driver, table string) {
	t.Helper()

	cleanupContext, cleanupCancel := context.WithTimeout(
		context.WithoutCancel(t.Context()),
		sqliteCleanupTimeout,
	)
	_, err := driver.Exec(cleanupContext, fmt.Sprintf("DROP TABLE %s", table))
	cleanupCancel()

	if err != nil {
		t.Errorf("drop SQLite table: %v", err)
	}
}

// sqliteTableName builds a unique SQLite table name from the test name.
func sqliteTableName(t *testing.T) string {
	t.Helper()

	bytes := make([]byte, 8)

	if _, err := rand.Read(bytes); err != nil {
		t.Fatalf("generate table suffix: %v", err)
	}

	name := strings.Map(sqliteTableNameCharacter, strings.ToLower(t.Name()))
	name = strings.Trim(name, "_")

	if len(name) > 32 {
		name = name[:32]
	}

	return fmt.Sprintf("arkoren_%s_%s", name, hex.EncodeToString(bytes))
}

// sqliteTableNameCharacter maps unsupported table-name runes to underscores.
func sqliteTableNameCharacter(character rune) rune {
	if isSQLiteTableNameDigit(character) || isSQLiteTableNameLetter(character) {
		return character
	}

	return '_'
}

// isSQLiteTableNameDigit reports whether character is an ASCII digit.
func isSQLiteTableNameDigit(character rune) bool {
	return character >= '0' && character <= '9'
}

// isSQLiteTableNameLetter reports whether character is an ASCII lowercase letter.
func isSQLiteTableNameLetter(character rune) bool {
	return character >= 'a' && character <= 'z'
}

// sqliteTestContext returns a deadline-bound context for a SQLite test.
func sqliteTestContext(t *testing.T) context.Context {
	t.Helper()

	operationContext, cancel := context.WithTimeout(t.Context(), sqliteTestTimeout)
	t.Cleanup(cancel)

	return operationContext
}

// requireTimeFormat verifies the DSN rewrite adds _time_format=sqlite once
// and leaves a caller-supplied _time_format alone.
func requireTimeFormat(t *testing.T, dsn, want string) {
	t.Helper()

	if got := sqlite.WithTimeFormat(dsn); got != want {
		t.Errorf("WithTimeFormat(%q) = %q, want %q", dsn, got, want)
	}
}

// TestWithTimeFormatBlank verifies the DSN rewrite for the blank case.
func TestWithTimeFormatBlank(t *testing.T) {
	t.Parallel()

	requireTimeFormat(t, "", "")
}

// TestWithTimeFormatMemory verifies the DSN rewrite for the memory case.
func TestWithTimeFormatMemory(t *testing.T) {
	t.Parallel()

	requireTimeFormat(t, ":memory:", ":memory:?_time_format=sqlite")
}

// TestWithTimeFormatPath verifies the DSN rewrite for the path case.
func TestWithTimeFormatPath(t *testing.T) {
	t.Parallel()

	requireTimeFormat(t, "app.db", "app.db?_time_format=sqlite")
}

// TestWithTimeFormatPathWithQuery verifies the DSN rewrite for the path with query case.
func TestWithTimeFormatPathWithQuery(t *testing.T) {
	t.Parallel()

	requireTimeFormat(t, "app.db?_pragma=busy_timeout(5000)", "app.db?_pragma=busy_timeout(5000)&_time_format=sqlite")
}

// TestWithTimeFormatFileURI verifies the DSN rewrite for the file uri case.
func TestWithTimeFormatFileURI(t *testing.T) {
	t.Parallel()

	requireTimeFormat(t, "file:app.db", "file:app.db?_time_format=sqlite")
}

// TestWithTimeFormatFileURIWithQuery verifies the DSN rewrite for the file uri with query case.
func TestWithTimeFormatFileURIWithQuery(t *testing.T) {
	t.Parallel()

	requireTimeFormat(t, "file:app.db?mode=ro", "file:app.db?mode=ro&_time_format=sqlite")
}

// TestWithTimeFormatSharedMemoryURI verifies the DSN rewrite for the shared memory uri case.
func TestWithTimeFormatSharedMemoryURI(t *testing.T) {
	t.Parallel()

	requireTimeFormat(t, "file:x?mode=memory&cache=shared", "file:x?mode=memory&cache=shared&_time_format=sqlite")
}

// TestWithTimeFormatAlreadySet verifies the DSN rewrite for the already set case.
func TestWithTimeFormatAlreadySet(t *testing.T) {
	t.Parallel()

	requireTimeFormat(t, "app.db?_time_format=sqlite", "app.db?_time_format=sqlite")
}

// TestWithTimeFormatAlreadySetOther verifies the DSN rewrite for the already set other case.
func TestWithTimeFormatAlreadySetOther(t *testing.T) {
	t.Parallel()

	requireTimeFormat(t, "file:app.db?mode=ro&_time_format=sqlite", "file:app.db?mode=ro&_time_format=sqlite")
}

// TestSQLiteTimeRoundTrip verifies a time.Time written to DATE, DATETIME, and
// TIMESTAMP columns scans back equal, and that CURRENT_TIMESTAMP defaults scan.
func TestSQLiteTimeRoundTrip(t *testing.T) {
	t.Parallel()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)
	ctx := sqliteTestContext(t)
	databaseClient := database.New(driver)

	_, err := databaseClient.Exec(ctx, query.Raw(
		"CREATE TABLE events (id INTEGER PRIMARY KEY, day DATE, at DATETIME, at_tz TIMESTAMP, made DATETIME DEFAULT CURRENT_TIMESTAMP)"))

	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	day := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	at := time.Date(2026, 3, 14, 15, 9, 26, 535897000, time.UTC)
	atTZ := time.Date(2026, 3, 14, 15, 9, 26, 0, time.FixedZone("", 2*60*60))

	_, err = databaseClient.Exec(ctx, query.Raw("INSERT INTO events (id, day, at, at_tz) VALUES (1, ?, ?, ?)", day, at, atTZ))

	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var got struct {
		// Day checks date-only values without losing their midnight instant.
		Day time.Time `db:"day"`

		// At checks timestamp precision through the configured native format.
		At time.Time `db:"at"`

		// AtTZ checks that a timestamp's offset preserves its absolute instant.
		AtTZ time.Time `db:"at_tz"`

		// Made checks that SQLite's CURRENT_TIMESTAMP default scans as nonzero.
		Made time.Time `db:"made"`
	}

	if err := databaseClient.Driver().Find(ctx, "SELECT day, at, at_tz, made FROM events WHERE id = 1", &got); err != nil {
		t.Fatalf("select: %v", err)
	}

	if !got.Day.Equal(day) || !got.At.Equal(at) || !got.AtTZ.Equal(atTZ) {
		t.Errorf("round trip = %v, %v, %v; want %v, %v, %v", got.Day, got.At, got.AtTZ, day, at, atTZ)
	}

	if got.Made.IsZero() {
		t.Error("CURRENT_TIMESTAMP default scanned as zero time")
	}
}

// TestNewRejectsBlankDSN verifies New rejects a blank DSN before connecting.
func TestNewRejectsBlankDSN(t *testing.T) {
	t.Parallel()

	if _, err := sqlite.New(" \n"); !errors.Is(err, sqlite.ErrInvalidConfig) {
		t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsNegativePoolLimits verifies every negative pool limit is
// invalid.
func TestNewRejectsNegativePoolLimits(t *testing.T) {
	t.Parallel()

	for _, configure := range []func(*sqlite.Builder){
		func(databaseClient *sqlite.Builder) {
			databaseClient.MaxOpenConns(-1)
		},
		func(databaseClient *sqlite.Builder) {
			databaseClient.MaxIdleConns(-1)
		},
		func(databaseClient *sqlite.Builder) {
			databaseClient.ConnMaxLifetime(-time.Nanosecond)
		},
		func(databaseClient *sqlite.Builder) {
			databaseClient.ConnMaxIdleTime(-time.Nanosecond)
		},
	} {
		if _, err := sqlite.New(":memory:", configure); !errors.Is(err, sqlite.ErrInvalidConfig) {
			t.Errorf("New() error = %v, want ErrInvalidConfig", err)
		}
	}
}

// TestNewFromWrapsPool verifies NewFrom uses the caller's pool, applies the
// pool limits, and reports the SQLite dialect.
func TestNewFromWrapsPool(t *testing.T) {
	t.Parallel()

	pool, err := sql.Open("sqlite", ":memory:")

	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	var retained *sqlite.Builder

	driver := sqlite.NewFrom(pool, func(databaseClient *sqlite.Builder) {
		calls++
		retained = databaseClient
		databaseClient.MaxOpenConns(1)
	})
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if driver.SQL() != pool {
		t.Fatal("SQL() is not the pool passed to NewFrom")
	}

	if driver.Dialect() != query.SQLite {
		t.Fatalf("Dialect() = %v, want SQLite", driver.Dialect())
	}

	retained.MaxOpenConns(9)

	if calls != 1 {
		t.Fatalf("callbacks = %d, want 1", calls)
	}

	if got := pool.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}

// TestMemoryDSNsDefaultToOneConnection covers native private and named-memory
// spellings, including parameters and percent-encoded SQLite URI mode values.
// Native Stats makes the constructor's policy observable without pool timing.
func TestMemoryDSNsDefaultToOneConnection(t *testing.T) {
	t.Parallel()

	for _, dsn := range []string{
		":memory:",
		":memory:?_pragma=foreign_keys(1)",
		"file::memory:",
		"file::memory:?cache=shared",
		"file:arkoren-memory?mode=%6demory&cache=shared",
	} {
		driver, err := sqlite.New(dsn)

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})

		if got := driver.SQL().Stats().MaxOpenConnections; got != 1 {
			t.Errorf("New(%q) MaxOpenConnections = %d, want 1", dsn, got)
		}

		var file string

		if err := driver.Find(t.Context(), "SELECT file FROM pragma_database_list WHERE name = 'main'", &file); err != nil {
			t.Fatal(err)
		}

		if file != "" {
			t.Errorf("New(%q) opened file %q, want native memory identity", dsn, file)
		}
	}
}

// TestFileNameDoesNotActivateMemoryPolicy checks that a mode=memory substring
// in a path is not mistaken for the native file URI's mode query parameter.
func TestFileNameDoesNotActivateMemoryPolicy(t *testing.T) {
	t.Parallel()

	driver, err := sqlite.New(filepath.Join(t.TempDir(), "mode=memory.sqlite"))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if got := driver.SQL().Stats().MaxOpenConnections; got != 0 {
		t.Fatalf("file-backed MaxOpenConnections = %d, want native default 0", got)
	}
}

// TestTimestampDefaultIgnoresFileName exercises actual writes and scans on a
// filename containing _time_format=. Only a native query parameter may suppress
// the driver's timestamp default; filename text must not change its policy.
func TestTimestampDefaultIgnoresFileName(t *testing.T) {
	t.Parallel()

	driver, err := sqlite.New(filepath.Join(t.TempDir(), "_time_format=notes.sqlite"))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx := t.Context()

	if _, err := driver.Exec(ctx, "CREATE TABLE events (at DATETIME)"); err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, time.October, 30, 12, 34, 56, 0, time.UTC)

	if _, err := driver.Exec(ctx, "INSERT INTO events VALUES (?)", want); err != nil {
		t.Fatal(err)
	}

	var got time.Time

	if err := driver.Find(ctx, "SELECT at FROM events", &got); err != nil {
		t.Fatal(err)
	}

	if !got.Equal(want) {
		t.Fatalf("timestamp = %v, want %v", got, want)
	}

	var stored string

	if err := driver.Find(ctx, "SELECT CAST(at AS TEXT) FROM events", &stored); err != nil {
		t.Fatal(err)
	}

	if stored != "2026-10-30 12:34:56+00:00" {
		t.Fatalf("stored timestamp = %q, want the documented SQLite format", stored)
	}
}

// TestBuilderPoliciesRunOnceInOrder verifies same-draft ordered callbacks,
// private completed-settings delegation, and independence from retained drafts.
func TestBuilderPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *sqlite.Builder
	calls := 0

	driver, err := sqlite.New(":memory:", func(databaseClient *sqlite.Builder) {
		calls++
		retained = databaseClient
		databaseClient.MaxOpenConns(1)
	}, func(databaseClient *sqlite.Builder) {
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

// TestBuilderCallbacksRunBeforeValidation verifies invalid required inputs still
// receive policies, before New returns the ordinary configuration error.
func TestBuilderCallbacksRunBeforeValidation(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := sqlite.New("", func(databaseClient *sqlite.Builder) {
		calls++
		databaseClient.MaxOpenConns(2)
	})

	if err == nil || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
}

// TestBuilderNilCallbackPanics checks that nil entries remain misuse even when
// required inputs are invalid; omission is tested separately.
func TestBuilderNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	_, _ = sqlite.New("", nil)
}

// TestBuilderPanicPropagates checks callback panics propagate unchanged and
// construction never invokes a later policy after the panic.
func TestBuilderPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := new(int)

	defer func() {
		if recover() != marker {
			t.Fatal("constructor changed the callback panic")
		}
	}()

	_, _ = sqlite.New("", func(*sqlite.Builder) {
		panic(marker)
	}, func(*sqlite.Builder) {
		t.Fatal("callback ran after panic")
	})
}

// TestNewFromNilCallbackPanics checks NewFrom does not filter nil policies or
// conflate them with nil callback slice expansion.
func TestNewFromNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	sqlite.NewFrom(nil, nil)
}

// TestBuilderEmptyPoliciesKeepDefaults verifies nil and empty expansions keep
// the existing in-memory single-connection default on each fresh construction.
func TestBuilderEmptyPoliciesKeepDefaults(t *testing.T) {
	t.Parallel()

	for _, configure := range [][]func(*sqlite.Builder){nil, {}} {
		driver, err := sqlite.New(":memory:", configure...)

		if err != nil {
			t.Fatal(err)
		}

		if driver.SQL().Stats().MaxOpenConnections != 1 {
			t.Error("in-memory default open limit changed")
		}

		if err := driver.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNewFromOwnsPoolAndKeepsNonpositiveLimits verifies NewFrom does not apply
// New's in-memory policy and Stop closes the transferred native pool.
func TestNewFromOwnsPoolAndKeepsNonpositiveLimits(t *testing.T) {
	t.Parallel()

	pool, err := sql.Open("sqlite", ":memory:")

	if err != nil {
		t.Fatal(err)
	}

	pool.SetMaxOpenConns(3)

	driver := sqlite.NewFrom(pool, func(databaseClient *sqlite.Builder) {
		databaseClient.MaxOpenConns(-1)
		databaseClient.MaxIdleConns(-1)
		databaseClient.ConnMaxLifetime(-1)
		databaseClient.ConnMaxIdleTime(-1)
	})

	if pool.Stats().MaxOpenConnections != 3 {
		t.Error("NewFrom replaced native pool limits")
	}

	if err := driver.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := pool.PingContext(t.Context()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("transferred pool remains open: %v", err)
	}
}

// TestConformance runs the database conformance suite.
func TestConformance(t *testing.T) {
	databasetest.Run(t, func(t *testing.T) database.Driver {
		t.Helper()

		driver, err := sqlite.New(filepath.Join(t.TempDir(), "conformance.db"))

		if err != nil {
			t.Fatalf("new driver: %v", err)
		}

		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Errorf("close driver: %v", err)
			}
		})

		return driver
	})
}
