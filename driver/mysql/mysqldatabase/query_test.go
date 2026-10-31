package mysqldatabase_test

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sqlmysql "github.com/go-sql-driver/mysql"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	mysql "arkoren.dev/driver/mysql/mysqldatabase"
	"arkoren.dev/foundation/query"
)

// newProtocolDriver uses the native driver's registered dial seam to connect
// New to an in-process wire fixture. Each test gets its own network name; no
// credentials or SQL reach a real service. Cleanup joins all fixture goroutines.
func newProtocolDriver(t *testing.T, dsn string, observe func(string)) *mysql.Driver {
	t.Helper()

	network := t.Name()
	var group sync.WaitGroup

	sqlmysql.RegisterDialContext(network, func(context.Context, string) (net.Conn, error) {
		client, server := net.Pipe()

		group.Go(func() {
			defer server.Close()

			if err := serveMySQL(server, observe); err != nil {
				t.Errorf("MySQL fixture: %v", err)
			}
		})

		return client, nil
	})

	t.Cleanup(func() {
		sqlmysql.DeregisterDialContext(network)
		group.Wait()
	})

	driver, err := mysql.New(strings.Replace(dsn, "@fixture(", "@"+network+"(", 1))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return driver
}

// serveMySQL supplies a protocol-4.1 handshake and text-query results. The
// fixture intentionally omits prepared statements and TLS, which these tests
// do not request. Authentication is accepted without inspecting secret bytes.
func serveMySQL(conn net.Conn, observe func(string)) error {
	if err := handshakeMySQL(conn); err != nil {
		return err
	}

	for {
		packet, err := readMySQLPacket(conn)

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		if len(packet) == 0 {
			return errors.New("empty command packet")
		}

		if packet[0] == 1 { // COM_QUIT ends the native connection.
			return nil
		}

		if observe != nil {
			observe(string(packet[1:]))
		}

		if err := sendMySQLRows(conn, string(packet[1:])); err != nil {
			return err
		}
	}
}

// handshakeMySQL negotiates protocol 4.1 and accepts native-password auth.
// Keeping authentication separate from commands mirrors the actual wire phases
// while avoiding inspection or storage of supplied fixture credentials.
func handshakeMySQL(conn net.Conn) error {
	greeting := append([]byte{10}, []byte("8.0.0\x00")...)
	greeting = append(greeting, 1, 0, 0, 0)
	greeting = append(greeting, []byte("12345678\x00")...)
	greeting = append(greeting, 0x01, 0x82, 45, 2, 0, 0x08, 0, 21)
	greeting = append(greeting, make([]byte, 10)...)
	greeting = append(greeting, []byte("123456789012\x00mysql_native_password\x00")...)

	if err := writeMySQLPacket(conn, 0, greeting); err != nil {
		return err
	}

	if _, err := readMySQLPacket(conn); err != nil {
		return err
	}

	return writeMySQLPacket(conn, 2, []byte{0, 0, 0, 2, 0, 0, 0})
}

// writeMySQLPacket writes one small uncompressed packet with its sequence ID.
// Fixtures stay below 256 bytes so the higher payload-length bytes are zero.
func writeMySQLPacket(conn net.Conn, sequence byte, payload []byte) error {
	packet := append([]byte{byte(len(payload)), 0, 0, sequence}, payload...)
	_, err := conn.Write(packet)

	return err
}

// readMySQLPacket consumes a complete native packet, excluding its header.
// Full reads keep net.Pipe fragmentation from changing the fixture's behavior.
func readMySQLPacket(conn net.Conn) ([]byte, error) {
	header := make([]byte, 4)

	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}

	length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	payload := make([]byte, length)
	_, err := io.ReadFull(conn, payload)

	return payload, err
}

