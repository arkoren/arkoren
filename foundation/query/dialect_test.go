package query_test

import (
	"errors"
	"reflect"
	"testing"

	"arkoren.dev/foundation/query"
)

// TestPostgresRejectsNonRowPreservingLocks verifies DISTINCT, GROUP BY, and
// HAVING cannot request either PostgreSQL row-lock mode through a derived query.
func TestPostgresRejectsNonRowPreservingLocks(t *testing.T) {
	t.Parallel()

	queries := []query.Query{
		query.Table("users", func(statement *query.Builder) {
			statement.Distinct()
		}),
		query.Table("users", func(statement *query.Builder) {
			statement.Select("role")
			statement.GroupBy("role")
		}),
		query.Table("users", func(statement *query.Builder) {
			statement.SelectRaw("COUNT(*)")
			statement.HavingRaw("COUNT(*) > ?", 1)
		}),
	}
	locks := []query.BuilderFunc{
		func(statement *query.Builder) {
			statement.ForUpdate()
		},
		func(statement *query.Builder) {
			statement.ForShare()
		},
	}

	for _, definition := range queries {
		for _, configure := range locks {
			requireUnsupported(t, definition.Derive(configure), query.Postgres)
		}
	}
}

// TestPostgresLockFailureLeavesDefinitionReusable verifies dialect validation
// does not change the base query or prevent MySQL compilation of its variant.
func TestPostgresLockFailureLeavesDefinitionReusable(t *testing.T) {
	t.Parallel()

	base := query.Table("users", func(statement *query.Builder) {
		statement.Select("id")
		statement.Distinct()
		statement.Where("active", true)
	})
	locked := base.Derive(func(statement *query.Builder) {
		statement.ForUpdate()
	})

	_, err := locked.BuildFor(query.Postgres)

	if !errors.Is(err, query.ErrUnsupported) {
		t.Fatalf("BuildFor(Postgres) = %v, want ErrUnsupported", err)
	}

	statement, err := base.BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if want := `SELECT DISTINCT "id" FROM "users" WHERE "active" = $1`; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if !reflect.DeepEqual(statement.Args(), []any{true}) {
		t.Fatalf("Args = %#v, want [true]", statement.Args())
	}

	if _, err := locked.BuildFor(query.MySQL); err != nil {
		t.Fatal(err)
	}
}

// TestMySQLRejectsDistinctComparisons verifies unsupported standard operators
// fail during compilation rather than producing SQL MySQL cannot execute.
func TestMySQLRejectsDistinctComparisons(t *testing.T) {
	t.Parallel()

	for _, operator := range []string{"IS DISTINCT FROM", "IS NOT DISTINCT FROM"} {
		requireUnsupported(t, query.Table("users", func(statement *query.Builder) {
			statement.Where("name", operator, "Ada")
		}), query.MySQL)
	}
}

// TestSQLiteRejectsQualifiedMutationColumns verifies each written column
// position rejects qualification instead of emitting invalid SQLite syntax.
func TestSQLiteRejectsQualifiedMutationColumns(t *testing.T) {
	t.Parallel()

	queries := []query.Query{
		query.Table("users", func(statement *query.Builder) {
			statement.Insert(map[string]any{"users.name": "Ada"})
		}),
		query.Table("users", func(statement *query.Builder) {
			statement.Update(map[string]any{"users.name": "Ada"})
			statement.Where("id", 7)
		}),
		query.Table("users", func(statement *query.Builder) {
			statement.Increment("users.visits", 1)
			statement.Where("id", 7)
		}),
		query.Table("users", func(statement *query.Builder) {
			statement.SetRaw("users.name", "upper(?)", "Ada")
			statement.Where("id", 7)
		}),
		query.Table("users", func(statement *query.Builder) {
			statement.Insert(map[string]any{"name": "Ada"})
			statement.OnConflict("users.name").DoNothing()
		}),
		query.Table("users", func(statement *query.Builder) {
			statement.Insert(map[string]any{"name": "Ada"})
			statement.OnConflict("name").DoUpdate("users.name")
		}),
	}

	for _, definition := range queries {
		requireUnsupported(t, definition, query.SQLite)
	}
}

