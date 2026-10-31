package query_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"arkoren.dev/foundation/query"
)

// TestUpsertRejectsDuplicateAssignments verifies duplicate update names are
// invalid across dialects and stay invalid after a later completion or Derive.
func TestUpsertRejectsDuplicateAssignments(t *testing.T) {
	t.Parallel()

	invalid := query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]any{"name": "Ada"})
		statement.OnConflict("name").DoUpdate("name", "name")
	})
	overridden := invalid.Derive(func(statement *query.Builder) {
		statement.OnConflict("name").DoNothing()
	})

	for _, dialect := range []query.Dialect{query.Postgres, query.SQLite, query.MySQL} {
		_, err := invalid.BuildFor(dialect)

		if !errors.Is(err, query.ErrInvalidQuery) {
			t.Fatalf("BuildFor(%s) = %v, want ErrInvalidQuery", dialect, err)
		}

		_, err = overridden.BuildFor(dialect)

		if !errors.Is(err, query.ErrInvalidQuery) {
			t.Fatalf("overridden BuildFor(%s) = %v, want ErrInvalidQuery", dialect, err)
		}
	}
}

// TestZeroPagingRemainsExplicit verifies zero limit/offset survive publication
// and inspection without selecting the omitted/unbounded behavior.
func TestZeroPagingRemainsExplicit(t *testing.T) {
	t.Parallel()

	definition := query.Table("users", func(statement *query.Builder) {
		statement.Limit(0)
		statement.Offset(0)
	})

	spec := definition.Inspect()

	if spec.Limit == nil || spec.Offset == nil {
		t.Fatal("explicit zero paging disappeared from Inspect")
	}

	if *spec.Limit != 0 || *spec.Offset != 0 {
		t.Fatalf("paging = %d, %d, want explicit zeros", *spec.Limit, *spec.Offset)
	}

	statement, err := definition.BuildFor(query.SQLite)

	if err != nil {
		t.Fatal(err)
	}

	if want := `SELECT * FROM "users" LIMIT 0 OFFSET 0`; statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}
}

// TestNegativePagingErrorsStaySticky verifies replacing invalid pagination
// does not repair the original declaration error, including through Derive.
func TestNegativePagingErrorsStaySticky(t *testing.T) {
	t.Parallel()

	queries := []query.Query{
		query.Table("users", func(statement *query.Builder) {
			statement.Limit(-1)
			statement.Limit(20)
		}),
		query.Table("users", func(statement *query.Builder) {
			statement.Offset(-1)
			statement.Offset(0)
		}),
	}

	for _, definition := range queries {
		_, err := definition.Derive().BuildFor(query.Postgres)

		if !errors.Is(err, query.ErrInvalidQuery) {
			t.Fatalf("BuildFor = %v, want ErrInvalidQuery", err)
		}
	}
}

// ExampleBuilderFunc composes a reusable construction policy through the public
// alias and compiles its published query, without retaining a mutable draft.
func ExampleBuilderFunc() {
	policies := []query.BuilderFunc{
		func(statement *query.Builder) {
			statement.Where("active", true)
		},
	}
	statement, err := query.Table("users", policies...).BuildFor(query.Postgres)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(statement.SQL())
	fmt.Println(statement.Args())
	// Output:
	// SELECT * FROM "users" WHERE "active" = $1
	// [true]
}

// ExampleBuilder_HavingRaw selects an aggregate before filtering its single
// group, rather than retaining the incompatible default wildcard selection.
func ExampleBuilder_HavingRaw() {
	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.SelectRaw("COUNT(*) AS total")
		statement.HavingRaw("COUNT(*) > ?", 5)
	}).BuildFor(query.Postgres)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(statement.SQL(), statement.Args())
	// Output:
	// SELECT COUNT(*) AS total FROM "users" HAVING COUNT(*) > $1 [5]
}

