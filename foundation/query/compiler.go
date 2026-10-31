package query

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// errEmptyPredicateGroup rejects groups that cannot produce a SQL condition.
// Empty scopes are omitted separately and do not use this error.
var errEmptyPredicateGroup = errors.New("predicate group cannot be empty")

// errUnfiltered rejects UPDATE and DELETE statements that would affect every
// row without an explicit [Builder.Unfiltered] call.
var errUnfiltered = errors.New("update or delete without where; call Unfiltered to affect every row")

// compiler renders structured query state into SQL and bindings.
type compiler struct {
	// Builder accumulates SQL text in placeholder-binding order.
	strings.Builder

	// dialect selects placeholder and identifier syntax.
	dialect Dialect

	// args holds bound values in placeholder order.
	args []any

	// err is the first error found while rendering.
	err error
}

// bind writes the dialect placeholder for value and records the value.
func (compiler *compiler) bind(value any) {
	compiler.args = append(compiler.args, value)
	compiler.WriteString(compiler.dialect.placeholder(len(compiler.args)))
}

// fail records the first compile error.
func (compiler *compiler) fail(err error) {
	if compiler.err == nil {
		compiler.err = err
	}
}

// compile renders the query's statement kind.
func (compiler *compiler) compile(query queryState) error {
	compiler.Grow(128)

	if query.source == nil && len(query.table.parts) == 0 {
		return errors.New("table cannot be blank")
	}

	switch query.kind {
	case KindSelect:
		if err := compiler.compileSelect(query); err != nil {
			return err
		}
	case KindInsert:
		if err := compiler.compileInsert(query); err != nil {
			return err
		}
	case KindUpdate:
		if err := compiler.compileUpdate(query); err != nil {
			return err
		}
	case KindDelete:
		if err := compiler.compileDelete(query); err != nil {
			return err
		}
	default:
		return errors.New("statement kind is unsupported")
	}

	return compiler.err
}

// unusedClauses rejects clauses a mutation statement cannot render. Where
// predicates are checked separately because UPDATE and DELETE use them.
func unusedClauses(query queryState, statement string) error {
	var clause string

	switch {
	case len(query.columns) > 0:
		clause = "select columns"
	case query.distinct:
		clause = "distinct"
	case len(query.joins) > 0:
		clause = "joins"
	case len(query.groups) > 0:
		clause = "group by"
	case len(query.havings) > 0:
		clause = "having"
	case len(query.orders) > 0:
		clause = "order by"
	case query.limit != nil:
		clause = "limit"
	case query.offset != nil:
		clause = "offset"
	case query.lock != lockNone || query.skipLocked || query.noWait:
		clause = "locking"
	case query.conflict != nil && statement != "insert":
		clause = "on conflict"
	case len(query.expressions) > 0 && statement != "update":
		clause = "increment, decrement, or set raw"
	default:
		return nil
	}

	return fmt.Errorf("%s does not support %s", statement, clause)
}

// compileSelect renders a SELECT statement, including the derived table of
// a raw or query source. A subquery compiles into the same compiler, so
// its arguments precede the outer ones and $n numbering stays continuous.
func (compiler *compiler) compileSelect(query queryState) error {
	if query.err != nil {
		return query.err
	}

	if len(query.returning) > 0 {
		return errors.New("select does not support returning")
	}

	if query.conflict != nil {
		return errors.New("select does not support on conflict")
	}

	if query.source == nil && len(query.table.parts) == 0 {
		return errors.New("table cannot be blank")
	}

	compiler.WriteString("SELECT ")

	if query.distinct {
		compiler.WriteString("DISTINCT ")
	}

	compiler.writeColumns(query.columns)
	compiler.WriteString(" FROM ")

	if err := compiler.writeSource(query); err != nil {
		return err
	}

	compiler.compileClauses(query)

	return compiler.writeLock(query)
}

// defaultAlias names a derived table when [Builder.As] was not called.
const defaultAlias = "t"

