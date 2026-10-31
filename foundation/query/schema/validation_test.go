package schema_test

import (
	"strings"
	"testing"

	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// TestValidationReportsFirstActionError preserves declaration-order diagnostics
// even when collecting all columns is necessary to resolve forward references.
func TestValidationReportsFirstActionError(t *testing.T) {
	t.Parallel()

	operation := schema.Create("users", func(table *schema.Table) {
		table.Index("missing")
		table.Text("name")
		table.Text("name")
	})

	requireInvalid(t, operation)

	if !strings.Contains(operation.Err().Error(), `key column "missing" is not defined`) {
		t.Fatalf("action order changed the first error: %v", operation.Err())
	}
}

// TestAlterAddedPrimaryForeignColumn preserves both named constraints on a
// non-SQLite column; SQLite still rejects added primary-key constraints.
func TestAlterAddedPrimaryForeignColumn(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("profiles", func(table *schema.Table) {
		table.BigInteger("user_id").Primary().References("users", "id")
	})

	requireSQL(t, operation, query.Postgres,
		`ALTER TABLE "profiles" ADD COLUMN "user_id" bigint NOT NULL, `+
			`ADD CONSTRAINT "profiles_pkey" PRIMARY KEY ("user_id"), `+
			`ADD CONSTRAINT "profiles_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users" ("id")`)
	requireUnsupported(t, operation, query.SQLite)
}

// TestReferentialActionsCanBeCleared verifies empty action values restore
// omitted clauses, rather than emitting an invalid or explicit default action.
func TestReferentialActionsCanBeCleared(t *testing.T) {
	t.Parallel()

	operation := schema.Create("posts", func(table *schema.Table) {
		table.ForeignID("user_id", "users").OnDelete(schema.Cascade).OnDelete("").OnUpdate(schema.NoAction).OnUpdate("")
	})

	requireSQL(t, operation, query.Postgres,
		`CREATE TABLE "posts" ("user_id" bigint NOT NULL, `+
			`CONSTRAINT "posts_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users" ("id"))`)
}