// ExampleBuilder_OrHaving filters a compatible grouped selection and appends
// the second group filter with OR while retaining binding order.
func ExampleBuilder_OrHaving() {
	statement, err := query.Table("users", func(statement *query.Builder) {
		statement.Select("role")
		statement.GroupBy("role")
		statement.Having("role", "admin")
		statement.OrHaving("role", "owner")
	}).BuildFor(query.Postgres)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(statement.SQL(), statement.Args())
	// Output:
	// SELECT "role" FROM "users" GROUP BY "role" HAVING "role" = $1 OR "role" = $2 [admin owner]
}

// TestFromRawDefaultAlias verifies the default alias and binding order.
func TestFromRawDefaultAlias(t *testing.T) {
	t.Parallel()

	builder := query.FromRaw(query.Raw("SELECT id, n FROM x WHERE k = ?", "a"), func(statement *query.Builder) {
		statement.Select("id")
		statement.Where("n", ">", 3)
	})

	requireBuilderSQL(t, builder, query.ANSI,
		"SELECT \"id\" FROM (\nSELECT id, n FROM x WHERE k = ?\n) AS \"t\" WHERE \"n\" > ?")
	requireBuilderSQL(t, builder, query.SQLite,
		"SELECT \"id\" FROM (\nSELECT id, n FROM x WHERE k = ?\n) AS \"t\" WHERE \"n\" > ?")
	requireBuilderSQL(t, builder, query.Postgres,
		"SELECT \"id\" FROM (\nSELECT id, n FROM x WHERE k = $1\n) AS \"t\" WHERE \"n\" > $2")
	requireBuilderSQL(t, builder, query.MySQL,
		"SELECT `id` FROM (\nSELECT id, n FROM x WHERE k = ?\n) AS `t` WHERE `n` > ?")

	statement, err := builder.Build()

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(statement.Args(), []any{"a", 3}) {
		t.Fatalf("unexpected args: %#v", statement.Args())
	}
}

// TestFromRawAs verifies a custom alias on every dialect.
func TestFromRawAs(t *testing.T) {
	t.Parallel()

	builder := query.FromRaw(query.Raw("SELECT 1 AS a"), func(statement *query.Builder) {
		statement.As("one")
	})

	requireBuilderSQL(t, builder, query.ANSI, "SELECT * FROM (\nSELECT 1 AS a\n) AS \"one\"")
	requireBuilderSQL(t, builder, query.SQLite, "SELECT * FROM (\nSELECT 1 AS a\n) AS \"one\"")
	requireBuilderSQL(t, builder, query.Postgres, "SELECT * FROM (\nSELECT 1 AS a\n) AS \"one\"")
	requireBuilderSQL(t, builder, query.MySQL, "SELECT * FROM (\nSELECT 1 AS a\n) AS `one`")
}

// TestFromRawPlaceholderMismatch verifies the placeholder count is checked.
func TestFromRawPlaceholderMismatch(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.FromRaw(query.Raw("SELECT ?")))
}

// TestFromRawEmpty verifies a blank source fails.
func TestFromRawEmpty(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.FromRaw(query.Raw(" ")))
}

// TestFromRawRejectsMutations verifies a raw source cannot be mutated.
func TestFromRawRejectsMutations(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.FromRaw(query.Raw("SELECT 1"), func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
	}))
	requireBuilderInvalid(t, query.FromRaw(query.Raw("SELECT 1"), func(statement *query.Builder) {
		statement.Update(map[string]int{"a": 1})
		statement.Unfiltered()
	}))
	requireBuilderInvalid(t, query.FromRaw(query.Raw("SELECT 1"), func(statement *query.Builder) {
		statement.Delete()
		statement.Unfiltered()
	}))
	requireBuilderInvalid(t, query.FromRaw(query.Raw("SELECT 1"), func(statement *query.Builder) {
		statement.Increment("a", 1)
		statement.Unfiltered()
	}))
}

// TestAsRequiresSource verifies As fails on a table builder and on a bad alias.
func TestAsRequiresSource(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("users", func(statement *query.Builder) {
		statement.As("u")
	}))
	requireBuilderInvalid(t, query.FromRaw(query.Raw("SELECT 1"), func(statement *query.Builder) {
		statement.As("a.b")
	}))
	requireBuilderInvalid(t, query.FromRaw(query.Raw("SELECT 1"), func(statement *query.Builder) {
		statement.As("bad alias")
	}))
}