// writeSource renders the table, or the parenthesized derived table with its
// alias.
func (compiler *compiler) writeSource(query queryState) error {
	if query.source == nil {
		compiler.WriteString(query.table.quoted(compiler.dialect))

		return nil
	}

	compiler.WriteString("(\n")

	if query.source.inner != nil {
		if query.source.inner.kind != KindSelect {
			return errors.New("subquery must be a select")
		}

		if err := compiler.compileSelect(query.source.inner.queryState); err != nil {
			return err
		}
	} else {
		if query.source.raw.isNamed {
			compiler.writeNamed(query.source.raw.query, query.source.raw.named)
		} else {
			compiler.writeRaw(query.source.raw.query, query.source.raw.args)
		}
	}

	compiler.WriteString("\n) AS ")

	alias := query.alias

	if len(alias.parts) == 0 {
		alias = identifier{
			parts: []string{defaultAlias},
		}
	}

	compiler.WriteString(alias.quoted(compiler.dialect))

	return nil
}

// writeLock renders the row lock after the paging clauses, or rejects a lock
// the dialect or the query cannot express.
func (compiler *compiler) writeLock(query queryState) error {
	if err := compiler.validateLock(query); err != nil {
		return err
	}

	if query.lock == lockNone {
		return nil
	}

	compiler.WriteString(" FOR ")

	if query.lock == lockUpdate {
		compiler.WriteString("UPDATE")
	} else {
		compiler.WriteString("SHARE")
	}

	if query.skipLocked {
		compiler.WriteString(" SKIP LOCKED")
	}

	if query.noWait {
		compiler.WriteString(" NOWAIT")
	}

	return nil
}

// validateLock checks modifier compatibility before rendering any lock clause.
// A dialect limitation wraps ErrUnsupported; malformed modifier combinations
// remain ordinary validation errors. Neither path changes the published query.
func (compiler *compiler) validateLock(query queryState) error {
	if query.skipLocked && query.noWait {
		return errors.New("skip locked and no wait cannot be combined")
	}

	if query.lock == lockNone {
		if query.skipLocked || query.noWait {
			return errors.New("skip locked and no wait require for update or for share")
		}

		return nil
	}

	if compiler.dialect.kind != postgresDialect && compiler.dialect.kind != mysqlDialect {
		return compiler.dialect.unsupported("row locking")
	}

	if compiler.dialect.kind == postgresDialect {
		return postgresLockError(query)
	}

	return nil
}

// postgresLockError rejects structured shapes whose output no longer identifies
// lockable rows. Raw selection expressions are opaque and are not inspected.
func postgresLockError(query queryState) error {
	var clause string

	switch {
	case query.distinct:
		clause = "DISTINCT"
	case len(query.groups) > 0:
		clause = "GROUP BY"
	case len(query.havings) > 0:
		clause = "HAVING"
	default:
		return nil
	}

	return Postgres.unsupported("row locking with " + clause)
}

// writeReturning renders RETURNING, or rejects dialects without it.
func (compiler *compiler) writeReturning(columns []identifier) error {
	if len(columns) == 0 {
		return nil
	}

	if compiler.dialect.kind != postgresDialect && compiler.dialect.kind != sqliteDialect {
		return compiler.dialect.unsupported("RETURNING")
	}

	compiler.writeIdentifiers(" RETURNING ", columns)

	return nil
}

// compileInsert renders an INSERT statement.
func (compiler *compiler) compileInsert(query queryState) error {
	if err := unusedClauses(query, "insert"); err != nil {
		return err
	}

	if len(query.predicates) > 0 || len(query.scopes) > 0 {
		return errors.New("insert does not support where")
	}

	if len(query.rows) == 0 || len(query.rows[0]) == 0 {
		return errors.New("insert values cannot be empty")
	}

	if !matchingRows(query.rows) {
		return errors.New("insert rows must contain the same columns")
	}

	if query.conflict != nil && compiler.dialect.kind == ansiDialect {
		return compiler.dialect.unsupported("upsert")
	}

	compiler.WriteString("INSERT ")

	if query.conflict != nil && query.conflict.nothing && compiler.dialect.kind == mysqlDialect {
		compiler.WriteString("IGNORE ")
	}

	compiler.WriteString("INTO ")
	compiler.WriteString(query.table.quoted(compiler.dialect))
	compiler.WriteString(" (")
	compiler.writeAssignments(query.rows[0], false)
	compiler.WriteString(") VALUES ")
	compiler.writeRows(query.rows)

	if err := compiler.writeConflict(query.conflict); err != nil {
		return err
	}

	return compiler.writeReturning(query.returning)
}

