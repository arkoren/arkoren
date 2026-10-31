package validate_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestDiagnosticOwnershipImportedEnumKeepsDefinition checks the direct JSON
// diagnostic path, which does not return Fail from a typed rule. Nested values
// exposed in one result must not rewrite the imported enum or later results.
func TestDiagnosticOwnershipImportedEnumKeepsDefinition(t *testing.T) {
	t.Parallel()

	doc := `{"enum":[{"roles":["reader"]}]}`

	schema := fromDoc(t, doc)

	input := map[string]any{"roles": []any{"blocked"}}

	_, err := schema.Parse(context.Background(), input)

	first := diagnosticViolation(t, err, "")

	first.Params["values"].([]any)[0].(map[string]any)["roles"].([]any)[0] = "writer"

	_, err = schema.Parse(context.Background(), input)

	second := diagnosticViolation(t, err, "")

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{"values": []any{map[string]any{"roles": []any{"reader"}}}}

	if !reflect.DeepEqual(second.Params, want) || string(encoded) != doc {
		t.Fatalf("enum definition changed: params=%#v, schema=%s", second.Params, encoded)
	}

	if _, err := schema.Parse(context.Background(), map[string]any{"roles": []any{"reader"}}); err != nil {
		t.Fatalf("enum lost an originally allowed value: %v", err)
	}
}

// TestDiagnosticOwnershipImportedConstKeepsDefinition checks that a newly
// allocated values list is not sufficient isolation: its nested constant must
// also be detached from the schema before a violation reaches the caller.
func TestDiagnosticOwnershipImportedConstKeepsDefinition(t *testing.T) {
	t.Parallel()

	doc := `{"const":{"roles":["reader"]}}`

	schema := fromDoc(t, doc)

	input := map[string]any{"roles": []any{"blocked"}}

	_, err := schema.Parse(context.Background(), input)

	first := diagnosticViolation(t, err, "")

	first.Params["values"].([]any)[0].(map[string]any)["roles"].([]any)[0] = "writer"

	_, err = schema.Parse(context.Background(), input)

	second := diagnosticViolation(t, err, "")

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{"values": []any{map[string]any{"roles": []any{"reader"}}}}

	if !reflect.DeepEqual(second.Params, want) || string(encoded) != doc {
		t.Fatalf("const definition changed: params=%#v, schema=%s", second.Params, encoded)
	}

	if _, err := schema.Parse(context.Background(), map[string]any{"roles": []any{"reader"}}); err != nil {
		t.Fatalf("const lost its originally allowed value: %v", err)
	}
}

// TestDiagnosticOwnershipImportCapturesDocumentBytes checks input ownership
// separately from diagnostic publication. FromJSONSchema must finish owning
// decoded enum and const values before the caller reuses the source byte slice.
func TestDiagnosticOwnershipImportCapturesDocumentBytes(t *testing.T) {
	t.Parallel()

	original := `{"properties":{"constant":{"const":"reader"},"enum":{"enum":["reader"]}}}`
	doc := []byte(original)

	schema, err := validate.FromJSONSchema(doc)

	if err != nil {
		t.Fatal(err)
	}

	copy(doc, bytes.ReplaceAll(doc, []byte("reader"), []byte("writer")))

	_, err = schema.Parse(context.Background(), map[string]any{"constant": "reader", "enum": "reader"})

	if err != nil {
		t.Fatalf("source byte mutation changed imported rules: %v", err)
	}

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != original {
		t.Fatalf("source byte mutation changed exported schema: %s", encoded)
	}
}

// fromDoc builds a validator from a JSON Schema document, failing the test
// on a build error.
func fromDoc(t *testing.T, doc string) validate.JSONSchema {
	t.Helper()

	schema, err := validate.FromJSONSchema([]byte(doc))

	if err != nil {
		t.Fatal(err)
	}

	return schema
}

// decoded parses JSON into the value types FromJSONSchema validates.
func decoded(t *testing.T, text string) any {
	t.Helper()

	var value any

	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}

	return value
}

// TestFromJSONSchemaRejectsUnsupportedKeyword checks that unsupported keywords
// produce an invalid-schema error instead of being ignored.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaRejectsUnsupportedKeyword$" .
func TestFromJSONSchemaRejectsUnsupportedKeyword(t *testing.T) {
	t.Parallel()

	_, err := validate.FromJSONSchema([]byte(`{"type":"object","properties":{"a":{"$ref":"#/$defs/x"}}}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("error = %v", err)
	}

	want := `unsupported keyword "$ref"`

	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), `"/properties/a/$ref"`) {
		t.Fatalf("error = %q, want it to name %s and its pointer", err, want)
	}
}

// TestFromJSONSchemaRejectsOtherKeywords checks rejection of unsupported
// keyword families throughout the supported document subset.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaRejectsOtherKeywords$" .
func TestFromJSONSchemaRejectsOtherKeywords(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{
		`{"$schema":"https://json-schema.org/draft/2020-12/schema"}`,
		`{"oneOf":[{"type":"string"}]}`,
		`{"type":["string","null"]}`,
		`{"additionalProperties":false}`,
		`{"items":{"allOf":[]}}`,
		`{"minLength":"3"}`,
		`{"minLength":-1}`,
		`{"minLength":1.5}`,
		`{"enum":[]}`,
		`{"required":[1]}`,
		`{"pattern":"("}`,
		`{"multipleOf":0}`,
		`{"properties":[]}`,
		`{"type":"string","default":5}`,
		`[]`,
		`not json`,
	} {
		if _, err := validate.FromJSONSchema([]byte(doc)); !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("%s: error = %v", doc, err)
		}
	}
}

// TestFromJSONSchemaTypeMismatch checks the type violation reported for input
// of the wrong JSON kind.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaTypeMismatch$" .
func TestFromJSONSchemaTypeMismatch(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"object","properties":{"name":{"type":"string"},"tags":{"type":"array","items":{"type":"boolean"}}}}`)

	err := schema.Validate(context.Background(), decoded(t, `{"name":5,"tags":[true,"x"]}`))

	requireCodes(t, err, map[string][]string{"/name": {"type"}, "/tags/1": {"type"}})
}

// TestFromJSONSchemaIntegerRejectsFractions checks that integer schemas reject
// fractional numeric values.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaIntegerRejectsFractions$" .
func TestFromJSONSchemaIntegerRejectsFractions(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"integer"}`)

	if err := schema.Validate(context.Background(), decoded(t, `3`)); err != nil {
		t.Fatal(err)
	}

	if err := schema.Validate(context.Background(), decoded(t, `3.0`)); err != nil {
		t.Fatal(err)
	}

	requireCode(t, schema.Validate(context.Background(), decoded(t, `3.5`)), "type")
	requireCode(t, schema.Validate(context.Background(), decoded(t, `"3"`)), "type")
}

// TestFromJSONSchemaAcceptsGoIntegers checks numeric validation for Go integer
// values as well as decoded JSON numbers.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaAcceptsGoIntegers$" .
func TestFromJSONSchemaAcceptsGoIntegers(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"integer","minimum":2}`)

	if err := schema.Validate(context.Background(), 3); err != nil {
		t.Fatal(err)
	}

	requireCode(t, schema.Validate(context.Background(), int64(1)), "number.min")
}

