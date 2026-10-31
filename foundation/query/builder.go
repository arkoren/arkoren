package query

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// BuilderFunc edits the shared construction draft used by Table, FromRaw,
// and Query.Derive. It is an alias, so ordinary callbacks and callback slices
// are accepted without conversion. Supplied callbacks run synchronously;
// publication detaches owned draft state, not arbitrary argument objects.
//
// Example:
//
//	policies := []query.BuilderFunc{
//		func(statement *query.Builder) {
//			statement.Where("active", true)
//		},
//	}
//	users := query.Table("users", policies...)
//	_ = users
type BuilderFunc = func(*Builder)

// errSourceMutation rejects turning a raw-sourced SELECT into a mutation.
var errSourceMutation = errors.New("a FromRaw source supports only select")

// ErrInvalidQuery identifies validation or compilation failures from Query and
// RawStatement. Build errors wrap the underlying cause as well as this sentinel,
// so callers can classify them without depending on diagnostic text.
//
// Example:
//
//	_, err := query.Table("invalid table").Build()
//	_ = errors.Is(err, query.ErrInvalidQuery)
var ErrInvalidQuery = errors.New("invalid query")

// Kind identifies the SQL statement a [Query] compiles to.
// Its zero value is KindSelect; obtain the actual kind from Query.Inspect.
//
// Example:
//
//	kind := query.Table("users").Inspect().Kind
//	_ = kind.String() // "SELECT"
type Kind uint8

const (
	// KindSelect is the zero kind, so a new Builder selects.
	//
	// Example:
	//
	//	_ = query.Table("users").Inspect().Kind == query.KindSelect
	KindSelect Kind = iota

	// KindInsert compiles rows into INSERT.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.Insert(map[string]any{"name": "Ada"})
	//	})
	//	_ = users.Inspect().Kind == query.KindInsert
	KindInsert

	// KindUpdate compiles updates and predicates into UPDATE.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.Update(map[string]any{"name": "Ada"})
	//		statement.Where("id", 7)
	//	})
	//	_ = users.Inspect().Kind == query.KindUpdate
	KindUpdate

	// KindDelete compiles predicates into DELETE.
	//
	// Example:
	//
	//	users := query.Table("users", func(statement *query.Builder) {
	//		statement.Delete()
	//		statement.Where("id", 7)
	//	})
	//	_ = users.Inspect().Kind == query.KindDelete
	KindDelete
)

// String returns the statement keyword: SELECT, INSERT, UPDATE, or DELETE.
// Values outside the declared kinds retain the SELECT fallback.
//
// Example:
//
//	_ = query.Table("users").Inspect().Kind.String() // "SELECT"
func (kind Kind) String() string {
	switch kind {
	case KindInsert:
		return "INSERT"
	case KindUpdate:
		return "UPDATE"
	case KindDelete:
		return "DELETE"
	default:
		return "SELECT"
	}
}

// joinKind identifies a join operation.
type joinKind string

const (
	// innerJoin retains rows with a matching join condition.
	innerJoin joinKind = "INNER JOIN"

	// leftJoin retains unmatched rows from the left side.
	leftJoin joinKind = "LEFT JOIN"

	// rightJoin retains unmatched rows from the right side.
	rightJoin joinKind = "RIGHT JOIN"

	// crossJoin forms a Cartesian product without a join condition.
	crossJoin joinKind = "CROSS JOIN"
)

// join describes one structured table join.
type join struct {
	// kind selects the join keyword and whether a condition is rendered.
	kind joinKind

	// table is the validated name of the joined table.
	table identifier

	// first is the left column of the join comparison.
	first identifier

	// operator is the normalized comparison operator.
	operator string

	// second is the right column of the join comparison.
	second identifier
}

// order describes one structured ordering.
type order struct {
	// column is the validated ordering column.
	column identifier

	// direction is ASC or DESC and is reversed for backward seeks.
	direction string
}

// assignment associates a column with a bound value.
type assignment struct {
	// column is the validated target column; assignments sort by this name.
	column identifier

	// value is bound as an argument, not interpolated into SQL.
	value any
}

// lockMode identifies the row lock a SELECT requests.
type lockMode uint8

const (
	// lockNone is the zero mode: no row locking.
	lockNone lockMode = iota

	// lockUpdate requests FOR UPDATE.
	lockUpdate

	// lockShare requests FOR SHARE.
	lockShare
)

// source is the derived table of a SELECT built by [FromRaw] or
// [Query.Count]: either caller-supplied SQL or another published query.
type source struct {
	// raw retains named or positional binding until compilation.
	raw RawStatement

	// inner is a published query compiled into the same statement, so placeholder
	// numbering continues across the subquery.
	inner *Query
}

// expression is a computed UPDATE assignment: column = column op ?, or
// column = raw SQL.
type expression struct {
	// column is the target of the computed UPDATE assignment.
	column identifier

	// operator is "+" or "-" for [Builder.Increment] and
	// [Builder.Decrement]; it is empty for raw expressions.
	operator string

	// raw is the SQL assigned by [Builder.SetRaw].
	raw string

	// args binds the placeholders of raw, or holds the single amount of an
	// increment or decrement.
	args []any
}