// sendMySQLRows returns a single integer or DATETIME column, or an empty result
// for WHERE false. Metadata uses native MySQL type codes, so the pinned driver's
// actual parseTime policy determines whether DATETIME becomes time.Time.
func sendMySQLRows(conn net.Conn, statement string) error {
	if !strings.HasPrefix(statement, "SELECT ") {
		return writeMySQLPacket(conn, 1, []byte{0, 0, 0, 2, 0, 0, 0})
	}

	columnType, value := byte(3), "7"

	if strings.Contains(statement, "DATETIME") {
		columnType, value = 12, "2026-10-30 12:34:56"
	}

	column := []byte{3, 'd', 'e', 'f', 0, 0, 0, 5, 'v', 'a', 'l', 'u', 'e', 0}
	column = append(column, 12, 45, 0, 64, 0, 0, 0, columnType, 0, 0, 0, 0, 0)
	eof := []byte{0xfe, 0, 0, 2, 0}
	packets := [][]byte{{1}, column, eof}

	if !strings.Contains(statement, "false") {
		packets = append(packets, append([]byte{byte(len(value))}, value...))
	}

	packets = append(packets, eof)

	for index, packet := range packets {
		if err := writeMySQLPacket(conn, byte(index+1), packet); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
	}

	return nil
}

// TestSelectClearsExistingDestination checks empty-result semantics through New,
// the real native MySQL parser, wire decoder, and database/sql row ownership.
func TestSelectClearsExistingDestination(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t, "user@fixture(fixture)/db", nil)
	values := []int{99}

	if err := driver.Select(t.Context(), "SELECT 7 WHERE false", &values); err != nil {
		t.Fatal(err)
	}

	if len(values) != 0 {
		t.Fatalf("Select empty result = %v, want empty", values)
	}
}

// TestSelectReplacesExistingDestination prevents reused caller slices from
// mixing an earlier result with rows returned by the current native query.
func TestSelectReplacesExistingDestination(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t, "user@fixture(fixture)/db", nil)
	values := []int{99}

	if err := driver.Select(t.Context(), "SELECT 7", &values); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(values, []int{7}) {
		t.Fatalf("Select = %v, want [7]", values)
	}
}

// TestSelectReplacesPointerDestination keeps pointer-element scanning intact
// while dropping the old result without mutating its pointed-to values.
func TestSelectReplacesPointerDestination(t *testing.T) {
	t.Parallel()

	driver := newProtocolDriver(t, "user@fixture(fixture)/db", nil)
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

// TestMySQLSelectReusesDestination exercises actual server results through a
// retained one-slot session. Unit coverage uses the native wire parser too, but
// cannot establish full server interoperability by itself.
func TestMySQLSelectReusesDestination(t *testing.T) {
	t.Parallel()

	driver := boundedMySQL(t, 1)
	ctx := testContext(t)
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

// TestMySQLParseTimeDefaultWithPasswordDelimiters provisions one disposable
// account whose password includes both former policy-confusing substrings. It
// requires the integration service's administrative role; no existing account is
// changed, and cleanup drops only this uniquely named account.
func TestMySQLParseTimeDefaultWithPasswordDelimiters(t *testing.T) {
	t.Parallel()

	admin := boundedMySQL(t, 1)
	ctx := testContext(t)
	name := uniqueName(t, "timestamp")

	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY 'why?parseTime=secret'", name)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanup, cancel := cleanupContext()
		defer cancel()

		if _, err := admin.Exec(cleanup, fmt.Sprintf("DROP USER '%s'@'%%'", name)); err != nil {
			t.Error(err)
		}
	})

	config, err := sqlmysql.ParseDSN(integrationDSN(t))

	if err != nil {
		t.Fatal(err)
	}

	config.User, config.Passwd, config.DBName = name, "why?parseTime=secret", ""
	config.ParseTime = false // FormatDSN omits false, so New must apply its default.

	driver, err := mysql.New(config.FormatDSN())

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	var value time.Time

	if err := driver.Find(ctx, "SELECT CAST('2026-10-30 12:34:56' AS DATETIME)", &value); err != nil {
		t.Fatal(err)
	}

	if value.Year() != 2026 {
		t.Fatalf("DATETIME = %v, want parsed time", value)
	}
}

