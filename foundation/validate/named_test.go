package validate_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/foundation/validate"
)

// namedNames and namedLabels represent application-owned containers. Tests keep
// them independent of collection so neither foundation module imports the other.
type namedNames []string

// namedLabels holds values whose normalization must detach the input map.
type namedLabels map[string]string

// TestNamedSlicePreservesPreparationAndOwnership runs child preparation only
// once and checks that source rules observe the normalized named result.
func TestNamedSlicePreservesPreparationAndOwnership(t *testing.T) {
	t.Parallel()

	calls := 0

	schema := validate.Named[namedNames](validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(name *validate.StringBuilder[string]) {
			name.Normalize(func(value string) string {
				calls++

				return strings.TrimSpace(value)
			}).Min(1)
		})
		items.Min(1)
	}))

	input := namedNames{" Ada "}

	got, err := schema.Parse(context.Background(), input)

	if err != nil || !reflect.DeepEqual(got, namedNames{"Ada"}) {
		t.Fatalf("Parse = %v, %v", got, err)
	}

	if calls != 1 || input[0] != " Ada " {
		t.Fatalf("calls = %d, input = %v", calls, input)
	}

	requireCodes(t, schema.Validate(context.Background(), namedNames{" "}), map[string][]string{
		"/0": {"string.min"},
	})
}

// TestNamedMapStructFieldSupportsExactType binds the adapter through the normal
// typed child callback and preserves JSON paths and per-value input ownership.
func TestNamedMapStructFieldSupportsExactType(t *testing.T) {
	t.Parallel()

	type Request struct {
		// Labels is the exact named field selected by the adapter draft.
		Labels namedLabels `json:"labels"`
	}

	schema := validate.Struct(func(object *validate.StructBuilder[Request]) {
		object.Field("Labels", func(labels *validate.NamedBuilder[namedLabels]) {
			labels.Use(validate.Map(func(entries *validate.MapBuilder[string, string]) {
				entries.Values(func(value *validate.StringBuilder[string]) {
					value.Trim().Min(1)
				})
			}))
		})
	})

	input := Request{Labels: namedLabels{"name": " Ada "}}

	got, err := schema.Parse(context.Background(), input)

	if err != nil || got.Labels["name"] != "Ada" || input.Labels["name"] != " Ada " {
		t.Fatalf("Parse = %v, %v; input = %v", got, err, input)
	}

	requireCodes(t, schema.Validate(context.Background(), Request{Labels: namedLabels{"bad/key": " "}}),
		map[string][]string{"/labels/bad~1key": {"string.min"}})
}

// TestNamedSliceFieldAndDefaults preserves nil presence, required metadata, and
// independently allocated defaults through a named field's execution slot.
func TestNamedSliceFieldAndDefaults(t *testing.T) {
	t.Parallel()

	type Request struct {
		// Names checks named slice registration rather than an implicit conversion.
		Names namedNames `json:"names"`
	}

	schema := validate.Struct(func(object *validate.StructBuilder[Request]) {
		object.Field("Names", func(names *validate.NamedBuilder[namedNames]) {
			names.Use(validate.Slice(func(items *validate.SliceBuilder[string]) {
				items.Default([]string{"Ada"})
			}))
		})
	})

	got, err := schema.Parse(context.Background(), Request{})

	if err != nil || !reflect.DeepEqual(got.Names, namedNames{"Ada"}) {
		t.Fatalf("default Parse = %v, %v", got, err)
	}

	got.Names[0] = "changed"

	second, err := schema.Parse(context.Background(), Request{})

	if err != nil || second.Names[0] != "Ada" {
		t.Fatalf("second default = %v, %v", second, err)
	}
}

// TestNamedRequiredJSONSchema retains required metadata and child constraints.
func TestNamedRequiredJSONSchema(t *testing.T) {
	t.Parallel()

	type Request struct {
		// Names receives the adapted Required presence constraint.
		Names namedNames `json:"names"`
	}

	schema := validate.Struct(func(object *validate.StructBuilder[Request]) {
		object.Field("Names", func(names *validate.NamedBuilder[namedNames]) {
			names.Use(validate.Slice(func(items *validate.SliceBuilder[string]) {
				items.Required().Min(1)
			}))
		})
	})

	wantSchema(t, schema,
		`{"properties":{"names":{"minItems":1,"type":"array"}},"required":["names"],"type":"object"}`)
	requireCodes(t, schema.Validate(context.Background(), Request{}),
		map[string][]string{"/names": {"required"}})
}

// TestNamedRejectsDifferentElementTypes prevents reflection conversion from
// weakening the exact-type invariant used by all child registrations.
func TestNamedRejectsDifferentElementTypes(t *testing.T) {
	t.Parallel()

	schema := validate.Named[namedNames](validate.Slice[int]())

	_, err := schema.JSONSchema()

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("export error = %v", err)
	}

	requirePanic(t, func() {
		_ = schema.Validate(context.Background(), nil)
	})
}

// TestNamedRejectsScalarTypes restricts adaptation to containers rather than
// introducing a general-purpose numeric or scalar conversion feature.
func TestNamedRejectsScalarTypes(t *testing.T) {
	t.Parallel()

	type Count int

	_, err := validate.Named[Count](validate.Int[int]()).JSONSchema()

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("scalar adapter error = %v", err)
	}
}

