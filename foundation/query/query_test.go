package query_test

import (
	"errors"
	"reflect"
	"testing"

	"arkoren.dev/foundation/query"
)

// BenchmarkQueryShortStatic measures construction plus dialect compilation.
func BenchmarkQueryShortStatic(benchmark *testing.B) {
	benchmark.ReportAllocs()

	for benchmark.Loop() {
		_, err := query.Table("users", func(statement *query.Builder) {
			statement.Select("id", "name")
			statement.Where("active", true)
			statement.Limit(20)
		}).BuildFor(query.Postgres)

		if err != nil {
			benchmark.Fatal(err)
		}
	}
}

// BenchmarkQueryLongConditional measures append-heavy declarations with branches.
func BenchmarkQueryLongConditional(benchmark *testing.B) {
	benchmark.ReportAllocs()

	for benchmark.Loop() {
		_, err := query.Table("users", func(statement *query.Builder) {
			statement.Select("id", "name", "email", "created_at")

			for index := range 20 {
				if index%2 == 0 {
					statement.Where("score", ">", index)
				}
			}

			statement.OrderBy("created_at")
			statement.OrderBy("id")
			statement.Limit(50)
		}).BuildFor(query.Postgres)

		if err != nil {
			benchmark.Fatal(err)
		}
	}
}

// BenchmarkQueryDerive measures publication from an already-published base.
func BenchmarkQueryDerive(benchmark *testing.B) {
	base := query.Table("users", func(statement *query.Builder) {
		statement.Where("tenant_id", 7)
		statement.WhereNull("deleted_at")
	})

	benchmark.ReportAllocs()

	for benchmark.Loop() {
		_, err := base.Derive(func(statement *query.Builder) {
			statement.Select("id", "name")
			statement.Where("active", true)
		}).BuildFor(query.Postgres)

		if err != nil {
			benchmark.Fatal(err)
		}
	}
}

// BenchmarkQueryGroups measures restricted nested predicate publication.
func BenchmarkQueryGroups(benchmark *testing.B) {
	benchmark.ReportAllocs()

	for benchmark.Loop() {
		_, err := query.Table("users", func(statement *query.Builder) {
			statement.WhereGroup(func(predicate *query.PredicateBuilder) {
				predicate.Where("role", "admin")
				predicate.OrWhere("role", "editor")
			})
			statement.Where("active", true)
		}).BuildFor(query.Postgres)

		if err != nil {
			benchmark.Fatal(err)
		}
	}
}

// BenchmarkQuerySubqueries measures published EXISTS compilation and binding.
func BenchmarkQuerySubqueries(benchmark *testing.B) {
	benchmark.ReportAllocs()

	for benchmark.Loop() {
		members := query.Table("members", func(statement *query.Builder) {
			statement.WhereColumn("members.user_id", "users.id")
			statement.Where("tenant_id", 7)
		})

		_, err := query.Table("users", func(statement *query.Builder) {
			statement.WhereExists(members)
			statement.Where("active", true)
		}).BuildFor(query.Postgres)

		if err != nil {
			benchmark.Fatal(err)
		}
	}
}

// TestBuilderUsesValueSemantics verifies that derived builders do not change
// their shared base builder.
func TestBuilderUsesValueSemantics(t *testing.T) {
	t.Parallel()

	base := query.Table("users", func(statement *query.Builder) {
		statement.Where("enabled", true)
	})
	admins := base.Derive(func(statement *query.Builder) {
		statement.Where("role", "admin")
	})
	editors := base.Derive(func(statement *query.Builder) {
		statement.Where("role", "editor")
	})

	adminStatement, err := admins.Build()

	if err != nil {
		t.Fatal(err)
	}

	editorStatement, err := editors.Build()

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(adminStatement.Args(), []any{true, "admin"}) {
		t.Fatalf("unexpected admin arguments: %#v", adminStatement.Args())
	}

	if !reflect.DeepEqual(editorStatement.Args(), []any{true, "editor"}) {
		t.Fatalf("unexpected editor arguments: %#v", editorStatement.Args())
	}
}

// TestTableOmittedCallbacksUseDefaults checks the default published SELECT.
func TestTableOmittedCallbacksUseDefaults(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("users"), query.ANSI, `SELECT * FROM "users"`)
}

