package query_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"arkoren.dev/foundation/query"
)

// ExampleRawStatement compiles named bindings and demonstrates that Args owns
// its returned slice independently of both the raw definition and statement.
func ExampleRawStatement() {
	definition := query.RawNamed("SELECT :id", map[string]any{"id": 7})

	statement, err := definition.BuildFor(query.Postgres)

	if err != nil {
		fmt.Println(err)

		return
	}

	arguments := statement.Args()
	arguments[0] = 9

	fmt.Println(statement.SQL(), statement.Args())
	fmt.Println(query.Postgres.String(), query.Postgres.TransactionalDDL())
	// Output:
	// SELECT $1 [7]
	// postgres true
}

// TestStatementArgsReturnsCopy verifies that compiled statement arguments are
// immutable through the public API.
func TestStatementArgsReturnsCopy(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 1)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	args := statement.Args()
	args[0] = 2

	if statement.Args()[0] != 1 {
		t.Fatalf("statement arguments were mutated: %#v", statement.Args())
	}
}

// TestRawNamedBindsByName verifies names become dialect placeholders in order
// of appearance and repeated names bind again.
func TestRawNamedBindsByName(t *testing.T) {
	t.Parallel()

	statement := query.RawNamed("SELECT * FROM t WHERE a = :a OR b = :b OR c = :a", map[string]any{"a": 1, "b": "x"})

	built, err := statement.BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if want := "SELECT * FROM t WHERE a = $1 OR b = $2 OR c = $3"; built.SQL() != want {
		t.Fatalf("SQL = %q, want %q", built.SQL(), want)
	}

	if want := []any{1, "x", 1}; !reflect.DeepEqual(built.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", built.Args(), want)
	}

	built, err = statement.BuildFor(query.MySQL)

	if err != nil {
		t.Fatal(err)
	}

	if want := "SELECT * FROM t WHERE a = ? OR b = ? OR c = ?"; built.SQL() != want {
		t.Fatalf("SQL = %q, want %q", built.SQL(), want)
	}
}

// TestRawNamedKeepsCastsAndSkipsQuotes verifies :: and text in quotes and
// comments are written unchanged.
func TestRawNamedKeepsCastsAndSkipsQuotes(t *testing.T) {
	t.Parallel()

	sql := "SELECT :id::text, ':no', \":no\", `:no` /* :no */ FROM t -- :no\nWHERE x = :x AND y ? 'z'"

	built, err := query.RawNamed(sql, map[string]any{"id": 1, "x": 2}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	want := "SELECT $1::text, ':no', \":no\", `:no` /* :no */ FROM t -- :no\nWHERE x = $2 AND y ? 'z'"

	if built.SQL() != want {
		t.Fatalf("SQL = %q, want %q", built.SQL(), want)
	}
}

// TestRawNamedMissingNameFails verifies a name without an argument is an
// invalid query, while an unused argument is fine.
func TestRawNamedMissingNameFails(t *testing.T) {
	t.Parallel()

	_, err := query.RawNamed("SELECT :a, :b", map[string]any{"a": 1}).Build()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("Build() error = %v, want ErrInvalidQuery", err)
	}

	if _, err := query.RawNamed("SELECT :a", map[string]any{"a": 1, "extra": 2}).Build(); err != nil {
		t.Fatalf("Build() with an unused argument error = %v", err)
	}
}

// TestRawNamedCopiesArguments verifies later changes to the map do not reach
// the statement.
func TestRawNamedCopiesArguments(t *testing.T) {
	t.Parallel()

	args := map[string]any{"a": 1}
	statement := query.RawNamed("SELECT :a", args)

	args["a"] = 2

	built, err := statement.Build()

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(built.Args(), []any{1}) {
		t.Fatalf("Args = %#v, want [1]", built.Args())
	}
}

// TestRawNamedLeavesLoneColons verifies a colon not followed by a name is text.
func TestRawNamedLeavesLoneColons(t *testing.T) {
	t.Parallel()

	built, err := query.RawNamed("SELECT a[1:2], '12:30', :1x", nil).Build()

	if err != nil {
		t.Fatal(err)
	}

	if want := "SELECT a[1:2], '12:30', :1x"; built.SQL() != want {
		t.Fatalf("SQL = %q, want %q", built.SQL(), want)
	}
}

// TestRawANSI verifies Raw keeps ? placeholders on ANSI.
func TestRawANSI(t *testing.T) {
	t.Parallel()

	statement, err := query.Raw("UPDATE users SET a = ? WHERE b = ?", 1, "x").Build()

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != "UPDATE users SET a = ? WHERE b = ?" {
		t.Fatalf("unexpected SQL: %s", statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{1, "x"}) {
		t.Fatalf("unexpected args: %#v", statement.Args())
	}
}

// TestRawPostgres verifies Raw numbers placeholders and keeps ?? literal.
func TestRawPostgres(t *testing.T) {
	t.Parallel()

	statement, err := query.Raw("SELECT * FROM t WHERE data ?? 'a' AND n = ? AND m = '?'", 5).
		BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != "SELECT * FROM t WHERE data ? 'a' AND n = $1 AND m = '?'" {
		t.Fatalf("unexpected SQL: %s", statement.SQL())
	}
}

// TestRawMySQL verifies Raw keeps ? placeholders on MySQL.
func TestRawMySQL(t *testing.T) {
	t.Parallel()

	statement, err := query.Raw("SELECT ? -- why?", 1).BuildFor(query.MySQL)

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != "SELECT ? -- why?" {
		t.Fatalf("unexpected SQL: %s", statement.SQL())
	}
}

// TestRawSQLite verifies Raw keeps ? placeholders on SQLite.
func TestRawSQLite(t *testing.T) {
	t.Parallel()

	statement, err := query.Raw("DELETE FROM t WHERE a IN (?, ?)", 1, 2).BuildFor(query.SQLite)

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != "DELETE FROM t WHERE a IN (?, ?)" {
		t.Fatalf("unexpected SQL: %s", statement.SQL())
	}
}

// TestRawCountMismatch verifies a wrong placeholder count fails.
func TestRawCountMismatch(t *testing.T) {
	t.Parallel()

	_, err := query.Raw("SELECT ?", 1, 2).Build()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("got %v, want ErrInvalidQuery", err)
	}
}

// TestRawEmpty verifies blank SQL fails.
func TestRawEmpty(t *testing.T) {
	t.Parallel()

	_, err := query.Raw("  ").Build()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("got %v, want ErrInvalidQuery", err)
	}
}

// TestRawCopiesArgs verifies later changes to the caller's slice do not leak.
func TestRawCopiesArgs(t *testing.T) {
	t.Parallel()

	args := []any{1}
	raw := query.Raw("SELECT ?", args...)

	args[0] = 2

	statement, err := raw.Build()

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(statement.Args(), []any{1}) {
		t.Fatalf("unexpected args: %#v", statement.Args())
	}
}

// TestRawIsStatementer verifies RawStatement satisfies Statementer.
func TestRawIsStatementer(t *testing.T) {
	t.Parallel()

	var _ query.Statementer = query.Raw("SELECT 1")
}
