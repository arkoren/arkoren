package database_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/foundation/query"
)

// TestRetainedDatabasePolicyCannotReconfigurePublishedFacade verifies detached
// namespace and telemetry settings without opening a connection or transaction.
func TestRetainedDatabasePolicyCannotReconfigurePublishedFacade(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	var retained *database.Builder
	db := database.New(adapterStub{}, func(connection *database.Builder) {
		retained = connection
		connection.Telemetry(fake.Telemetry(t))
		connection.Namespace("billing")
	})
	retained.Telemetry(telemetry.New(nil))
	retained.Namespace("mutated")

	if _, err := db.Exec(t.Context(), query.Raw("SELECT 1")); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "SELECT", telemetry.String("db.namespace", "billing"))
}

// errSecret carries a value that must never reach telemetry.
var errSecret = errors.New("duplicate key value (email)=(alice@example.com)")

// fakeRows is a cursor over ints.
type fakeRows struct {
	// values holds the integer rows returned in order by the cursor.
	values []int
	// index tracks the one-based current row after Next.
	index int
	// err is the failure reported after iteration.
	err error
	// closed records whether the caller released the cursor.
	closed bool
	// closeErr reports cleanup failure independently of iteration.
	closeErr error
	// scanErr fails decoding the current row before its value is copied.
	scanErr error
}

// Next advances to the next configured integer, returning false after the last one.
func (rows *fakeRows) Next() bool {
	rows.index++

	return rows.index <= len(rows.values)
}

// Scan copies the current integer into dest; callers must pass an *int after Next.
func (rows *fakeRows) Scan(dest any) error {
	if rows.scanErr != nil {
		return rows.scanErr
	}

	*dest.(*int) = rows.values[rows.index-1]

	return nil
}

// Err returns the configured iteration failure independently of cursor position.
func (rows *fakeRows) Err() error {
	return rows.err
}

// Close records cursor release so tests can check early termination.
func (rows *fakeRows) Close() error {
	rows.closed = true

	return rows.closeErr
}

// fakeDriver is a Driver whose operations return configured results.
type fakeDriver struct {
	// Driver reserves the full contract while explicit methods supply test results.
	database.Driver

	// err is the failure returned by the test driver.
	err error
	// rows is the cursor returned by successful query calls.
	rows *fakeRows
}

// Exec returns one affected row and the configured operation error.
func (driver fakeDriver) Exec(context.Context, string, ...any) (int64, error) {
	return 1, driver.err
}

// Select returns the configured read error without populating the destination.
func (driver fakeDriver) Select(context.Context, string, any, ...any) error {
	return driver.err
}

// Find returns the configured lookup error without populating the destination.
func (driver fakeDriver) Find(context.Context, string, any, ...any) error {
	return driver.err
}

// Query returns the configured cursor unless an operation error is set.
func (driver fakeDriver) Query(context.Context, string, ...any) (database.Rows, error) {
	if driver.err != nil {
		return nil, driver.err
	}

	return driver.rows, nil
}

// Lock returns a no-op unlock callback and the configured operation error.
func (driver fakeDriver) Lock(context.Context, string) (func(context.Context) error, error) {
	return func(context.Context) error {
		return nil
	}, driver.err
}

// TryLock reports acquisition only when the configured operation error is nil.
func (driver fakeDriver) TryLock(context.Context, string) (func(context.Context) error, bool, error) {
	return func(context.Context) error {
		return nil
	}, driver.err == nil, driver.err
}