// TestTableInvalidNamePrecedesCallbacks preserves required-input validation timing.
func TestTableInvalidNamePrecedesCallbacks(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("invalid table", func(statement *query.Builder) {
		t.Fatal("invalid table name must be rejected before callback invocation")
	}))

	invalid := query.FromRaw(query.Raw(" "), func(statement *query.Builder) {
		t.Fatal("empty raw source must be rejected before callback invocation")
	})

	requireBuilderInvalid(t, invalid)

	if invalid.Inspect().Raw {
		t.Fatal("invalid required source must not be installed")
	}
}

// TestTableEmptyCallbackMatchesOmission checks declarations may add no settings.
func TestTableEmptyCallbackMatchesOmission(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("users", func(statement *query.Builder) {
	}), query.ANSI, `SELECT * FROM "users"`)
}

// TestQueryNilAndEmptyCallbackSlices distinguishes omitted callbacks from nil entries.
func TestQueryNilAndEmptyCallbackSlices(t *testing.T) {
	t.Parallel()

	var omitted []func(*query.Builder)
	empty := []func(*query.Builder){}
	base := query.Table("users", omitted...)

	requireBuilderSQL(t, base.Derive(empty...), query.ANSI, `SELECT * FROM "users"`)
	requireBuilderSQL(t, query.FromRaw(query.Raw("SELECT 1"), omitted...).Derive(empty...),
		query.ANSI, "SELECT * FROM (\nSELECT 1\n) AS \"t\"")
}

// TestQueryCallbacksShareOneDraft checks order, once-only invocation and replacement.
func TestQueryCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	var retained *query.Builder
	calls := 0
	users := query.Table("users", func(statement *query.Builder) {
		retained = statement
		calls++
		statement.Select("old")
		statement.Where("active", true)
	}, func(statement *query.Builder) {
		if retained != statement || calls != 1 {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		statement.Select("id")
		statement.AddSelect("name")
	})

	requireBuilderSQL(t, users, query.ANSI, `SELECT "id", "name" FROM "users" WHERE "active" = ?`)

	if calls != 2 {
		t.Fatalf("callbacks called %d times", calls)
	}
}

// TestQueryDeriveCallbacksShareOneDraft checks ordered same-draft edits on a copy.
func TestQueryDeriveCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	base := query.Table("users")
	var retained *query.Builder
	calls := 0
	users := base.Derive(func(statement *query.Builder) {
		retained = statement
		calls++
		statement.Select("old")
	}, func(statement *query.Builder) {
		if statement != retained || calls != 1 {
			t.Fatal("derivation callbacks were not ordered on one draft")
		}

		calls++
		statement.Select("id")
	})

	retained.Select("password")

	requireBuilderSQL(t, users, query.ANSI, `SELECT "id" FROM "users"`)
	requireBuilderSQL(t, base, query.ANSI, `SELECT * FROM "users"`)

	if calls != 2 {
		t.Fatalf("derivation callbacks called %d times", calls)
	}
}

// TestFromRawCallbacksShareOneDraft checks source construction uses the same
// callback contract without precompiling or invoking callbacks during Build.
func TestFromRawCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	var retained *query.Builder
	calls := 0
	users := query.FromRaw(query.Raw("SELECT id FROM users"), func(statement *query.Builder) {
		retained = statement
		calls++
		statement.As("members")
	}, func(statement *query.Builder) {
		if retained != statement || calls != 1 {
			t.Fatal("raw source callbacks were not ordered on one draft")
		}

		calls++
		statement.Select("id")
	})

	retained.From("passwords")

	requireBuilderSQL(t, users, query.ANSI, "SELECT \"id\" FROM (\nSELECT id FROM users\n) AS \"members\"")

	if calls != 2 {
		t.Fatalf("raw source callbacks called %d times", calls)
	}
}

// TestQueryDeriveSuppliedNilCallbackPanics checks inherited errors do not filter
// nil callback entries out of an explicitly requested derivation.
func TestQueryDeriveSuppliedNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected supplied nil derivation callback to panic")
		}
	}()

	query.Table("invalid table").Derive(nil)
}