// writeConflict renders the dialect's upsert clause. MySQL needs none for
// DoNothing, which compileInsert renders as INSERT IGNORE.
func (compiler *compiler) writeConflict(conflict *upsert) error {
	if conflict == nil {
		return nil
	}

	if compiler.dialect.kind == mysqlDialect {
		compiler.writeDuplicateKey(conflict)

		return nil
	}

	if len(conflict.target) == 0 && !conflict.nothing {
		return errors.New("do update requires conflict columns")
	}

	compiler.WriteString(" ON CONFLICT")

	if len(conflict.target) > 0 {
		compiler.writeConflictTarget(conflict.target)
	}

	if conflict.nothing {
		compiler.WriteString(" DO NOTHING")

		return nil
	}

	compiler.WriteString(" DO UPDATE SET ")

	for index, column := range conflict.update {
		if index > 0 {
			compiler.WriteString(", ")
		}

		quoted := compiler.quotedMutationColumn(column)
		compiler.WriteString(quoted + " = EXCLUDED." + quoted)
	}

	return nil
}

// writeConflictTarget renders a nonempty conflict-column list. Unlike read
// identifiers, these names must be unqualified when compiling for SQLite.
func (compiler *compiler) writeConflictTarget(columns []identifier) {
	compiler.WriteString(" (")

	for index, column := range columns {
		if index > 0 {
			compiler.WriteString(", ")
		}

		compiler.WriteString(compiler.quotedMutationColumn(column))
	}

	compiler.WriteByte(')')
}

// quotedMutationColumn quotes a written column and records unsupported SQLite
// qualification. Read-side references and qualified source tables do not use
// this guard; other dialects retain their existing column representations.
func (compiler *compiler) quotedMutationColumn(column identifier) string {
	if compiler.dialect.kind == sqliteDialect && len(column.parts) > 1 {
		compiler.fail(compiler.dialect.unsupported("qualified mutation columns"))
	}

	return column.quoted(compiler.dialect)
}

// writeDuplicateKey renders MySQL's ON DUPLICATE KEY UPDATE clause.
func (compiler *compiler) writeDuplicateKey(conflict *upsert) {
	if conflict.nothing {
		return
	}

	compiler.WriteString(" ON DUPLICATE KEY UPDATE ")

	for index, column := range conflict.update {
		if index > 0 {
			compiler.WriteString(", ")
		}

		quoted := column.quoted(compiler.dialect)
		compiler.WriteString(quoted + " = VALUES(" + quoted + ")")
	}
}

// compileUpdate renders an UPDATE statement.
func (compiler *compiler) compileUpdate(query queryState) error {
	if err := unusedClauses(query, "update"); err != nil {
		return err
	}

	if len(query.updates) == 0 && len(query.expressions) == 0 {
		return errors.New("update values cannot be empty")
	}

	if err := checkDuplicateUpdates(query); err != nil {
		return err
	}

	filters := query.filters()

	if len(filters) == 0 && !query.unfiltered {
		return errUnfiltered
	}

	compiler.WriteString("UPDATE ")
	compiler.WriteString(query.table.quoted(compiler.dialect))
	compiler.WriteString(" SET ")
	compiler.writeAssignments(query.updates, true)
	compiler.writeExpressions(query.expressions, len(query.updates) > 0)
	compiler.writePredicates(" WHERE ", filters)

	return compiler.writeReturning(query.returning)
}

