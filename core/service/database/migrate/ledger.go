package migrate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// ledger owns one operation's bookkeeping target and SQL. Its identifier parts
// are private and fixed before user steps run. On MySQL the schema is explicit,
// so a step's USE statement cannot redirect a later history insert or deletion.
type ledger struct {
	// db executes every statement with the caller's retained-session context.
	db *database.Database
	// parts are owned, validated configured identifiers. MySQL's discovered
	// current schema is a separate quoted component, not a parsed SQL fragment.
	parts []string
}

// prepareLedger creates bookkeeping storage when absent and, on MySQL, verifies
// actual binary column/index metadata before reading history or running steps.
// It never alters an existing table. table has passed Migrator.checkConfig.
func prepareLedger(ctx context.Context, db *database.Database, table string) (ledger, error) {
	history := ledger{db: db, parts: strings.Split(table, ".")}

	if db.Dialect() == query.MySQL {
		if err := history.prepareMySQL(ctx); err != nil {
			return ledger{}, err
		}

		return history, nil
	}

	create := schema.CreateIfNotExists(table, func(table *schema.Table) {
		table.String("id", maxIDLength).Primary()
		table.Integer("batch")
		table.TimestampTZ("ran_at").DefaultNow()
	})

	if err := Schema(create)(ctx, db); err != nil {
		return ledger{}, fmt.Errorf("migrate prepare table: %w", err)
	}

	return history, nil
}

// quotedTable quotes components independently with the public dialect contract.
// A discovered database containing dots or quote characters remains one name;
// it is never reinterpreted through the configured dotted-identifier grammar.
func (history ledger) quotedTable() string {
	parts := make([]string, len(history.parts))

	for index, part := range history.parts {
		parts[index] = history.db.Dialect().Quote(part)
	}

	return strings.Join(parts, ".")
}

// records reads a detached snapshot without imposing presentation or rollback
// order. MySQL uses the fully qualified target frozen during preparation.
func (history ledger) records(ctx context.Context) ([]record, error) {
	var statement query.Statementer

	if history.db.Dialect() == query.MySQL {
		statement = query.Raw("SELECT `id`, `batch`, `ran_at` FROM " + history.quotedTable())
	} else {
		statement = query.Table(strings.Join(history.parts, "."), func(statement *query.Builder) {
			statement.Select("id", "batch", "ran_at")
		})
	}

	rows, err := history.db.Select[record](ctx, statement)

	if err != nil {
		return nil, fmt.Errorf("migrate read records: %w", err)
	}

	return slices.Clone(rows.Items()), nil
}

// insert records a completed migration without changing its public ID bytes.
// Only MySQL binds the ID as []byte, avoiding character-string conversion at
// the binary ledger seam; other dialects keep their existing query and values.
func (history ledger) insert(ctx context.Context, id string, batch int) error {
	var statement query.Statementer

	if history.db.Dialect() == query.MySQL {
		statement = query.Raw("INSERT INTO "+history.quotedTable()+
			" (`batch`, `id`, `ran_at`) VALUES (?, ?, ?)", batch, []byte(id), time.Now().UTC())
	} else {
		statement = query.Table(strings.Join(history.parts, "."), func(statement *query.Builder) {
			statement.Insert(map[string]any{"id": id, "batch": batch, "ran_at": time.Now().UTC()})
		})
	}

	if _, err := history.db.Exec(ctx, statement); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	return nil
}

// delete removes only the exact completed ID after Down succeeds. The MySQL
// byte argument and qualified target are the same representation used by insert.
func (history ledger) delete(ctx context.Context, id string) error {
	var statement query.Statementer

	if history.db.Dialect() == query.MySQL {
		statement = query.Raw("DELETE FROM "+history.quotedTable()+" WHERE `id` = ?", []byte(id))
	} else {
		statement = query.Table(strings.Join(history.parts, "."), func(statement *query.Builder) {
			statement.Where("id", id)
			statement.Delete()
		})
	}

	if _, err := history.db.Exec(ctx, statement); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	return nil
}

// record is one row of the bookkeeping table.
type record struct {
	// ID is the migration id.
	ID string `db:"id"`

	// Batch is the Up run that applied the migration.
	Batch int `db:"batch"`

	// RanAt is the raw ran_at value. Drivers return a time.Time, text, or
	// bytes depending on the dialect; see [parseTime].
	RanAt any `db:"ran_at"`
}
