package postgresdatabase_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/databasetest"
	postgres "arkoren.dev/driver/postgres/postgresdatabase"
	"arkoren.dev/foundation/query"
)

const (
	// postgresDatabaseName is the schema created for integration tests.
	postgresDatabaseName = "arkoren_integration"

	// postgresPassword authenticates the PostgreSQL integration service.
	postgresPassword = "arkoren-integration"

	// postgresOperationTimeout bounds each PostgreSQL database operation.
	postgresOperationTimeout = 10 * time.Second

	// postgresCleanupTimeout bounds integration-test cleanup.
	postgresCleanupTimeout = 10 * time.Second
)

// postgresEndpoint identifies the shared PostgreSQL integration-test server.
type postgresEndpoint struct {
	// host is the server hostname.
	host string

	// port is the server port.
	port string
}

// sharedPostgresEndpoint identifies the shared PostgreSQL integration-test server.
var sharedPostgresEndpoint = postgresEndpoint{
	host: "localhost",
	port: "15432",
}

// postgresRecord represents a row used by PostgreSQL integration tests.
type postgresRecord struct {
	// ID is the test record identifier.
	ID int `db:"id"`

	// Name is the test record name.
	Name string `db:"name"`
}

// postgresConnection exposes the connection lifecycle methods used by tests.
type postgresConnection interface {
	// Stop releases the database connection.
	Stop(context.Context) error

	// Ping verifies the database connection.
	Ping(context.Context) error
}

// TestPostgresPing verifies the driver can ping PostgreSQL.
func TestPostgresPing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	_, connection, _ := newPostgresDatabase(t)

	if err := connection.Ping(postgresTestContext(t)); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

// TestPostgresNamedInsertAndQuery verifies named query execution against PostgreSQL.
func TestPostgresNamedInsertAndQuery(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	databaseClient, _, table := newPostgresDatabase(t)
	operationContext := postgresTestContext(t)
	record := postgresRecord{
		ID:   1,
		Name: "named query",
	}

	insertPostgresRecord(t, databaseClient, operationContext, table, record)

	statement := fmt.Sprintf("SELECT id, name FROM %s WHERE id = :id", table)

	actual, err := databaseClient.Find[postgresRecord](operationContext, query.RawNamed(statement, query.Values(postgresRecord{
		ID: record.ID,
	})))

	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}

	if actual != record {
		t.Fatalf("Find() = %#v, want %#v", actual, record)
	}
}

// insertPostgresRecord inserts record through a named query.
func insertPostgresRecord(
	t *testing.T,
	databaseClient *database.Database,
	operationContext context.Context,
	table string,
	record postgresRecord,
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

// TestPostgresNoRowsMapping verifies PostgreSQL no-row errors map to the database sentinel.
func TestPostgresNoRowsMapping(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	databaseClient, _, table := newPostgresDatabase(t)
	statement := fmt.Sprintf("SELECT id, name FROM %s WHERE id = :id", table)

	_, err := databaseClient.Find[postgresRecord](postgresTestContext(t), query.RawNamed(statement, query.Values(postgresRecord{
		ID: 1,
	})))

	if !errors.Is(err, database.ErrNoRows) {
		t.Fatalf("Find() error = %v, want ErrNoRows", err)
	}
}

// TestPostgresTransactionCommits verifies successful PostgreSQL transactions commit.
func TestPostgresTransactionCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	databaseClient, _, table := newPostgresDatabase(t)
	operationContext := postgresTestContext(t)
	record := postgresRecord{
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
	var actual postgresRecord

	actual, err = databaseClient.Find[postgresRecord](operationContext, query.RawNamed(statement, query.Values(postgresRecord{
		ID: record.ID,
	})))

	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}

	if actual != record {
		t.Fatalf("Find() = %#v, want %#v", actual, record)
	}
}

