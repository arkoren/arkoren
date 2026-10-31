package migrate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"arkoren.dev/foundation/query"
)

// mysqlColumn is the complete SHOW FULL COLUMNS row shape. Fields not used for
// safety checks are still represented because the public SQL scanner rejects
// unknown result columns; they are not interpreted as proof of a safe layout.
type mysqlColumn struct {
	// Field is the actual column name, independent of its declared case.
	Field string `db:"Field"`
	// Type is the server's actual storage declaration, not the requested DDL.
	Type string `db:"Type"`
	// Collation is NULL for binary storage and non-NULL for character columns.
	Collation *string `db:"Collation"`
	// Null reports whether the column permits SQL NULL.
	Null string `db:"Null"`
	// Key is display metadata; full key safety comes from SHOW INDEX instead.
	Key any `db:"Key"`
	// Default retains the server value without depending on its native type.
	Default any `db:"Default"`
	// Extra identifies generated or otherwise noncanonical column behavior.
	Extra string `db:"Extra"`
	// Privileges is descriptive visibility metadata, not an authorization check.
	Privileges any `db:"Privileges"`
	// Comment is retained only to satisfy the public row-scanning contract.
	Comment any `db:"Comment"`
}

// mysqlIndex is the SHOW INDEX result shape across supported MySQL metadata.
// Nullable and unused fields retain native values; only a complete, sole id
// primary-key part establishes the required uniqueness invariant.
type mysqlIndex struct {
	// Table is the server-reported object name, not a target-resolution source.
	Table any `db:"Table"`
	// NonUnique is zero for a unique index.
	NonUnique int64 `db:"Non_unique"`
	// KeyName is PRIMARY for a primary-key index.
	KeyName string `db:"Key_name"`
	// Sequence is the one-based position within a multi-column index.
	Sequence int64 `db:"Seq_in_index"`
	// ColumnName is NULL for functional index parts.
	ColumnName *string `db:"Column_name"`
	// Collation describes index direction, not the column character collation.
	Collation any `db:"Collation"`
	// Cardinality is an estimate and is not used to infer uniqueness.
	Cardinality any `db:"Cardinality"`
	// SubPart is NULL only when the whole column is indexed.
	SubPart *int64 `db:"Sub_part"`
	// Packed is server-specific packing metadata.
	Packed any `db:"Packed"`
	// Null is descriptive index nullability metadata.
	Null any `db:"Null"`
	// IndexType reports the index implementation without changing its identity.
	IndexType any `db:"Index_type"`
	// Comment contains server status annotations.
	Comment any `db:"Comment"`
	// IndexComment contains the operator-supplied description.
	IndexComment any `db:"Index_comment"`
	// Visible reports optimizer visibility, not uniqueness enforcement.
	Visible any `db:"Visible"`
	// Expression is NULL for the ordinary id index required by this ledger.
	Expression *string `db:"Expression"`
}

// prepareMySQL freezes the actual schema, prepares new binary storage, and
// validates the existing target. CREATE IF NOT EXISTS never converts legacy
// columns. All discovery, metadata and history SQL uses the lock-owning session.
func (history *ledger) prepareMySQL(ctx context.Context) error {
	if err := history.resolveMySQL(ctx); err != nil {
		return err
	}

	create := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (`id` VARBINARY(%d) NOT NULL PRIMARY KEY, "+
		"`batch` int NOT NULL, `ran_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6))",
		history.quotedTable(), maxIDLength)

	if _, err := history.db.Exec(ctx, query.Raw(create)); err != nil {
		return fmt.Errorf("migrate prepare table: %w", err)
	}

	return history.checkMySQL(ctx)
}

// resolveMySQL distinguishes configured identifier components from the actual
// current schema returned by the server. The latter may contain punctuation,
// so it is quoted as one component rather than parsed or normalized again.
func (history *ledger) resolveMySQL(ctx context.Context) error {
	if len(history.parts) > 2 {
		return fmt.Errorf("migrate MySQL ledger: %w: table must have one or two identifier components",
			ErrInvalidMigration)
	}

	if len(history.parts) == 2 {
		return nil
	}

	name, err := history.db.Find[*string](ctx, query.Raw("SELECT DATABASE()"))

	if err != nil {
		return fmt.Errorf("migrate resolve MySQL ledger schema: %w", err)
	}

	if name == nil || *name == "" {
		return fmt.Errorf("migrate MySQL ledger: %w: select a database or configure a qualified Table",
			ErrInvalidMigration)
	}

	history.parts = append([]string{*name}, history.parts...)

	return nil
}

