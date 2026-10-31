package bcrypthash

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestBuilderCostReturnsDraft verifies replacement and receiver identity
// without normalizing invalid input before the construction boundary.
func TestBuilderCostReturnsDraft(t *testing.T) {
	t.Parallel()

	hasher := &Builder{}

	if hasher.Cost(10) != hasher || hasher.Cost(-1) != hasher {
		t.Fatal("setting returned a different draft")
	}

	if hasher.config.Cost != -1 {
		t.Fatal("cost did not replace the previous setting")
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
		hasher.Cost(6)
	}

	first := New(policy, func(hasher *Builder) {
		if hasher != retained || calls != 1 {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		hasher.Cost(7)
	})

	retained.Cost(9)
	firstDraft := retained

	second := New(policy)

	if retained == firstDraft || calls != 3 {
		t.Fatal("policy was replayed or draft was reused")
	}

	if first.config.Cost != 7 || second.config.Cost != 6 {
		t.Fatal("publication or replacement failed")
	}
}

// TestConstructionOmissionAndZeroDefaults verifies nil/empty expansion omits
// callbacks and an explicit zero cost restores the established default.
func TestConstructionOmissionAndZeroDefaults(t *testing.T) {
	t.Parallel()

	var policies []func(*Builder)

	for _, driver := range []*Driver{New(), New(policies...), New([]func(*Builder){}...),
		New(func(hasher *Builder) {
			hasher.Cost(0)
		})} {
		if driver.config.Cost != DefaultCost {
			t.Fatalf("cost = %d", driver.config.Cost)
		}
	}
}

// TestConstructionClampsInvalidCosts verifies normalization retains the
// existing native min/max bounds rather than rejecting construction.
func TestConstructionClampsInvalidCosts(t *testing.T) {
	t.Parallel()

	low := New(func(hasher *Builder) {
		hasher.Cost(-1)
	})
	high := New(func(hasher *Builder) {
		hasher.Cost(100)
	})

	if low.config.Cost != bcrypt.MinCost || high.config.Cost != bcrypt.MaxCost {
		t.Fatal("cost bounds changed")
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