// TestMySQLGoexitDoesNotCommit verifies the native wire regression against an
// actual server. Only this test's temporary table is changed, and its transaction
// must roll back even though runtime.Goexit does not return a callback error.
func TestMySQLGoexitDoesNotCommit(t *testing.T) {
	t.Parallel()

	driver := boundedMySQL(t, 1)
	ctx := testContext(t)
	table := uniqueName(t, "goexit")

	if _, err := driver.Exec(ctx, "CREATE TABLE "+table+" (value INT)"); err != nil {
		t.Fatal(err)
	}

	dropTables(t, database.New(driver), table)
	var group sync.WaitGroup

	group.Go(func() {
		_ = driver.WithTransaction(ctx, func(tx database.Driver) error {
			if _, err := tx.Exec(ctx, "INSERT INTO "+table+" VALUES (7)"); err != nil {
				t.Error(err)

				return err
			}

			runtime.Goexit()

			return nil
		})
	})
	group.Wait()

	var count int

	if err := driver.Find(ctx, "SELECT COUNT(*) FROM "+table, &count); err != nil || count != 0 {
		t.Fatalf("interrupted transaction rows = %d, %v; want 0", count, err)
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

	driver, connection := newMySQLDriver(t)
	registerMySQLConnectionCleanup(t, connection)

	ctx := testContext(t)
	table := tableName(t)

	_, err := driver.Exec(ctx, "CREATE TABLE "+table+" (id INT PRIMARY KEY, displayname VARCHAR(255) NOT NULL, "+
		"settings JSON NOT NULL, nickname VARCHAR(255), created_at DATETIME(6) NOT NULL, revision INT NOT NULL)")

	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	registerMySQLTableCleanup(t, driver, table)

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

// ledgerTuple is a byte-observable history snapshot. Timestamp text retains all
// six MySQL fractional digits so conversion checks cannot hide history changes.
type ledgerTuple struct {
	// IDHex contains every stored ID byte, independent of connection charset.
	IDHex string `db:"id_hex"`
	// Batch is the original application batch, not a recomputed sequence.
	Batch int `db:"batch"`
	// RanAt preserves the database timestamp representation for comparisons.
	RanAt string `db:"ran_at"`
}

// ledgerColumnState reads the actual id declaration from MySQL's dictionary.
// It is independent of the DDL requested by the migrator under test.
type ledgerColumnState struct {
	// Type is the actual storage type, such as varbinary rather than varchar.
	Type string `db:"data_type"`
	// Bytes is the declared byte capacity, not a character count.
	Bytes int64 `db:"bytes"`
	// Collation is NULL for binary columns.
	Collation *string `db:"collation_name"`
	// Nullable is NO for canonical ledger IDs.
	Nullable string `db:"is_nullable"`
}

// createLedgerSchema provisions only a uniquely named database in the current
// disposable integration server. Existing project databases are never altered.
func createLedgerSchema(t *testing.T, db *database.Database) string {
	t.Helper()

	name := uniqueName(t, "ledger_db")
	quoted := query.MySQL.Quote(name)

	if _, err := db.Exec(t.Context(), query.Raw("CREATE DATABASE "+quoted+
		" CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci")); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()

		if _, err := db.Exec(ctx, query.Raw("DROP DATABASE "+quoted)); err != nil {
			t.Errorf("drop disposable ledger schema: %v", err)
		}
	})

	return name
}

// ledgerSQL names the test-owned history table using separate identifier parts.
// All callers supply a schema generated by createLedgerSchema.
func ledgerSQL(schema string) string {
	return query.MySQL.Quote(schema) + ".`history`"
}

// ledgerSnapshot observes raw identity bytes and complete history through the
// public database facade, not driver or migrator implementation details.
func ledgerSnapshot(t *testing.T, db *database.Database, table string) []ledgerTuple {
	t.Helper()

	rows, err := db.Select[ledgerTuple](t.Context(), query.Raw("SELECT HEX(id) AS id_hex, batch, "+
		"DATE_FORMAT(ran_at, '%Y-%m-%d %H:%i:%s.%f') AS ran_at FROM "+table+" ORDER BY HEX(id)"))

	if err != nil {
		t.Fatal(err)
	}

	return rows.Items()
}

