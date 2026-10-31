package sqlitedatabase_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// newScopeDatabase seeds an independent in-memory database for each test.
// Both tenants match each raw branch, while row 5 matches neither, so the
// first branch exposes a missing outer group without masking other filters.
func newScopeDatabase(t *testing.T) *database.Database {
	t.Helper()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)
	databaseClient := database.New(driver)
	ctx := sqliteTestContext(t)

	if _, err := databaseClient.Exec(ctx, query.Raw(`CREATE TABLE users (
		id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, name TEXT NOT NULL,
		email TEXT NOT NULL, backup_email TEXT NOT NULL)`)); err != nil {
		t.Fatal(err)
	}

	if _, err := databaseClient.Exec(ctx, query.Raw(`INSERT INTO users VALUES
		(1, 7, 'original', 'MATCH', 'other'),
		(2, 8, 'original', 'MATCH', 'other'),
		(3, 7, 'original', 'other', 'MATCH'),
		(4, 8, 'original', 'other', 'MATCH'),
		(5, 7, 'original', 'other', 'other')`)); err != nil {
		t.Fatal(err)
	}

	return databaseClient
}

// scopedEmailUsers uses trusted SQL text and bound values to find either
// address while requiring tenant 7 for both branches of the disjunction.
func scopedEmailUsers() query.Query {
	return query.Table("users", func(statement *query.Builder) {
		statement.WhereRaw("lower(email) = ? OR lower(backup_email) = ?", "match", "match")
		statement.Scope(func(predicate *query.PredicateBuilder) {
			predicate.Where("tenant_id", 7)
		})
	})
}

// TestRawScopeSelectTenantIsolation executes the original bypass shape and
// requires both matching rows in tenant 7, but neither matching tenant 8 row.
func TestRawScopeSelectTenantIsolation(t *testing.T) {
	t.Parallel()

	databaseClient := newScopeDatabase(t)

	rows, err := databaseClient.Select[sqliteRecord](sqliteTestContext(t), scopedEmailUsers().Derive(func(statement *query.Builder) {
		statement.Select("id", "name")
		statement.OrderBy("id")
	}))

	if err != nil {
		t.Fatal(err)
	}

	want := []sqliteRecord{
		{
			ID:   1,
			Name: "original",
		},
		{
			ID:   3,
			Name: "original",
		},
	}

	if !reflect.DeepEqual(rows.Items(), want) {
		t.Fatalf("rows = %#v, want %#v", rows.Items(), want)
	}
}

// TestRawScopeUpdateTenantIsolation verifies an update affects both intended
// rows and leaves every other row unchanged, including the bypass candidate.
func TestRawScopeUpdateTenantIsolation(t *testing.T) {
	t.Parallel()

	databaseClient := newScopeDatabase(t)
	ctx := sqliteTestContext(t)

	affected, err := databaseClient.Exec(ctx, scopedEmailUsers().Derive(func(statement *query.Builder) {
		statement.Update(map[string]any{"name": "changed"})
	}))

	if err != nil {
		t.Fatal(err)
	}

	rows, err := databaseClient.Select[sqliteRecord](ctx, query.Table("users", func(statement *query.Builder) {
		statement.Select("id", "name")
		statement.OrderBy("id")
	}))

	if err != nil {
		t.Fatal(err)
	}

	want := []sqliteRecord{
		{
			ID:   1,
			Name: "changed",
		},
		{
			ID:   2,
			Name: "original",
		},
		{
			ID:   3,
			Name: "changed",
		},
		{
			ID:   4,
			Name: "original",
		},
		{
			ID:   5,
			Name: "original",
		},
	}

	if affected != 2 || !reflect.DeepEqual(rows.Items(), want) {
		t.Fatalf("affected = %d, rows = %#v, want 2 and %#v", affected, rows.Items(), want)
	}
}

// TestRawScopeDeleteTenantIsolation verifies only matching tenant 7 rows are
// deleted; matching tenant 8 rows and an unmatched tenant 7 row must survive.
func TestRawScopeDeleteTenantIsolation(t *testing.T) {
	t.Parallel()

	databaseClient := newScopeDatabase(t)
	ctx := sqliteTestContext(t)

	affected, err := databaseClient.Exec(ctx, scopedEmailUsers().Derive(func(statement *query.Builder) {
		statement.Delete()
	}))

	if err != nil {
		t.Fatal(err)
	}

	rows, err := databaseClient.Select[sqliteRecord](ctx, query.Table("users", func(statement *query.Builder) {
		statement.Select("id", "name")
		statement.OrderBy("id")
	}))

	if err != nil {
		t.Fatal(err)
	}

	want := []sqliteRecord{
		{
			ID:   2,
			Name: "original",
		},
		{
			ID:   4,
			Name: "original",
		},
		{
			ID:   5,
			Name: "original",
		},
	}

	if affected != 2 || !reflect.DeepEqual(rows.Items(), want) {
		t.Fatalf("affected = %d, rows = %#v, want 2 and %#v", affected, rows.Items(), want)
	}
}

