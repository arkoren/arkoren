package database_test

import (
	"testing"

	"arkoren.dev/core/service/database"
)

// settings is the value type stored by the JSON tests.
type settings struct {
	// Theme is a string field.
	Theme string `json:"theme"`

	// Beta is a boolean field.
	Beta bool `json:"beta"`
}

// TestJSONScanAcceptsStringAndBytes checks the text column representation decodes.
func TestJSONScanAcceptsStringAndBytes(t *testing.T) {
	t.Parallel()

	const document = `{"theme":"dark","beta":true}`
	want := settings{Theme: "dark", Beta: true}

	var got database.JSON[settings]

	if err := got.Scan(document); err != nil {
		t.Fatalf("%s: Scan() error = %v", "string", err)
	}

	if got.V != want {
		t.Errorf("%s: V = %+v, want %+v", "string", got.V, want)
	}
}

// TestJSONScanAcceptsBytes checks a byte-backed column decodes to the typed value.
func TestJSONScanAcceptsBytes(t *testing.T) {
	t.Parallel()

	const document = `{"theme":"dark","beta":true}`
	want := settings{Theme: "dark", Beta: true}
	var got database.JSON[settings]

	if err := got.Scan([]byte(document)); err != nil {
		t.Fatalf("%s: Scan() error = %v", "bytes", err)
	}

	if got.V != want {
		t.Errorf("%s: V = %+v, want %+v", "bytes", got.V, want)
	}
}

// TestJSONScanNilResetsToZero verifies SQL NULL clears the value.
func TestJSONScanNilResetsToZero(t *testing.T) {
	t.Parallel()

	got := database.JSON[settings]{V: settings{Theme: "dark"}}

	if err := got.Scan(nil); err != nil {
		t.Fatal(err)
	}

	if got.V != (settings{}) {
		t.Errorf("V = %+v, want zero", got.V)
	}
}

// TestJSONScanRejectsBadInput checks unsupported column types preserve existing state.
func TestJSONScanRejectsBadInput(t *testing.T) {
	t.Parallel()

	got := database.JSON[settings]{V: settings{Theme: "keep"}}

	if err := got.Scan(7); err == nil {
		t.Errorf("%s: Scan() error = nil, want error", "int")
	}

	if got.V.Theme != "keep" {
		t.Errorf("%s: V changed on error: %+v", "int", got.V)
	}
}

// TestJSONScanRejectsMalformedDocument checks decoding failure preserves existing state.
func TestJSONScanRejectsMalformedDocument(t *testing.T) {
	t.Parallel()

	got := database.JSON[settings]{V: settings{Theme: "keep"}}

	if err := got.Scan("{not json"); err == nil {
		t.Errorf("%s: Scan() error = nil, want error", "invalid")
	}

	if got.V.Theme != "keep" {
		t.Errorf("%s: V changed on error: %+v", "invalid", got.V)
	}
}

// TestJSONValueEncodesString verifies Value returns the JSON text.
func TestJSONValueEncodesString(t *testing.T) {
	t.Parallel()

	got, err := database.JSON[settings]{V: settings{Theme: "dark", Beta: true}}.Value()

	if err != nil {
		t.Fatal(err)
	}

	if got != `{"theme":"dark","beta":true}` {
		t.Errorf("Value() = %v", got)
	}
}

// TestJSONRoundTrip verifies Value output scans back to the same value.
func TestJSONRoundTrip(t *testing.T) {
	t.Parallel()

	want := database.JSON[[]int]{V: []int{1, 2, 3}}

	encoded, err := want.Value()

	if err != nil {
		t.Fatal(err)
	}

	var got database.JSON[[]int]

	if err := got.Scan(encoded); err != nil {
		t.Fatal(err)
	}

	if len(got.V) != 3 || got.V[2] != 3 {
		t.Errorf("round trip = %v, want %v", got.V, want.V)
	}
}

// TestJSONZeroMapUsesJSONV2Object pins the documented zero-map representation
// to JSON v2, rather than the legacy JSON-null behavior.
func TestJSONZeroMapUsesJSONV2Object(t *testing.T) {
	t.Parallel()

	value, err := (database.JSON[map[string]int]{}).Value()

	if err != nil || value != "{}" {
		t.Fatalf("Value = %v, %v; want {}", value, err)
	}
}

// TestJSONZeroSliceUsesJSONV2Array verifies a nil slice is an empty JSON array,
// not SQL NULL or a JSON null literal.
func TestJSONZeroSliceUsesJSONV2Array(t *testing.T) {
	t.Parallel()

	value, err := (database.JSON[[]int]{}).Value()

	if err != nil || value != "[]" {
		t.Fatalf("Value = %v, %v; want []", value, err)
	}
}

// TestJSONZeroBytesUsesJSONV2String distinguishes the base64-encoded byte-slice
// representation from ordinary arrays and JSON null.
func TestJSONZeroBytesUsesJSONV2String(t *testing.T) {
	t.Parallel()

	value, err := (database.JSON[[]byte]{}).Value()

	if err != nil || value != `""` {
		t.Fatalf("Value = %v, %v; want empty JSON string", value, err)
	}
}

// TestJSONZeroPointerUsesJSONNull verifies a pointer is still encoded as the
// JSON null literal and never as a nil SQL driver.Value.
func TestJSONZeroPointerUsesJSONNull(t *testing.T) {
	t.Parallel()

	value, err := (database.JSON[*int]{}).Value()

	if err != nil || value != "null" {
		t.Fatalf("Value = %v, %v; want null string", value, err)
	}
}