// TestFromJSONSchemaNullIsAbsent checks optional null values and the Required
// constraint in decoded JSON validation.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaNullIsAbsent$" .
func TestFromJSONSchemaNullIsAbsent(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"object","required":["a"],"properties":{"a":{"type":"string","minLength":3},"b":{"type":"integer"}}}`)

	if err := schema.Validate(context.Background(), decoded(t, `{"a":null,"b":null}`)); err != nil {
		t.Fatal(err)
	}

	requireCodes(t, schema.Validate(context.Background(), decoded(t, `{"b":1}`)), map[string][]string{"/a": {"required"}})
}

// TestFromJSONSchemaZeroValuesAreChecked checks that JSON zero values remain
// present for numeric, string and boolean constraints.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaZeroValuesAreChecked$" .
func TestFromJSONSchemaZeroValuesAreChecked(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"object","required":["name"],"properties":{"name":{"type":"string","minLength":1},"n":{"type":"integer","minimum":1}}}`)

	requireCodes(t, schema.Validate(context.Background(), decoded(t, `{"name":"","n":0}`)),
		map[string][]string{"/name": {"string.min"}, "/n": {"number.min"}})
}

// TestFromJSONSchemaRulesAndCodes checks keyword validation and stable
// violation codes against the supported rule subset.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaRulesAndCodes$" .
func TestFromJSONSchemaRulesAndCodes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	requireCode(t, fromDoc(t, `{"type":"string","maxLength":2}`).Validate(ctx, "abc"), "string.max")
	requireCode(t, fromDoc(t, `{"type":"string","pattern":"^a+$"}`).Validate(ctx, "ab"), "string.regex")
	requireCode(t, fromDoc(t, `{"enum":["a","b"]}`).Validate(ctx, "c"), "one_of")
	requireCode(t, fromDoc(t, `{"const":true}`).Validate(ctx, false), "one_of")
	requireCode(t, fromDoc(t, `{"enum":[1,2]}`).Validate(ctx, 3.0), "one_of")
	requireCode(t, fromDoc(t, `{"maximum":5}`).Validate(ctx, 6.0), "number.max")
	requireCode(t, fromDoc(t, `{"exclusiveMinimum":5}`).Validate(ctx, 5.0), "number.gt")
	requireCode(t, fromDoc(t, `{"exclusiveMaximum":5}`).Validate(ctx, 5.0), "number.lt")
	requireCode(t, fromDoc(t, `{"multipleOf":5}`).Validate(ctx, 12.0), "number.multiple_of")
	requireCode(t, fromDoc(t, `{"minItems":2}`).Validate(ctx, []any{1.0}), "slice.min")
	requireCode(t, fromDoc(t, `{"maxItems":1}`).Validate(ctx, []any{1.0, 2.0}), "slice.max")
	requireCode(t, fromDoc(t, `{"uniqueItems":true}`).Validate(ctx, []any{"a", "a"}), "slice.unique")
	requireCode(t, fromDoc(t, `{"minProperties":1}`).Validate(ctx, map[string]any{}), "map.min")
	requireCode(t, fromDoc(t, `{"maxProperties":0}`).Validate(ctx, map[string]any{"a": 1.0}), "map.max")
}

// TestJSONSchemaOversizedUniqueArrayStopsBeforeUniqueness exercises both
// expensive paths after maxItems: duplicate detection and item validation.
// Only the size error belongs to an array that is already too large.
func TestJSONSchemaOversizedUniqueArrayStopsBeforeUniqueness(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"maxItems":1,"uniqueItems":true,"items":{"type":"string"}}`)

	items := make([]any, 128)

	for index := range items {
		items[index] = float64(index)
	}

	items[len(items)-1] = items[0]

	parsed, err := schema.Parse(context.Background(), items)

	if parsed != nil {
		t.Fatalf("oversized result = %v, want nil", parsed)
	}

	requireCodes(t, err, map[string][]string{"": {"slice.max"}})
}

// TestJSONSchemaUniqueItemsHonorsCancellation requires cancellation to take
// precedence over validation failures, including an already oversized array.
// No timing threshold or large vulnerable workload is needed to reproduce it.
func TestJSONSchemaUniqueItemsHonorsCancellation(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"maxItems":1,"uniqueItems":true}`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	parsed, err := schema.Parse(ctx, []any{1.0, 2.0})

	if !errors.Is(err, context.Canceled) || parsed != nil {
		t.Fatalf("canceled Parse = %v, %v; want nil, context.Canceled", parsed, err)
	}
}

// TestJSONSchemaUniqueItemsRejectsStructuralDuplicates covers the decoded
// value domain, including object order and nested arrays rather than identity.
// Each pair must produce the same array-level uniqueness violation.
func TestJSONSchemaUniqueItemsRejectsStructuralDuplicates(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"uniqueItems":true}`)

	for _, input := range []string{
		`[null,null]`, `[true,true]`, `[false,false]`, `["a","a"]`, `[1,1.0]`,
		`[[1,{"a":[true,null]}],[1,{"a":[true,null]}]]`,
		`[{"a":1,"b":[2]},{"b":[2],"a":1}]`,
		`[[],[]]`, `[{},{}]`,
	} {
		requireCode(t, schema.Validate(context.Background(), decoded(t, input)), "slice.unique")
	}
}

// TestJSONSchemaUniqueItemsPreservesNumericEquality checks mathematical JSON
// equality across supported Go numeric types, including signed zero. Adjacent
// large integers are covered separately and must not collapse through float64.
func TestJSONSchemaUniqueItemsPreservesNumericEquality(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"uniqueItems":true}`)

	for _, input := range [][]any{
		{int(1), float64(1)}, {int64(1), int(1)}, {int64(1), float64(1)},
		{math.Copysign(0, -1), float64(0)},
		{[]any{math.Copysign(0, -1)}, []any{float64(0)}},
	} {
		requireCode(t, schema.Validate(context.Background(), input), "slice.unique")
	}
}

// TestJSONSchemaUniqueItemsPreservesNilContainerDistinctions keeps application
// container-presence choices independent from corrected numeric equality.
// Typed nil containers remain distinct from empty containers and nil itself.
func TestJSONSchemaUniqueItemsPreservesNilContainerDistinctions(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"uniqueItems":true}`)

	input := []any{
		[]any(nil), []any{}, map[string]any(nil), map[string]any{}, nil,
	}

	if err := schema.Validate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

// TestJSONSchemaUniqueItemsSeparatesStructuralKeys tests distinct values that
// would collide in concatenated, untyped, or delimiter-only encodings. Full
// length-prefixed keys must distinguish both nested values and object names.
func TestJSONSchemaUniqueItemsSeparatesStructuralKeys(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"uniqueItems":true,"maxItems":100}`)

	input := decoded(t, `[null,false,true,0,1,"0","n","",[],{},["a","bc"],["ab","c"],
		{"a":"bc"},{"ab":"c"},{"a\u0000":"b"},{"a":"\u0000b"},[1,2],[2,1],{"a":null},{"b":null}]`)

	if err := schema.Validate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

