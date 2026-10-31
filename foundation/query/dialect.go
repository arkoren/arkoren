package query

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrUnsupported indicates that a dialect cannot express a statement or
// clause. Build errors that carry it also match [ErrInvalidQuery].
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.ForUpdate()
//	})
//	_, err := users.BuildFor(query.SQLite)
//	_ = errors.Is(err, query.ErrUnsupported)
var ErrUnsupported = errors.New("unsupported by dialect")

// dialectKind enumerates the dialects the compiler supports.
type dialectKind uint8

const (
	// ansiDialect is the zero kind, so the zero Dialect is ANSI.
	ansiDialect dialectKind = iota

	// sqliteDialect selects SQLite syntax.
	sqliteDialect

	// postgresDialect selects PostgreSQL syntax.
	postgresDialect

	// mysqlDialect selects MySQL syntax.
	mysqlDialect
)

// Dialect selects the SQL syntax a database expects for bind parameters and
// quoted identifiers. Dialects are opaque, comparable values; use [ANSI],
// [SQLite], [Postgres], or [MySQL]. The zero Dialect is ANSI.
//
// Example:
//
//	dialect := query.Postgres
//	statement, err := query.Table("users").BuildFor(dialect)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
type Dialect struct {
	// kind identifies the dialect.
	kind dialectKind
}

var (
	// ANSI uses ? placeholders and double-quoted identifiers. [Query.Build]
	// compiles for ANSI.
	//
	// Example:
	//
	//	statement, err := query.Table("users").BuildFor(query.ANSI)
	//	if err != nil {
	//		return err
	//	}
	//	_ = statement.SQL()
	ANSI = Dialect{
		kind: ansiDialect,
	}

	// SQLite uses ? placeholders and double-quoted identifiers.
	//
	// Example:
	//
	//	statement, err := query.Table("users").BuildFor(query.SQLite)
	//	if err != nil {
	//		return err
	//	}
	//	_ = statement.SQL()
	SQLite = Dialect{
		kind: sqliteDialect,
	}

	// Postgres uses numbered $n placeholders and double-quoted identifiers.
	//
	// Example:
	//
	//	statement, err := query.Table("users", func(statement *query.Builder) {
	//		statement.Where("id", 7)
	//	}).BuildFor(query.Postgres)
	//	if err != nil {
	//		return err
	//	}
	//	_ = statement.Args()
	Postgres = Dialect{
		kind: postgresDialect,
	}

	// MySQL uses ? placeholders and backtick-quoted identifiers, so it works
	// without the ANSI_QUOTES SQL mode.
	//
	// Example:
	//
	//	statement, err := query.Table("users").BuildFor(query.MySQL)
	//	if err != nil {
	//		return err
	//	}
	//	_ = statement.SQL()
	MySQL = Dialect{
		kind: mysqlDialect,
	}
)

// String returns the dialect name.
// Names are lowercase: ansi, sqlite, postgres, or mysql, including ansi for
// the zero Dialect. The result is diagnostic text, not a dialect constructor.
//
// Example:
//
//	_ = query.Postgres.String() // "postgres"
func (dialect Dialect) String() string {
	switch dialect.kind {
	case sqliteDialect:
		return "sqlite"
	case postgresDialect:
		return "postgres"
	case mysqlDialect:
		return "mysql"
	default:
		return "ansi"
	}
}

// placeholder renders the n-th bind parameter, counting from 1.
func (dialect Dialect) placeholder(n int) string {
	if dialect.kind == postgresDialect {
		return "$" + strconv.Itoa(n)
	}

	return "?"
}

// Quote wraps one identifier part in the dialect's quote character and
// doubles any quote character inside it. It does not split qualified names
// and does not validate: quote "schema.table" part by part.
//
// Example:
//
//	query.MySQL.Quote("users")    // `users`
//	query.Postgres.Quote("users") // "users"
func (dialect Dialect) Quote(part string) string {
	if dialect.kind == mysqlDialect {
		return "`" + strings.ReplaceAll(part, "`", "``") + "`"
	}

	return `"` + strings.ReplaceAll(part, `"`, `""`) + `"`
}

// TransactionalDDL reports whether the dialect rolls back schema changes with
// the surrounding transaction. PostgreSQL and SQLite do. MySQL implicitly
// commits around most DDL statements. It is also false for ANSI: the standard
// does not require transactional DDL and real databases differ, so the
// compiler promises nothing for the generic dialect.
//
// Example:
//
//	transactional := query.Postgres.TransactionalDDL()
//	_ = transactional // true; schema execution can roll back with its transaction.
func (dialect Dialect) TransactionalDDL() bool {
	return dialect.kind == postgresDialect || dialect.kind == sqliteDialect
}

// unsupported reports that dialect cannot express feature.
func (dialect Dialect) unsupported(feature string) error {
	return fmt.Errorf("%w: %s on %s", ErrUnsupported, feature, dialect)
}
