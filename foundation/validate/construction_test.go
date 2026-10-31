package validate_test

import (
	"context"
	"errors"
	"mime/multipart"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestBuilderExplicitEmptyScalarChild checks concrete pointer inference when
// no child callbacks are needed, without introducing reflection allocation.
func TestBuilderExplicitEmptyScalarChild(t *testing.T) {
	t.Parallel()

	type form struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name string
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field[validate.StringBuilder[string]]("Name")
	})

	if err := schema.Validate(context.Background(), form{}); err != nil {
		t.Fatal(err)
	}
}

// TestBuilderRetainedCompositeChildCannotChangeSchema checks publication of a
// pointer's child and its own settings, including append-heavy rule containers.
func TestBuilderRetainedCompositeChildCannotChangeSchema(t *testing.T) {
	t.Parallel()

	var retained *validate.StringBuilder[string]

	schema := validate.Ptr(validate.String[string](), func(pointer *validate.PtrBuilder[string]) {
		pointer.Value(func(name *validate.StringBuilder[string]) {
			retained = name
			name.Trim()
		})
	})

	retained.Min(20)
	input := " Ada "

	value, err := schema.Parse(context.Background(), &input)

	if err != nil {
		t.Fatal(err)
	}

	if *value != "Ada" || input != " Ada " {
		t.Fatalf("value = %q, input = %q", *value, input)
	}
}

// requirePanic fails unless fn panics with an error wrapping
// validate.ErrInvalidSchema, and returns the panic message.
func requirePanic(t *testing.T, fn func()) string {
	t.Helper()

	var message string

	func() {
		defer func() {
			recovered := recover()

			err, ok := recovered.(error)

			if !ok || !errors.Is(err, validate.ErrInvalidSchema) {
				t.Fatalf("panic = %#v, want an error wrapping ErrInvalidSchema", recovered)
			}

			message = err.Error()
		}()

		fn()
	}()

	return message
}

// requireInvalidReuse distinguishes a deliberate construction error from an
// incidental runtime panic or silent publication of a weakened schema.
func requireInvalidReuse(t *testing.T, construct func()) {
	t.Helper()

	defer func() {
		err, ok := recover().(error)

		if !ok || !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("construction panic = %v, want ErrInvalidSchema", err)
		}
	}()

	construct()
}

// TestReuseBuilderRejectsCrossRoleComposite prevents FileBuilder from silently
// discarding validation contained inside a same-value-type pointer schema.
func TestReuseBuilderRejectsCrossRoleComposite(t *testing.T) {
	t.Parallel()

	shared := validate.Ptr(validate.Struct(func(object *validate.StructBuilder[multipart.FileHeader]) {
		object.Field("Filename", func(name *validate.StringBuilder[string]) {
			name.Min(3)
		})
	}))

	requireInvalidReuse(t, func() {
		validate.File(func(upload *validate.FileBuilder) {
			upload.Use(shared)
		})
	})
}

// TestReuseBuilderTypedNilPointerIsDomainError protects the publication boundary
// from calling a promoted value-receiver method through a nil schema pointer.
func TestReuseBuilderTypedNilPointerIsDomainError(t *testing.T) {
	t.Parallel()

	var shared *validate.SliceSchema[int]

	requireInvalidReuse(t, func() {
		validate.Slice(func(items *validate.SliceBuilder[int]) {
			items.Use(shared)
		})
	})
}

// TestReuseBuilderLocalChildReplacementBeforeImport stays compatible with
// ordinary mutable draft setters when no imported child constraints are at risk.
func TestReuseBuilderLocalChildReplacementBeforeImport(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Min(10)
		})
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Min(1)
		})
	})

	if err := schema.Validate(context.Background(), []int{1}); err != nil {
		t.Fatal(err)
	}
}

// TestReuseBuilderSourceWithoutChildrenKeepsLocalChild prevents a collection-only
// source from clearing an already configured local child definition.
func TestReuseBuilderSourceWithoutChildrenKeepsLocalChild(t *testing.T) {
	t.Parallel()

	shared := validate.Slice[int]()
	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Min(3)
		})
		items.Use(shared)
	})

	if err := schema.Validate(context.Background(), []int{1}); err == nil {
		t.Fatal("collection-only source cleared local element validation")
	}
}