// upsert describes the conflict handling of an INSERT.
type upsert struct {
	// target lists the conflict columns; MySQL ignores it.
	target []identifier

	// update lists the columns overwritten from the inserted row.
	update []identifier

	// nothing skips conflicting rows instead of updating them.
	nothing bool
}

// queryState owns the SQL representation of drafts and published queries.
// Containers detach at publication; arbitrary SQL argument objects stay shared.
type queryState struct {
	// kind selects the statement compiler; its zero value means SELECT.
	kind Kind

	// table is the validated target unless source supplies a derived table.
	table identifier

	// source replaces table with a derived table in SELECT statements.
	source *source

	// alias names a derived table; an empty alias uses the compiler default.
	alias identifier

	// columns is the select list; empty compiles as *.
	columns []identifier

	// distinct requests duplicate elimination in SELECT.
	distinct bool

	// joins preserves the order of declared table joins.
	joins []join

	// predicates is the WHERE clause, shared by SELECT, UPDATE, and DELETE.
	predicates []predicate

	// groups holds the GROUP BY columns in declaration order.
	groups []identifier

	// havings filters grouped results independently of WHERE predicates.
	havings []predicate

	// orders holds ordering columns and directions, including seek ordering.
	orders []order

	// limit caps result rows; nil omits the clause and differs from explicit zero.
	limit *int

	// offset skips rows; nil omits the clause and differs from explicit zero.
	offset *int

	// rows holds INSERT rows; every row has the same sorted columns.
	rows [][]assignment

	// updates holds sorted literal UPDATE assignments, before expressions.
	updates []assignment

	// expressions are computed UPDATE assignments, rendered after updates.
	expressions []expression

	// conflict adds upsert handling to INSERT.
	conflict *upsert

	// returning lists the columns an INSERT, UPDATE, or DELETE returns.
	returning []identifier

	// lock selects the SELECT row-lock clause; zero disables row locking.
	lock lockMode

	// skipLocked omits rows whose locks cannot be acquired immediately.
	skipLocked bool

	// noWait requests an immediate error rather than waiting for a row lock.
	noWait bool

	// scopes are predicate groups ANDed after predicates at compile time, so
	// later OR predicates cannot bypass them.
	scopes []predicate

	// unfiltered permits UPDATE and DELETE without predicates.
	unfiltered bool

	// err is the first error recorded; later calls leave it in place.
	err error
}

// Builder declares mutable SQL settings during construction.
// Settings return this same draft. Drafts cannot compile or execute, and are
// not concurrency-safe. Publication detaches their owned state.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Where("active", true)
//	})
//	_ = users
type Builder struct {
	// queryState holds the draft's private mutable representation.
	queryState
}

// Table publishes a query targeting name after invoking configure in order.
// Callbacks share one draft and run synchronously once; supplied nil callbacks
// panic. Invalid names return an invalid query before callbacks are invoked.
// No callbacks, including an expanded empty slice, selects every column.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Select("id", "name")
//	})
//	_ = users
func Table(name string, configure ...BuilderFunc) Query {
	table, err := parseIdentifier(name, false)

	draft := &Builder{
		queryState: queryState{
			kind:  KindSelect,
			table: table,
			err:   err,
		},
	}

	if err != nil {
		return Query{
			queryState: draft.queryState,
		}
	}

	for _, configure := range configure {
		configure(draft)
	}

	return Query{
		queryState: draft.snapshot(),
	}
}

// From sets the query's source table.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.From("accounts")
//	})
//	_ = users
func (statement *Builder) From(name string) *Builder {
	if statement.err != nil {
		return statement
	}

	table, err := parseIdentifier(name, false)

	statement.table = table
	statement.source = nil
	statement.alias = identifier{}
	statement.err = err

	return statement
}

// FromRaw publishes a SELECT over a raw subquery, with default alias "t".
// Use As to change the alias. Positional or named source arguments bind before
// outer predicates through the normal dialect compiler, not precompiled SQL.
// Empty SQL and source mutations are invalid. Callback invocation and ownership
// follow Table; trusted SQL is required because source text is not escaped.
//
// Example:
//
//	users := query.FromRaw(query.Raw("SELECT id FROM users WHERE active = ?", true),
//		func(statement *query.Builder) {
//			statement.Where("id", ">", 10)
//		})
//	_ = users
func FromRaw(statement RawStatement, configure ...BuilderFunc) Query {
	_, err := rawIdentifier(statement.query)

	if err != nil {
		return Query{
			queryState: queryState{
				err: err,
			},
		}
	}

	draft := &Builder{
		queryState: queryState{
			source: &source{
				raw: statement,
			},
		},
	}

	for _, configure := range configure {
		configure(draft)
	}

	return Query{
		queryState: draft.snapshot(),
	}
}

// As names the subquery source of a [FromRaw] query. It is an error on
// builders without a raw source: plain tables cannot be aliased, and a table
// name with an alias, such as "posts p", is an invalid identifier.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.FromRaw(query.Raw("SELECT id FROM users"), func(statement *query.Builder) {
//		statement.As("members")
//	})
//	_ = users
func (statement *Builder) As(alias string) *Builder {
	if statement.err != nil {
		return statement
	}

	if statement.source == nil {
		return statement.withError(errors.New("as requires a FromRaw source"))
	}

	parsed, err := parseIdentifier(alias, false)

	if err != nil || len(parsed.parts) != 1 {
		return statement.withError(fmt.Errorf("invalid alias %q", alias))
	}

	statement.alias = parsed

	return statement
}