// TestSelectClearsExistingDestination exercises the public raw driver, whose
// empty-result contract also applies when a caller reuses a populated slice.
func TestSelectClearsExistingDestination(t *testing.T) {
	t.Parallel()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)
	values := []int{99}

	if err := driver.Select(t.Context(), "SELECT 7 WHERE false", &values); err != nil {
		t.Fatal(err)
	}

	if len(values) != 0 {
		t.Fatalf("Select empty result = %v, want empty", values)
	}
}

// TestSelectReplacesExistingDestination ensures successful results contain only
// the current query's rows, rather than appending them to a previous result.
func TestSelectReplacesExistingDestination(t *testing.T) {
	t.Parallel()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)
	values := []int{99}

	if err := driver.Select(t.Context(), "SELECT 7", &values); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(values, []int{7}) {
		t.Fatalf("Select = %v, want [7]", values)
	}
}

// TestSelectReplacesPointerDestination protects pointer-element support while
// dropping previous results without overwriting the old pointed-to values.
func TestSelectReplacesPointerDestination(t *testing.T) {
	t.Parallel()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)
	previous := 99
	values := []*int{&previous}

	if err := driver.Select(t.Context(), "SELECT 7", &values); err != nil {
		t.Fatal(err)
	}

	if len(values) != 1 || *values[0] != 7 {
		t.Fatalf("Select = %v, want one pointer to 7", values)
	}

	if previous != 99 {
		t.Fatal("Select mutated a previous element")
	}
}

// Audit is embedded by value into profile and flattened into its columns.
type Audit struct {
	// CreatedAt is when the row was created.
	CreatedAt time.Time `db:"created_at"`

	// Revision counts the updates.
	Revision int `db:"revision"`
}

// profile covers every field rule: tags with options, untagged names,
// embedded structs, skipped fields, JSON values, and nullable pointers.
type profile struct {
	Audit

	// ID is the primary key.
	ID int `db:"id,omitempty"`

	// DisplayName is untagged and maps to the column displayname.
	DisplayName string

	// Settings is stored as JSON text.
	Settings database.JSON[map[string]bool] `db:"settings"`

	// Nickname is nullable.
	Nickname *string `db:"nickname"`

	// Cache is never stored.
	Cache string `db:"-"`
}

// newProfileDatabase creates the profiles table in an in-memory database.
func newProfileDatabase(t *testing.T) *database.Database {
	t.Helper()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)

	_, err := driver.Exec(sqliteTestContext(t), `CREATE TABLE profiles (
		id INTEGER PRIMARY KEY,
		displayname TEXT NOT NULL,
		settings TEXT NOT NULL,
		nickname TEXT,
		created_at DATETIME NOT NULL,
		revision INTEGER NOT NULL
	)`)

	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	return database.New(driver)
}

// inUTC returns rows with their creation times in UTC, so they compare with
// reflect.DeepEqual regardless of the location the driver scanned them with.
func inUTC(rows []profile) []profile {
	for index := range rows {
		rows[index].CreatedAt = rows[index].CreatedAt.UTC()
	}

	return rows
}