// TestSQLiteKeepsQualifiedReadColumns verifies mutation validation leaves
// qualified selections, table names, and value predicates available.
func TestSQLiteKeepsQualifiedReadColumns(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("main.users", func(statement *query.Builder) {
		statement.Select("users.name")
		statement.Where("users.id", 7)
	}).BuildFor(query.SQLite)

	if err != nil {
		t.Fatal(err)
	}

	if want := `SELECT "users"."name" FROM "main"."users" WHERE "users"."id" = ?`; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if !reflect.DeepEqual(statement.Args(), []any{7}) {
		t.Fatalf("Args = %#v, want [7]", statement.Args())
	}
}

// TestQualifiedMutationFailureLeavesOtherDialectsReusable verifies SQLite's
// guard neither rejects PostgreSQL composite-field assignments nor changes
// captured map values or the assignment-before-filter argument order.
func TestQualifiedMutationFailureLeavesOtherDialectsReusable(t *testing.T) {
	t.Parallel()

	values := map[string]any{"address.city": "Paris"}
	definition := query.Table("users", func(statement *query.Builder) {
		statement.Update(values)
		statement.Where("id", 7)
	})

	values["address.city"] = "Lyon"

	requireUnsupported(t, definition, query.SQLite)

	statement, err := definition.BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if want := `UPDATE "users" SET "address"."city" = $1 WHERE "id" = $2`; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if want := []any{"Paris", 7}; !reflect.DeepEqual(statement.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
	}
}

// TestMySQLRejectsDistinctColumnComparisons verifies column predicates use the
// same dialect guard as bound-value predicates, including JOIN conditions.
func TestMySQLRejectsDistinctColumnComparisons(t *testing.T) {
	t.Parallel()

	for _, operator := range []string{"IS DISTINCT FROM", "IS NOT DISTINCT FROM"} {
		requireUnsupported(t, query.Table("users", func(statement *query.Builder) {
			statement.WhereColumn("name", operator, "previous_name")
		}), query.MySQL)
		requireUnsupported(t, query.Table("users", func(statement *query.Builder) {
			statement.Join("profiles", "users.name", operator, "profiles.name")
		}), query.MySQL)
	}
}