// assertLedgerBytes compares the persisted byte identities rather than text
// equality under the schema's deliberately insensitive default collation.
func assertLedgerBytes(t *testing.T, rows []ledgerTuple, ids []string) {
	t.Helper()

	expected := make([]string, 0, len(ids))

	for _, id := range ids {
		expected = append(expected, strings.ToUpper(hex.EncodeToString([]byte(id))))
	}

	slices.Sort(expected)

	actual := make([]string, 0, len(rows))

	for _, row := range rows {
		actual = append(actual, row.IDHex)
	}

	if !slices.Equal(actual, expected) {
		t.Fatalf("persisted ID bytes = %v, want %v", actual, expected)
	}
}

// assertBinaryLedger verifies actual canonical column metadata. Case/accent
// independence alone would not prove the required binary storage declaration.
func assertBinaryLedger(t *testing.T, db *database.Database, schema string) {
	t.Helper()

	column, err := db.Find[ledgerColumnState](t.Context(), query.Raw(
		"SELECT DATA_TYPE AS data_type, CHARACTER_OCTET_LENGTH AS bytes, "+
			"COLLATION_NAME AS collation_name, IS_NULLABLE AS is_nullable "+
			"FROM information_schema.columns WHERE table_schema = ? AND table_name = 'history' AND column_name = 'id'",
		schema))

	if err != nil {
		t.Fatal(err)
	}

	if column.Type != "varbinary" || column.Bytes != 191 {
		t.Fatalf("id storage = %+v", column)
	}

	if column.Collation != nil || column.Nullable != "NO" {
		t.Fatalf("id character/null behavior = %+v", column)
	}
}

// ledgerDefinitions records which public callbacks run while allowing the
// migrator itself to persist each original UTF-8 ID through the native driver.
func ledgerDefinitions(ids []string, runs map[string]int, reverted *[]string) []migrate.Definition {
	definitions := make([]migrate.Definition, 0, len(ids))

	for _, id := range ids {
		definitions = append(definitions, migrate.NewMigration(id, func(migration *migrate.MigrationBuilder) {
			migration.Up(func(context.Context, *database.Database) error {
				runs[id]++

				return nil
			})
			migration.Down(func(context.Context, *database.Database) error {
				*reverted = append(*reverted, id)

				return nil
			})
		}))
	}

	return definitions
}

// TestMySQLLedgerNativeByteIdentities covers case/accent/canonical-Unicode
// distinctions, supplementary characters, and the inclusive 191-byte boundary.
func TestMySQLLedgerNativeByteIdentities(t *testing.T) {
	t.Parallel()

	db := openPool(t, integrationDSN(t))
	schema := createLedgerSchema(t, db)
	ids := []string{"case-a", "accent-e", "accent-é", "unicode-é", "unicode-e\u0301",
		"symbol-🚀", strings.Repeat("é", 95) + "x", "case-A"}
	runs := map[string]int{}
	var reverted []string
	migrator := newMigrator(t, db, schema+".history", uniqueName(t, "lock"),
		ledgerDefinitions(ids, runs, &reverted)...)

	for range 2 {
		if err := migrator.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	for _, id := range ids {
		if runs[id] != 1 {
			t.Fatalf("ID %q ran %d times", id, runs[id])
		}
	}

	assertBinaryLedger(t, db, schema)
	assertLedgerBytes(t, ledgerSnapshot(t, db, ledgerSQL(schema)), ids)

	if err := migrator.Rollback(t.Context(), migrate.Steps(1)); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(reverted, []string{"case-A"}) {
		t.Fatalf("reverted = %v, want exact final ID", reverted)
	}

	assertLedgerBytes(t, ledgerSnapshot(t, db, ledgerSQL(schema)), ids[:len(ids)-1])
}

// markerMigration makes an early callback observable both in Go and in
// committed MySQL state. The fixture must remain untouched after rejection.
func markerMigration(id, marker string, calls *int) migrate.Definition {
	return migrate.NewMigration(id, func(migration *migrate.MigrationBuilder) {
		migration.Up(func(ctx context.Context, db *database.Database) error {
			(*calls)++
			_, err := db.Exec(ctx, query.Raw("INSERT INTO "+marker+" VALUES (?)", "up"))

			return err
		})
		migration.Down(func(ctx context.Context, db *database.Database) error {
			(*calls)++
			_, err := db.Exec(ctx, query.Raw("INSERT INTO "+marker+" VALUES (?)", "down"))

			return err
		})
	})
}

// createLegacyLedger builds only a test-owned old layout. The collation is a
// fixed test literal, never caller input; the seed preserves nonzero history.
func createLegacyLedger(t *testing.T, db *database.Database, schema, collation string) {
	t.Helper()

	if _, err := db.Exec(t.Context(), query.Raw("CREATE TABLE "+ledgerSQL(schema)+
		" (id VARCHAR(191) CHARACTER SET utf8mb4 COLLATE "+collation+" NOT NULL PRIMARY KEY, "+
		"batch int NOT NULL, ran_at datetime(6) NOT NULL)")); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(t.Context(), query.Raw("INSERT INTO "+ledgerSQL(schema)+" VALUES (?, ?, ?)",
		"applied", 7, time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC))); err != nil {
		t.Fatal(err)
	}
}

