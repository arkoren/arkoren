package query_test

import (
	"reflect"
	"testing"

	"arkoren.dev/foundation/query"
)

// TestPostgresCountDropsIncompatibleRowLock verifies Count can discard a lock
// PostgreSQL rejects on the original DISTINCT definition, retaining bindings.
func TestPostgresCountDropsIncompatibleRowLock(t *testing.T) {
	t.Parallel()

	definition := query.Table("users", func(statement *query.Builder) {
		statement.Select("id")
		statement.Distinct()
		statement.Where("active", true)
		statement.ForShare()
	})

	requireUnsupported(t, definition, query.Postgres)

	statement, err := definition.Count().BuildFor(query.Postgres)

	if err != nil {
		t.Fatal(err)
	}

	want := "SELECT COUNT(*) FROM (\nSELECT DISTINCT \"id\" FROM \"users\" " +
		"WHERE \"active\" = $1\n) AS \"count\""

	if statement.SQL() != want {
		t.Fatalf("SQL = %q, want %q", statement.SQL(), want)
	}

	if !reflect.DeepEqual(statement.Args(), []any{true}) {
		t.Fatalf("Args = %#v, want [true]", statement.Args())
	}
}

// TestCountPlain verifies a plain query swaps its columns for COUNT(*).
func TestCountPlain(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Select("id", "name")
		statement.Where("active", true)
		statement.OrderBy("name")
		statement.Limit(10)
		statement.Offset(5)
		statement.ForUpdate()
	}).Count()

	requireBuilderSQL(t, builder, query.ANSI, `SELECT COUNT(*) FROM "users" WHERE "active" = ?`)
	requireBuilderSQL(t, builder, query.SQLite, `SELECT COUNT(*) FROM "users" WHERE "active" = ?`)
	requireBuilderSQL(t, builder, query.Postgres, `SELECT COUNT(*) FROM "users" WHERE "active" = $1`)
	requireBuilderSQL(t, builder, query.MySQL, "SELECT COUNT(*) FROM `users` WHERE `active` = ?")
}

// TestCountGrouped verifies a grouped query is wrapped.
func TestCountGrouped(t *testing.T) {
	t.Parallel()

	builder := query.Table("orders", func(statement *query.Builder) {
		statement.Select("user_id")
		statement.Where("paid", true)
		statement.GroupBy("user_id")
		statement.HavingRaw("SUM(total) > ?", 100)
		statement.OrderBy("user_id")
		statement.Limit(3)
	}).Count()

	requireBuilderSQL(t, builder, query.ANSI, "SELECT COUNT(*) FROM (\n"+
		`SELECT "user_id" FROM "orders" WHERE "paid" = ? GROUP BY "user_id" HAVING SUM(total) > ?`+
		"\n"+`) AS "count"`)
	requireBuilderSQL(t, builder, query.SQLite, "SELECT COUNT(*) FROM (\n"+
		`SELECT "user_id" FROM "orders" WHERE "paid" = ? GROUP BY "user_id" HAVING SUM(total) > ?`+
		"\n"+`) AS "count"`)
	requireBuilderSQL(t, builder, query.Postgres, "SELECT COUNT(*) FROM (\n"+
		`SELECT "user_id" FROM "orders" WHERE "paid" = $1 GROUP BY "user_id" HAVING SUM(total) > $2`+
		"\n"+`) AS "count"`)
	requireBuilderSQL(t, builder, query.MySQL, "SELECT COUNT(*) FROM (\n"+
		"SELECT `user_id` FROM `orders` WHERE `paid` = ? GROUP BY `user_id` HAVING SUM(total) > ?"+
		"\n"+") AS `count`")
}

// TestCountDistinct verifies a distinct query is wrapped.
func TestCountDistinct(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.Select("role")
		statement.Distinct()
		statement.OrderBy("role")
	}).Count()

	requireBuilderSQL(t, builder, query.ANSI, "SELECT COUNT(*) FROM (\n"+
		`SELECT DISTINCT "role" FROM "users"`+"\n"+`) AS "count"`)
	requireBuilderSQL(t, builder, query.SQLite, "SELECT COUNT(*) FROM (\n"+
		`SELECT DISTINCT "role" FROM "users"`+"\n"+`) AS "count"`)
	requireBuilderSQL(t, builder, query.Postgres, "SELECT COUNT(*) FROM (\n"+
		`SELECT DISTINCT "role" FROM "users"`+"\n"+`) AS "count"`)
	requireBuilderSQL(t, builder, query.MySQL, "SELECT COUNT(*) FROM (\n"+
		"SELECT DISTINCT `role` FROM `users`"+"\n"+") AS `count`")
}

// TestCountRawSource verifies a raw-sourced query is wrapped and its
// placeholders stay numbered across the nesting.
func TestCountRawSource(t *testing.T) {
	t.Parallel()

	builder := query.FromRaw(query.Raw("SELECT id FROM events WHERE kind = ?", "click"), func(statement *query.Builder) {
		statement.As("e")
		statement.Where("id", ">", 10)
	}).Count()

	requireBuilderSQL(t, builder, query.Postgres, "SELECT COUNT(*) FROM (\n"+
		`SELECT * FROM (`+"\n"+`SELECT id FROM events WHERE kind = $1`+"\n"+`) AS "e" WHERE "id" > $2`+
		"\n"+`) AS "count"`)

	statement, err := builder.BuildFor(query.MySQL)

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(statement.Args(), []any{"click", 10}) {
		t.Fatalf("unexpected args: %#v", statement.Args())
	}
}

