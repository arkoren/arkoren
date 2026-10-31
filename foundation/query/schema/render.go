package schema

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"arkoren.dev/foundation/query"
)

// typeNames lists the type of each portable column kind for ANSI, Postgres,
// MySQL, and SQLite, in that order. Bounded string, char, and decimal types
// are parameterized and rendered by [sqlType] instead; kindRaw has no entry.
var typeNames = [kindRaw + 1][4]string{
	kindString:       {"", "", "", "TEXT"},
	kindText:         {"text", "text", "text", "TEXT"},
	kindInteger:      {"integer", "integer", "int", "INTEGER"},
	kindBigInteger:   {"bigint", "bigint", "bigint", "INTEGER"},
	kindBoolean:      {"boolean", "boolean", "boolean", "INTEGER"},
	kindFloat:        {"double precision", "double precision", "double", "REAL"},
	kindDecimal:      {"", "", "", "NUMERIC"},
	kindBinary:       {"blob", "bytea", "blob", "BLOB"},
	kindUUID:         {"char(36)", "uuid", "char(36)", "TEXT"},
	kindJSON:         {"json", "jsonb", "json", "TEXT"},
	kindDate:         {"date", "date", "date", "DATE"},
	kindTimestamp:    {"timestamp(6)", "timestamp(6)", "datetime(6)", "DATETIME"},
	kindTimestampTZ:  {"timestamp with time zone", "timestamptz", "datetime(6)", "TIMESTAMP"},
	kindSmallInteger: {"smallint", "smallint", "smallint", "INTEGER"},
	kindChar:         {"", "", "", "TEXT"},
}

// sqlType checks the captured kind and renders any type parameters before
// selecting a fixed native name. Raw types bypass portable mapping unchanged;
// SQLite uses affinity names instead of varchar, char, or decimal parameters.
func sqlType(dialect query.Dialect, column *Column) (string, error) {
	if column.kind == kindRaw {
		return column.rawType, nil
	}

	if column.kind == 0 || column.kind > kindRaw {
		return "", fmt.Errorf("column %q has no type", column.name)
	}

	if dialect != query.SQLite {
		switch column.kind {
		case kindString:
			return "varchar(" + strconv.Itoa(column.length) + ")", nil
		case kindChar:
			return "char(" + strconv.Itoa(column.length) + ")", nil
		case kindDecimal:
			precision, scale := strconv.Itoa(column.precision), strconv.Itoa(column.scale)

			if dialect == query.MySQL {
				return "decimal(" + precision + "," + scale + ")", nil
			}

			return "numeric(" + precision + "," + scale + ")", nil
		}
	}

	return fixedTypeName(dialect, typeNames[column.kind]), nil
}

// fixedTypeName selects one native spelling from the portable type table.
// The table's columns are ANSI, PostgreSQL, MySQL, and SQLite; the zero dialect
// keeps the existing ANSI fallback without exposing that layout to sqlType.
func fixedTypeName(dialect query.Dialect, names [4]string) string {
	switch dialect {
	case query.Postgres:
		return names[1]
	case query.MySQL:
		return names[2]
	case query.SQLite:
		return names[3]
	default:
		return names[0]
	}
}

// defaultExpression renders the DEFAULT value of column. MySQL (8.0.13 and
// later) rejects plain literal defaults on text, blob, and json columns, so
// it gets those wrapped in parentheses; so does a default set on a column of
// unknown type, as [Change] does for a default-only change.
func defaultExpression(dialect query.Dialect, column *Column) (string, error) {
	var expression string

	switch column.defaultKind {
	case defaultNow:
		if dialect == query.MySQL {
			return "CURRENT_TIMESTAMP(6)", nil
		}

		return "CURRENT_TIMESTAMP", nil
	case defaultRaw:
		expression = column.defaultExpr
	default:
		var err error

		if expression, err = literal(dialect, column.defaultValue); err != nil {
			return "", fmt.Errorf("column %q: %w", column.name, err)
		}
	}

	switch column.kind {
	case 0, kindText, kindBinary, kindJSON:
		if dialect == query.MySQL {
			return "(" + expression + ")", nil
		}
	}

	return expression, nil
}

