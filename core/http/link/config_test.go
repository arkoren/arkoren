package link

import (
	"errors"
	"testing"

	"arkoren.dev/foundation/key"
)

// TestSignerSettingsDetachSecretsAndLists checks capture and publication copies,
// including nested secret bytes that remain reachable from a retained draft.
func TestSignerSettingsDetachSecretsAndLists(t *testing.T) {
	t.Parallel()

	previous := []byte("0123456789abcdef0123456789abcdef")

	material, err := key.NewShared(previous)

	if err != nil {
		t.Fatal(err)
	}

	names := []string{"utm_source"}
	var retained *Builder
	config := newSettings(func(signer *Builder) {
		retained = signer
		signer.Previous(material)
		signer.IgnoreQuery(names...)

		previous[0] = 'x'
		names[0] = "changed"
	})

	retained.settings.PreviousSecrets[0] = key.Shared{}
	retained.settings.IgnoreQuery[0] = "changed again"
	retained.Origin("https://changed.example.com")

	if string(config.PreviousSecrets[0].Bytes()) != "0123456789abcdef0123456789abcdef" || config.IgnoreQuery[0] != "utm_source" {
		t.Fatal("published settings alias inputs or retained draft")
	}
}

// TestSignerValidationFollowsAllCallbacks preserves invalid-secret validation
// timing while allowing later callbacks to replace invalid optional settings.
func TestSignerValidationFollowsAllCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := New(key.Shared{}, func(signer *Builder) {
		calls++
		signer.Origin("invalid")
	}, func(signer *Builder) {
		calls++
		signer.Origin("https://app.example.com")
	})

	if !errors.Is(err, ErrInvalidSecret) || calls != 2 {
		t.Fatalf("err = %v, callbacks = %d", err, calls)
	}
}

// signingFixture creates validated material for construction-only scenarios.
func signingFixture(t *testing.T) key.Shared {
	t.Helper()

	material, err := key.NewShared([]byte("current secret current secret 32"))

	if err != nil {
		t.Fatal(err)
	}

	return material
}

// TestConstructionCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestConstructionCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	order := 0
	_, _ = New(signingFixture(t), func(signer *Builder) {
		retained = signer
		order++

		if signer.Origin("https://app.example.com") != signer {
			t.Fatal("setting returned another draft")
		}
	}, func(signer *Builder) {
		if order != 1 || signer != retained {
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
	_, _ = New(signingFixture(t), omitted...)
	_, _ = New(signingFixture(t), []func(*Builder){}...)

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	_, _ = New(signingFixture(t), nil, func(signer *Builder) {
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
	_, _ = New(signingFixture(t), func(signer *Builder) {
		panic("construction sentinel")
	}, func(signer *Builder) {
		reached = true
	})
}