// WithTransaction runs fn with the same fake, preserving configured results.
func (driver fakeDriver) WithTransaction(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// WithSession preserves the fake's configured results for scoped operations.
// It overrides the nil embedded interface so session tests need no native pool.
func (driver fakeDriver) WithSession(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// Dialect reports the ANSI dialect.
func (fakeDriver) Dialect() query.Dialect {
	return query.ANSI
}

// dialectedDriver reports the PostgreSQL dialect.
type dialectedDriver struct {
	// fakeDriver supplies operation results while Dialect selects PostgreSQL.
	fakeDriver
}

// Dialect reports PostgreSQL to exercise automatic telemetry system selection.
func (dialectedDriver) Dialect() query.Dialect {
	return query.Postgres
}

// WithSession keeps the overridden dialect visible through callback drivers,
// just as the root reports it for telemetry system resolution.
func (driver dialectedDriver) WithSession(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// newTraced creates an isolated telemetry recorder and applies caller settings
// after the initial tracing policy, allowing tests to override it.
func newTraced(t *testing.T, driver database.Driver, opts ...func(*database.Builder)) (*database.Database, *telemetrytest.Fake) {
	t.Helper()

	fake := telemetrytest.New()

	return database.New(driver, append([]func(*database.Builder){func(connection *database.Builder) {
		connection.Telemetry(fake.Telemetry(t))
	}}, opts...)...), fake
}

// attr reads a string span attribute, preserving absence as a false result.
func attr(t *testing.T, span memorytelemetry.Span, key string) (string, bool) {
	t.Helper()

	value, ok := span.Attr(key)

	if !ok {
		return "", false
	}

	return value.AsString(), true
}

// TestExecSpanAndMetric checks SQL operation attributes and duration measurements together.
func TestExecSpanAndMetric(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, dialectedDriver{}, func(connection *database.Builder) {
		connection.Namespace("app")
		connection.QueryText(true)
	})

	statement := query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 7)
		statement.Delete()
	})

	if _, err := db.Exec(t.Context(), statement); err != nil {
		t.Fatal(err)
	}

	span, ok := fake.Span("DELETE users")

	if !ok {
		t.Fatalf("no DELETE users span in %v", fake.Spans())
	}

	if span.Kind != telemetry.Client {
		t.Errorf("kind = %v, want client", span.Kind)
	}

	want := map[string]string{
		"db.system.name":     "postgresql",
		"db.namespace":       "app",
		"db.operation.name":  "DELETE",
		"db.collection.name": "users",
	}

	for key, value := range want {
		if got, _ := attr(t, span, key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}

	text, _ := attr(t, span, "db.query.text")

	if !strings.Contains(text, "$1") || strings.Contains(text, "7") {
		t.Errorf("query text %q should hold placeholders only", text)
	}

	fake.AssertHistogram(t, "db.client.operation.duration", 1,
		telemetry.String("db.system.name", "postgresql"),
		telemetry.String("db.operation.name", "DELETE"),
		telemetry.String("db.collection.name", "users"),
		telemetry.String("db.namespace", "app"),
		telemetry.String("error.type", ""))
}

// TestQueryTextDisabled verifies statement text is excluded unless explicitly enabled.
func TestQueryTextDisabled(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, fakeDriver{}, func(connection *database.Builder) {
		connection.QueryText(false)
	})

	if _, err := db.Select[int](t.Context(), query.Raw("SELECT 1")); err != nil {
		t.Fatal(err)
	}

	span, ok := fake.Span("SELECT")

	if !ok {
		t.Fatal("no SELECT span")
	}

	if _, found := span.Attr("db.query.text"); found {
		t.Error("db.query.text recorded although disabled")
	}

	if got, _ := attr(t, span, "db.system.name"); got != "other_sql" {
		t.Errorf("system = %q, want other_sql", got)
	}
}

// TestQueryTextDefaultsToPrivate verifies SQL literals are not exported by
// merely enabling telemetry. Bound-argument omission cannot redact raw SQL.
func TestQueryTextDefaultsToPrivate(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, fakeDriver{})

	if _, err := db.Exec(t.Context(), query.Raw("SELECT 'private-customer-value'")); err != nil {
		t.Fatal(err)
	}

	span, ok := fake.Span("SELECT")

	if !ok {
		t.Fatal("missing SELECT span")
	}

	if _, found := span.Attr("db.query.text"); found {
		t.Fatal("default telemetry exported unredacted SQL text")
	}
}

// TestQueryTextOptInRetainsVerbatimSQL documents the explicit trust boundary:
// enabling text preserves literal SQL but still omits separately bound values.
func TestQueryTextOptInRetainsVerbatimSQL(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, fakeDriver{}, func(connection *database.Builder) {
		connection.QueryText(true)
	})

	if _, err := db.Exec(t.Context(), query.Raw("SELECT 'fixed-literal', ?", "private-bound-value")); err != nil {
		t.Fatal(err)
	}

	span, ok := fake.Span("SELECT")

	if !ok {
		t.Fatal("missing SELECT span")
	}

	text, _ := attr(t, span, "db.query.text")

	if text != "SELECT 'fixed-literal', ?" {
		t.Fatalf("query text = %q, want SQL without bound value", text)
	}
}

// TestOperationRecognizesSQLWhitespace verifies the operation label uses SQL
// token boundaries, not only a literal space between verb and selected column.
func TestOperationRecognizesSQLWhitespace(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, fakeDriver{})

	if _, err := db.Exec(t.Context(), query.Raw("\tSeLeCt\n\tid FROM users")); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "SELECT", telemetry.String("db.operation.name", "SELECT"))
}

