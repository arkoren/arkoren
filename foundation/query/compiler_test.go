package query_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/foundation/query"
)

// TestSelectBuildsParameterizedQuery verifies the primary select clauses and
// ordered bindings.
func TestSelectBuildsParameterizedQuery(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Select("users.id", "users.name")
		statement.Distinct()
		statement.Where("active", true)
		statement.Where("age", ">=", 18)
		statement.OrderByDesc("created_at")
		statement.Limit(20)
		statement.Offset(10)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expectedSQL := `SELECT DISTINCT "users"."id", "users"."name" FROM "users" ` +
		`WHERE "active" = ? AND "age" >= ? ORDER BY "created_at" DESC OFFSET 10 ROWS FETCH FIRST 20 ROWS ONLY`

	if statement.SQL() != expectedSQL {
		t.Fatalf("expected %q, got %q", expectedSQL, statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{true, 18}) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestSelectDefaultsToWildcard verifies that an omitted projection selects all
// columns.
func TestSelectDefaultsToWildcard(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users").Build()

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != `SELECT * FROM "users"` {
		t.Fatalf("unexpected SQL: %q", statement.SQL())
	}
}

// TestAddSelectAppendsColumns verifies that AddSelect retains the existing
// projection.
func TestAddSelectAppendsColumns(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Select("id")
		statement.AddSelect("name", "email")
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `SELECT "id", "name", "email" FROM "users"`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}
}

// TestSelectBuildsJoinsGroupingAndHaving verifies structured joins and
// aggregate clauses.
func TestSelectBuildsJoinsGroupingAndHaving(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Select("users.id")
		statement.Join("orders", "users.id", "=", "orders.user_id")
		statement.LeftJoin("profiles", "users.id", "=", "profiles.user_id")
		statement.CrossJoin("regions")
		statement.GroupBy("users.id")
		statement.Having("users.id", ">", 2)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `SELECT "users"."id" FROM "users" ` +
		`INNER JOIN "orders" ON "users"."id" = "orders"."user_id" ` +
		`LEFT JOIN "profiles" ON "users"."id" = "profiles"."user_id" ` +
		`CROSS JOIN "regions" GROUP BY "users"."id" HAVING "users"."id" > ?`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{2}) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestInsertBuildsDeterministicStatement verifies sorted columns and bindings.
func TestInsertBuildsDeterministicStatement(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]any{
			"name":   "Ada",
			"active": true,
		})
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `INSERT INTO "users" ("active", "name") VALUES (?, ?)`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{true, "Ada"}) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestInsertRowsAcceptsNamedMapType verifies generic named map support.
func TestInsertRowsAcceptsNamedMapType(t *testing.T) {
	t.Parallel()

	type userValues map[string]string

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.InsertRows(
			userValues{"name": "Ada", "role": "admin"},
			userValues{"name": "Grace", "role": "editor"},
		)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `INSERT INTO "users" ("name", "role") VALUES (?, ?), (?, ?)`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}

	expectedArgs := []any{"Ada", "admin", "Grace", "editor"}

	if !reflect.DeepEqual(statement.Args(), expectedArgs) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestUpdateBuildsAssignmentsBeforePredicates verifies SQL binding order.
func TestUpdateBuildsAssignmentsBeforePredicates(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 42)
		statement.Update(map[string]any{"name": "Ada", "active": false})
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `UPDATE "users" SET "active" = ?, "name" = ? WHERE "id" = ?`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{false, "Ada", 42}) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestDeleteRetainsPredicates verifies structured delete compilation.
func TestDeleteRetainsPredicates(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("active", false)
		statement.Delete()
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `DELETE FROM "users" WHERE "active" = ?`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}
}

// TestMutationStatementsRejectSelectClauses verifies that clauses a mutation
// cannot render fail instead of being silently dropped.
func TestMutationStatementsRejectSelectClauses(t *testing.T) {
	t.Parallel()

	_, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("active", false)
		statement.OrderBy("id")
		statement.Limit(1)
		statement.Delete()
	}).Build()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected ErrInvalidQuery, got %v", err)
	}
}

// TestDeleteWithoutWhereIsRejected verifies the mutation safety guard and
// the explicit opt-in needed to delete every row.
func TestDeleteWithoutWhereIsRejected(t *testing.T) {
	t.Parallel()

	if _, err := query.Table("users", func(statement *query.Builder) {
		statement.Delete()
	}).Build(); err == nil {
		t.Fatal("unfiltered delete accepted")
	}

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Delete()
		statement.Unfiltered()
	}).Build()

	if err != nil || statement.SQL() != `DELETE FROM "users"` {
		t.Fatalf("statement = %+v, err = %v", statement, err)
	}
}

