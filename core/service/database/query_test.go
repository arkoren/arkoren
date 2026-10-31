package database_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// recordingDriver records the SQL and arguments it receives.
type recordingDriver struct {
	// sql is the last query text received.
	sql *string

	// args are the last arguments received.
	args *[]any
}

// record stores the received query and arguments.
func (driver recordingDriver) record(sql string, args []any) {
	*driver.sql = sql
	*driver.args = args
}

// Exec records the query and reports one affected row.
func (driver recordingDriver) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	driver.record(sql, args)

	return 1, nil
}

// Select records the query and returns no rows.
func (driver recordingDriver) Select(_ context.Context, sql string, _ any, args ...any) error {
	driver.record(sql, args)

	return nil
}

// Query records the query and fails, ending the cursor.
func (driver recordingDriver) Query(_ context.Context, sql string, args ...any) (database.Rows, error) {
	driver.record(sql, args)

	return nil, errNoCursor
}

// Find records the query.
func (driver recordingDriver) Find(_ context.Context, sql string, _ any, args ...any) error {
	driver.record(sql, args)

	return nil
}

// Lock returns a no-op unlock function.
func (recordingDriver recordingDriver) Lock(context.Context, string) (func(context.Context) error, error) {
	return func(context.Context) error {
		return nil
	}, nil
}

// TryLock returns a no-op unlock function and reports the lock as acquired.
func (recordingDriver recordingDriver) TryLock(context.Context, string) (func(context.Context) error, bool, error) {
	return func(context.Context) error {
		return nil
	}, true, nil
}

// WithTransaction runs fn with the same driver.
func (driver recordingDriver) WithTransaction(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// WithSession keeps query recording active throughout fn. The stub performs
// no I/O and has no physical connection to acquire or release.
func (driver recordingDriver) WithSession(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// Dialect reports the ANSI dialect.
func (recordingDriver) Dialect() query.Dialect {
	return query.ANSI
}

// postgresDriver is a recordingDriver that reports the Postgres dialect.
type postgresDriver struct {
	// recordingDriver preserves call recording while Dialect selects PostgreSQL.
	recordingDriver
}

// Dialect reports the Postgres dialect.
func (postgresDriver) Dialect() query.Dialect {
	return query.Postgres
}

// WithTransaction runs fn with the same Postgres driver.
func (driver postgresDriver) WithTransaction(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// WithSession preserves the PostgreSQL dialect in the callback instead of
// promoting the embedded ANSI recording driver's implementation.
func (driver postgresDriver) WithSession(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// errNoCursor ends cursors opened by recordingDriver.
var errNoCursor = errors.New("no cursor")

// newRecording returns a recordingDriver and the values it records into.
func newRecording() (recordingDriver, *string, *[]any) {
	var text string
	var args []any

	return recordingDriver{sql: &text, args: &args}, &text, &args
}

// TestSelectUsesDriverDialect verifies that the builder compiles for the
// dialect reported by the driver.
func TestSelectUsesDriverDialect(t *testing.T) {
	t.Parallel()

	recording, text, args := newRecording()
	db := database.New(postgresDriver{recording})

	_, err := db.Select[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
	}))

	if err != nil {
		t.Fatal(err)
	}

	if *text != `SELECT * FROM "users" WHERE "id" = $1` {
		t.Fatalf("unexpected SQL: %q", *text)
	}

	if !reflect.DeepEqual(*args, []any{7}) {
		t.Fatalf("unexpected arguments: %#v", *args)
	}
}

// TestSelectCompilesForANSI verifies compiling for an ANSI driver.
// report a dialect.
func TestSelectCompilesForANSI(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	_, err := db.Select[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
	}))

	if err != nil {
		t.Fatal(err)
	}

	if *text != `SELECT * FROM "users" WHERE "id" = ?` {
		t.Fatalf("unexpected SQL: %q", *text)
	}
}

// TestExecUsesDriverDialect verifies mutations compile for the driver.
func TestExecUsesDriverDialect(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(postgresDriver{recording})

	affected, err := db.Exec(context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
		statement.Update(map[string]any{"name": "Ada"})
	}))

	if err != nil {
		t.Fatal(err)
	}

	if affected != 1 || *text != `UPDATE "users" SET "name" = $1 WHERE "id" = $2` {
		t.Fatalf("unexpected result %d, SQL %q", affected, *text)
	}
}

// TestFindUsesDriverDialect verifies single-row lookups compile for the
// driver.
func TestFindUsesDriverDialect(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(postgresDriver{recording})

	if _, err := db.Find[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
	})); err != nil {
		t.Fatal(err)
	}

	if *text != `SELECT * FROM "users" WHERE "id" = $1` {
		t.Fatalf("unexpected SQL: %q", *text)
	}
}

// TestCursorUsesDriverDialect verifies cursors compile for the driver.
func TestCursorUsesDriverDialect(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(postgresDriver{recording})

	_, err := db.Cursor[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
	})).Items()

	if !errors.Is(err, errNoCursor) {
		t.Fatalf("expected driver error, got %v", err)
	}

	if *text != `SELECT * FROM "users" WHERE "id" = $1` {
		t.Fatalf("unexpected SQL: %q", *text)
	}
}

// TestExecReturnsBuildError verifies that invalid builders never reach
// the driver.
func TestExecReturnsBuildError(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	_, err := db.Exec(context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Delete()
	}))

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}

	if *text != "" {
		t.Fatalf("driver received %q", *text)
	}
}

