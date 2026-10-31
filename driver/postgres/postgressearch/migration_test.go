package postgressearch_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/core/service/search"
	"arkoren.dev/driver/postgres/postgresdatabase"
	"arkoren.dev/driver/postgres/postgressearch"
	"arkoren.dev/foundation/query"
)

// TestPingBeforeMigratingNamesMigrations checks readiness before table creation.
// Both readiness entry points must identify Migrations as the required remedy.
func TestPingBeforeMigratingNamesMigrations(t *testing.T) {
	db := integration(t)

	t.Parallel()

	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance(uniqueInstance())
	})

	if err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]func(context.Context) error{"Ping": driver.Ping, "Start": driver.Start} {
		err := call(t.Context())

		if !errors.Is(err, search.ErrNotFound) || !strings.Contains(err.Error(), "Migrations()") {
			t.Errorf("%s error = %v, want ErrNotFound naming Migrations()", name, err)
		}
	}
}

// TestMigrationsCreateAndDropTheTables exercises both directions of driver DDL.
// Readiness must succeed after Up and report missing tables after Rollback.
func TestMigrationsCreateAndDropTheTables(t *testing.T) {
	db := integration(t)

	t.Parallel()

	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance(uniqueInstance())
	})

	if err != nil {
		t.Fatal(err)
	}

	migrator := newMigrator(t, db, driver)

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}

	if err := driver.Ping(t.Context()); err != nil {
		t.Fatalf("Ping after Up: %v", err)
	}

	if err := migrator.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if err := driver.Ping(t.Context()); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Ping after Rollback error = %v, want ErrNotFound", err)
	}
}

// TestDigitLeadingInstanceMigrationsRoundTrip runs both shipped migration
// directions through the public migrator on PostgreSQL. Readiness names both
// missing tables after rollback; a second cycle also catches leftover objects.
func TestDigitLeadingInstanceMigrationsRoundTrip(t *testing.T) {
	db := integration(t)

	t.Parallel()

	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance("0_" + uniqueInstance())
	})

	if err != nil {
		t.Fatal(err)
	}

	migrator := newMigrator(t, db, driver)
	t.Cleanup(func() {
		if err := migrator.Rollback(context.Background()); err != nil {
			t.Errorf("cleanup Rollback: %v", err)
		}
	})

	for range 2 {
		if err := migrator.Up(t.Context()); err != nil {
			t.Fatalf("Up: %v", err)
		}

		if err := driver.Ping(t.Context()); err != nil {
			t.Fatalf("Ping after Up: %v", err)
		}

		if err := migrator.Rollback(t.Context()); err != nil {
			t.Fatalf("Rollback: %v", err)
		}

		err := driver.Ping(t.Context())

		if !errors.Is(err, search.ErrNotFound) {
			t.Fatalf("Ping after Rollback = %v, want ErrNotFound", err)
		}

		if !strings.Contains(err.Error(), "search_documents") || !strings.Contains(err.Error(), "search_indexes") {
			t.Fatalf("Ping after Rollback = %v, want both tables missing", err)
		}
	}
}

// TestPingNamesOnlyTheMissingTable simulates a partially applied deployment.
// Its readiness error must identify the absent table without naming the survivor.
func TestPingNamesOnlyTheMissingTable(t *testing.T) {
	db := integration(t)

	t.Parallel()

	instance := uniqueInstance()

	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance(instance)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := newMigrator(t, db, driver).Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}

	t.Cleanup(func() {
		if _, err := db.Exec(context.Background(), query.Raw("DROP TABLE IF EXISTS "+instance+"_search_documents")); err != nil {
			t.Errorf("drop documents table: %v", err)
		}
	})

	// Dropping one table by hand simulates a half-applied deployment.
	if _, err := db.Exec(t.Context(), query.Raw("DROP TABLE "+instance+"_search_indexes")); err != nil {
		t.Fatal(err)
	}

	err = driver.Ping(t.Context())

	if !errors.Is(err, search.ErrNotFound) || !strings.Contains(err.Error(), instance+"_search_indexes") ||
		strings.Contains(err.Error(), "search_documents") {
		t.Fatalf("Ping error = %v, want ErrNotFound naming only the indexes table", err)
	}
}