// TestSelectRawAndHavingRaw verifies raw selection and HAVING expressions
// compose with structured grouping and retain bound arguments.
func TestSelectRawAndHavingRaw(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("orders", func(statement *query.Builder) {
		statement.Select("customer_id")
		statement.SelectRaw("COUNT(*) AS total")
		statement.GroupBy("customer_id")
		statement.HavingRaw("COUNT(*) > ?", 5)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	want := `SELECT "customer_id", COUNT(*) AS total FROM "orders" GROUP BY "customer_id" HAVING COUNT(*) > ?`

	if statement.SQL() != want || len(statement.Args()) != 1 {
		t.Fatalf("statement = %+v", statement)
	}
}

// TestBuildRejectsUnsupportedOperator verifies that operators are allow-listed.
func TestBuildRejectsUnsupportedOperator(t *testing.T) {
	t.Parallel()

	_, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("name", "CONTAINS", "Ada")
	}).Build()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected invalid query error, got %v", err)
	}

	if !strings.Contains(err.Error(), "unsupported operator") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBuildRejectsEmptyGroup verifies that empty parentheses cannot be emitted.
func TestBuildRejectsEmptyGroup(t *testing.T) {
	t.Parallel()

	_, err := query.Table("users", func(statement *query.Builder) {
		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
		})
	}).Build()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected invalid query error, got %v", err)
	}
}

// TestBuildRejectsMismatchedInsertRows verifies consistent batch columns.
func TestBuildRejectsMismatchedInsertRows(t *testing.T) {
	t.Parallel()

	_, err := query.Table("users", func(statement *query.Builder) {
		statement.InsertRows(
			map[string]string{"name": "Ada"},
			map[string]string{"role": "editor"},
		)
	}).Build()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected invalid query error, got %v", err)
	}
}

// TestBuildRejectsEmptyUpdate verifies that updates require assignments.
func TestBuildRejectsEmptyUpdate(t *testing.T) {
	t.Parallel()

	_, err := query.Table("users", func(statement *query.Builder) {
		statement.Update(map[string]string{})
	}).Build()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("expected invalid query error, got %v", err)
	}
}

// requireBuilderSQL fails unless builder compiles to want for dialect.
func requireBuilderSQL(t *testing.T, builder query.Query, dialect query.Dialect, want string) {
	t.Helper()

	statement, err := builder.BuildFor(dialect)

	if err != nil {
		t.Fatalf("%s: %v", dialect, err)
	}

	if statement.SQL() != want {
		t.Fatalf("%s:\n got %s\nwant %s", dialect, statement.SQL(), want)
	}
}

// requireBuilderInvalid fails unless builder reports ErrInvalidQuery.
func requireBuilderInvalid(t *testing.T, builder query.Query) {
	t.Helper()

	if _, err := builder.Build(); !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("got %v, want ErrInvalidQuery", err)
	}
}

// TestOffsetWithoutLimitMySQL verifies MySQL gets an unbounded LIMIT.
func TestOffsetWithoutLimitMySQL(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("users", func(statement *query.Builder) {
		statement.Offset(5)
	}), query.MySQL,
		"SELECT * FROM `users` LIMIT 18446744073709551615 OFFSET 5")
}

// TestOffsetWithoutLimitSQLite verifies SQLite gets LIMIT -1.
func TestOffsetWithoutLimitSQLite(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("users", func(statement *query.Builder) {
		statement.Offset(5)
	}), query.SQLite, `SELECT * FROM "users" LIMIT -1 OFFSET 5`)
}

// TestOffsetWithoutLimitPostgres verifies Postgres emits a bare OFFSET.
func TestOffsetWithoutLimitPostgres(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("users", func(statement *query.Builder) {
		statement.Offset(5)
	}), query.Postgres, `SELECT * FROM "users" OFFSET 5`)
}

// TestOffsetWithoutLimitANSI verifies ANSI emits OFFSET n ROWS.
func TestOffsetWithoutLimitANSI(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("users", func(statement *query.Builder) {
		statement.Offset(5)
	}), query.ANSI, `SELECT * FROM "users" OFFSET 5 ROWS`)
}

