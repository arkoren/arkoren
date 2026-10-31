package locale

import (
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestCatalogDetachesBothMapLayers checks capture and publication ownership.
// Retaining either caller maps or the draft cannot change fixed translations.
func TestCatalogDetachesBothMapLayers(t *testing.T) {
	t.Parallel()

	catalog := validate.Catalog{
		Default: "en",
		Locales: map[string]validate.Messages{
			"en": {
				"required": "original",
			},
		},
	}
	var retained *Builder
	config := newSettings(func(localePolicy *Builder) {
		retained = localePolicy
		localePolicy.Catalog(catalog)

		catalog.Locales["en"]["required"] = "changed at capture"
	})

	retained.settings.Catalog.Locales["en"]["required"] = "changed after publication"
	retained.settings.Catalog.Locales["es"] = validate.Messages{}

	if config.Catalog.Locales["en"]["required"] != "original" || len(config.Catalog.Locales) != 1 {
		t.Fatal("published translations alias caller or draft maps")
	}
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	Middleware(func(localePolicy *Builder) {
		retained = localePolicy
		order++

		if localePolicy.QueryParam("lang") != localePolicy {
			t.Fatal("setting returned another draft")
		}
	}, func(localePolicy *Builder) {
		if order != 1 || localePolicy != retained {
			t.Fatal("callbacks were reordered or used different drafts")
		}

		order++
	})

	if order != 2 {
		t.Fatalf("callback count = %d, want 2", order)
	}
}

// TestConstructionOmissionAndEmptyExpansions supply zero callbacks. A supplied nil
// is instead misuse and must panic before a later callback can execute.
func TestConstructionOmissionAndEmptyExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)
	Middleware(omitted...)
	Middleware([]func(*Builder){}...)

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	Middleware(nil, func(localePolicy *Builder) {
		reached = true
	})
}

// TestConstructionApplicationPanicPropagates keeps callback misuse separate from
// ordinary constructor validation and prevents later policies from executing.
func TestConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	reached := false
	defer func() {
		if value := recover(); value != "construction sentinel" || reached {
			t.Fatalf("panic = %v, later callback = %v", value, reached)
		}
	}()
	Middleware(func(localePolicy *Builder) {
		panic("construction sentinel")
	}, func(localePolicy *Builder) {
		reached = true
	})
}