// TestJSONSchemaUniqueItemsRetainsChildFailuresBelowLimit checks that only
// oversized arrays lose error collection. Admitted arrays still report both
// duplicates and failures in their element schemas.
func TestJSONSchemaUniqueItemsRetainsChildFailuresBelowLimit(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"maxItems":2,"uniqueItems":true,"items":{"type":"string"}}`)

	requireCodes(t, schema.Validate(context.Background(), []any{1.0, 1.0}),
		map[string][]string{"": {"slice.unique"}, "/0": {"type"}, "/1": {"type"}})
}

// TestJSONSchemaOversizedArrayStillChecksSiblings makes the short-circuit's
// scope explicit: an invalid array does not hide errors in sibling properties.
func TestJSONSchemaOversizedArrayStillChecksSiblings(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"properties":{"a":{"maxItems":0,"const":[]},"b":{"type":"string"}}}`)

	requireCodes(t, schema.Validate(context.Background(), decoded(t, `{"a":[1],"b":2}`)),
		map[string][]string{"/a": {"slice.max"}, "/b": {"type"}})
}

// TestJSONSchemaUniqueItemsDefaultsAndInputRemainIndependent verifies that
// uniqueness still sees original items, not defaulted copies, and that nested
// defaults and parsed containers never become aliases of caller input.
func TestJSONSchemaUniqueItemsDefaultsAndInputRemainIndependent(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"uniqueItems":true,"items":{"properties":{"tag":{"default":["x"]}}}}`)

	input := decoded(t, `[{},{"tag":["x"]}]`)

	parsed, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(parsed, decoded(t, `[{"tag":["x"]},{"tag":["x"]}]`)) {
		t.Fatalf("parsed = %v", parsed)
	}

	parsed.([]any)[1].(map[string]any)["tag"].([]any)[0] = "changed"
	parsed.([]any)[0].(map[string]any)["tag"].([]any)[0] = "changed"

	if !reflect.DeepEqual(input, decoded(t, `[{},{"tag":["x"]}]`)) {
		t.Fatalf("input was modified: %v", input)
	}

	second, err := schema.Parse(context.Background(), input)

	if err != nil || !reflect.DeepEqual(second, decoded(t, `[{"tag":["x"]},{"tag":["x"]}]`)) {
		t.Fatalf("second Parse = %v, %v", second, err)
	}
}

// TestJSONSchemaUniqueItemsRejectsNonJSONValues documents the intentional
// restriction of structural keys to finite JSON-like values. Nonfinite and
// unsupported Go values get an item-level type error rather than a slow fallback.
func TestJSONSchemaUniqueItemsRejectsNonJSONValues(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"uniqueItems":true}`)

	for _, value := range []any{math.NaN(), math.Inf(1), uint(1), []int{1}, map[string]any{"n": math.NaN()}} {
		requireCodes(t, schema.Validate(context.Background(), []any{value}),
			map[string][]string{"/0": {"type"}})
	}
}

// BenchmarkJSONSchemaUniqueItemsDistinct measures increasing admitted array
// sizes without maxItems. Keep inputs bounded and distinct so this measures
// the previous worst case, not early exit upon encountering a duplicate.
func BenchmarkJSONSchemaUniqueItemsDistinct(b *testing.B) {
	schema, err := validate.FromJSONSchema([]byte(`{"uniqueItems":true}`))

	if err != nil {
		b.Fatal(err)
	}

	for _, size := range []int{256, 512, 1024, 2048, 4096} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			items := make([]any, size)

			for index := range items {
				items[index] = float64(index)
			}

			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				if err := schema.Validate(context.Background(), items); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkJSONSchemaUniqueItemsNested measures structural encoding and object
// sorting with bounded, distinct nested items. These measurements complement
// the scalar benchmark; they are not timing assertions in the regression suite.
func BenchmarkJSONSchemaUniqueItemsNested(b *testing.B) {
	schema, err := validate.FromJSONSchema([]byte(`{"uniqueItems":true}`))

	if err != nil {
		b.Fatal(err)
	}

	for _, size := range []int{256, 512, 1024, 2048, 4096} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			items := make([]any, size)

			for index := range items {
				items[index] = map[string]any{"id": float64(index), "values": []any{true, "x", nil}}
			}

			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				if err := schema.Validate(context.Background(), items); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkJSONSchemaOversizedUniqueItems measures the maxItems rejection
// path separately: input size must not change the amount of content validation.
func BenchmarkJSONSchemaOversizedUniqueItems(b *testing.B) {
	schema, err := validate.FromJSONSchema([]byte(`{"maxItems":100,"uniqueItems":true}`))

	if err != nil {
		b.Fatal(err)
	}

	for _, size := range []int{256, 512, 1024, 2048, 4096} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			items := make([]any, size)

			for index := range items {
				items[index] = float64(index)
			}

			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				if err := schema.Validate(context.Background(), items); err == nil {
					b.Fatal("oversized array passed")
				}
			}
		})
	}
}

// TestFromJSONSchemaFormats checks each supported string format against
// accepted and rejected inputs.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaFormats$" .
func TestFromJSONSchemaFormats(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	requireCode(t, fromDoc(t, `{"type":"string","format":"email"}`).Validate(ctx, "nope"), "string.email")
	requireCode(t, fromDoc(t, `{"type":"string","format":"uri"}`).Validate(ctx, "/relative"), "string.url")
	requireCode(t, fromDoc(t, `{"type":"string","format":"uuid"}`).Validate(ctx, "1234"), "string.uuid")
	requireCode(t, fromDoc(t, `{"type":"string","format":"duration"}`).Validate(ctx, "soon"), "string.duration")
	requireCode(t, fromDoc(t, `{"type":"string","format":"date-time"}`).Validate(ctx, "yesterday"), "type")

	// Valid values, and formats that are only annotations, pass.
	for doc, value := range map[string]string{
		`{"type":"string","format":"email"}`:       "ada@example.com",
		`{"type":"string","format":"uri"}`:         "https://example.com/a",
		`{"type":"string","format":"uuid"}`:        "123e4567-e89b-12d3-a456-426614174000",
		`{"type":"string","format":"duration"}`:    "PT1H30M",
		`{"type":"string","format":"go-duration"}`: "1h30m",
		`{"type":"string","format":"date-time"}`:   "2026-10-16T10:00:00Z",
		`{"type":"string","format":"binary"}`:      "not checked",
		`{"type":"string","format":"ulid"}`:        "not checked",
	} {
		if err := fromDoc(t, doc).Validate(ctx, value); err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
	}
}

