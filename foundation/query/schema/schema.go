package schema

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"arkoren.dev/foundation/query"
)

// maxNameBytes is the longest identifier part, in bytes, that the package
// accepts. It is the PostgreSQL limit and the strictest of the dialects.
const maxNameBytes = 63

// ReferentialAction is the action a foreign key takes when the referenced row
// is deleted or updated. Empty ("") omits the explicit action; passing empty to
// OnDelete or OnUpdate clears a previous choice and leaves database defaults.
// Unknown nonempty values retain a construction error rather than rendering SQL.
//
// Example:
//
//	op := schema.Create("posts", func(table *schema.Table) {
//		table.ForeignID("user_id", "users").OnDelete(schema.Cascade).OnDelete("")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
type ReferentialAction string

const (
	// Cascade propagates the delete or update to referencing rows.
	// The database enforces the action; declarations do not execute it.
	//
	// Example:
	//
	//	op := schema.Create("posts", func(table *schema.Table) {
	//		table.ForeignID("user_id", "users").OnDelete(schema.Cascade)
	//	})
	//	if err := op.Err(); err != nil {
	//		return err
	//	}
	Cascade ReferentialAction = "CASCADE"

	// SetNull sets the referencing column to NULL.
	// Make that column Nullable as well so the database can apply the action.
	//
	// Example:
	//
	//	op := schema.Create("posts", func(table *schema.Table) {
	//		table.ForeignID("user_id", "users").Nullable().OnDelete(schema.SetNull)
	//	})
	//	if err := op.Err(); err != nil {
	//		return err
	//	}
	SetNull ReferentialAction = "SET NULL"

	// Restrict rejects the delete or update immediately.
	// Enforcement belongs to the database, not declaration construction.
	//
	// Example:
	//
	//	op := schema.Create("posts", func(table *schema.Table) {
	//		table.ForeignID("user_id", "users").OnDelete(schema.Restrict)
	//	})
	//	if err := op.Err(); err != nil {
	//		return err
	//	}
	Restrict ReferentialAction = "RESTRICT"

	// NoAction rejects the delete or update at the end of the statement.
	// It emits an explicit NO ACTION; empty ReferentialAction instead omits SQL.
	//
	// Example:
	//
	//	op := schema.Create("posts", func(table *schema.Table) {
	//		table.ForeignID("user_id", "users").OnUpdate(schema.NoAction)
	//	})
	//	if err := op.Err(); err != nil {
	//		return err
	//	}
	NoAction ReferentialAction = "NO ACTION"
)

// valid reports whether action is empty (unset) or a known action.
func (action ReferentialAction) valid() bool {
	switch action {
	case "", Cascade, SetNull, Restrict, NoAction:
		return true
	default:
		return false
	}
}

// Operation is one schema change: a table created, altered, renamed, or
// dropped. It compiles to one or more argument-free SQL statements whose
// number is fixed when the operation is constructed and does not depend on
// the dialect.
//
// An Operation is an immutable value. Problems found while constructing it
// (invalid names, unknown index columns, modifier misuse, an empty callback)
// are kept and reported by [Operation.Err]; problems that depend on the
// dialect (for example SQLite changing a column) surface when a statement is
// compiled and match [query.ErrUnsupported]. Every error matches
// [query.ErrInvalidQuery].
// The zero value has no statements and no construction error; constructors
// reject missing declarations instead of publishing such an empty operation.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.ID()
//	})
//	for _, definition := range op.Statements() {
//		statement, err := definition.BuildFor(query.Postgres)
//		if err != nil {
//			return err
//		}
//		fmt.Println(statement.SQL())
//	}
type Operation struct {
	// err is the first construction problem, already wrapped.
	err error

	// steps are the statements in execution order.
	steps []step
}

// step is one statement of an Operation. render produces its SQL text for a
// dialect; err, when set, replaces it with the operation's construction error.
type step struct {
	// render compiles the captured declaration lazily for a requested dialect.
	render func(dialect query.Dialect) (string, error)

	// err replaces rendering when construction already failed.
	err error
}

// BuildFor compiles the statement for dialect. It returns the operation's
// construction error when there is one. The SQL goes through [query.Raw], so
// a "?" outside quotes in a raw expression is a placeholder and fails; "?"
// inside a quoted default is kept as written.
//
// Example:
//
//	definition := schema.Drop("users").Statements()[0]
//	statement, err := definition.BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
func (step step) BuildFor(dialect query.Dialect) (query.Statement, error) {
	if step.err != nil {
		return query.Statement{}, step.err
	}

	sql, err := step.render(dialect)

	if err != nil {
		return query.Statement{}, invalid(err)
	}

	return query.Raw(sql).BuildFor(dialect)
}

// invalid marks err as an invalid query.
func invalid(err error) error {
	return fmt.Errorf("%w: %w", query.ErrInvalidQuery, err)
}

// unsupported reports that dialect cannot express feature.
func unsupported(dialect query.Dialect, feature string) error {
	return fmt.Errorf("%w: %s on %s", query.ErrUnsupported, feature, dialect)
}

// Err returns the first problem found while constructing the operation, or
// nil. The error matches [query.ErrInvalidQuery]. Dialect-specific problems
// are not reported here: they appear when a statement is compiled.
// This check performs no SQL rendering or I/O and does not change the value.
//
// Example:
//
//	op := schema.Create("users")
//	if err := op.Err(); err != nil {
//		return err // Missing columns are reported before execution.
//	}
func (operation Operation) Err() error {
	return operation.err
}

