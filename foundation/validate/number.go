package validate

import (
	"context"
	"math"
	"reflect"
	"slices"
)

// signed is the set of signed integer types an [IntSchema] accepts.
type signed interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

// unsigned is the set of unsigned integer types a [UintSchema] accepts.
type unsigned interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

// floating is the set of float types a [FloatSchema] accepts.
type floating interface {
	~float32 | ~float64
}

// number is any numeric type with an ordering.
type number interface {
	signed | unsigned | floating
}

// numberMin builds the Min rule. param is min in a JSON friendly type.
func numberMin[N number](limit N, param any) func(context.Context, N) error {
	return func(_ context.Context, v N) error {
		if v >= limit {
			return nil
		}

		return Fail("number.min", map[string]any{"min": param})
	}
}

// numberMax builds the Max rule.
func numberMax[N number](limit N, param any) func(context.Context, N) error {
	return func(_ context.Context, v N) error {
		if v <= limit {
			return nil
		}

		return Fail("number.max", map[string]any{"max": param})
	}
}

// numberBetween builds the Between rule.
func numberBetween[N number](low, high N, lowParam, highParam any) func(context.Context, N) error {
	if low > high {
		invalidf("Between(%v, %v): minimum is greater than maximum", low, high)
	}

	return func(_ context.Context, v N) error {
		if v >= low && v <= high {
			return nil
		}

		return Fail("number.between", map[string]any{"min": lowParam, "max": highParam})
	}
}

// numberCompare builds the Gt, Gte, Lt and Lte rules.
func numberCompare[N number](code string, limit N, param any, holds func(v, limit N) bool) func(context.Context, N) error {
	return func(_ context.Context, v N) error {
		if holds(v, limit) {
			return nil
		}

		return Fail(code, map[string]any{"value": param})
	}
}

// numberSign builds the Positive and NonNegative rules.
func numberSign[N number](code string, holds func(v N) bool) func(context.Context, N) error {
	return func(_ context.Context, v N) error {
		if holds(v) {
			return nil
		}

		return Fail(code, nil)
	}
}

// IntSchema validates signed integers, including named types. Construct it
// with Int and configure its IntBuilder; the published value is immutable.
//
// WARNING: zero counts as absent and skips every rule except Required, so
// an optional schema configured with Min(1) accepts 0. Use Required or Ptr when
// zero is a meaningful value that must be range-checked.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Min(1)
//	})
//	if err := schema.Validate(context.Background(), 3); err != nil {
//		panic(err)
//	}
type IntSchema[N signed] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[N]
}

// Int publishes a schema for signed N, inferred from typed callbacks. Each
// callback edits one fresh draft once in order; publication detaches its rules.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int64]) {
//		number.Positive()
//	})
//	if err := schema.Validate(context.Background(), 12); err != nil {
//		panic(err)
//	}
func Int[N signed](configure ...IntBuilderFunc[N]) IntSchema[N] {
	draft := &IntBuilder[N]{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema IntSchema[N]) rtype() reflect.Type {
	return reflect.TypeFor[N]()
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema IntSchema[N]) compile(e *env, rep *report, where string) *node {
	typ := reflect.TypeFor[N]()
	n := schema.build(e, rep, where, leafHooks[N]{
		typ: typ,
		get: func(v reflect.Value) N {
			return N(v.Int())
		},
	})

	schema.verifyDefault(n, rep, where)

	return n
}

// Abs replaces a negative value with its absolute value. The minimum
// value of a signed type has no positive counterpart and stays unchanged.
// It is a normalizer: it runs before presence, Default and every rule, in
// the order written.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Abs()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Abs() *IntBuilder[N] {
	number.base = number.normalize(func(v N) N {
		if v < 0 && -v > 0 {
			return -v
		}

		return v
	})

	return number
}

// Clamp limits the value to the range from low to high. It panics with
// [ErrInvalidSchema] when low is greater than high. It is a normalizer:
// it runs before presence, Default and every rule, in the order written.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Clamp(0, 100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Clamp(low, high N) *IntBuilder[N] {
	if low > high {
		invalidf("Clamp(%v, %v): minimum is greater than maximum", low, high)
	}

	number.base = number.normalize(func(v N) N {
		return min(max(v, low), high)
	})

	return number
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.Int[int]()
//	if err := schema.Validate(context.Background(), 3); err != nil {
//		panic(err)
//	}
func (schema IntSchema[N]) Validate(ctx context.Context, v N) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Int[int]()
//	value, err := schema.Parse(context.Background(), 3)
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema IntSchema[N]) Parse(ctx context.Context, v N) (N, error) {
	return execute(ctx, compileStandalone[N](schema), v)
}