// TestFromJSONSchemaMessageIsTranslated checks that decoded-schema violations
// use the context catalog and locale.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaMessageIsTranslated$" .
func TestFromJSONSchemaMessageIsTranslated(t *testing.T) {
	t.Parallel()

	catalog := validate.Catalog{
		Locales: map[string]validate.Messages{"es": {"string.min": "mínimo {min}"}},
	}
	ctx := validate.NewLocaleContext(validate.NewCatalogContext(context.Background(), catalog), "es")

	err := fromDoc(t, `{"type":"string","minLength":3}`).Validate(ctx, "ab")

	errs, _ := errors.AsType[validate.Errors](err)

	if got := errs[""][0].Message; got != "mínimo 3" {
		t.Fatalf("message = %q", got)
	}
}

// TestFromJSONSchemaParseAppliesDefaults checks nested default application
// while preserving already supplied values.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaParseAppliesDefaults$" .
func TestFromJSONSchemaParseAppliesDefaults(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"object","properties":{`+
		`"plan":{"type":"string","default":"free"},`+
		`"limits":{"type":"object","properties":{"seats":{"type":"integer","default":5}}},`+
		`"tags":{"type":"array","default":["a"],"items":{"type":"string"}}}}`)

	input := decoded(t, `{"limits":{},"plan":null}`)

	parsed, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	want := decoded(t, `{"plan":"free","limits":{"seats":5},"tags":["a"]}`)

	if !jsonEqualForTest(parsed, want) {
		t.Fatalf("parsed = %v, want %v", parsed, want)
	}

	if !jsonEqualForTest(input, decoded(t, `{"limits":{},"plan":null}`)) {
		t.Fatalf("input was modified: %v", input)
	}
}

// TestFromJSONSchemaParseCopiesDefaults checks that parsed default containers
// do not share mutable storage between results.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaParseCopiesDefaults$" .
func TestFromJSONSchemaParseCopiesDefaults(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"object","properties":{"tags":{"type":"array","default":["a"]}}}`)

	first, err := schema.Parse(context.Background(), map[string]any{})

	if err != nil {
		t.Fatal(err)
	}

	first.(map[string]any)["tags"].([]any)[0] = "changed"

	second, _ := schema.Parse(context.Background(), map[string]any{})

	if got := second.(map[string]any)["tags"].([]any)[0]; got != "a" {
		t.Fatalf("default was shared: %v", got)
	}
}

// TestFromJSONSchemaParseFailureReturnsNil checks that a failed decoded-JSON
// parse does not publish a partial result.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaParseFailureReturnsNil$" .
func TestFromJSONSchemaParseFailureReturnsNil(t *testing.T) {
	t.Parallel()

	parsed, err := fromDoc(t, `{"type":"string"}`).Parse(context.Background(), 5.0)

	if err == nil || parsed != nil {
		t.Fatalf("parsed = %v, %v", parsed, err)
	}
}

// TestFromJSONSchemaAdditionalPropertiesValidatesValues checks child
// constraints on object properties outside the declared property set.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaAdditionalPropertiesValidatesValues$" .
func TestFromJSONSchemaAdditionalPropertiesValidatesValues(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":{"type":"integer"}}`)

	requireCodes(t, schema.Validate(context.Background(), decoded(t, `{"id":"x","a":1,"b":"two"}`)), map[string][]string{"/b": {"type"}})
}

// TestFromJSONSchemaPointersAreEscaped checks JSON Pointer escaping for object
// names containing slash or tilde.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaPointersAreEscaped$" .
func TestFromJSONSchemaPointersAreEscaped(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"object","additionalProperties":{"type":"integer"}}`)

	requireCodes(t, schema.Validate(context.Background(), decoded(t, `{"a/b":"x"}`)), map[string][]string{"/a~1b": {"type"}})
}

// TestFromJSONSchemaIgnoresKeywordsForOtherKinds checks that constraints only
// apply to their corresponding JSON kinds.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaIgnoresKeywordsForOtherKinds$" .
func TestFromJSONSchemaIgnoresKeywordsForOtherKinds(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"minLength":3,"minimum":10}`)

	for _, value := range []any{"abc", 11.0, true, []any{}} {
		if err := schema.Validate(context.Background(), value); err != nil {
			t.Fatalf("%v: %v", value, err)
		}
	}

	requireCode(t, schema.Validate(context.Background(), "ab"), "string.min")
}

// TestFromJSONSchemaRenderIsCanonical checks deterministic rendering of a
// parsed schema document.
//
// Example:
//
//	go test -run "^TestFromJSONSchemaRenderIsCanonical$" .
func TestFromJSONSchemaRenderIsCanonical(t *testing.T) {
	t.Parallel()

	rendered, err := fromDoc(t, `{ "type": "object", "properties": {"b": {"type":"integer"}, "a": {"type":"string","minLength": 2}}, "required": ["a"] }`).JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	want := `{"properties":{"a":{"minLength":2,"type":"string"},"b":{"type":"integer"}},"required":["a"],"type":"object"}`

	if string(rendered) != want {
		t.Fatalf("rendered = %s", rendered)
	}
}

// jsonEqualForTest compares decoded values by their canonical JSON.
func jsonEqualForTest(left, right any) bool {
	leftJSON, _ := json.Marshal(left, json.Deterministic(true))
	rightJSON, _ := json.Marshal(right, json.Deterministic(true))

	return string(leftJSON) == string(rightJSON)
}

// TestImportedMissingDefaultPreparesNestedDefaults checks that selecting a
// missing property's default has the same recursive behavior as supplying that
// default explicitly. The caller-owned input stays empty and unchanged.
func TestImportedMissingDefaultPreparesNestedDefaults(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"properties":{"profile":{"default":{},"properties":{"role":{"default":"reader"}}}}}`)

	input := map[string]any{}

	value, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{"profile": map[string]any{"role": "reader"}}

	if !reflect.DeepEqual(value, want) || len(input) != 0 {
		t.Fatalf("Parse = %#v, input = %#v, want %#v and empty input", value, input, want)
	}
}

// TestImportedRequiredEmptyNameUsesChildPointer distinguishes the root pointer
// from a property's legal empty JSON name. Required diagnostics for that child
// belong at "/", not the validated object's root pointer "".
func TestImportedRequiredEmptyNameUsesChildPointer(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"required":[""]}`)

	_, err := schema.Parse(context.Background(), map[string]any{})

	requireCodes(t, err, map[string][]string{"/": {"required"}})
}

// TestImportedRequiredRejectsDuplicateNames checks the supported keyword's
// JSON Schema contract: names must be unique, not just strings. Invalid schema
// input must be rejected before it can produce duplicate runtime diagnostics.
func TestImportedRequiredRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	_, err := validate.FromJSONSchema([]byte(`{"required":["name","name"]}`))

	if err == nil {
		t.Fatal("FromJSONSchema accepted duplicate required names")
	}
}

