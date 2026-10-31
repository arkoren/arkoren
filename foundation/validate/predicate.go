package validate

import (
	"reflect"
	"slices"
	"strings"
	"time"

	"arkoren.dev/foundation/validate/internal/fields"
)

// condFunc evaluates a bound condition against the enclosing struct.
type condFunc func(obj reflect.Value) bool

// env is the binding environment of a [Struct]: the struct type whose
// sibling fields conditions and field comparisons refer to.
type env struct {
	// typ is the enclosing struct type used to bind sibling selectors.
	typ reflect.Type
	// info caches JSON names and promoted-field metadata for that struct.
	info *fields.Struct
}

// condition is a detached read-only expression built by ConditionBuilder.
// Binding checks field paths and constants against the nearest struct type.
type condition struct {
	// bind resolves an observation against its enclosing struct and records
	// construction problems.
	bind func(e *env, rep *report, where string) condFunc

	// describe returns the violation params that explain the condition,
	// or nil when it has no simple description.
	describe func(e *env) map[string]any
}

// params describes the condition for a violation, or returns nil.
func (condition condition) params(e *env) map[string]any {
	if condition.describe == nil || e == nil {
		return nil
	}

	return condition.describe(e)
}

// describeField describes a condition on one sibling field. The op param
// selects the message variant, for example "required_if:eq".
func describeField(op, field string, key string, value any) func(e *env) map[string]any {
	return func(e *env) map[string]any {
		rep := &report{}
		info := e.lookupField(field, rep, "")

		if info == nil {
			return nil
		}

		params := map[string]any{"op": op, "field": info.JSONName}

		if key != "" {
			params[key] = value
		}

		return params
	}
}

// never is the condition bound when binding failed. The problem was
// already reported.
func never(reflect.Value) bool {
	return false
}

// bound binds the condition, reporting a problem for an empty expression and
// for use outside a Struct.
func (condition condition) bound(e *env, rep *report, where string) condFunc {
	if condition.bind == nil {
		rep.addf(where, "condition expression must not be empty")

		return never
	}

	if e == nil {
		rep.addf(where, "conditions and field references need an enclosing Struct")

		return never
	}

	return condition.bind(e, rep, where)
}

// lookupField compiles dot-separated Go names through structs and pointers.
// The combined index follows promoted fields and preserves nil traversal as
// absence. JSON names, collection selectors and parent references are rejected.
func (env *env) lookupField(name string, rep *report, where string) *fields.Field {
	info := env.info
	typ := env.typ
	path := &fields.Field{
		GoName: name,
	}
	parts := strings.Split(name, ".")
	names := make([]string, 0, len(parts))

	for index, part := range parts {
		field, ok := info.Lookup(typ, part)

		if !ok {
			rep.addf(where, "%s", unknownFieldProblem(info, part))

			return nil
		}

		path.Index = append(path.Index, field.Index...)
		path.Type = field.Type

		if field.JSONName != "" {
			names = append(names, field.JSONName)
		}

		if index == len(parts)-1 {
			break
		}

		typ = field.Type

		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}

		if typ.Kind() != reflect.Struct {
			rep.addf(where, "path %q traverses non-struct field %q", name, part)

			return nil
		}

		info = fields.Inspect(typ)
	}

	path.JSONName = strings.Join(names, ".")

	return path
}

// presentValue reports whether v counts as present.
func presentValue(v reflect.Value) bool {
	return !isAbsent(v)
}

// isAbsent is the single presence test: the zero value of the type, a nil
// pointer, slice or map, and for time.Time any zero instant whatever its
// location.
func isAbsent(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}

	if v.Type() == timeType && v.CanInterface() {
		moment, _ := v.Interface().(time.Time)

		return moment.IsZero()
	}

	return v.IsZero()
}

// equalCondition observes whether the selected field equals value. Value must be
// assignable to the field type; an untyped constant such as "admin" also
// matches a named string type.
func equalCondition(field string, value any) condition {
	return condition{
		bind: func(e *env, rep *report, where string) condFunc {
			return bindEquality(e, rep, where, "Eq", field, []any{value}, true)
		},
		describe: describeField("eq", field, "value", value),
	}
}

// unequalCondition observes inequality with the same assignment checks as
// equalCondition. Unreachable paths are false, not fabricated scalar zeros.
func unequalCondition(field string, value any) condition {
	return condition{
		bind: func(e *env, rep *report, where string) condFunc {
			return bindEquality(e, rep, where, "NotEq", field, []any{value}, false)
		},
		describe: describeField("not_eq", field, "value", value),
	}
}

// inCondition observes membership in a captured value list. Constants follow
// equalCondition's coercion rules; caller list storage is detached immediately.
func inCondition(field string, values ...any) condition {
	values = slices.Clone(values)

	return condition{
		bind: func(e *env, rep *report, where string) condFunc {
			return bindEquality(e, rep, where, "In", field, values, true)
		},
		describe: describeField("in", field, "values", values),
	}
}

// notInCondition observes nonmembership using a detached value list. Like
// unequalCondition, an unreachable nested path is false rather than negation.
func notInCondition(field string, values ...any) condition {
	values = slices.Clone(values)

	return condition{
		bind: func(e *env, rep *report, where string) condFunc {
			return bindEquality(e, rep, where, "NotIn", field, values, false)
		},
		describe: describeField("not_in", field, "values", values),
	}
}