// checkDuplicateUpdates rejects a column assigned more than once.
func checkDuplicateUpdates(query queryState) error {
	columns := make([]identifier, 0, len(query.updates)+len(query.expressions))

	for _, assigned := range query.updates {
		columns = append(columns, assigned.column)
	}

	for _, computed := range query.expressions {
		columns = append(columns, computed.column)
	}

	return duplicateAssignmentError(columns)
}

// writeExpressions renders computed assignments; separated reports whether
// plain assignments precede them.
func (compiler *compiler) writeExpressions(expressions []expression, separated bool) {
	for index, computed := range expressions {
		if index > 0 || separated {
			compiler.WriteString(", ")
		}

		quoted := compiler.quotedMutationColumn(computed.column)
		compiler.WriteString(quoted + " = ")

		if computed.operator == "" {
			compiler.writeRaw(computed.raw, computed.args)

			continue
		}

		compiler.WriteString(quoted + " " + computed.operator + " ")
		compiler.bind(computed.args[0])
	}
}

// compileDelete renders a DELETE statement.
func (compiler *compiler) compileDelete(query queryState) error {
	if err := unusedClauses(query, "delete"); err != nil {
		return err
	}

	filters := query.filters()

	if len(filters) == 0 && !query.unfiltered {
		return errUnfiltered
	}

	compiler.WriteString("DELETE FROM ")
	compiler.WriteString(query.table.quoted(compiler.dialect))
	compiler.writePredicates(" WHERE ", filters)

	return compiler.writeReturning(query.returning)
}

// filters returns the WHERE predicates with the entire nonempty user chain
// grouped before ANDing scopes. Raw predicates are not parsed, so grouping
// cannot depend on finding structured OR operators. Without scopes the user
// chain is unchanged; compilation never mutates the query's predicate slices.
func (query queryState) filters() []predicate {
	user := query.predicates

	if len(query.scopes) == 0 {
		return user
	}

	if len(user) > 0 {
		user = []predicate{{
			kind:       predicateGroup,
			boolean:    booleanAnd,
			predicates: user,
		}}
	}

	return slices.Concat(user, query.scopes)
}

// compileClauses renders clauses shared by query statements.
func (compiler *compiler) compileClauses(query queryState) {
	compiler.writeJoins(query.joins)
	compiler.writePredicates(" WHERE ", query.filters())
	compiler.writeIdentifiers(" GROUP BY ", query.groups)
	compiler.writePredicates(" HAVING ", query.havings)
	compiler.writeOrders(query.orders)
	compiler.writeLimitOffset(query.limit, query.offset)
}

// writeColumns renders selected columns or the wildcard default.
func (compiler *compiler) writeColumns(columns []identifier) {
	if len(columns) == 0 {
		compiler.WriteByte('*')

		return
	}

	compiler.writeIdentifierList(columns)
}

// writeIdentifiers renders an optional identifier clause.
func (compiler *compiler) writeIdentifiers(prefix string, identifiers []identifier) {
	if len(identifiers) == 0 {
		return
	}

	compiler.WriteString(prefix)
	compiler.writeIdentifierList(identifiers)
}

// writeIdentifierList renders comma-separated identifiers.
func (compiler *compiler) writeIdentifierList(identifiers []identifier) {
	for index, name := range identifiers {
		if index > 0 {
			compiler.WriteString(", ")
		}

		compiler.WriteString(name.quoted(compiler.dialect))
	}
}

// writeJoins renders structured joins.
func (compiler *compiler) writeJoins(joins []join) {
	for _, joined := range joins {
		compiler.WriteByte(' ')
		compiler.WriteString(string(joined.kind))
		compiler.WriteByte(' ')
		compiler.WriteString(joined.table.quoted(compiler.dialect))

		if joined.kind != crossJoin {
			compiler.checkOperator(joined.operator)
			compiler.WriteString(" ON ")
			compiler.WriteString(joined.first.quoted(compiler.dialect))
			compiler.WriteByte(' ')
			compiler.WriteString(joined.operator)
			compiler.WriteByte(' ')
			compiler.WriteString(joined.second.quoted(compiler.dialect))
		}
	}
}

