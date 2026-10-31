package validate_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestErrorStringUnknownCode verifies the summary names unknown codes.
func TestErrorStringUnknownCode(t *testing.T) {
	t.Parallel()

	errs := validate.Errors{"/a": {{
		Code: "app.custom",
	}}}

	if got := errs.Error(); got != "validation failed: /a: app.custom" {
		t.Fatalf("Error() = %q", got)
	}
}

// diagnosticViolation extracts one violation at pointer through the public
// error contract. Unexpected operational errors or missing diagnostics fail the
// caller instead of hiding an ownership failure behind a type assertion panic.
func diagnosticViolation(t *testing.T, err error, pointer string) validate.Violation {
	t.Helper()

	violations, ok := errors.AsType[validate.Errors](err)

	if !ok {
		t.Fatalf("Parse error = %v (%T), want validate.Errors", err, err)
	}

	if len(violations[pointer]) != 1 {
		t.Fatalf("violations at %q = %#v, want one", pointer, violations[pointer])
	}

	return violations[pointer][0]
}

// TestDiagnosticOwnershipFailCapturesTypedSlice checks that a caller can reuse
// its []int storage after Fail without rewriting the recorded rule arguments.
// Parse also verifies that the snapshot keeps the original concrete Go type.
func TestDiagnosticOwnershipFailCapturesTypedSlice(t *testing.T) {
	t.Parallel()

	numbers := []int{3, 5}
	failure := validate.Fail("app.numbers", map[string]any{"numbers": numbers})
	numbers[0] = 99

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return failure
		})
	})

	_, err := schema.Parse(context.Background(), "value")

	got := diagnosticViolation(t, err, "").Params["numbers"]

	if !reflect.DeepEqual(got, []int{3, 5}) {
		t.Fatalf("captured numbers = %#v (%T), want []int{3, 5}", got, got)
	}
}

// TestDiagnosticOwnershipFailCapturesTypedMap checks that a string-valued map
// is captured just like map[string]any, without converting its concrete type.
func TestDiagnosticOwnershipFailCapturesTypedMap(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"role": "reader"}
	failure := validate.Fail("app.labels", map[string]any{"labels": labels})
	labels["role"] = "writer"

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return failure
		})
	})

	_, err := schema.Parse(context.Background(), "value")

	got := diagnosticViolation(t, err, "").Params["labels"]

	if !reflect.DeepEqual(got, map[string]string{"role": "reader"}) {
		t.Fatalf("captured labels = %#v (%T), want the original typed map", got, got)
	}
}

// TestDiagnosticOwnershipParseDetachesReusableFail checks both existing and
// future Parse results. Mutating one result must not rewrite a previously
// returned result or the cached failure retained by the Check callback.
func TestDiagnosticOwnershipParseDetachesReusableFail(t *testing.T) {
	t.Parallel()

	failure := validate.Fail("app.reusable", map[string]any{
		"limit":  3,
		"nested": []any{map[string]any{"limit": 3}},
	})

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return failure
		})
	})

	_, err := schema.Parse(context.Background(), "first")

	first := diagnosticViolation(t, err, "")

	_, err = schema.Parse(context.Background(), "second")

	second := diagnosticViolation(t, err, "")

	first.Params["limit"] = 99
	first.Params["nested"].([]any)[0].(map[string]any)["limit"] = 99

	want := map[string]any{"limit": 3, "nested": []any{map[string]any{"limit": 3}}}

	if !reflect.DeepEqual(second.Params, want) {
		t.Fatalf("existing result changed: %#v", second.Params)
	}

	_, err = schema.Parse(context.Background(), "third")

	third := diagnosticViolation(t, err, "")

	if !reflect.DeepEqual(third.Params, want) {
		t.Fatalf("future result changed: %#v", third.Params)
	}
}