// TestSystemOverride checks explicit system configuration overrides dialect inference.
func TestSystemOverride(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, dialectedDriver{}, func(connection *database.Builder) {
		connection.System("cockroachdb")
	})

	if _, err := db.Exec(t.Context(), query.Raw("VACUUM")); err != nil {
		t.Fatal(err)
	}

	// An unknown verb leaves the operation out, so the span is named after the system.
	fake.AssertSpan(t, "cockroachdb", telemetry.String("db.system.name", "cockroachdb"))
}

// TestFindNotFoundIsNotAnError verifies an absent row does not mark telemetry as failed.
func TestFindNotFoundIsNotAnError(t *testing.T) {
	t.Parallel()

	for _, notFound := range []error{database.ErrNoRows, sql.ErrNoRows} {
		db, fake := newTraced(t, fakeDriver{err: notFound})

		if _, err := db.Find[int](t.Context(), query.Table("users", func(statement *query.Builder) {
			statement.Select("id")
		})); !errors.Is(err, notFound) {
			t.Fatalf("err = %v, want %v", err, notFound)
		}

		span, _ := fake.Span("SELECT users")

		if span.Status == telemetry.Error {
			t.Errorf("%v marked the span as an error", notFound)
		}

		if _, found := span.Attr("error.type"); found {
			t.Errorf("%v set error.type", notFound)
		}
	}
}

// TestErrorsRecordTypeOnly guards against recording sensitive driver error messages.
func TestErrorsRecordTypeOnly(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, fakeDriver{err: errSecret})

	if _, err := db.Exec(t.Context(), query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]any{"email": "alice@example.com"})
	})); err == nil {
		t.Fatal("expected error")
	}

	span, _ := fake.Span("INSERT users")

	if span.Status != telemetry.Error {
		t.Fatalf("status = %v, want error", span.Status)
	}

	if got, _ := attr(t, span, "error.type"); got != "*errors.errorString" {
		t.Errorf("error.type = %q", got)
	}

	if len(span.Events) != 0 {
		t.Errorf("events = %v, want none (no RecordError)", span.Events)
	}

	if strings.Contains(span.StatusDescription, "alice") {
		t.Errorf("status description leaks error text: %q", span.StatusDescription)
	}

	fake.AssertHistogram(t, "db.client.operation.duration", 1, telemetry.String("error.type", "*errors.errorString"))
}

// TestLockSpan checks lock acquisition emits SQL client telemetry without the lock name.
func TestLockSpan(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, fakeDriver{})

	if _, err := db.Lock(t.Context(), "jobs"); err != nil {
		t.Fatal(err)
	}

	span, ok := fake.Span("LOCK")

	if !ok {
		t.Fatal("no LOCK span")
	}

	for _, key := range []string{"db.query.text", "db.collection.name"} {
		if _, found := span.Attr(key); found {
			t.Errorf("lock span has %s", key)
		}
	}

	if strings.Contains(span.Name, "jobs") {
		t.Error("lock name leaked into span name")
	}

	failing, failingFake := newTraced(t, fakeDriver{err: database.ErrEmptyLockName})

	if _, err := failing.Lock(t.Context(), ""); err == nil {
		t.Fatal("expected error")
	}

	failingFake.AssertSpan(t, "LOCK", telemetry.String("error.type", "database.ErrEmptyLockName"))
}

// TestTryLockSpan checks nonblocking lock acquisition uses its distinct operation label.
func TestTryLockSpan(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, fakeDriver{})

	if _, _, err := db.TryLock(t.Context(), "jobs"); err != nil {
		t.Fatal(err)
	}

	span, ok := fake.Span("TRY_LOCK")

	if !ok {
		t.Fatal("no TRY_LOCK span")
	}

	if _, found := span.Attr("db.query.text"); found {
		t.Error("try lock span has db.query.text")
	}

	if strings.Contains(span.Name, "jobs") {
		t.Error("lock name leaked into span name")
	}

	if _, ok := fake.Span("LOCK"); ok {
		t.Error("TryLock emitted a LOCK span")
	}

	failing, failingFake := newTraced(t, fakeDriver{err: database.ErrEmptyLockName})

	if _, _, err := failing.TryLock(t.Context(), ""); err == nil {
		t.Fatal("expected error")
	}

	failingFake.AssertSpan(t, "TRY_LOCK", telemetry.String("error.type", "database.ErrEmptyLockName"))
}