// TestFromRawSuppliedNilCallbackPanics checks valid raw sources invoke nil entries.
func TestFromRawSuppliedNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected supplied nil raw-source callback to panic")
		}
	}()

	query.FromRaw(query.Raw("SELECT 1"), nil)
}

// TestQuerySuppliedNilCallbackPanics checks direct invocation of callback entries.
func TestQuerySuppliedNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected supplied nil callback to panic")
		}
	}()

	query.Table("users", nil)
}

// TestQueryCallbackPanicPropagates checks application panics are not recovered.
func TestQueryCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() != "application panic" {
			t.Fatal("callback panic changed")
		}
	}()

	query.Table("users").Derive(func(statement *query.Builder) {
		panic("application panic")
	})
}

// TestQueryBuilderReturnsReceiver checks ignored results and short chains mutate.
func TestQueryBuilderReturnsReceiver(t *testing.T) {
	t.Parallel()

	users := query.Table("users", func(statement *query.Builder) {
		if statement.Select("id") != statement || statement.Where("active", true) != statement {
			t.Fatal("setting returned a different draft")
		}

		statement.OrderBy("id").Limit(5)
	})

	requireBuilderSQL(t, users, query.Postgres,
		`SELECT "id" FROM "users" WHERE "active" = $1 ORDER BY "id" ASC LIMIT 5`)
}

// TestQueryDeriveWithoutCallbacksPreservesBase checks no-op derivation and scopes.
func TestQueryDeriveWithoutCallbacksPreservesBase(t *testing.T) {
	t.Parallel()

	base := rawScopedUsers()

	before, err := base.BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	after, err := base.Derive().BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if before.SQL() != after.SQL() || !reflect.DeepEqual(before.Args(), after.Args()) {
		t.Fatal("empty derivation changed the base")
	}
}

// TestQueryDeriveKeepsInheritedError checks later valid settings cannot repair it.
func TestQueryDeriveKeepsInheritedError(t *testing.T) {
	t.Parallel()

	base := query.Table("invalid table")
	_, before := base.Build()
	_, after := base.Derive(func(statement *query.Builder) {
		statement.From("users")
	}).Build()

	if !errors.Is(after, query.ErrInvalidQuery) || before.Error() != after.Error() {
		t.Fatalf("inherited error changed: %v -> %v", before, after)
	}
}

// TestQueryDeriveCannotResetInheritedError checks whole-draft assignment cannot
// erase a failure that predates the derivation's callbacks.
func TestQueryDeriveCannotResetInheritedError(t *testing.T) {
	t.Parallel()

	base := query.Table("invalid table")
	_, before := base.Build()
	_, after := base.Derive(func(statement *query.Builder) {
		*statement = query.Builder{}
		statement.From("users")
	}).Build()

	if !errors.Is(after, query.ErrInvalidQuery) || before.Error() != after.Error() {
		t.Fatalf("reset erased inherited error: %v -> %v", before, after)
	}
}

// TestQueryDerivationsAreIndependent checks branches cannot change their base.
func TestQueryDerivationsAreIndependent(t *testing.T) {
	t.Parallel()

	base := query.Table("users", func(statement *query.Builder) {
		statement.Where("active", true)
	})
	admins := base.Derive(func(statement *query.Builder) {
		statement.Where("role", "admin")
	})
	editors := base.Derive(func(statement *query.Builder) {
		statement.OrWhere("role", "editor")
	})

	requireScopedStatement(t, base, query.ANSI, `SELECT * FROM "users" WHERE "active" = ?`, true)
	requireScopedStatement(t, admins, query.ANSI,
		`SELECT * FROM "users" WHERE "active" = ? AND "role" = ?`, true, "admin")
	requireScopedStatement(t, editors, query.ANSI,
		`SELECT * FROM "users" WHERE "active" = ? OR "role" = ?`, true, "editor")
}

