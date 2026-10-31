package postgresdatabase_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"arkoren.dev/core/service/database"
	postgres "arkoren.dev/driver/postgres/postgresdatabase"
	"arkoren.dev/foundation/query"
)

// newProtocolDriver transfers a real pgxpool pool to NewFrom. Its public native
// dial hook talks only to an in-process PostgreSQL wire fixture, not a service.
// Simple protocol keeps the fixture focused on row ownership and result shape.
func newProtocolDriver(t *testing.T) *postgres.Driver {
	t.Helper()

	config, err := pgxpool.ParseConfig("postgres://fixture@localhost/db?sslmode=disable&default_query_exec_mode=simple_protocol")

	if err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup

	config.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()

		group.Go(func() {
			defer server.Close()

			if err := servePostgres(server); err != nil {
				t.Errorf("PostgreSQL fixture: %v", err)
			}
		})

		return client, nil
	}

	pool, err := pgxpool.NewWithConfig(t.Context(), config)

	if err != nil {
		t.Fatal(err)
	}

	driver := postgres.NewFrom(pool)
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}

		group.Wait()
	})

	return driver
}

// servePostgres implements startup and simple queries using pgx's public wire
// codec. Each query returns one integer unless its SQL requests an empty result.
// Unexpected frontend messages fail rather than silently approximating pgx.
func servePostgres(conn net.Conn) error {
	backend := pgproto3.NewBackend(conn, conn)

	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return err
	}

	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})

	if err := backend.Flush(); err != nil {
		return err
	}

	for {
		message, err := backend.Receive()

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		if _, ok := message.(*pgproto3.Terminate); ok {
			return nil
		}

		request, ok := message.(*pgproto3.Query)

		if !ok {
			return fmt.Errorf("unexpected frontend message %T", message)
		}

		if err := sendPostgresRows(backend, request.String); err != nil {
			return err
		}
	}
}

// sendPostgresRows produces text-format int4 rows and a complete command result.
// The connection stays readable until Terminate so real pgx cleanup can finish.
func sendPostgresRows(backend *pgproto3.Backend, statement string) error {
	backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{
		{Name: []byte("value"), DataTypeOID: 23, DataTypeSize: 4},
	}})

	if !strings.Contains(statement, "false") {
		backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("7")}})
	}

	backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})

	return backend.Flush()
}

// TestSelectClearsExistingDestination checks the core empty-result promise at
// NewFrom's public native seam, including the real pgx row adapter and pool.
func TestSelectClearsExistingDestination(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t)
	values := []int{99}

	if err := driver.Select(t.Context(), "SELECT 7 WHERE false", &values); err != nil {
		t.Fatal(err)
	}

	if len(values) != 0 {
		t.Fatalf("Select empty result = %v, want empty", values)
	}
}

// TestSelectReplacesExistingDestination prevents a reused slice from retaining
// rows from an earlier operation when a nonempty native result is scanned.
func TestSelectReplacesExistingDestination(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t)
	values := []int{99}

	if err := driver.Select(t.Context(), "SELECT 7", &values); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(values, []int{7}) {
		t.Fatalf("Select = %v, want [7]", values)
	}
}

