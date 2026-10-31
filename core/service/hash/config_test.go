package hash_test

import (
	"testing"

	"arkoren.dev/core/service/hash"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestHashBuilderPublishesSnapshot checks ordered once-only callbacks, fluent
// returns and detached publication of a shared telemetry service selection.
func TestHashBuilderPublishesSnapshot(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	metrics := fake.Telemetry(t)
	var retained *hash.Builder
	calls := 0
	hasher := hash.New(&driver{needsRehash: true}, func(hasher *hash.Builder) {
		retained = hasher
		calls++
	}, func(hasher *hash.Builder) {
		if hasher != retained || calls != 1 || hasher.Telemetry(metrics) != hasher {
			t.Fatal("callback order or draft identity changed")
		}

		calls++
	})
	retained.Telemetry(telemetry.New(nil))

	if !hasher.NeedsRehash(nil) || calls != 2 {
		t.Fatal("callback count or rehash behavior changed")
	}

	fake.AssertCounter(t, "arkoren.hash.operations", 1, telemetry.String("arkoren.hash.outcome", "rehash_needed"))
}

// TestHashBuilderNilExpansionPreservesDefaults checks both nil and empty
// callback slices remain omissions rather than erroneous callback entries.
func TestHashBuilderNilExpansionPreservesDefaults(t *testing.T) {
	t.Parallel()

	var policies []hash.BuilderFunc
	hasher := hash.New(identity{}, policies...)

	if hasher.NeedsRehash(nil) {
		t.Fatal("nil policies changed driver behavior")
	}

	hasher = hash.New(identity{}, []hash.BuilderFunc{}...)

	if hasher.NeedsRehash(nil) {
		t.Fatal("empty policies changed driver behavior")
	}
}

// TestHashBuilderNilEntryPanics distinguishes a supplied nil entry from an
// omitted policy slice. Callback misuse remains a programmer panic.
func TestHashBuilderNilEntryPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	hash.New(identity{}, nil)
}

// TestHashBuilderPanicPreservesIdentity verifies construction invokes policies
// directly, without recovery, translation or replay through another constructor.
func TestHashBuilderPanicPreservesIdentity(t *testing.T) {
	t.Parallel()

	want := new(int)
	defer func() {
		if got := recover(); got != want {
			t.Fatalf("panic = %v, want %v", got, want)
		}
	}()
	hash.New(identity{}, func(hasher *hash.Builder) {
		panic(want)
	})
}