// assertLegacyLedgerRejected exercises Up and Down through a real insensitive
// or noncanonical textual ledger, proving callbacks and history remain intact.
func assertLegacyLedgerRejected(t *testing.T, collation string) {
	t.Helper()

	db := openPool(t, integrationDSN(t))
	schema := createLedgerSchema(t, db)
	createLegacyLedger(t, db, schema, collation)
	marker := query.MySQL.Quote(schema) + ".`marker`"

	if _, err := db.Exec(t.Context(), query.Raw("CREATE TABLE "+marker+" (direction varchar(8))")); err != nil {
		t.Fatal(err)
	}

	before := ledgerSnapshot(t, db, ledgerSQL(schema))
	calls := 0
	migrator := newMigrator(t, db, schema+".history", uniqueName(t, "lock"),
		markerMigration("applied", marker, &calls), markerMigration("pending", marker, &calls))

	if err := migrator.Up(t.Context()); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want unsupported legacy layout", err)
	}

	if err := migrator.Rollback(t.Context()); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Rollback = %v, want unsupported legacy layout", err)
	}

	if calls != 0 || !slices.Equal(before, ledgerSnapshot(t, db, ledgerSQL(schema))) {
		t.Fatalf("legacy callbacks/history changed: calls=%d", calls)
	}

	count, err := db.Find[int](t.Context(), query.Raw("SELECT COUNT(*) FROM "+marker))

	if err != nil || count != 0 {
		t.Fatalf("marker rows = %d, %v; want untouched", count, err)
	}

	actual, err := db.Find[string](t.Context(), query.Raw(
		"SELECT COLLATION_NAME FROM information_schema.columns "+
			"WHERE table_schema = ? AND table_name = 'history' AND column_name = 'id'", schema))

	if err != nil || actual != collation {
		t.Fatalf("legacy column was changed: %q, %v", actual, err)
	}
}

// TestMySQLLedgerNativeRejectsLegacyCI checks the common insensitive old layout.
func TestMySQLLedgerNativeRejectsLegacyCI(t *testing.T) {
	t.Parallel()

	assertLegacyLedgerRejected(t, "utf8mb4_0900_ai_ci")
}

// TestMySQLLedgerNativeRejectsLegacyPadSpaceBin checks a _bin suffix does not
// turn a character column into the required padding-free byte storage.
func TestMySQLLedgerNativeRejectsLegacyPadSpaceBin(t *testing.T) {
	t.Parallel()

	assertLegacyLedgerRejected(t, "utf8mb4_bin")
}