// Select replaces the selected columns. An empty selection compiles as *.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Select("id", "name")
//	})
//	_ = users
func (statement *Builder) Select(columns ...string) *Builder {
	if statement.err != nil {
		return statement
	}

	statement.columns = nil

	return statement.AddSelect(columns...)
}

// AddSelect appends selected columns.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.AddSelect("email")
//	})
//	_ = users
func (statement *Builder) AddSelect(columns ...string) *Builder {
	if statement.err != nil {
		return statement
	}

	parsed, err := parseIdentifiers(columns, true)

	if err != nil {
		return statement.withError(err)
	}

	statement.columns = append(statement.columns, parsed...)

	return statement
}

// SelectRaw appends expression, such as "COUNT(*) AS total", to the
// selected columns verbatim. An empty expression is an error. Never pass user
// input: it is not escaped. Unlike [Builder.HavingRaw], SelectRaw takes no
// arguments and does not scan placeholders: a ? is copied as written, so use
// the single ? for PostgreSQL's JSON operator, not the ?? binding escape.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.SelectRaw("COUNT(*) AS total")
//	})
//	_ = users
func (statement *Builder) SelectRaw(expression string) *Builder {
	if statement.err != nil {
		return statement
	}

	raw, err := rawIdentifier(expression)

	if err != nil {
		return statement.withError(err)
	}

	statement.columns = append(statement.columns, raw)

	return statement
}

// HavingRaw appends an AND aggregate filter rendered verbatim, such as
// "COUNT(*) > ?", with args bound to its placeholders. An empty expression is
// an error, and the number of ? placeholders must equal len(args); write ??
// for a literal question mark. Never pass user input as expression: it is not
// escaped.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.SelectRaw("COUNT(*) AS total")
//		statement.HavingRaw("COUNT(*) > ?", 5)
//	})
//	_ = users
func (statement *Builder) HavingRaw(expression string, args ...any) *Builder {
	if statement.err != nil {
		return statement
	}

	built, err := rawPredicate(booleanAnd, expression, args)

	if err != nil {
		return statement.withError(err)
	}

	statement.havings = append(statement.havings, built)

	return statement
}

// WhereRaw appends an AND filter rendered verbatim, such as
// "lower(email) = ?", with args bound to its placeholders. The placeholder
// rules of [Builder.HavingRaw] apply. Never pass user input as expression.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereRaw("lower(email) = ?", "ada@example.test")
//	})
//	_ = users
func (statement *Builder) WhereRaw(expression string, args ...any) *Builder {
	return statement.addRaw(booleanAnd, expression, args)
}

// OrWhereRaw appends an OR filter rendered verbatim. See [Builder.WhereRaw].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereRaw("lower(email) = ?", "ada@example.test")
//	})
//	_ = users
func (statement *Builder) OrWhereRaw(expression string, args ...any) *Builder {
	return statement.addRaw(booleanOr, expression, args)
}

// IsSelect reports whether the query compiles to a SELECT statement, as
// opposed to an INSERT, UPDATE, or DELETE.
//
// Example:
//
//	users := query.Table("users")
//	_ = users.IsSelect()
func (query Query) IsSelect() bool {
	return query.kind == KindSelect
}

// Unfiltered allows an UPDATE or DELETE without a WHERE clause, affecting
// every row. Without it, Build rejects such statements. It has no effect on
// SELECT or INSERT.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Delete()
//		statement.Unfiltered()
//	})
//	_ = users
func (statement *Builder) Unfiltered() *Builder {
	statement.unfiltered = true

	return statement
}

// Distinct makes the select statement return distinct rows.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Distinct()
//	})
//	_ = users
func (statement *Builder) Distinct() *Builder {
	statement.distinct = true

	return statement
}

// Where appends an AND value comparison.
// Pass (column, value) for equality or (column, operator, value) for an
// explicit supported operator. Equality/inequality with typed or untyped nil
// becomes IS NULL/IS NOT NULL. Invalid arguments record a sticky error.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Where("active", true)
//	})
//	_ = users
func (statement *Builder) Where(column string, args ...any) *Builder {
	return statement.addComparison(booleanAnd, column, args...)
}

// OrWhere appends an OR value comparison.
// Argument forms and nil handling follow [Builder.Where].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhere("role", "admin")
//	})
//	_ = users
func (statement *Builder) OrWhere(column string, args ...any) *Builder {
	return statement.addComparison(booleanOr, column, args...)
}

// WhereNull appends an AND null comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereNull("deleted_at")
//	})
//	_ = users
func (statement *Builder) WhereNull(column string) *Builder {
	return statement.addNull(booleanAnd, column, false)
}

// OrWhereNull appends an OR null comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereNull("deleted_at")
//	})
//	_ = users
func (statement *Builder) OrWhereNull(column string) *Builder {
	return statement.addNull(booleanOr, column, false)
}