// TestFromJSONSchemaPatternRejectsScalarTypes checks that numbers and booleans
// cannot become an empty regular expression through a failed type assertion.
// Every rejection retains the schema sentinel and the keyword's root pointer.
//
// Example:
//
//	go test -run '^TestFromJSONSchemaPatternRejectsScalarTypes$' .
func TestFromJSONSchemaPatternRejectsScalarTypes(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{
		`{"pattern":true}`, `{"pattern":false}`, `{"pattern":0}`, `{"pattern":1.5}`,
	} {
		_, err := validate.FromJSONSchema([]byte(doc))

		if !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("%s: error = %v, want ErrInvalidSchema", doc, err)
		}

		if !strings.Contains(err.Error(), `"/pattern": pattern must be a string`) {
			t.Fatalf("%s: error = %v, want pattern type error at /pattern", doc, err)
		}
	}
}

// TestFromJSONSchemaPatternRejectsContainerTypes checks both empty and populated
// JSON containers. Neither their contents nor their emptiness makes them valid
// pattern values; construction must identify the malformed keyword.
//
// Example:
//
//	go test -run '^TestFromJSONSchemaPatternRejectsContainerTypes$' .
func TestFromJSONSchemaPatternRejectsContainerTypes(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{
		`{"pattern":[]}`, `{"pattern":["^a+$"]}`, `{"pattern":{}}`, `{"pattern":{"value":"^a+$"}}`,
	} {
		_, err := validate.FromJSONSchema([]byte(doc))

		if !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("%s: error = %v, want ErrInvalidSchema", doc, err)
		}

		if !strings.Contains(err.Error(), `"/pattern": pattern must be a string`) {
			t.Fatalf("%s: error = %v, want pattern type error at /pattern", doc, err)
		}
	}
}

// TestFromJSONSchemaPatternRejectsNull distinguishes a null keyword value from
// an omitted pattern. Null input values can be absent during validation, but a
// schema's pattern keyword must still contain a string.
//
// Example:
//
//	go test -run '^TestFromJSONSchemaPatternRejectsNull$' .
func TestFromJSONSchemaPatternRejectsNull(t *testing.T) {
	t.Parallel()

	_, err := validate.FromJSONSchema([]byte(`{"pattern":null}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("error = %v, want ErrInvalidSchema", err)
	}

	if !strings.Contains(err.Error(), `"/pattern": pattern must be a string`) {
		t.Fatalf("error = %v, want pattern type error at /pattern", err)
	}
}

// TestFromJSONSchemaPatternTypeErrorEscapesPointer checks that malformed nested
// patterns retain their complete schema location. Property names containing
// slash and tilde must use RFC 6901 escaping in the construction error.
//
// Example:
//
//	go test -run '^TestFromJSONSchemaPatternTypeErrorEscapesPointer$' .
func TestFromJSONSchemaPatternTypeErrorEscapesPointer(t *testing.T) {
	t.Parallel()

	_, err := validate.FromJSONSchema([]byte(`{"properties":{"a/b~c":{"items":{"pattern":false}}}}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("error = %v, want ErrInvalidSchema", err)
	}

	if !strings.Contains(err.Error(), `"/properties/a~1b~0c/items/pattern": pattern must be a string`) {
		t.Fatalf("error = %v, want escaped nested pattern pointer", err)
	}
}

// TestFromJSONSchemaPatternAcceptsEmptyString preserves valid empty expressions.
// The string type check must not reject an empty pattern, which matches both
// empty and nonempty string input under Go's regular-expression semantics.
//
// Example:
//
//	go test -run '^TestFromJSONSchemaPatternAcceptsEmptyString$' .
func TestFromJSONSchemaPatternAcceptsEmptyString(t *testing.T) {
	t.Parallel()

	schema, err := validate.FromJSONSchema([]byte(`{"type":"string","pattern":""}`))

	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{"", "anything"} {
		if err := schema.Validate(context.Background(), value); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", value, err)
		}
	}
}

// TestFromJSONSchemaPatternChecksString preserves compilation and execution of
// nonempty string patterns. Matching input passes while a nonmatch retains the
// public string.regex violation at the input's root pointer.
//
// Example:
//
//	go test -run '^TestFromJSONSchemaPatternChecksString$' .
func TestFromJSONSchemaPatternChecksString(t *testing.T) {
	t.Parallel()

	schema, err := validate.FromJSONSchema([]byte(`{"type":"string","pattern":"^a+$"}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := schema.Validate(context.Background(), "aaa"); err != nil {
		t.Fatal(err)
	}

	requireCodes(t, schema.Validate(context.Background(), "baaa"), map[string][]string{"": {"string.regex"}})
}

// TestFromJSONSchemaPatternCompileErrorPreservesPointer keeps invalid string
// expressions distinct from wrong keyword types. Compilation failures still
// wrap ErrInvalidSchema and identify the escaped nested pattern location.
//
// Example:
//
//	go test -run '^TestFromJSONSchemaPatternCompileErrorPreservesPointer$' .
func TestFromJSONSchemaPatternCompileErrorPreservesPointer(t *testing.T) {
	t.Parallel()

	_, err := validate.FromJSONSchema([]byte(`{"properties":{"a/b~c":{"pattern":"("}}}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("error = %v, want ErrInvalidSchema", err)
	}

	if !strings.Contains(err.Error(), `"/properties/a~1b~0c/pattern": pattern does not compile:`) {
		t.Fatalf("error = %v, want compilation error at escaped nested pattern pointer", err)
	}
}

// TestImportedNumericMinimumPreservesAdjacentIntegers checks both sides of
// the first integer binary64 cannot represent. A schema must not round its
// minimum down and admit a value that is mathematically below that minimum.
func TestImportedNumericMinimumPreservesAdjacentIntegers(t *testing.T) {
	t.Parallel()

	doc := `{"minimum":9007199254740993}`

	schema := fromDoc(t, doc)

	_, err := schema.Parse(context.Background(), int64(9007199254740992))

	requireCode(t, err, "number.min")

	if got := diagnosticViolation(t, err, "").Message; got != "must be at least 9007199254740993" {
		t.Fatalf("exact numeric diagnostic = %q", got)
	}

	if err := schema.Validate(context.Background(), int64(9007199254740993)); err != nil {
		t.Fatal(err)
	}

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != doc {
		t.Fatalf("minimum lost precision on export: %s", encoded)
	}
}

// TestImportedEnumPreservesAdjacentIntegers verifies exact equality against
// schema literals, including numbers nested inside object and array values.
func TestImportedEnumPreservesAdjacentIntegers(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"enum":[{"id":[9007199254740993]}]}`)

	_, err := schema.Parse(context.Background(), map[string]any{"id": []any{int64(9007199254740992)}})

	requireCode(t, err, "one_of")

	if err := schema.Validate(context.Background(), map[string]any{"id": []any{int64(9007199254740993)}}); err != nil {
		t.Fatal(err)
	}
}

// TestImportedConstPreservesRepresentableBoundary verifies that exact input
// integers are not rounded even when the schema's constant itself fits binary64.
func TestImportedConstPreservesRepresentableBoundary(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"const":9007199254740992}`)

	_, err := schema.Parse(context.Background(), int64(9007199254740993))

	requireCode(t, err, "one_of")
}

