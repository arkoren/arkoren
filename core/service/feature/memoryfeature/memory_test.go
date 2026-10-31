package memoryfeature_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/featuretest"
	"arkoren.dev/core/service/feature/memoryfeature"
)

// harness adapts [memoryfeature.Driver] to [featuretest.Harness].
type harness struct {
	// driver is the memory implementation adapted to the conformance harness.
	driver *memoryfeature.Driver
}

// Driver returns the driver under test.
func (harness harness) Driver() feature.Driver {
	return harness.driver
}

// Set installs flag: its default value (skipped when nil), each override and
// finally the disabled state, which needs the flag to exist already.
func (harness harness) Set(t *testing.T, flag featuretest.Flag) {
	harness.driver.Delete(flag.Key)

	value := flag.Value

	for _, override := range flag.Overrides {
		value = override
		break
	}

	switch value.(type) {
	case bool:
		installFixture[bool](t, harness.driver, flag)
	case string:
		installFixture[string](t, harness.driver, flag)
	case int64:
		installFixture[int64](t, harness.driver, flag)
	case float64:
		installFixture[float64](t, harness.driver, flag)
	default:
		t.Fatalf("unsupported fixture type %T", value)
	}

	if flag.Disabled {
		harness.driver.Disable(flag.Key)
	}
}

// installFixture adapts the suite's raw canonical values to typed memory
// setup. Suite fixtures have one type throughout their value and overrides.
func installFixture[T feature.Primitive](t *testing.T, driver *memoryfeature.Driver, fixture featuretest.Flag) {
	t.Helper()

	var fallback T
	definition := feature.NewFlag(fixture.Key, fallback)

	if fixture.Value != nil {
		set(t, driver, definition, fixture.Value.(T))
	}

	for subject, value := range fixture.Overrides {
		if err := driver.SetFor(definition, subject, value.(T)); err != nil {
			t.Fatal(err)
		}
	}
}

// Delete removes the flag key.
func (harness harness) Delete(_ *testing.T, key string) {
	harness.driver.Delete(key)
}

// TestConformance runs the feature flag conformance suite against the memory
// driver.
func TestConformance(t *testing.T) {
	t.Parallel()

	featuretest.Run(t, func(*testing.T) featuretest.Harness {
		return harness{driver: memoryfeature.New()}
	})
}

// evaluate runs one Evaluate call and fails the test on an unexpected error.
func evaluate(t *testing.T, driver *memoryfeature.Driver, key string, kind feature.Kind, subjectKey string) feature.Result {
	t.Helper()

	result, err := driver.Evaluate(context.Background(), feature.Request{
		Key: key, Kind: kind, Subject: feature.Subject{Key: subjectKey},
	})

	if err != nil {
		t.Fatalf("Evaluate(%q): %v", key, err)
	}

	return result
}

// TestSetStoresIntegersAsInt64 verifies every Go int kind is read as int64.
func TestSetStoresIntegersAsInt64(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	set(t, driver, feature.NewFlag("a", 0), 7)
	set(t, driver, feature.NewFlag("b", int8(0)), int8(7))
	set(t, driver, feature.NewFlag("c", int32(0)), int32(7))

	for _, key := range []string{"a", "b", "c"} {
		if got := evaluate(t, driver, key, feature.KindInt, "").Value; got != int64(7) {
			t.Fatalf("%s = %#v, want int64(7)", key, got)
		}
	}
}

// TestSetStoresFloat32AsFloat64 verifies float32 widens to float64.
func TestSetStoresFloat32AsFloat64(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	set(t, driver, feature.NewFlag("ratio", float32(0)), float32(0.5))

	if got := evaluate(t, driver, "ratio", feature.KindFloat, "").Value; got != float64(0.5) {
		t.Fatalf("value = %#v, want float64(0.5)", got)
	}
}

// TestSetRejectsZeroDefinition verifies setup validates before mutation.
func TestSetRejectsZeroDefinition(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()

	if err := driver.Set(feature.Flag[bool]{}, true); !errors.Is(err, feature.ErrInvalidFlag) {
		t.Fatalf("Set error = %v, want ErrInvalidFlag", err)
	}

	if err := driver.SetFor(feature.Flag[bool]{}, "u1", true); !errors.Is(err, feature.ErrInvalidFlag) {
		t.Fatalf("SetFor error = %v, want ErrInvalidFlag", err)
	}
}

// TestSetKeepsOverrides verifies replacing the default keeps overrides.
func TestSetKeepsOverrides(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	definition := feature.NewFlag("flag", "")
	set(t, driver, definition, "a")

	if err := driver.SetFor(definition, "u1", "b"); err != nil {
		t.Fatal(err)
	}

	set(t, driver, definition, "c")

	if got := evaluate(t, driver, "flag", feature.KindString, "u1").Value; got != "b" {
		t.Fatalf("u1 = %v, want b", got)
	}

	if got := evaluate(t, driver, "flag", feature.KindString, "u2").Value; got != "c" {
		t.Fatalf("u2 = %v, want c", got)
	}
}

// TestSetForTargetsOneSubject verifies the override reason and default reason.
func TestSetForTargetsOneSubject(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	definition := feature.NewFlag("flag", false)
	set(t, driver, definition, false)

	if err := driver.SetFor(definition, "u1", true); err != nil {
		t.Fatal(err)
	}

	match := evaluate(t, driver, "flag", feature.KindBool, "u1")

	if match.Value != true || match.Reason != feature.ReasonTargetingMatch {
		t.Fatalf("u1 = %+v, want true with targeting_match", match)
	}

	other := evaluate(t, driver, "flag", feature.KindBool, "u2")

	if other.Value != false || other.Reason != feature.ReasonStatic {
		t.Fatalf("u2 = %+v, want false with static", other)
	}
}

