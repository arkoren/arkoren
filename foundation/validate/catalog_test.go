package validate_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// EmbedInner provides promoted fields behind an embedded pointer. Tests
// distinguish missing embedded values from allocated nested data.
//
// Example:
//
//	value := EmbedInner{
//		Name: "Ada",
//	}
//	_ = value.Name
type EmbedInner struct {
	// Name holds the name observed by string rules or sibling conditions.
	// Required must also check it when the embedded parent pointer is nil.
	//
	// Example:
	//
	//	value := EmbedInner{}
	//	value.Name = "Ada"
	Name string
	// Tags provides slice storage for child rules and copy-isolation checks.
	// A nil parent embedding must not allocate this collection to validate it.
	//
	// Example:
	//
	//	value := EmbedInner{}
	//	value.Tags = []string{"admin"}
	Tags []string
}

// embedOuter adds a sibling to an embedded pointer fixture. It exercises
// promoted-field traversal without allocating absent parents.
type embedOuter struct {
	// EmbedInner promotes fields through an optional embedded pointer.
	*EmbedInner
	// X provides an independent sibling for condition and promotion checks.
	X int
}

// nestedEmbed places embedded-pointer fixtures in direct and optional child
// positions. Both paths must preserve the same absence semantics.
type nestedEmbed struct {
	// Inner provides a nested object containing an embedded pointer.
	Inner embedOuter `json:"inner"`
	// Opt provides an optional nested object for nil-traversal checks.
	Opt *embedOuter
}

// timeHolder combines boolean and time fields for presence checks. The time
// comparison also checks zero instants with non-UTC locations.
type timeHolder struct {
	// Flag provides the boolean target for acceptance or presence checks.
	Flag string
	// At provides the instant used by zero-time presence or omission checks.
	At time.Time
	// Other provides an independent sibling for comparison or registration
	// checks.
	Other time.Time
}

// codeLiteral recognizes built-in violation code literals in source. The
// message-coverage test uses it to find rules lacking English translations.
var codeLiteral = regexp.MustCompile(`^((string|number|bool|time|slice|map)\.[a-z_]+|[a-z]+(_[a-z]+)*_field|(required|prohibited)_[a-z_]+|one_of|not_one_of|required)$`)

// derivedObject pairs a condition gate with a constrained value. Independent
// schema constructions must not change each other through shared storage.
type derivedObject struct {
	// Gate supplies a sibling observation for conditional validation.
	Gate string
	// Val provides the constrained value governed by the Gate sibling.
	Val string
}

// requireConditionReason verifies the Val field's single violation carries
// code and the message that explains the condition.
func requireConditionReason(t *testing.T, field validate.StringSchema[string], input derivedObject, code, want string) {
	t.Helper()

	schema := validate.Struct[derivedObject](func(object *validate.StructBuilder[derivedObject]) {
		object.Field("Gate", func(value *validate.StringBuilder[string]) {
		})
		object.Field("Val", func(value *validate.StringBuilder[string]) {
			value.Use(field)
		})
	})

	errs, ok := errors.AsType[validate.Errors](schema.Validate(context.Background(), input))

	if !ok || len(errs["/Val"]) != 1 {
		t.Fatalf("errs = %v", errs)
	}

	violation := errs["/Val"][0]

	if violation.Code != code || violation.Message != want {
		t.Errorf("got %q %q, want %q %q", violation.Code, violation.Message, code, want)
	}
}

// TestConditionReasonMessagesEq verifies the eq condition explains itself.
func TestConditionReasonMessagesEq(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.Eq("Gate", "on")
		})
	}), derivedObject{
		Gate: "on",
	}, "required_if", "is required when Gate is on")
}

// TestConditionReasonMessagesNotEq verifies the not eq condition explains itself.
func TestConditionReasonMessagesNotEq(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.NotEq("Gate", "off")
		})
	}), derivedObject{
		Gate: "on",
	}, "required_if", "is required when Gate is not off")
}

// TestConditionReasonMessagesIn verifies the in condition explains itself.
func TestConditionReasonMessagesIn(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.In("Gate", "a", "b")
		})
	}), derivedObject{
		Gate: "a",
	}, "required_if", "is required when Gate is one of: a, b")
}

// TestConditionReasonMessagesNotIn verifies the not in condition explains itself.
func TestConditionReasonMessagesNotIn(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.NotIn("Gate", "a", "b")
		})
	}), derivedObject{
		Gate: "c",
	}, "required_if", "is required when Gate is not one of: a, b")
}

