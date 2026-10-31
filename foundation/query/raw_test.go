package query_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/foundation/query"
)

// FuzzWriteRaw checks raw expression compilation invariants: no panics, build
// errors always match ErrInvalidQuery, and a successful build binds exactly the
// arguments it was given.
func FuzzWriteRaw(f *testing.F) {
	f.Add("COUNT(*) > ?", uint8(1))
	f.Add("data ?? 'a' AND n > ?", uint8(1))
	f.Add("n > ? -- why?\nAND m = ?", uint8(2))
	f.Add("n > /* ? */ ?", uint8(1))
	f.Add("'unterminated ?", uint8(0))
	f.Add("", uint8(0))

	f.Fuzz(func(t *testing.T, expression string, count uint8) {
		args := make([]any, int(count)%8)

		statement, err := query.Table("t", func(statement *query.Builder) {
			statement.HavingRaw(expression, args...)
		}).BuildFor(query.Postgres)

		if err != nil {
			if !errors.Is(err, query.ErrInvalidQuery) {
				t.Fatalf("error does not match ErrInvalidQuery: %v", err)
			}

			return
		}

		if len(statement.Args()) != len(args) {
			t.Fatalf("bound %d arguments for %d given", len(statement.Args()), len(args))
		}

		if !strings.HasPrefix(statement.SQL(), `SELECT * FROM "t" HAVING `) {
			t.Fatalf("unexpected prefix: %q", statement.SQL())
		}

		if !strings.Contains(expression, "?") && !strings.HasSuffix(statement.SQL(), expression) {
			t.Fatalf("expression without placeholders was altered: %q", statement.SQL())
		}
	})
}

// TestRawMySQLBindsAfterDoubleMinus verifies -- without following whitespace
// is subtraction and unary minus, not a MySQL line comment.
func TestRawMySQLBindsAfterDoubleMinus(t *testing.T) {
	t.Parallel()

	statement, err := query.Raw("SELECT ?--?", 1, 2).BuildFor(query.MySQL)

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != "SELECT ?--?" || !reflect.DeepEqual(statement.Args(), []any{1, 2}) {
		t.Fatalf("statement = %q, %#v", statement.SQL(), statement.Args())
	}
}

// TestRawNamedMySQLBindsAfterDoubleMinus verifies named placeholders obey
// the same MySQL comment boundary and preserve their encounter order.
func TestRawNamedMySQLBindsAfterDoubleMinus(t *testing.T) {
	t.Parallel()

	statement, err := query.RawNamed("SELECT :first--:second",
		map[string]any{"first": 1, "second": 2}).BuildFor(query.MySQL)

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != "SELECT ?--?" || !reflect.DeepEqual(statement.Args(), []any{1, 2}) {
		t.Fatalf("statement = %q, %#v", statement.SQL(), statement.Args())
	}
}

// TestRawPostgresSkipsNestedComments verifies a nested block comment cannot
// consume an argument or terminate quoting before the outer comment closes.
func TestRawPostgresSkipsNestedComments(t *testing.T) {
	t.Parallel()

	statement, err := query.Raw("SELECT ? /* outer /* inner */ ? ' */ , ?", 7, 9).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if want := "SELECT $1 /* outer /* inner */ ? ' */ , $2"; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if want := []any{7, 9}; !reflect.DeepEqual(statement.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
	}
}

// TestRawNamedSourcePostgresSkipsNestedComments verifies named bindings and
// outer query bindings remain ordered after PostgreSQL nested comments.
func TestRawNamedSourcePostgresSkipsNestedComments(t *testing.T) {
	t.Parallel()

	source := query.RawNamed("SELECT :id /* outer /* inner */ :ignored */", map[string]any{"id": 7})

	statement, err := query.FromRaw(source, func(statement *query.Builder) {
		statement.Where("active", true)
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	want := "SELECT * FROM (\nSELECT $1 /* outer /* inner */ :ignored */\n) AS \"t\" WHERE \"active\" = $2"

	if statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if want := []any{7, true}; !reflect.DeepEqual(statement.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
	}
}

// TestRawPostgresEndsLineCommentAtCarriageReturn verifies PostgreSQL's CR
// line ending does not hide real placeholders in the next SQL line.
func TestRawPostgresEndsLineCommentAtCarriageReturn(t *testing.T) {
	t.Parallel()

	statement, err := query.Raw("SELECT ? -- ignored ?\r, ?", 7, 9).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if want := "SELECT $1 -- ignored ?\r, $2"; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if want := []any{7, 9}; !reflect.DeepEqual(statement.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
	}
}

// TestRawNamedPostgresEndsLineCommentAtCarriageReturn verifies the common
// scanner resumes named binding after the same PostgreSQL line ending.
func TestRawNamedPostgresEndsLineCommentAtCarriageReturn(t *testing.T) {
	t.Parallel()

	statement, err := query.RawNamed("SELECT :id -- :ignored\r, :next",
		map[string]any{"id": 7, "next": 9}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if want := "SELECT $1 -- :ignored\r, $2"; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if want := []any{7, 9}; !reflect.DeepEqual(statement.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
	}
}

// TestRawNonNestingDialectsKeepFirstCommentTerminator protects MySQL and
// SQLite: an inner /* is comment text, not the start of a nested comment.
func TestRawNonNestingDialectsKeepFirstCommentTerminator(t *testing.T) {
	t.Parallel()

	for _, dialect := range []query.Dialect{query.MySQL, query.SQLite} {
		statement, err := query.Raw("SELECT 1 /* outer /* inner */ + ?", 7).BuildFor(dialect)

		if err != nil {
			t.Fatal(err)
		}

		if want := "SELECT 1 /* outer /* inner */ + ?"; statement.SQL() != want {
			t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
		}

		if want := []any{7}; !reflect.DeepEqual(statement.Args(), want) {
			t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
		}
	}
}

// TestRawANSISkipsNestedComments covers the standard dialect's matching block
// delimiters independently of PostgreSQL's numbered placeholders.
func TestRawANSISkipsNestedComments(t *testing.T) {
	t.Parallel()

	statement, err := query.Raw("SELECT ? /* outer /* inner */ ? */ , ?", 7, 9).Build()

	if err != nil {
		t.Fatal(err)
	}

	if want := "SELECT ? /* outer /* inner */ ? */ , ?"; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if want := []any{7, 9}; !reflect.DeepEqual(statement.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
	}
}

// TestRawNamedKeepsDoubledQuotesAndCasts protects the standard quoting rules
// when quotes contain named parameters, comment markers, or literal colons.
func TestRawNamedKeepsDoubledQuotesAndCasts(t *testing.T) {
	t.Parallel()

	statement, err := query.RawNamed("SELECT ':a''/* :b */', \":a\"\":b\", `:a``:b`, :id::text",
		map[string]any{"id": 7}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if want := "SELECT ':a''/* :b */', \":a\"\":b\", `:a``:b`, $1::text"; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if want := []any{7}; !reflect.DeepEqual(statement.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
	}
}

// TestSelectRawKeepsLiteralQuestionMark verifies selection expressions do not
// use the binding scanner: PostgreSQL's JSON operator stays a single ?.
func TestSelectRawKeepsLiteralQuestionMark(t *testing.T) {
	t.Parallel()

	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.SelectRaw("data ? 'name' AS present")
		statement.Where("id", 7)
	}).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if want := `SELECT data ? 'name' AS present FROM "users" WHERE "id" = $1`; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if want := []any{7}; !reflect.DeepEqual(statement.Args(), want) {
		t.Fatalf("Args = %#v, want %#v", statement.Args(), want)
	}
}
