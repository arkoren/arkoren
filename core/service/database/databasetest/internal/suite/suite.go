package suite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// settings holds the timeout resolved by the public conformance entry point.
type settings struct {
	// timeout bounds the context handed to each subtest.
	timeout time.Duration
}

// record is the row type of the suite's table.
type record struct {
	// ID is the primary key.
	ID int `db:"id"`

	// Name is a required text column.
	Name string `db:"name"`
}

// harness is the per-subtest state: a driver and its dedicated table.
type harness struct {
	// t is the subtest.
	t *testing.T

	// driver is the driver under test.
	driver database.Driver

	// dialect rewrites positional placeholders.
	dialect query.Dialect

	// table is the unique table name.
	table string

	// timeout bounds contexts from ctx.
	timeout time.Duration
}

// conformanceTest is one named behavior check.
type conformanceTest struct {
	// name is the subtest name.
	name string

	// run executes the check.
	run func(h *harness)
}

// Run executes the private behavior checks with the resolved per-test timeout.
// The caller owns configuration; each check receives a new driver and table.
func Run(t *testing.T, newDriver func(t *testing.T) database.Driver, timeout time.Duration) {
	t.Helper()

	config := settings{timeout: timeout}

	var tests []conformanceTest
	tests = append(tests, statementTests...)
	tests = append(tests, queryTests...)
	tests = append(tests, cursorTests...)
	tests = append(tests, transactionTests...)
	tests = append(tests, sessionTests...)
	tests = append(tests, lockTests...)
	tests = append(tests, schemaTests...)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.run(newHarness(t, newDriver(t), config))
		})
	}
}

// newHarness creates the subtest table and registers its removal.
func newHarness(t *testing.T, driver database.Driver, config settings) *harness {
	t.Helper()

	h := &harness{
		t:       t,
		driver:  driver,
		dialect: database.New(driver).Dialect(),
		table:   "arkoren_dbtest_" + randomSuffix(t),
		timeout: config.timeout,
	}

	if _, err := driver.Exec(h.ctx(), h.sql("CREATE TABLE {t} (id INTEGER PRIMARY KEY, name VARCHAR(255) NOT NULL)")); err != nil {
		t.Fatalf("create table: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
		defer cancel()

		if _, err := driver.Exec(ctx, h.sql("DROP TABLE IF EXISTS {t}")); err != nil {
			t.Errorf("drop table: %v", err)
		}
	})

	return h
}

// randomSuffix returns a unique hex identifier for table and lock names.
func randomSuffix(t *testing.T) string {
	t.Helper()

	buf := make([]byte, 8)

	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random name: %v", err)
	}

	return hex.EncodeToString(buf)
}

// ctx returns a context bounded by the suite timeout.
func (harness *harness) ctx() context.Context {
	harness.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), harness.timeout)
	harness.t.Cleanup(cancel)

	return ctx
}

// sql substitutes {t} with the table name and rewrites ? placeholders for
// the driver dialect.
func (harness *harness) sql(statement string) string {
	statement = strings.ReplaceAll(statement, "{t}", harness.table)

	if harness.dialect != query.Postgres {
		return statement
	}

	var out strings.Builder

	n := 0

	for _, r := range statement {
		if r != '?' {
			out.WriteRune(r)
			continue
		}

		n++
		fmt.Fprintf(&out, "$%d", n)
	}

	return out.String()
}

// seed inserts rows through driver, which may be a transaction.
func (harness *harness) seed(driver database.Driver, rows ...record) {
	harness.t.Helper()

	for _, row := range rows {
		affected, err := driver.Exec(harness.ctx(), harness.sql("INSERT INTO {t} (id, name) VALUES (?, ?)"), row.ID, row.Name)

		if err != nil || affected != 1 {
			harness.t.Fatalf("seed %v: affected = %d, error = %v", row, affected, err)
		}
	}
}

// rows returns the table's rows ordered by id, read through driver.
func (harness *harness) rows(driver database.Driver) []record {
	harness.t.Helper()

	var rows []record

	if err := driver.Select(harness.ctx(), harness.sql("SELECT id, name FROM {t} ORDER BY id"), &rows); err != nil {
		harness.t.Fatalf("read rows: %v", err)
	}

	return rows
}

// threeRows is the standard fixture.
var threeRows = []record{{1, "one"}, {2, "two"}, {3, "three"}}