// TestConditionReasonMessagesPresent verifies the present condition explains itself.
func TestConditionReasonMessagesPresent(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.Present("Gate")
		})
	}), derivedObject{
		Gate: "c",
	}, "required_if", "is required when Gate is present")
}

// TestConditionReasonMessagesAbsent verifies the absent condition explains itself.
func TestConditionReasonMessagesAbsent(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.Absent("Gate")
		})
	}), derivedObject{}, "required_if", "is required when Gate is not present")
}

// TestConditionReasonMessagesUnless verifies the unless condition explains itself.
func TestConditionReasonMessagesUnless(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.RequiredUnless(func(condition *validate.ConditionBuilder) {
			condition.Eq("Gate", "on")
		})
	}), derivedObject{
		Gate: "off",
	}, "required_unless", "is required unless Gate is on")
}

// TestConditionReasonMessagesProhibitedIf verifies the prohibited if condition explains itself.
func TestConditionReasonMessagesProhibitedIf(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.ProhibitedIf(func(condition *validate.ConditionBuilder) {
			condition.Eq("Gate", "on")
		})
	}), derivedObject{
		Gate: "on",
		Val:  "x",
	}, "prohibited_if", "must not be present when Gate is on")
}

// TestConditionReasonMessagesProhibitedUnless verifies the prohibited unless condition explains itself.
func TestConditionReasonMessagesProhibitedUnless(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.ProhibitedUnless(func(condition *validate.ConditionBuilder) {
			condition.Eq("Gate", "on")
		})
	}), derivedObject{
		Gate: "off",
		Val:  "x",
	}, "prohibited_unless", "must not be present unless Gate is on")
}

// TestConditionReasonMessagesGenericAnd verifies the generic and condition explains itself.
func TestConditionReasonMessagesGenericAnd(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.And(func(condition *validate.ConditionBuilder) {
				condition.Eq("Gate", "on")
			})
		})
	}), derivedObject{
		Gate: "on",
	}, "required_if", "is required")
}

// TestConditionReasonMessagesGenericNot verifies the generic not condition explains itself.
func TestConditionReasonMessagesGenericNot(t *testing.T) {
	t.Parallel()

	requireConditionReason(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.ProhibitedUnless(func(condition *validate.ConditionBuilder) {
			condition.Not(func(condition *validate.ConditionBuilder) {
				condition.Eq("Gate", "on")
			})
		})
	}), derivedObject{
		Gate: "on",
		Val:  "x",
	}, "prohibited_unless", "must not be present")
}

// TestConditionReasonMessagesLocalization verifies catalogs may translate the
// plain code or the variant, and the violation params.
func TestConditionReasonMessagesLocalization(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.Struct[derivedObject](func(object *validate.StructBuilder[derivedObject]) {
		object.Field("Gate", func(value *validate.StringBuilder[string]) {
		})
		object.Field("Val", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("Gate", "on")
			})
		})
	})

	err := schema.Validate(ctx, derivedObject{
		Gate: "on",
	})

	errs, _ := errors.AsType[validate.Errors](err)

	// A catalog that translates only the plain code wins over the built-in
	// English variant.
	catalog := validate.NewCatalogContext(ctx, validate.Catalog{
		Default: "es",
		Locales: map[string]validate.Messages{"es": {"required_if": "es obligatorio"}},
	})

	if got := errs.Localize(catalog)["/Val"][0].Message; got != "es obligatorio" {
		t.Errorf("localized = %q", got)
	}

	// A catalog may translate the variant.
	catalog = validate.NewCatalogContext(ctx, validate.Catalog{
		Default: "es",
		Locales: map[string]validate.Messages{"es": {"required_if:eq": "obligatorio si {field} es {value}"}},
	})

	if got := errs.Localize(catalog)["/Val"][0].Message; got != "obligatorio si Gate es on" {
		t.Errorf("localized = %q", got)
	}

	params := errs["/Val"][0].Params

	if !reflect.DeepEqual(slices.Sorted(maps.Keys(params)), []string{"field", "op", "value"}) {
		t.Errorf("params = %v", params)
	}
}

// spanish returns a fresh partial Spanish catalog for fallback tests. Missing
// codes deliberately exercise the later lookup stages.
func spanish() validate.Messages {
	return validate.Messages{
		"string.min": "debe tener al menos {min} caracteres",
		"required":   "es obligatorio",
	}
}

