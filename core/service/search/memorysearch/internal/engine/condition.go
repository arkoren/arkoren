package engine

import (
	"slices"

	"arkoren.dev/core/service/search"
)

// matchesConditions evaluates a condition sequence: conditions are joined
// with AND, or OR when marked, and AND binds tighter than OR.
func matchesConditions(fields map[string]any, conditions []search.Condition) bool {
	matched, run := false, true

	for i, condition := range conditions {
		if i > 0 && condition.Or {
			matched, run = matched || run, true
		}

		run = run && matchesCondition(fields, condition)
	}

	return matched || run
}

// matchesCondition evaluates one comparison or group against fields. IN
// requires any typed equality; NOT IN requires every typed inequality, so
// an incompatible operand prevents NOT IN from matching a stored scalar.
func matchesCondition(fields map[string]any, condition search.Condition) bool {
	if len(condition.Group) > 0 {
		return matchesConditions(fields, condition.Group)
	}

	stored := fields[condition.Field]

	switch condition.Operator {
	case search.OpIn:
		return slices.ContainsFunc(condition.Values, func(value any) bool {
			return sameKind(stored, value) && compare(stored, value) == 0
		})
	case search.OpNotIn:
		return !slices.ContainsFunc(condition.Values, func(value any) bool {
			return !sameKind(stored, value) || compare(stored, value) == 0
		})
	}

	if !sameKind(stored, condition.Value) {
		return false
	}

	c := compare(stored, condition.Value)

	switch condition.Operator {
	case search.OpEqual:
		return c == 0
	case search.OpNotEqual:
		return c != 0
	case search.OpLess:
		return c < 0
	case search.OpLessOrEqual:
		return c <= 0
	case search.OpGreater:
		return c > 0
	default:
		return c >= 0
	}
}
