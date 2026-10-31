package telemetry_test

import (
	"fmt"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry"
)

// checkAttrKind fails the test unless attr has key "k", the wanted kind, value
// and text form, and is valid.
func checkAttrKind(t *testing.T, attr telemetry.Attr, kind telemetry.AttrKind, value any, text string) {
	t.Helper()

	if attr.Key() != "k" || attr.Kind() != kind || !attr.Valid() {
		t.Fatalf("key, kind, valid = %q, %v, %v", attr.Key(), attr.Kind(), attr.Valid())
	}

	if got := attr.Text(); got != text {
		t.Errorf("Text = %q, want %q", got, text)
	}

	if got := attr.String(); got != "k="+text {
		t.Errorf("String = %q", got)
	}

	switch want := value.(type) {
	case []string:
		got, _ := attr.Value().([]string)

		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("Value = %v, want %v", got, want)
		}
	default:
		if got := attr.Value(); got != want {
			t.Errorf("Value = %v, want %v", got, want)
		}
	}
}

// TestAttrKindString checks the constructor's kind, accessors, and text form.
func TestAttrKindString(t *testing.T) {
	t.Parallel()

	checkAttrKind(t, telemetry.String("k", "v"), telemetry.AttrString, "v", "v")
}

// TestAttrKindInt checks the constructor's kind, accessors, and text form.
func TestAttrKindInt(t *testing.T) {
	t.Parallel()

	checkAttrKind(t, telemetry.Int("k", 7), telemetry.AttrInt64, int64(7), "7")
}

// TestAttrKindInt64 checks the constructor's kind, accessors, and text form.
func TestAttrKindInt64(t *testing.T) {
	t.Parallel()

	checkAttrKind(t, telemetry.Int64("k", -9), telemetry.AttrInt64, int64(-9), "-9")
}

// TestAttrKindFloat checks the constructor's kind, accessors, and text form.
func TestAttrKindFloat(t *testing.T) {
	t.Parallel()

	checkAttrKind(t, telemetry.Float64("k", 1.5), telemetry.AttrFloat64, 1.5, "1.5")
}

// TestAttrKindBool checks the constructor's kind, accessors, and text form.
func TestAttrKindBool(t *testing.T) {
	t.Parallel()

	checkAttrKind(t, telemetry.Bool("k", true), telemetry.AttrBool, true, "true")
}

// TestAttrKindStrings checks the constructor's kind, accessors, and text form.
func TestAttrKindStrings(t *testing.T) {
	t.Parallel()

	checkAttrKind(t, telemetry.Strings("k", []string{"a", "b"}), telemetry.AttrStrings, []string{"a", "b"}, "a,b")
}

// TestAttrKindDuration checks the constructor's kind, accessors, and text form.
func TestAttrKindDuration(t *testing.T) {
	t.Parallel()

	checkAttrKind(t, telemetry.Duration("k", 1500*time.Millisecond), telemetry.AttrFloat64, 1.5, "1.5")
}

// TestAttrInvalid checks that the zero Attr and empty keys are invalid.
func TestAttrInvalid(t *testing.T) {
	t.Parallel()

	if (telemetry.Attr{}).Valid() || telemetry.String("", "v").Valid() {
		t.Fatal("zero attribute or empty key reported valid")
	}

	if (telemetry.Attr{}).Value() != nil {
		t.Fatal("zero attribute has a value")
	}
}

// TestAttrImmutable checks that slices are copied on the way in and out.
func TestAttrImmutable(t *testing.T) {
	t.Parallel()

	input := []string{"a", "b"}
	attr := telemetry.Strings("k", input)

	input[0] = "changed"
	attr.AsStrings()[1] = "changed"

	if got := attr.AsStrings(); got[0] != "a" || got[1] != "b" {
		t.Fatalf("attribute changed through shared slice: %v", got)
	}
}

// TestAttrEqual checks Equal across kinds and values.
func TestAttrEqual(t *testing.T) {
	t.Parallel()

	if !telemetry.Int("k", 1).Equal(telemetry.Int64("k", 1)) {
		t.Error("Int and Int64 with equal values differ")
	}

	if telemetry.Int("k", 1).Equal(telemetry.Float64("k", 1)) {
		t.Error("different kinds are equal")
	}

	if telemetry.Strings("k", []string{"a"}).Equal(telemetry.Strings("k", []string{"b"})) {
		t.Error("different slices are equal")
	}

	if telemetry.Bool("k", false).Equal(telemetry.Bool("k", true)) {
		t.Error("different bools are equal")
	}
}

// TestAccessorsOtherKinds checks zero values for mismatched accessors.
func TestAccessorsOtherKinds(t *testing.T) {
	t.Parallel()

	attr := telemetry.String("k", "v")

	if attr.AsInt64() != 0 || attr.AsFloat64() != 0 || attr.AsBool() || attr.AsStrings() != nil {
		t.Fatal("accessors of another kind must return zero values")
	}

	if telemetry.Int("k", 3).AsString() != "" {
		t.Fatal("AsString of an integer must be empty")
	}
}

// ExampleAttr_Value demonstrates typed, copied attribute values used by public
// snapshot consumers. Editing the returned slice leaves the Attr unchanged.
func ExampleAttr_Value() {
	attr := telemetry.Strings("roles", []string{"reader"})

	if roles, ok := attr.Value().([]string); ok {
		roles[0] = "editor"
	}

	fmt.Println(attr.Key(), attr.Kind(), attr.Valid())
	fmt.Println(attr.AsStrings(), attr.Text(), attr.String())
	fmt.Println(attr.Equal(telemetry.Strings("roles", []string{"reader"})))
	// Output:
	// roles strings true
	// [reader] reader roles=reader
	// true
}