// OneOf accepts only the listed values.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.OneOf(1, 2, 3)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) OneOf(values ...N) *IntBuilder[N] {
	number.base = number.addRule(oneOfRule(values, true)).annotate("enum", slices.Clone(values))

	return number
}

// NotOneOf rejects the listed values.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.NotOneOf(13)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) NotOneOf(values ...N) *IntBuilder[N] {
	number.base = number.addRule(oneOfRule(values, false))

	return number
}

// Min requires at least limit.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Min(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Min(limit N) *IntBuilder[N] {
	number.base = number.addRule(numberMin(limit, int64(limit))).annotate("minimum", int64(limit))

	return number
}

// Max allows at most limit.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Max(100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Max(limit N) *IntBuilder[N] {
	number.base = number.addRule(numberMax(limit, int64(limit))).annotate("maximum", int64(limit))

	return number
}

// Between requires a value from low to high, inclusive.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Between(1, 100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Between(low, high N) *IntBuilder[N] {
	number.base = number.addRule(numberBetween(low, high, int64(low), int64(high))).
		annotate("minimum", int64(low)).annotate("maximum", int64(high))

	return number
}

// Gt requires a value greater than limit.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Gt(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Gt(limit N) *IntBuilder[N] {
	number.base = number.addRule(numberCompare("number.gt", limit, int64(limit), func(v, l N) bool {
		return v > l
	})).annotate("exclusiveMinimum", int64(limit))

	return number
}

// Gte requires a value greater than or equal to limit.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Gte(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Gte(limit N) *IntBuilder[N] {
	number.base = number.addRule(numberCompare("number.gte", limit, int64(limit), func(v, l N) bool {
		return v >= l
	})).annotate("minimum", int64(limit))

	return number
}

// Lt requires a value less than limit.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Lt(100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Lt(limit N) *IntBuilder[N] {
	number.base = number.addRule(numberCompare("number.lt", limit, int64(limit), func(v, l N) bool {
		return v < l
	})).annotate("exclusiveMaximum", int64(limit))

	return number
}

// Lte requires a value less than or equal to limit.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Lte(100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Lte(limit N) *IntBuilder[N] {
	number.base = number.addRule(numberCompare("number.lte", limit, int64(limit), func(v, l N) bool {
		return v <= l
	})).annotate("maximum", int64(limit))

	return number
}

// Positive requires a value greater than zero.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.Positive()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) Positive() *IntBuilder[N] {
	number.base = number.addRule(numberSign("number.positive", func(v N) bool {
		return v > 0
	})).annotate("exclusiveMinimum", 0)

	return number
}

// NonNegative requires a value of zero or more.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.NonNegative()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) NonNegative() *IntBuilder[N] {
	number.base = number.addRule(numberSign("number.non_negative", func(v N) bool {
		return v >= 0
	})).annotate("minimum", 0)

	return number
}

// MultipleOf requires a multiple of a strictly positive factor; validated
// values may be negative. Zero retains its immediate ErrInvalidSchema panic.
// A negative factor records a sticky construction diagnostic: Struct aggregates
// it with field errors, standalone execution panics with ErrInvalidSchema, and
// JSONSchema returns an error wrapping that sentinel. Callbacks still finish.
//
// Example:
//
//	schema := validate.Int(func(number *validate.IntBuilder[int]) {
//		number.MultipleOf(5)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *IntBuilder[N]) MultipleOf(factor N) *IntBuilder[N] {
	if factor == 0 {
		invalidf("MultipleOf(0) is not defined")
	}

	if factor < 0 {
		number.base = number.problemf("MultipleOf(%v): factor must be positive", factor)

		return number
	}

	number.base = number.addRule(func(_ context.Context, v N) error {
		if v%factor == 0 {
			return nil
		}

		return Fail("number.multiple_of", map[string]any{"multiple": int64(factor)})
	}).annotate("multipleOf", int64(factor))

	return number
}

// UintSchema validates unsigned integers, including named types. Construct it
// with Uint and configure its UintBuilder; the published value is immutable.
//
// WARNING: zero counts as absent and skips every rule except Required, so
// an optional schema configured with Min(1) accepts 0. Use Required or Ptr when
// zero is a meaningful value that must be range-checked.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Max(9)
//	})
//	if err := schema.Validate(context.Background(), 3); err != nil {
//		panic(err)
//	}
type UintSchema[N unsigned] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[N]
}