// TestNamedZeroAndNilSourceRejectConstruction ensures missing schemas cannot
// silently disable field checks or panic with unrelated nil-interface failures.
func TestNamedZeroAndNilSourceRejectConstruction(t *testing.T) {
	t.Parallel()

	var zero validate.NamedSchema[namedNames]

	_, err := zero.JSONSchema()

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("zero adapter error = %v", err)
	}

	_, err = validate.Named[namedNames](validate.Schema[[]string](nil)).JSONSchema()

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("nil source error = %v", err)
	}
}

// TestNamedSourcePointerSnapshot detaches source schema pointer reassignment.
func TestNamedSourcePointerSnapshot(t *testing.T) {
	t.Parallel()

	source := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Min(2)
	})
	schema := validate.Named[namedNames](&source)

	source = validate.Slice[string]()

	requireCode(t, schema.Validate(context.Background(), namedNames{"Ada"}), "slice.min")
}

// TestNamedRetainedDraftDoesNotChangeSchema preserves publication ownership
// even when the caller retains and later reuses the temporary adapter draft.
func TestNamedRetainedDraftDoesNotChangeSchema(t *testing.T) {
	t.Parallel()

	type Request struct {
		// Names supplies the named slice selected by the retained child draft.
		Names namedNames
	}
	var retained *validate.NamedBuilder[namedNames]

	schema := validate.Struct(func(object *validate.StructBuilder[Request]) {
		object.Field("Names", func(names *validate.NamedBuilder[namedNames]) {
			retained = names
			names.Use(validate.Slice[string]())
		})
	})

	requirePanic(t, func() {
		retained.Use(validate.Slice[string]())
	})

	if err := schema.Validate(context.Background(), Request{Names: namedNames{"Ada"}}); err != nil {
		t.Fatal(err)
	}
}

// TestNamedNestedCollectionPreparation supports adapters inside both element
// callbacks and pointer adapters without losing addressability of source views.
func TestNamedNestedCollectionPreparation(t *testing.T) {
	t.Parallel()

	schema := validate.Ptr(validate.Slice(func(groups *validate.SliceBuilder[namedNames]) {
		groups.Values(func(names *validate.NamedBuilder[namedNames]) {
			names.Use(validate.Slice(func(items *validate.SliceBuilder[string]) {
				items.Values(func(name *validate.StringBuilder[string]) {
					name.Trim()
				})
			}))
		})
	}))

	input := []namedNames{{" Ada "}}

	got, err := schema.Parse(context.Background(), &input)

	if err != nil || (*got)[0][0] != "Ada" || input[0][0] != " Ada " {
		t.Fatalf("nested Parse = %v, %v; input = %v", got, err, input)
	}
}

// TestNamedDoesNotWeakenExactFieldChecks ensures the new adapter is explicit:
// an ordinary slice draft still cannot validate a field of a named slice type.
func TestNamedDoesNotWeakenExactFieldChecks(t *testing.T) {
	t.Parallel()

	type Request struct {
		// Names is deliberately not the []string type of the selected draft.
		Names namedNames
	}

	requirePanic(t, func() {
		validate.Struct(func(object *validate.StructBuilder[Request]) {
			object.Field("Names", func(items *validate.SliceBuilder[string]) {
			})
		})
	})
}

// TestNamedComparisonRetainsSourceType documents the adapter's deliberate
// boundary: it does not rewrite exact types expected by cross-field rules.
func TestNamedComparisonRetainsSourceType(t *testing.T) {
	t.Parallel()

	type Request struct {
		// Names receives a source rule expecting an ordinary []string target.
		Names namedNames
		// Other is named too, so EqField cannot silently convert this target.
		Other namedNames
	}

	requirePanic(t, func() {
		validate.Struct(func(object *validate.StructBuilder[Request]) {
			object.Field("Names", func(names *validate.NamedBuilder[namedNames]) {
				names.Use(validate.Slice(func(items *validate.SliceBuilder[string]) {
					items.EqField("Other")
				}))
			})
		})
	})
}

// TestNamedInvalidSourceStaysInvalid rejects a zero source schema rather than
// hiding its construction problem behind otherwise-compatible container types.
func TestNamedInvalidSourceStaysInvalid(t *testing.T) {
	t.Parallel()

	var source validate.MapSchema[string, string]

	_, err := validate.Named[namedLabels](source).JSONSchema()

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("invalid source error = %v", err)
	}
}

// TestNamedUnreachablePromotedField preserves the engine's read-only zero view:
// required rules report absence without attempting a pointer-view conversion
// on an unaddressable field or allocating its missing embedded owner.
func TestNamedUnreachablePromotedField(t *testing.T) {
	t.Parallel()

	type Embedded struct {
		// Names is unreachable when Request.Embedded is nil.
		Names namedNames `json:"names"`
	}
	type Request struct {
		// Embedded promotes Names while retaining optional-owner semantics.
		*Embedded
	}

	schema := validate.Struct(func(object *validate.StructBuilder[Request]) {
		object.Field("Names", func(names *validate.NamedBuilder[namedNames]) {
			names.Use(validate.Slice(func(items *validate.SliceBuilder[string]) {
				items.Required()
			}))
		})
	})

	requireCodes(t, schema.Validate(context.Background(), Request{}),
		map[string][]string{"/names": {"required"}})
}