// TestValuesRoundTripThroughSelect verifies a struct written with
// query.Values reads back identically with Select, Find, and a cursor.
func TestValuesRoundTripThroughSelect(t *testing.T) {
	t.Parallel()

	databaseClient := newProfileDatabase(t)
	ctx := sqliteTestContext(t)
	nick := "ada"

	want := []profile{
		{
			Audit: Audit{
				CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				Revision:  2,
			},
			ID:          1,
			DisplayName: "Ada",
			Settings: database.JSON[map[string]bool]{
				V: map[string]bool{"beta": true},
			},
			Nickname: &nick,
		},
		{
			Audit: Audit{
				CreatedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
				Revision:  1,
			},
			ID:          2,
			DisplayName: "Grace",
			Settings: database.JSON[map[string]bool]{
				V: map[string]bool{},
			},
		},
	}

	for _, row := range want {
		row.Cache = "not stored"

		if _, err := databaseClient.Exec(ctx, query.Table("profiles", func(statement *query.Builder) {
			statement.Insert(query.Values(row))
		})); err != nil {
			t.Fatalf("insert %d: %v", row.ID, err)
		}
	}

	// Cache is skipped on write, so it reads back empty.
	selected, err := databaseClient.Select[profile](ctx, query.Table("profiles", func(statement *query.Builder) {
		statement.OrderBy("id")
	}))

	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}

	if got := inUTC(selected.Items()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Select() = %#v, want %#v", got, want)
	}

	found, err := databaseClient.Find[profile](ctx, query.Table("profiles", func(statement *query.Builder) {
		statement.Where("id", 1)
	}))

	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}

	if got := inUTC([]profile{found})[0]; !reflect.DeepEqual(got, want[0]) {
		t.Fatalf("Find() = %#v, want %#v", got, want[0])
	}

	streamed, err := databaseClient.Cursor[profile](ctx, query.Table("profiles", func(statement *query.Builder) {
		statement.OrderBy("id")
	})).Items()

	if err != nil {
		t.Fatalf("Cursor() error = %v", err)
	}

	if !reflect.DeepEqual(inUTC(streamed), want) {
		t.Fatalf("Cursor() = %#v, want %#v", streamed, want)
	}
}

// TestValuesDrivesNamedUpdate verifies the map from Values binds RawNamed.
func TestValuesDrivesNamedUpdate(t *testing.T) {
	t.Parallel()

	databaseClient := newProfileDatabase(t)
	ctx := sqliteTestContext(t)
	row := profile{
		Audit: Audit{
			CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		ID:          1,
		DisplayName: "Ada",
		Settings: database.JSON[map[string]bool]{
			V: map[string]bool{},
		},
	}

	if _, err := databaseClient.Exec(ctx, query.Table("profiles", func(statement *query.Builder) {
		statement.Insert(query.Values(row))
	})); err != nil {
		t.Fatalf("insert: %v", err)
	}

	row.DisplayName = "Ada Lovelace"

	affected, err := databaseClient.Exec(ctx, query.RawNamed(
		"UPDATE profiles SET displayname = :displayname WHERE id = :id", query.Values(row)))

	if err != nil || affected != 1 {
		t.Fatalf("Exec() = %d, %v, want 1 row", affected, err)
	}

	name, err := databaseClient.Find[string](ctx, query.Table("profiles", func(statement *query.Builder) {
		statement.Select("displayname")
		statement.Where("id", 1)
	}))

	if err != nil || name != "Ada Lovelace" {
		t.Fatalf("Find() = %q, %v, want the updated name", name, err)
	}
}

// TestSelectUnknownColumnFails verifies a result column without a field is an
// error that names the column.
func TestSelectUnknownColumnFails(t *testing.T) {
	t.Parallel()

	databaseClient := newProfileDatabase(t)

	_, err := databaseClient.Select[struct {
		// ID maps the known result column; surprise deliberately has no field.
		ID int `db:"id"`
	}](sqliteTestContext(t), query.Raw("SELECT 1 AS id, 2 AS surprise"))

	if !errors.Is(err, query.ErrUnknownColumn) {
		t.Fatalf("Select() error = %v, want ErrUnknownColumn", err)
	}
}

// TestCursorPaginateReadsKeysWithSharedRules verifies pagination keys follow
// the field rules, including a column of an embedded struct.
func TestCursorPaginateReadsKeysWithSharedRules(t *testing.T) {
	t.Parallel()

	databaseClient := newProfileDatabase(t)
	ctx := sqliteTestContext(t)

	for id := 1; id <= 3; id++ {
		row := profile{
			Audit: Audit{
				CreatedAt: time.Date(2026, 1, id, 0, 0, 0, 0, time.UTC),
			},
			ID:          id,
			DisplayName: "n",
			Settings: database.JSON[map[string]bool]{
				V: map[string]bool{},
			},
		}

		if _, err := databaseClient.Exec(ctx, query.Table("profiles", func(statement *query.Builder) {
			statement.Insert(query.Values(row))
		})); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	builder := query.Table("profiles", func(statement *query.Builder) {
		statement.OrderBy("created_at")
		statement.OrderBy("id")
	})

	first, err := databaseClient.CursorPaginate[profile](ctx, builder, pagination.CursorParams{
		PerPage: 2,
	})

	if err != nil {
		t.Fatalf("CursorPaginate() error = %v", err)
	}

	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %#v, want 2 items and a next cursor", first)
	}

	second, err := databaseClient.CursorPaginate[profile](ctx, builder, pagination.CursorParams{
		PerPage: 2,
		Cursor:  first.NextCursor,
	})

	if err != nil {
		t.Fatalf("CursorPaginate(next) error = %v", err)
	}

	if len(second.Items) != 1 || second.Items[0].ID != 3 {
		t.Fatalf("second page = %#v, want only profile 3", second)
	}
}

// pageRow is a row of the pagination test table.
type pageRow struct {
	// ID is the unique row identifier.
	ID int `db:"id"`

	// Grp is a low-cardinality grouping column.
	Grp string `db:"grp"`

	// Score is a non-unique column with many ties.
	Score int `db:"score"`

	// CreatedAt is a time key.
	CreatedAt time.Time `db:"created_at"`
}

// pageBase is the creation time of row 1; each row is an hour later.
var pageBase = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// newPageDatabase creates a database holding ten rows with ids 1 to 10,
// grp "a" for odd ids and "b" for even ones, score id%3, and increasing
// creation times. Owners map every row to a label.
func newPageDatabase(t *testing.T) *database.Database {
	t.Helper()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)
	databaseClient := database.New(driver)
	ctx := sqliteTestContext(t)

	schema := []string{
		"CREATE TABLE items (id INTEGER PRIMARY KEY, grp TEXT NOT NULL, score INTEGER NOT NULL, created_at DATETIME NOT NULL)",
		"CREATE TABLE owners (item_id INTEGER PRIMARY KEY, label TEXT NOT NULL)",
	}

	for _, statement := range schema {
		if _, err := driver.Exec(ctx, statement); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}

	for id := 1; id <= 10; id++ {
		grp := "a"

		if id%2 == 0 {
			grp = "b"
		}

		_, err := databaseClient.Exec(ctx, query.Table("items", func(statement *query.Builder) {
			statement.Insert(map[string]any{
				"id":         id,
				"grp":        grp,
				"score":      id % 3,
				"created_at": pageBase.Add(time.Duration(id) * time.Hour),
			})
		}))

		if err != nil {
			t.Fatalf("insert item: %v", err)
		}

		_, err = databaseClient.Exec(ctx, query.Table("owners", func(statement *query.Builder) {
			statement.Insert(map[string]any{
				"item_id": id,
				"label":   "owner",
			})
		}))

		if err != nil {
			t.Fatalf("insert owner: %v", err)
		}
	}

	return databaseClient
}