// Statements returns the operation's statements in execution order. Each one
// compiles lazily with BuildFor and yields a [query.Statement] without
// arguments. When [Operation.Err] is set, a single statement is returned
// and its BuildFor reports that error, so an unchecked operation never runs
// partially. The returned slice is a copy.
//
// Example:
//
//	op := schema.Create("tags", func(table *schema.Table) {
//		table.ID()
//		table.String("name", 50).Unique()
//	})
//	for _, statement := range op.Statements() { // CREATE TABLE, CREATE UNIQUE INDEX
//		compiled, err := statement.BuildFor(query.Postgres)
//		if err != nil {
//			return err
//		}
//		fmt.Println(compiled.SQL())
//	}
func (operation Operation) Statements() []query.Statementer {
	if operation.err != nil {
		return []query.Statementer{step{
			err: operation.err,
		}}
	}

	statements := make([]query.Statementer, len(operation.steps))

	for index, s := range operation.steps {
		statements[index] = s
	}

	return statements
}

// failed returns an Operation that reports err.
func failed(err error) Operation {
	return Operation{
		err: invalid(err),
	}
}

// Rename renames the table from to to. The new name must be unqualified: a
// table keeps its schema when renamed.
// Invalid identifiers are retained by Operation.Err. It does not check table
// existence or execute DDL; compilation remains lazy for the chosen dialect.
//
// Example:
//
//	op := schema.Rename("users", "members")
//	if err := op.Err(); err != nil {
//		return err
//	}
//	_ = op.Statements()
func Rename(from, to string) Operation {
	source, err := parseName(from, true)

	if err != nil {
		return failed(err)
	}

	target, err := parseName(to, false)

	if err != nil {
		return failed(err)
	}

	return Operation{
		steps: []step{{
			render: func(dialect query.Dialect) (string, error) {
				return "ALTER TABLE " + source.quoted(dialect) + " RENAME TO " + target.quoted(dialect), nil
			},
		}},
	}
}

// Drop drops the table. The name may be schema-qualified.
// Invalid identifiers fail construction; declaration and compilation never
// contact a database. Execute the result only at your migration boundary.
//
// Example:
//
//	op := schema.Drop("users")
//	statement, err := op.Statements()[0].BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
func Drop(table string) Operation {
	return drop(table, false)
}

// DropIfExists drops the table when it exists. ANSI has no such syntax, so
// compiling for [query.ANSI] fails with [query.ErrUnsupported].
// Invalid names fail construction; existence itself is checked by the database
// only when the compiled statement is executed.
//
// Example:
//
//	op := schema.DropIfExists("users")
//	statement, err := op.Statements()[0].BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
func DropIfExists(table string) Operation {
	return drop(table, true)
}

// drop builds the DROP TABLE operation shared by [Drop] and [DropIfExists].
func drop(table string, ifExists bool) Operation {
	name, err := parseName(table, true)

	if err != nil {
		return failed(err)
	}

	return Operation{
		steps: []step{{
			render: func(dialect query.Dialect) (string, error) {
				sql := "DROP TABLE "

				if ifExists {
					if dialect == query.ANSI {
						return "", unsupported(dialect, "DROP TABLE IF EXISTS")
					}

					sql += "IF EXISTS "
				}

				return sql + name.quoted(dialect), nil
			},
		}},
	}
}

// tableName is a validated, possibly schema-qualified table name split into
// its parts.
type tableName []string

// parseName validates a table name. Qualified names, such as schema.table,
// are accepted only when qualified is true. Every part must be an unquoted
// SQL identifier of at most [maxNameBytes] bytes.
func parseName(value string, qualified bool) (tableName, error) {
	parts := strings.Split(value, ".")

	for _, part := range parts {
		if err := checkName(part); err != nil {
			return nil, fmt.Errorf("invalid name %q: %w", value, err)
		}
	}

	if !qualified && len(parts) > 1 {
		return nil, fmt.Errorf("name %q cannot be qualified", value)
	}

	return parts, nil
}

// checkName validates one unqualified identifier.
func checkName(name string) error {
	if len(name) > maxNameBytes {
		return fmt.Errorf("%q is longer than %d bytes", name, maxNameBytes)
	}

	for index, character := range name {
		if character != '_' && !unicode.IsLetter(character) && (index == 0 || !unicode.IsDigit(character)) {
			return errors.New("not an unquoted SQL identifier")
		}
	}

	if name == "" {
		return errors.New("empty identifier")
	}

	return nil
}

// quoted renders the name quoted for dialect.
func (name tableName) quoted(dialect query.Dialect) string {
	quoted := make([]string, len(name))

	for index, part := range name {
		quoted[index] = dialect.Quote(part)
	}

	return strings.Join(quoted, ".")
}

// constraintName derives a deterministic name from every part of the table
// name, so a qualified table s.t yields s_t_col_key, plus the columns and
// suffix.
func constraintName(table tableName, columns []string, suffix string) (string, error) {
	name := strings.Join(append(slices.Clone(table), columns...), "_") + "_" + suffix

	if len(name) > maxNameBytes {
		return "", fmt.Errorf("generated name %q is longer than %d bytes", name, maxNameBytes)
	}

	return name, nil
}
