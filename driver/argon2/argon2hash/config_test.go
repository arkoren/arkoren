package argon2hash

import (
	"testing"

	"github.com/matthewhartstonge/argon2"
)

// TestBuilderSettingsReturnDraft verifies every setting keeps the same receiver
// and replaces its scalar value without normalizing it before publication.
func TestBuilderSettingsReturnDraft(t *testing.T) {
	t.Parallel()

	hasher := &Builder{}
	hasher.HashLength(64)
	hasher.SaltLength(24)
	hasher.TimeCost(1)
	hasher.MemoryCost(32)
	hasher.Parallelism(1)
	hasher.Mode(argon2.ModeArgon2id)
	hasher.Version(argon2.Version13)

	settings := []*Builder{
		hasher.HashLength(32), hasher.SaltLength(16), hasher.TimeCost(3),
		hasher.MemoryCost(64), hasher.Parallelism(2),
		hasher.Mode(argon2.ModeArgon2i), hasher.Version(argon2.Version10),
	}

	for _, setting := range settings {
		if setting != hasher {
			t.Fatal("setting returned a different draft")
		}
	}

	want := config{
		HashLength:  32,
		SaltLength:  16,
		TimeCost:    3,
		MemoryCost:  64,
		Parallelism: 2,
		Mode:        argon2.ModeArgon2i,
		Version:     argon2.Version10,
	}

	if hasher.config != want {
		t.Fatalf("settings = %#v, want %#v", hasher.config, want)
	}
}

// TestConstructionCallbacksOrderedAndDetached verifies one draft per New,
// ordered once-only edits, reusable policies and independent publication.
func TestConstructionCallbacksOrderedAndDetached(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	policy := func(hasher *Builder) {
		calls++
		retained = hasher
		hasher.TimeCost(2)
	}

	first := New(policy, func(hasher *Builder) {
		if hasher != retained || calls != 1 {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		hasher.TimeCost(3)
	})

	retained.TimeCost(9)
	firstDraft := retained

	second := New(policy)

	if retained == firstDraft || calls != 3 {
		t.Fatal("policy was replayed or draft was reused")
	}

	if first.config.TimeCost != 3 || second.config.TimeCost != 2 {
		t.Fatal("publication or replacement failed")
	}
}

// TestConstructionOmissionAndZeroDefaults verifies nil/empty expansion is
// omission and zero settings are normalized only after all callbacks.
func TestConstructionOmissionAndZeroDefaults(t *testing.T) {
	t.Parallel()

	var policies []BuilderFunc
	want := argon2.DefaultConfig()

	for _, driver := range []*Driver{New(), New(policies...), New([]BuilderFunc{}...),
		New(func(hasher *Builder) {
			hasher.HashLength(0)
			hasher.SaltLength(0)
			hasher.TimeCost(0)
			hasher.MemoryCost(0)
			hasher.Parallelism(0)
			hasher.Mode(0)
			hasher.Version(0)
		})} {
		if driver.config != want {
			t.Fatalf("config = %#v, want %#v", driver.config, want)
		}
	}
}

// TestConstructionNilCallbackPanics verifies nil entries remain ordinary
// function-call misuse rather than being filtered as omitted settings.
func TestConstructionNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	New(nil)
}

// TestConstructionCallbackPanicPropagates verifies application panics are not
// recovered or translated into hashing errors by the constructor.
func TestConstructionCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recovered := recover(); recovered != "policy" {
			t.Fatalf("panic = %v", recovered)
		}
	}()

	New(func(*Builder) {
		panic("policy")
	})
}

// TestInvalidModePassedThrough verifies construction does not introduce eager
// native parameter validation or normalize nonzero unsupported parameters.
func TestInvalidModePassedThrough(t *testing.T) {
	t.Parallel()

	driver := New(func(hasher *Builder) {
		hasher.Mode(argon2.Mode(99))
		hasher.MemoryCost(8)
		hasher.Parallelism(1)
	})

	if driver.config.Mode != argon2.Mode(99) {
		t.Fatal("nonzero unsupported mode was rewritten")
	}
}