// WhereNotNull appends an AND not-null comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereNotNull("email")
//	})
//	_ = users
func (statement *Builder) WhereNotNull(column string) *Builder {
	return statement.addNull(booleanAnd, column, true)
}

// OrWhereNotNull appends an OR not-null comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereNotNull("email")
//	})
//	_ = users
func (statement *Builder) OrWhereNotNull(column string) *Builder {
	return statement.addNull(booleanOr, column, true)
}

// WhereIn appends an AND membership comparison.
// An empty value list, including an expanded nil slice, is always false and
// binds no arguments. Use an explicit type argument when no values infer T.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereIn("role", "admin", "editor")
//	})
//	_ = users
func (statement *Builder) WhereIn[T any](column string, values ...T) *Builder {
	return statement.addIn(booleanAnd, column, anyValues(values), false)
}

// OrWhereIn appends an OR membership comparison.
// Empty lists are always false and bind no arguments, as in [Builder.WhereIn].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereIn("role", "admin", "editor")
//	})
//	_ = users
func (statement *Builder) OrWhereIn[T any](column string, values ...T) *Builder {
	return statement.addIn(booleanOr, column, anyValues(values), false)
}

// WhereNotIn appends an AND negative membership comparison.
// An empty value list, including an expanded nil slice, is always true and
// binds no arguments; it does not restrict an UPDATE or DELETE to fewer rows.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereNotIn("role", "guest")
//	})
//	_ = users
func (statement *Builder) WhereNotIn[T any](column string, values ...T) *Builder {
	return statement.addIn(booleanAnd, column, anyValues(values), true)
}

// OrWhereNotIn appends an OR negative membership comparison.
// Empty lists are always true and bind no arguments, as in [Builder.WhereNotIn].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereNotIn("role", "guest")
//	})
//	_ = users
func (statement *Builder) OrWhereNotIn[T any](column string, values ...T) *Builder {
	return statement.addIn(booleanOr, column, anyValues(values), true)
}

// WhereBetween appends an AND inclusive range comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereBetween("score", 1, 10)
//	})
//	_ = users
func (statement *Builder) WhereBetween(column string, minimum, maximum any) *Builder {
	return statement.addBetween(booleanAnd, column, minimum, maximum, false)
}

// OrWhereBetween appends an OR inclusive range comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereBetween("score", 1, 10)
//	})
//	_ = users
func (statement *Builder) OrWhereBetween(column string, minimum, maximum any) *Builder {
	return statement.addBetween(booleanOr, column, minimum, maximum, false)
}

// WhereNotBetween appends an AND negative range comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereNotBetween("score", 1, 10)
//	})
//	_ = users
func (statement *Builder) WhereNotBetween(column string, minimum, maximum any) *Builder {
	return statement.addBetween(booleanAnd, column, minimum, maximum, true)
}

// OrWhereNotBetween appends an OR negative range comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereNotBetween("score", 1, 10)
//	})
//	_ = users
func (statement *Builder) OrWhereNotBetween(column string, minimum, maximum any) *Builder {
	return statement.addBetween(booleanOr, column, minimum, maximum, true)
}

// WhereColumn appends an AND comparison between columns.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereColumn("created_at", "<", "updated_at")
//	})
//	_ = users
func (statement *Builder) WhereColumn(first string, args ...string) *Builder {
	return statement.addColumn(booleanAnd, first, args...)
}

// OrWhereColumn appends an OR comparison between columns.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereColumn("created_at", "<", "updated_at")
//	})
//	_ = users
func (statement *Builder) OrWhereColumn(first string, args ...string) *Builder {
	return statement.addColumn(booleanOr, first, args...)
}

// WhereGroup appends a parenthesized AND predicate group.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
// Callbacks run directly once, synchronously in order on one restricted
// draft; nil entries panic. The final group must contain predicates, even
// when callbacks are omitted or an expanded empty slice is supplied.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.Where("active", true)
//		})
//	})
//	_ = users
func (statement *Builder) WhereGroup(configure ...PredicateBuilderFunc) *Builder {
	return statement.addGroup(booleanAnd, false, configure...)
}

// OrWhereGroup appends a parenthesized OR predicate group.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
// Callbacks run directly once, synchronously in order on one restricted
// draft; nil entries panic. The final group must contain predicates, even
// when callbacks are omitted or an expanded empty slice is supplied.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereGroup(func(predicate *query.PredicateBuilder) {
//			predicate.Where("active", true)
//		})
//	})
//	_ = users
func (statement *Builder) OrWhereGroup(configure ...PredicateBuilderFunc) *Builder {
	return statement.addGroup(booleanOr, false, configure...)
}

// WhereNotGroup appends a negated AND predicate group.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
// Callbacks run directly once, synchronously in order on one restricted
// draft; nil entries panic. The final group must contain predicates, even
// when callbacks are omitted or an expanded empty slice is supplied.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.WhereNotGroup(func(predicate *query.PredicateBuilder) {
//			predicate.Where("active", true)
//		})
//	})
//	_ = users
func (statement *Builder) WhereNotGroup(configure ...PredicateBuilderFunc) *Builder {
	return statement.addGroup(booleanAnd, true, configure...)
}