// TestCursorYieldsBuildError verifies that cursors report build errors.
func TestCursorYieldsBuildError(t *testing.T) {
	t.Parallel()

	recording, _, _ := newRecording()
	db := database.New(recording)

	_, err := db.Cursor[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Delete()
	})).Items()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}
}

// TestWithTransactionKeepsDialect verifies a database used inside a
// transaction keeps the driver dialect.
func TestWithTransactionKeepsDialect(t *testing.T) {
	t.Parallel()

	recording, _, _ := newRecording()
	db := database.New(postgresDriver{recording})

	err := db.WithTransaction(context.Background(), func(context.Context) error {
		if db.Dialect() != query.Postgres {
			t.Fatal("database lost the Postgres dialect")
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestSelectReturnsBuildError verifies that invalid builders never reach
// the driver.
func TestSelectReturnsBuildError(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	_, err := db.Select[int](context.Background(), query.Table("bad name"))

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}

	if *text != "" {
		t.Fatalf("driver received %q", *text)
	}
}

// TestFindReturnsBuildError verifies that invalid builders never reach
// the driver.
func TestFindReturnsBuildError(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	_, err := db.Find[int](context.Background(), query.Table("bad name"))

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}

	if *text != "" {
		t.Fatalf("driver received %q", *text)
	}
}

// TestBuildErrorsAreWrapped verifies the facade names the failed operation.
func TestBuildErrorsAreWrapped(t *testing.T) {
	t.Parallel()

	recording, _, _ := newRecording()
	db := database.New(recording)

	_, err := db.Exec(context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Delete()
	}))

	if err == nil || !strings.HasPrefix(err.Error(), "build query: ") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestSelectRejectsNonSelect verifies mutation builders are refused.
func TestSelectRejectsNonSelect(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	_, err := db.Select[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.Delete()
	}))

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}

	if *text != "" {
		t.Fatalf("driver received %q", *text)
	}
}

// TestFindRejectsNonSelect verifies mutation builders are refused.
func TestFindRejectsNonSelect(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	_, err := db.Find[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.Delete()
	}))

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}

	if *text != "" {
		t.Fatalf("driver received %q", *text)
	}
}

// TestCursorRejectsNonSelect verifies the cursor yields the refusal
// without reaching the driver.
func TestCursorRejectsNonSelect(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	_, err := db.Cursor[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.Delete()
	})).Items()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}

	if *text != "" {
		t.Fatalf("driver received %q", *text)
	}
}

// TestExecCompilesForANSI verifies compiling for an ANSI driver.
func TestExecCompilesForANSI(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	if _, err := db.Exec(context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
		statement.Delete()
	})); err != nil {
		t.Fatal(err)
	}

	if *text != `DELETE FROM "users" WHERE "id" = ?` {
		t.Fatalf("unexpected SQL: %q", *text)
	}
}

// TestFindCompilesForANSI verifies compiling for an ANSI driver.
func TestFindCompilesForANSI(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	if _, err := db.Find[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
	})); err != nil {
		t.Fatal(err)
	}

	if *text != `SELECT * FROM "users" WHERE "id" = ?` {
		t.Fatalf("unexpected SQL: %q", *text)
	}
}

// TestCursorCompilesForANSI verifies compiling for an ANSI driver.
func TestCursorCompilesForANSI(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(recording)

	_, err := db.Cursor[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
	})).Items()

	if !errors.Is(err, errNoCursor) {
		t.Fatalf("expected driver error, got %v", err)
	}

	if *text != `SELECT * FROM "users" WHERE "id" = ?` {
		t.Fatalf("unexpected SQL: %q", *text)
	}
}

// TestDialectReportsDriverDialect verifies the reported dialect.
func TestDialectReportsDriverDialect(t *testing.T) {
	t.Parallel()

	recording, _, _ := newRecording()

	if database.New(recording).Dialect() != query.ANSI {
		t.Fatal("recording driver should report ANSI")
	}

	if database.New(postgresDriver{recording}).Dialect() != query.Postgres {
		t.Fatal("postgres driver should report Postgres")
	}
}

// TestSelectAllowsReturning verifies write builders with RETURNING scan rows.
func TestSelectAllowsReturning(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(postgresDriver{recording})

	_, err := db.Select[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
		statement.Returning("id")
	}))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *text != `INSERT INTO "users" ("a") VALUES ($1) RETURNING "id"` {
		t.Fatalf("unexpected SQL %q", *text)
	}
}

// TestFindAllowsReturning verifies write builders with RETURNING find a row.
func TestFindAllowsReturning(t *testing.T) {
	t.Parallel()

	recording, text, _ := newRecording()
	db := database.New(postgresDriver{recording})

	_, err := db.Find[int](context.Background(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.Delete()
		statement.Returning("id")
	}))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *text != `DELETE FROM "users" WHERE "id" = $1 RETURNING "id"` {
		t.Fatalf("unexpected SQL %q", *text)
	}
}

// TestSelectAllowsRaw verifies raw statements are passed through.
func TestSelectAllowsRaw(t *testing.T) {
	t.Parallel()

	recording, text, args := newRecording()
	db := database.New(recording)

	_, err := db.Select[int](context.Background(), query.Raw("DELETE FROM t WHERE id = ? RETURNING id", 7))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *text != "DELETE FROM t WHERE id = ? RETURNING id" || !reflect.DeepEqual(*args, []any{7}) {
		t.Fatalf("unexpected query %q %v", *text, *args)
	}
}