// TestBuildForPostgresNumbersPlaceholders verifies $n numbering across
// comparisons, membership, and ranges.
func TestBuildForPostgresNumbersPlaceholders(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("active", true)
		statement.WhereIn("role", "admin", "editor")
		statement.WhereBetween("age", 18, 65)
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	expectedSQL := `SELECT * FROM "users" WHERE "active" = $1 AND "role" IN ($2, $3) ` +
		`AND "age" BETWEEN $4 AND $5`

	if statement.SQL() != expectedSQL {
		t.Fatalf("expected %q, got %q", expectedSQL, statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{true, "admin", "editor", 18, 65}) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestBuildForPostgresInsert verifies numbered placeholders in inserted rows.
func TestBuildForPostgresInsert(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.InsertRows(map[string]any{"name": "Ada"}, map[string]any{"name": "Grace"})
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	expectedSQL := `INSERT INTO "users" ("name") VALUES ($1), ($2)`

	if statement.SQL() != expectedSQL {
		t.Fatalf("expected %q, got %q", expectedSQL, statement.SQL())
	}
}

// TestBuildForPostgresUpdate verifies numbered placeholders in assignments
// followed by filters.
func TestBuildForPostgresUpdate(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
		statement.Update(map[string]any{"name": "Ada"})
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	expectedSQL := `UPDATE "users" SET "name" = $1 WHERE "id" = $2`

	if statement.SQL() != expectedSQL {
		t.Fatalf("expected %q, got %q", expectedSQL, statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{"Ada", 7}) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestBuildForPostgresRenumbersRawPlaceholders verifies that ? in raw
// expressions become numbered placeholders while quoted ? stays literal.
func TestBuildForPostgresRenumbersRawPlaceholders(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("orders", func(statement *query.Builder) {
		statement.Select("customer_id")
		statement.Where("status", "paid")
		statement.GroupBy("customer_id")
		statement.HavingRaw("COUNT(*) > ? AND MAX(note) <> '?'", 5)
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	expectedSQL := `SELECT "customer_id" FROM "orders" WHERE "status" = $1 ` +
		`GROUP BY "customer_id" HAVING COUNT(*) > $2 AND MAX(note) <> '?'`

	if statement.SQL() != expectedSQL {
		t.Fatalf("expected %q, got %q", expectedSQL, statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{"paid", 5}) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestBuildForMySQLUsesBackticks verifies MySQL identifier quoting.
func TestBuildForMySQLUsesBackticks(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Select("users.id")
		statement.Where("active", true)
	}).BuildFor(query.MySQL)

	if err != nil {
		t.Fatal(err)
	}

	expectedSQL := "SELECT `users`.`id` FROM `users` WHERE `active` = ?"

	if statement.SQL() != expectedSQL {
		t.Fatalf("expected %q, got %q", expectedSQL, statement.SQL())
	}
}

// TestBuildForZeroDialectIsANSI verifies the zero Dialect compiles as ANSI.
func TestBuildForZeroDialectIsANSI(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
	}).BuildFor(query.Dialect{})

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != `SELECT * FROM "users" WHERE "id" = ?` {
		t.Fatalf("unexpected SQL: %q", statement.SQL())
	}

	if (query.Dialect{}) != query.ANSI {
		t.Fatal("zero Dialect must equal ANSI")
	}
}

// TestBuildForMySQLEscapesBackticks verifies quote characters are escaped.
func TestBuildForMySQLEscapesBackticks(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.SelectRaw("1")
		statement.Where("id", 7)
	}).BuildFor(query.MySQL)

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != "SELECT 1 FROM `users` WHERE `id` = ?" {
		t.Fatalf("unexpected SQL: %q", statement.SQL())
	}
}

// TestDialectString verifies dialect names.
func TestDialectString(t *testing.T) {
	t.Parallel()

	got := []string{query.ANSI.String(), query.SQLite.String(), query.Postgres.String(), query.MySQL.String()}

	if !reflect.DeepEqual(got, []string{"ansi", "sqlite", "postgres", "mysql"}) {
		t.Fatalf("unexpected names: %v", got)
	}
}

// TestBuildMatchesANSI verifies that Build compiles for the ANSI dialect.
func TestBuildMatchesANSI(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
	})

	built, err := builder.Build()

	if err != nil {
		t.Fatal(err)
	}

	ansi, err := builder.BuildFor(query.ANSI)

	if err != nil {
		t.Fatal(err)
	}

	if built.SQL() != ansi.SQL() || built.SQL() != `SELECT * FROM "users" WHERE "id" = ?` {
		t.Fatalf("unexpected SQL: %q and %q", built.SQL(), ansi.SQL())
	}
}

// requireUnsupported fails unless builder reports ErrInvalidQuery and ErrUnsupported.
func requireUnsupported(t *testing.T, builder query.Statementer, dialect query.Dialect) {
	t.Helper()

	_, err := builder.BuildFor(dialect)

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("got %v, want ErrInvalidQuery", err)
	}

	if !errors.Is(err, query.ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}

// TestDialectTransactionalDDL verifies which dialects roll back DDL.
func TestDialectTransactionalDDL(t *testing.T) {
	t.Parallel()

	for dialect, want := range map[query.Dialect]bool{
		query.ANSI: false, query.SQLite: true, query.Postgres: true, query.MySQL: false,
	} {
		if got := dialect.TransactionalDDL(); got != want {
			t.Fatalf("%s: TransactionalDDL() = %t, want %t", dialect, got, want)
		}
	}
}

// TestDialectQuote verifies identifier quoting and quote doubling.
func TestDialectQuote(t *testing.T) {
	t.Parallel()

	if got := query.MySQL.Quote("a`b"); got != "`a``b`" {
		t.Fatalf("mysql: %s", got)
	}

	if got := query.Postgres.Quote(`a"b`); got != `"a""b"` {
		t.Fatalf("postgres: %s", got)
	}
}