// Uint publishes a schema for unsigned N, inferred from typed callbacks.
// Callbacks share one fresh draft and run once in argument order.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint64]) {
//		number.Min(1)
//	})
//	if err := schema.Validate(context.Background(), 3); err != nil {
//		panic(err)
//	}
func Uint[N unsigned](configure ...UintBuilderFunc[N]) UintSchema[N] {
	draft := &UintBuilder[N]{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema UintSchema[N]) rtype() reflect.Type {
	return reflect.TypeFor[N]()
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema UintSchema[N]) compile(e *env, rep *report, where string) *node {
	typ := reflect.TypeFor[N]()
	n := schema.build(e, rep, where, leafHooks[N]{
		typ: typ,
		get: func(v reflect.Value) N {
			return N(v.Uint())
		},
	})

	schema.verifyDefault(n, rep, where)

	return n
}

// Clamp limits the value to the range from low to high. It panics with
// [ErrInvalidSchema] when low is greater than high. It is a normalizer:
// it runs before presence, Default and every rule, in the order written.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Clamp(0, 100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) Clamp(low, high N) *UintBuilder[N] {
	if low > high {
		invalidf("Clamp(%v, %v): minimum is greater than maximum", low, high)
	}

	number.base = number.normalize(func(v N) N {
		return min(max(v, low), high)
	})

	return number
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.Uint[uint]()
//	if err := schema.Validate(context.Background(), 3); err != nil {
//		panic(err)
//	}
func (schema UintSchema[N]) Validate(ctx context.Context, v N) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Uint[uint]()
//	value, err := schema.Parse(context.Background(), 3)
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema UintSchema[N]) Parse(ctx context.Context, v N) (N, error) {
	return execute(ctx, compileStandalone[N](schema), v)
}

// OneOf accepts only the listed values.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.OneOf(1, 2, 3)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) OneOf(values ...N) *UintBuilder[N] {
	number.base = number.addRule(oneOfRule(values, true)).annotate("enum", slices.Clone(values))

	return number
}

// NotOneOf rejects the listed values.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.NotOneOf(13)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) NotOneOf(values ...N) *UintBuilder[N] {
	number.base = number.addRule(oneOfRule(values, false))

	return number
}

// Min requires at least limit.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Min(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) Min(limit N) *UintBuilder[N] {
	number.base = number.addRule(numberMin(limit, uint64(limit))).annotate("minimum", uint64(limit))

	return number
}

// Max allows at most limit.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Max(100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) Max(limit N) *UintBuilder[N] {
	number.base = number.addRule(numberMax(limit, uint64(limit))).annotate("maximum", uint64(limit))

	return number
}

// Between requires a value from low to high, inclusive.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Between(1, 100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) Between(low, high N) *UintBuilder[N] {
	number.base = number.addRule(numberBetween(low, high, uint64(low), uint64(high))).
		annotate("minimum", uint64(low)).annotate("maximum", uint64(high))

	return number
}

// Gt requires a value greater than limit.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Gt(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) Gt(limit N) *UintBuilder[N] {
	number.base = number.addRule(numberCompare("number.gt", limit, uint64(limit), func(v, l N) bool {
		return v > l
	})).annotate("exclusiveMinimum", uint64(limit))

	return number
}

// Gte requires a value greater than or equal to limit.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Gte(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) Gte(limit N) *UintBuilder[N] {
	number.base = number.addRule(numberCompare("number.gte", limit, uint64(limit), func(v, l N) bool {
		return v >= l
	})).annotate("minimum", uint64(limit))

	return number
}

// Lt requires a value less than limit.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Lt(100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) Lt(limit N) *UintBuilder[N] {
	number.base = number.addRule(numberCompare("number.lt", limit, uint64(limit), func(v, l N) bool {
		return v < l
	})).annotate("exclusiveMaximum", uint64(limit))

	return number
}

// Lte requires a value less than or equal to limit.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Lte(100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) Lte(limit N) *UintBuilder[N] {
	number.base = number.addRule(numberCompare("number.lte", limit, uint64(limit), func(v, l N) bool {
		return v <= l
	})).annotate("maximum", uint64(limit))

	return number
}

// Positive requires a value greater than zero.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.Positive()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) Positive() *UintBuilder[N] {
	number.base = number.addRule(numberSign("number.positive", func(v N) bool {
		return v > 0
	})).annotate("exclusiveMinimum", 0)

	return number
}

// NonNegative is always satisfied by unsigned values and exists for
// symmetry with the other number schemas.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.NonNegative()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) NonNegative() *UintBuilder[N] {
	return number
}

