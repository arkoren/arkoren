package validate_test

import (
	"context"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestBuilderNestedPointerDefaultPreparedBeforeCondition ensures explicit
// allocation defaults make their nested unconditional field defaults observable.
func TestBuilderNestedPointerDefaultPreparedBeforeCondition(t *testing.T) {
	t.Parallel()

	type profile struct {
		// Role selects the identity or condition branch under test.
		Role string
	}
	type form struct {
		// Name holds the name observed by string rules or sibling conditions.
		Name string
		// Profile provides the nested object traversed by dotted field selectors.
		Profile *profile
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(name *validate.StringBuilder[string]) {
			name.RequiredIf(func(predicate *validate.ConditionBuilder) {
				predicate.Eq("Profile.Role", "admin")
			})
		})
		object.Field("Profile", func(pointer *validate.PtrBuilder[profile]) {
			pointer.Default(profile{})
			pointer.Value(func(details *validate.StructBuilder[profile]) {
				details.Field("Role", func(role *validate.StringBuilder[string]) {
					role.Default("admin")
				})
			})
		})
	})

	if err := schema.Validate(context.Background(), form{}); err == nil {
		t.Fatal("nested default was not observed")
	}
}

// TestBuilderNestedPointerNormalizerRunsOnce checks the preparation/execution
// boundary does not reapply reachable nested normalization or mutate input.
func TestBuilderNestedPointerNormalizerRunsOnce(t *testing.T) {
	t.Parallel()

	type profile struct {
		// Number provides the numeric value observed by nested conditions.
		Number int
	}
	type form struct {
		// Profile provides the nested object traversed by dotted field selectors.
		Profile *profile
	}

	calls := 0

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Profile", func(pointer *validate.PtrBuilder[profile]) {
			pointer.Value(func(details *validate.StructBuilder[profile]) {
				details.Field("Number", func(number *validate.IntBuilder[int]) {
					number.Normalize(func(value int) int {
						calls++

						return value + 1
					})
				})
			})
		})
	})

	input := form{
		Profile: &profile{
			Number: 1,
		},
	}

	value, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 || value.Profile.Number != 2 || input.Profile.Number != 1 {
		t.Fatalf("calls = %d, result = %+v, input = %+v", calls, value.Profile, input.Profile)
	}
}