// TestLimitANSIUsesFetchFirst verifies ANSI emits FETCH FIRST for a limit.
func TestLimitANSIUsesFetchFirst(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("users", func(statement *query.Builder) {
		statement.Limit(3)
	}), query.ANSI, `SELECT * FROM "users" FETCH FIRST 3 ROWS ONLY`)
}

// TestLimitAndOffsetPostgres verifies the LIMIT and OFFSET order.
func TestLimitAndOffsetPostgres(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("users", func(statement *query.Builder) {
		statement.Limit(3)
		statement.Offset(5)
	}), query.Postgres, `SELECT * FROM "users" LIMIT 3 OFFSET 5`)
}

// TestHavingRawClonesArgs verifies later changes to the caller's slice do not
// reach the builder.
func TestHavingRawClonesArgs(t *testing.T) {
	t.Parallel()

	args := []any{5}
	builder := query.Table("orders", func(statement *query.Builder) {
		statement.GroupBy("customer_id")
		statement.HavingRaw("COUNT(*) > ?", args...)
	})

	args[0] = 9

	statement, err := builder.Build()

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(statement.Args(), []any{5}) {
		t.Fatalf("args aliased: %#v", statement.Args())
	}
}

// TestRawRejectsTooFewArguments verifies placeholder and argument counts match.
func TestRawRejectsTooFewArguments(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.HavingRaw("a > ? AND b > ?", 1)
	}))
}

// TestRawRejectsTooManyArguments verifies surplus arguments are not bound silently.
func TestRawRejectsTooManyArguments(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.HavingRaw("a > ?", 1, 2)
	}))
}

// TestRawDoubleQuestionMarkIsLiteral verifies ?? escapes a question mark.
func TestRawDoubleQuestionMarkIsLiteral(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("t", func(statement *query.Builder) {
		statement.HavingRaw("data ?? 'a' AND n > ?", 1)
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	want := `SELECT * FROM "t" HAVING data ? 'a' AND n > $1`

	if statement.SQL() != want || len(statement.Args()) != 1 {
		t.Fatalf("got %q with %v", statement.SQL(), statement.Args())
	}
}

// TestRawSkipsLineComments verifies a ? inside a -- comment is not a placeholder.
func TestRawSkipsLineComments(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("t", func(statement *query.Builder) {
		statement.HavingRaw("n > ? -- really?\nAND m = 1", 1)
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	want := "SELECT * FROM \"t\" HAVING n > $1 -- really?\nAND m = 1"

	if statement.SQL() != want {
		t.Fatalf("got %q, want %q", statement.SQL(), want)
	}
}

// TestRawSkipsBlockComments verifies a ? inside a block comment is not a placeholder.
func TestRawSkipsBlockComments(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("t", func(statement *query.Builder) {
		statement.HavingRaw("n > /* why? */ ?", 1)
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	want := `SELECT * FROM "t" HAVING n > /* why? */ $1`

	if statement.SQL() != want {
		t.Fatalf("got %q, want %q", statement.SQL(), want)
	}
}

// TestSelectRawRejectsEmpty verifies an empty select expression is an error.
func TestSelectRawRejectsEmpty(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.SelectRaw("")
	}))
}

// TestHavingRawRejectsEmpty verifies an empty having expression is an error.
func TestHavingRawRejectsEmpty(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.HavingRaw("")
	}))
}

