package schema_test

import (
	"strings"
	"testing"

	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// TestGuardedCreateANSIRestrictionComesFirst preserves the table-level
// restriction before raw placeholder compilation can reject a column's type.
func TestGuardedCreateANSIRestrictionComesFirst(t *testing.T) {
	t.Parallel()

	operation := schema.CreateIfNotExists("users", func(table *schema.Table) {
		table.Column("name", "?")
	})

	requireUnsupported(t, operation, query.ANSI)

	_, err := operation.Statements()[0].BuildFor(query.ANSI)

	if !strings.Contains(err.Error(), "CREATE TABLE IF NOT EXISTS on ansi") {
		t.Fatal(err)
	}
}

// TestChangeSQLiteRestrictionPrecedesUsing preserves the first native feature
// rejection when SQLite cannot express either the change or its conversion.
func TestChangeSQLiteRestrictionPrecedesUsing(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("users", func(table *schema.Table) {
		table.Change("id").Integer().Using("id::integer")
	})

	requireUnsupported(t, operation, query.SQLite)

	_, err := operation.Statements()[0].BuildFor(query.SQLite)

	if !strings.Contains(err.Error(), "ALTER COLUMN on sqlite") {
		t.Fatal(err)
	}
}

// TestChangeMySQLUsingRestrictionPrecedesRestatement preserves USING's
// rejection before MySQL's requirement for both type and nullability.
func TestChangeMySQLUsingRestrictionPrecedesRestatement(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("users", func(table *schema.Table) {
		table.Change("id").Integer().Using("id::integer")
	})

	requireUnsupported(t, operation, query.MySQL)

	_, err := operation.Statements()[0].BuildFor(query.MySQL)

	if !strings.Contains(err.Error(), "ALTER COLUMN ... USING on mysql") {
		t.Fatal(err)
	}
}

// TestChangePostgresKeepsAspectOrderAndSnapshot preserves TYPE/USING before
// nullability and default clauses, with no reads from retained change drafts.
func TestChangePostgresKeepsAspectOrderAndSnapshot(t *testing.T) {
	t.Parallel()

	var retained *schema.Change
	operation := schema.Alter("users", func(table *schema.Table) {
		retained = table.Change("id").BigInteger().Using("id::bigint").NotNull().Default(0).DropDefault()
	})

	retained.Text().Nullable().Default("later").Using("id::text")

	requireSQL(t, operation, query.Postgres,
		`ALTER TABLE "users" ALTER COLUMN "id" TYPE bigint USING id::bigint, `+
			`ALTER COLUMN "id" SET NOT NULL, ALTER COLUMN "id" DROP DEFAULT`)
}

// TestChangeMySQLRestatesOnlyRequestedDefinition preserves MODIFY COLUMN's
// whole-column spelling without inventing a default that was not restated.
func TestChangeMySQLRestatesOnlyRequestedDefinition(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("users", func(table *schema.Table) {
		table.Change("name").String(160).Nullable()
	})

	requireSQL(t, operation, query.MySQL, "ALTER TABLE `users` MODIFY COLUMN `name` varchar(160)")
}

// TestGuardedCreateKeepsColumnsKeysAndIndexOrder covers forward declarations,
// two callbacks, and later draft edits while keeping CREATE and index order.
func TestGuardedCreateKeepsColumnsKeysAndIndexOrder(t *testing.T) {
	t.Parallel()

	var retained *schema.Column
	operation := schema.CreateIfNotExists("main.memberships", func(table *schema.Table) {
		table.Index("user_id")
		table.Primary("tenant_id", "user_id")
		table.Integer("tenant_id").References("tenants", "id")
	}, func(table *schema.Table) {
		retained = table.BigInteger("user_id").Unique()
	})

	retained.Nullable().Default(7)

	requireSQL(t, operation, query.SQLite,
		`CREATE TABLE IF NOT EXISTS "main"."memberships" ("tenant_id" INTEGER NOT NULL, `+
			`"user_id" INTEGER NOT NULL, CONSTRAINT "main_memberships_pkey" PRIMARY KEY ("tenant_id", "user_id"), `+
			`CONSTRAINT "main_memberships_tenant_id_fkey" FOREIGN KEY ("tenant_id") REFERENCES "tenants" ("id"))`,
		`CREATE INDEX IF NOT EXISTS "main"."main_memberships_user_id_index" ON "memberships" ("user_id")`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "main"."main_memberships_user_id_key" ON "memberships" ("user_id")`)
}

// TestAlterKeepsEveryActionInWrittenOrder characterizes all ALTER action
// families, including a unique column's extra index and native foreign drops.
func TestAlterKeepsEveryActionInWrittenOrder(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("s.posts", func(table *schema.Table) {
		table.DropColumn("legacy")
		table.RenameColumn("old", "new")
		table.Change("new").String(20).NotNull()
		table.DropIndex("new")
		table.DropUnique("email")
		table.Foreign("user_id", "users", "id").OnDelete(schema.Cascade)
		table.DropForeign("user_id")
		table.Text("tag").Nullable().Unique()
		table.Index("tag")
	})

	requireSQL(t, operation, query.Postgres,
		`ALTER TABLE "s"."posts" DROP COLUMN "legacy"`,
		`ALTER TABLE "s"."posts" RENAME COLUMN "old" TO "new"`,
		`ALTER TABLE "s"."posts" ALTER COLUMN "new" TYPE varchar(20), ALTER COLUMN "new" SET NOT NULL`,
		`DROP INDEX "s"."s_posts_new_index"`,
		`DROP INDEX "s"."s_posts_email_key"`,
		`ALTER TABLE "s"."posts" ADD CONSTRAINT "s_posts_user_id_fkey" `+
			`FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON DELETE CASCADE`,
		`ALTER TABLE "s"."posts" DROP CONSTRAINT "s_posts_user_id_fkey"`,
		`ALTER TABLE "s"."posts" ADD COLUMN "tag" text`,
		`CREATE UNIQUE INDEX "s_posts_tag_key" ON "s"."posts" ("tag")`,
		`CREATE INDEX "s_posts_tag_index" ON "s"."posts" ("tag")`)
	requireSQL(t, operation, query.MySQL,
		"ALTER TABLE `s`.`posts` DROP COLUMN `legacy`",
		"ALTER TABLE `s`.`posts` RENAME COLUMN `old` TO `new`",
		"ALTER TABLE `s`.`posts` MODIFY COLUMN `new` varchar(20) NOT NULL",
		"DROP INDEX `s_posts_new_index` ON `s`.`posts`",
		"DROP INDEX `s_posts_email_key` ON `s`.`posts`",
		"ALTER TABLE `s`.`posts` ADD CONSTRAINT `s_posts_user_id_fkey` "+
			"FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE",
		"ALTER TABLE `s`.`posts` DROP FOREIGN KEY `s_posts_user_id_fkey`",
		"ALTER TABLE `s`.`posts` ADD COLUMN `tag` text",
		"CREATE UNIQUE INDEX `s_posts_tag_key` ON `s`.`posts` (`tag`)",
		"CREATE INDEX `s_posts_tag_index` ON `s`.`posts` (`tag`)")
}

// FuzzStringLiteral checks that rendered default string literals stay
// quote-balanced for every dialect: the body holds only doubled single quotes,
// and on MySQL only doubled backslashes.
func FuzzStringLiteral(f *testing.F) {
	f.Add("plain")
	f.Add("it's")
	f.Add(`a\b`)
	f.Add(`\'`)
	f.Add("nul\x00byte")
	f.Add("a?b")
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		op := schema.Create("t", func(table *schema.Table) {
			table.String("a", 10).Default(text)
		})

		for _, dialect := range []query.Dialect{query.ANSI, query.Postgres, query.MySQL, query.SQLite} {
			statement, err := op.Statements()[0].BuildFor(dialect)

			if err != nil {
				if !strings.Contains(text, "\x00") {
					t.Fatalf("%s: unexpected error for %q: %v", dialect, text, err)
				}

				continue
			}

			_, literal, _ := strings.Cut(statement.SQL(), " DEFAULT ")
			literal = strings.TrimSuffix(literal, ")")

			if len(literal) < 2 || literal[0] != '\'' || literal[len(literal)-1] != '\'' {
				t.Fatalf("%s: literal %q is not quoted", dialect, literal)
			}

			body := literal[1 : len(literal)-1]

			if strings.Contains(strings.ReplaceAll(body, "''", ""), "'") {
				t.Fatalf("%s: unbalanced quote in %q", dialect, literal)
			}

			if dialect == query.MySQL && strings.Contains(strings.ReplaceAll(body, `\\`, ""), `\`) {
				t.Fatalf("mysql: unescaped backslash in %q", literal)
			}
		}
	})
}

// TestSQLiteRejectsAddedNotNullColumnWithNullDefault verifies DEFAULT NULL
// cannot bypass SQLite's requirement for a non-NULL default on ADD COLUMN.
func TestSQLiteRejectsAddedNotNullColumnWithNullDefault(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("users", func(table *schema.Table) {
		table.Integer("visits").Default(nil)
	})

	requireUnsupported(t, operation, query.SQLite)
}

// TestSQLiteRejectsQualifiedForeignTable verifies the compiler does not emit
// a schema-qualified REFERENCES target, which SQLite's grammar cannot express.
func TestSQLiteRejectsQualifiedForeignTable(t *testing.T) {
	t.Parallel()

	operation := schema.Create("posts", func(table *schema.Table) {
		table.ForeignID("user_id", "main.users")
	})

	requireUnsupported(t, operation, query.SQLite)
	requireSQL(t, operation, query.Postgres,
		`CREATE TABLE "posts" ("user_id" bigint NOT NULL, CONSTRAINT "posts_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "main"."users" ("id"))`)
}

// TestSQLiteRejectsAddedQualifiedForeignTable covers SQLite's inline
// REFERENCES rendering for a newly added nullable column.
func TestSQLiteRejectsAddedQualifiedForeignTable(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("posts", func(table *schema.Table) {
		table.ForeignID("user_id", "main.users").Nullable()
	})

	requireUnsupported(t, operation, query.SQLite)
}

// TestAlterRejectsMultiplePrimaryColumns verifies separate ADD COLUMN actions
// cannot publish two primary-key constraints with the same generated name.
func TestAlterRejectsMultiplePrimaryColumns(t *testing.T) {
	t.Parallel()

	operation := schema.Alter("memberships", func(table *schema.Table) {
		table.Integer("tenant_id").Primary()
		table.Integer("user_id").Primary()
	})

	requireInvalid(t, operation)
}
