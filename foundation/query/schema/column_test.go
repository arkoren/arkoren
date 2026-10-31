package schema_test

import (
	"math"
	"strings"
	"testing"

	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// TestColumnValidationPrefersIdentityType characterizes the first diagnostic
// when an identity column also has invalid nullability, defaults, and actions.
func TestColumnValidationPrefersIdentityType(t *testing.T) {
	t.Parallel()

	operation := schema.Create("users", func(table *schema.Table) {
		table.Text("id").AutoIncrement().Nullable().DefaultNow().OnDelete(schema.Cascade)
	})

	requireInvalid(t, operation)

	if !strings.Contains(operation.Err().Error(), `auto-increment column "id" must be an integer`) {
		t.Fatal(operation.Err())
	}
}

// TestColumnValidationPrefersIdentityNullability keeps the nullable-identity
// failure ahead of both its default conflict and incompatible timestamp default.
func TestColumnValidationPrefersIdentityNullability(t *testing.T) {
	t.Parallel()

	operation := schema.Create("users", func(table *schema.Table) {
		table.Integer("id").AutoIncrement().Nullable().DefaultNow()
	})

	requireInvalid(t, operation)

	if !strings.Contains(operation.Err().Error(), `auto-increment column "id" cannot be nullable`) {
		t.Fatal(operation.Err())
	}
}

// TestColumnValidationPrefersIdentityDefault keeps the identity's default
// prohibition ahead of the timestamp-type and missing-reference diagnostics.
func TestColumnValidationPrefersIdentityDefault(t *testing.T) {
	t.Parallel()

	operation := schema.Create("users", func(table *schema.Table) {
		table.Integer("id").AutoIncrement().DefaultNow().OnDelete(schema.Cascade)
	})

	requireInvalid(t, operation)

	if !strings.Contains(operation.Err().Error(), `auto-increment column "id" cannot have a default`) {
		t.Fatal(operation.Err())
	}
}

// TestColumnValidationPrefersTimestampDefault retains the default-type error
// before an otherwise invalid referential action on the same column.
func TestColumnValidationPrefersTimestampDefault(t *testing.T) {
	t.Parallel()

	operation := schema.Create("users", func(table *schema.Table) {
		table.Text("name").DefaultNow().OnDelete(schema.Cascade)
	})

	requireInvalid(t, operation)

	if !strings.Contains(operation.Err().Error(), `column "name": DefaultNow needs a timestamp column`) {
		t.Fatal(operation.Err())
	}
}

// TestChangeUsingAloneRemainsEmpty preserves the empty-change diagnostic:
// a conversion expression alone is not an independently changed aspect.
func TestChangeUsingAloneRemainsEmpty(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("users", func(table *schema.Table) {
		table.Change("id").Using("id::integer")
	})

	requireInvalid(t, operation)

	if !strings.Contains(operation.Err().Error(), `change of column "id" sets nothing`) {
		t.Fatal(operation.Err())
	}
}

// TestChangeUsingRequiresTypeAfterOtherAspect keeps type validation distinct
// from the empty-change check when nullability was explicitly selected.
func TestChangeUsingRequiresTypeAfterOtherAspect(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("users", func(table *schema.Table) {
		table.Change("id").NotNull().Using("id::integer")
	})

	requireInvalid(t, operation)

	if !strings.Contains(operation.Err().Error(), `change of column "id": Using needs a type`) {
		t.Fatal(operation.Err())
	}
}

// TestChangeUntypedDefaultNowRemainsValid leaves the existing column's type
// to the database when the declaration changes only its timestamp default.
func TestChangeUntypedDefaultNowRemainsValid(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("events", func(table *schema.Table) {
		table.Change("created_at").DefaultNow()
	})

	requireSQL(t, operation, query.Postgres,
		`ALTER TABLE "events" ALTER COLUMN "created_at" SET DEFAULT CURRENT_TIMESTAMP`)
	requireSQL(t, operation, query.MySQL,
		"ALTER TABLE `events` ALTER COLUMN `created_at` SET DEFAULT CURRENT_TIMESTAMP(6)")
}

// TestChangeTypedDefaultNowKeepsTypeError rejects an explicit incompatible
// type, without changing the valid untyped default-only declaration above.
func TestChangeTypedDefaultNowKeepsTypeError(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("events", func(table *schema.Table) {
		table.Change("created_at").Text().DefaultNow()
	})

	requireInvalid(t, operation)

	if !strings.Contains(operation.Err().Error(), `column "created_at": DefaultNow needs a timestamp column`) {
		t.Fatal(operation.Err())
	}
}

// TestDefaultKeepsInvalidLiteralErrors verifies that an invalid value cannot
// be repaired by a later default or callback before construction is checked.
func TestDefaultKeepsInvalidLiteralErrors(t *testing.T) {
	t.Parallel()

	for _, value := range []any{struct{}{}, (*int)(nil), math.NaN(), math.Inf(1), math.Inf(-1), "bad\x00text"} {
		calls := 0
		operation := schema.Create("settings", func(table *schema.Table) {
			table.Text("value").Default(value).Default("replacement")
		}, func(table *schema.Table) {
			calls++
			table.Integer("version")
		})

		requireInvalid(t, operation)

		if calls != 1 {
			t.Fatalf("later callback ran %d times, want 1", calls)
		}
	}
}

// TestChangeDefaultKeepsInvalidLiteralError covers the same capture contract
// when an ALTER default is replaced or dropped before publication.
func TestChangeDefaultKeepsInvalidLiteralError(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("settings", func(table *schema.Table) {
		table.Change("value").Default(math.NaN()).DropDefault()
	})

	requireInvalid(t, operation)
}

// TestDefaultReportsFirstLiteralError verifies a later invalid column does not
// replace the first diagnostic retained by the shared declaration draft.
func TestDefaultReportsFirstLiteralError(t *testing.T) {
	t.Parallel()

	operation := schema.Create("settings", func(table *schema.Table) {
		table.Text("first").Default(struct{}{})
		table.Float("second").Default(math.NaN())
	})

	requireInvalid(t, operation)

	if !strings.Contains(operation.Err().Error(), `column "first"`) {
		t.Fatalf("first error was replaced: %v", operation.Err())
	}
}

// TestDropForeignRejectsLongGeneratedName requires construction preflight to
// reject an overlong constraint name before any statement can be executed.
func TestDropForeignRejectsLongGeneratedName(t *testing.T) {
	t.Parallel()

	operation := schema.Alter(strings.Repeat("a", 60), func(table *schema.Table) {
		table.DropForeign("x")
	})

	requireInvalid(t, operation)
}