// TestRetainedQueryBuilderCannotChangePublishedQuery checks owned input slices,
// root settings, and retained nested predicates detach before compilation.
func TestRetainedQueryBuilderCannotChangePublishedQuery(t *testing.T) {
	t.Parallel()

	var retained *query.Builder
	var group *query.PredicateBuilder
	var scope *query.PredicateBuilder
	columns := []string{"id"}
	values := []int{1, 2}
	users := query.Table("users", func(statement *query.Builder) {
		retained = statement
		statement.Select(columns...)
		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
			group = predicate
			predicate.WhereIn("id", values...)
		})
		statement.Scope(func(predicate *query.PredicateBuilder) {
			scope = predicate
			predicate.Where("tenant", 7)
		})
	})

	columns[0] = "password"
	values[0] = 9
	retained.Select("password").Limit(1)
	group.OrWhere("public", true)
	scope.OrWhere("public", true)

	requireScopedStatement(t, users, query.ANSI,
		`SELECT "id" FROM "users" WHERE (("id" IN (?, ?))) AND ("tenant" = ?)`, 1, 2, 7)
}

// TestRetainedConflictCannotChangePublishedQuery checks child completion cannot
// rewrite an INSERT after publication, including its supplied map and columns.
func TestRetainedConflictCannotChangePublishedQuery(t *testing.T) {
	t.Parallel()

	var retained *query.Conflict
	row := map[string]string{"email": "ada@example.test", "name": "Ada"}
	columns := []string{"email"}
	users := query.Table("users", func(statement *query.Builder) {
		statement.Insert(row)
		retained = statement.OnConflict(columns...)
		retained.DoUpdate("name")
	})

	row["name"] = "Changed"
	columns[0] = "name"
	retained.DoNothing()

	requireScopedStatement(t, users, query.Postgres,
		`INSERT INTO "users" ("email", "name") VALUES ($1, $2) ON CONFLICT ("email") DO UPDATE SET "name" = EXCLUDED."name"`,
		"ada@example.test", "Ada")
}

// TestConflictCompletesParentBuilder checks both child actions return their parent.
func TestConflictCompletesParentBuilder(t *testing.T) {
	t.Parallel()

	query.Table("users", func(statement *query.Builder) {
		if statement.OnConflict("email").DoUpdate("name") != statement {
			t.Fatal("DoUpdate did not return parent")
		}

		if statement.OnConflict().DoNothing() != statement {
			t.Fatal("DoNothing did not return parent")
		}
	})
}

// TestMutableQueryDeclarationsCannotExecute checks every draft's public surface.
// Reflection also catches accidental compilation methods with other signatures.
func TestMutableQueryDeclarationsCannotExecute(t *testing.T) {
	t.Parallel()

	for _, draft := range []any{&query.Builder{}, &query.PredicateBuilder{}, &query.Conflict{}} {
		requireNonExecutableDraft(t, draft)
	}
}

// requireNonExecutableDraft verifies the publication boundary for one draft
// role, including compilation-like methods with signatures unlike Statementer.
func requireNonExecutableDraft(t *testing.T, draft any) {
	t.Helper()

	if _, ok := draft.(query.Statementer); ok {
		t.Fatalf("%T implements Statementer", draft)
	}

	for _, name := range []string{"Build", "BuildFor", "Inspect", "IsSelect", "Count", "Seek"} {
		if _, ok := reflect.TypeOf(draft).MethodByName(name); ok {
			t.Fatalf("%T exposes %s", draft, name)
		}
	}
}

// TestPredicateGroupsShareDraftUntilFinalValidation checks an empty first callback
// is valid when another callback fills the same group, including nested groups.
func TestPredicateGroupsShareDraftUntilFinalValidation(t *testing.T) {
	t.Parallel()

	var retained *query.PredicateBuilder
	users := query.Table("users", func(statement *query.Builder) {
		statement.WhereNotGroup(func(predicate *query.PredicateBuilder) {
			retained = predicate
		}, func(predicate *query.PredicateBuilder) {
			if predicate != retained {
				t.Fatal("group callbacks received different drafts")
			}

			if predicate.OrWhereNotGroup(func(predicate *query.PredicateBuilder) {
				predicate.Where("active", true)
			}) != predicate {
				t.Fatal("nested group returned another receiver")
			}
		})
	})

	requireBuilderSQL(t, users, query.ANSI, `SELECT * FROM "users" WHERE NOT (NOT ("active" = ?))`)
}