// writePredicates renders an optional predicate clause.
func (compiler *compiler) writePredicates(prefix string, predicates []predicate) {
	if len(predicates) == 0 {
		return
	}

	compiler.WriteString(prefix)

	for index, condition := range predicates {
		if index > 0 {
			compiler.WriteByte(' ')
			compiler.WriteString(string(condition.boolean))
			compiler.WriteByte(' ')
		}

		compiler.writePredicate(condition)
	}
}

// writePredicate renders one structured predicate.
func (compiler *compiler) writePredicate(condition predicate) {
	switch condition.kind {
	case predicateComparison:
		compiler.writeComparison(condition)
	case predicateNull:
		compiler.writeNull(condition)
	case predicateIn:
		compiler.writeIn(condition)
	case predicateBetween:
		compiler.writeBetween(condition)
	case predicateColumn:
		compiler.writeColumnComparison(condition)
	case predicateGroup:
		compiler.writeGroup(condition)
	case predicateRaw:
		compiler.writeRaw(condition.column.raw, condition.values)
	case predicateExists:
		compiler.writeExists(condition)
	}
}

// checkOperator rejects recognized operators a dialect cannot render. All
// structured comparisons, including joins, pass through this guard; raw SQL
// remains the caller's responsibility.
func (compiler *compiler) checkOperator(operator string) {
	if caseInsensitiveOperator(operator) && compiler.dialect.kind != postgresDialect {
		compiler.fail(compiler.dialect.unsupported(operator))
	}

	if compiler.dialect.kind != mysqlDialect {
		return
	}

	switch operator {
	case "IS DISTINCT FROM", "IS NOT DISTINCT FROM":
		compiler.fail(compiler.dialect.unsupported(operator))
	}
}

// writeComparison renders a bound value comparison.
func (compiler *compiler) writeComparison(condition predicate) {
	compiler.checkOperator(condition.operator)
	compiler.WriteString(condition.column.quoted(compiler.dialect))
	compiler.WriteByte(' ')
	compiler.WriteString(condition.operator)
	compiler.WriteByte(' ')
	compiler.bind(condition.value)
}

// writeNull renders a null comparison.
func (compiler *compiler) writeNull(condition predicate) {
	compiler.WriteString(condition.column.quoted(compiler.dialect))

	if condition.negated {
		compiler.WriteString(" IS NOT NULL")

		return
	}

	compiler.WriteString(" IS NULL")
}

// writeIn renders a membership comparison.
func (compiler *compiler) writeIn(condition predicate) {
	if len(condition.values) == 0 {
		if condition.negated {
			compiler.WriteString("1 = 1")

			return
		}

		compiler.WriteString("0 = 1")

		return
	}

	compiler.WriteString(condition.column.quoted(compiler.dialect))

	if condition.negated {
		compiler.WriteString(" NOT")
	}

	compiler.WriteString(" IN (")
	compiler.writePlaceholders(condition.values)
	compiler.WriteByte(')')
}

// writeBetween renders an inclusive range comparison.
func (compiler *compiler) writeBetween(condition predicate) {
	compiler.WriteString(condition.column.quoted(compiler.dialect))

	if condition.negated {
		compiler.WriteString(" NOT")
	}

	compiler.WriteString(" BETWEEN ")
	compiler.bind(condition.values[0])
	compiler.WriteString(" AND ")
	compiler.bind(condition.values[1])
}

// writeColumnComparison renders a comparison between columns.
func (compiler *compiler) writeColumnComparison(condition predicate) {
	compiler.checkOperator(condition.operator)
	compiler.WriteString(condition.column.quoted(compiler.dialect))
	compiler.WriteByte(' ')
	compiler.WriteString(condition.operator)
	compiler.WriteByte(' ')
	compiler.WriteString(condition.second.quoted(compiler.dialect))
}

// writeGroup renders a nested predicate group.
func (compiler *compiler) writeGroup(condition predicate) {
	if condition.negated {
		compiler.WriteString("NOT ")
	}

	compiler.WriteByte('(')
	compiler.writePredicates("", condition.predicates)
	compiler.WriteByte(')')
}