// pageIDs lists the row ids of items.
func pageIDs(items []pageRow) []int {
	ids := make([]int, 0, len(items))

	for _, item := range items {
		ids = append(ids, item.ID)
	}

	return ids
}

// walkCursor follows next cursors from the first window and returns the
// ids of every window.
func walkCursor(t *testing.T, databaseClient *database.Database, b query.Query, perPage int) [][]int {
	t.Helper()

	ctx := sqliteTestContext(t)
	var windows [][]int
	cursor := ""

	for range 20 {
		result, err := databaseClient.CursorPaginate[pageRow](ctx, b, pagination.CursorParams{
			Cursor:  cursor,
			PerPage: perPage,
		})

		if err != nil {
			t.Fatalf("CursorPaginate() error = %v", err)
		}

		windows = append(windows, pageIDs(result.Items))

		if result.NextCursor == "" {
			return windows
		}

		cursor = result.NextCursor
	}

	t.Fatal("cursor walk did not finish")

	return nil
}

// TestSQLitePaginatePages verifies offset pages and totals.
func TestSQLitePaginatePages(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	ctx := sqliteTestContext(t)
	b := query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := databaseClient.Paginate[pageRow](ctx, b, pagination.PageParams{
		Page:    2,
		PerPage: 4,
	})

	if err != nil {
		t.Fatalf("Paginate() error = %v", err)
	}

	if got, want := pageIDs(page.Items), []int{5, 6, 7, 8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}

	if page.Total != 10 || page.LastPage != 3 || page.CurrentPage != 2 {
		t.Fatalf("page = %+v, want total 10, last 3, current 2", page)
	}
}