// TestDiagnosticOwnershipParseDetachesReusableAt exercises the sibling-field
// reporting branch. It must publish independent parameters at the JSON field
// pointer rather than lend the reusable At error's captured map to a result.
func TestDiagnosticOwnershipParseDetachesReusableAt(t *testing.T) {
	t.Parallel()

	// booking gives At a registered Go field whose JSON name differs.
	type booking struct {
		// End receives the object-level diagnostic at /end.
		End int `json:"end"`
	}

	failure := validate.At("End", "app.order", map[string]any{
		"limits": []int{3, 5},
	})

	schema := validate.Struct(func(object *validate.StructBuilder[booking]) {
		object.Field("End", func(number *validate.IntBuilder[int]) {
		})
		object.Check(func(context.Context, booking) error {
			return failure
		})
	})

	_, err := schema.Parse(context.Background(), booking{End: 2})

	first := diagnosticViolation(t, err, "/end")

	first.Params["limits"].([]int)[0] = 99
	first.Params["added"] = true

	_, err = schema.Parse(context.Background(), booking{End: 2})

	got := diagnosticViolation(t, err, "/end").Params
	want := map[string]any{"limits": []int{3, 5}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reused At parameters = %#v, want %#v", got, want)
	}
}

// TestDiagnosticOwnershipLocalizeDetachesKnownParams verifies localization
// rerenders known messages while keeping the source and separately localized
// results independent, including nested typed containers.
func TestDiagnosticOwnershipLocalizeDetachesKnownParams(t *testing.T) {
	t.Parallel()

	source := validate.Errors{"": {{
		Code: "string.min",
		Params: map[string]any{
			"min":    3,
			"nested": []any{map[string]string{"role": "reader"}},
		},
		Message: "original",
	}}}

	localized := source.Localize(context.Background())
	peer := source.Localize(context.Background())

	localized[""][0].Params["min"] = 99
	localized[""][0].Params["nested"].([]any)[0].(map[string]string)["role"] = "writer"

	want := map[string]any{
		"min":    3,
		"nested": []any{map[string]string{"role": "reader"}},
	}

	if !reflect.DeepEqual(source[""][0].Params, want) {
		t.Fatalf("Localize exposed source parameters: %#v", source[""][0].Params)
	}

	if !reflect.DeepEqual(peer[""][0].Params, want) {
		t.Fatalf("Localize results share parameters: %#v", peer[""][0].Params)
	}

	if source[""][0].Message != "original" || localized[""][0].Message != "must be at least 3 characters" {
		t.Fatalf("source/localized messages = %q/%q", source[""][0].Message, localized[""][0].Message)
	}
}

// TestDiagnosticOwnershipLocalizeDetachesUnknownParams verifies the no-catalog
// branch copies parameters too, while preserving the caller's custom message.
func TestDiagnosticOwnershipLocalizeDetachesUnknownParams(t *testing.T) {
	t.Parallel()

	source := validate.Errors{"": {{
		Code:    "app.unknown",
		Params:  map[string]any{"limits": []int{3, 5}},
		Message: "custom text",
	}}}

	localized := source.Localize(context.Background())

	localized[""][0].Params["limits"].([]int)[0] = 99
	localized[""][0].Params["added"] = true

	if !reflect.DeepEqual(source[""][0].Params, map[string]any{"limits": []int{3, 5}}) {
		t.Fatalf("unknown-code source parameters changed: %#v", source[""][0].Params)
	}

	if localized[""][0].Message != "custom text" {
		t.Fatalf("unknown-code message = %q", localized[""][0].Message)
	}
}

// TestDiagnosticOwnershipPreservesNamedContainers checks recursive ownership
// through named map keys, slices and fixed arrays. Exact large integer values
// also prevent a lossy JSON encode/decode implementation from passing.
func TestDiagnosticOwnershipPreservesNamedContainers(t *testing.T) {
	t.Parallel()

	// label is a named string key, which still represents a JSON object key.
	type label string
	// numbers is a typed sequence whose identity must survive capture.
	type numbers []int64
	// groups nests named sequences inside a named string-keyed map.
	type groups map[label][1]numbers

	// exact exceeds the integer precision of a float64 JSON round trip.
	const exact = int64(1<<53 + 1)

	input := groups{"primary": {numbers{exact}}}
	failure := validate.Fail("app.groups", map[string]any{"groups": input})
	input["primary"][0][0] = 99

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return failure
		})
	})

	_, err := schema.Parse(context.Background(), "value")

	got := diagnosticViolation(t, err, "").Params["groups"]
	want := groups{"primary": {numbers{exact}}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("named containers = %#v (%T), want %#v", got, got, want)
	}
}