// TestImportedDecimalMinimumPreservesAllDigits checks a decimal whose tail
// disappears under float64 conversion. The instance is exactly the JSON number
// 0.1, not the larger schema literal, and must fail the minimum constraint.
func TestImportedDecimalMinimumPreservesAllDigits(t *testing.T) {
	t.Parallel()

	doc := `{"minimum":0.1000000000000000000001}`

	schema := fromDoc(t, doc)

	_, err := schema.Parse(context.Background(), 0.1)

	requireCode(t, err, "number.min")

	encoded, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != doc {
		t.Fatalf("decimal lost precision on export: %s", encoded)
	}
}

// TestImportedDefaultPreservesLargeNumber checks that Parse never returns a
// rounded default. Its returned numeric representation must encode the exact
// original JSON number rather than silently substituting an adjacent integer.
func TestImportedDefaultPreservesLargeNumber(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"default":9007199254740993}`)

	value, err := schema.Parse(context.Background(), nil)

	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(value)

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `9007199254740993` {
		t.Fatalf("default was rounded: %s (%T)", encoded, value)
	}
}

// TestImportedExactNumericInputKeepsTypeAndStorage checks the lossless input
// representation through Parse. The result keeps jsontext.Value rather than
// converting the number, but must not lend the caller's mutable byte storage.
func TestImportedExactNumericInputKeepsTypeAndStorage(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"type":"integer","minimum":9007199254740993}`)

	input := jsontext.Value(`9007199254740993`)

	value, err := schema.Parse(context.Background(), input)

	if err != nil {
		t.Fatal(err)
	}

	output, ok := value.(jsontext.Value)

	if !ok {
		t.Fatalf("Parse changed concrete numeric input type: %T", value)
	}

	output[0] = '8'

	if string(input) != `9007199254740993` {
		t.Fatalf("numeric result exposed input bytes: %s", input)
	}
}

// TestImportedUniqueItemsPreservesAdjacentIntegers checks that uniqueness uses
// the same exact numeric domain as bounds, enum and const. Converting both
// integers to binary64 would incorrectly report a duplicate.
func TestImportedUniqueItemsPreservesAdjacentIntegers(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"uniqueItems":true}`)

	input := []any{int64(9007199254740992), int64(9007199254740993)}

	if _, err := schema.Parse(context.Background(), input); err != nil {
		t.Fatalf("distinct exact integers rejected: %v", err)
	}
}

// TestImportedUniqueItemsComparesNestedNumbersMathematically checks JSON
// numeric equality below container boundaries. Concrete Go representations
// remain unchanged, but int64(1) and the JSON decimal 1.0 are equal values.
func TestImportedUniqueItemsComparesNestedNumbersMathematically(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"uniqueItems":true}`)

	input := []any{map[string]any{"n": int64(1)}, map[string]any{"n": float64(1)}}

	_, err := schema.Parse(context.Background(), input)

	requireCode(t, err, "slice.unique")
}

