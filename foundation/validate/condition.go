package validate

import (
	"slices"
)

// ConditionBuilder records read-only observations of the nearest enclosing
// struct. Multiple observations on one draft are ANDed. Paths use dotted Go
// field names through structs and pointer-to-struct fields, never JSON names,
// collection indexes or parent references. Nil intermediate pointers are absent.
// The draft performs no validation and never changes observed values.
//
// Example:
//
//	type Form struct {
//		Mode, Name string
//	}
//
//	schema := validate.Struct(func(object *validate.StructBuilder[Form]) {
//		object.Field("Name", func(name *validate.StringBuilder[string]) {
//			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
//				predicate.Eq("Mode", "active")
//			})
//		})
//	})
//
//	_ = schema
type ConditionBuilder struct {
	// expressions owns the ordered observations accumulated during construction.
	expressions []condition
}

// ConditionBuilderFunc edits one temporary observation draft. Callbacks run
// once in order. A nil callback panics; an empty resulting expression is an
// invalid schema, including an empty And, Or or Not group.
//
// Example:
//
//	var active validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.Eq("Mode", "active")
//	}
//
//	_ = active
type ConditionBuilderFunc = func(*ConditionBuilder)

// buildCondition snapshots a nonempty AND expression. A single observation
// keeps its detailed localization parameters rather than becoming a group.
func buildCondition(configure ...ConditionBuilderFunc) condition {
	expressions := conditionExpressions(configure)

	if len(expressions) == 1 {
		return expressions[0]
	}

	return andCondition(expressions...)
}

// conditionExpressions executes construction callbacks on one fresh draft and
// copies the expression container so retained drafts cannot change a group.
func conditionExpressions(configure []ConditionBuilderFunc) []condition {
	predicate := &ConditionBuilder{}

	for _, callback := range configure {
		callback(predicate)
	}

	if len(predicate.expressions) == 0 {
		invalidf("condition expression must not be empty")
	}

	return slices.Clone(predicate.expressions)
}

// add records a detached expression and returns the same construction draft.
func (predicate *ConditionBuilder) add(expression condition) *ConditionBuilder {
	predicate.expressions = append(predicate.expressions, expression)

	return predicate
}

// Eq observes equality with value, using the field's exact type or a constant
// of the same scalar family. A missing nested field never compares equal.
//
// Example:
//
//	var active validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.Eq("Profile.Role", "admin")
//	}
//
//	_ = active
func (predicate *ConditionBuilder) Eq(path string, value any) *ConditionBuilder {
	return predicate.add(equalCondition(path, value))
}

// NotEq observes inequality with value. Unlike Not(Eq), an unresolved nested
// path returns false. Direct scalar zero values remain ordinary comparisons.
//
// Example:
//
//	var ordinary validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.NotEq("Profile.Role", "admin")
//	}
//
//	_ = ordinary
func (predicate *ConditionBuilder) NotEq(path string, value any) *ConditionBuilder {
	return predicate.add(unequalCondition(path, value))
}

// In observes membership in a copied value list. Type mismatches are reported
// during enclosing schema construction; an unresolved nested path is false.
//
// Example:
//
//	var staff validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.In("Role", "admin", "editor")
//	}
//
//	_ = staff
func (predicate *ConditionBuilder) In(path string, values ...any) *ConditionBuilder {
	return predicate.add(inCondition(path, values...))
}

// NotIn observes nonmembership in a copied value list. A missing intermediate
// pointer returns false rather than comparing a fabricated scalar zero.
//
// Example:
//
//	var customer validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.NotIn("Role", "admin", "editor")
//	}
//
//	_ = customer
func (predicate *ConditionBuilder) NotIn(path string, values ...any) *ConditionBuilder {
	return predicate.add(notInCondition(path, values...))
}

// Present observes nonzero scalars and nonnil pointers, slices and maps.
// An allocated pointer to zero and an allocated empty collection are present.
//
// Example:
//
//	var tagged validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.Present("Tags")
//	}
//
//	_ = tagged
func (predicate *ConditionBuilder) Present(path string) *ConditionBuilder {
	return predicate.add(presentCondition(path))
}

// Absent complements Present, including a path behind a nil intermediate
// pointer. It never allocates a pointer to make the path reachable.
//
// Example:
//
//	var anonymous validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.Absent("Profile")
//	}
//
//	_ = anonymous
func (predicate *ConditionBuilder) Absent(path string) *ConditionBuilder {
	return predicate.add(absentCondition(path))
}

// And adds one nonempty conjunctive group. Callbacks share one child draft;
// the published group is independent of later edits to that draft.
//
// Example:
//
//	var active validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.And(func(group *validate.ConditionBuilder) {
//			group.Eq("Role", "admin").Present("Token")
//		})
//	}
//
//	_ = active
func (predicate *ConditionBuilder) And(configure ...ConditionBuilderFunc) *ConditionBuilder {
	return predicate.add(andCondition(conditionExpressions(configure)...))
}

// Or adds one nonempty disjunctive group. Observations within the group's
// shared draft are alternatives; other observations on the parent remain ANDed.
//
// Example:
//
//	var staff validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.Or(func(group *validate.ConditionBuilder) {
//			group.Eq("Role", "admin").Eq("Role", "editor")
//		})
//	}
//
//	_ = staff
func (predicate *ConditionBuilder) Or(configure ...ConditionBuilderFunc) *ConditionBuilder {
	return predicate.add(orCondition(conditionExpressions(configure)...))
}

// Not negates one nonempty AND group. This logical complement deliberately
// turns a false comparison on a missing nested path into true.
//
// Example:
//
//	var nonadmin validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.Not(func(group *validate.ConditionBuilder) {
//			group.Eq("Profile.Role", "admin")
//		})
//	}
//
//	_ = nonadmin
func (predicate *ConditionBuilder) Not(configure ...ConditionBuilderFunc) *ConditionBuilder {
	return predicate.add(notCondition(buildCondition(configure...)))
}

// If observes a pure function of the exact nearest enclosing struct type T.
// The function must not mutate its argument or values reachable from it.
// A nil function or mismatched T is a construction error, not a false result.
//
// Example:
//
//	type Form struct {
//		Enabled bool
//	}
//
//	var enabled validate.ConditionBuilderFunc = func(predicate *validate.ConditionBuilder) {
//		predicate.If(func(form Form) bool {
//			return form.Enabled
//		})
//	}
//
//	_ = enabled
func (predicate *ConditionBuilder) If[T any](fn func(T) bool) *ConditionBuilder {
	return predicate.add(predicateCondition(fn))
}