// TestMySQLLedgerNativeRejectsUnsupportedNoPadText documents the conservative
// canonical-layout policy without claiming this text collation folds case.
func TestMySQLLedgerNativeRejectsUnsupportedNoPadText(t *testing.T) {
	t.Parallel()

	assertLegacyLedgerRejected(t, "utf8mb4_0900_bin")
}

// withLedgerSchema retains one physical connection for a test-owned USE change
// and restores its original schema even after an operation error. It returns
// cleanup errors alongside the test operation's cause.
func withLedgerSchema(
	ctx context.Context,
	db *database.Database,
	schema string,
	fn func(context.Context) error,
) error {
	return db.WithSession(ctx, func(ctx context.Context) (e error) {
		original, err := db.Find[string](ctx, query.Raw("SELECT DATABASE()"))

		if err != nil {
			return err
		}

		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), databaseCleanupTimeout)
			defer cancel()
			_, err := db.Exec(cleanupCtx, query.Raw("USE "+query.MySQL.Quote(original)))
			e = errors.Join(e, err)
		}()

		if _, err := db.Exec(ctx, query.Raw("USE "+query.MySQL.Quote(schema))); err != nil {
			return err
		}

		return fn(ctx)
	})
}

// TestMySQLLedgerNativeFreezesDiscoveredTarget proves an unqualified ledger
// stays in its original schema after a callback changes the current database.
func TestMySQLLedgerNativeFreezesDiscoveredTarget(t *testing.T) {
	t.Parallel()

	db := openPool(t, integrationDSN(t))
	original, other := createLedgerSchema(t, db), createLedgerSchema(t, db)
	createLegacyLedger(t, db, other, "utf8mb4_0900_ai_ci")
	migration := migrate.NewMigration("frozen-é", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(query.Raw("USE " + query.MySQL.Quote(other))))
		migration.Down(migrate.Exec(query.Raw("USE " + query.MySQL.Quote(other))))
	})
	migrator := newMigrator(t, db, "history", uniqueName(t, "lock"), migration)

	if err := withLedgerSchema(t.Context(), db, original, migrator.Up); err != nil {
		t.Fatal(err)
	}

	assertLedgerBytes(t, ledgerSnapshot(t, db, ledgerSQL(original)), []string{"frozen-é"})

	if err := withLedgerSchema(t.Context(), db, original, func(ctx context.Context) error {
		return migrator.Rollback(ctx)
	}); err != nil {
		t.Fatal(err)
	}

	assertLedgerBytes(t, ledgerSnapshot(t, db, ledgerSQL(original)), nil)
	assertLedgerBytes(t, ledgerSnapshot(t, db, ledgerSQL(other)), []string{"applied"})
}

// TestMySQLLedgerNativeQualifiedTargetIgnoresDefault checks actual qualified
// inspection cannot accidentally validate the default database's safe ledger.
func TestMySQLLedgerNativeQualifiedTargetIgnoresDefault(t *testing.T) {
	t.Parallel()

	db := openPool(t, integrationDSN(t))
	defaultSchema, target := createLedgerSchema(t, db), createLedgerSchema(t, db)
	createLegacyLedger(t, db, target, "utf8mb4_0900_ai_ci")
	safe := newMigrator(t, db, defaultSchema+".history", uniqueName(t, "lock"))

	if _, err := safe.Status(t.Context()); err != nil {
		t.Fatal(err)
	}

	unsafe := newMigrator(t, db, target+".history", uniqueName(t, "lock"))

	err := withLedgerSchema(t.Context(), db, defaultSchema, func(ctx context.Context) error {
		_, err := unsafe.Status(ctx)

		return err
	})

	if !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("qualified legacy target = %v, want rejection", err)
	}
}

