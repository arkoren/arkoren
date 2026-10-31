package feature_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/memoryfeature"
)

// stub is a Driver that returns a fixed result and counts its calls.
type stub struct {
	// result is the fixed evaluation result returned to the facade.
	result feature.Result
	// calls counts evaluations so tests can detect pre-driver validation.
	calls atomic.Int32
}

// Evaluate returns the stub's result.
func (stub *stub) Evaluate(context.Context, feature.Request) (feature.Result, error) {
	stub.calls.Add(1)

	return stub.result, nil
}

// Plan is a named string type used to read flags.
type Plan string

// TestValueReturnsDefaultWhenDisabled verifies a disabled flag is not an error.
func TestValueReturnsDefaultWhenDisabled(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	definition := feature.NewFlag("flag", false)

	if err := driver.Set(definition, true); err != nil {
		t.Fatal(err)
	}

	driver.Disable("flag")

	got, err := feature.New(driver).Value(context.Background(), definition, feature.Subject{})

	if err != nil || got {
		t.Fatalf("Value = %v, %v; want false, nil", got, err)
	}
}

// TestValueReturnsDefaultAndErrNotFound verifies an unknown key.
func TestValueReturnsDefaultAndErrNotFound(t *testing.T) {
	t.Parallel()

	got, err := feature.New(memoryfeature.New()).Value(context.Background(), feature.NewFlag("missing", "fallback"), feature.Subject{})

	if !errors.Is(err, feature.ErrNotFound) || got != "fallback" {
		t.Fatalf("Value = %q, %v; want fallback, ErrNotFound", got, err)
	}
}

// TestValueReadsIntFlag verifies T int against an int flag.
func TestValueReadsIntFlag(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	definition := feature.NewFlag("limit", 0)

	if err := driver.Set(definition, 25); err != nil {
		t.Fatal(err)
	}

	got, err := feature.New(driver).Value(context.Background(), definition, feature.Subject{})

	if err != nil || got != 25 {
		t.Fatalf("Value = %d, %v; want 25, nil", got, err)
	}
}

// TestValueInt8OverflowReturnsDefault verifies overflow is a type mismatch.
func TestValueInt8OverflowReturnsDefault(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()

	// The provider holds a wider integer than the application's definition.
	if err := driver.Set(feature.NewFlag("limit", 0), 300); err != nil {
		t.Fatal(err)
	}

	got, err := feature.New(driver).Value(context.Background(), feature.NewFlag("limit", int8(7)), feature.Subject{})

	if !errors.Is(err, feature.ErrTypeMismatch) || got != 7 {
		t.Fatalf("Value = %d, %v; want 7, ErrTypeMismatch", got, err)
	}
}

// TestValueIntReadsWholeFloat verifies float64(3) reads as the int 3.
func TestValueIntReadsWholeFloat(t *testing.T) {
	t.Parallel()

	driver := &stub{result: feature.Result{Value: float64(3)}}

	got, err := feature.New(driver).Value(context.Background(), feature.NewFlag("flag", 0), feature.Subject{})

	if err != nil || got != 3 {
		t.Fatalf("Value = %d, %v; want 3, nil", got, err)
	}
}

// TestValueIntRejectsFractionalFloat verifies 3.5 is a type mismatch.
func TestValueIntRejectsFractionalFloat(t *testing.T) {
	t.Parallel()

	driver := &stub{result: feature.Result{Value: 3.5}}

	got, err := feature.New(driver).Value(context.Background(), feature.NewFlag("flag", 9), feature.Subject{})

	if !errors.Is(err, feature.ErrTypeMismatch) || got != 9 {
		t.Fatalf("Value = %d, %v; want 9, ErrTypeMismatch", got, err)
	}
}

// TestValueFloatReadsInt64 verifies an int64 result reads as a float.
func TestValueFloatReadsInt64(t *testing.T) {
	t.Parallel()

	driver := &stub{result: feature.Result{Value: int64(4)}}

	got, err := feature.New(driver).Value(context.Background(), feature.NewFlag("flag", 0.0), feature.Subject{})

	if err != nil || got != 4.0 {
		t.Fatalf("Value = %v, %v; want 4, nil", got, err)
	}
}