// bindEquality builds the shared implementation of Eq, NotEq, In and
// NotIn. A match returns want.
func bindEquality(
	e *env,
	rep *report,
	where, name, field string,
	values []any,
	want bool,
) condFunc {
	info := e.lookupField(field, rep, where)

	if info == nil {
		return never
	}

	equal := equality(info.Type)

	if equal == nil {
		rep.addf(where, "%s(%q): type %s is not comparable", name, field, info.Type)

		return never
	}

	targets := make([]reflect.Value, 0, len(values))

	for _, value := range values {
		target, ok := coerce(value, info.Type)

		if !ok {
			rep.addf(where, "%s(%q): value %#v is not assignable to %s", name, field, value, info.Type)

			continue
		}

		targets = append(targets, target)
	}

	return func(obj reflect.Value) bool {
		current := info.Value(obj)

		if !current.IsValid() {
			return false
		}

		matched := slices.ContainsFunc(targets, func(target reflect.Value) bool {
			return equal(current, target)
		})

		return matched == want
	}
}

// presentCondition observes nonzero scalars and nonnil pointers/collections.
// An unreachable intermediate pointer makes the observation false.
func presentCondition(field string) condition {
	return condition{
		bind: func(e *env, rep *report, where string) condFunc {
			info := e.lookupField(field, rep, where)

			if info == nil {
				return never
			}

			return func(obj reflect.Value) bool {
				return presentValue(info.Value(obj))
			}
		},
		describe: describeField("present", field, "", nil),
	}
}

// absentCondition complements presence, including unreachable nested paths.
func absentCondition(field string) condition {
	negated := notCondition(presentCondition(field))
	negated.describe = describeField("absent", field, "", nil)

	return negated
}

// andCondition binds every detached observation and requires all to hold.
func andCondition(conds ...condition) condition {
	return condition{
		bind: func(e *env, rep *report, where string) condFunc {
			bound := bindAll(e, rep, where, conds)

			return func(obj reflect.Value) bool {
				return !slices.ContainsFunc(bound, func(cond condFunc) bool {
					return !cond(obj)
				})
			}
		},
	}
}

// orCondition binds every detached observation and accepts any true result.
func orCondition(conds ...condition) condition {
	return condition{
		bind: func(e *env, rep *report, where string) condFunc {
			bound := bindAll(e, rep, where, conds)

			return func(obj reflect.Value) bool {
				return slices.ContainsFunc(bound, func(cond condFunc) bool {
					return cond(obj)
				})
			}
		},
	}
}

// notCondition complements a detached expression, including missing-path false.
func notCondition(cond condition) condition {
	return condition{
		bind: func(e *env, rep *report, where string) condFunc {
			bound := cond.bound(e, rep, where)

			return func(obj reflect.Value) bool {
				return !bound(obj)
			}
		},
	}
}

// predicateCondition is true when fn holds for the enclosing struct. T must be the
// exact type of that struct, which [Struct] checks at construction.
func predicateCondition[T any](fn func(T) bool) condition {
	return condition{
		bind: func(e *env, rep *report, where string) condFunc {
			if e.typ != reflect.TypeFor[T]() {
				rep.addf(where, "If[%s] does not match the enclosing object type %s", reflect.TypeFor[T](), e.typ)

				return never
			}

			if fn == nil {
				rep.addf(where, "If function is nil")

				return never
			}

			return func(obj reflect.Value) bool {
				typed, _ := obj.Interface().(T)

				return fn(typed)
			}
		},
	}
}

// bindAll binds a list of conditions.
func bindAll(e *env, rep *report, where string, conds []condition) []condFunc {
	bound := make([]condFunc, len(conds))

	for index, cond := range conds {
		bound[index] = cond.bound(e, rep, where)
	}

	return bound
}

// timeType identifies time.Time during condition and schema compilation. Exact
// type identity avoids treating unrelated structs as instants.
var timeType = reflect.TypeFor[time.Time]()

// equality returns the equality test for values of typ, or nil when
// values of typ cannot be compared.
func equality(typ reflect.Type) func(left, right reflect.Value) bool {
	if typ == timeType {
		return func(left, right reflect.Value) bool {
			leftTime, _ := left.Interface().(time.Time)
			rightTime, _ := right.Interface().(time.Time)

			return leftTime.Equal(rightTime)
		}
	}

	if !typ.Comparable() || typ.Kind() == reflect.Interface {
		return nil
	}

	return func(left, right reflect.Value) bool {
		return left.Equal(right)
	}
}

// coerce converts value to typ. It accepts assignable values and untyped
// constants of the same family, such as "admin" for a named string type
// or 3 for an int64.
func coerce(value any, typ reflect.Type) (reflect.Value, bool) {
	if value == nil {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan:
			return reflect.Zero(typ), true
		default:
			return reflect.Value{}, false
		}
	}

	given := reflect.ValueOf(value)

	if given.Type().AssignableTo(typ) {
		target := reflect.New(typ).Elem()
		target.Set(given)

		return target, true
	}

	if given.Type().PkgPath() != "" || family(given.Kind()) == 0 || family(given.Kind()) != family(typ.Kind()) {
		return reflect.Value{}, false
	}

	converted := given.Convert(typ)

	if !converted.Convert(given.Type()).Equal(given) {
		return reflect.Value{}, false
	}

	return converted, true
}

// family groups the basic kinds that untyped constants convert between.
func family(kind reflect.Kind) int {
	switch kind {
	case reflect.String:
		return 1
	case reflect.Bool:
		return 2
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return 3
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return 4
	case reflect.Float32, reflect.Float64:
		return 5
	default:
		return 0
	}
}
