package engine

import (
	"cmp"
	"slices"
	"strings"

	"arkoren.dev/core/service/search"
)

// sameKind reports whether a stored value and an operand have the same
// kind, one of string, number, or boolean.
func sameKind(stored, operand any) bool {
	switch operand.(type) {
	case string:
		_, ok := stored.(string)

		return ok
	case float64:
		_, ok := stored.(float64)

		return ok
	case bool:
		_, ok := stored.(bool)

		return ok
	default:
		return false
	}
}

// compare orders two values of the same kind: strings by lowercase bytes,
// numbers numerically, and booleans with false first.
func compare(a, b any) int {
	switch a := a.(type) {
	case string:
		return strings.Compare(strings.ToLower(a), strings.ToLower(b.(string)))
	case float64:
		return cmp.Compare(a, b.(float64))
	default:
		return boolOrder(a.(bool)) - boolOrder(b.(bool))
	}
}

// boolOrder maps false to 0 and true to 1.
func boolOrder(value bool) int {
	if value {
		return 1
	}

	return 0
}

// compareField orders two documents by one sort key. A document without a
// scalar value sorts after one with it, in both directions, and values of
// different kinds compare equal because mixed fields are undefined.
func compareField(a, b any, desc bool) int {
	// A value is comparable with itself exactly when it is a string, number,
	// or boolean, which is what the schema allows a sortable field to hold.
	switch aPresent, bPresent := sameKind(a, a), sameKind(b, b); {
	case !aPresent && !bPresent:
		return 0
	case !aPresent:
		return 1
	case !bPresent:
		return -1
	case !sameKind(a, b):
		return 0
	case desc:
		return -compare(a, b)
	default:
		return compare(a, b)
	}
}

// orderResults applies explicit keys, relevance, and the final identity tie.
func orderResults(results []result, orders []search.Order) {
	slices.SortFunc(results, func(a, b result) int {
		for _, order := range orders {
			if c := compareField(a.doc.fields[order.Field], b.doc.fields[order.Field], order.Desc); c != 0 {
				return c
			}
		}

		return cmp.Or(cmp.Compare(b.score, a.score), strings.Compare(a.id, b.id))
	})
}
