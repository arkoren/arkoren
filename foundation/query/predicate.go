package query

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// booleanOperator joins a predicate to the preceding predicate.
type booleanOperator string

const (
	// booleanAnd requires both the preceding expression and this predicate.
	booleanAnd booleanOperator = "AND"

	// booleanOr accepts either the preceding expression or this predicate.
	booleanOr booleanOperator = "OR"
)

// predicateKind identifies the structured predicate representation.
type predicateKind uint8

const (
	// predicateComparison compares a column with one bound argument.
	predicateComparison predicateKind = iota

	// predicateNull tests whether a column is NULL, optionally negated.
	predicateNull

	// predicateIn tests membership in a bound value list.
	predicateIn

	// predicateBetween compares a column with two inclusive bounds.
	predicateBetween

	// predicateColumn compares two validated column identifiers.
	predicateColumn

	// predicateGroup encloses nested predicates in parentheses.
	predicateGroup

	// predicateRaw retains caller-written SQL and its bound arguments.
	predicateRaw

	// predicateExists tests whether a published subquery returns any rows.
	predicateExists
)

// predicate describes one structured WHERE or HAVING condition.
type predicate struct {
	// kind determines which operands the compiler reads.
	kind predicateKind

	// boolean joins this predicate to the preceding expression.
	boolean booleanOperator

	// column is the left operand, or the raw expression of predicateRaw.
	column identifier

	// second is the right column of a predicateColumn comparison.
	second identifier

	// operator is the normalized operator for value or column comparisons.
	operator string

	// value is the bound operand of a predicateComparison.
	value any

	// values holds IN members, BETWEEN bounds, or raw expression arguments.
	values []any

	// predicates holds the members of a predicateGroup.
	predicates []predicate

	// subquery is the SELECT of a predicateExists.
	subquery *Query

	// negated turns IN, BETWEEN, NULL, group, and exists predicates into their negation.
	negated bool
}

// comparisonPredicate creates a validated value comparison.
func comparisonPredicate(boolean booleanOperator, column string, args ...any) (predicate, error) {
	parsed, err := parseIdentifier(column, false)

	if err != nil {
		return predicate{}, err
	}

	operator, value, err := comparisonArguments(args)

	if err != nil {
		return predicate{}, err
	}

	return comparisonValue(boolean, parsed, operator, value), nil
}

// comparisonArguments interprets two-part (column, value) and three-part
// (column, operator, value) comparisons.
func comparisonArguments(args []any) (string, any, error) {
	if len(args) == 1 {
		return "=", args[0], nil
	}

	if len(args) != 2 {
		return "", nil, errors.New("where requires a value or an operator and value")
	}

	operator, ok := args[0].(string)

	if !ok {
		return "", nil, errors.New("where operator must be a string")
	}

	operator, err := normalizeOperator(operator)

	return operator, args[1], err
}

// isNil reports whether value is untyped nil or a typed nil pointer, map,
// slice, or interface, which database drivers bind as NULL.
func isNil(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)

	switch reflected.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return reflected.IsNil()
	default:
		return false
	}
}

// comparisonValue normalizes nil equality, including typed nil values, into
// a null predicate.
func comparisonValue(boolean booleanOperator, column identifier, operator string, value any) predicate {
	if (operator == "=" || operator == "<>") && isNil(value) {
		return predicate{
			kind:    predicateNull,
			boolean: boolean,
			column:  column,
			negated: operator == "<>",
		}
	}

	return predicate{
		kind:     predicateComparison,
		boolean:  boolean,
		column:   column,
		operator: operator,
		value:    value,
	}
}

// normalizeOperator validates and normalizes a comparison operator. ILIKE
// variants pass here and are rejected for other dialects at compile time.
func normalizeOperator(operator string) (string, error) {
	operator = strings.ToUpper(strings.Join(strings.Fields(operator), " "))

	switch operator {
	case "!=":
		return "<>", nil
	case "=", "<>", "<", "<=", ">", ">=", "LIKE", "NOT LIKE", "ILIKE", "NOT ILIKE",
		"IS DISTINCT FROM", "IS NOT DISTINCT FROM":
		return operator, nil
	default:
		return "", fmt.Errorf("unsupported operator %q", operator)
	}
}

// caseInsensitiveOperator reports whether operator is an ILIKE variant.
func caseInsensitiveOperator(operator string) bool {
	return operator == "ILIKE" || operator == "NOT ILIKE"
}

// nullPredicate creates a null comparison.
func nullPredicate(boolean booleanOperator, column string, negated bool) (predicate, error) {
	parsed, err := parseIdentifier(column, false)

	if err != nil {
		return predicate{}, err
	}

	return predicate{
		kind:    predicateNull,
		boolean: boolean,
		column:  parsed,
		negated: negated,
	}, nil
}

// inPredicate creates a membership predicate and copies its values.
func inPredicate(boolean booleanOperator, column string, values []any, negated bool) (predicate, error) {
	parsed, err := parseIdentifier(column, false)

	if err != nil {
		return predicate{}, err
	}

	return predicate{
		kind:    predicateIn,
		boolean: boolean,
		column:  parsed,
		values:  slices.Clone(values),
		negated: negated,
	}, nil
}

// betweenPredicate creates an inclusive range predicate.
func betweenPredicate(
	boolean booleanOperator,
	column string,
	minimum any,
	maximum any,
	negated bool,
) (predicate, error) {
	parsed, err := parseIdentifier(column, false)

	if err != nil {
		return predicate{}, err
	}

	return predicate{
		kind:    predicateBetween,
		boolean: boolean,
		column:  parsed,
		values:  []any{minimum, maximum},
		negated: negated,
	}, nil
}

// columnPredicate creates a comparison between two columns.
func columnPredicate(boolean booleanOperator, first string, args ...string) (predicate, error) {
	operator, second, err := columnArguments(args)

	if err != nil {
		return predicate{}, err
	}

	firstIdentifier, err := parseIdentifier(first, false)

	if err != nil {
		return predicate{}, err
	}

	secondIdentifier, err := parseIdentifier(second, false)

	if err != nil {
		return predicate{}, err
	}

	return predicate{
		kind:     predicateColumn,
		boolean:  boolean,
		column:   firstIdentifier,
		second:   secondIdentifier,
		operator: operator,
	}, nil
}

// columnArguments interprets two- and three-part column comparisons.
func columnArguments(args []string) (string, string, error) {
	if len(args) == 1 {
		return "=", args[0], nil
	}

	if len(args) != 2 {
		return "", "", errors.New("where column requires a column or an operator and column")
	}

	operator, err := normalizeOperator(args[0])

	return operator, args[1], err
}