// TestCountRejectsMutation verifies only selects can be counted.
func TestCountRejectsMutation(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.Delete()
		statement.Where("a", 1)
	}).Count())
}

// TestCountLeavesReceiver verifies Count does not change the original.
func TestCountLeavesReceiver(t *testing.T) {
	t.Parallel()

	builder := query.Table("users", func(statement *query.Builder) {
		statement.OrderBy("id")
		statement.Limit(5)
	})

	_ = builder.Count()

	requireBuilderSQL(t, builder, query.Postgres, `SELECT * FROM "users" ORDER BY "id" ASC LIMIT 5`)
}

// TestSeekForward verifies the expanded keyset filter for mixed directions.
func TestSeekForward(t *testing.T) {
	t.Parallel()

	builder := query.Table("posts", func(statement *query.Builder) {
		statement.Where("published", true)
		statement.OrderBy("score")
		statement.OrderByDesc("created_at")
		statement.OrderBy("id")
	}).Seek([]any{1, 2, 3}, false)

	const ansi = `SELECT * FROM "posts" WHERE ("published" = ?) AND ("score" > ? OR ("score" = ? AND ` +
		`"created_at" < ?) OR ("score" = ? AND "created_at" = ? AND "id" > ?)) ` +
		`ORDER BY "score" ASC, "created_at" DESC, "id" ASC`

	requireBuilderSQL(t, builder, query.ANSI, ansi)
	requireBuilderSQL(t, builder, query.SQLite, ansi)
	requireBuilderSQL(t, builder, query.Postgres, `SELECT * FROM "posts" WHERE ("published" = $1) AND `+
		`("score" > $2 OR ("score" = $3 AND "created_at" < $4) OR ("score" = $5 AND `+
		`"created_at" = $6 AND "id" > $7)) ORDER BY "score" ASC, "created_at" DESC, "id" ASC`)
	requireBuilderSQL(t, builder, query.MySQL, "SELECT * FROM `posts` WHERE (`published` = ?) AND "+
		"(`score` > ? OR (`score` = ? AND `created_at` < ?) OR (`score` = ? AND "+
		"`created_at` = ? AND `id` > ?)) ORDER BY `score` ASC, `created_at` DESC, `id` ASC")

	statement, err := builder.BuildFor(query.MySQL)

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(statement.Args(), []any{true, 1, 1, 2, 1, 2, 3}) {
		t.Fatalf("unexpected args: %#v", statement.Args())
	}
}

// TestSeekBackward verifies backward flips comparisons and directions.
func TestSeekBackward(t *testing.T) {
	t.Parallel()

	builder := query.Table("posts", func(statement *query.Builder) {
		statement.OrderByDesc("created_at")
		statement.OrderBy("id")
	}).Seek([]any{"x", 9}, true)

	requireBuilderSQL(t, builder, query.ANSI, `SELECT * FROM "posts" WHERE ("created_at" > ? OR `+
		`("created_at" = ? AND "id" < ?)) ORDER BY "created_at" ASC, "id" DESC`)
	requireBuilderSQL(t, builder, query.SQLite, `SELECT * FROM "posts" WHERE ("created_at" > ? OR `+
		`("created_at" = ? AND "id" < ?)) ORDER BY "created_at" ASC, "id" DESC`)
	requireBuilderSQL(t, builder, query.Postgres, `SELECT * FROM "posts" WHERE ("created_at" > $1 OR `+
		`("created_at" = $2 AND "id" < $3)) ORDER BY "created_at" ASC, "id" DESC`)
	requireBuilderSQL(t, builder, query.MySQL, "SELECT * FROM `posts` WHERE (`created_at` > ? OR "+
		"(`created_at` = ? AND `id` < ?)) ORDER BY `created_at` ASC, `id` DESC")
}

// TestSeekSingleOrder verifies one ordering yields one comparison.
func TestSeekSingleOrder(t *testing.T) {
	t.Parallel()

	requireBuilderSQL(t, query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("id")
	}).Seek([]any{5}, false), query.Postgres,
		`SELECT * FROM "t" WHERE ("id" > $1) ORDER BY "id" ASC`)
}

// TestSeekErrors verifies invalid seeks fail.
func TestSeekErrors(t *testing.T) {
	t.Parallel()

	requireBuilderInvalid(t, query.Table("t").Seek([]any{1}, false))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("a")
	}).Seek([]any{1, 2}, false))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("a")
	}).Seek(nil, false))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("a")
	}).Seek([]any{nil}, false))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("a")
	}).Seek([]any{(*int)(nil)}, true))
	requireBuilderInvalid(t, query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("a")
		statement.Delete()
		statement.Where("a", 1)
	}).Seek([]any{1}, false))
}

// TestSeekLeavesReceiver verifies Seek does not reverse the original orders.
func TestSeekLeavesReceiver(t *testing.T) {
	t.Parallel()

	builder := query.Table("t", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	_ = builder.Seek([]any{1}, true)

	requireBuilderSQL(t, builder, query.ANSI, `SELECT * FROM "t" ORDER BY "id" ASC`)
}
