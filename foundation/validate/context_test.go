package validate_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestCatalogContextCapturesMessageMaps checks ownership at the context
// boundary through Parse's rendered diagnostics. Both the locale registry and
// each message map must be independent from subsequent caller edits.
func TestCatalogContextCapturesMessageMaps(t *testing.T) {
	t.Parallel()

	messages := validate.Messages{"required": "original"}
	catalog := validate.Catalog{
		Default: "en",
		Locales: map[string]validate.Messages{"en": messages},
	}
	ctx := validate.NewCatalogContext(context.Background(), catalog)
	messages["required"] = "edited inner map"
	catalog.Locales["en"] = validate.Messages{"required": "replaced outer entry"}

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Required()
	})

	_, err := schema.Parse(ctx, "")

	violation := diagnosticViolation(t, err, "")

	if violation.Message != "original" {
		t.Fatalf("captured message = %q, want original", violation.Message)
	}
}

// ExampleNewCatalogContext renders messages in another language.
func ExampleNewCatalogContext() {
	catalog := validate.Catalog{
		Default: "en",
		Locales: map[string]validate.Messages{
			"es": {"string.min": "debe tener al menos {min} caracteres"},
		},
	}

	ctx := validate.NewCatalogContext(context.Background(), catalog)
	ctx = validate.NewLocaleContext(ctx, "es-MX")

	err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}).Validate(ctx, "ab")

	errs, ok := errors.AsType[validate.Errors](err)

	if !ok {
		fmt.Println(err)

		return
	}

	fmt.Println(errs[""][0].Message)

	// Output: debe tener al menos 3 caracteres
}

// TestLocaleFrom verifies the locale round-trips through the context.
func TestLocaleFrom(t *testing.T) {
	t.Parallel()

	if _, ok := validate.LocaleFromContext(context.Background()); ok {
		t.Fatal("empty context reports a locale")
	}

	locale, ok := validate.LocaleFromContext(validate.NewLocaleContext(context.Background(), "es-MX"))

	if !ok || locale != "es-MX" {
		t.Fatalf("locale = %q, %v", locale, ok)
	}
}

// TestWithClockPinsFuture verifies the context clock drives Future.
func TestWithClockPinsFuture(t *testing.T) {
	t.Parallel()

	moment := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx := validate.NewClockContext(context.Background(), func() time.Time {
		return moment
	})

	requireCodes(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.Future()
	}).Validate(ctx, moment.Add(-time.Second)),
		map[string][]string{"": {"time.future"}})

	if err := validate.Time(func(moment *validate.TimeBuilder) {
		moment.Future()
	}).Validate(ctx, moment.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}