// TestPredicateGroupOmittedCallbacksRemainInvalid checks final empty-group validation.
func TestPredicateGroupOmittedCallbacksRemainInvalid(t *testing.T) {
	t.Parallel()

	var omitted []func(*query.PredicateBuilder)

	requireBuilderInvalid(t, query.Table("users", func(statement *query.Builder) {
		statement.WhereGroup(omitted...)
	}))
}

// TestPredicateGroupExpandedEmptyCallbacksRemainInvalid checks explicit empty
// callback slices do not manufacture a predicate or bypass validation.
func TestPredicateGroupExpandedEmptyCallbacksRemainInvalid(t *testing.T) {
	t.Parallel()

	empty := []func(*query.PredicateBuilder){}

	requireBuilderInvalid(t, query.Table("users", func(statement *query.Builder) {
		statement.OrWhereNotGroup(empty...)
	}))
}

// TestScopesUseIndependentDrafts checks Scope's list is not a shared pipeline.
func TestScopesUseIndependentDrafts(t *testing.T) {
	t.Parallel()

	var first *query.PredicateBuilder
	users := query.Table("users", func(statement *query.Builder) {
		statement.Scope(func(predicate *query.PredicateBuilder) {
			first = predicate
			predicate.Where("tenant", 7)
		}, func(predicate *query.PredicateBuilder) {
			if first == predicate {
				t.Fatal("independent scopes received the same draft")
			}

			predicate.Where("active", true)
			predicate.OrWhere("role", "admin")
		})
	})

	requireScopedStatement(t, users, query.ANSI,
		`SELECT * FROM "users" WHERE ("tenant" = ?) AND ("active" = ? OR "role" = ?)`, 7, true, "admin")
}

// TestScopeOmittedAndEmptySlicesAddNothing checks both callback cardinalities
// preserve ordinary user predicates and the missing-filter mutation guard.
func TestScopeOmittedAndEmptySlicesAddNothing(t *testing.T) {
	t.Parallel()

	var omitted []query.Scope
	empty := []query.Scope{}
	users := query.Table("users", func(statement *query.Builder) {
		statement.Scope()
		statement.Scope(omitted...)
		statement.Scope(empty...)
	})

	requireBuilderSQL(t, users, query.ANSI, `SELECT * FROM "users"`)
	requireBuilderInvalid(t, users.Derive(func(statement *query.Builder) {
		statement.Delete()
	}))
}

// TestPredicateGroupSuppliedNilPanics checks groups invoke supplied nil directly.
func TestPredicateGroupSuppliedNilPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected nil group callback panic")
		}
	}()

	query.Table("users", func(statement *query.Builder) {
		statement.OrWhereGroup(nil)
	})
}

// TestPredicateDraftHasOnlyFilterMethods checks restricted declarations cannot
// mutate statement shape or hide SQL execution behind an accidental new method.
func TestPredicateDraftHasOnlyFilterMethods(t *testing.T) {
	t.Parallel()

	draft := reflect.TypeFor[*query.PredicateBuilder]()

	for _, name := range []string{
		"From", "As", "Select", "AddSelect", "SelectRaw", "Distinct", "Scope",
		"Join", "LeftJoin", "RightJoin", "CrossJoin", "GroupBy", "Having", "HavingRaw",
		"OrderBy", "Limit", "Offset", "Insert", "InsertRows", "Update", "Delete",
		"Increment", "SetRaw", "Returning", "Unfiltered", "OnConflict", "ForUpdate",
	} {
		if _, ok := draft.MethodByName(name); ok {
			t.Fatalf("restricted predicate draft exposes %s", name)
		}
	}
}