// TestDiagnosticOwnershipPreservesNilContainers checks that capture, Parse and
// Localize preserve typed nil values and allocated empty containers rather than
// normalize them through JSON. A nil top-level Params remains nil as well.
func TestDiagnosticOwnershipPreservesNilContainers(t *testing.T) {
	t.Parallel()

	params := map[string]any{
		"nil":         nil,
		"nil_slice":   []any(nil),
		"nil_map":     map[string]string(nil),
		"empty_slice": []int{},
		"empty_map":   map[string]string{},
	}
	failure := validate.Fail("app.nil", params)

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return failure
		})
	})

	_, err := schema.Parse(context.Background(), "value")

	violation := diagnosticViolation(t, err, "")

	source := validate.Errors{"": {violation}, "/nil": {{Code: "required"}}}

	localized := source.Localize(context.Background())

	if !reflect.DeepEqual(localized[""][0].Params, params) {
		t.Fatalf("nil/empty distinction changed: %#v", localized[""][0].Params)
	}

	if localized["/nil"][0].Params != nil {
		t.Fatalf("nil Params became %#v", localized["/nil"][0].Params)
	}
}

// TestDiagnosticOwnershipRetainsOpaqueValues defines the shallow boundary:
// structs, pointers and non-string-keyed maps are application-owned objects.
// Capturing or localizing them must not traverse their fields or replace native
// identities, even when some of those values can be serialized to JSON.
func TestDiagnosticOwnershipRetainsOpaqueValues(t *testing.T) {
	t.Parallel()

	// record is an arbitrary application object, not a parameter container.
	type record struct {
		// Values deliberately retains application-owned storage inside a struct.
		Values []int
	}

	object := record{Values: []int{3}}
	native := time.Date(2026, time.October, 30, 0, 0, 0, 0, time.UTC)
	indexed := map[int]string{1: "before"}
	failure := validate.Fail("app.opaque", map[string]any{
		"struct":  object,
		"pointer": &object,
		"native":  native,
		"indexed": indexed,
	})
	object.Values[0] = 99
	indexed[1] = "after"

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return failure
		})
	})

	_, err := schema.Parse(context.Background(), "value")

	violation := diagnosticViolation(t, err, "")

	localized := (validate.Errors{"": {violation}}).Localize(context.Background())

	want := map[string]any{
		"struct":  object,
		"pointer": &object,
		"native":  native,
		"indexed": indexed,
	}

	if !reflect.DeepEqual(localized[""][0].Params, want) {
		t.Fatalf("opaque values were traversed or converted: %#v", localized[""][0].Params)
	}

	if localized[""][0].Params["pointer"] != &object {
		t.Fatal("native/application pointer identity changed")
	}
}

// TestDiagnosticOwnershipErrorsAddAndAllRemainBorrowed distinguishes engine
// publication from the mutable Errors record API. Add retains the supplied
// parameters, and All exposes them rather than creating independent snapshots.
func TestDiagnosticOwnershipErrorsAddAndAllRemainBorrowed(t *testing.T) {
	t.Parallel()

	params := map[string]any{"min": 3}
	violations := validate.Errors{}
	violations.Add("", validate.Violation{Code: "string.min", Params: params})

	for _, failures := range violations.All() {
		failures[0].Params["min"] = 5
	}

	if params["min"] != 5 {
		t.Fatalf("Add/All stopped exposing borrowed parameters: %#v", params)
	}
}