// TestSQLitePaginateGroupBy verifies the total counts groups, not rows.
func TestSQLitePaginateGroupBy(t *testing.T) {
	t.Parallel()

	type groupRow struct {
		// Grp is the group name.
		Grp string `db:"grp"`

		// N is the group size.
		N int `db:"n"`
	}

	databaseClient := newPageDatabase(t)
	b := query.Table("items", func(statement *query.Builder) {
		statement.Select("grp")
		statement.SelectRaw("COUNT(*) AS n")
		statement.GroupBy("grp")
		statement.OrderBy("grp")
	})

	page, err := databaseClient.Paginate[groupRow](sqliteTestContext(t), b, pagination.PageParams{
		PerPage: 1,
	})

	if err != nil {
		t.Fatalf("Paginate() error = %v", err)
	}

	if page.Total != 2 || page.LastPage != 2 || len(page.Items) != 1 || page.Items[0].N != 5 {
		t.Fatalf("page = %+v, want total 2 over 2 pages with first group of 5", page)
	}
}

// TestSQLitePaginateNoOrder verifies a builder without order is rejected.
func TestSQLitePaginateNoOrder(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)

	_, err := databaseClient.Paginate[pageRow](sqliteTestContext(t), query.Table("items"), pagination.PageParams{})

	if !errors.Is(err, database.ErrNoOrder) {
		t.Fatalf("Paginate() error = %v, want ErrNoOrder", err)
	}
}

// TestSQLiteCursorWalk verifies first, middle, and last windows.
func TestSQLiteCursorWalk(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)

	windows := walkCursor(t, databaseClient, query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("id")
	}), 4)

	want := [][]int{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10}}

	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %v, want %v", windows, want)
	}
}

// TestSQLiteCursorExactMultiple verifies no extra empty window follows a
// full last window.
func TestSQLiteCursorExactMultiple(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)

	windows := walkCursor(t, databaseClient, query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("id")
	}), 5)

	want := [][]int{{1, 2, 3, 4, 5}, {6, 7, 8, 9, 10}}

	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %v, want %v", windows, want)
	}
}

// TestSQLiteCursorEmpty verifies an empty result has no cursors.
func TestSQLiteCursorEmpty(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	b := query.Table("items", func(statement *query.Builder) {
		statement.Where("id", ">", 100)
		statement.OrderBy("id")
	})

	result, err := databaseClient.CursorPaginate[pageRow](sqliteTestContext(t), b, pagination.CursorParams{})

	if err != nil {
		t.Fatalf("CursorPaginate() error = %v", err)
	}

	if result.Items == nil || len(result.Items) != 0 || result.NextCursor != "" || result.PrevCursor != "" {
		t.Fatalf("result = %+v, want empty window without cursors", result)
	}
}

// TestSQLiteCursorPrevRoundTrip verifies prev cursors page backward and
// restore display order.
func TestSQLiteCursorPrevRoundTrip(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	ctx := sqliteTestContext(t)
	b := query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	first, err := databaseClient.CursorPaginate[pageRow](ctx, b, pagination.CursorParams{
		PerPage: 3,
	})

	if err != nil {
		t.Fatalf("first CursorPaginate() error = %v", err)
	}

	if first.PrevCursor != "" {
		t.Fatalf("first window PrevCursor = %q, want empty", first.PrevCursor)
	}

	second, err := databaseClient.CursorPaginate[pageRow](ctx, b, pagination.CursorParams{
		Cursor:  first.NextCursor,
		PerPage: 3,
	})

	if err != nil {
		t.Fatalf("second CursorPaginate() error = %v", err)
	}

	if got, want := pageIDs(second.Items), []int{4, 5, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second items = %v, want %v", got, want)
	}

	if second.PrevCursor == "" || second.NextCursor == "" {
		t.Fatalf("second window = %+v, want both cursors", second)
	}

	back, err := databaseClient.CursorPaginate[pageRow](ctx, b, pagination.CursorParams{
		Cursor:  second.PrevCursor,
		PerPage: 3,
	})

	if err != nil {
		t.Fatalf("back CursorPaginate() error = %v", err)
	}

	if got, want := pageIDs(back.Items), []int{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("back items = %v, want %v", got, want)
	}

	if back.PrevCursor != "" || back.NextCursor == "" {
		t.Fatalf("back window = %+v, want next cursor only", back)
	}
}

// TestSQLiteCursorPrevHasMore verifies a backward window with more rows
// before it keeps a prev cursor and always has a next cursor.
func TestSQLiteCursorPrevHasMore(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	ctx := sqliteTestContext(t)
	b := query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	last, err := databaseClient.CursorPaginate[pageRow](ctx, b, pagination.CursorParams{
		Cursor:  mustCursorAfter(t, databaseClient, b, 2, 2),
		PerPage: 2,
	})

	if err != nil {
		t.Fatalf("CursorPaginate() error = %v", err)
	}

	back, err := databaseClient.CursorPaginate[pageRow](ctx, b, pagination.CursorParams{
		Cursor:  last.PrevCursor,
		PerPage: 2,
	})

	if err != nil {
		t.Fatalf("back CursorPaginate() error = %v", err)
	}

	if got, want := pageIDs(back.Items), []int{3, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("back items = %v, want %v", got, want)
	}

	if back.PrevCursor == "" || back.NextCursor == "" {
		t.Fatalf("back window = %+v, want both cursors", back)
	}
}