// TestFromReplacesRawSource verifies From returns to a plain table.
func TestFromReplacesRawSource(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.FromRaw(query.Raw("SELECT 1"), func(statement *query.Builder) {
		statement.As("x")
		statement.From("users")
	}), query.ANSI,
		`SELECT * FROM "users"`)
}

// TestWhereRaw verifies WhereRaw and OrWhereRaw on every dialect.
func TestWhereRaw(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("active", true)
		statement.WhereRaw("lower(email) = ?", "a@b.c")
		statement.OrWhereRaw("age > ? AND age < ?", 1, 9)
	})

	requireBuilderSQL(t, builder, query.ANSI,
		`SELECT * FROM "users" WHERE "active" = ? AND lower(email) = ? OR age > ? AND age < ?`)
	requireBuilderSQL(t, builder, query.SQLite,
		`SELECT * FROM "users" WHERE "active" = ? AND lower(email) = ? OR age > ? AND age < ?`)
	requireBuilderSQL(t, builder, query.Postgres,
		`SELECT * FROM "users" WHERE "active" = $1 AND lower(email) = $2 OR age > $3 AND age < $4`)
	requireBuilderSQL(t, builder, query.MySQL,
		"SELECT * FROM `users` WHERE `active` = ? AND lower(email) = ? OR age > ? AND age < ?")
}

// TestWhereRawInGroupAndUpdate verifies raw predicates work in groups and mutations.
func TestWhereRawInGroupAndUpdate(t *testing.T) {
	t.Parallel()

	builder := query.Table("t", func(statement *query.Builder) {
		statement.Update(map[string]int{"a": 1})
		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
			predicate.WhereRaw("x ?? 'k'").OrWhereRaw("y = ?", 2)
		})
	})

	requireBuilderSQL(t, builder, query.Postgres, `UPDATE "t" SET "a" = $1 WHERE (x ? 'k' OR y = $2)`)
}

// TestWhereRawErrors verifies empty expressions and wrong counts fail.
func TestWhereRawErrors(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.WhereRaw("")
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.OrWhereRaw("a = ?")
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.WhereRaw("a = 1", 2)
	}))
}

// TestReturningInsert verifies RETURNING on INSERT.
func TestReturningInsert(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
		statement.Returning("id", "a")
	})

	requireBuilderSQL(t, builder, query.Postgres, `INSERT INTO "users" ("a") VALUES ($1) RETURNING "id", "a"`)
	requireBuilderSQL(t, builder, query.SQLite, `INSERT INTO "users" ("a") VALUES (?) RETURNING "id", "a"`)
	requireUnsupported(t, builder, query.MySQL)
	requireUnsupported(t, builder, query.ANSI)
}

// TestReturningUpdate verifies RETURNING on UPDATE.
func TestReturningUpdate(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.Update(map[string]int{"a": 1})
		statement.Returning("*")
	})

	requireBuilderSQL(t, builder, query.Postgres, `UPDATE "users" SET "a" = $1 WHERE "id" = $2 RETURNING *`)
	requireBuilderSQL(t, builder, query.SQLite, `UPDATE "users" SET "a" = ? WHERE "id" = ? RETURNING *`)
	requireUnsupported(t, builder, query.MySQL)
	requireUnsupported(t, builder, query.ANSI)
}

// TestReturningDelete verifies RETURNING on DELETE.
func TestReturningDelete(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.Delete()
		statement.Returning("id")
	})

	requireBuilderSQL(t, builder, query.Postgres, `DELETE FROM "users" WHERE "id" = $1 RETURNING "id"`)
	requireBuilderSQL(t, builder, query.SQLite, `DELETE FROM "users" WHERE "id" = ? RETURNING "id"`)
	requireUnsupported(t, builder, query.MySQL)
	requireUnsupported(t, builder, query.ANSI)
}

// TestReturningErrors verifies empty, invalid, and select usage fail.
func TestReturningErrors(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Delete()
		statement.Unfiltered()
		statement.Returning()
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Delete()
		statement.Unfiltered()
		statement.Returning("a b")
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Returning("id")
	}))
}

