package validate

import (
	"context"
	"reflect"
	"time"
)

// TimeSchema validates [time.Time] values. Create one with [Time]. The
// zero time counts as absent. Future and Past read the clock from the
// context, see [NewClockContext].
//
// Example:
//
//	schema := validate.Time(func(moment *validate.TimeBuilder) {
//		moment.UTC()
//	})
//	value, err := schema.Parse(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value.Location())
type TimeSchema struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[time.Time]
}

// Time publishes a schema for time.Time. Callbacks run once in order on one
// fresh draft and retained drafts cannot change the immutable result.
//
// Example:
//
//	schema := validate.Time(func(moment *validate.TimeBuilder) {
//		moment.UTC()
//	})
//	value, err := schema.Parse(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value.Location())
func Time(configure ...TimeBuilderFunc) TimeSchema {
	draft := &TimeBuilder{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema TimeSchema) rtype() reflect.Type {
	return timeType
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema TimeSchema) compile(e *env, rep *report, where string) *node {
	n := schema.build(e, rep, where, leafHooks[time.Time]{
		typ: timeType,
		get: func(v reflect.Value) time.Time {
			moment, _ := v.Interface().(time.Time)

			return moment
		},
	})

	schema.verifyDefault(n, rep, where)

	return n
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.Time()
//	if err := schema.Validate(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
//		panic(err)
//	}
func (schema TimeSchema) Validate(ctx context.Context, v time.Time) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Time()
//	value, err := schema.Parse(context.Background(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema TimeSchema) Parse(ctx context.Context, v time.Time) (time.Time, error) {
	return execute(ctx, compileStandalone[time.Time](schema), v)
}

// UTC converts the time to UTC. It is a normalizer: it runs before
// presence, Default and every rule, in the order written. The zero time
// stays zero.
//
// Example:
//
//	schema := validate.Time(func(moment *validate.TimeBuilder) {
//		moment.UTC()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (moment *TimeBuilder) UTC() *TimeBuilder {
	moment.base = moment.normalize(func(v time.Time) time.Time {
		return v.UTC()
	})

	return moment
}

// After requires a time after limit.
//
// Example:
//
//	schema := validate.Time(func(moment *validate.TimeBuilder) {
//		moment.After(time.Unix(0, 0))
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (moment *TimeBuilder) After(limit time.Time) *TimeBuilder {
	moment.base = moment.addRule(func(_ context.Context, v time.Time) error {
		if v.After(limit) {
			return nil
		}

		return Fail("time.after", map[string]any{"time": limit})
	})

	return moment
}

// Before requires a time before limit.
//
// Example:
//
//	schema := validate.Time(func(moment *validate.TimeBuilder) {
//		moment.Before(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (moment *TimeBuilder) Before(limit time.Time) *TimeBuilder {
	moment.base = moment.addRule(func(_ context.Context, v time.Time) error {
		if v.Before(limit) {
			return nil
		}

		return Fail("time.before", map[string]any{"time": limit})
	})

	return moment
}

// Between requires a time from low to high, inclusive.
//
// Example:
//
//	schema := validate.Time(func(moment *validate.TimeBuilder) {
//		moment.Between(time.Unix(0, 0), time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (moment *TimeBuilder) Between(low, high time.Time) *TimeBuilder {
	if low.After(high) {
		invalidf("Between: minimum is after maximum")
	}

	moment.base = moment.addRule(func(_ context.Context, v time.Time) error {
		if !v.Before(low) && !v.After(high) {
			return nil
		}

		return Fail("time.between", map[string]any{"min": low, "max": high})
	})

	return moment
}

// Future requires a time after the current time.
//
// Example:
//
//	schema := validate.Time(func(moment *validate.TimeBuilder) {
//		moment.Future()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (moment *TimeBuilder) Future() *TimeBuilder {
	moment.base = moment.addRule(func(ctx context.Context, v time.Time) error {
		if v.After(currentTime(ctx)) {
			return nil
		}

		return Fail("time.future", nil)
	})

	return moment
}

// Past requires a time before the current time.
//
// Example:
//
//	schema := validate.Time(func(moment *validate.TimeBuilder) {
//		moment.Past()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (moment *TimeBuilder) Past() *TimeBuilder {
	moment.base = moment.addRule(func(ctx context.Context, v time.Time) error {
		if v.Before(currentTime(ctx)) {
			return nil
		}

		return Fail("time.past", nil)
	})

	return moment
}

// AfterField requires a time after the sibling time field.
//
// Example:
//
//	type Interval struct {
//		Start, End time.Time
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Interval]) {
//		object.Field("End", func(moment *validate.TimeBuilder) {
//			moment.AfterField("Start")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (moment *TimeBuilder) AfterField(field string) *TimeBuilder {
	moment.base = moment.relate(relAfter, field)

	return moment
}

// BeforeField requires a time before the sibling time field.
//
// Example:
//
//	type Interval struct {
//		Start, End time.Time
//	}
//	schema := validate.Struct(func(object *validate.StructBuilder[Interval]) {
//		object.Field("Start", func(moment *validate.TimeBuilder) {
//			moment.BeforeField("End")
//		})
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (moment *TimeBuilder) BeforeField(field string) *TimeBuilder {
	moment.base = moment.relate(relBefore, field)

	return moment
}