// createLedgerUser provisions a fixture account without ALTER privileges. Its
// database-scoped CREATE permits the migrator's existing preparation statement.
func createLedgerUser(t *testing.T, db *database.Database, schema string) string {
	t.Helper()

	user := uniqueName(t, "ledger_user")
	account := "'" + user + "'@'%'"

	if _, err := db.Exec(t.Context(), query.Raw("CREATE USER "+account+" IDENTIFIED BY 'ledger-fixture-only'")); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()

		if _, err := db.Exec(ctx, query.Raw("DROP USER "+account)); err != nil {
			t.Errorf("drop disposable ledger account: %v", err)
		}
	})

	if _, err := db.Exec(t.Context(), query.Raw("GRANT SELECT, INSERT, DELETE, CREATE ON "+
		query.MySQL.Quote(schema)+".* TO "+account)); err != nil {
		t.Fatal(err)
	}

	return user
}

// disappearingLedgerDriver injects a real server-side metadata failure after
// preparation of an empty fixture table. It does not fake SQL results or errors.
type disappearingLedgerDriver struct {
	// Driver is the real root or retained native executor.
	database.Driver
	// table is the already quoted, uniquely test-owned ledger target.
	table string
}

// WithSession preserves fault injection inside the retained native session,
// without changing the public driver contract or production driver code.
func (driver disappearingLedgerDriver) WithSession(ctx context.Context, fn func(database.Driver) error) error {
	return driver.Driver.WithSession(ctx, func(session database.Driver) error {
		return fn(disappearingLedgerDriver{Driver: session, table: driver.table})
	})
}

// Exec removes only the newly prepared empty fixture before SHOW can inspect
// it, making MySQL itself return the missing-table metadata error.
func (driver disappearingLedgerDriver) Exec(ctx context.Context, statement string, args ...any) (int64, error) {
	affected, err := driver.Driver.Exec(ctx, statement, args...)

	if err != nil {
		return affected, err
	}

	if strings.HasPrefix(statement, "CREATE TABLE IF NOT EXISTS ") {
		_, err = driver.Driver.Exec(ctx, "DROP TABLE "+driver.table)
	}

	return affected, err
}

// explicitlyConvertLedger performs an operator action on a disposable fixture,
// never through Migrator. Strict mode is restored before the session is released.
func explicitlyConvertLedger(ctx context.Context, db *database.Database, table string) error {
	return db.WithSession(ctx, func(ctx context.Context) (e error) {
		mode, err := db.Find[string](ctx, query.Raw("SELECT @@SESSION.sql_mode"))

		if err != nil {
			return err
		}

		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), databaseCleanupTimeout)
			defer cancel()
			_, err := db.Exec(cleanupCtx, query.Raw("SET SESSION sql_mode = ?", mode))
			e = errors.Join(e, err)
		}()

		if _, err := db.Exec(ctx, query.Raw("SET SESSION sql_mode = ?", mode+",STRICT_ALL_TABLES")); err != nil {
			return err
		}

		_, err = db.Exec(ctx, query.Raw("ALTER TABLE "+table+" MODIFY COLUMN id VARBINARY(191) NOT NULL"))

		return err
	})
}

// requireConvertibleLedger verifies the fixture's UTF-8 storage bytes fit the
// approved width without transcoding. It is not a production repair helper.
func requireConvertibleLedger(t *testing.T, db *database.Database, table string) {
	t.Helper()

	unsafe, err := db.Find[int](t.Context(), query.Raw("SELECT COUNT(*) FROM "+table+
		" WHERE OCTET_LENGTH(id) > 191 OR HEX(id) <> HEX(CONVERT(id USING utf8mb4))"))

	if err != nil || unsafe != 0 {
		t.Fatalf("fixture does not satisfy byte-preservation preconditions: %d, %v", unsafe, err)
	}
}