// TestErrorsAllOrder verifies All yields pointers in a stable order with
// numeric segments compared as numbers.
func TestErrorsAllOrder(t *testing.T) {
	t.Parallel()

	errs := validate.Errors{}

	for _, pointer := range []string{"/items/10", "/b", "/items/2", "", "/items/2/qty", "/a"} {
		errs.Add(pointer, validate.Violation{
			Code: "x",
		})
	}

	var got []string

	for pointer := range errs.All() {
		got = append(got, pointer)
	}

	want := []string{"", "/a", "/b", "/items/2", "/items/2/qty", "/items/10"}

	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestErrorsAllStopsEarly verifies the iterator honors break.
func TestErrorsAllStopsEarly(t *testing.T) {
	t.Parallel()

	errs := validate.Errors{"/a": {{
		Code: "x",
	}}, "/b": {{
		Code: "x",
	}}}

	count := 0

	for range errs.All() {
		count++

		break
	}

	if count != 1 {
		t.Fatalf("count = %d", count)
	}
}

// TestErrorsAdd verifies Add appends per pointer.
func TestErrorsAdd(t *testing.T) {
	t.Parallel()

	errs := validate.Errors{}
	errs.Add("/a", validate.Violation{
		Code: "one",
	})
	errs.Add("/a", validate.Violation{
		Code: "two",
	})

	if len(errs["/a"]) != 2 || errs["/a"][1].Code != "two" {
		t.Fatalf("errs = %#v", errs)
	}
}

// TestErrorsErrorString verifies the stable English summary.
func TestErrorsErrorString(t *testing.T) {
	t.Parallel()

	errs := validate.Errors{}
	errs.Add("/b", validate.Violation{
		Code: "required",
	})
	errs.Add("", validate.Violation{
		Code: "custom",
	})
	errs.Add("/a", validate.Violation{
		Code:   "string.min",
		Params: map[string]any{"min": 3},
	})

	want := "validation failed: (root): custom; /a: must be at least 3 characters; /b: is required"

	if got := errs.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestErrorsAsType verifies Errors is found through wrapping.
func TestErrorsAsType(t *testing.T) {
	t.Parallel()

	err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}).Validate(context.Background(), "a")

	wrapped := fmt.Errorf("handling: %w", err)

	errs, ok := errors.AsType[validate.Errors](wrapped)

	if !ok || len(errs[""]) != 1 {
		t.Fatalf("AsType failed: %v", wrapped)
	}
}

// TestValidateReturnsLiteralNil verifies success is an untyped nil error.
func TestValidateReturnsLiteralNil(t *testing.T) {
	t.Parallel()

	err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(1)
	}).Validate(context.Background(), "ok")

	if err != nil {
		t.Fatalf("err = %#v, want nil", err)
	}

	_, err = validate.Struct[address]().Parse(context.Background(), address{})

	if err != nil {
		t.Fatalf("err = %#v, want nil", err)
	}
}

// TestParseReturnsZeroOnFailure verifies Parse yields the zero value when
// validation fails.
func TestParseReturnsZeroOnFailure(t *testing.T) {
	t.Parallel()

	got, err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(5)
		value.Default("valid")
	}).Parse(context.Background(), "ab")

	if err == nil || got != "" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

// TestErrorsJSON verifies the wire format of violations.
func TestErrorsJSON(t *testing.T) {
	t.Parallel()

	err := validate.Struct[account](func(object *validate.StructBuilder[account]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Email", func(value *validate.StringBuilder[string]) {
			value.Min(12)
		})
	}).Validate(context.Background(), account{
		Email: "short",
	})

	encoded, marshalErr := json.Marshal(err)

	if marshalErr != nil {
		t.Fatal(marshalErr)
	}

	want := `{"/Email":[{"code":"string.min","params":{"min":12},"message":"must be at least 12 characters"}],` +
		`"/name":[{"code":"required","message":"is required"}]}`

	var gotValue, wantValue map[string]any

	if json.Unmarshal(encoded, &gotValue) != nil || json.Unmarshal([]byte(want), &wantValue) != nil {
		t.Fatal("cannot decode JSON")
	}

	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
}

// TestFailWithoutParamsOmitsThem verifies params are omitted when empty.
func TestFailWithoutParamsOmitsThem(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(validate.Violation{
		Code:    "x",
		Message: "m",
	})

	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(encoded), "params") {
		t.Fatalf("json = %s", encoded)
	}
}

