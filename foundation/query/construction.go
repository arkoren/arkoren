package query

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// duplicateAssignmentError rejects repeated written columns in UPDATE and
// upsert assignments. It compares validated names without changing their
// order or contents; database-specific case folding remains database-owned.
func duplicateAssignmentError(columns []identifier) error {
	seen := make(map[string]struct{}, len(columns))

	for _, column := range columns {
		name := strings.Join(column.parts, ".")

		if _, found := seen[name]; found {
			return fmt.Errorf("column %q is assigned more than once", name)
		}

		seen[name] = struct{}{}
	}

	return nil
}

// addComparison appends a value comparison.
func (statement *Builder) addComparison(boolean booleanOperator, column string, args ...any) *Builder {
	if statement.err != nil {
		return statement
	}

	built, err := comparisonPredicate(boolean, column, args...)

	return statement.appendPredicate(built, err)
}

// addNull appends a null comparison.
func (statement *Builder) addNull(boolean booleanOperator, column string, negated bool) *Builder {
	if statement.err != nil {
		return statement
	}

	built, err := nullPredicate(boolean, column, negated)

	return statement.appendPredicate(built, err)
}

// addIn appends a membership comparison.
func (statement *Builder) addIn(
	boolean booleanOperator,
	column string,
	values []any,
	negated bool,
) *Builder {
	if statement.err != nil {
		return statement
	}

	built, err := inPredicate(boolean, column, values, negated)

	return statement.appendPredicate(built, err)
}

// addBetween appends an inclusive range comparison.
func (statement *Builder) addBetween(
	boolean booleanOperator,
	column string,
	minimum any,
	maximum any,
	negated bool,
) *Builder {
	if statement.err != nil {
		return statement
	}

	built, err := betweenPredicate(boolean, column, minimum, maximum, negated)

	return statement.appendPredicate(built, err)
}

// addColumn appends a column comparison.
func (statement *Builder) addColumn(boolean booleanOperator, first string, args ...string) *Builder {
	if statement.err != nil {
		return statement
	}

	built, err := columnPredicate(boolean, first, args...)

	return statement.appendPredicate(built, err)
}

// addGroup appends a nested predicate group.
func (statement *Builder) addGroup(
	boolean booleanOperator,
	negated bool,
	configure ...PredicateBuilderFunc,
) *Builder {
	nested, err := nestedPredicates(configure...)

	if statement.err != nil {
		return statement
	}

	if err != nil {
		return statement.withError(err)
	}

	if len(nested) == 0 {
		return statement.withError(errEmptyPredicateGroup)
	}

	nestedGroup := predicate{
		kind:       predicateGroup,
		boolean:    boolean,
		predicates: nested,
		negated:    negated,
	}

	return statement.appendPredicate(nestedGroup, nil)
}

// nestedPredicates invokes an ordered declaration on one restricted draft.
// Publication detaches retained nested drafts; nil callbacks panic directly.
// Validation concerns the completed predicate expression, not each callback.
func nestedPredicates(configure ...PredicateBuilderFunc) ([]predicate, error) {
	predicate := &PredicateBuilder{}

	for _, configure := range configure {
		configure(predicate)
	}

	return snapshotPredicates(predicate.draft.predicates), predicate.draft.err
}

// addScope declares one independent protected group at compile time.
// An empty declaration adds nothing; supplied nil callbacks panic directly.
func (statement *Builder) addScope(scope Scope) *Builder {
	nested, err := nestedPredicates(scope)

	if statement.err != nil {
		return statement
	}

	if err != nil || len(nested) == 0 {
		return statement.withError(err)
	}

	return statement.appendScope(predicate{
		kind:       predicateGroup,
		boolean:    booleanAnd,
		predicates: nested,
	})
}

// appendScope mutates the draft's protected predicate list.
// Publication, not each setting, detaches the list from retained drafts.
func (statement *Builder) appendScope(added predicate) *Builder {
	statement.scopes = append(statement.scopes, added)

	return statement
}