// OrWhereNotGroup appends a negated OR predicate group.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
// Callbacks run directly once, synchronously in order on one restricted
// draft; nil entries panic. The final group must contain predicates, even
// when callbacks are omitted or an expanded empty slice is supplied.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrWhereNotGroup(func(predicate *query.PredicateBuilder) {
//			predicate.Where("active", true)
//		})
//	})
//	_ = users
func (statement *Builder) OrWhereNotGroup(configure ...PredicateBuilderFunc) *Builder {
	return statement.addGroup(booleanOr, true, configure...)
}

// Scope is a reusable function that adds predicates to a query.
//
// Example:
//
//	active := func(predicate *query.PredicateBuilder) {
//		predicate.Where("active", true)
//	}
//	_ = query.Table("users", func(statement *query.Builder) {
//		statement.Scope(active)
//	})
type Scope = PredicateBuilderFunc

// Scope adds each scope's predicates as a parenthesized group that is ANDed
// after the entire nonempty user predicate chain, itself parenthesized when
// the statement compiles. Thus an OR in WhereRaw or a later OrWhere cannot
// bypass a scope. Raw expressions must still be trusted SQL with bound values;
// grouping does not validate or sandbox arbitrary SQL. Scopes also apply to
// UPDATE, DELETE, and [Query.Count], and satisfy the guard that
// [Builder.Unfiltered] lifts. Each callback receives an independent restricted
// draft, once in order. No callbacks or an empty scope adds nothing; supplied
// nil callbacks panic. The restricted surface permits only predicates, and
// INSERT rejects scopes. Publication detaches retained predicate drafts.
//
// Example:
//
//	active := func(predicate *query.PredicateBuilder) {
//		predicate.Where("active", true)
//	}
//	statement, err := query.Table("users", func(statement *query.Builder) {
//		statement.Where("role", "admin")
//		statement.Scope(active)
//	}).Build()
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
func (statement *Builder) Scope(scopes ...Scope) *Builder {
	for _, scope := range scopes {
		statement.addScope(scope)
	}

	return statement
}

// WhereExists appends an AND EXISTS (sub) filter. The subquery must be a
// SELECT; correlate it with [Builder.WhereColumn]. MySQL rejects an UPDATE
// or DELETE whose subquery reads the target table (error 1093).
//
// Example:
//
//	members := query.Table("members", func(statement *query.Builder) {
//		statement.WhereColumn("members.group_id", "posts.group_id")
//		statement.Where("members.user_id", 7)
//	})
//	_ = query.Table("posts", func(statement *query.Builder) {
//		statement.WhereExists(members)
//	})
func (statement *Builder) WhereExists(sub Query) *Builder {
	return statement.addExists(booleanAnd, sub, false)
}

// WhereNotExists appends an AND NOT EXISTS (sub) filter. See
// [Builder.WhereExists].
//
// Example:
//
//	banned := query.Table("bans", func(statement *query.Builder) {
//		statement.WhereColumn("bans.user_id", "users.id")
//	})
//	_ = query.Table("users", func(statement *query.Builder) {
//		statement.WhereNotExists(banned)
//	})
func (statement *Builder) WhereNotExists(sub Query) *Builder {
	return statement.addExists(booleanAnd, sub, true)
}

// OrWhereExists appends an OR EXISTS (sub) filter. See [Builder.WhereExists].
//
// Example:
//
//	owned := query.Table("owners", func(statement *query.Builder) {
//		statement.WhereColumn("owners.post_id", "posts.id")
//	})
//	_ = query.Table("posts", func(statement *query.Builder) {
//		statement.Where("public", true)
//		statement.OrWhereExists(owned)
//	})
func (statement *Builder) OrWhereExists(sub Query) *Builder {
	return statement.addExists(booleanOr, sub, false)
}

// OrWhereNotExists appends an OR NOT EXISTS (sub) filter. See
// [Builder.WhereExists].
//
// Example:
//
//	hidden := query.Table("hides", func(statement *query.Builder) {
//		statement.WhereColumn("hides.post_id", "posts.id")
//	})
//	_ = query.Table("posts", func(statement *query.Builder) {
//		statement.Where("public", true)
//		statement.OrWhereNotExists(hidden)
//	})
func (statement *Builder) OrWhereNotExists(sub Query) *Builder {
	return statement.addExists(booleanOr, sub, true)
}

// Join appends an inner join with a column comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Join("accounts", "accounts.id", "=", "users.account_id")
//	})
//	_ = users
func (statement *Builder) Join(table, first, operator, second string) *Builder {
	return statement.addJoin(innerJoin, table, first, operator, second)
}

// LeftJoin appends a left join with a column comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.LeftJoin("accounts", "accounts.id", "=", "users.account_id")
//	})
//	_ = users
func (statement *Builder) LeftJoin(table, first, operator, second string) *Builder {
	return statement.addJoin(leftJoin, table, first, operator, second)
}

// RightJoin appends a right join with a column comparison.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.RightJoin("accounts", "accounts.id", "=", "users.account_id")
//	})
//	_ = users
func (statement *Builder) RightJoin(table, first, operator, second string) *Builder {
	return statement.addJoin(rightJoin, table, first, operator, second)
}