// messageOf validates input and returns the first root violation message. It
// fails the calling test when validation does not return the expected Errors
// value.
func messageOf(t *testing.T, ctx context.Context, schema validate.StringSchema[string], input string) string {
	t.Helper()

	err := schema.Validate(ctx, input)

	errs, ok := errors.AsType[validate.Errors](err)

	if !ok {
		t.Fatalf("err = %v", err)
	}

	return errs[""][0].Message
}

// TestLocaleExactMatch verifies the requested locale is used.
func TestLocaleExactMatch(t *testing.T) {
	t.Parallel()

	ctx := validate.NewCatalogContext(context.Background(), validate.Catalog{
		Default: "en",
		Locales: map[string]validate.Messages{"es-MX": spanish(), "es": {"string.min": "base"}},
	})
	ctx = validate.NewLocaleContext(ctx, "es-MX")

	got := messageOf(t, ctx, validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}), "a")

	if got != "debe tener al menos 3 caracteres" {
		t.Fatalf("message = %q", got)
	}
}

// TestLocaleBaseLanguageFallback verifies es-AR falls back to es.
func TestLocaleBaseLanguageFallback(t *testing.T) {
	t.Parallel()

	ctx := validate.NewCatalogContext(context.Background(), validate.Catalog{
		Default: "en",
		Locales: map[string]validate.Messages{"es": spanish()},
	})
	ctx = validate.NewLocaleContext(ctx, "es-AR")

	if got := messageOf(t, ctx, validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}), "a"); got != "debe tener al menos 3 caracteres" {
		t.Fatalf("message = %q", got)
	}
}

// TestLocaleCatalogDefaultFallback verifies a missing locale uses the
// catalog default before built-in English.
func TestLocaleCatalogDefaultFallback(t *testing.T) {
	t.Parallel()

	ctx := validate.NewCatalogContext(context.Background(), validate.Catalog{
		Default: "es",
		Locales: map[string]validate.Messages{"es": spanish()},
	})
	ctx = validate.NewLocaleContext(ctx, "fr")

	if got := messageOf(t, ctx, validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}), "a"); got != "debe tener al menos 3 caracteres" {
		t.Fatalf("message = %q", got)
	}
}

// TestLocaleBuiltInEnglishFallback verifies codes missing from a locale
// fall back to built-in English.
func TestLocaleBuiltInEnglishFallback(t *testing.T) {
	t.Parallel()

	ctx := validate.NewCatalogContext(context.Background(), validate.Catalog{
		Default: "es",
		Locales: map[string]validate.Messages{"es": spanish()},
	})
	ctx = validate.NewLocaleContext(ctx, "es")

	if got := messageOf(t, ctx, validate.String(func(value *validate.StringBuilder[string]) {
		value.Max(1)
	}), "abc"); got != "must be at most 1 characters" {
		t.Fatalf("message = %q", got)
	}
}

// TestLocaleCodeFallback verifies an unknown code renders as itself.
func TestLocaleCodeFallback(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Check(func(context.Context, string) error {
			return validate.Fail("custom.code", nil)
		})
	})

	if got := messageOf(t, context.Background(), schema, "x"); got != "custom.code" {
		t.Fatalf("message = %q", got)
	}
}

// TestLocaleCustomCodeTranslated verifies catalogs can add messages for
// application codes, with interpolation.
func TestLocaleCustomCodeTranslated(t *testing.T) {
	t.Parallel()

	ctx := validate.NewCatalogContext(context.Background(), validate.Catalog{
		Locales: map[string]validate.Messages{"en": {"custom.taken": "{name} is taken, {missing}"}},
	})
	ctx = validate.NewLocaleContext(ctx, "en")

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Check(func(context.Context, string) error {
			return validate.Fail("custom.taken", map[string]any{"name": "ada"})
		})
	})

	if got := messageOf(t, ctx, schema, "x"); got != "ada is taken, {missing}" {
		t.Fatalf("message = %q", got)
	}
}

// TestLocaleInterpolationFormats verifies lists and times are formatted.
func TestLocaleInterpolationFormats(t *testing.T) {
	t.Parallel()

	got := messageOf(t, context.Background(), validate.String(func(value *validate.StringBuilder[string]) {
		value.OneOf("a", "b")
	}), "z")

	if got != "must be one of: a, b" {
		t.Fatalf("message = %q", got)
	}

	moment := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	err := validate.Time(func(timestamp *validate.TimeBuilder) {
		timestamp.After(moment)
	}).Validate(context.Background(), moment.Add(-time.Hour))

	errs, _ := errors.AsType[validate.Errors](err)

	if got := errs[""][0].Message; got != "must be after 2026-01-02T03:04:05Z" {
		t.Fatalf("message = %q", got)
	}
}