// mustCursorAfter walks hops windows of perPage rows from the start and
// returns the next cursor reached.
func mustCursorAfter(t *testing.T, databaseClient *database.Database, b query.Query, hops, perPage int) string {
	t.Helper()

	cursor := ""

	for range hops {
		result, err := databaseClient.CursorPaginate[pageRow](sqliteTestContext(t), b, pagination.CursorParams{
			Cursor:  cursor,
			PerPage: perPage,
		})

		if err != nil {
			t.Fatalf("CursorPaginate() error = %v", err)
		}

		cursor = result.NextCursor
	}

	return cursor
}

// TestSQLiteCursorTies verifies a non-unique column with an id tiebreaker
// visits every row exactly once.
func TestSQLiteCursorTies(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)

	windows := walkCursor(t, databaseClient, query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("score")
		statement.OrderBy("id")
	}), 3)

	want := [][]int{{3, 6, 9}, {1, 4, 7}, {10, 2, 5}, {8}}

	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %v, want %v", windows, want)
	}
}

// TestSQLiteCursorMixedDirections verifies ASC and DESC orders together.
func TestSQLiteCursorMixedDirections(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)

	windows := walkCursor(t, databaseClient, query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("score")
		statement.OrderByDesc("id")
	}), 4)

	want := [][]int{{9, 6, 3, 10}, {7, 4, 1, 8}, {5, 2}}

	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %v, want %v", windows, want)
	}
}

// TestSQLiteCursorTimeKeys verifies time.Time keys survive the cursor.
func TestSQLiteCursorTimeKeys(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)

	windows := walkCursor(t, databaseClient, query.Table("items", func(statement *query.Builder) {
		statement.OrderByDesc("created_at")
		statement.OrderBy("id")
	}), 4)

	want := [][]int{{10, 9, 8, 7}, {6, 5, 4, 3}, {2, 1}}

	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %v, want %v", windows, want)
	}
}

// TestSQLiteCursorFromRawJoin verifies a raw joined source with a
// qualified order column.
func TestSQLiteCursorFromRawJoin(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	b := query.FromRaw(query.Raw("SELECT i.id AS id, i.grp AS grp, i.score AS score, i.created_at AS created_at "+
		"FROM items i JOIN owners o ON o.item_id = i.id"), func(statement *query.Builder) {
		statement.OrderBy("score")
		statement.OrderBy("id")
	})

	windows := walkCursor(t, databaseClient, b, 5)

	want := [][]int{{3, 6, 9, 1, 4}, {7, 10, 2, 5, 8}}

	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %v, want %v", windows, want)
	}
}

// TestSQLiteCursorTampered verifies garbage cursors are client errors.
func TestSQLiteCursorTampered(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	b := query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	_, err := databaseClient.CursorPaginate[pageRow](sqliteTestContext(t), b, pagination.CursorParams{
		Cursor: "not-a-cursor",
	})

	if !isCursorDecode(err) {
		t.Fatalf("CursorPaginate() error = %v, want ErrCursorDecode", err)
	}
}

// TestSQLiteCursorOtherOrder verifies a cursor cannot be used with a
// different ordering.
func TestSQLiteCursorOtherOrder(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	ctx := sqliteTestContext(t)

	first, err := databaseClient.CursorPaginate[pageRow](ctx, query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("id")
	}), pagination.CursorParams{
		PerPage: 2,
	})

	if err != nil {
		t.Fatalf("CursorPaginate() error = %v", err)
	}

	_, err = databaseClient.CursorPaginate[pageRow](ctx, query.Table("items", func(statement *query.Builder) {
		statement.OrderByDesc("id")
	}), pagination.CursorParams{
		Cursor: first.NextCursor,
	})

	if !isCursorDecode(err) {
		t.Fatalf("CursorPaginate() error = %v, want ErrCursorDecode", err)
	}
}

