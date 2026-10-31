package secureheaders

import (
	"testing"
)

// TestCompletedSettingsAppliesEverySetting verifies each setting after replacement.
func TestCompletedSettingsAppliesEverySetting(t *testing.T) {
	t.Parallel()

	config := newSettings(
		func(headers *Builder) {
			headers.settings = settings{}
		},
		func(headers *Builder) {
			headers.ContentTypeOptions("a")
		},
		func(headers *Builder) {
			headers.FrameOptions("b")
		},
		func(headers *Builder) {
			headers.ReferrerPolicy("c")
		},
		func(headers *Builder) {
			headers.XSSProtection("d")
		},
		func(headers *Builder) {
			headers.StrictTransportSecurity("e")
		},
		func(headers *Builder) {
			headers.ContentSecurityPolicy("f")
		},
		func(headers *Builder) {
			headers.PermissionsPolicy("g")
		},
	)

	want := settings{
		ContentTypeOptions:      "a",
		FrameOptions:            "b",
		ReferrerPolicy:          "c",
		XSSProtection:           "d",
		StrictTransportSecurity: "e",
		ContentSecurityPolicy:   "f",
		PermissionsPolicy:       "g",
	}

	if config != want {
		t.Fatalf("config = %#v, want %#v", config, want)
	}
}

// TestCompletedSettingsStartsFromDefaults verifies the secure defaults.
func TestCompletedSettingsStartsFromDefaults(t *testing.T) {
	t.Parallel()

	config := newSettings()

	if config.ContentTypeOptions != "nosniff" || config.FrameOptions != "DENY" || config.XSSProtection != "0" {
		t.Fatalf("config = %#v", config)
	}
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	Middleware(func(headers *Builder) {
		retained = headers
		order++

		if headers.FrameOptions("") != headers {
			t.Fatal("setting returned another draft")
		}
	}, func(headers *Builder) {
		if order != 1 || headers != retained {
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
	Middleware(nil, func(headers *Builder) {
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
	Middleware(func(headers *Builder) {
		panic("construction sentinel")
	}, func(headers *Builder) {
		reached = true
	})
}

// TestBuilderPublicationDetachesRoot checks completed settings do not retain the
// mutable construction root. Services and function captures remain shared.
func TestBuilderPublicationDetachesRoot(t *testing.T) {
	t.Parallel()

	var retained *Builder
	config := newSettings(func(headers *Builder) {
		retained = headers
		headers.FrameOptions("")
	})
	retained.FrameOptions("DENY")

	if config.FrameOptions != "" {
		t.Fatal("retained draft changed published settings")
	}
}
