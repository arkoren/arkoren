package query_test

import (
	"reflect"
	"testing"

	"arkoren.dev/foundation/query"
)

// rawScopedUsers combines filters around a raw disjunction with two mandatory
// scopes. Arguments are distinct so compilation also detects reordered binds.
func rawScopedUsers() query.Query {
	return query.Table("users", func(statement *query.Builder) {
		statement.Where("enabled", true)
		statement.WhereRaw("lower(email) = ? OR lower(backup_email) = ?", "first", "second")
		statement.Where("verified", false)
		statement.Scope(func(predicate *query.PredicateBuilder) {
			predicate.Where("tenant_id", 7)
		}, func(predicate *query.PredicateBuilder) {
			predicate.WhereRaw("region = ? OR region = ?", "east", "west")
		})
	})
}

// requireScopedStatement checks both SQL grouping and binding order, including
// assignment arguments that must precede filters in an UPDATE.
func requireScopedStatement(t *testing.T, builder query.Query, dialect query.Dialect, sql string, args ...any) {
	t.Helper()

	statement, err := builder.BuildFor(dialect)

	if err != nil {
		t.Fatal(err)
	}

	if statement.SQL() != sql {
		t.Fatalf("SQL = %s\nwant %s", statement.SQL(), sql)
	}

	if !reflect.DeepEqual(statement.Args(), args) {
		t.Fatalf("args = %#v, want %#v", statement.Args(), args)
	}
}

// TestRawScopeSelect groups the complete user chain on every dialect, not
// just raw predicates, so neither branch can escape either scope.
func TestRawScopeSelect(t *testing.T) {
	t.Parallel()

	builder := rawScopedUsers()
	const ansi = `SELECT * FROM "users" WHERE ("enabled" = ? AND lower(email) = ? OR lower(backup_email) = ? AND "verified" = ?) AND ("tenant_id" = ?) AND (region = ? OR region = ?)`

	requireScopedStatement(t, builder, query.ANSI, ansi, true, "first", "second", false, 7, "east", "west")
	requireScopedStatement(t, builder, query.SQLite, ansi, true, "first", "second", false, 7, "east", "west")
	requireScopedStatement(t, builder, query.Postgres,
		`SELECT * FROM "users" WHERE ("enabled" = $1 AND lower(email) = $2 OR lower(backup_email) = $3 AND "verified" = $4) AND ("tenant_id" = $5) AND (region = $6 OR region = $7)`,
		true, "first", "second", false, 7, "east", "west")
	requireScopedStatement(t, builder, query.MySQL,
		"SELECT * FROM `users` WHERE (`enabled` = ? AND lower(email) = ? OR lower(backup_email) = ? AND `verified` = ?) AND (`tenant_id` = ?) AND (region = ? OR region = ?)",
		true, "first", "second", false, 7, "east", "west")
}

// TestRawScopeUpdate preserves assignment-first binds and isolates both raw
// branches from the tenant and region requirements on every dialect.
func TestRawScopeUpdate(t *testing.T) {
	t.Parallel()

	builder := rawScopedUsers().Derive(func(statement *query.Builder) {
		statement.Update(map[string]any{"name": "changed"})
	})
	const ansi = `UPDATE "users" SET "name" = ? WHERE ("enabled" = ? AND lower(email) = ? OR lower(backup_email) = ? AND "verified" = ?) AND ("tenant_id" = ?) AND (region = ? OR region = ?)`

	requireScopedStatement(t, builder, query.ANSI, ansi, "changed", true, "first", "second", false, 7, "east", "west")
	requireScopedStatement(t, builder, query.SQLite, ansi, "changed", true, "first", "second", false, 7, "east", "west")
	requireScopedStatement(t, builder, query.Postgres,
		`UPDATE "users" SET "name" = $1 WHERE ("enabled" = $2 AND lower(email) = $3 OR lower(backup_email) = $4 AND "verified" = $5) AND ("tenant_id" = $6) AND (region = $7 OR region = $8)`,
		"changed", true, "first", "second", false, 7, "east", "west")
	requireScopedStatement(t, builder, query.MySQL,
		"UPDATE `users` SET `name` = ? WHERE (`enabled` = ? AND lower(email) = ? OR lower(backup_email) = ? AND `verified` = ?) AND (`tenant_id` = ?) AND (region = ? OR region = ?)",
		"changed", true, "first", "second", false, 7, "east", "west")
}