// TestWhereTypedNilPointerIsNull verifies a typed nil pointer becomes IS NULL.
func TestWhereTypedNilPointerIsNull(t *testing.T) {
	t.Parallel()

	var missing *int
	statement, err := query.Table("t", func(statement *query.Builder) {
		statement.Where("a", missing)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != `SELECT * FROM "t" WHERE "a" IS NULL` || len(statement.Args()) != 0 {
		t.Fatalf("got %q with %v", statement.SQL(), statement.Args())
	}
}

// TestWhereTypedNilMapAndSliceAreNull verifies nil maps and slices become IS NULL.
func TestWhereTypedNilMapAndSliceAreNull(t *testing.T) {
	t.Parallel()

	var missingMap map[string]int
	var missingSlice []byte
	statement, err := query.Table("t", func(statement *query.Builder) {
		statement.Where("a", missingMap)
		statement.Where("b", "<>", missingSlice)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != `SELECT * FROM "t" WHERE "a" IS NULL AND "b" IS NOT NULL` {
		t.Fatalf("got %q", statement.SQL())
	}
}

// typedNilError is an error type used to build typed nil interface values.
type typedNilError struct{}

// Error returns fixed diagnostic text without dereferencing the receiver.
// A nil *typedNilError can therefore satisfy error in null-binding tests.
//
// Example:
//
//	var failure error = (*typedNilError)(nil)
//	_ = failure.Error()
func (*typedNilError) Error() string {
	return "typed nil"
}

// TestWhereTypedNilInInterfaceIsNull verifies a typed nil held in an interface becomes IS NULL.
func TestWhereTypedNilInInterfaceIsNull(t *testing.T) {
	t.Parallel()

	var missing error = (*typedNilError)(nil)
	statement, err := query.Table("t", func(statement *query.Builder) {
		statement.Where("a", missing)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != `SELECT * FROM "t" WHERE "a" IS NULL` {
		t.Fatalf("got %q", statement.SQL())
	}
}

// TestWhereNonNilPointerIsBound verifies a non-nil pointer is still bound.
func TestWhereNonNilPointerIsBound(t *testing.T) {
	t.Parallel()

	value := 3
	statement, err := query.Table("t", func(statement *query.Builder) {
		statement.Where("a", &value)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != `SELECT * FROM "t" WHERE "a" = ?` || len(statement.Args()) != 1 {
		t.Fatalf("got %q with %v", statement.SQL(), statement.Args())
	}
}

// TestInsertRejectsJoin verifies mutations reject joins instead of dropping them.
func TestInsertRejectsJoin(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Join("u", "t.id", "=", "u.id")
		statement.Insert(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Join("u", "t.id", "=", "u.id")
		statement.Where("a", 1)
		statement.Update(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Join("u", "t.id", "=", "u.id")
		statement.Where("a", 1)
		statement.Delete()
	}))
}

// TestMutationsRejectGroupBy verifies mutations reject GROUP BY.
func TestMutationsRejectGroupBy(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.GroupBy("a")
		statement.Insert(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.GroupBy("a")
		statement.Where("a", 1)
		statement.Update(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.GroupBy("a")
		statement.Where("a", 1)
		statement.Delete()
	}))
}

// TestMutationsRejectHaving verifies mutations reject HAVING.
func TestMutationsRejectHaving(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Having("a", 1)
		statement.Insert(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Having("a", 1)
		statement.Where("a", 1)
		statement.Update(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Having("a", 1)
		statement.Where("a", 1)
		statement.Delete()
	}))
}

// TestMutationsRejectOrderBy verifies mutations reject ORDER BY.
func TestMutationsRejectOrderBy(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("a")
		statement.Insert(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("a")
		statement.Where("a", 1)
		statement.Update(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("a")
		statement.Where("a", 1)
		statement.Delete()
	}))
}

// TestMutationsRejectLimit verifies mutations reject LIMIT.
func TestMutationsRejectLimit(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Limit(1)
		statement.Insert(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Limit(1)
		statement.Where("a", 1)
		statement.Update(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Limit(1)
		statement.Where("a", 1)
		statement.Delete()
	}))
}

// TestMutationsRejectOffset verifies mutations reject OFFSET.
func TestMutationsRejectOffset(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Offset(1)
		statement.Insert(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Offset(1)
		statement.Where("a", 1)
		statement.Update(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Offset(1)
		statement.Where("a", 1)
		statement.Delete()
	}))
}

// TestMutationsRejectDistinct verifies mutations reject DISTINCT.
func TestMutationsRejectDistinct(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Distinct()
		statement.Insert(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Distinct()
		statement.Where("a", 1)
		statement.Update(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Distinct()
		statement.Where("a", 1)
		statement.Delete()
	}))
}

// TestInsertRejectsWhere verifies INSERT cannot use WHERE.
func TestInsertRejectsWhere(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.Insert(map[string]int{"a": 1})
	}))
}

// TestUpdateAndDeleteAcceptWhere verifies WHERE remains valid on UPDATE and DELETE.
func TestUpdateAndDeleteAcceptWhere(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.Update(map[string]int{"b": 2})
	}), query.ANSI,
		`UPDATE "t" SET "b" = ? WHERE "a" = ?`)
	requireBuilderSQL(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.Delete()
	}), query.ANSI, `DELETE FROM "t" WHERE "a" = ?`)
}

// TestWhereGroupRejectsLimit verifies limits are absent from the restricted surface.
func TestWhereGroupRejectsLimit(t *testing.T) {
	t.Parallel()

	if _, ok := any(&query.PredicateBuilder{}).(interface{ Limit(int) *query.Builder }); ok {
		t.Fatal("predicate draft exposes Limit")
	}
}