// TestCanceledSessionRejectsFreshOperationContext checks cancellation belongs
// to the callback scope, not only to individual SQL contexts. A fresh context
// cannot resume work on a retained session after its owning scope was canceled.
func TestCanceledSessionRejectsFreshOperationContext(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	err := driver.WithSession(ctx, func(session database.Driver) error {
		cancel()

		var value int

		if err := session.Find(context.Background(), "SELECT 7", &value); !errors.Is(err, context.Canceled) {
			t.Errorf("fresh-context Find = %v, want context.Canceled", err)
		}

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithSession = %v, want context.Canceled", err)
	}
}

// TestSelectReplacesPointerDestination preserves support for pointer elements
// while ensuring old pointers do not survive a successful replacement query.
func TestSelectReplacesPointerDestination(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t)
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

// TestPostgresSelectReusesDestination checks replacement against an actual
// server, including empty results and a one-slot retained-session pool. The
// protocol fixture separately makes this regression runnable without services.
func TestPostgresSelectReusesDestination(t *testing.T) {
	t.Parallel()

	driver := boundedDriver(t, 1)
	ctx := postgresTestContext(t)
	values := []int{99}

	err := driver.WithSession(ctx, func(session database.Driver) error {
		if err := session.Select(ctx, "SELECT 7", &values); err != nil {
			return err
		}

		if !slices.Equal(values, []int{7}) {
			t.Fatalf("Select = %v, want [7]", values)
		}

		return session.Select(ctx, "SELECT 7 WHERE false", &values)
	})

	if err != nil || len(values) != 0 {
		t.Fatalf("empty Select = %v, %v; want empty", values, err)
	}
}

// TestPostgresCanceledSessionCannotResume verifies a fresh context cannot run
// SQL on a callback canceled after acquiring its actual physical connection.
func TestPostgresCanceledSessionCannotResume(t *testing.T) {
	t.Parallel()

	driver := boundedDriver(t, 1)
	ctx, cancel := context.WithCancel(postgresTestContext(t))
	t.Cleanup(cancel)

	err := driver.WithSession(ctx, func(session database.Driver) error {
		cancel()

		var value int

		if err := session.Find(context.Background(), "SELECT 7", &value); !errors.Is(err, context.Canceled) {
			t.Errorf("fresh-context Find = %v, want context.Canceled", err)
		}

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithSession = %v, want context.Canceled", err)
	}
}

// Audit is embedded by value into profile and flattened into its columns.
type Audit struct {
	// CreatedAt is when the row was created.
	CreatedAt time.Time `db:"created_at"`

	// Revision counts the updates.
	Revision int `db:"revision"`
}

// profile covers the field rules: tags with options, untagged names, embedded
// structs, skipped fields, JSON values, and nullable pointers.
type profile struct {
	Audit

	// ID is the primary key.
	ID int `db:"id,omitempty"`

	// DisplayName is untagged and maps to the column displayname.
	DisplayName string

	// Settings is stored as JSON.
	Settings database.JSON[map[string]bool] `db:"settings"`

	// Nickname is nullable.
	Nickname *string `db:"nickname"`

	// Cache is never stored.
	Cache string `db:"-"`
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
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	driver, connection := newPostgresDriver(t)
	registerPostgresConnectionCleanup(t, connection)

	ctx := postgresTestContext(t)
	table := postgresTableName(t)

	_, err := driver.Exec(ctx, "CREATE TABLE "+table+" (id INT PRIMARY KEY, displayname VARCHAR(255) NOT NULL, "+
		"settings jsonb NOT NULL, nickname VARCHAR(255), created_at timestamptz NOT NULL, revision INT NOT NULL)")

	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	registerPostgresTableCleanup(t, driver, table)

	databaseClient := database.New(driver)
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

		if _, err := databaseClient.Exec(ctx, query.Table(table, func(statement *query.Builder) {
			statement.Insert(query.Values(row))
		})); err != nil {
			t.Fatalf("insert %d: %v", row.ID, err)
		}
	}

	// Cache is skipped on write, so it reads back empty.
	selected, err := databaseClient.Select[profile](ctx, query.Table(table, func(statement *query.Builder) {
		statement.OrderBy("id")
	}))

	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}

	if got := inUTC(selected.Items()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Select() = %#v, want %#v", got, want)
	}

	found, err := databaseClient.Find[profile](ctx, query.Table(table, func(statement *query.Builder) {
		statement.Where("id", 1)
	}))

	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}

	if got := inUTC([]profile{found})[0]; !reflect.DeepEqual(got, want[0]) {
		t.Fatalf("Find() = %#v, want %#v", got, want[0])
	}

	streamed, err := databaseClient.Cursor[profile](ctx, query.Table(table, func(statement *query.Builder) {
		statement.OrderBy("id")
	})).Items()

	if err != nil {
		t.Fatalf("Cursor() error = %v", err)
	}

	if !reflect.DeepEqual(inUTC(streamed), want) {
		t.Fatalf("Cursor() = %#v, want %#v", streamed, want)
	}
}