// MultipleOf requires a multiple of factor, which must not be zero.
//
// Example:
//
//	schema := validate.Uint(func(number *validate.UintBuilder[uint]) {
//		number.MultipleOf(5)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *UintBuilder[N]) MultipleOf(factor N) *UintBuilder[N] {
	if factor == 0 {
		invalidf("MultipleOf(0) is not defined")
	}

	number.base = number.addRule(func(_ context.Context, v N) error {
		if v%factor == 0 {
			return nil
		}

		return Fail("number.multiple_of", map[string]any{"multiple": uint64(factor)})
	}).annotate("multipleOf", uint64(factor))

	return number
}

// FloatSchema validates floating point numbers, including named types. Construct
// it with Float and configure its FloatBuilder; the published value is immutable.
//
// WARNING: zero counts as absent and skips every rule except Required, so
// an optional schema configured with Min(1) accepts 0. Use Required or Ptr when
// zero is a meaningful value that must be range-checked. NaN input fails every
// range rule. Numeric bounds and MultipleOf factors must be finite. Non-finite
// arguments record sticky construction diagnostics; Struct aggregates them,
// standalone execution panics with ErrInvalidSchema, and JSONSchema returns an
// error wrapping that sentinel, even after a later finite setting.
// Exported bounds retain N's JSON encoding precision: a float32 bound of 0.1
// is emitted as 0.1, not as the widened binary64 decimal. Runtime comparisons
// and existing numeric diagnostic parameter types are unchanged.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Min(0)
//	})
//	if err := schema.Validate(context.Background(), 1.5); err != nil {
//		panic(err)
//	}
type FloatSchema[N floating] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[N]
}

// Float publishes a schema for floating N, inferred from typed callbacks.
// Callbacks run once in order; retained drafts cannot change published rules.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float32]) {
//		number.Max(10)
//	})
//	if err := schema.Validate(context.Background(), 1.5); err != nil {
//		panic(err)
//	}
func Float[N floating](configure ...FloatBuilderFunc[N]) FloatSchema[N] {
	draft := &FloatBuilder[N]{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema FloatSchema[N]) rtype() reflect.Type {
	return reflect.TypeFor[N]()
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema FloatSchema[N]) compile(e *env, rep *report, where string) *node {
	typ := reflect.TypeFor[N]()
	n := schema.build(e, rep, where, leafHooks[N]{
		typ: typ,
		get: func(v reflect.Value) N {
			return N(v.Float())
		},
	})

	schema.verifyDefault(n, rep, where)

	return n
}

// Abs replaces the value with its absolute value. It is a normalizer: it
// runs before presence, Default and every rule, in the order written.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Abs()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Abs() *FloatBuilder[N] {
	number.base = number.normalize(func(v N) N {
		return N(math.Abs(float64(v)))
	})

	return number
}

// Round rounds the value to the given number of decimal places, halves
// away from zero. It panics with [ErrInvalidSchema] when places is
// negative. NaN, infinities and values too large to scale are unchanged.
// It is a normalizer, see Abs.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Round(2)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Round(places int) *FloatBuilder[N] {
	if places < 0 {
		invalidf("Round(%d): places is negative", places)
	}

	scale := math.Pow10(places)

	number.base = number.normalize(func(v N) N {
		scaled := float64(v) * scale

		if math.IsInf(scaled, 0) || math.IsNaN(scaled) {
			return v
		}

		return N(math.Round(scaled) / scale)
	})

	return number
}

// Clamp limits the value to the range from low to high. It panics with
// [ErrInvalidSchema] when low is greater than high. It is a normalizer:
// it runs before presence, Default and every rule, in the order written.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Clamp(0, 100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Clamp(low, high N) *FloatBuilder[N] {
	if low > high {
		invalidf("Clamp(%v, %v): minimum is greater than maximum", low, high)
	}

	number.base = number.normalize(func(v N) N {
		return min(max(v, low), high)
	})

	return number
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.Float[float64]()
//	if err := schema.Validate(context.Background(), 3.5); err != nil {
//		panic(err)
//	}
func (schema FloatSchema[N]) Validate(ctx context.Context, v N) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Float[float64]()
//	value, err := schema.Parse(context.Background(), 3.5)
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema FloatSchema[N]) Parse(ctx context.Context, v N) (N, error) {
	return execute(ctx, compileStandalone[N](schema), v)
}

// OneOf accepts only the listed values.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.OneOf(1, 2.5)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) OneOf(values ...N) *FloatBuilder[N] {
	number.base = number.addRule(oneOfRule(values, true)).annotate("enum", slices.Clone(values))

	return number
}

// NotOneOf rejects the listed values.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.NotOneOf(13)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) NotOneOf(values ...N) *FloatBuilder[N] {
	number.base = number.addRule(oneOfRule(values, false))

	return number
}