// addExists appends an EXISTS predicate over a SELECT subquery.
func (statement *Builder) addExists(boolean booleanOperator, sub Query, negated bool) *Builder {
	if statement.err != nil {
		return statement
	}

	if sub.err != nil {
		return statement.withError(sub.err)
	}

	if sub.kind != KindSelect {
		return statement.withError(errors.New("exists subquery must be a select"))
	}

	return statement.appendPredicate(predicate{
		kind:     predicateExists,
		boolean:  boolean,
		subquery: &sub,
		negated:  negated,
	}, nil)
}

// appendPredicate records a draft predicate or preserves its first error.
// Publication detaches this list and its nested predicate containers.
func (statement *Builder) appendPredicate(added predicate, err error) *Builder {
	if err != nil {
		return statement.withError(err)
	}

	statement.predicates = append(statement.predicates, added)

	return statement
}

// rawPredicate creates a verbatim predicate with bound args.
func rawPredicate(boolean booleanOperator, expression string, args []any) (predicate, error) {
	raw, err := rawIdentifier(expression)

	if err != nil {
		return predicate{}, err
	}

	return predicate{
		kind:    predicateRaw,
		boolean: boolean,
		column:  raw,
		values:  slices.Clone(args),
	}, nil
}

// addRaw appends a raw WHERE predicate.
func (statement *Builder) addRaw(boolean booleanOperator, expression string, args []any) *Builder {
	if statement.err != nil {
		return statement
	}

	built, err := rawPredicate(boolean, expression, args)

	return statement.appendPredicate(built, err)
}

// addExpression turns the query into an UPDATE and appends a computed
// assignment.
func (statement *Builder) addExpression(column, operator, raw string, args []any) *Builder {
	if statement.err != nil {
		return statement
	}

	if statement.source != nil {
		return statement.withError(errSourceMutation)
	}

	parsed, err := parseIdentifier(column, false)

	if err != nil {
		return statement.withError(err)
	}

	if operator != "" && isNil(args[0]) {
		return statement.withError(errors.New("increment amount cannot be nil"))
	}

	statement.kind = KindUpdate
	statement.expressions = append(statement.expressions, expression{
		column:   parsed,
		operator: operator,
		raw:      raw,
		args:     slices.Clone(args),
	})

	return statement
}

// addHaving appends an aggregate predicate.
func (statement *Builder) addHaving(boolean booleanOperator, column string, args ...any) *Builder {
	if statement.err != nil {
		return statement
	}

	built, err := comparisonPredicate(boolean, column, args...)

	if err != nil {
		return statement.withError(err)
	}

	statement.havings = append(statement.havings, built)

	return statement
}

// addJoin appends a structured join.
func (statement *Builder) addJoin(kind joinKind, table, first, operator, second string) *Builder {
	if statement.err != nil {
		return statement
	}

	joined, err := makeJoin(kind, table, first, operator, second)

	if err != nil {
		return statement.withError(err)
	}

	statement.joins = append(statement.joins, joined)

	return statement
}

// makeJoin validates a structured join.
func makeJoin(kind joinKind, table, first, operator, second string) (join, error) {
	tableIdentifier, err := parseIdentifier(table, false)

	if err != nil || kind == crossJoin {
		return join{
			kind:  kind,
			table: tableIdentifier,
		}, err
	}

	built, err := columnPredicate(booleanAnd, first, operator, second)

	if err != nil {
		return join{}, err
	}

	return join{
		kind:     kind,
		table:    tableIdentifier,
		first:    built.column,
		operator: built.operator,
		second:   built.second,
	}, nil
}

// addOrder appends a structured ordering.
func (statement *Builder) addOrder(column, direction string) *Builder {
	if statement.err != nil {
		return statement
	}

	parsed, err := parseIdentifier(column, false)

	if err != nil {
		return statement.withError(err)
	}

	statement.orders = append(statement.orders, order{
		column:    parsed,
		direction: direction,
	})

	return statement
}