// TestRawScopeDelete ensures destructive statements use the same complete
// grouping as reads on all supported dialects.
func TestRawScopeDelete(t *testing.T) {
	t.Parallel()

	builder := rawScopedUsers().Derive(func(statement *query.Builder) {
		statement.Delete()
	})
	const ansi = `DELETE FROM "users" WHERE ("enabled" = ? AND lower(email) = ? OR lower(backup_email) = ? AND "verified" = ?) AND ("tenant_id" = ?) AND (region = ? OR region = ?)`

	requireScopedStatement(t, builder, query.ANSI, ansi, true, "first", "second", false, 7, "east", "west")
	requireScopedStatement(t, builder, query.SQLite, ansi, true, "first", "second", false, 7, "east", "west")
	requireScopedStatement(t, builder, query.Postgres,
		`DELETE FROM "users" WHERE ("enabled" = $1 AND lower(email) = $2 OR lower(backup_email) = $3 AND "verified" = $4) AND ("tenant_id" = $5) AND (region = $6 OR region = $7)`,
		true, "first", "second", false, 7, "east", "west")
	requireScopedStatement(t, builder, query.MySQL,
		"DELETE FROM `users` WHERE (`enabled` = ? AND lower(email) = ? OR lower(backup_email) = ? AND `verified` = ?) AND (`tenant_id` = ?) AND (region = ? OR region = ?)",
		true, "first", "second", false, 7, "east", "west")
}

// TestRawScopeSinglePredicateAndCount covers the minimal bypass and verifies
// Count retains grouping without changing a reusable unscoped builder.
func TestRawScopeSinglePredicateAndCount(t *testing.T) {
	t.Parallel()

	base := query.Table("users", func(statement *query.Builder) {
		statement.WhereRaw("lower(email) = ? OR lower(backup_email) = ?", "a", "b")
	})
	scoped := base.Derive(func(statement *query.Builder) {
		statement.Scope(active)
	})

	requireScopedStatement(t, scoped, query.ANSI,
		`SELECT * FROM "users" WHERE (lower(email) = ? OR lower(backup_email) = ?) AND ("active" = ?)`, "a", "b", true)
	requireScopedStatement(t, scoped.Count(), query.ANSI,
		`SELECT COUNT(*) FROM "users" WHERE (lower(email) = ? OR lower(backup_email) = ?) AND ("active" = ?)`, "a", "b", true)
	requireScopedStatement(t, scoped.Derive(func(statement *query.Builder) {
		statement.Select("role")
		statement.GroupBy("role")
	}).Count(), query.ANSI,
		"SELECT COUNT(*) FROM (\n"+
			`SELECT "role" FROM "users" WHERE (lower(email) = ? OR lower(backup_email) = ?) AND ("active" = ?) GROUP BY "role"`+
			"\n"+`) AS "count"`, "a", "b", true)
	requireScopedStatement(t, base.Derive(func(statement *query.Builder) {
		statement.Scope(func(predicate *query.PredicateBuilder) {
		})
	}), query.ANSI,
		`SELECT * FROM "users" WHERE lower(email) = ? OR lower(backup_email) = ?`, "a", "b")
	requireScopedStatement(t, base, query.ANSI,
		`SELECT * FROM "users" WHERE lower(email) = ? OR lower(backup_email) = ?`, "a", "b")
}

// TestRawScopeNestedGroup keeps raw disjunctions contained when a scoped
// subquery is reused inside EXISTS; compiling it must not mutate its filters.
func TestRawScopeNestedGroup(t *testing.T) {
	t.Parallel()

	sub := query.Table("users", func(statement *query.Builder) {
		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
			predicate.WhereRaw("email = ? OR backup_email = ?", "a", "b")
		})
		statement.Scope(active)
	})

	requireScopedStatement(t, query.Table("posts", func(statement *query.Builder) {
		statement.WhereExists(sub)
	}), query.Postgres,
		`SELECT * FROM "posts" WHERE EXISTS (SELECT * FROM "users" WHERE ((email = $1 OR backup_email = $2)) AND ("active" = $3))`, "a", "b", true)
	requireScopedStatement(t, sub, query.ANSI,
		`SELECT * FROM "users" WHERE ((email = ? OR backup_email = ?)) AND ("active" = ?)`, "a", "b", true)
}

// active is a scope restricting rows to active ones.
func active(predicate *query.PredicateBuilder) {
	predicate.Where("active", true)
}

// TestScopeDoesNotLeakOrIntoSelect verifies user OR predicates are wrapped.
func TestScopeDoesNotLeakOrIntoSelect(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.OrWhere("b", 2)
		statement.Scope(active)
	})

	requireBuilderSQL(t, builder, query.ANSI, `SELECT * FROM "users" WHERE ("a" = ? OR "b" = ?) AND ("active" = ?)`)
}

// TestScopeDoesNotLeakOrIntoUpdate verifies UPDATE wraps user OR predicates.
func TestScopeDoesNotLeakOrIntoUpdate(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.OrWhere("b", 2)
		statement.Scope(active)
		statement.Update(map[string]any{"x": 1})
	})

	requireBuilderSQL(t, builder, query.ANSI, `UPDATE "users" SET "x" = ? WHERE ("a" = ? OR "b" = ?) AND ("active" = ?)`)
}