// TestCursorSpanCoversIteration checks the span remains open while rows are consumed.
func TestCursorSpanCoversIteration(t *testing.T) {
	t.Parallel()

	rows := &fakeRows{values: []int{1, 2}}
	db, fake := newTraced(t, fakeDriver{rows: rows})

	cursor := db.Cursor[int](t.Context(), query.Table("users", func(statement *query.Builder) {
		statement.Select("id")
	}))

	fake.AssertSpanCount(t, 0)

	var got []int

	for value, err := range cursor.Iter() {
		if err != nil {
			t.Fatal(err)
		}

		got = append(got, value)

		fake.AssertSpanCount(t, 0)
	}

	if !slices.Equal(got, []int{1, 2}) || !rows.closed {
		t.Fatalf("got %v closed=%v", got, rows.closed)
	}

	fake.AssertSpan(t, "SELECT users")
	fake.AssertSpanCount(t, 1)
}

// TestCursorSpanEndsWhenAbandonedAndOnError checks early termination releases telemetry state.
func TestCursorSpanEndsWhenAbandonedAndOnError(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, fakeDriver{rows: &fakeRows{values: []int{1, 2, 3}}})

	for range db.Cursor[int](t.Context(), query.Table("users", func(statement *query.Builder) {
		statement.Select("id")
	})).Iter() {
		break
	}

	fake.AssertSpanCount(t, 1)

	failing, failingFake := newTraced(t, fakeDriver{rows: &fakeRows{values: []int{1}, err: errSecret}})

	for range failing.Cursor[int](t.Context(), query.Table("users", func(statement *query.Builder) {
		statement.Select("id")
	})).Iter() {
	}

	failingFake.AssertSpan(t, "SELECT users", telemetry.String("error.type", "*errors.errorString"))
}

// TestCursorReturnsCloseFailure verifies exhausting a cursor reports cleanup
// errors to the caller and marks the same public operation's telemetry failed.
func TestCursorReturnsCloseFailure(t *testing.T) {
	t.Parallel()

	rows := &fakeRows{values: []int{1}, closeErr: errSecret}
	db, fake := newTraced(t, fakeDriver{rows: rows})

	_, err := db.Cursor[int](t.Context(), query.Raw("SELECT id FROM users")).Items()

	if !errors.Is(err, errSecret) {
		t.Fatalf("Cursor = %v, want close failure", err)
	}

	fake.AssertSpan(t, "SELECT", telemetry.String("error.type", "*errors.errorString"))
}

// TestCursorJoinsScanAndCloseFailures checks cleanup cannot replace or hide
// the row-decoding cause when the cursor ends before exhaustion.
func TestCursorJoinsScanAndCloseFailures(t *testing.T) {
	t.Parallel()

	cleanup := errors.New("cursor close failed")
	rows := &fakeRows{values: []int{1}, scanErr: errSecret, closeErr: cleanup}
	db := database.New(fakeDriver{rows: rows})

	_, err := db.Cursor[int](t.Context(), query.Raw("SELECT id FROM users")).Items()

	if !errors.Is(err, errSecret) || !errors.Is(err, cleanup) {
		t.Fatalf("Cursor = %v, want scan and close failures", err)
	}
}

// TestAbandonedCursorRecordsCloseFailure verifies cleanup is still attempted
// after yield returns false. The iterator must not call that consumer again.
func TestAbandonedCursorRecordsCloseFailure(t *testing.T) {
	t.Parallel()

	rows := &fakeRows{values: []int{1, 2}, closeErr: errSecret}
	db, fake := newTraced(t, fakeDriver{rows: rows})

	for range db.Cursor[int](t.Context(), query.Raw("SELECT id FROM users")).Iter() {
		break
	}

	if !rows.closed {
		t.Fatal("abandoned cursor was not closed")
	}

	fake.AssertSpan(t, "SELECT", telemetry.String("error.type", "*errors.errorString"))
}

// TestBuildErrorsAreNotTraced checks invalid declarations do not create SQL client spans.
func TestBuildErrorsAreNotTraced(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, fakeDriver{})

	if _, err := db.Select[int](t.Context(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.Delete()
	})); err == nil {
		t.Fatal("expected build error")
	}

	fake.AssertSpanCount(t, 0)
}

// TestTransactionInheritsTelemetry checks scoped operations retain the facade's recorder.
func TestTransactionInheritsTelemetry(t *testing.T) {
	t.Parallel()

	db, fake := newTraced(t, dialectedDriver{}, func(connection *database.Builder) {
		connection.Namespace("app")
	})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		_, err := db.Exec(ctx, query.Table("users", func(statement *query.Builder) {
			statement.Where("id", 1)
			statement.Delete()
		}))

		return err
	})

	if err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "DELETE users",
		telemetry.String("db.system.name", "postgresql"), telemetry.String("db.namespace", "app"))
}