// TestWhereGroupRejectsJoin verifies group callbacks cannot add joins.
func TestWhereGroupRejectsJoin(t *testing.T) {
	t.Parallel()

	if _, ok := any(&query.PredicateBuilder{}).(interface {
		Join(string, string, string, string) *query.Builder
	}); ok {
		t.Fatal("predicate draft exposes Join")
	}
}

// TestWhereGroupRejectsStatementChange verifies group callbacks cannot change the statement.
func TestWhereGroupRejectsStatementChange(t *testing.T) {
	t.Parallel()

	if _, ok := any(&query.PredicateBuilder{}).(interface{ Delete() *query.Builder }); ok {
		t.Fatal("predicate draft exposes Delete")
	}
}

// TestBuildRejectsLongTableName verifies the 63-byte limit applies to DML tables.
func TestBuildRejectsLongTableName(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table(strings.Repeat("a", 64)))
}

// TestBuildRejectsLongColumnName verifies the 63-byte limit applies to DML columns.
func TestBuildRejectsLongColumnName(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Where(strings.Repeat("a", 64), 1)
	}))
}

// TestBuildAcceptsMaximumNameLength verifies a 63-byte name is still valid.
func TestBuildAcceptsMaximumNameLength(t *testing.T) {
	t.Parallel()

	if _, err := query.Table(strings.Repeat("a", 63)).Build(); err != nil {
		t.Fatal(err)
	}
}

// TestWhereILikePostgres verifies ILIKE compiles on Postgres.
func TestWhereILikePostgres(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", "ilike", "x%")
	}), query.Postgres, `SELECT * FROM "t" WHERE "a" ILIKE $1`)
}

// TestWhereNotILikePostgres verifies NOT ILIKE compiles on Postgres.
func TestWhereNotILikePostgres(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", "NOT ILIKE", "x%")
	}), query.Postgres,
		`SELECT * FROM "t" WHERE "a" NOT ILIKE $1`)
}

// TestWhereILikeUnsupportedOnMySQL verifies MySQL rejects ILIKE.
func TestWhereILikeUnsupportedOnMySQL(t *testing.T) {
	t.Parallel()

	requireUnsupported(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", "ILIKE", "x%")
	}), query.MySQL)
}

// TestWhereILikeUnsupportedOnSQLite verifies SQLite rejects ILIKE.
func TestWhereILikeUnsupportedOnSQLite(t *testing.T) {
	t.Parallel()

	requireUnsupported(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", "ILIKE", "x%")
	}), query.SQLite)
}

// TestWhereILikeUnsupportedOnANSI verifies ANSI rejects ILIKE.
func TestWhereILikeUnsupportedOnANSI(t *testing.T) {
	t.Parallel()

	requireUnsupported(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", "ILIKE", "x%")
	}), query.ANSI)
}

// TestWhereColumnILikeUnsupportedOnMySQL verifies column comparisons reject ILIKE too.
func TestWhereColumnILikeUnsupportedOnMySQL(t *testing.T) {
	t.Parallel()

	_, err := query.Table("t", func(statement *query.Builder) {
		statement.WhereColumn("a", "ILIKE", "b")
	}).BuildFor(query.MySQL)

	if !errors.Is(err, query.ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}

// TestWhereIsDistinctFrom verifies IS DISTINCT FROM is allowed and whitespace-normalized.
func TestWhereIsDistinctFrom(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", "is  distinct from", 1)
	}), query.Postgres,
		`SELECT * FROM "t" WHERE "a" IS DISTINCT FROM $1`)
}

// TestWhereIsNotDistinctFrom verifies IS NOT DISTINCT FROM is allowed.
func TestWhereIsNotDistinctFrom(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", "IS NOT DISTINCT FROM", 1)
	}), query.ANSI,
		`SELECT * FROM "t" WHERE "a" IS NOT DISTINCT FROM ?`)
}

// TestBuilderIsSelect verifies IsSelect distinguishes statement kinds.
func TestBuilderIsSelect(t *testing.T) {
	t.Parallel()

	if !query.Table("t").IsSelect() {
		t.Fatal("select builder reported as not select")
	}

	if query.Table("t", func(statement *query.Builder) {
		statement.Delete()
	}).IsSelect() {
		t.Fatal("delete builder reported as select")
	}

	if query.Table("t", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
	}).IsSelect() {
		t.Fatal("insert builder reported as select")
	}
}
