package mysqldatabase_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/databasetest"
	mysql "arkoren.dev/driver/mysql/mysqldatabase"
	"arkoren.dev/foundation/query"
)

// TestBuilderAcceptsConnectionSettings checks all pool setters on a zero draft.
// Constructors validate the completed values rather than intermediate edits.
func TestBuilderAcceptsConnectionSettings(t *testing.T) {
	t.Parallel()

	databaseClient := &mysql.Builder{}

	if databaseClient.MaxOpenConns(2) != databaseClient || databaseClient.MaxIdleConns(1) != databaseClient {
		t.Fatal("connection limit setters changed the draft")
	}

	if databaseClient.ConnMaxLifetime(time.Minute) != databaseClient || databaseClient.ConnMaxIdleTime(time.Second) != databaseClient {
		t.Fatal("connection age setters changed the draft")
	}
}

const (
	// mysqlDatabaseName is the schema created for integration tests.
	mysqlDatabaseName = "arkoren_integration"

	// mysqlRootPassword authenticates the MySQL integration service.
	mysqlRootPassword = "arkoren-integration"

	// databaseTestTimeout bounds each MySQL database operation.
	databaseTestTimeout = 10 * time.Second

	// databaseCleanupTimeout bounds integration-test cleanup.
	databaseCleanupTimeout = 10 * time.Second
)

// mysqlRecord represents a row used by MySQL integration tests.
type mysqlRecord struct {
	// ID is the test record identifier.
	ID int `db:"id"`

	// Name is the test record name.
	Name string `db:"name"`
}

// mysqlConnection exposes the connection lifecycle methods used by tests.
type mysqlConnection interface {
	// Stop releases the database connection.
	Stop(context.Context) error

	// Ping verifies the database connection.
	Ping(context.Context) error
}

// TestMySQLPing verifies public readiness without creating a test table first.
// This keeps connection, authentication, and startup failures separate from DDL.
func TestMySQLPing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	_, connection := newMySQLDriver(t)
	registerMySQLConnectionCleanup(t, connection)

	if err := connection.Ping(testContext(t)); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

// TestMySQLNamedInsertAndQuery verifies named query execution against MySQL.
func TestMySQLNamedInsertAndQuery(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	databaseClient, _, table := newMySQLDatabase(t)
	operationContext := testContext(t)
	record := mysqlRecord{
		ID:   1,
		Name: "named query",
	}

	insertMySQLRecord(t, databaseClient, operationContext, table, record)

	statement := fmt.Sprintf("SELECT id, name FROM %s WHERE id = :id", table)
	actual, err := databaseClient.Find[mysqlRecord](operationContext, query.RawNamed(statement, query.Values(mysqlRecord{
		ID: record.ID,
	})))

	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}

	if actual != record {
		t.Fatalf("Find() = %#v, want %#v", actual, record)
	}
}

// insertMySQLRecord inserts record through a named query.
func insertMySQLRecord(
	t *testing.T,
	databaseClient *database.Database,
	operationContext context.Context,
	table string,
	record mysqlRecord,
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

// TestMySQLNoRowsMapping verifies MySQL no-row errors map to the database sentinel.
func TestMySQLNoRowsMapping(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	databaseClient, _, table := newMySQLDatabase(t)
	statement := fmt.Sprintf("SELECT id, name FROM %s WHERE id = :id", table)

	_, err := databaseClient.Find[mysqlRecord](testContext(t), query.RawNamed(statement, query.Values(mysqlRecord{
		ID: 1,
	})))

	if !errors.Is(err, database.ErrNoRows) {
		t.Fatalf("Find() error = %v, want ErrNoRows", err)
	}
}

// TestMySQLTransactionCommits verifies successful MySQL transactions commit.
func TestMySQLTransactionCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	databaseClient, _, table := newMySQLDatabase(t)
	operationContext := testContext(t)
	record := mysqlRecord{
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
	var actual mysqlRecord

	actual, err = databaseClient.Find[mysqlRecord](operationContext, query.RawNamed(statement, query.Values(mysqlRecord{
		ID: record.ID,
	})))

	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}

	if actual != record {
		t.Fatalf("Find() = %#v, want %#v", actual, record)
	}
}

// TestMySQLTransactionRollsBackReturnedError verifies failed MySQL transactions roll back.
func TestMySQLTransactionRollsBackReturnedError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	databaseClient, _, table := newMySQLDatabase(t)
	operationContext := testContext(t)
	record := mysqlRecord{
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

	_, err = databaseClient.Find[mysqlRecord](operationContext, query.RawNamed(statement, query.Values(mysqlRecord{
		ID: record.ID,
	})))

	if !errors.Is(err, database.ErrNoRows) {
		t.Fatalf("Find() error = %v, want ErrNoRows", err)
	}
}

// newMySQLDatabase creates an isolated MySQL table and driver for a test.
func newMySQLDatabase(t *testing.T) (*database.Database, mysqlConnection, string) {
	t.Helper()

	operationContext := testContext(t)
	driver, connection := newMySQLDriver(t)
	registerMySQLConnectionCleanup(t, connection)
	table := tableName(t)
	createMySQLTable(t, driver, operationContext, table)
	registerMySQLTableCleanup(t, driver, table)

	return database.New(driver), connection, table
}

// newMySQLDriver creates a driver and exposes its connection lifecycle.
func newMySQLDriver(t *testing.T) (*mysql.Driver, mysqlConnection) {
	t.Helper()

	driver, err := mysql.New(mysqlConnectionURL())

	if err != nil {
		t.Fatalf("new MySQL driver: %v", err)
	}

	connection := mysqlConnection(driver)

	return driver, connection
}