// TestValueReadsNamedType verifies a named string type works.
func TestValueReadsNamedType(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	definition := feature.NewFlag("plan", Plan("free"))

	if err := driver.Set(definition, Plan("pro")); err != nil {
		t.Fatal(err)
	}

	got, err := feature.New(driver).Value(context.Background(), definition, feature.Subject{})

	if err != nil || got != Plan("pro") {
		t.Fatalf("Value = %q, %v; want pro, nil", got, err)
	}
}

// TestValueRejectsUnsupportedAttributeWithoutCallingDriver verifies subject
// validation happens before the driver runs.
func TestValueRejectsUnsupportedAttributeWithoutCallingDriver(t *testing.T) {
	t.Parallel()

	driver := &stub{}
	subject := feature.Subject{Key: "u1", Attributes: map[string]any{"seats": 5}}

	got, err := feature.New(driver).Value(context.Background(), feature.NewFlag("flag", true), subject)

	if !errors.Is(err, feature.ErrInvalidSubject) || !got {
		t.Fatalf("Value = %v, %v; want true, ErrInvalidSubject", got, err)
	}

	if driver.calls.Load() != 0 {
		t.Fatalf("driver called %d times, want 0", driver.calls.Load())
	}
}

// TestValueAcceptsAllAttributeTypes verifies the six allowed types.
func TestValueAcceptsAllAttributeTypes(t *testing.T) {
	t.Parallel()

	driver := &stub{result: feature.Result{Value: true}}
	subject := feature.Subject{Key: "u1", Attributes: map[string]any{
		"s": "x", "b": true, "i": int64(1), "f": 1.5, "l": []string{"a"}, "t": time.Unix(0, 0),
	}}

	got, err := feature.New(driver).Value(context.Background(), feature.NewFlag("flag", false), subject)

	if err != nil || !got {
		t.Fatalf("Value = %v, %v; want true, nil", got, err)
	}
}

// TestValueWrongDriverTypeIsTypeMismatch verifies a driver bug is reported.
func TestValueWrongDriverTypeIsTypeMismatch(t *testing.T) {
	t.Parallel()

	driver := &stub{result: feature.Result{Value: "yes"}}

	got, err := feature.New(driver).Value(context.Background(), feature.NewFlag("flag", false), feature.Subject{})

	if !errors.Is(err, feature.ErrTypeMismatch) || got {
		t.Fatalf("Value = %v, %v; want false, ErrTypeMismatch", got, err)
	}
}

// TestValueDetailReturnsVariantAndReason verifies the metadata passes through.
func TestValueDetailReturnsVariantAndReason(t *testing.T) {
	t.Parallel()

	driver := &stub{result: feature.Result{Value: true, Variant: "on", Reason: feature.ReasonSplit}}

	detail, err := feature.New(driver).ValueDetail(context.Background(), feature.NewFlag("flag", false), feature.Subject{})
	want := feature.Detail[bool]{Value: true, Variant: "on", Reason: feature.ReasonSplit}

	if err != nil || detail != want {
		t.Fatalf("ValueDetail = %+v, %v; want %+v, nil", detail, err, want)
	}
}

// TestValueDetailKeepsDriverReasonForNilValue verifies the default case.
func TestValueDetailKeepsDriverReasonForNilValue(t *testing.T) {
	t.Parallel()

	driver := &stub{result: feature.Result{Variant: "off", Reason: feature.ReasonDisabled}}

	detail, err := feature.New(driver).ValueDetail(context.Background(), feature.NewFlag("flag", 5), feature.Subject{})
	want := feature.Detail[int]{Value: 5, Variant: "off", Reason: feature.ReasonDisabled}

	if err != nil || detail != want {
		t.Fatalf("ValueDetail = %+v, %v; want %+v, nil", detail, err, want)
	}
}