// TestScopeDoesNotLeakOrIntoDelete verifies DELETE wraps user OR predicates.
func TestScopeDoesNotLeakOrIntoDelete(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.OrWhere("b", 2)
		statement.Scope(active)
		statement.Delete()
	})

	requireBuilderSQL(t, builder, query.ANSI, `DELETE FROM "users" WHERE ("a" = ? OR "b" = ?) AND ("active" = ?)`)
}

// TestScopeDoesNotLeakOrIntoCount verifies plain Count keeps the scope once.
func TestScopeDoesNotLeakOrIntoCount(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.OrWhere("b", 2)
		statement.Scope(active)
	}).Count()

	requireBuilderSQL(t, builder, query.ANSI, `SELECT COUNT(*) FROM "users" WHERE ("a" = ? OR "b" = ?) AND ("active" = ?)`)
}

// TestScopeAppliesOnceToDerivedCount verifies a wrapped Count does not repeat the scope.
func TestScopeAppliesOnceToDerivedCount(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Select("role")
		statement.Where("a", 1)
		statement.OrWhere("b", 2)
		statement.Scope(active)
		statement.GroupBy("role")
	}).Count()

	requireBuilderSQL(t, builder, query.ANSI, "SELECT COUNT(*) FROM (\n"+
		`SELECT "role" FROM "users" WHERE ("a" = ? OR "b" = ?) AND ("active" = ?) GROUP BY "role"`+
		"\n"+`) AS "count"`)
}

// TestSeekDoesNotLeakOr verifies Seek wraps user OR predicates.
func TestSeekDoesNotLeakOr(t *testing.T) {
	t.Parallel()

	builder := query.Table("posts", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.OrWhere("b", 2)
		statement.OrderBy("id")
	}).Seek([]any{5}, false)

	requireBuilderSQL(t, builder, query.ANSI,
		`SELECT * FROM "posts" WHERE ("a" = ? OR "b" = ?) AND ("id" > ?) ORDER BY "id" ASC`)
}

// TestScopeSurvivesLaterOrWhere verifies OrWhere after Scope stays inside the user group.
func TestScopeSurvivesLaterOrWhere(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.Scope(active)
		statement.OrWhere("b", 2)
	})

	requireBuilderSQL(t, builder, query.ANSI, `SELECT * FROM "users" WHERE ("a" = ? OR "b" = ?) AND ("active" = ?)`)
}

// TestScopeGroupsPlainPredicates verifies even AND-only user predicates are
// grouped, so scope isolation does not depend on inspecting predicate syntax.
func TestScopeGroupsPlainPredicates(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.Scope(active)
	})

	requireBuilderSQL(t, builder, query.ANSI, `SELECT * FROM "users" WHERE ("a" = ?) AND ("active" = ?)`)
}

// TestScopeAppliesMultipleScopesAsGroups verifies each scope is its own group.
func TestScopeAppliesMultipleScopesAsGroups(t *testing.T) {
	t.Parallel()

	tenant := func(predicate *query.PredicateBuilder) {
		predicate.Where("tenant", 3).OrWhere("shared", true)
	}
	builder := query.Table("users", func(statement *query.Builder) {
		statement.Scope(active, tenant)
	})

	requireBuilderSQL(t, builder, query.ANSI, `SELECT * FROM "users" WHERE ("active" = ?) AND ("tenant" = ? OR "shared" = ?)`)
}

// TestScopeIgnoresEmptyScopes verifies an empty protected declaration is a no-op.
func TestScopeIgnoresEmptyScopes(t *testing.T) {
	t.Parallel()

	empty := func(predicate *query.PredicateBuilder) {
	}
	builder := query.Table("users", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.OrWhere("b", 2)
		statement.Scope(empty)
	})

	requireBuilderSQL(t, builder, query.ANSI, `SELECT * FROM "users" WHERE "a" = ? OR "b" = ?`)
}

// TestScopeSuppliedNilPanics verifies callback misuse is not silently ignored.
func TestScopeSuppliedNilPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected supplied nil scope to panic")
		}
	}()

	query.Table("users", func(statement *query.Builder) {
		statement.Scope(nil)
	})
}

// TestScopePropagatesError verifies a scope's error fails the build.
func TestScopePropagatesError(t *testing.T) {
	t.Parallel()

	broken := func(predicate *query.PredicateBuilder) {
		predicate.Where("", 1)
	}

	requireBuilderInvalid(t, query.Table("users", func(statement *query.Builder) {
		statement.Scope(broken)
	}))
}