// CrossJoin appends a cross join.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.CrossJoin("roles")
//	})
//	_ = users
func (statement *Builder) CrossJoin(table string) *Builder {
	return statement.addJoin(crossJoin, table, "", "", "")
}

// GroupBy appends grouping columns.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Select("role")
//		statement.GroupBy("role")
//	})
//	_ = users
func (statement *Builder) GroupBy(columns ...string) *Builder {
	if statement.err != nil {
		return statement
	}

	parsed, err := parseIdentifiers(columns, false)

	if err != nil {
		return statement.withError(err)
	}

	statement.groups = append(statement.groups, parsed...)

	return statement
}

// Having appends an AND aggregate filter.
// Use a compatible grouped or aggregate selection; the database validates
// grouping semantics. Comparison arguments follow [Builder.Where].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Select("role")
//		statement.GroupBy("role")
//		statement.Having("role", "admin")
//	})
//	_ = users
func (statement *Builder) Having(column string, args ...any) *Builder {
	return statement.addHaving(booleanAnd, column, args...)
}

// OrHaving appends an OR aggregate filter.
// Selection and grouping requirements are those of [Builder.Having].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Select("role")
//		statement.GroupBy("role")
//		statement.Having("role", "admin")
//		statement.OrHaving("role", "owner")
//	})
//	_ = users
func (statement *Builder) OrHaving(column string, args ...any) *Builder {
	return statement.addHaving(booleanOr, column, args...)
}

// OrderBy appends ascending ordering for column.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrderBy("id")
//	})
//	_ = users
func (statement *Builder) OrderBy(column string) *Builder {
	return statement.addOrder(column, "ASC")
}

// OrderByDesc appends descending ordering for column.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrderByDesc("created_at")
//	})
//	_ = users
func (statement *Builder) OrderByDesc(column string) *Builder {
	return statement.addOrder(column, "DESC")
}

// Latest orders descending by column.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Latest("created_at")
//	})
//	_ = users
func (statement *Builder) Latest(column string) *Builder {
	return statement.OrderByDesc(column)
}

// Oldest orders ascending by column.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Oldest("created_at")
//	})
//	_ = users
func (statement *Builder) Oldest(column string) *Builder {
	return statement.OrderBy(column)
}

// Limit restricts the maximum number of returned rows. Only SELECT accepts
// it; INSERT, UPDATE, and DELETE reject it at Build.
// Omission leaves the result unbounded. Zero explicitly requests no rows;
// negative input records an error that later calls cannot repair.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Limit(20)
//	})
//	_ = users
func (statement *Builder) Limit(limit int) *Builder {
	if limit < 0 {
		return statement.withError(errors.New("limit cannot be negative"))
	}

	statement.limit = &limit

	return statement
}

// Offset skips rows before returning results. It may be used without Limit;
// each dialect then emits its own unbounded-limit form. Only SELECT accepts
// it.
// Omission adds no offset clause. Zero explicitly skips no rows and remains
// visible through Inspect; negative input records a sticky validation error.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Offset(40)
//	})
//	_ = users
func (statement *Builder) Offset(offset int) *Builder {
	if offset < 0 {
		return statement.withError(errors.New("offset cannot be negative"))
	}

	statement.offset = &offset

	return statement
}

// Insert changes the query into a single-row insert statement.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Insert(map[string]any{"name": "Ada"})
//	})
//	_ = users
func (statement *Builder) Insert[M ~map[string]V, V any](values M) *Builder {
	return statement.InsertRows(values)
}

// InsertRows changes the query into a multi-row insert statement.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.InsertRows(map[string]any{"name": "Ada"}, map[string]any{"name": "Grace"})
//	})
//	_ = users
func (statement *Builder) InsertRows[M ~map[string]V, V any](values ...M) *Builder {
	if statement.err != nil {
		return statement
	}

	if statement.source != nil {
		return statement.withError(errSourceMutation)
	}

	rows, err := makeRows(values)

	statement.kind = KindInsert
	statement.rows = rows
	statement.err = err

	return statement
}

// Update changes the query into an update statement. It replaces earlier
// Update values but keeps expressions added by [Builder.Increment],
// [Builder.Decrement], and [Builder.SetRaw].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Where("id", 7)
//		statement.Update(map[string]any{"name": "Ada"})
//	})
//	_ = users
func (statement *Builder) Update[M ~map[string]V, V any](values M) *Builder {
	if statement.err != nil {
		return statement
	}

	if statement.source != nil {
		return statement.withError(errSourceMutation)
	}

	updates, err := makeAssignments(values)

	statement.kind = KindUpdate
	statement.updates = updates
	statement.err = err

	return statement
}

// Delete changes the query into a delete statement.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Where("id", 7)
//		statement.Delete()
//	})
//	_ = users
func (statement *Builder) Delete() *Builder {
	if statement.source != nil {
		return statement.withError(errSourceMutation)
	}

	statement.kind = KindDelete

	return statement
}

