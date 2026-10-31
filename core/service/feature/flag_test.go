package feature_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/memoryfeature"
)

// TestFlagRetainsLiteralNameAndFallback checks publication preserves the
// exact provider identity and named Go type without normalization.
func TestFlagRetainsLiteralNameAndFallback(t *testing.T) {
	t.Parallel()

	definition := feature.NewFlag("Checkout.v2/Enabled", Plan("free"))

	if err := definition.Err(); err != nil {
		t.Fatal(err)
	}

	if definition.Name() != "Checkout.v2/Enabled" || definition.Fallback() != Plan("free") {
		t.Fatalf("definition = %q, %q", definition.Name(), definition.Fallback())
	}
}

// TestInvalidFlagSkipsDriver checks malformed definitions return the declared
// fallback with an error reason before calling a provider.
func TestInvalidFlagSkipsDriver(t *testing.T) {
	t.Parallel()

	driver := &stub{}
	definition := feature.NewFlag("", "fallback")

	detail, err := feature.New(driver).ValueDetail(t.Context(), definition, feature.Subject{})

	if !errors.Is(err, feature.ErrInvalidFlag) || detail.Value != "fallback" || detail.Reason != feature.ReasonError {
		t.Fatalf("ValueDetail = %+v, %v", detail, err)
	}

	if driver.calls.Load() != 0 {
		t.Fatalf("driver calls = %d", driver.calls.Load())
	}
}

// TestZeroFlagRejected checks a missing constructor cannot evaluate the empty
// provider key, even if its Go value type is otherwise supported.
func TestZeroFlagRejected(t *testing.T) {
	t.Parallel()

	definition := feature.Flag[bool]{}

	value, err := feature.New(memoryfeature.New()).Value(t.Context(), definition, feature.Subject{})

	if !errors.Is(err, feature.ErrInvalidFlag) || value {
		t.Fatalf("Value = %v, %v", value, err)
	}
}

// TestFlagSetupPreservesLargeNamedInteger checks typed memory setup does not
// round-trip integer values through float64 or discard named type identity.
func TestFlagSetupPreservesLargeNamedInteger(t *testing.T) {
	t.Parallel()

	// limit models an application-owned integer unit with exact precision.
	type limit int64
	definition := feature.NewFlag("upload-bytes", limit(10))
	driver := memoryfeature.New()
	const want limit = 9007199254740993

	if err := driver.Set(definition, want); err != nil {
		t.Fatal(err)
	}

	value, err := feature.New(driver).Value(t.Context(), definition, feature.Subject{})

	if err != nil || value != want {
		t.Fatalf("Value = %d, %v; want %d", value, err, want)
	}
}

// TestFlagOverrideAndFallbackShareDefinition checks setup changes provider
// values, never the definition's fallback, including override-only flags.
func TestFlagOverrideAndFallbackShareDefinition(t *testing.T) {
	t.Parallel()

	definition := feature.NewFlag("plan", Plan("free"))
	driver := memoryfeature.New()

	if err := driver.SetFor(definition, "u1", Plan("pro")); err != nil {
		t.Fatal(err)
	}

	flags := feature.New(driver)

	value, err := flags.Value(t.Context(), definition, feature.Subject{Key: "u1"})

	if err != nil || value != Plan("pro") {
		t.Fatalf("override = %q, %v", value, err)
	}

	value, err = flags.Value(t.Context(), definition, feature.Subject{Key: "u2"})

	if err != nil || value != definition.Fallback() {
		t.Fatalf("fallback = %q, %v", value, err)
	}
}

// TestFlagCanceledEvaluationReturnsFallback checks a typed definition retains
// the driver's cancellation error and the same safe fallback.
func TestFlagCanceledEvaluationReturnsFallback(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	value, err := feature.New(memoryfeature.New()).Value(ctx, feature.NewFlag("limit", 10), feature.Subject{})

	if !errors.Is(err, context.Canceled) || value != 10 {
		t.Fatalf("Value = %d, %v", value, err)
	}
}

// TestFlagRetypingRejected checks evaluation and setup cannot select a type
// independently from a shared definition or substitute unsupported values.
func TestFlagRetypingRejected(t *testing.T) {
	t.Parallel()

	command := exec.CommandContext(t.Context(), "go", "test", "-gcflags=-e", "./testdata/retyping")

	output, err := command.CombinedOutput()

	if err == nil {
		t.Fatal("flag retyping compiled")
	}

	for _, diagnostic := range []string{
		"cannot convert feature.NewFlag",
		"cannot use feature.NewFlag",
		"argument to flags.Value[int]",
		"argument to flags.ValueDetail[int]",
		"argument to driver.Set[int]",
		"argument to driver.SetFor[int]",
		"does not satisfy feature.Primitive",
	} {
		if !strings.Contains(string(output), diagnostic) {
			t.Fatalf("missing %q diagnostic: %s", diagnostic, output)
		}
	}
}