// TestOnConflictDoUpdate verifies upsert on every dialect.
func TestOnConflictDoUpdate(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]any{"email": "a", "name": "n"})
		statement.OnConflict("email").DoUpdate("name")
	})

	requireBuilderSQL(t, builder, query.Postgres, `INSERT INTO "users" ("email", "name") VALUES ($1, $2) `+
		`ON CONFLICT ("email") DO UPDATE SET "name" = EXCLUDED."name"`)
	requireBuilderSQL(t, builder, query.SQLite, `INSERT INTO "users" ("email", "name") VALUES (?, ?) `+
		`ON CONFLICT ("email") DO UPDATE SET "name" = EXCLUDED."name"`)
	requireBuilderSQL(t, builder, query.MySQL, "INSERT INTO `users` (`email`, `name`) VALUES (?, ?) "+
		"ON DUPLICATE KEY UPDATE `name` = VALUES(`name`)")
	requireUnsupported(t, builder, query.ANSI)
}

// TestOnConflictMultipleColumns verifies multi-column targets and updates.
func TestOnConflictMultipleColumns(t *testing.T) {
	t.Parallel()

	builder := query.Table("t", func(statement *query.Builder) {
		statement.OnConflict("a", "b").DoUpdate("c", "d")
		statement.Insert(map[string]int{"a": 1})
	})

	requireBuilderSQL(t, builder, query.Postgres, `INSERT INTO "t" ("a") VALUES ($1) `+
		`ON CONFLICT ("a", "b") DO UPDATE SET "c" = EXCLUDED."c", "d" = EXCLUDED."d"`)
	requireBuilderSQL(t, builder, query.MySQL, "INSERT INTO `t` (`a`) VALUES (?) "+
		"ON DUPLICATE KEY UPDATE `c` = VALUES(`c`), `d` = VALUES(`d`)")
}

// TestOnConflictDoNothing verifies DoNothing on every dialect.
func TestOnConflictDoNothing(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
		statement.OnConflict("a").DoNothing()
	})

	requireBuilderSQL(t, builder, query.Postgres,
		`INSERT INTO "users" ("a") VALUES ($1) ON CONFLICT ("a") DO NOTHING`)
	requireBuilderSQL(t, builder, query.SQLite,
		`INSERT INTO "users" ("a") VALUES (?) ON CONFLICT ("a") DO NOTHING`)
	requireBuilderSQL(t, builder, query.MySQL, "INSERT IGNORE INTO `users` (`a`) VALUES (?)")
	requireUnsupported(t, builder, query.ANSI)
}

// TestOnConflictDoNothingWithoutTarget verifies the target is optional for DoNothing.
func TestOnConflictDoNothingWithoutTarget(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
		statement.OnConflict().DoNothing()
	})

	requireBuilderSQL(t, builder, query.Postgres, `INSERT INTO "users" ("a") VALUES ($1) ON CONFLICT DO NOTHING`)
	requireBuilderSQL(t, builder, query.SQLite, `INSERT INTO "users" ("a") VALUES (?) ON CONFLICT DO NOTHING`)
}

// TestOnConflictWithReturning verifies upsert composes with RETURNING.
func TestOnConflictWithReturning(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
		statement.OnConflict("a").DoUpdate("a")
		statement.Returning("id")
	})

	requireBuilderSQL(t, builder, query.Postgres, `INSERT INTO "users" ("a") VALUES ($1) `+
		`ON CONFLICT ("a") DO UPDATE SET "a" = EXCLUDED."a" RETURNING "id"`)
}