// TestSQLiteEachBatches verifies Each visits every row across batches.
func TestSQLiteEachBatches(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	var ids []int

	for item, err := range databaseClient.Each[pageRow](sqliteTestContext(t), query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("score")
		statement.OrderBy("id")
	}), 3).Iter() {
		if err != nil {
			t.Fatalf("Each() error = %v", err)
		}

		ids = append(ids, item.ID)
	}

	want := []int{3, 6, 9, 1, 4, 7, 10, 2, 5, 8}

	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

// TestSQLiteEachEarlyBreak verifies Each stops when the consumer does.
func TestSQLiteEachEarlyBreak(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	var ids []int

	for item, err := range databaseClient.Each[pageRow](sqliteTestContext(t), query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("id")
	}), 2).Iter() {
		if err != nil {
			t.Fatalf("Each() error = %v", err)
		}

		ids = append(ids, item.ID)

		if len(ids) == 3 {
			break
		}
	}

	if want := []int{1, 2, 3}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

// TestSQLiteEachFreshBuilder verifies Each with a fresh builder yields
// every row without skipping any.
func TestSQLiteEachFreshBuilder(t *testing.T) {
	t.Parallel()

	databaseClient := newPageDatabase(t)
	var ids []int

	for item, err := range databaseClient.Each[pageRow](sqliteTestContext(t), query.Table("items", func(statement *query.Builder) {
		statement.OrderBy("id")
	}), 4).Iter() {
		if err != nil {
			t.Fatalf("Each() error = %v", err)
		}

		ids = append(ids, item.ID)
	}

	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

// isCursorDecode reports whether err wraps pagination.ErrCursorDecode.
func isCursorDecode(err error) bool {
	return err != nil && errors.Is(err, pagination.ErrCursorDecode)
}

// newSchemaDatabase opens an in-memory SQLite database for schema tests.
func newSchemaDatabase(t *testing.T) (*database.Database, func(statement string, column int) []string) {
	t.Helper()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)

	// inspect runs a PRAGMA or query and returns the text of one result column.
	inspect := func(statement string, column int) []string {
		t.Helper()

		rows, err := driver.SQL().QueryContext(sqliteTestContext(t), statement)

		if err != nil {
			t.Fatalf("query %q: %v", statement, err)
		}

		t.Cleanup(func() {
			if err := rows.Close(); err != nil {
				t.Errorf("close rows of %q: %v", statement, err)
			}
		})

		columns, err := rows.Columns()

		if err != nil {
			t.Fatalf("columns of %q: %v", statement, err)
		}

		var values []string

		for rows.Next() {
			cells := make([]any, len(columns))
			pointers := make([]any, len(columns))

			for index := range cells {
				pointers[index] = &cells[index]
			}

			if err := rows.Scan(pointers...); err != nil {
				t.Fatalf("scan %q: %v", statement, err)
			}

			values = append(values, fmt.Sprint(cells[column]))
		}

		if err := rows.Err(); err != nil {
			t.Fatalf("rows of %q: %v", statement, err)
		}

		return values
	}

	return database.New(driver), inspect
}

// execSchema runs every statement of operation through the facade.
func execSchema(t *testing.T, databaseClient *database.Database, operation schema.Operation) {
	t.Helper()

	if err := operation.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}

	for _, statement := range operation.Statements() {
		if _, err := databaseClient.Exec(sqliteTestContext(t), statement); err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
	}
}

// createSchemaTables creates users and posts tables.
func createSchemaTables(t *testing.T, databaseClient *database.Database) {
	t.Helper()

	execSchema(t, databaseClient, schema.CreateIfNotExists("users", func(table *schema.Table) {
		table.ID()
		table.String("email", 255).Unique()
		table.String("name", 100).Nullable()
		table.Boolean("active").Default(true)
		table.Timestamps()
	}))
	execSchema(t, databaseClient, schema.Create("posts", func(table *schema.Table) {
		table.ID()
		table.ForeignID("user_id", "users").OnDelete(schema.Cascade)
		table.Text("body")
	}))
}

// requireStrings fails unless got equals want.
func requireStrings(t *testing.T, label string, got, want []string) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

// TestSQLiteCreateTableColumnNames verifies created column names.
func TestSQLiteCreateTableColumnNames(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	requireStrings(t, "columns", inspect("PRAGMA table_info(users)", 1),
		[]string{"id", "email", "name", "active", "created_at", "updated_at"})
}

// TestSQLiteCreateTableNotNullFlags verifies created column nullability.
func TestSQLiteCreateTableNotNullFlags(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	requireStrings(t, "notnull", inspect("PRAGMA table_info(users)", 3), []string{"0", "1", "0", "1", "1", "1"})
}