// checkMySQL verifies actual column and full primary-key metadata before any
// user step or ledger-row mutation. Inability to observe metadata is an
// inspection failure, not evidence that an operator should convert storage.
func (history ledger) checkMySQL(ctx context.Context) error {
	columns, err := history.db.Select[mysqlColumn](ctx,
		query.Raw("SHOW FULL COLUMNS FROM "+history.quotedTable()))

	if err != nil {
		return fmt.Errorf("migrate inspect MySQL ledger %s columns; migration steps not started: %w",
			history.quotedTable(), err)
	}

	if err := history.checkMySQLColumn(columns.Items()); err != nil {
		return err
	}

	indexes, err := history.db.Select[mysqlIndex](ctx,
		query.Raw("SHOW INDEX FROM "+history.quotedTable()))

	if err != nil {
		return fmt.Errorf("migrate inspect MySQL ledger %s indexes; migration steps not started: %w",
			history.quotedTable(), err)
	}

	return history.checkMySQLPrimary(indexes.Items())
}

// checkMySQLColumn accepts only canonical variable-length byte storage. A text
// _bin collation is not equivalent: it can retain padding and charset rules.
// Missing/hidden rows remain unverified and never produce conversion advice.
func (history ledger) checkMySQLColumn(columns []mysqlColumn) error {
	index := slices.IndexFunc(columns, func(column mysqlColumn) bool {
		return strings.EqualFold(column.Field, "id")
	})

	if index < 0 {
		return fmt.Errorf("migrate inspect MySQL ledger %s: id column metadata unavailable; "+
			"verify visibility and schema; migration steps not started", history.quotedTable())
	}

	column := columns[index]
	storage := column.Type == fmt.Sprintf("varbinary(%d)", maxIDLength) && column.Collation == nil
	ordinary := column.Null == "NO" && column.Extra == ""

	if storage && ordinary {
		return nil
	}

	collation := "NULL"

	if column.Collation != nil {
		collation = *column.Collation
	}

	return history.invalidMySQLLayout(fmt.Sprintf("id type=%q collation=%q nullable=%q extra=%q",
		column.Type, collation, column.Null, column.Extra))
}

// checkMySQLPrimary requires one whole ordinary id column, rather than relying
// on SHOW COLUMNS' ambiguous PRI marker for composite or prefix indexes.
// Additional unique indexes could impose narrower prefix/collation equality
// even with an exact primary key, so only nonunique secondary indexes are allowed.
func (history ledger) checkMySQLPrimary(indexes []mysqlIndex) error {
	if slices.ContainsFunc(indexes, func(index mysqlIndex) bool {
		return index.KeyName != "PRIMARY" && index.NonUnique == 0
	}) {
		return history.invalidMySQLLayout("additional unique index imposes noncanonical ID constraints")
	}

	primary := slices.DeleteFunc(indexes, func(index mysqlIndex) bool {
		return index.KeyName != "PRIMARY"
	})

	if len(primary) == 0 {
		return fmt.Errorf("migrate inspect MySQL ledger %s: primary-key metadata unavailable; "+
			"verify visibility and schema; migration steps not started", history.quotedTable())
	}

	if len(primary) != 1 {
		return history.invalidMySQLLayout("primary key has multiple parts")
	}

	return history.checkMySQLPrimaryPart(primary[0])
}

// checkMySQLPrimaryPart validates the sole primary index part after aggregate
// key selection. A whole, ordinary id column must supply unique byte equality;
// index prefixes, expressions and malformed sequence metadata cannot prove it.
func (history ledger) checkMySQLPrimaryPart(index mysqlIndex) error {
	if index.ColumnName == nil || !strings.EqualFold(*index.ColumnName, "id") {
		return history.invalidMySQLLayout("primary key is not the ordinary id column")
	}

	if index.NonUnique != 0 || index.Sequence != 1 {
		return history.invalidMySQLLayout("primary-key uniqueness or sequence is invalid")
	}

	if index.SubPart != nil || index.Expression != nil {
		return history.invalidMySQLLayout("primary key is a prefix or expression")
	}

	return nil
}

// invalidMySQLLayout classifies a confirmed unsupported schema using the
// existing domain sentinel. The notice never includes ID values or attempts
// repair: an operator must verify bytes and history before explicit conversion.
func (history ledger) invalidMySQLLayout(actual string) error {
	return fmt.Errorf("migrate MySQL ledger %s: %w: %s; require VARBINARY(%d) NOT NULL "+
		"with a full single-column PRIMARY KEY(id); migration steps not started; "+
		"operator must review explicit repair preserving ID bytes and history; no automatic conversion attempted",
		history.quotedTable(), ErrInvalidMigration, actual, maxIDLength)
}