// TestDefaultResolvedLazily checks operations observe telemetry installed after construction.
func TestDefaultResolvedLazily(t *testing.T) {
	db := database.New(fakeDriver{})
	fake := telemetrytest.New()

	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	if _, err := db.Exec(t.Context(), query.Table("users", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.Delete()
	})); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "DELETE users")
}

// TestDisabledTelemetryAllocatesNothing compares execution with an uninstrumented baseline.
func TestDisabledTelemetryAllocatesNothing(t *testing.T) {
	statement := query.Raw("SELECT 1")
	var statementer query.Statementer = statement
	plain := database.New(fakeDriver{}, func(connection *database.Builder) {
		connection.Telemetry(telemetry.New(nil))
	})

	ctx := context.Background()

	traced := testing.AllocsPerRun(100, func() {
		_, _ = plain.Exec(ctx, statementer)
	})

	// Compiling the statement and calling the driver is the uninstrumented
	// baseline.
	baseline := testing.AllocsPerRun(100, func() {
		built, _ := statement.BuildFor(query.ANSI)
		_, _ = fakeDriver{}.Exec(ctx, built.SQL(), built.Args()...)
	})

	if traced != baseline {
		t.Errorf("disabled telemetry changed allocations per Exec from %v to %v", baseline, traced)
	}
}

// BenchmarkExecNoTelemetry measures facade execution with instrumentation disabled.
func BenchmarkExecNoTelemetry(b *testing.B) {
	db := database.New(fakeDriver{}, func(connection *database.Builder) {
		connection.Telemetry(telemetry.New(nil))
	})

	var statement query.Statementer = query.Raw("SELECT 1")
	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		_, _ = db.Exec(ctx, statement)
	}
}

// BenchmarkExecDriverBaseline measures direct execution for comparison with facade overhead.
func BenchmarkExecDriverBaseline(b *testing.B) {
	driver := fakeDriver{}
	statement := query.Raw("SELECT 1")
	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		built, _ := statement.BuildFor(query.ANSI)
		_, _ = driver.Exec(ctx, built.SQL(), built.Args()...)
	}
}

// TestDatabasePoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestDatabasePoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *database.Builder
	calls := 0
	component := database.New(adapterStub{}, func(connection *database.Builder) {
		calls++
		retained = connection
	}, func(connection *database.Builder) {
		if calls != 1 || retained != connection {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	database.New(adapterStub{}, func(connection *database.Builder) {
		if retained == connection {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestDatabaseNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestDatabaseNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*database.Builder)
	empty := []func(*database.Builder){}

	if database.New(adapterStub{}, absent...) == nil || database.New(adapterStub{}, empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestDatabaseNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestDatabaseNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		database.New(adapterStub{}, nil, func(connection *database.Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestDatabasePolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestDatabasePolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		database.New(adapterStub{}, func(connection *database.Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestDatabaseTelemetryReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestDatabaseTelemetryReturnsDraft(t *testing.T) {
	t.Parallel()

	connection := &database.Builder{}

	if connection.Telemetry(nil) != connection {
		t.Fatal("setting returned a different draft")
	}
}

// TestDatabaseSystemReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestDatabaseSystemReturnsDraft(t *testing.T) {
	t.Parallel()

	connection := &database.Builder{}

	if connection.System("custom") != connection {
		t.Fatal("setting returned a different draft")
	}
}

// TestDatabaseNamespaceReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestDatabaseNamespaceReturnsDraft(t *testing.T) {
	t.Parallel()

	connection := &database.Builder{}

	if connection.Namespace("billing") != connection {
		t.Fatal("setting returned a different draft")
	}
}

// TestDatabaseQueryTextReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestDatabaseQueryTextReturnsDraft(t *testing.T) {
	t.Parallel()

	connection := &database.Builder{}

	if connection.QueryText(false) != connection {
		t.Fatal("setting returned a different draft")
	}
}

// TestDatabaseCursorCodecReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestDatabaseCursorCodecReturnsDraft(t *testing.T) {
	t.Parallel()

	connection := &database.Builder{}

	if connection.CursorCodec(nil) != connection {
		t.Fatal("setting returned a different draft")
	}
}

// TestDatabaseEventsReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestDatabaseEventsReturnsDraft(t *testing.T) {
	t.Parallel()

	connection := &database.Builder{}

	if connection.Events(nil) != connection {
		t.Fatal("setting returned a different draft")
	}
}