// Min requires at least limit. The bound must be finite; FloatSchema documents
// how invalid bounds are recorded and reported without stopping callbacks.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Min(0.5)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Min(limit N) *FloatBuilder[N] {
	number.base = number.addRule(numberMin(limit, float64(limit))).annotate("minimum", limit)

	return number
}

// Max allows at most limit. The bound must be finite; FloatSchema documents
// how invalid bounds are recorded and reported without stopping callbacks.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Max(100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Max(limit N) *FloatBuilder[N] {
	number.base = number.addRule(numberMax(limit, float64(limit))).annotate("maximum", limit)

	return number
}

// Between requires a value from low to high, inclusive. Both bounds must be
// finite; FloatSchema documents how non-finite bounds are recorded and reported.
// A reversed range retains its immediate ErrInvalidSchema panic.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Between(0.5, 100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Between(low, high N) *FloatBuilder[N] {
	number.base = number.addRule(numberBetween(low, high, float64(low), float64(high))).
		annotate("minimum", low).annotate("maximum", high)

	return number
}

// Gt requires a value greater than limit. The bound must be finite;
// FloatSchema documents how invalid bounds are recorded and reported.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Gt(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Gt(limit N) *FloatBuilder[N] {
	number.base = number.addRule(numberCompare("number.gt", limit, float64(limit), func(v, l N) bool {
		return v > l
	})).annotate("exclusiveMinimum", limit)

	return number
}

// Gte requires a value greater than or equal to limit. The bound must be finite;
// FloatSchema documents how invalid bounds are recorded and reported.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Gte(1)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Gte(limit N) *FloatBuilder[N] {
	number.base = number.addRule(numberCompare("number.gte", limit, float64(limit), func(v, l N) bool {
		return v >= l
	})).annotate("minimum", limit)

	return number
}

// Lt requires a value less than limit. The bound must be finite;
// FloatSchema documents how invalid bounds are recorded and reported.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Lt(100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Lt(limit N) *FloatBuilder[N] {
	number.base = number.addRule(numberCompare("number.lt", limit, float64(limit), func(v, l N) bool {
		return v < l
	})).annotate("exclusiveMaximum", limit)

	return number
}

// Lte requires a value less than or equal to limit. The bound must be finite;
// FloatSchema documents how invalid bounds are recorded and reported.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Lte(100)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Lte(limit N) *FloatBuilder[N] {
	number.base = number.addRule(numberCompare("number.lte", limit, float64(limit), func(v, l N) bool {
		return v <= l
	})).annotate("maximum", limit)

	return number
}

// Positive requires a value greater than zero.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.Positive()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) Positive() *FloatBuilder[N] {
	number.base = number.addRule(numberSign("number.positive", func(v N) bool {
		return v > 0
	})).annotate("exclusiveMinimum", 0)

	return number
}

// NonNegative requires a value of zero or more.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.NonNegative()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) NonNegative() *FloatBuilder[N] {
	number.base = number.addRule(numberSign("number.non_negative", func(v N) bool {
		return v >= 0
	})).annotate("minimum", 0)

	return number
}

// MultipleOf requires a multiple of a finite, strictly positive factor;
// validated values may be negative. Zero retains its immediate ErrInvalidSchema
// panic. Negative and non-finite factors record sticky construction diagnostics:
// Struct aggregates them, standalone execution panics with ErrInvalidSchema,
// and JSONSchema returns an error wrapping that sentinel. Callbacks still finish.
// The check divides the shortest decimal JSON encodings of value and factor
// exactly and requires an integer quotient, as JSON Schema does. No epsilon is
// applied: a nearby decimal nonmultiple is rejected. Float32 retains its own
// encoding precision rather than being widened before decimal interpretation.
//
// Example:
//
//	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
//		number.MultipleOf(0.5)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (number *FloatBuilder[N]) MultipleOf(factor N) *FloatBuilder[N] {
	if factor == 0 {
		invalidf("MultipleOf(0) is not defined")
	}

	if factor < 0 {
		number.base = number.problemf("MultipleOf(%v): factor must be positive", factor)

		return number
	}

	if math.IsNaN(float64(factor)) || math.IsInf(float64(factor), 0) {
		number.base = number.problemf("multipleOf(%v): bound must be finite", factor)

		return number
	}

	number.base = number.addRule(func(_ context.Context, v N) error {
		if decimalMultiple(float64(v), float64(factor), reflect.TypeFor[N]().Bits()) {
			return nil
		}

		return Fail("number.multiple_of", map[string]any{"multiple": factor})
	}).annotate("multipleOf", factor)

	return number
}