// TestSQLiteCreateTablePrimaryKey verifies the primary key column.
func TestSQLiteCreateTablePrimaryKey(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	requireStrings(t, "pk", inspect("PRAGMA table_info(users)", 5), []string{"1", "0", "0", "0", "0", "0"})
}

// TestSQLiteCreateTableUniqueConstraint verifies the unique constraint index.
func TestSQLiteCreateTableUniqueConstraint(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	requireStrings(t, "unique flags", inspect("PRAGMA index_list(users)", 2), []string{"1"})
}

// TestSQLiteCreateTableForeignKeyTable verifies the referenced table.
func TestSQLiteCreateTableForeignKeyTable(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	requireStrings(t, "foreign table", inspect("PRAGMA foreign_key_list(posts)", 2), []string{"users"})
}

// TestSQLiteCreateTableForeignKeyOnDelete verifies the ON DELETE action.
func TestSQLiteCreateTableForeignKeyOnDelete(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	requireStrings(t, "on delete", inspect("PRAGMA foreign_key_list(posts)", 6), []string{"CASCADE"})
}

// TestSQLiteCreateIndexIsListed verifies index creation.
func TestSQLiteCreateIndexIsListed(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	execSchema(t, databaseClient, schema.Alter("posts", func(table *schema.Table) {
		table.Index("user_id")
	}))

	requireStrings(t, "indexes", inspect("PRAGMA index_list(posts)", 1), []string{"posts_user_id_index"})
}

// TestSQLiteCreateIndexIsUnique verifies the unique flag of a created index.
func TestSQLiteCreateIndexIsUnique(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	execSchema(t, databaseClient, schema.Alter("posts", func(table *schema.Table) {
		table.Unique("user_id")
	}))

	requireStrings(t, "unique flags", inspect("PRAGMA index_list(posts)", 2), []string{"1"})
}

// TestSQLiteDropIndexRemovesIndex verifies index removal.
func TestSQLiteDropIndexRemovesIndex(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	execSchema(t, databaseClient, schema.Alter("posts", func(table *schema.Table) {
		table.Index("user_id")
	}))
	execSchema(t, databaseClient, schema.Alter("posts", func(table *schema.Table) {
		table.DropIndex("user_id")
	}))

	if got := inspect("PRAGMA index_list(posts)", 1); len(got) != 0 {
		t.Fatalf("indexes after drop = %v", got)
	}
}

// TestSQLiteAlterAddColumn verifies ADD COLUMN.
func TestSQLiteAlterAddColumn(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	execSchema(t, databaseClient, schema.Alter("users", func(table *schema.Table) {
		table.String("avatar", 255).Nullable()
	}))

	got := inspect("PRAGMA table_info(users)", 1)

	if len(got) == 0 || got[len(got)-1] != "avatar" {
		t.Fatalf("columns = %v", got)
	}
}

// TestSQLiteAlterRenameColumn verifies RENAME COLUMN.
func TestSQLiteAlterRenameColumn(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	execSchema(t, databaseClient, schema.Alter("users", func(table *schema.Table) {
		table.RenameColumn("name", "full_name")
	}))

	requireStrings(t, "columns", inspect("PRAGMA table_info(users)", 1),
		[]string{"id", "email", "full_name", "active", "created_at", "updated_at"})
}

// TestSQLiteAlterDropColumn verifies DROP COLUMN.
func TestSQLiteAlterDropColumn(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	execSchema(t, databaseClient, schema.Alter("users", func(table *schema.Table) {
		table.DropColumn("name")
	}))

	requireStrings(t, "columns", inspect("PRAGMA table_info(users)", 1),
		[]string{"id", "email", "active", "created_at", "updated_at"})
}

// TestSQLiteDropTableRemovesTable verifies DROP TABLE.
func TestSQLiteDropTableRemovesTable(t *testing.T) {
	t.Parallel()

	databaseClient, inspect := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	execSchema(t, databaseClient, schema.Drop("posts"))

	if got := inspect("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'posts'", 0); len(got) != 0 {
		t.Fatalf("tables after drop = %v", got)
	}
}

// TestSQLiteDropTableIfExistsIsIdempotent verifies repeated DROP TABLE IF EXISTS succeeds.
func TestSQLiteDropTableIfExistsIsIdempotent(t *testing.T) {
	t.Parallel()

	databaseClient, _ := newSchemaDatabase(t)
	createSchemaTables(t, databaseClient)

	execSchema(t, databaseClient, schema.DropIfExists("posts"))
	execSchema(t, databaseClient, schema.DropIfExists("posts"))
}