// registerMySQLConnectionCleanup closes connection when the test completes.
func registerMySQLConnectionCleanup(t *testing.T, connection mysqlConnection) {
	t.Helper()

	t.Cleanup(func() {
		closeMySQLConnection(t, connection)
	})
}

// closeMySQLConnection closes connection and reports a cleanup failure.
func closeMySQLConnection(t *testing.T, connection mysqlConnection) {
	t.Helper()

	if err := connection.Stop(context.Background()); err != nil {
		t.Errorf("close MySQL driver: %v", err)
	}
}

// createMySQLTable creates the isolated table used by a test.
func createMySQLTable(
	t *testing.T,
	driver database.Driver,
	operationContext context.Context,
	table string,
) {
	t.Helper()

	statement := fmt.Sprintf("CREATE TABLE %s (id INT PRIMARY KEY, name VARCHAR(255) NOT NULL)", table)

	if _, err := driver.Exec(operationContext, statement); err != nil {
		t.Fatalf("create MySQL table: %v", err)
	}
}

// registerMySQLTableCleanup drops table when the test completes.
func registerMySQLTableCleanup(t *testing.T, driver database.Driver, table string) {
	t.Helper()

	t.Cleanup(func() {
		dropMySQLTable(t, driver, table)
	})
}

// dropMySQLTable drops table and reports a cleanup failure.
func dropMySQLTable(t *testing.T, driver database.Driver, table string) {
	t.Helper()

	cleanupContext, cleanupCancel := context.WithTimeout(
		context.WithoutCancel(t.Context()),
		databaseCleanupTimeout,
	)
	_, err := driver.Exec(cleanupContext, fmt.Sprintf("DROP TABLE %s", table))
	cleanupCancel()

	if err != nil {
		t.Errorf("drop MySQL table: %v", err)
	}
}

// mysqlConnectionURL builds the MySQL DSN for the integration-test server.
func mysqlConnectionURL() string {
	return fmt.Sprintf(
		"root:%s@tcp(%s)/%s?parseTime=true",
		mysqlRootPassword,
		"localhost:3306",
		mysqlDatabaseName,
	)
}

// tableName builds a unique MySQL table name from the test name.
func tableName(t *testing.T) string {
	t.Helper()

	bytes := make([]byte, 8)

	if _, err := rand.Read(bytes); err != nil {
		t.Fatalf("generate table suffix: %v", err)
	}

	name := strings.Map(mysqlTableNameCharacter, strings.ToLower(t.Name()))
	name = strings.Trim(name, "_")

	if len(name) > 32 {
		name = name[:32]
	}

	return fmt.Sprintf("arkoren_%s_%s", name, hex.EncodeToString(bytes))
}

// mysqlTableNameCharacter maps unsupported table-name runes to underscores.
func mysqlTableNameCharacter(character rune) rune {
	if isMySQLTableNameDigit(character) || isMySQLTableNameLetter(character) {
		return character
	}

	return '_'
}

// isMySQLTableNameDigit reports whether character is an ASCII digit.
func isMySQLTableNameDigit(character rune) bool {
	return character >= '0' && character <= '9'
}

// isMySQLTableNameLetter reports whether character is an ASCII lowercase letter.
func isMySQLTableNameLetter(character rune) bool {
	return character >= 'a' && character <= 'z'
}

// testContext returns a deadline-bound context for a MySQL test.
func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), databaseTestTimeout)
	t.Cleanup(cancel)

	return ctx
}

// TestConformance runs the database conformance suite against a live server; it is skipped under -short or without the DSN environment variable.
func TestConformance(t *testing.T) {
	dsn := integrationDSN(t)

	databasetest.Run(t, func(t *testing.T) database.Driver {
		t.Helper()

		driver, err := mysql.New(dsn)

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

// TestGoexitRollsBackTransaction verifies that a callback which never returns
// cannot commit. The real native driver translates its transaction methods to
// wire commands; no private wrapper state or synthetic driver interface is used.
func TestGoexitRollsBackTransaction(t *testing.T) {
	t.Parallel()

	commands := make(chan string, 8)
	driver := newProtocolDriver(t, "user@fixture(fixture)/db", func(statement string) {
		commands <- statement
	})
	var group sync.WaitGroup

	group.Go(func() {
		_ = driver.WithTransaction(t.Context(), func(database.Driver) error {
			runtime.Goexit()

			return nil
		})
	})
	group.Wait()
	close(commands)

	var got []string

	for command := range commands {
		got = append(got, command)
	}

	if !slices.Equal(got, []string{"START TRANSACTION", "ROLLBACK"}) {
		t.Fatalf("transaction commands = %q, want BEGIN then ROLLBACK", got)
	}
}

// TestGoexitRollsBackNestedTransaction checks both savepoint and outer cleanup.
// An unwinding goroutine must roll back nested work before releasing its
// savepoint, then roll back the outer transaction rather than committing it.
func TestGoexitRollsBackNestedTransaction(t *testing.T) {
	t.Parallel()

	commands := make(chan string, 8)
	driver := newProtocolDriver(t, "user@fixture(fixture)/db", func(statement string) {
		commands <- statement
	})
	var group sync.WaitGroup

	group.Go(func() {
		_ = driver.WithTransaction(t.Context(), func(tx database.Driver) error {
			return tx.WithTransaction(t.Context(), func(database.Driver) error {
				runtime.Goexit()

				return nil
			})
		})
	})
	group.Wait()
	close(commands)

	var got []string

	for command := range commands {
		got = append(got, command)
	}

	want := []string{"START TRANSACTION", "SAVEPOINT arkoren_1", "ROLLBACK TO SAVEPOINT arkoren_1", "RELEASE SAVEPOINT arkoren_1", "ROLLBACK"}

	if !slices.Equal(got, want) {
		t.Fatalf("nested transaction commands = %q, want %q", got, want)
	}
}