// Increment changes the query into an update statement that adds by to
// column, rendered as column = column + ?. It composes with [Builder.Update]
// and other expressions; assigning one column twice fails at Build. A nil
// amount is an error.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Where("id", 7)
//		statement.Increment("visits", 1)
//	})
//	_ = users
func (statement *Builder) Increment(column string, by any) *Builder {
	return statement.addExpression(column, "+", "", []any{by})
}

// Decrement changes the query into an update statement that subtracts by
// from column, rendered as column = column - ?. See [Builder.Increment].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Where("id", 7)
//		statement.Decrement("credits", 1)
//	})
//	_ = users
func (statement *Builder) Decrement(column string, by any) *Builder {
	return statement.addExpression(column, "-", "", []any{by})
}

// SetRaw changes the query into an update statement that assigns the SQL
// expression to column, such as SetRaw("seen_at", "COALESCE(seen_at, ?)",
// now). Placeholder rules follow [Builder.HavingRaw]. Never pass user input
// as expression.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Where("id", 7)
//		statement.SetRaw("visits", "visits + ?", 1)
//	})
//	_ = users
func (statement *Builder) SetRaw(column, expression string, args ...any) *Builder {
	if _, err := rawIdentifier(expression); err != nil {
		return statement.withError(err)
	}

	return statement.addExpression(column, "", expression, args)
}

// Returning asks INSERT, UPDATE, and DELETE to return the listed columns,
// such as "id" or "*". It is supported on [Postgres] and [SQLite]; building
// for [MySQL] or [ANSI], or for a SELECT, fails (ANSI and MySQL with
// [ErrUnsupported]). Execute such statements as queries, not exec calls.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Insert(map[string]any{"name": "Ada"})
//		statement.Returning("id")
//	})
//	_ = users
func (statement *Builder) Returning(columns ...string) *Builder {
	if statement.err != nil {
		return statement
	}

	if len(columns) == 0 {
		return statement.withError(errors.New("returning requires at least one column"))
	}

	parsed, err := parseIdentifiers(columns, true)

	if err != nil {
		return statement.withError(err)
	}

	statement.returning = parsed

	return statement
}

// Conflict selects how an INSERT handles rows that violate a unique
// constraint. Create one with [Builder.OnConflict] and finish it with
// [Conflict.DoUpdate] or [Conflict.DoNothing].
// Completion changes the attached parent; publication detaches conflict state.
// Neither the child nor its parent draft can compile or execute SQL.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Insert(map[string]any{"email": "ada@example.test", "name": "Ada"})
//		conflict := statement.OnConflict("email")
//		conflict.DoUpdate("name")
//	})
//	_ = users
type Conflict struct {
	// parent is the INSERT draft changed by completion methods.
	parent *Builder

	// target holds the parsed conflict columns.
	target []identifier
}

// OnConflict begins upsert handling for an INSERT, naming the columns of the
// unique constraint that may conflict. [Postgres] and [SQLite] use them as the
// ON CONFLICT target and require a nonempty target for [Conflict.DoUpdate].
// [MySQL] reacts to any unique key and ignores the target, including an empty
// one. [Conflict.DoNothing] permits omission on all supported dialects;
// upserts are unsupported on [ANSI].
// It returns an attached child draft. Finish with DoUpdate or DoNothing to
// install conflict handling; either completion returns this parent Builder.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Insert(map[string]any{"email": "ada@example.test"})
//		statement.OnConflict("email").DoNothing()
//	})
//	_ = users
func (statement *Builder) OnConflict(columns ...string) *Conflict {
	if statement.err != nil {
		return &Conflict{
			parent: statement,
		}
	}

	parsed, err := parseIdentifiers(columns, false)

	if err != nil {
		return &Conflict{
			parent: statement.withError(err),
		}
	}

	return &Conflict{
		parent: statement,
		target: parsed,
	}
}

// DoUpdate overwrites the listed columns of the existing row with the values
// the INSERT proposed. It renders SET "c" = EXCLUDED."c" on [Postgres] and
// [SQLite], and ON DUPLICATE KEY UPDATE `c` = VALUES(`c`) on [MySQL]. VALUES()
// is deprecated since MySQL 8.0.20 but works on every MySQL and MariaDB.
// The column list must be nonempty and contain no duplicate assignments.
// Invalid input records a sticky error on the parent draft.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Insert(map[string]any{"email": "ada@example.test", "name": "Ada"})
//		statement.OnConflict("email").DoUpdate("name")
//	})
//	_ = users
func (conflict *Conflict) DoUpdate(columns ...string) *Builder {
	statement := conflict.parent

	if statement.err != nil {
		return statement
	}

	if len(columns) == 0 {
		return statement.withError(errors.New("do update requires at least one column"))
	}

	parsed, err := parseIdentifiers(columns, false)

	if err != nil {
		return statement.withError(err)
	}

	if err := duplicateAssignmentError(parsed); err != nil {
		return statement.withError(err)
	}

	statement.conflict = &upsert{
		target: conflict.target,
		update: parsed,
	}

	return statement
}