// TestPtrCapturesPublishedSchemaPointer checks the positional attachment
// boundary, not Use. Reassigning the caller's schema variable must not replace
// the rules of an already published pointer definition or its schema export.
func TestPtrCapturesPublishedSchemaPointer(t *testing.T) {
	t.Parallel()

	inner := validate.String(func(text *validate.StringBuilder[string]) {
		text.Min(3)
	})
	schema := validate.Ptr(&inner)

	inner = validate.String[string]()
	input := "x"

	_, err := schema.Parse(context.Background(), &input)

	requireCodes(t, err, map[string][]string{"": {"string.min"}})

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"minLength":3,"type":"string"}` {
		t.Fatalf("Ptr schema changed after source reassignment: %s", encoded)
	}
}

// TestPtrPreparationDetachesEmbeddedOwner covers a pointer field without any
// normalizer. Preparation still replaces its storage, so the promoted field's
// embedded owner must be copied even when its rules are otherwise read-only.
func TestPtrPreparationDetachesEmbeddedOwner(t *testing.T) {
	t.Parallel()

	// Owner promotes a pointer field through an embedded pointer.
	type Owner struct {
		// Name must be detached in the result without changing the source slot.
		Name *string
	}
	// form makes Name reachable through a caller-owned embedded allocation.
	type form struct {
		// Owner retains the source object's promoted field storage.
		*Owner
	}

	name := "Ada"
	input := form{Owner: &Owner{Name: &name}}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(pointer *validate.PtrBuilder[string]) {
			pointer.Value(func(text *validate.StringBuilder[string]) {
			})
		})
	})

	value, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if input.Name != &name || value.Owner == input.Owner || value.Name == input.Name {
		t.Fatalf("Parse changed or shared embedded storage: input=%p/%p result=%p/%p",
			input.Owner, input.Name, value.Owner, value.Name)
	}
}

// TestReuseBuilderPointerPointeeNormalizerPreserved checks pointer-form imports
// retain pointee preparation and still copy caller-owned pointer storage.
func TestReuseBuilderPointerPointeeNormalizerPreserved(t *testing.T) {
	t.Parallel()

	shared := validate.Ptr(validate.Int(func(number *validate.IntBuilder[int]) {
		number.Abs()
	}))
	schema := validate.Ptr[int](nil, func(pointer *validate.PtrBuilder[int]) {
		pointer.Use(&shared)
	})

	input := -3

	value, err := schema.Parse(context.Background(), &input)

	if err != nil {
		t.Fatal(err)
	}

	if *value != 3 || input != -3 {
		t.Fatalf("result = %d, input = %d", *value, input)
	}
}

// TestReuseBuilderRejectsReplacingImportedPointee protects both validators and
// normalizers retained by a previously imported pointer definition.
func TestReuseBuilderRejectsReplacingImportedPointee(t *testing.T) {
	t.Parallel()

	shared := validate.Ptr(validate.Int(func(number *validate.IntBuilder[int]) {
		number.Min(3)
	}))

	requireInvalidReuse(t, func() {
		validate.Ptr[int](nil, func(pointer *validate.PtrBuilder[int]) {
			pointer.Use(shared)
			pointer.Value(func(number *validate.IntBuilder[int]) {

			})
		})
	})
}

// TestReuseBuilderRejectsMultiplePointees prevents repeated Use from hiding
// an earlier pointer source even when both schemas happen to use the same type.
func TestReuseBuilderRejectsMultiplePointees(t *testing.T) {
	t.Parallel()

	shared := validate.Ptr(validate.Int[int]())

	requireInvalidReuse(t, func() {
		validate.Ptr[int](nil, func(pointer *validate.PtrBuilder[int]) {
			pointer.Use(shared)
			pointer.Use(shared)
		})
	})
}

// TestReuseBuilderRejectsLosingAdapterNormalizer ensures the bare scalar adapter
// exception never permits a meaningful pointee transformation to disappear.
func TestReuseBuilderRejectsLosingAdapterNormalizer(t *testing.T) {
	t.Parallel()

	shared := validate.Ptr(validate.Int[int]())

	requireInvalidReuse(t, func() {
		validate.Ptr(validate.Int(func(number *validate.IntBuilder[int]) {
			number.Abs()
		}), func(pointer *validate.PtrBuilder[int]) {
			pointer.Use(shared)
		})
	})
}

// TestReuseBuilderBareAdapterAcceptsSharedPointee preserves Ptr's common adapter
// call shape when the positional scalar schema contributes no runtime behavior.
func TestReuseBuilderBareAdapterAcceptsSharedPointee(t *testing.T) {
	t.Parallel()

	shared := validate.Ptr(validate.Int(func(number *validate.IntBuilder[int]) {
		number.Min(3)
	}))
	schema := validate.Ptr(validate.Int[int](), func(pointer *validate.PtrBuilder[int]) {
		pointer.Use(shared)
	})

	value := 1

	if err := schema.Validate(context.Background(), &value); err == nil {
		t.Fatal("shared pointee validation was lost")
	}
}

// TestPtrPresence verifies a pointer to a zero value is present.
func TestPtrPresence(t *testing.T) {
	t.Parallel()

	zero := 0

	schema := validate.Ptr[int](validate.Int(func(number *validate.IntBuilder[int]) {
		number.Min(1)
	}))

	requireCodes(t, schema.Validate(context.Background(), &zero), map[string][]string{"": {"number.min"}})

	if err := schema.Validate(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	requireCodes(t, validate.Ptr(validate.Int[int](), func(pointer *validate.PtrBuilder[int]) {
		pointer.Use(schema)
		pointer.Required()
	}).Validate(context.Background(), nil), map[string][]string{"": {"required"}})
}

// TestPtrBoolFalseIsValue verifies Ptr(Bool()) accepts an explicit false.
func TestPtrBoolFalseIsValue(t *testing.T) {
	t.Parallel()

	no := false

	schema := validate.Ptr[bool](validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Declined()
	}), func(pointer *validate.PtrBuilder[bool]) {
		pointer.Required()
	})

	if err := schema.Validate(context.Background(), &no); err != nil {
		t.Fatal(err)
	}
}

// TestPtrParseClonesPointee verifies Parse never writes through the
// caller's pointer.
func TestPtrParseClonesPointee(t *testing.T) {
	t.Parallel()

	type inner struct {
		// Mode holds the mode used by default and condition checks.
		Mode string
	}

	schema := validate.Ptr[inner](validate.Struct[inner](func(object *validate.StructBuilder[inner]) {
		object.Field("Mode", func(value *validate.StringBuilder[string]) {
			value.Default("x")
		})
	}))

	input := &inner{}

	got, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if input.Mode != "" || got.Mode != "x" {
		t.Fatalf("input = %+v, output = %+v", input, got)
	}
}