// TestImportedUniqueItemsKeepsStructuralBoundaries protects exact structural
// keys without depending on private key encodings. Delimiters, object names,
// array boundaries and scalar kinds must not cause false duplicate matches.
func TestImportedUniqueItemsKeepsStructuralBoundaries(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"uniqueItems":true}`)

	input := []any{
		[]any{"a", "bc"}, []any{"ab", "c"},
		map[string]any{"a": "bc"}, map[string]any{"ab": "c"},
		"n", "a\x00b", "a", false, nil,
	}

	if _, err := schema.Parse(context.Background(), input); err != nil {
		t.Fatalf("different structural values collided: %v", err)
	}
}

// roundTripVerdict returns the pointers that a violation was reported at,
// or nil for a pass.
func roundTripVerdict(t *testing.T, err error) []string {
	t.Helper()

	if err == nil {
		return nil
	}

	errs, ok := errors.AsType[validate.Errors](err)

	if !ok {
		t.Fatalf("error %v is not validate.Errors", err)
	}

	return slices.Sorted(maps.Keys(errs))
}

// requireRoundTrip fails unless s accepts every value in accept and
// rejects every value in reject, and the validator built from its JSON
// Schema gives the same verdict, at the same pointers, for the JSON of
// each value.
func requireRoundTrip[T any](t *testing.T, s validate.Schema[T], accept, reject []T) {
	t.Helper()

	ctx := context.Background()

	doc, err := s.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	check, err := validate.FromJSONSchema(doc)

	if err != nil {
		t.Fatalf("FromJSONSchema(%s): %v", doc, err)
	}

	rendered, err := check.JSONSchema()

	if err != nil || string(rendered) != string(doc) {
		t.Fatalf("re-rendered %s, want %s (%v)", rendered, doc, err)
	}

	for want, values := range map[bool][]T{
		true:  accept,
		false: reject,
	} {
		for _, value := range values {
			encoded, err := json.Marshal(value)

			if err != nil {
				t.Fatal(err)
			}

			var generic any

			if err := json.Unmarshal(encoded, &generic); err != nil {
				t.Fatal(err)
			}

			goPointers := roundTripVerdict(t, s.Validate(ctx, value))
			jsonPointers := roundTripVerdict(t, check.Validate(ctx, generic))

			if (goPointers == nil) != want {
				t.Fatalf("%s: Go schema verdict for %s: pointers %v, want pass=%v", doc, encoded, goPointers, want)
			}

			if !slices.Equal(goPointers, jsonPointers) {
				t.Fatalf("%s: %s: Go pointers %v, JSON pointers %v", doc, encoded, goPointers, jsonPointers)
			}
		}
	}
}

// roundTripTier is a named enum value used by the schema round-trip tests. Its
// JSON representation remains a string.
type roundTripTier string

// roundTripAddress supplies nested object values for round-trip checks. The
// same schema is exercised directly and behind pointers.
type roundTripAddress struct {
	// City holds a nested string whose diagnostic path uses the JSON field name.
	City string `json:"city"`
	// Zip holds the postal value constrained by the nested address schema.
	Zip string `json:"zip"`
}

// roundTripAccount combines the supported Go-to-JSON field shapes for round-
// trip checks. Optional collections and pointer children test presence at the
// serialization boundary.
type roundTripAccount struct {
	// Email holds the address checked by email rules and JSON-name discovery.
	Email string `json:"email"`
	// Tier holds the named string enum used in the object schema.
	Tier roundTripTier `json:"tier"`
	// Tags provides slice storage for child rules and copy-isolation checks.
	Tags []string `json:"tags,omitempty"`
	// Limits provides numeric map values for serialized collection validation.
	Limits map[string]int `json:"limits,omitempty"`
	// Address provides a nested object for traversal and diagnostic-path checks.
	Address roundTripAddress `json:"address"`
	// Backup provides an optional nested object for pointer-presence checks.
	Backup *roundTripAddress `json:"backup"`
	// Timeout provides the duration representation checked by inference or
	// serialization.
	Timeout validate.Duration `json:"timeout"`
	// Extra provides an additional value outside the primary child constraints.
	Extra any `json:"extra,omitempty"`
}

// TestRoundTripString checks agreement between native string constraints and
// their rendered JSON Schema after serialization.
//
// Example:
//
//	go test -run "^TestRoundTripString$" .
func TestRoundTripString(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}), []string{"abc", "abcd"}, []string{"ab"})
	requireRoundTrip(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Max(3)
	}), []string{"abc", "é"}, []string{"abcd"})
	requireRoundTrip(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Len(2)
	}), []string{"éé"}, []string{"a", "abc"})
	requireRoundTrip(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Email()
	}), []string{"ada@example.com"}, []string{"ada", "Ada <ada@example.com>"})
	requireRoundTrip(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.UUID()
	}), []string{"123e4567-e89b-12d3-a456-426614174000"}, []string{"123e4567"})
	requireRoundTrip(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Duration()
	}), []string{"182ms", "1h30m"}, []string{"soon", "5"})
	requireRoundTrip(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.URL()
	}), []string{"https://example.com/a"}, []string{"/a", "example.com"})
}

// TestRoundTripStringEnumAndRegex checks enum agreement after serialization
// for named and ordinary strings.
//
// Example:
//
//	go test -run "^TestRoundTripStringEnumAndRegex$" .
func TestRoundTripStringEnumAndRegex(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.String(func(value *validate.StringBuilder[roundTripTier]) {
		value.OneOf("free", "pro")
	}), []roundTripTier{"free", "pro"}, []roundTripTier{"gold"})
	requireRoundTrip(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.OneOf("a", "b")
	}), []string{"a"}, []string{"c"})
}

// TestRoundTripInt checks signed numeric constraint agreement across the Go
// and decoded-JSON validators.
//
// Example:
//
//	go test -run "^TestRoundTripInt$" .
func TestRoundTripInt(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Min(5)
	}), []int{5, 9}, []int{4, -1})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Max(5)
	}), []int{5, -9}, []int{6})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Between(2, 4)
	}), []int{2, 3, 4}, []int{1, 5})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Gt(2)
	}), []int{3}, []int{2, 1})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Gte(2)
	}), []int{2}, []int{1})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Lt(2)
	}), []int{1}, []int{2})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Lte(2)
	}), []int{2}, []int{3})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Positive()
	}), []int{1}, []int{-1})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.NonNegative()
	}), []int{1}, []int{-1})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.MultipleOf(5)
	}), []int{10, -5}, []int{7})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.OneOf(1, 2)
	}), []int{1, 2}, []int{3})
	requireRoundTrip(t, validate.Int(func(number *validate.IntBuilder[int8]) {
		number.Max(100)
	}), []int8{5}, []int8{101})
}

// TestRoundTripUint checks unsigned bounds and membership agreement across the
// serialization boundary.
//
// Example:
//
//	go test -run "^TestRoundTripUint$" .
func TestRoundTripUint(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Max(10)
	}), []uint{1, 10}, []uint{11})
	requireRoundTrip(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Between(2, 4)
	}), []uint{3}, []uint{1, 5})
	requireRoundTrip(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.OneOf(1, 3)
	}), []uint{3}, []uint{2})
}

// TestRoundTripFloat checks floating-point bounds and divisibility agreement
// after serialization.
//
// Example:
//
//	go test -run "^TestRoundTripFloat$" .
func TestRoundTripFloat(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Min(0.5)
	}), []float64{0.5, 2.25}, []float64{0.25})
	requireRoundTrip(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Max(0.5)
	}), []float64{0.25}, []float64{0.75})
	requireRoundTrip(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Gt(1.5)
		number.Lt(3.5)
	}), []float64{2}, []float64{1.5, 3.5})
	requireRoundTrip(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.MultipleOf(0.5)
	}), []float64{1.5, 3}, []float64{1.25})
}

// TestRoundTripBool checks boolean intent and unconstrained values across both
// validation paths.
//
// Example:
//
//	go test -run "^TestRoundTripBool$" .
func TestRoundTripBool(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Accepted()
	}), []bool{true}, []bool{false})
	requireRoundTrip(t, validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Declined()
	}), []bool{false}, []bool{true})
	requireRoundTrip(t, validate.Bool[bool](), []bool{true, false}, nil)
}

// TestRoundTripTime checks time-string representation agreement, including the
// zero instant.
//
// Example:
//
//	go test -run "^TestRoundTripTime$" .
func TestRoundTripTime(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.Time(), []time.Time{time.Date(2026, 10, 16, 10, 0, 0, 5, time.UTC), {}}, nil)
}

// TestRoundTripSlice checks nested element, size and uniqueness constraints
// after collection serialization.
//
// Example:
//
//	go test -run "^TestRoundTripSlice$" .
func TestRoundTripSlice(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Min(5)
		})
	}), [][]int{{5, 6}, {}}, [][]int{{5, 1, 2}})
	requireRoundTrip(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Min(2)
	}), [][]string{{"a", "b"}}, [][]string{{"a"}})
	requireRoundTrip(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Max(1)
	}), [][]string{{"a"}}, [][]string{{"a", "b"}})
	requireRoundTrip(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Len(2)
	}), [][]string{{"a", "b"}}, [][]string{{"a"}})
	requireRoundTrip(t, validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
		})

		items.Unique()
	}), [][]string{{"a", "b"}}, [][]string{{"a", "a"}})
	requireRoundTrip(t, validate.Slice(func(items *validate.SliceBuilder[[]int]) {
		items.Values(func(items *validate.SliceBuilder[int]) {
			items.Values(func(number *validate.IntBuilder[int]) {
				number.Min(2)
			})
		})
	}), [][][]int{{{2}, {3}}}, [][][]int{{{2}, {1}}})
}

// TestRoundTripMap checks map-value constraints and property-count bounds
// after object serialization.
//
// Example:
//
//	go test -run "^TestRoundTripMap$" .
func TestRoundTripMap(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Keys(func(value *validate.StringBuilder[string]) {
		})
		entries.Values(func(number *validate.IntBuilder[int]) {
			number.Min(5)
		})
	}), []map[string]int{{"a": 5}}, []map[string]int{{"a": 5, "b": 1}})
	requireRoundTrip(t, validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Keys(func(value *validate.StringBuilder[string]) {
		})
		entries.Values(func(number *validate.IntBuilder[int]) {
		})

		entries.Min(1)
	}), []map[string]int{{"a": 1}}, []map[string]int{{}})
	requireRoundTrip(t, validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Keys(func(value *validate.StringBuilder[string]) {
		})
		entries.Values(func(number *validate.IntBuilder[int]) {
		})

		entries.Max(1)
	}), []map[string]int{{"a": 1}}, []map[string]int{{"a": 1, "b": 2}})
}

// TestRoundTripPtr checks optional-pointer and forced-pointee presence at the
// JSON boundary.
//
// Example:
//
//	go test -run "^TestRoundTripPtr$" .
func TestRoundTripPtr(t *testing.T) {
	t.Parallel()

	five, one, zero := 5, 1, 0

	requireRoundTrip(t, validate.Ptr[int](validate.Int(func(number *validate.IntBuilder[int]) {
		number.Min(5)
	})), []*int{nil, &five}, []*int{&one, &zero})
}

// TestRoundTripAny checks unconstrained dynamic JSON-compatible values through
// both validators.
//
// Example:
//
//	go test -run "^TestRoundTripAny$" .
func TestRoundTripAny(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.Any(), []any{"x", 1, true, []any{"a"}, map[string]any{"a": 1}, nil}, nil)
}

// TestRoundTripText checks text-backed duration representations through both
// validators.
//
// Example:
//
//	go test -run "^TestRoundTripText$" .
func TestRoundTripText(t *testing.T) {
	t.Parallel()

	requireRoundTrip(t, validate.Text[validate.Duration](), []validate.Duration{validate.Duration(182 * time.Millisecond), 0}, nil)
}

// TestRoundTripStruct checks nested rules and pointer agreement while array
// contents remain within maxItems. Oversized-array error collection has its
// own explicit compatibility test instead of requiring identical child errors.
func TestRoundTripStruct(t *testing.T) {
	t.Parallel()

	schema := validate.Struct[roundTripAccount](func(object *validate.StructBuilder[roundTripAccount]) {
		object.Field("Email", func(value *validate.StringBuilder[string]) {
			value.Required()
			value.Email()
		})
		object.Field("Tier", func(value *validate.StringBuilder[roundTripTier]) {
			value.OneOf("free", "pro")
			value.Required()
		})
		object.Field("Tags", func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
				value.Min(2)
			})
			items.Max(3)
		})
		object.Field("Limits", func(entries *validate.MapBuilder[string, int]) {
			entries.Keys(func(value *validate.StringBuilder[string]) {
			})
			entries.Values(func(number *validate.IntBuilder[int]) {
				number.Min(1)
			})
		})
		object.Field("Address", func(object *validate.StructBuilder[roundTripAddress]) {
			object.Field("City", func(value *validate.StringBuilder[string]) {
				value.Required()
			})
			object.Field("Zip", func(value *validate.StringBuilder[string]) {
				value.Len(5)
			})
		})
		object.Field("Backup", func(pointer *validate.PtrBuilder[roundTripAddress]) {
			pointer.Value(func(object *validate.StructBuilder[roundTripAddress]) {
				object.Field("City", func(value *validate.StringBuilder[string]) {
					value.Required()
				})
			})
		})
		object.Field("Timeout", func(value *validate.TextBuilder[validate.Duration]) {
			value.Required()
		})
		object.Field("Extra", func(value *validate.AnyBuilder) {
		})
	})

	good := roundTripAccount{
		Email:  "ada@example.com",
		Tier:   "pro",
		Tags:   []string{"ab"},
		Limits: map[string]int{"a": 1},
		Address: roundTripAddress{
			City: "Lisbon",
			Zip:  "12345",
		},
		Timeout: validate.Duration(time.Second),
		Extra:   []any{1.0},
	}

	badEmail := good
	badEmail.Email = "nope"

	missingTier := good
	missingTier.Tier = ""

	badNested := good
	badNested.Tags = []string{"ab", "c", "de"}
	badNested.Limits = map[string]int{"a": 1, "b": 0}
	badNested.Address = roundTripAddress{
		City: "Lisbon",
		Zip:  "1",
	}
	badNested.Backup = &roundTripAddress{
		City: "Porto",
	}

	requireRoundTrip(t, schema, []roundTripAccount{good}, []roundTripAccount{badEmail, missingTier, badNested})
}

// TestRoundTripInferredStruct checks type-only documents for nested objects,
// collections and recursive children after serialization.
//
// Example:
//
//	go test -run "^TestRoundTripInferredStruct$" .
func TestRoundTripInferredStruct(t *testing.T) {
	t.Parallel()

	account := roundTripAccount{
		Email:   "ada@example.com",
		Timeout: validate.Duration(time.Second),
		Backup:  &roundTripAddress{},
	}

	requireRoundTrip(t, validate.Infer[roundTripAccount](), []roundTripAccount{account, {}}, nil)
	requireRoundTrip(t, validate.Infer[[]roundTripAccount](), [][]roundTripAccount{{account}, nil}, nil)
	requireRoundTrip(t, validate.Infer[map[string]inferTreeNode](), []map[string]inferTreeNode{{"a": {
		Name: "root",
		Children: []inferTreeNode{{
			Name: "leaf",
		}},
	}}}, nil)
}

// TestRoundTripZeroValuesDiffer documents the presence-rule difference:
// Struct skips the rules and Required
// semantics of zero values, JSON Schema does not.
func TestRoundTripZeroValuesDiffer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.Struct[roundTripAddress](func(object *validate.StructBuilder[roundTripAddress]) {
		object.Field("City", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Zip", func(value *validate.StringBuilder[string]) {
			value.Min(3)
		})
	})

	doc, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	check, err := validate.FromJSONSchema(doc)

	if err != nil {
		t.Fatal(err)
	}

	requireCodes(t, schema.Validate(ctx, roundTripAddress{}), map[string][]string{"/city": {"required"}})
	requireCodes(t, check.Validate(ctx, map[string]any{"city": "", "zip": ""}), map[string][]string{"/zip": {"string.min"}})
}

// TestRoundTripOversizedArrayCollectsOnlySize documents the intentional
// error-collection difference: the typed slice still checks children, but
// JSON Schema stops at maxItems before traversing an oversized array.
// Both validators must continue to reject the same oversized input.
func TestRoundTripOversizedArrayCollectsOnlySize(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(value *validate.StringBuilder[string]) {
			value.Min(2)
		})

		items.Max(1)
	})

	doc, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	check, err := validate.FromJSONSchema(doc)

	if err != nil {
		t.Fatal(err)
	}

	requireCodes(t, schema.Validate(context.Background(), []string{"a", "b"}),
		map[string][]string{"": {"slice.max"}, "/0": {"string.min"}, "/1": {"string.min"}})
	requireCodes(t, check.Validate(context.Background(), []any{"a", "b"}),
		map[string][]string{"": {"slice.max"}})
}