// TestInstancesLiveSideBySide checks instance-scoped tables and migration groups.
// Synchronizing an index in one instance must not create it in another instance.
func TestInstancesLiveSideBySide(t *testing.T) {
	db := integration(t)

	t.Parallel()

	first, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance(uniqueInstance())
	})

	if err != nil {
		t.Fatal(err)
	}

	second, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance(uniqueInstance())
	})

	if err != nil {
		t.Fatal(err)
	}

	migrator := newMigrator(t, db, first, second)

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatalf("Up: %v", err)
	}

	t.Cleanup(func() {
		if err := migrator.Rollback(context.Background()); err != nil {
			t.Errorf("Rollback: %v", err)
		}
	})

	schema := search.Schema{
		Name:       "shared_name",
		Searchable: []string{"title"},
	}

	if err := first.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatalf("Sync first: %v", err)
	}

	if err := first.Put(t.Context(), schema, []search.Document{{
		ID:     "a",
		Source: []byte(`{"title":"alpha"}`),
	}}); err != nil {
		t.Fatalf("Put first: %v", err)
	}

	if err := second.Check(t.Context(), []search.Schema{schema}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Check on the second instance error = %v, want ErrNotFound", err)
	}

	if err := second.Put(t.Context(), schema, []search.Document{{
		ID:     "a",
		Source: []byte(`{"title":"beta"}`),
	}}); !errors.Is(err, search.ErrNotFound) {
		t.Fatalf("Put on the second instance error = %v, want ErrNotFound", err)
	}
}