// TestScopeRejectedOnInsert verifies INSERT cannot carry scopes.
func TestScopeRejectedOnInsert(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("users", func(statement *query.Builder) {
		statement.Scope(active)
		statement.Insert(map[string]any{"a": 1})
	}))
}

// TestScopeSatisfiesUnfilteredGuard verifies a scope counts as a filter.
func TestScopeSatisfiesUnfilteredGuard(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("users", func(statement *query.Builder) {
		statement.Scope(active)
		statement.Delete()
	}), query.ANSI, `DELETE FROM "users" WHERE ("active" = ?)`)
}

// TestScopeKeepsBuilderImmutable verifies Scope does not modify its receiver.
func TestScopeKeepsBuilderImmutable(t *testing.T) {
	t.Parallel()

	base := query.Table("users", func(statement *query.Builder) {
		statement.Where("a", 1)
	})

	_ = base.Derive(func(statement *query.Builder) {
		statement.Scope(active)
	})

	requireBuilderSQL(t, base, query.ANSI, `SELECT * FROM "users" WHERE "a" = ?`)
}

// members returns a correlated membership subquery.
func members() query.Query {
	return query.Table("members", func(statement *query.Builder) {
		statement.WhereColumn("members.group_id", "posts.group_id")
		statement.Where("members.user_id", 7)
	})
}

// TestWhereExistsCompilesSubquery verifies the EXISTS variants.
func TestWhereExistsCompilesSubquery(t *testing.T) {
	t.Parallel()

	const inner = `SELECT * FROM "members" WHERE "members"."group_id" = "posts"."group_id" AND "members"."user_id" = ?`
	base := query.Table("posts", func(statement *query.Builder) {
		statement.Where("public", true)
	})

	requireBuilderSQL(t, base.Derive(func(statement *query.Builder) {
		statement.WhereExists(members())
	}), query.ANSI, `SELECT * FROM "posts" WHERE "public" = ? AND EXISTS (`+inner+`)`)
	requireBuilderSQL(t, base.Derive(func(statement *query.Builder) {
		statement.WhereNotExists(members())
	}), query.ANSI, `SELECT * FROM "posts" WHERE "public" = ? AND NOT EXISTS (`+inner+`)`)
	requireBuilderSQL(t, base.Derive(func(statement *query.Builder) {
		statement.OrWhereExists(members())
	}), query.ANSI, `SELECT * FROM "posts" WHERE "public" = ? OR EXISTS (`+inner+`)`)
	requireBuilderSQL(t, base.Derive(func(statement *query.Builder) {
		statement.OrWhereNotExists(members())
	}), query.ANSI, `SELECT * FROM "posts" WHERE "public" = ? OR NOT EXISTS (`+inner+`)`)
}

// TestWhereExistsNumbersPostgresPlaceholders verifies $n continues across the subquery.
func TestWhereExistsNumbersPostgresPlaceholders(t *testing.T) {
	t.Parallel()

	builder := query.Table("posts", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.WhereExists(members())
		statement.Where("b", 2)
	})

	statement, err := builder.BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	want := `SELECT * FROM "posts" WHERE "a" = $1 AND EXISTS (SELECT * FROM "members" WHERE ` +
		`"members"."group_id" = "posts"."group_id" AND "members"."user_id" = $2) AND "b" = $3`

	if statement.SQL() != want {
		t.Fatalf("got %s\nwant %s", statement.SQL(), want)
	}

	if !reflect.DeepEqual(statement.Args(), []any{1, 7, 2}) {
		t.Fatalf("args = %v", statement.Args())
	}
}

// TestWhereExistsPropagatesSubqueryError verifies a broken subquery fails the build.
func TestWhereExistsPropagatesSubqueryError(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("posts", func(statement *query.Builder) {
		statement.WhereExists(query.Table("members", func(statement *query.Builder) {
			statement.Where("", 1)
		}))
	}))
}

// TestWhereExistsRejectsNonSelect verifies the subquery must be a SELECT.
func TestWhereExistsRejectsNonSelect(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("posts", func(statement *query.Builder) {
		statement.WhereExists(query.Table("members", func(statement *query.Builder) {
			statement.Where("a", 1)
			statement.Delete()
		}))
	}))
}

// TestWhereExistsAppliesSubqueryScopes verifies scopes inside the subquery compile.
func TestWhereExistsAppliesSubqueryScopes(t *testing.T) {
	t.Parallel()

	sub := query.Table("members", func(statement *query.Builder) {
		statement.Where("a", 1)
		statement.OrWhere("b", 2)
		statement.Scope(active)
	})

	requireBuilderSQL(t, query.Table("posts", func(statement *query.Builder) {
		statement.WhereExists(sub)
	}), query.ANSI,
		`SELECT * FROM "posts" WHERE EXISTS (SELECT * FROM "members" WHERE ("a" = ? OR "b" = ?) AND ("active" = ?))`)
}