// TestDerivedLaterOrCannotBypassScopesOrSeek checks protections remain separate
// across publication, derivation, forward seeks, and count transformation.
func TestDerivedLaterOrCannotBypassScopesOrSeek(t *testing.T) {
	t.Parallel()

	base := query.Table("users", func(statement *query.Builder) {
		statement.Scope(func(predicate *query.PredicateBuilder) {
			predicate.Where("tenant", 7)
		}, func(predicate *query.PredicateBuilder) {
			predicate.WhereNull("deleted_at")
		})
		statement.Where("active", true)
		statement.OrderBy("id")
	})
	var retained *query.Builder
	derived := base.Seek([]any{10}, false).Derive(func(statement *query.Builder) {
		retained = statement
		statement.OrWhere("public", true)
	})

	retained.OrWhere("public", false)

	requireScopedStatement(t, derived, query.Postgres,
		`SELECT * FROM "users" WHERE ("active" = $1 OR "public" = $2) AND ("tenant" = $3) AND ("deleted_at" IS NULL) AND ("id" > $4) ORDER BY "id" ASC`,
		true, true, 7, 10)
	requireScopedStatement(t, derived.Count(), query.Postgres,
		`SELECT COUNT(*) FROM "users" WHERE ("active" = $1 OR "public" = $2) AND ("tenant" = $3) AND ("deleted_at" IS NULL) AND ("id" > $4)`,
		true, true, 7, 10)
	requireScopedStatement(t, base, query.Postgres,
		`SELECT * FROM "users" WHERE ("active" = $1) AND ("tenant" = $2) AND ("deleted_at" IS NULL) ORDER BY "id" ASC`, true, 7)
}

// TestQueryArgumentsKeepCallerOwnership checks publication copies containers,
// not arbitrary SQL argument values that drivers may interpret by identity.
func TestQueryArgumentsKeepCallerOwnership(t *testing.T) {
	t.Parallel()

	value := []byte("first")
	users := query.Table("users", func(statement *query.Builder) {
		statement.Where("payload", value)
	})

	value[0] = 'F'

	statement, err := users.Build()

	if err != nil {
		t.Fatal(err)
	}

	if string(statement.Args()[0].([]byte)) != "First" {
		t.Fatal("publication recursively copied a caller-owned SQL argument")
	}
}

// TestFromRawNamedSourceBindsBeforeOuterArguments checks the existing named binder
// handles repetition, casts, quoting, and comments before outer positional values.
func TestFromRawNamedSourceBindsBeforeOuterArguments(t *testing.T) {
	t.Parallel()

	args := map[string]any{"tenant": 7}
	users := query.FromRaw(query.RawNamed(
		"SELECT ':ignored' AS label, id::text FROM users WHERE tenant = :tenant OR tenant = :tenant /* :ignored */", args),
		func(statement *query.Builder) {
			statement.As("members")
			statement.Where("id", 9)
			statement.OrderBy("id")
		})

	args["tenant"] = 99

	requireScopedStatement(t, users, query.Postgres,
		"SELECT * FROM (\nSELECT ':ignored' AS label, id::text FROM users WHERE tenant = $1 OR tenant = $2 /* :ignored */\n) AS \"members\" WHERE \"id\" = $3 ORDER BY \"id\" ASC", 7, 7, 9)

	statement, err := users.Count().BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(statement.Args(), []any{7, 7, 9}) {
		t.Fatalf("count arguments = %v", statement.Args())
	}

	statement, err = users.Seek([]any{10}, false).BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(statement.Args(), []any{7, 7, 9, 10}) {
		t.Fatalf("seek arguments = %v", statement.Args())
	}
}

// TestFromRawNamedSourceDialectGoldens checks source and outer placeholders share
// one dialect compiler, with the same binding order under every supported dialect.
func TestFromRawNamedSourceDialectGoldens(t *testing.T) {
	t.Parallel()

	users := query.FromRaw(query.RawNamed("SELECT id FROM users WHERE tenant = :tenant",
		map[string]any{"tenant": 7}), func(statement *query.Builder) {
		statement.As("members")
		statement.Where("id", ">", 10)
	})

	requireScopedStatement(t, users, query.ANSI,
		"SELECT * FROM (\nSELECT id FROM users WHERE tenant = ?\n) AS \"members\" WHERE \"id\" > ?", 7, 10)
	requireScopedStatement(t, users, query.SQLite,
		"SELECT * FROM (\nSELECT id FROM users WHERE tenant = ?\n) AS \"members\" WHERE \"id\" > ?", 7, 10)
	requireScopedStatement(t, users, query.Postgres,
		"SELECT * FROM (\nSELECT id FROM users WHERE tenant = $1\n) AS \"members\" WHERE \"id\" > $2", 7, 10)
	requireScopedStatement(t, users, query.MySQL,
		"SELECT * FROM (\nSELECT id FROM users WHERE tenant = ?\n) AS `members` WHERE `id` > ?", 7, 10)
}