// TestPostgresTransactionRollsBackReturnedError verifies failed PostgreSQL transactions roll back.
func TestPostgresTransactionRollsBackReturnedError(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	databaseClient, _, table := newPostgresDatabase(t)
	operationContext := postgresTestContext(t)
	record := postgresRecord{
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

	_, err = databaseClient.Find[postgresRecord](operationContext, query.RawNamed(statement, query.Values(postgresRecord{
		ID: record.ID,
	})))

	if !errors.Is(err, database.ErrNoRows) {
		t.Fatalf("Find() error = %v, want ErrNoRows", err)
	}
}

// newPostgresDatabase creates an isolated PostgreSQL table and driver for a test.
func newPostgresDatabase(t *testing.T) (*database.Database, postgresConnection, string) {
	t.Helper()

	driver, connection := newPostgresDriver(t)
	registerPostgresConnectionCleanup(t, connection)

	table := postgresTableName(t)
	operationContext := postgresTestContext(t)

	createPostgresTable(t, driver, operationContext, table)
	registerPostgresTableCleanup(t, driver, table)

	return database.New(driver), connection, table
}

// newPostgresDriver creates a driver and exposes its connection lifecycle.
func newPostgresDriver(t *testing.T) (*postgres.Driver, postgresConnection) {
	t.Helper()

	driver, err := postgres.New(postgresConnectionURL(sharedPostgresEndpoint.host, sharedPostgresEndpoint.port))

	if err != nil {
		t.Fatalf("new PostgreSQL driver: %v", err)
	}

	connection := postgresConnection(driver)

	return driver, connection
}

// registerPostgresConnectionCleanup closes connection when the test completes.
func registerPostgresConnectionCleanup(t *testing.T, connection postgresConnection) {
	t.Helper()

	t.Cleanup(func() {
		closePostgresConnection(t, connection)
	})
}

// closePostgresConnection closes connection and reports a cleanup failure.
func closePostgresConnection(t *testing.T, connection postgresConnection) {
	t.Helper()

	if err := connection.Stop(context.Background()); err != nil {
		t.Errorf("close PostgreSQL driver: %v", err)
	}
}

// createPostgresTable creates the isolated table used by a test.
func createPostgresTable(
	t *testing.T,
	driver database.Driver,
	operationContext context.Context,
	table string,
) {
	t.Helper()

	statement := fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, name TEXT NOT NULL)", table)

	if _, err := driver.Exec(operationContext, statement); err != nil {
		t.Fatalf("create PostgreSQL table: %v", err)
	}
}

// registerPostgresTableCleanup drops table when the test completes.
func registerPostgresTableCleanup(t *testing.T, driver database.Driver, table string) {
	t.Helper()

	t.Cleanup(func() {
		dropPostgresTable(t, driver, table)
	})
}

// dropPostgresTable drops table and reports a cleanup failure.
func dropPostgresTable(t *testing.T, driver database.Driver, table string) {
	t.Helper()

	cleanupContext, cleanupCancel := context.WithTimeout(
		context.WithoutCancel(t.Context()),
		postgresCleanupTimeout,
	)

	_, err := driver.Exec(cleanupContext, fmt.Sprintf("DROP TABLE %s", table))
	cleanupCancel()

	if err != nil {
		t.Errorf("drop PostgreSQL table: %v", err)
	}
}

// postgresConnectionURL builds the PostgreSQL DSN for the integration-test server.
func postgresConnectionURL(host, port string) string {
	return fmt.Sprintf(
		"postgres://postgres:%s@%s/%s?sslmode=disable",
		postgresPassword,
		net.JoinHostPort(host, port),
		postgresDatabaseName,
	)
}

// postgresTableName builds a unique PostgreSQL table name from the test name.
func postgresTableName(t *testing.T) string {
	t.Helper()

	bytes := make([]byte, 8)

	if _, err := rand.Read(bytes); err != nil {
		t.Fatalf("generate table suffix: %v", err)
	}

	name := strings.Map(postgresTableNameCharacter, strings.ToLower(t.Name()))
	name = strings.Trim(name, "_")

	if len(name) > 32 {
		name = name[:32]
	}

	return fmt.Sprintf("arkoren_%s_%s", name, hex.EncodeToString(bytes))
}

// postgresTableNameCharacter maps unsupported table-name runes to underscores.
func postgresTableNameCharacter(character rune) rune {
	if isPostgresTableNameDigit(character) || isPostgresTableNameLetter(character) {
		return character
	}

	return '_'
}

// isPostgresTableNameDigit reports whether character is an ASCII digit.
func isPostgresTableNameDigit(character rune) bool {
	return character >= '0' && character <= '9'
}

// isPostgresTableNameLetter reports whether character is an ASCII lowercase letter.
func isPostgresTableNameLetter(character rune) bool {
	return character >= 'a' && character <= 'z'
}

// postgresTestContext returns a deadline-bound context for a PostgreSQL test.
func postgresTestContext(t *testing.T) context.Context {
	t.Helper()

	operationContext, cancel := context.WithTimeout(t.Context(), postgresOperationTimeout)
	t.Cleanup(cancel)

	return operationContext
}

// TestConformance runs the database conformance suite against a live server; it is skipped under -short or without the DSN environment variable.
func TestConformance(t *testing.T) {
	dsn := integrationDSN(t)

	databasetest.Run(t, func(t *testing.T) database.Driver {
		t.Helper()

		driver, err := postgres.New(dsn)

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