// TestOnConflictErrors verifies invalid upserts fail.
func TestOnConflictErrors(t *testing.T) {
	t.Parallel()

	insert := query.Table("t", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
	})

	requireBuilderInvalid(t, insert.Derive(func(statement *query.Builder) {
		statement.OnConflict("a").DoUpdate()
	}))
	requireBuilderInvalid(t, insert.Derive(func(statement *query.Builder) {
		statement.OnConflict("a b").DoNothing()
	}))
	requireBuilderInvalid(t, insert.Derive(func(statement *query.Builder) {
		statement.OnConflict("a").DoUpdate("b c")
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.Delete()
		statement.OnConflict("a").DoNothing()
	}))

	_, err := insert.Derive(func(statement *query.Builder) {
		statement.OnConflict().DoUpdate("a")
	}).BuildFor(query.Postgres)

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("got %v, want ErrInvalidQuery", err)
	}

	_, err = insert.Derive(func(statement *query.Builder) {
		statement.OnConflict().DoUpdate("a")
	}).BuildFor(query.SQLite)

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("got %v, want ErrInvalidQuery", err)
	}

	if _, err = insert.Derive(func(statement *query.Builder) {
		statement.OnConflict().DoUpdate("a")
	}).BuildFor(query.MySQL); err != nil {
		t.Fatal(err)
	}
}

// TestOnConflictLeavesReceiver verifies the insert builder is not changed.
func TestOnConflictLeavesReceiver(t *testing.T) {
	t.Parallel()

	insert := query.Table("t", func(statement *query.Builder) {
		statement.Insert(map[string]int{"a": 1})
	})

	_ = insert.Derive(func(statement *query.Builder) {
		statement.OnConflict("a").DoNothing()
	})

	requireBuilderSQL(t, insert, query.Postgres, `INSERT INTO "t" ("a") VALUES ($1)`)
}

// TestIncrementDecrement verifies expression updates on every dialect.
func TestIncrementDecrement(t *testing.T) {
	t.Parallel()

	builder := query.Table("posts", func(statement *query.Builder) {
		statement.Where("id", 7)
		statement.Increment("views", 1)
		statement.Decrement("stock", 2)
	})

	requireBuilderSQL(t, builder, query.ANSI,
		`UPDATE "posts" SET "views" = "views" + ?, "stock" = "stock" - ? WHERE "id" = ?`)
	requireBuilderSQL(t, builder, query.SQLite,
		`UPDATE "posts" SET "views" = "views" + ?, "stock" = "stock" - ? WHERE "id" = ?`)
	requireBuilderSQL(t, builder, query.Postgres,
		`UPDATE "posts" SET "views" = "views" + $1, "stock" = "stock" - $2 WHERE "id" = $3`)
	requireBuilderSQL(t, builder, query.MySQL,
		"UPDATE `posts` SET `views` = `views` + ?, `stock` = `stock` - ? WHERE `id` = ?")

	statement, err := builder.Build()

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(statement.Args(), []any{1, 2, 7}) {
		t.Fatalf("unexpected args: %#v", statement.Args())
	}
}

// TestUpdateComposesWithExpressions verifies map values precede expressions
// regardless of call order.
func TestUpdateComposesWithExpressions(t *testing.T) {
	t.Parallel()

	builder := query.Table("posts", func(statement *query.Builder) {
		statement.Where("id", 7)
		statement.Increment("views", 1)
		statement.SetRaw("seen_at", "COALESCE(seen_at, ?)", "now")
		statement.Update(map[string]string{"title": "x", "slug": "y"})
	})

	requireBuilderSQL(t, builder, query.ANSI, `UPDATE "posts" SET "slug" = ?, "title" = ?, `+
		`"views" = "views" + ?, "seen_at" = COALESCE(seen_at, ?) WHERE "id" = ?`)
	requireBuilderSQL(t, builder, query.SQLite, `UPDATE "posts" SET "slug" = ?, "title" = ?, `+
		`"views" = "views" + ?, "seen_at" = COALESCE(seen_at, ?) WHERE "id" = ?`)
	requireBuilderSQL(t, builder, query.Postgres, `UPDATE "posts" SET "slug" = $1, "title" = $2, `+
		`"views" = "views" + $3, "seen_at" = COALESCE(seen_at, $4) WHERE "id" = $5`)
	requireBuilderSQL(t, builder, query.MySQL, "UPDATE `posts` SET `slug` = ?, `title` = ?, "+
		"`views` = `views` + ?, `seen_at` = COALESCE(seen_at, ?) WHERE `id` = ?")
}

