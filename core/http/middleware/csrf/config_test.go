package csrf

import (
	"testing"
)

// TestCompletedSettingsAppliesCallbacksAfterReplacement verifies callback order.
func TestCompletedSettingsAppliesCallbacksAfterReplacement(t *testing.T) {
	t.Parallel()

	config := newSettings(
		func(protection *Builder) {
			protection.settings = settings{
				TrustedOrigins: []string{"https://a.example"},
			}
		},
		func(protection *Builder) {
			protection.TrustedOrigins("https://b.example")
		},
	)

	if len(config.TrustedOrigins) != 1 || config.TrustedOrigins[0] != "https://b.example" {
		t.Fatalf("config = %#v", config)
	}
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	Middleware(func(protection *Builder) {
		retained = protection
		order++

		if protection.TrustedOrigins("https://app.example.com") != protection {
			t.Fatal("setting returned another draft")
		}
	}, func(protection *Builder) {
		if order != 1 || protection != retained {
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
	Middleware(nil, func(protection *Builder) {
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
	Middleware(func(protection *Builder) {
		panic("construction sentinel")
	}, func(protection *Builder) {
		reached = true
	})
}

// TestTrustedOriginsDetachesInputAndDraft checks capture and publication copies.
// Later caller or retained-root edits cannot alter the fixed policy.
func TestTrustedOriginsDetachesInputAndDraft(t *testing.T) {
	t.Parallel()

	values := []string{"https://app.example.com"}
	var retained *Builder
	config := newSettings(func(protection *Builder) {
		retained = protection
		protection.TrustedOrigins(values...)

		values[0] = "changed before publication"
	})

	retained.settings.TrustedOrigins[0] = "changed after publication"

	if config.TrustedOrigins[0] != "https://app.example.com" {
		t.Fatal("published settings alias caller or retained draft")
	}
}