// TestSetForWithoutDefaultReturnsDefaultReason verifies a flag with only
// overrides has no value for other subjects.
func TestSetForWithoutDefaultReturnsDefaultReason(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()

	if err := driver.SetFor(feature.NewFlag("flag", false), "u1", true); err != nil {
		t.Fatal(err)
	}

	result := evaluate(t, driver, "flag", feature.KindBool, "u2")

	if result.Value != nil || result.Reason != feature.ReasonDefault {
		t.Fatalf("result = %+v, want nil value with default", result)
	}
}

// TestAnonymousSubjectNeverMatchesOverride verifies an empty subject key
// does not hit an override stored under the empty key.
func TestAnonymousSubjectNeverMatchesOverride(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	definition := feature.NewFlag("flag", "")
	set(t, driver, definition, "a")

	if err := driver.SetFor(definition, "", "b"); err != nil {
		t.Fatal(err)
	}

	if got := evaluate(t, driver, "flag", feature.KindString, "").Value; got != "a" {
		t.Fatalf("value = %v, want a", got)
	}
}

// TestDisableAndEnable verifies a disabled flag stays known and recovers.
func TestDisableAndEnable(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	set(t, driver, feature.NewFlag("flag", false), true)
	driver.Disable("flag")

	off := evaluate(t, driver, "flag", feature.KindBool, "")

	if off.Value != nil || off.Reason != feature.ReasonDisabled {
		t.Fatalf("disabled = %+v, want nil value with disabled", off)
	}

	driver.Enable("flag")

	if got := evaluate(t, driver, "flag", feature.KindBool, "").Value; got != true {
		t.Fatalf("enabled value = %v, want true", got)
	}
}

// TestDisableMissingKeyDoesNotCreateIt verifies Disable on an unknown key
// leaves it unknown.
func TestDisableMissingKeyDoesNotCreateIt(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	driver.Disable("flag")

	_, err := driver.Evaluate(context.Background(), feature.Request{Key: "flag", Kind: feature.KindBool})

	if !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestDeleteRemovesFlagAndOverrides verifies Delete forgets everything.
func TestDeleteRemovesFlagAndOverrides(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	definition := feature.NewFlag("flag", false)
	set(t, driver, definition, true)

	if err := driver.SetFor(definition, "u1", false); err != nil {
		t.Fatal(err)
	}

	driver.Delete("flag")

	_, err := driver.Evaluate(context.Background(), feature.Request{
		Key: "flag", Kind: feature.KindBool, Subject: feature.Subject{Key: "u1"},
	})

	if !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestEvaluateConvertsIntToFloat verifies an int64 flag read as a float.
func TestEvaluateConvertsIntToFloat(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	set(t, driver, feature.NewFlag("flag", 0), 3)

	if got := evaluate(t, driver, "flag", feature.KindFloat, "").Value; got != float64(3) {
		t.Fatalf("value = %#v, want float64(3)", got)
	}
}

// TestEvaluateReturnsWholeFloatForInt verifies a float flag read as an int is
// left for the facade to check.
func TestEvaluateReturnsWholeFloatForInt(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	set(t, driver, feature.NewFlag("flag", 0.0), 3.5)

	if got := evaluate(t, driver, "flag", feature.KindInt, "").Value; got != 3.5 {
		t.Fatalf("value = %#v, want float64(3.5)", got)
	}
}

// TestEvaluateTypeMismatch verifies a string flag read as a bool.
func TestEvaluateTypeMismatch(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	set(t, driver, feature.NewFlag("flag", ""), "on")

	_, err := driver.Evaluate(context.Background(), feature.Request{Key: "flag", Kind: feature.KindBool})

	if !errors.Is(err, feature.ErrTypeMismatch) {
		t.Fatalf("err = %v, want ErrTypeMismatch", err)
	}
}

// TestEvaluateCanceledContext verifies the context error wins over the flag.
func TestEvaluateCanceledContext(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	set(t, driver, feature.NewFlag("flag", false), true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := driver.Evaluate(ctx, feature.Request{Key: "flag", Kind: feature.KindBool})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestConcurrentSetAndEvaluate verifies writers and readers race-free.
func TestConcurrentSetAndEvaluate(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	definition := feature.NewFlag("flag", 0)
	set(t, driver, definition, 0)

	var wg sync.WaitGroup

	for i := range 20 {
		wg.Go(func() {
			if err := driver.SetFor(definition, "u", i); err != nil {
				t.Error(err)

				return
			}

			driver.Disable("flag")
			driver.Enable("flag")

			_, _ = driver.Evaluate(context.Background(), feature.Request{
				Key: "flag", Kind: feature.KindInt, Subject: feature.Subject{Key: "u"},
			})
		})
	}

	wg.Wait()
}

// set installs a typed value and stops the test before evaluation if setup
// rejects the definition. It preserves the caller location in failures.
func set[T feature.Primitive](t *testing.T, driver *memoryfeature.Driver, definition feature.Flag[T], value T) {
	t.Helper()

	if err := driver.Set(definition, value); err != nil {
		t.Fatal(err)
	}
}
