package feature_test

import (
	"testing"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/memoryfeature"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestRetainedFlagsPolicyCannotReconfigurePublishedFacade verifies detached
// provider settings while retaining the live driver's dynamic evaluations.
func TestRetainedFlagsPolicyCannotReconfigurePublishedFacade(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := memoryfeature.New()
	var retained *feature.Builder
	flags := feature.New(driver, func(flags *feature.Builder) {
		retained = flags
		flags.Telemetry(fake.Telemetry(t))
		flags.Name("primary")
	})
	retained.Telemetry(telemetry.New(nil))
	retained.Name("mutated")

	checkout := feature.NewFlag("checkout", false)

	if err := driver.Set(checkout, true); err != nil {
		t.Fatal(err)
	}

	value, err := flags.Value(t.Context(), checkout, feature.Subject{})

	if err != nil {
		t.Fatal(err)
	}

	if !value {
		t.Fatal("published flags did not retain the live driver")
	}

	fake.AssertSpan(t, "feature evaluate", telemetry.String("feature_flag.provider.name", "primary"))
}

// TestFlagsPoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestFlagsPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *feature.Builder
	calls := 0
	component := feature.New(nil, func(flags *feature.Builder) {
		calls++
		retained = flags
	}, func(flags *feature.Builder) {
		if calls != 1 || retained != flags {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	feature.New(nil, func(flags *feature.Builder) {
		if retained == flags {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestFlagsNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestFlagsNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*feature.Builder)
	empty := []func(*feature.Builder){}

	if feature.New(nil, absent...) == nil || feature.New(nil, empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestFlagsNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestFlagsNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		feature.New(nil, nil, func(flags *feature.Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestFlagsPolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestFlagsPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		feature.New(nil, func(flags *feature.Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestFlagsTelemetryReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestFlagsTelemetryReturnsDraft(t *testing.T) {
	t.Parallel()

	flags := &feature.Builder{}

	if flags.Telemetry(nil) != flags {
		t.Fatal("setting returned a different draft")
	}
}

// TestFlagsNameReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestFlagsNameReturnsDraft(t *testing.T) {
	t.Parallel()

	flags := &feature.Builder{}

	if flags.Name("primary") != flags {
		t.Fatal("setting returned a different draft")
	}
}

// TestFlagsEventsReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestFlagsEventsReturnsDraft(t *testing.T) {
	t.Parallel()

	flags := &feature.Builder{}

	if flags.Events(nil) != flags {
		t.Fatal("setting returned a different draft")
	}
}