// DoNothing skips rows that conflict. It renders ON CONFLICT DO NOTHING on
// [Postgres] and [SQLite], and INSERT IGNORE on [MySQL], which also downgrades
// other errors, such as invalid values, to warnings.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.Insert(map[string]any{"email": "ada@example.test", "name": "Ada"})
//		statement.OnConflict("email").DoNothing()
//	})
//	_ = users
func (conflict *Conflict) DoNothing() *Builder {
	statement := conflict.parent

	if statement.err != nil {
		return statement
	}

	statement.conflict = &upsert{
		target:  conflict.target,
		nothing: true,
	}

	return statement
}

// ForUpdate locks the selected rows against concurrent writes until the
// transaction ends, rendered as FOR UPDATE. Locking is supported on
// [Postgres] and [MySQL]; [SQLite] has no row locks and [ANSI] has no portable
// form, so those dialects fail with [ErrUnsupported]. Only SELECT accepts it.
// PostgreSQL also rejects DISTINCT, GROUP BY, and HAVING with a row lock,
// returning ErrUnsupported. Raw expressions remain database-validated.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.ForUpdate()
//	})
//	_ = users
func (statement *Builder) ForUpdate() *Builder {
	statement.lock = lockUpdate

	return statement
}

// ForShare locks the selected rows against concurrent updates while allowing
// other readers to lock them, rendered as FOR SHARE. MySQL needs 8.0 or later.
// See [Builder.ForUpdate] for dialect support.
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.ForShare()
//	})
//	_ = users
func (statement *Builder) ForShare() *Builder {
	statement.lock = lockShare

	return statement
}

// SkipLocked makes a locking SELECT skip rows another transaction has
// locked. It requires [Builder.ForUpdate] or [Builder.ForShare] and cannot
// combine with [Builder.NoWait].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.ForUpdate()
//		statement.SkipLocked()
//	})
//	_ = users
func (statement *Builder) SkipLocked() *Builder {
	statement.skipLocked = true

	return statement
}

// NoWait makes a locking SELECT fail at once when a row is locked by another
// transaction. It requires [Builder.ForUpdate] or [Builder.ForShare] and
// cannot combine with [Builder.SkipLocked].
// It mutates this draft and returns the same receiver. Invalid settings
// remain recorded until the published query is compiled.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.ForUpdate()
//		statement.NoWait()
//	})
//	_ = users
func (statement *Builder) NoWait() *Builder {
	statement.noWait = true

	return statement
}

// Build compiles the query into parameterized SQL for the [ANSI] dialect.
//
// Example:
//
//	statement, err := query.Table("users").Build()
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
func (query Query) Build() (Statement, error) {
	return query.BuildFor(ANSI)
}

// BuildFor compiles the query into parameterized SQL for dialect. Raw
// expressions passed to *Raw methods use ? placeholders; BuildFor rewrites
// each ? outside quoted text and comments to the dialect placeholder, and
// ?? stands for a literal question mark. INSERT, UPDATE, and DELETE builders
// that carry clauses they cannot use, such as joins or limits, fail.
// SQLite rejects qualified INSERT, UPDATE, and conflict column names with
// ErrUnsupported; qualified table names and read-side references remain valid.
//
// Example:
//
//	statement, err := query.Table("users").BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
func (query Query) BuildFor(dialect Dialect) (Statement, error) {
	if query.err != nil {
		return Statement{}, fmt.Errorf("%w: %w", ErrInvalidQuery, query.err)
	}

	statementCompiler := compiler{
		dialect: dialect,
	}

	if err := statementCompiler.compile(query.queryState); err != nil {
		return Statement{}, fmt.Errorf("%w: %w", ErrInvalidQuery, err)
	}

	return Statement{
		query: statementCompiler.String(),
		args:  statementCompiler.args,
	}, nil
}

// withError records err when the query does not already contain an error.
func (statement *Builder) withError(err error) *Builder {
	if statement.err == nil {
		statement.err = err
	}

	return statement
}

// parseIdentifiers validates a list of identifiers.
func parseIdentifiers(values []string, wildcard bool) ([]identifier, error) {
	identifiers := make([]identifier, 0, len(values))

	for _, value := range values {
		parsed, err := parseIdentifier(value, wildcard)

		if err != nil {
			return nil, err
		}

		identifiers = append(identifiers, parsed)
	}

	return identifiers, nil
}

// anyValues converts a typed slice to positional arguments.
func anyValues[S ~[]T, T any](values S) []any {
	converted := make([]any, len(values))

	for index, value := range values {
		converted[index] = value
	}

	return converted
}

// makeAssignments sorts a map into deterministic column assignments.
func makeAssignments[M ~map[string]V, V any](values M) ([]assignment, error) {
	columns := slices.Sorted(maps.Keys(values))
	assignments := make([]assignment, 0, len(columns))

	for _, column := range columns {
		parsed, err := parseIdentifier(column, false)

		if err != nil {
			return nil, err
		}

		assignments = append(assignments, assignment{
			column: parsed,
			value:  values[column],
		})
	}

	return assignments, nil
}

// makeRows converts maps into deterministic insert rows.
func makeRows[S ~[]M, M ~map[string]V, V any](values S) ([][]assignment, error) {
	rows := make([][]assignment, 0, len(values))

	for _, value := range values {
		row, err := makeAssignments(value)

		if err != nil {
			return nil, err
		}

		rows = append(rows, row)
	}

	return rows, nil
}