// TestDigitLeadingInstanceEmitsValidSQL drives construction, migrations, and
// search operations through the public native database adapter. Its local wire
// peer rejects numeric unquoted identifiers; it does not implement PostgreSQL
// storage or claim integration coverage of full-text or transaction semantics.
func TestDigitLeadingInstanceEmitsValidSQL(t *testing.T) {
	t.Parallel()

	db := identifierDatabase(t)

	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance("0")
	})

	if err != nil {
		t.Fatal(err)
	}

	migrator := migrate.New(db)

	if err := migrator.Add(driver.Migrations()...); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatalf("migrate digit-leading instance: %v", err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("readiness: %v", err)
	}

	schema := search.Schema{Name: "products", Searchable: []string{"title"}}

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if err := driver.Put(t.Context(), schema, []search.Document{{ID: "p1", Source: []byte(`{"title":"red shoes"}`)}}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, err := driver.Search(t.Context(), schema, search.Spec{}, 10, 0); err != nil {
		t.Fatalf("Search: %v", err)
	}

	if err := driver.Delete(t.Context(), schema, []string{"p1"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if err := driver.Drop(t.Context(), schema); err != nil {
		t.Fatalf("Drop: %v", err)
	}
}

// TestDigitLeadingInstanceStoresDocuments verifies the same identifier boundary
// on a real server, including DDL, regclass readiness, definitions, and document
// reads. It is deliberately skipped by short/local protocol-only validation.
func TestDigitLeadingInstanceStoresDocuments(t *testing.T) {
	db := integration(t)

	t.Parallel()

	driver, err := postgressearch.NewFrom(db, func(engine *postgressearch.Builder) {
		engine.Instance("0_" + uniqueInstance())
	})

	if err != nil {
		t.Fatal(err)
	}

	migrator := newMigrator(t, db, driver)

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := migrator.Rollback(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}

	schema := search.Schema{Name: "products", Searchable: []string{"title"}}

	if err := driver.Sync(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Put(t.Context(), schema, []search.Document{{ID: "p1", Source: []byte(`{"title":"red shoes"}`)}}); err != nil {
		t.Fatal(err)
	}

	matches, err := driver.Search(t.Context(), schema, search.Spec{}, 10, 0)

	if err != nil {
		t.Fatal(err)
	}

	if len(matches.Items) != 1 {
		t.Fatalf("Search = %+v, want the indexed document", matches)
	}

	if err := driver.Check(t.Context(), []search.Schema{schema}); err != nil {
		t.Fatal(err)
	}
}

// identifierDatabase supplies a real pgxpool and public SQL driver over a pipe.
// Simple protocol keeps the peer small while retaining native SQL argument
// encoding. Cleanup closes the owned pool, then joins the protocol peer.
func identifierDatabase(t *testing.T) *database.Database {
	t.Helper()

	connection, peer := net.Pipe()

	config, err := pgxpool.ParseConfig("postgres://fixture@127.0.0.1:1/fixture?sslmode=disable")

	if err != nil {
		t.Fatal(err)
	}

	config.MaxConns = 1
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}

	done := make(chan error, 1)

	go func() {
		done <- serveIdentifierSQL(peer)
	}()

	pool, err := pgxpool.NewWithConfig(t.Context(), config)

	if err != nil {
		t.Fatal(err)
	}

	owner := postgresdatabase.NewFrom(pool)
	t.Cleanup(func() {
		if err := owner.Stop(context.Background()); err != nil {
			t.Error(err)
		}

		_ = connection.Close() // Release a peer awaiting the next frontend message.
		_ = peer.Close()       // The fixture owns both pipe ends.

		if err := <-done; err != nil {
			t.Errorf("PostgreSQL peer: %v", err)
		}
	})

	return database.New(owner)
}

// serveIdentifierSQL handles native startup and simple-query messages only. It
// validates identifier tokenization at the wire boundary, rather than inspecting
// driver internals or source. Real server semantics remain integration work.
func serveIdentifierSQL(peer net.Conn) error {
	backend := pgproto3.NewBackend(peer, peer)

	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return err
	}

	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "16.0"})
	backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})

	if err := backend.Flush(); err != nil {
		return err
	}

	invalid := regexp.MustCompile(`(?i)\b(?:table|index|into|from|update)\s+[0-9]`)

	for {
		message, err := backend.Receive()

		if err != nil {
			return identifierPeerClosed(err)
		}

		if _, done := message.(*pgproto3.Terminate); done {
			return nil
		}

		statement, ok := message.(*pgproto3.Query)

		if !ok {
			return fmt.Errorf("unexpected frontend message %T", message)
		}

		identifierSQLReply(backend, statement.String, invalid)
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})

		if err := backend.Flush(); err != nil {
			return identifierPeerClosed(err)
		}
	}
}

// identifierSQLReply models only the result shapes needed by public migrations
// and search calls. Numeric unquoted identifiers and invalid regclass inputs
// fail with PostgreSQL's syntax-error class before any synthetic success reply.
func identifierSQLReply(backend *pgproto3.Backend, statement string, invalid *regexp.Regexp) {
	regclassInvalid := strings.Contains(statement, "to_regclass(name)") && strings.Contains(statement, "'0_search_")

	if invalid.MatchString(statement) || regclassInvalid {
		backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42601", Message: "unquoted digit-leading identifier"})

		return
	}

	upper := strings.ToUpper(strings.TrimSpace(statement))

	if strings.HasPrefix(upper, "INSERT") {
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("INSERT 0 1")})

		return
	}

	if !strings.HasPrefix(upper, "SELECT") {
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(strings.Fields(upper)[0])})

		return
	}

	if strings.Contains(upper, "EXISTS") || strings.Contains(upper, "ADVISORY") {
		backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 16}}})
		backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("t")}})
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})

		return
	}

	if strings.Contains(upper, "COUNT(*)") {
		backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("count"), DataTypeOID: 20}}})
		backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("0")}})
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})

		return
	}

	backend.Send(&pgproto3.RowDescription{})
	backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 0")})
}

// identifierPeerClosed accepts ordinary fixture shutdown; malformed messages or
// unexpected protocol failures remain visible to the owning test.
func identifierPeerClosed(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return nil
	}

	return err
}