// TestMySQLLedgerNativeExplicitFixtureConversion preserves strict UTF-8 tuple
// history before resuming pending work. Production Migrator never runs this ALTER.
func TestMySQLLedgerNativeExplicitFixtureConversion(t *testing.T) {
	t.Parallel()

	db := openPool(t, integrationDSN(t))
	schema := createLedgerSchema(t, db)
	createLegacyLedger(t, db, schema, "utf8mb4_0900_ai_ci")

	if _, err := db.Exec(t.Context(), query.Raw("UPDATE "+ledgerSQL(schema)+" SET id = ?", "applied-é")); err != nil {
		t.Fatal(err)
	}

	before := ledgerSnapshot(t, db, ledgerSQL(schema))
	migrator := newMigrator(t, db, schema+".history", uniqueName(t, "lock"))

	if err := migrator.Up(t.Context()); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("legacy layout = %v, want explicit-conversion requirement", err)
	}

	requireConvertibleLedger(t, db, ledgerSQL(schema))

	if err := explicitlyConvertLedger(t.Context(), db, ledgerSQL(schema)); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(before, ledgerSnapshot(t, db, ledgerSQL(schema))) {
		t.Fatal("explicit fixture conversion changed history")
	}

	assertBinaryLedger(t, db, schema)

	if err := migrator.Add(migrate.NewMigration("applied-é", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(context.Context, *database.Database) error {
			t.Fatal("conversion replayed recorded work")

			return nil
		})
	}), migrate.NewMigration("applied-e", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(context.Context, *database.Database) error {
			return nil
		})
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	after := ledgerSnapshot(t, db, ledgerSQL(schema))

	if len(after) != 2 || !slices.Contains(after, before[0]) {
		t.Fatalf("history after resume = %v, want original tuple %v", after, before)
	}
}

// assertLedgerTransport checks both native parameter-execution modes against a
// non-UTF-8 connection charset without changing ID bytes or metadata semantics.
func assertLedgerTransport(t *testing.T, dsn string) {
	t.Helper()

	db := openPool(t, dsn)
	schema := createLedgerSchema(t, db)
	ids := []string{"transport-é", "transport-e\u0301", "transport-🚀", strings.Repeat("é", 95) + "x"}
	runs := map[string]int{}
	var reverted []string
	migrator := newMigrator(t, db, schema+".history", uniqueName(t, "lock"),
		ledgerDefinitions(ids, runs, &reverted)...)

	for range 2 {
		if err := migrator.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	assertLedgerBytes(t, ledgerSnapshot(t, db, ledgerSQL(schema)), ids)

	for _, id := range ids {
		if runs[id] != 1 {
			t.Fatalf("native transport replayed %q: %d", id, runs[id])
		}
	}
}

// TestMySQLLedgerNativeLatin1PreparedBytes exercises binary bindings through
// native prepared execution while the connection's character set is latin1.
func TestMySQLLedgerNativeLatin1PreparedBytes(t *testing.T) {
	t.Parallel()

	assertLedgerTransport(t, integrationDSN(t)+"&charset=latin1&interpolateParams=false")
}

// TestMySQLLedgerNativeLatin1InterpolatedBytes exercises the native driver's
// alternative interpolation mode without treating Unicode IDs as latin1 text.
func TestMySQLLedgerNativeLatin1InterpolatedBytes(t *testing.T) {
	t.Parallel()

	assertLedgerTransport(t, integrationDSN(t)+"&charset=latin1&interpolateParams=true")
}

// TestMySQLLedgerNativeRejectsAdditionalUnique checks metadata is re-read after
// a canonical ledger gains a fixture-only uniqueness constraint on ID prefixes.
func TestMySQLLedgerNativeRejectsAdditionalUnique(t *testing.T) {
	t.Parallel()

	db := openPool(t, integrationDSN(t))
	schema := createLedgerSchema(t, db)
	calls := 0
	migrator := newMigrator(t, db, schema+".history", uniqueName(t, "lock"),
		migrate.NewMigration("pending", func(migration *migrate.MigrationBuilder) {
			migration.Up(func(context.Context, *database.Database) error {
				calls++

				return nil
			})
		}))

	if _, err := migrator.Status(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(t.Context(), query.Raw("ALTER TABLE "+ledgerSQL(schema)+
		" ADD UNIQUE KEY id_prefix (id(8))")); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Up(t.Context()); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want rejection of additional unique index", err)
	}

	if calls != 0 || len(ledgerSnapshot(t, db, ledgerSQL(schema))) != 0 {
		t.Fatalf("unsupported unique layout ran callbacks: %d", calls)
	}
}