// ExampleErrors shows the JSON form that API clients receive.
func ExampleErrors() {
	err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}).Validate(context.Background(), "ab")

	encoded, err := json.Marshal(err)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(string(encoded))

	// Output: {"":[{"code":"string.min","params":{"min":3},"message":"must be at least 3 characters"}]}
}

// TestCompareSegmentsTotalOrder verifies the pointer order is total for
// mixed numeric and non-numeric keys.
func TestCompareSegmentsTotalOrder(t *testing.T) {
	t.Parallel()

	keys := []string{"10", "1a", "9", "01", "", "a", "99999999999999999999999", "18446744073709551616"}
	errs := validate.Errors{}

	for _, key := range keys {
		errs.Add("/"+key, validate.Violation{
			Code: "x",
		})
	}

	var order []string

	for pointer := range errs.All() {
		order = append(order, strings.TrimPrefix(pointer, "/"))
	}

	want := []string{"01", "9", "10", "18446744073709551616", "99999999999999999999999", "", "1a", "a"}

	if !slices.Equal(order, want) {
		t.Fatalf("order = %q, want %q", order, want)
	}

	shuffled := slices.Clone(keys)
	slices.Reverse(shuffled)

	other := validate.Errors{}

	for _, key := range shuffled {
		other.Add("/"+key, validate.Violation{
			Code: "x",
		})
	}

	var again []string

	for pointer := range other.All() {
		again = append(again, strings.TrimPrefix(pointer, "/"))
	}

	if !slices.Equal(order, again) {
		t.Fatalf("order depends on input: %q vs %q", order, again)
	}
}

// codesOf flattens a validation error to pointer -> violation codes. It
// fails the test when err is neither nil nor validate.Errors.
func codesOf(t *testing.T, err error) map[string][]string {
	t.Helper()

	if err == nil {
		return nil
	}

	errs, ok := errors.AsType[validate.Errors](err)

	if !ok {
		t.Fatalf("error %v (%T) is not validate.Errors", err, err)
	}

	codes := make(map[string][]string, len(errs))

	for pointer, violations := range errs {
		for _, violation := range violations {
			codes[pointer] = append(codes[pointer], violation.Code)
		}
	}

	return codes
}

// requireCodes fails unless err carries exactly the wanted codes.
func requireCodes(t *testing.T, err error, want map[string][]string) {
	t.Helper()

	got := codesOf(t, err)

	if len(want) == 0 && got == nil {
		return
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("violations = %v, want %v (error: %v)", got, want, err)
	}
}

// requireCode fails unless err is nil for an empty code, or carries exactly
// one violation with the given code at the root pointer.
func requireCode(t *testing.T, err error, code string) {
	t.Helper()

	if code == "" {
		requireCodes(t, err, nil)

		return
	}

	requireCodes(t, err, map[string][]string{"": {code}})
}

// TestLocalizeRerendersMessages verifies Errors.Localize.
func TestLocalizeRerendersMessages(t *testing.T) {
	t.Parallel()

	err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}).Validate(context.Background(), "a")

	errs, _ := errors.AsType[validate.Errors](err)

	ctx := validate.NewCatalogContext(context.Background(), validate.Catalog{
		Locales: map[string]validate.Messages{"es": spanish()},
	})
	ctx = validate.NewLocaleContext(ctx, "es")

	localized := errs.Localize(ctx)

	if got := localized[""][0].Message; got != "debe tener al menos 3 caracteres" {
		t.Fatalf("message = %q", got)
	}

	if errs[""][0].Message != "must be at least 3 characters" {
		t.Fatal("Localize modified the receiver")
	}
}

// TestLocalizeKeepsUnknownCodeMessage verifies custom messages survive.
func TestLocalizeKeepsUnknownCodeMessage(t *testing.T) {
	t.Parallel()

	errs := validate.Errors{"": {{
		Code:    "app.custom",
		Message: "custom text",
	}}}

	if got := errs.Localize(context.Background())[""][0].Message; got != "custom text" {
		t.Fatalf("message = %q", got)
	}
}