// TestSetRawOnly verifies SetRaw alone is a valid update.
func TestSetRawOnly(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.SetRaw("a", "NOW()")
	}), query.MySQL,
		"UPDATE `t` SET `a` = NOW() WHERE `id` = ?")
}

// TestExpressionErrors verifies invalid expression updates fail.
func TestExpressionErrors(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Unfiltered()
		statement.Increment("a", nil)
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Unfiltered()
		statement.Increment("a b", 1)
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Unfiltered()
		statement.SetRaw("a", " ")
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Unfiltered()
		statement.SetRaw("a", "?")
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Unfiltered()
		statement.Increment("a", 1)
		statement.Decrement("a", 1)
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Unfiltered()
		statement.Increment("a", 1)
		statement.Update(map[string]int{"a": 2})
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Increment("a", 1)
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Increment("a", 1)
		statement.Where("id", 1)
		statement.Insert(map[string]int{"a": 1})
	}))
}

// TestForUpdate verifies FOR UPDATE placement and dialect support.
func TestForUpdate(t *testing.T) {
	t.Parallel()

	builder := query.Table("jobs", func(statement *query.Builder) {
		statement.Where("state", "new")
		statement.OrderBy("id")
		statement.Limit(1)
		statement.ForUpdate()
	})

	requireBuilderSQL(t, builder, query.Postgres,
		`SELECT * FROM "jobs" WHERE "state" = $1 ORDER BY "id" ASC LIMIT 1 FOR UPDATE`)
	requireBuilderSQL(t, builder, query.MySQL,
		"SELECT * FROM `jobs` WHERE `state` = ? ORDER BY `id` ASC LIMIT 1 FOR UPDATE")
	requireUnsupported(t, builder, query.SQLite)
	requireUnsupported(t, builder, query.ANSI)
}

// TestForShare verifies FOR SHARE.
func TestForShare(t *testing.T) {
	t.Parallel()

	builder := query.Table("jobs", func(statement *query.Builder) {
		statement.ForShare()
	})

	requireBuilderSQL(t, builder, query.Postgres, `SELECT * FROM "jobs" FOR SHARE`)
	requireBuilderSQL(t, builder, query.MySQL, "SELECT * FROM `jobs` FOR SHARE")
	requireUnsupported(t, builder, query.SQLite)
	requireUnsupported(t, builder, query.ANSI)
}

// TestSkipLocked verifies SKIP LOCKED.
func TestSkipLocked(t *testing.T) {
	t.Parallel()

	builder := query.Table("jobs", func(statement *query.Builder) {
		statement.ForUpdate()
		statement.SkipLocked()
	})

	requireBuilderSQL(t, builder, query.Postgres, `SELECT * FROM "jobs" FOR UPDATE SKIP LOCKED`)
	requireBuilderSQL(t, builder, query.MySQL, "SELECT * FROM `jobs` FOR UPDATE SKIP LOCKED")
	requireUnsupported(t, builder, query.SQLite)
	requireUnsupported(t, builder, query.ANSI)
}

// TestNoWait verifies NOWAIT.
func TestNoWait(t *testing.T) {
	t.Parallel()

	builder := query.Table("jobs", func(statement *query.Builder) {
		statement.ForShare()
		statement.NoWait()
	})

	requireBuilderSQL(t, builder, query.Postgres, `SELECT * FROM "jobs" FOR SHARE NOWAIT`)
	requireBuilderSQL(t, builder, query.MySQL, "SELECT * FROM `jobs` FOR SHARE NOWAIT")
	requireUnsupported(t, builder, query.SQLite)
	requireUnsupported(t, builder, query.ANSI)
}

// TestLockErrors verifies misuse of lock options fails.
func TestLockErrors(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.SkipLocked()
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.NoWait()
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.ForUpdate()
		statement.SkipLocked()
		statement.NoWait()
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.ForUpdate()
		statement.Delete()
	}))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.ForShare()
		statement.Update(map[string]int{"a": 1})
		statement.Unfiltered()
	}))
}