// literal formats a scalar already checked by validateLiteral. Dialect-specific
// quoting and boolean spelling remain lazy; construction owns content validation.
func literal(dialect query.Dialect, value any) (string, error) {
	if value == nil {
		return "NULL", nil
	}

	reflected := reflect.ValueOf(value)

	switch reflected.Kind() {
	case reflect.String:
		return stringLiteral(dialect, reflected.String()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(reflected.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(reflected.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(reflected.Float(), 'g', -1, reflected.Type().Bits()), nil
	case reflect.Bool:
		return booleanLiteral(dialect, reflected.Bool()), nil
	default:
		return "", fmt.Errorf("unsupported default type %T", value)
	}
}

// booleanLiteral selects the dialect's numeric or keyword truth representation.
// Numeric dialects use 1/0; ANSI and PostgreSQL use TRUE/FALSE.
func booleanLiteral(dialect query.Dialect, value bool) string {
	if dialect == query.MySQL || dialect == query.SQLite {
		if value {
			return "1"
		}

		return "0"
	}

	return strings.ToUpper(strconv.FormatBool(value))
}

// stringLiteral quotes text, doubling single quotes and, on MySQL, escaping
// backslashes. Capture has already rejected NUL bytes. Escaping is correct under
// standard server modes: PostgreSQL with standard_conforming_strings=on
// (the default) and MySQL without NO_BACKSLASH_ESCAPES.
func stringLiteral(dialect query.Dialect, text string) string {
	if dialect == query.MySQL {
		text = strings.ReplaceAll(text, `\`, `\\`)
	}

	return "'" + strings.ReplaceAll(text, "'", "''") + "'"
}

// columnDefinition renders the column name, type, nullability, and default.
// It also checks SQLite's reference-target restriction for column modifiers,
// including references rendered later as separate CREATE TABLE constraints.
func columnDefinition(dialect query.Dialect, column *Column) (string, error) {
	typ, err := sqlType(dialect, column)

	if err != nil {
		return "", err
	}

	if dialect == query.SQLite && len(column.foreign.table) > 1 {
		return "", unsupported(dialect, "schema-qualified REFERENCES")
	}

	name := dialect.Quote(column.name)

	if column.autoIncrement {
		switch dialect {
		case query.MySQL:
			return name + " " + typ + " NOT NULL AUTO_INCREMENT PRIMARY KEY", nil
		case query.SQLite:
			return name + " " + typ + " PRIMARY KEY AUTOINCREMENT", nil
		default:
			return name + " " + typ + " GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY", nil
		}
	}

	parts := []string{name, typ}

	if !column.nullable {
		parts = append(parts, "NOT NULL")
	}

	if column.defaultKind != defaultNone {
		expression, err := defaultExpression(dialect, column)

		if err != nil {
			return "", err
		}

		parts = append(parts, "DEFAULT", expression)
	}

	return strings.Join(parts, " "), nil
}

// quoteList quotes and comma-joins column names.
func quoteList(dialect query.Dialect, columns []string) string {
	quoted := make([]string, len(columns))

	for index, column := range columns {
		quoted[index] = dialect.Quote(column)
	}

	return strings.Join(quoted, ", ")
}

// referenceClause renders REFERENCES table (column) with actions.
func referenceClause(dialect query.Dialect, foreign foreignKey) string {
	clause := "REFERENCES " + foreign.table.quoted(dialect) + " (" + dialect.Quote(foreign.column) + ")"

	if foreign.onDelete != "" {
		clause += " ON DELETE " + string(foreign.onDelete)
	}

	if foreign.onUpdate != "" {
		clause += " ON UPDATE " + string(foreign.onUpdate)
	}

	return clause
}

// foreignKeyClause renders FOREIGN KEY (column) REFERENCES ... with actions.
func foreignKeyClause(dialect query.Dialect, column string, foreign foreignKey) string {
	return "FOREIGN KEY (" + dialect.Quote(column) + ") " + referenceClause(dialect, foreign)
}

// createTable assembles CREATE TABLE after the table-level dialect guard.
// Columns remain in declaration order, followed by the primary key and then
// foreign keys; indexes are separate steps and never enter these definitions.
func (table *Table) createTable(dialect query.Dialect, ifNotExists bool) (string, error) {
	if ifNotExists && dialect == query.ANSI {
		return "", unsupported(dialect, "CREATE TABLE IF NOT EXISTS")
	}

	definitions, foreignKeys, err := table.createColumns(dialect)

	if err != nil {
		return "", err
	}

	if len(table.primary) > 0 {
		name, err := constraintName(table.name, nil, "pkey")

		if err != nil {
			return "", err
		}

		definitions = append(definitions,
			"CONSTRAINT "+dialect.Quote(name)+" PRIMARY KEY ("+quoteList(dialect, table.primary)+")")
	}

	definitions = append(definitions, foreignKeys...)

	prefix := "CREATE TABLE "

	if ifNotExists {
		prefix += "IF NOT EXISTS "
	}

	return prefix + table.name.quoted(dialect) + " (" + strings.Join(definitions, ", ") + ")", nil
}

// createColumns renders columns and their associated foreign-key constraints
// in one declaration-order pass. The separate constraint slice lets createTable
// place the primary key first without reordering column validation failures.
func (table *Table) createColumns(dialect query.Dialect) ([]string, []string, error) {
	var definitions, foreignKeys []string

	for _, declaration := range table.actions {
		if declaration.kind != actionAddColumn {
			continue
		}

		column := declaration.column
		definition, err := columnDefinition(dialect, column)

		if err != nil {
			return nil, nil, err
		}

		definitions = append(definitions, definition)

		if !column.references {
			continue
		}

		name, err := constraintName(table.name, []string{column.name}, "fkey")

		if err != nil {
			return nil, nil, err
		}

		foreignKeys = append(foreignKeys,
			"CONSTRAINT "+dialect.Quote(name)+" "+foreignKeyClause(dialect, column.name, column.foreign))
	}

	return definitions, foreignKeys, nil
}

// addColumn renders ADD COLUMN plus the constraints the column carries. A
// unique column's index is a separate statement.
func (table *Table) addColumn(dialect query.Dialect, column *Column) (string, error) {
	if dialect == query.SQLite {
		if err := validateSQLiteAddition(column); err != nil {
			return "", err
		}
	}

	definition, err := columnDefinition(dialect, column)

	if err != nil {
		return "", err
	}

	if column.references && dialect == query.SQLite {
		definition += " " + referenceClause(dialect, column.foreign)
	}

	constraints, err := table.addedConstraints(dialect, column)

	if err != nil {
		return "", err
	}

	clauses := append([]string{"ADD COLUMN " + definition}, constraints...)

	return "ALTER TABLE " + table.name.quoted(dialect) + " " + strings.Join(clauses, ", "), nil
}

// validateSQLiteAddition rejects the dialect's known ADD COLUMN restrictions.
// It runs before formatting and retains the existing restriction precedence,
// including explicit DEFAULT NULL not satisfying a NOT NULL column.
func validateSQLiteAddition(column *Column) error {
	nonNullDefault := column.defaultKind != defaultNone && column.defaultValue != nil

	switch {
	case column.primary, column.autoIncrement:
		return unsupported(query.SQLite, "ADD COLUMN with a key constraint")
	case column.defaultKind == defaultNow:
		return unsupported(query.SQLite, "ADD COLUMN with DEFAULT now")
	case column.defaultKind == defaultRaw:
		return unsupported(query.SQLite, "ADD COLUMN with a raw DEFAULT")
	case column.references && nonNullDefault:
		return unsupported(query.SQLite, "ADD COLUMN with REFERENCES and a non-NULL default")
	case !column.nullable && column.defaultValue == nil:
		return unsupported(query.SQLite, "ADD COLUMN NOT NULL without a non-NULL default")
	}

	return nil
}

// addedConstraints renders constraints following ADD COLUMN on non-SQLite
// dialects. SQLite references are inline and other added key constraints have
// already failed validation, so no separate clauses are returned for it.
func (table *Table) addedConstraints(dialect query.Dialect, column *Column) ([]string, error) {
	if dialect == query.SQLite {
		return nil, nil
	}

	var clauses []string

	if column.primary && !column.autoIncrement {
		name, err := constraintName(table.name, nil, "pkey")

		if err != nil {
			return nil, err
		}

		clauses = append(clauses,
			"ADD CONSTRAINT "+dialect.Quote(name)+" PRIMARY KEY ("+dialect.Quote(column.name)+")")
	}

	if column.references {
		name, err := constraintName(table.name, []string{column.name}, "fkey")

		if err != nil {
			return nil, err
		}

		clauses = append(clauses,
			"ADD CONSTRAINT "+dialect.Quote(name)+" "+foreignKeyClause(dialect, column.name, column.foreign))
	}

	return clauses, nil
}

// changeColumn checks native restrictions before selecting whole-column MySQL
// rendering or ordered ANSI/PostgreSQL aspect clauses. SQLite rejection remains
// first, followed by unsupported USING, before any definition is rendered.
func (table *Table) changeColumn(dialect query.Dialect, change *Change) (string, error) {
	if dialect == query.SQLite {
		return "", unsupported(dialect, "ALTER COLUMN")
	}

	if change.using != "" && dialect != query.Postgres {
		return "", unsupported(dialect, "ALTER COLUMN ... USING")
	}

	prefix := "ALTER TABLE " + table.name.quoted(dialect) + " "

	if dialect == query.MySQL {
		clause, err := mysqlColumnChange(change)

		if err != nil {
			return "", err
		}

		return prefix + clause, nil
	}

	clauses, err := columnChangeClauses(dialect, change)

	if err != nil {
		return "", err
	}

	return prefix + strings.Join(clauses, ", "), nil
}

// mysqlColumnChange renders MODIFY COLUMN only with both type and nullability.
// Otherwise it renders the requested default-only change. Whole-column changes
// deliberately omit any default not restated by the declaration.
func mysqlColumnChange(change *Change) (string, error) {
	column := &change.column
	typed := column.kind != 0

	if typed || change.nullSet {
		if !typed || !change.nullSet {
			return "", unsupported(query.MySQL, "MODIFY COLUMN without both a type and Nullable or NotNull")
		}

		definition, err := columnDefinition(query.MySQL, column)

		if err != nil {
			return "", err
		}

		return "MODIFY COLUMN " + definition, nil
	}

	name := "ALTER COLUMN " + query.MySQL.Quote(column.name) + " "

	if change.dropDefault {
		return name + "DROP DEFAULT", nil
	}

	expression, err := defaultExpression(query.MySQL, column)

	if err != nil {
		return "", err
	}

	return name + "SET DEFAULT " + expression, nil
}

// columnChangeClauses renders ANSI/PostgreSQL aspects in the established order:
// type and optional USING, nullability, then the replacement or dropped default.
// Native feature checks have already run; no application draft is reread here.
func columnChangeClauses(dialect query.Dialect, change *Change) ([]string, error) {
	column := &change.column
	name := "ALTER COLUMN " + dialect.Quote(column.name) + " "
	var clauses []string

	if column.kind != 0 {
		clause, err := columnTypeChange(dialect, column, change.using)

		if err != nil {
			return nil, err
		}

		clauses = append(clauses, clause)
	}

	if change.nullSet {
		nullability := "SET NOT NULL"

		if column.nullable {
			nullability = "DROP NOT NULL"
		}

		clauses = append(clauses, name+nullability)
	}

	if change.dropDefault {
		clauses = append(clauses, name+"DROP DEFAULT")
	} else if column.defaultKind != defaultNone {
		expression, err := defaultExpression(dialect, column)

		if err != nil {
			return nil, err
		}

		clauses = append(clauses, name+"SET DEFAULT "+expression)
	}

	return clauses, nil
}

// columnTypeChange renders one type aspect without changing nullability or
// defaults. PostgreSQL alone uses TYPE and a permitted USING expression; ANSI
// keeps SET DATA TYPE. The caller has already rejected USING on other dialects.
func columnTypeChange(dialect query.Dialect, column *Column, using string) (string, error) {
	typ, err := sqlType(dialect, column)

	if err != nil {
		return "", err
	}

	name := "ALTER COLUMN " + dialect.Quote(column.name) + " "

	if dialect != query.Postgres {
		return name + "SET DATA TYPE " + typ, nil
	}

	clause := name + "TYPE " + typ

	if using != "" {
		clause += " USING " + using
	}

	return clause, nil
}

// indexStep returns the CREATE [UNIQUE] INDEX statement over columns. Postgres
// has no syntax to qualify the index name in CREATE INDEX, so it takes the
// table's schema implicitly, while SQLite expects the schema on the index name
// and not on the table, and the statement renders that form.
func (table *Table) indexStep(unique, ifNotExists bool, columns ...string) step {
	return step{
		render: func(dialect query.Dialect) (string, error) {
			if ifNotExists && (dialect == query.MySQL || dialect == query.ANSI) {
				return "", unsupported(dialect, "CREATE INDEX IF NOT EXISTS")
			}

			suffix := "index"
			sql := "CREATE "

			if unique {
				suffix = "key"
				sql += "UNIQUE "
			}

			name, err := constraintName(table.name, columns, suffix)

			if err != nil {
				return "", err
			}

			sql += "INDEX "

			if ifNotExists {
				sql += "IF NOT EXISTS "
			}

			indexName, onTable := tableName{name}, table.name

			if dialect == query.SQLite {
				indexName = append(slices.Clone(table.name[:len(table.name)-1]), name)
				onTable = table.name[len(table.name)-1:]
			}

			return sql + indexName.quoted(dialect) + " ON " + onTable.quoted(dialect) + " (" + quoteList(dialect, columns) + ")", nil
		},
	}
}

// dropIndexStep returns the DROP INDEX statement for the index that
// [Table.Index] (suffix "index") or [Table.Unique] (suffix "key") created
// over columns. MySQL needs ON table; Postgres and SQLite qualify the index
// name with the table's schema.
func (table *Table) dropIndexStep(suffix string, columns []string) step {
	return step{
		render: func(dialect query.Dialect) (string, error) {
			name, err := constraintName(table.name, columns, suffix)

			if err != nil {
				return "", err
			}

			if dialect == query.MySQL {
				return "DROP INDEX " + dialect.Quote(name) + " ON " + table.name.quoted(dialect), nil
			}

			indexName := tableName{name}

			if dialect == query.Postgres || dialect == query.SQLite {
				indexName = append(slices.Clone(table.name[:len(table.name)-1]), name)
			}

			return "DROP INDEX " + indexName.quoted(dialect), nil
		},
	}
}
