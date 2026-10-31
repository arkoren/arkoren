package query

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Statementer is implemented by everything that compiles to exactly one
// [Statement]: the DML [Query], [RawStatement], and each statement of a
// schema operation ([arkoren.dev/foundation/query/schema.Operation]).
// Mutable declaration drafts do not implement it; execution boundaries accept
// completed definitions and compile them for the database's actual dialect.
//
// Example:
//
//	var definition query.Statementer = query.Table("users")
//	statement, err := definition.BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
type Statementer interface {
	// BuildFor compiles a published definition for dialect, returning SQL and
	// ordered arguments or the definition's validation or dialect error.
	//
	// Example:
	//
	//	var definition query.Statementer = query.Table("users")
	//	statement, err := definition.BuildFor(query.Postgres)
	//	if err != nil {
	//		return err
	//	}
	//	_ = statement.Args()
	BuildFor(dialect Dialect) (Statement, error)
}

// Statement is compiled SQL and its ordered positional arguments.
// Obtain it through Query or RawStatement compilation. SQL returns immutable
// text; Args copies its outer slice, not the objects stored in argument slots.
// The zero Statement contains empty SQL and no arguments.
//
// Example:
//
//	statement, err := query.Raw("SELECT ?", 7).BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()  // "SELECT $1"
//	_ = statement.Args() // []any{7}
type Statement struct {
	// query is dialect-specific SQL with positional placeholders.
	query string

	// args holds values in placeholder order; Args returns a detached slice.
	args []any
}

// SQL returns the compiled SQL text.
// It contains placeholders rather than interpolated argument values; pass
// Args separately to the database. The zero Statement returns an empty string.
//
// Example:
//
//	statement, err := query.Table("users").Build()
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL() // SELECT * FROM "users"
func (statement Statement) SQL() string {
	return statement.query
}

// Args returns a copy of the ordered positional arguments.
// Editing the returned slice cannot change the statement. Argument objects
// themselves remain shared; no arguments returns nil.
//
// Example:
//
//	statement, err := query.Raw("SELECT ?", 7).Build()
//	if err != nil {
//		return err
//	}
//	arguments := statement.Args()
//	arguments[0] = 9 // A later statement.Args() still contains 7.
func (statement Statement) Args() []any {
	return slices.Clone(statement.args)
}

// RawStatement is caller-written SQL with positional ? or named :name
// placeholders, constructed by Raw or RawNamed. Compilation checks nonblank
// text and bindings, not SQL syntax. Use it for statements the structured API
// cannot express. Never build SQL from user input; pass values as arguments.
// Owned argument containers are copied; objects in their slots remain shared.
//
// Example:
//
//	definition := query.Raw("SELECT id FROM users WHERE active = ?", true)
//	statement, err := definition.BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
type RawStatement struct {
	// query retains trusted SQL until dialect-specific placeholder rewriting.
	query string

	// args holds copied positional arguments; their underlying objects are shared.
	args []any

	// named holds the arguments of a [RawNamed] statement, whose placeholders
	// are :name instead of ?.
	named map[string]any

	// isNamed selects the :name placeholder syntax.
	isNamed bool
}

// Raw creates a [RawStatement] from sql and its arguments. Placeholders follow
// the raw expression rules of [Builder.HavingRaw]: ? outside quotes and
// comments becomes the dialect placeholder, and ?? writes a literal ?. The
// argument slice is copied; argument objects themselves remain shared.
//
// Example:
//
//	statement, err := query.Raw("SELECT ?", 7).BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL() // "SELECT $1"
func Raw(sql string, args ...any) RawStatement {
	return RawStatement{
		query: sql,
		args:  slices.Clone(args),
	}
}

// RawNamed creates a [RawStatement] whose placeholders are names: each :name
// outside quotes and comments becomes the dialect placeholder and binds
// args["name"]. A name is a letter or underscore followed by letters, digits,
// and underscores, and may repeat, binding its value again each time. "::" is
// kept as written, so PostgreSQL casts such as id::text work. A ? is not a
// placeholder here and is kept as written. Quotes and comments follow the
// rules of [Raw]. A name without an entry in args fails at Build with
// [ErrInvalidQuery]; entries that no name uses are ignored, so the map
// returned by [Values] can be passed whole. The map is copied.
//
// Example:
//
//	statement := query.RawNamed("UPDATE users SET name = :name WHERE id = :id",
//		map[string]any{"name": "Ada", "id": 7})
func RawNamed(sql string, args map[string]any) RawStatement {
	return RawStatement{
		query:   sql,
		named:   maps.Clone(args),
		isNamed: true,
	}
}

// Build compiles the statement for the [ANSI] dialect.
// Binding failures have the same ErrInvalidQuery classification as BuildFor.
// It does not validate the SQL syntax or execute the statement.
//
// Example:
//
//	statement, err := query.Raw("SELECT ?", 7).Build()
//	if err != nil {
//		return err
//	}
//	_ = statement.Args()
func (statement RawStatement) Build() (Statement, error) {
	return statement.BuildFor(ANSI)
}

// BuildFor rewrites the placeholders for dialect. It fails with
// [ErrInvalidQuery] when the SQL is blank, the positional placeholder count
// differs from its argument count, or a named placeholder has no argument.
// Unused entries in a named argument map are ignored, as described by RawNamed.
// It leaves the definition unchanged and does not validate SQL syntax.
//
// Example:
//
//	definition := query.RawNamed("SELECT :id", map[string]any{"id": 7})
//	statement, err := definition.BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL() // "SELECT $1"
func (statement RawStatement) BuildFor(dialect Dialect) (Statement, error) {
	if strings.TrimSpace(statement.query) == "" {
		return Statement{}, fmt.Errorf("%w: %w", ErrInvalidQuery, errors.New("raw statement cannot be empty"))
	}

	statementCompiler := compiler{
		dialect: dialect,
	}

	if statement.isNamed {
		statementCompiler.writeNamed(statement.query, statement.named)
	} else {
		statementCompiler.writeRaw(statement.query, statement.args)
	}

	if statementCompiler.err != nil {
		return Statement{}, fmt.Errorf("%w: %w", ErrInvalidQuery, statementCompiler.err)
	}

	return Statement{
		query: statementCompiler.String(),
		args:  statementCompiler.args,
	}, nil
}