// writeExists renders EXISTS over the subquery, compiled into this compiler
// so placeholder numbering continues.
func (compiler *compiler) writeExists(condition predicate) {
	if condition.negated {
		compiler.WriteString("NOT ")
	}

	compiler.WriteString("EXISTS (")

	if err := compiler.compileSelect(condition.subquery.queryState); err != nil {
		compiler.fail(err)
	}

	compiler.WriteByte(')')
}

// writeOrders renders structured ordering.
func (compiler *compiler) writeOrders(orders []order) {
	if len(orders) == 0 {
		return
	}

	compiler.WriteString(" ORDER BY ")

	for index, ordering := range orders {
		if index > 0 {
			compiler.WriteString(", ")
		}

		compiler.WriteString(ordering.column.quoted(compiler.dialect))
		compiler.WriteByte(' ')
		compiler.WriteString(ordering.direction)
	}
}

// maxMySQLLimit is MySQL's documented stand-in for "no limit", required
// before OFFSET.
const maxMySQLLimit = "18446744073709551615"

// writeLimitOffset renders the dialect's row limiting. ANSI uses OFFSET and
// FETCH FIRST; the others use LIMIT and OFFSET, adding an unbounded LIMIT
// where the dialect requires one before OFFSET.
func (compiler *compiler) writeLimitOffset(limit, offset *int) {
	if compiler.dialect.kind == ansiDialect {
		if offset != nil {
			compiler.WriteString(" OFFSET ")
			compiler.WriteString(strconv.Itoa(*offset))
			compiler.WriteString(" ROWS")
		}

		if limit != nil {
			compiler.WriteString(" FETCH FIRST ")
			compiler.WriteString(strconv.Itoa(*limit))
			compiler.WriteString(" ROWS ONLY")
		}

		return
	}

	switch {
	case limit != nil:
		compiler.WriteString(" LIMIT ")
		compiler.WriteString(strconv.Itoa(*limit))
	case offset != nil && compiler.dialect.kind == mysqlDialect:
		compiler.WriteString(" LIMIT ")
		compiler.WriteString(maxMySQLLimit)
	case offset != nil && compiler.dialect.kind == sqliteDialect:
		compiler.WriteString(" LIMIT -1")
	}

	if offset != nil {
		compiler.WriteString(" OFFSET ")
		compiler.WriteString(strconv.Itoa(*offset))
	}
}

// writeAssignments renders sorted assignment columns and optional values.
func (compiler *compiler) writeAssignments(assignments []assignment, values bool) {
	for index, assigned := range assignments {
		if index > 0 {
			compiler.WriteString(", ")
		}

		compiler.WriteString(compiler.quotedMutationColumn(assigned.column))

		if values {
			compiler.WriteString(" = ")
			compiler.bind(assigned.value)
		}
	}
}

// writeRows renders insert placeholders and records their arguments.
func (compiler *compiler) writeRows(rows [][]assignment) {
	for rowIndex, row := range rows {
		if rowIndex > 0 {
			compiler.WriteString(", ")
		}

		compiler.WriteByte('(')

		for index, assigned := range row {
			if index > 0 {
				compiler.WriteString(", ")
			}

			compiler.bind(assigned.value)
		}

		compiler.WriteByte(')')
	}
}

// writePlaceholders renders placeholders and retains their values.
func (compiler *compiler) writePlaceholders(values []any) {
	for index, value := range values {
		if index > 0 {
			compiler.WriteString(", ")
		}

		compiler.bind(value)
	}
}

// matchingRows reports whether all insert rows contain the same columns.
func matchingRows(rows [][]assignment) bool {
	for _, row := range rows[1:] {
		if !matchingColumns(rows[0], row) {
			return false
		}
	}

	return true
}

// matchingColumns reports whether two rows contain equal ordered columns.
func matchingColumns(first, second []assignment) bool {
	if len(first) != len(second) {
		return false
	}

	for index := range first {
		if !slices.Equal(first[index].column.parts, second[index].column.parts) {
			return false
		}
	}

	return true
}