// TestValueDetailReportsReasonErrorOnFailure verifies the failure detail.
func TestValueDetailReportsReasonErrorOnFailure(t *testing.T) {
	t.Parallel()

	detail, err := feature.New(memoryfeature.New()).ValueDetail(context.Background(), feature.NewFlag("missing", "d"), feature.Subject{})
	want := feature.Detail[string]{Value: "d", Reason: feature.ReasonError}

	if !errors.Is(err, feature.ErrNotFound) || detail != want {
		t.Fatalf("ValueDetail = %+v, %v; want %+v, ErrNotFound", detail, err, want)
	}
}

// TestDriverAsFindsMemoryDriver verifies the concrete driver is reachable.
func TestDriverAsFindsMemoryDriver(t *testing.T) {
	t.Parallel()

	driver := memoryfeature.New()
	flags := feature.New(driver)

	got, ok := flags.DriverAs[*memoryfeature.Driver]()

	if !ok || got != driver {
		t.Fatalf("DriverAs = %v, %v; want the memory driver", got, ok)
	}

	if flags.Driver() != feature.Driver(driver) {
		t.Fatal("Driver() did not return the configured driver")
	}
}

// TestKindAndReasonStrings verifies the lowercase names used by telemetry.
func TestKindAndReasonStrings(t *testing.T) {
	t.Parallel()

	if got := feature.KindFloat.String(); got != "float" {
		t.Fatalf("KindFloat = %q", got)
	}

	if got := feature.ReasonTargetingMatch.String(); got != "targeting_match" {
		t.Fatalf("ReasonTargetingMatch = %q", got)
	}

	if got := feature.ReasonUnknown.String(); got != "unknown" {
		t.Fatalf("ReasonUnknown = %q", got)
	}
}

// TestKindsJSONUsesStrings preserves portable evaluation kinds rather than
// numeric implementation details. The empty kind remains outside the valid set.
func TestKindsJSONUsesStrings(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]feature.Kind{"", feature.KindBool, feature.KindString, feature.KindInt, feature.KindFloat})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["","bool","string","int","float"]` {
		t.Fatalf("kinds JSON = %s", data)
	}
}

// TestKindJSONPreservesUnknownValues keeps serialization independent of raw
// evaluation validation, so newer kind strings do not disappear on round trip.
func TestKindJSONPreservesUnknownValues(t *testing.T) {
	t.Parallel()

	var kind feature.Kind

	if err := json.Unmarshal([]byte(`"future-kind"`), &kind); err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(kind)

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `"future-kind"` || kind.String() != "future-kind" {
		t.Fatalf("kind = %q, JSON = %s", kind, data)
	}
}

// TestReasonJSONPreservesUnknownValues allows newer provider reasons to survive
// a round trip instead of becoming an unrelated numeric enum value.
func TestReasonJSONPreservesUnknownValues(t *testing.T) {
	t.Parallel()

	var reason feature.Reason

	if err := json.Unmarshal([]byte(`"provider_rule"`), &reason); err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(reason)

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `"provider_rule"` || reason.String() != "provider_rule" {
		t.Fatalf("reason = %q, JSON = %s", reason, data)
	}
}

// TestEmptyReasonDefaultsToUnknown preserves the meaning of drivers that omit
// a resolution reason while publishing a canonical facade result.
func TestEmptyReasonDefaultsToUnknown(t *testing.T) {
	t.Parallel()

	driver := &stub{result: feature.Result{Value: true}}

	detail, err := feature.New(driver).ValueDetail(t.Context(), feature.NewFlag("checkout", false), feature.Subject{})

	if err != nil {
		t.Fatal(err)
	}

	if detail.Reason != feature.ReasonUnknown {
		t.Fatalf("reason = %q, want unknown", detail.Reason)
	}

	if feature.Reason("").String() != "unknown" {
		t.Fatal("zero reason lost its default meaning")
	}
}
