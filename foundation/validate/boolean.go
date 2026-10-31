package validate

import (
	"context"
	"reflect"
)

// BoolSchema validates booleans and named bool types. Create one with
// [Bool]. Construction settings belong to BoolBuilder, not published schemas.
//
// False counts as absent, so Required on a bool means "must be true".
// Prefer [BoolBuilder.Accepted] and [BoolBuilder.Declined], or wrap the
// schema in [Ptr] so false is a present value.
//
// Example:
//
//	schema := validate.Bool(func(value *validate.BoolBuilder[bool]) {
//		value.Accepted()
//	})
//	if err := schema.Validate(context.Background(), true); err != nil {
//		panic(err)
//	}
type BoolSchema[B ~bool] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[B]
}

// Bool publishes a schema for B, inferred from typed callbacks. Callbacks run
// once in order and retained drafts cannot change the published result.
//
// Example:
//
//	schema := validate.Bool(func(value *validate.BoolBuilder[bool]) {
//		value.Declined()
//	})
//	if err := schema.Validate(context.Background(), false); err != nil {
//		panic(err)
//	}
func Bool[B ~bool](configure ...BoolBuilderFunc[B]) BoolSchema[B] {
	draft := &BoolBuilder[B]{}

	for _, callback := range configure {
		callback(draft)
	}

	return draft.snapshot()
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (rules BoolSchema[B]) rtype() reflect.Type {
	return reflect.TypeFor[B]()
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (rules BoolSchema[B]) compile(e *env, rep *report, where string) *node {
	typ := reflect.TypeFor[B]()
	n := rules.build(e, rep, where, leafHooks[B]{
		typ: typ,
		get: func(v reflect.Value) B {
			return B(v.Bool())
		},
	})

	rules.verifyDefault(n, rep, where)

	return n
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.Bool[bool]()
//	if err := schema.Validate(context.Background(), true); err != nil {
//		panic(err)
//	}
func (rules BoolSchema[B]) Validate(ctx context.Context, v B) error {
	_, err := rules.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.Bool[bool]()
//	value, err := schema.Parse(context.Background(), true)
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (rules BoolSchema[B]) Parse(ctx context.Context, v B) (B, error) {
	return execute(ctx, compileStandalone[B](rules), v)
}

// Accepted requires the value to be true, such as a terms checkbox.
// A false value fails, even though false is otherwise absent.
//
// Example:
//
//	schema := validate.Bool(func(value *validate.BoolBuilder[bool]) {
//		value.Accepted()
//	})
//	if err := schema.Validate(context.Background(), true); err != nil {
//		panic(err)
//	}
func (rules *BoolBuilder[B]) Accepted() *BoolBuilder[B] {
	rules.base = rules.markRequired("bool.accepted").addRule(func(_ context.Context, v B) error {
		if v {
			return nil
		}

		return Fail("bool.accepted", nil)
	}).annotate("const", true)

	return rules
}

// Declined requires the value to be false. True fails.
//
// Example:
//
//	schema := validate.Bool(func(value *validate.BoolBuilder[bool]) {
//		value.Declined()
//	})
//	if err := schema.Validate(context.Background(), false); err != nil {
//		panic(err)
//	}
func (rules *BoolBuilder[B]) Declined() *BoolBuilder[B] {
	rules.base = rules.addRule(func(_ context.Context, v B) error {
		if !v {
			return nil
		}

		return Fail("bool.declined", nil)
	}).annotate("const", false)

	return rules
}
