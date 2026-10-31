package query_test

import (
	"reflect"
	"testing"

	"arkoren.dev/foundation/query"
)

// TestWhereGroupBuildsNestedPredicates verifies explicit logical grouping.
func TestWhereGroupBuildsNestedPredicates(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("name", "John")
		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
			predicate.Where("votes", ">", 100).OrWhere("title", "Admin")
		})
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `SELECT * FROM "users" WHERE "name" = ? AND ("votes" > ? OR "title" = ?)`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{"John", 100, "Admin"}) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestWhereNotGroupBuildsNegatedPredicates verifies grouped negation.
func TestWhereNotGroupBuildsNegatedPredicates(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("products", func(statement *query.Builder) {
		statement.WhereNotGroup(func(predicate *query.PredicateBuilder) {
			predicate.Where("clearance", true).OrWhere("price", "<", 10)
		})
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `SELECT * FROM "products" WHERE NOT ("clearance" = ? OR "price" < ?)`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}
}

// TestWhereNullNormalizesNilEquality verifies Laravel-like nil handling.
func TestWhereNullNormalizesNilEquality(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Where("deleted_at", nil)
		statement.OrWhere("verified_at", "!=", nil)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `SELECT * FROM "users" WHERE "deleted_at" IS NULL OR "verified_at" IS NOT NULL`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}

	if len(statement.Args()) != 0 {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestWhereInAcceptsTypedValues verifies generic membership values.
func TestWhereInAcceptsTypedValues(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.WhereIn("id", 1, 2, 3)
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `SELECT * FROM "users" WHERE "id" IN (?, ?, ?)`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}

	if !reflect.DeepEqual(statement.Args(), []any{1, 2, 3}) {
		t.Fatalf("unexpected arguments: %#v", statement.Args())
	}
}

// TestWhereInEmptyValuesBuildsFalsePredicate verifies defined empty membership
// semantics.
func TestWhereInEmptyValuesBuildsFalsePredicate(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.WhereIn[int]("id")
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != `SELECT * FROM "users" WHERE 0 = 1` {
		t.Fatalf("unexpected SQL: %q", statement.SQL())
	}
}

// TestWhereNotInEmptyValuesBuildsTruePredicate verifies defined empty negative
// membership semantics.
func TestWhereNotInEmptyValuesBuildsTruePredicate(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.WhereNotIn[int]("id")
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != `SELECT * FROM "users" WHERE 1 = 1` {
		t.Fatalf("unexpected SQL: %q", statement.SQL())
	}
}

// TestWhereBetweenAndColumnBuildPredicates verifies range and column
// comparisons.
func TestWhereBetweenAndColumnBuildPredicates(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.WhereBetween("votes", 1, 100)
		statement.WhereColumn("updated_at", ">", "created_at")
	}).Build()

	if err != nil {
		t.Fatal(err)
	}

	expected := `SELECT * FROM "users" WHERE "votes" BETWEEN ? AND ? ` +
		`AND "updated_at" > "created_at"`

	if statement.SQL() != expected {
		t.Fatalf("expected %q, got %q", expected, statement.SQL())
	}
}
