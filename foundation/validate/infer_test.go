package validate_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// TestInferredParseHonorsCancellation keeps type-only schemas consistent with
// the execution contract without adding value rules or normalizing their input.
func TestInferredParseHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cancel()

	value, err := validate.Infer[string]().Parse(ctx, "Ada")

	if !errors.Is(err, context.Canceled) || value != "" {
		t.Fatalf("inferred Parse = %q, %v", value, err)
	}
}

// jsonObjectKey is a comparable struct whose JSON method supplies a map name.
// It has no text method, so inference must recognize JSON marshaling itself.
type jsonObjectKey struct {
	// name supplies the JSON object member name without a text-marshaling hook.
	name string
}

// MarshalJSON supplies the string required by JSON-v2's map-key boundary.
// The fixture cannot fail and does not change its retained key name.
//
// Example:
//
//	encoded, err := json.Marshal(map[jsonObjectKey]int{{name: "score"}: 3})
//	if err != nil {
//		panic(err)
//	}
//	_ = encoded
func (key jsonObjectKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(key.name)
}

// requireInferredEncoding checks inference against actual JSON-v2 output,
// without private reflection or binding assertions. Both decoding and schema
// import errors fail the caller before checking the encoded instance.
func requireInferredEncoding(t *testing.T, schema interface{ JSONSchema() ([]byte, error) }, value any) {
	t.Helper()

	encoded, err := json.Marshal(value)

	if err != nil {
		t.Fatal(err)
	}

	document, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	imported, err := validate.FromJSONSchema(document)

	if err != nil {
		t.Fatal(err)
	}

	var instance any

	if err := json.Unmarshal(encoded, &instance); err != nil {
		t.Fatal(err)
	}

	if err := imported.Validate(context.Background(), instance); err != nil {
		t.Fatalf("inferred schema %s rejects JSON-v2 output %s: %v", document, encoded, err)
	}
}

// TestInferenceStringifiedNumberMatchesEncoding checks the string tag's wire
// shape rather than the numeric kind of the Go field stored before encoding.
func TestInferenceStringifiedNumberMatchesEncoding(t *testing.T) {
	t.Parallel()

	// form encodes Count as a JSON string.
	type form struct {
		// Count uses JSON-v2's top-level numeric stringification option.
		Count int `json:"count,string"`
	}

	requireInferredEncoding(t, validate.Infer[form](), form{Count: 7})
}

// TestInferenceNamedBytesMatchEncoding checks Go 1.27's named-byte dispatch:
// a sequence of a defined uint8 type is an array, not base64 byte storage.
func TestInferenceNamedBytesMatchEncoding(t *testing.T) {
	t.Parallel()

	// octet is a defined element type rather than the builtin byte alias.
	type octet uint8

	requireInferredEncoding(t, validate.Infer[[]octet](), []octet{1, 2})
}

// TestInferenceUnsupportedByteFormatStaysOpaque checks the installed Go 1.27
// encoder's format-tag limitation. Inference must not promise array or base64
// output for a field whose format is unmodeled and whose encoding fails.
func TestInferenceUnsupportedByteFormatStaysOpaque(t *testing.T) {
	t.Parallel()

	// form requests a format unsupported by the Go 1.27 JSON-v2 encoder.
	type form struct {
		// Bytes must not inherit the default base64 metadata with this option.
		Bytes []byte `json:"bytes,format:array"`
	}

	wantSchema(t, validate.Infer[form](),
		`{"properties":{"bytes":{}},"required":["bytes"],"type":"object"}`)

	if encoded, err := json.Marshal(form{Bytes: []byte{1, 2}}); err == nil {
		t.Fatalf("JSON-v2 unexpectedly accepted unsupported byte format: %s", encoded)
	}
}

// TestInferenceUnsupportedTimeFormatStaysOpaque checks that time format tags
// do not inherit RFC 3339 metadata. Go 1.27 rejects the requested Unix format;
// accepting the inferred schema does not imply that encoding will succeed.
func TestInferenceUnsupportedTimeFormatStaysOpaque(t *testing.T) {
	t.Parallel()

	// form requests an unmodeled and currently unsupported time representation.
	type form struct {
		// Unix must not be described as default RFC 3339 date-time text.
		Unix time.Time `json:"unix,format:unix"`
	}

	wantSchema(t, validate.Infer[form](),
		`{"properties":{"unix":{}},"required":["unix"],"type":"object"}`)

	instant := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	if encoded, err := json.Marshal(form{Unix: instant}); err == nil {
		t.Fatalf("JSON-v2 unexpectedly accepted unsupported time format: %s", encoded)
	}
}

// TestInferenceFloatingMapKeysMatchEncoding checks native numeric map names.
// JSON-v2 stringifies finite floating keys just as it stringifies integer keys.
func TestInferenceFloatingMapKeysMatchEncoding(t *testing.T) {
	t.Parallel()

	requireInferredEncoding(t, validate.Infer[map[float64]int](), map[float64]int{1.25: 2})
}

// TestInferenceJSONMapKeyMatchesEncoding checks custom JSON map names without
// assuming that every supported key type implements encoding.TextMarshaler.
func TestInferenceJSONMapKeyMatchesEncoding(t *testing.T) {
	t.Parallel()

	requireInferredEncoding(t, validate.Infer[map[jsonObjectKey]int](),
		map[jsonObjectKey]int{{name: "score"}: 3})
}

// TestInferencePointerMapKeyMatchesEncoding covers native pointer dispatch.
// JSON-v2 dereferences a nonnil numeric key and stringifies the number as an
// object name. Inference describes the type, not pointer identity or nil safety.
func TestInferencePointerMapKeyMatchesEncoding(t *testing.T) {
	t.Parallel()

	key := 4

	requireInferredEncoding(t, validate.Infer[map[*int]int](), map[*int]int{&key: 3})
}

// TestInferenceInterfaceMapKeyMatchesEncoding checks dynamic key dispatch.
// The encoder owns which concrete interface values can become object names;
// type-only inference must not reject all such maps as unrepresentable.
func TestInferenceInterfaceMapKeyMatchesEncoding(t *testing.T) {
	t.Parallel()

	requireInferredEncoding(t, validate.Infer[map[any]int](), map[any]int{"score": 3})
}

// TestInferenceFormatOptionsRemainOpaque checks schema metadata independently
// of encoder support for format tags. Quoted commas cannot invent omission or
// embedding options, and unmodeled formats cannot advertise a false wire type.
func TestInferenceFormatOptionsRemainOpaque(t *testing.T) {
	t.Parallel()

	// form carries a quoted format whose contents resemble unrelated options.
	type form struct {
		// Label stays required even though its format contains option names.
		Label time.Time `json:"label,format:'2006,omitempty,inline'"`
		// Elapsed has no assumed numeric or textual wire shape.
		Elapsed time.Duration `json:"elapsed,omitempty,format:nano"`
	}

	wantSchema(t, validate.Infer[form](),
		`{"properties":{"elapsed":{},"label":{}},"required":["label"],"type":"object"}`)
}

// inferLevel is a named string used to test kind-based inference. It must
// describe a JSON string without losing named-type compatibility.
type inferLevel string

// inferBase provides an embedded JSON-named identifier for inference. The same
// type can also appear in independent sibling branches.
type inferBase struct {
	// ID provides an identifier for JSON naming and embedding checks.
	ID string `json:"id"`
}

// inferTreeNode is a recursive collection fixture for schema inference.
// Repeated types on the active path must terminate without hiding independent
// branches.
type inferTreeNode struct {
	// Name holds the name observed by string rules or sibling conditions.
	Name string `json:"name"`
	// Children introduces recursion through a slice of the enclosing type.
	Children []inferTreeNode `json:"children"`
}

// inferResult combines JSON tags, a named scalar and a duration adapter. Its
// inferred document checks required-field selection and exclusions together.
type inferResult struct {
	// Name holds the name observed by string rules or sibling conditions.
	Name string `json:"name"`
	// Elapsed holds a duration encoded through its text-marshaling adapter.
	Elapsed validate.Duration `json:"elapsed"`
	// Note provides optional text omitted from the inferred required list.
	Note string `json:"note,omitempty"`
	// Count provides a numeric value for conditional or inferred-field checks.
	Count int `json:"count"`
	// Skipped is excluded from JSON and must not become a visible schema
	// property.
	Skipped string `json:"-"`
}

// TestInferScalars checks the JSON kind inferred for built-in and named scalar
// values.
//
// Example:
//
//	go test -run "^TestInferScalars$" .
func TestInferScalars(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[string](), `{"type":"string"}`)
	wantSchema(t, validate.Infer[inferLevel](), `{"type":"string"}`)
	wantSchema(t, validate.Infer[bool](), `{"type":"boolean"}`)
	wantSchema(t, validate.Infer[int32](), `{"type":"integer"}`)
	wantSchema(t, validate.Infer[uint8](), `{"minimum":0,"type":"integer"}`)
	wantSchema(t, validate.Infer[float32](), `{"type":"number"}`)
}

// TestInferTextMarshalers checks that custom text encodings take precedence
// over underlying Go kinds.
//
// Example:
//
//	go test -run "^TestInferTextMarshalers$" .
func TestInferTextMarshalers(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[validate.Duration](), `{"format":"go-duration","type":"string"}`)
	wantSchema(t, validate.Infer[time.Time](), `{"format":"date-time","type":"string"}`)
	wantSchema(t, validate.Infer[*validate.Duration](), `{"format":"go-duration","type":"string"}`)
}

// TestInferStructFields checks inferred JSON names, field exclusion and
// required-field selection together.
//
// Example:
//
//	go test -run "^TestInferStructFields$" .
func TestInferStructFields(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[inferResult](),
		`{"properties":{"count":{"type":"integer"},"elapsed":{"format":"go-duration","type":"string"},`+
			`"name":{"type":"string"},"note":{"type":"string"}},"required":["name","elapsed","count"],"type":"object"}`)
}

// TestInferOmitZeroIsNotRequired checks that omitzero removes a field from the
// inferred required list.
//
// Example:
//
//	go test -run "^TestInferOmitZeroIsNotRequired$" .
func TestInferOmitZeroIsNotRequired(t *testing.T) {
	t.Parallel()

	type event struct {
		// At provides the instant used by zero-time presence or omission checks.
		At time.Time `json:"at,omitzero"`
		// Name holds the name observed by string rules or sibling conditions.
		Name string `json:"name"`
	}

	wantSchema(t, validate.Infer[event](),
		`{"properties":{"at":{"format":"date-time","type":"string"},"name":{"type":"string"}},"required":["name"],"type":"object"}`)
}

// TestInferEmbeddedStructIsPromoted checks that untagged embedded fields
// appear in the enclosing object document.
//
// Example:
//
//	go test -run "^TestInferEmbeddedStructIsPromoted$" .
func TestInferEmbeddedStructIsPromoted(t *testing.T) {
	t.Parallel()

	type user struct {
		// inferBase promotes a JSON-named identifier into the inferred object.
		inferBase
		// Email holds the address checked by email rules and JSON-name discovery.
		Email string `json:"email"`
	}

	wantSchema(t, validate.Infer[user](),
		`{"properties":{"email":{"type":"string"},"id":{"type":"string"}},"required":["id","email"],"type":"object"}`)
}

// TestInferCollections checks inferred element, map-value and byte-string
// representations.
//
// Example:
//
//	go test -run "^TestInferCollections$" .
func TestInferCollections(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[[]string](), `{"items":{"type":"string"},"type":"array"}`)
	wantSchema(t, validate.Infer[[3]int](), `{"items":{"type":"integer"},"type":"array"}`)
	wantSchema(t, validate.Infer[[]byte](), `{"type":"string"}`)
	wantSchema(t, validate.Infer[map[string]bool](), `{"additionalProperties":{"type":"boolean"},"type":"object"}`)
	wantSchema(t, validate.Infer[map[int]string](), `{"additionalProperties":{"type":"string"},"type":"object"}`)
	wantSchema(t, validate.Infer[map[validate.Duration]int](), `{"additionalProperties":{"type":"integer"},"type":"object"}`)
}

// TestInferPointer checks that pointer adaptation preserves the pointee
// document.
//
// Example:
//
//	go test -run "^TestInferPointer$" .
func TestInferPointer(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[*int](), `{"type":"integer"}`)
	wantSchema(t, validate.Infer[*[]string](), `{"items":{"type":"string"},"type":"array"}`)
}

// TestInferInterfaces checks that interface-valued shapes remain unconstrained
// JSON documents.
//
// Example:
//
//	go test -run "^TestInferInterfaces$" .
func TestInferInterfaces(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[any](), `{}`)
	wantSchema(t, validate.Infer[error](), `{}`)
	wantSchema(t, validate.Infer[[]any](), `{"items":{},"type":"array"}`)
}

// TestInferRecursiveTypeStopsAtSecondLevel checks that recursive child
// traversal terminates at the repeated active type.
//
// Example:
//
//	go test -run "^TestInferRecursiveTypeStopsAtSecondLevel$" .
func TestInferRecursiveTypeStopsAtSecondLevel(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[inferTreeNode](),
		`{"properties":{"children":{"items":{},"type":"array"},"name":{"type":"string"}},"required":["name","children"],"type":"object"}`)
}

// TestInferRecursiveThroughPointer checks cycle detection when recursive
// object links pass through pointers.
//
// Example:
//
//	go test -run "^TestInferRecursiveThroughPointer$" .
func TestInferRecursiveThroughPointer(t *testing.T) {
	t.Parallel()

	type list struct {
		// Next introduces recursion through a pointer to the enclosing type.
		Next *list `json:"next,omitempty"`
		// V provides a nonrecursive value beside the recursive pointer.
		V int `json:"v"`
	}

	wantSchema(t, validate.Infer[list](),
		`{"properties":{"next":{},"v":{"type":"integer"}},"required":["v"],"type":"object"}`)
}

// TestInferSameTypeTwiceIsNotACycle checks that independent sibling uses of
// one type both receive complete documents.
//
// Example:
//
//	go test -run "^TestInferSameTypeTwiceIsNotACycle$" .
func TestInferSameTypeTwiceIsNotACycle(t *testing.T) {
	t.Parallel()

	type pair struct {
		// Left provides one independent occurrence of a reusable nested type.
		Left inferBase `json:"left"`
		// Right provides a second independent occurrence of the same nested type.
		Right inferBase `json:"right"`
	}

	wantSchema(t, validate.Infer[pair](),
		`{"properties":{"left":{"properties":{"id":{"type":"string"}},"required":["id"],"type":"object"},`+
			`"right":{"properties":{"id":{"type":"string"}},"required":["id"],"type":"object"}},"required":["left","right"],"type":"object"}`)
}

// TestInferRawDurationIsAnError checks consistent invalid-schema reporting for
// time.Duration at every schema entry point.
//
// Example:
//
//	go test -run "^TestInferRawDurationIsAnError$" .
func TestInferRawDurationIsAnError(t *testing.T) {
	t.Parallel()

	schema := validate.Infer[struct {
		// Timeout provides the duration representation checked by inference or
		// serialization.
		Timeout time.Duration `json:"timeout"`
	}]()

	if !errors.Is(schema.Err(), validate.ErrInvalidSchema) {
		t.Fatalf("Err() = %v", schema.Err())
	}

	if _, err := schema.JSONSchema(); !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("JSONSchema error = %v", err)
	}

	if err := schema.Validate(context.Background(), struct {
		// Timeout provides the duration representation checked by inference or
		// serialization.
		Timeout time.Duration `json:"timeout"`
	}{}); !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("Validate error = %v", err)
	}
}

// TestInferErrorNamesThePath checks that an unsupported nested type reports
// its exact field path.
//
// Example:
//
//	go test -run "^TestInferErrorNamesThePath$" .
func TestInferErrorNamesThePath(t *testing.T) {
	t.Parallel()

	err := validate.Infer[[]struct {
		// Timeout provides the duration representation checked by inference or
		// serialization.
		Timeout time.Duration
	}]().Err()

	if err == nil {
		t.Fatal("expected an error")
	}

	want := `validate: invalid schema: Infer[[]struct { Timeout time.Duration }]: items: field "Timeout": time.Duration has no JSON representation; use validate.Duration`

	if err.Error() != want {
		t.Fatalf("error = %q\nwant    %q", err, want)
	}
}

// TestInferUnsupportedKinds checks invalid-schema reporting for kinds without
// supported JSON representations.
//
// Example:
//
//	go test -run "^TestInferUnsupportedKinds$" .
func TestInferUnsupportedKinds(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]error{
		"chan":       validate.Infer[chan int]().Err(),
		"func":       validate.Infer[func()]().Err(),
		"complex":    validate.Infer[complex128]().Err(),
		"map key":    validate.Infer[map[bool]int]().Err(),
		"nested":     validate.Infer[map[string][]chan int]().Err(),
		"struct key": validate.Infer[map[struct{}]int]().Err(),
	} {
		if !errors.Is(err, validate.ErrInvalidSchema) {
			t.Fatalf("%s: Err() = %v", name, err)
		}
	}
}

// TestInferValidateAndParseAcceptEverything checks that type-only inference
// adds no runtime value constraints.
//
// Example:
//
//	go test -run "^TestInferValidateAndParseAcceptEverything$" .
func TestInferValidateAndParseAcceptEverything(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.Infer[inferResult]()

	value := inferResult{
		Count: 0,
	}

	if err := schema.Validate(ctx, value); err != nil {
		t.Fatal(err)
	}

	parsed, err := schema.Parse(ctx, value)

	if err != nil || parsed != value {
		t.Fatalf("parsed = %v, %v", parsed, err)
	}
}

// TestInferInsideStructRegistration verifies inferred composite schemas still
// register with their exact field types and retain their JSON Schema documents.
func TestInferInsideStructRegistration(t *testing.T) {
	t.Parallel()

	type report struct {
		// Items provides child values for collection traversal and default checks.
		Items []inferBase `json:"items"`
		// Owner provides a nested object alongside the inferred collection.
		Owner *inferBase `json:"owner"`
	}

	schema := validate.Struct[report](func(object *validate.StructBuilder[report]) {
		object.Field("Items", func(items *validate.SliceBuilder[inferBase]) {
			items.Use(validate.Infer[[]inferBase]())
		})
		object.Field("Owner", func(pointer *validate.PtrBuilder[inferBase]) {
			pointer.Value(func(object *validate.StructBuilder[inferBase]) {
				object.Use(validate.Infer[inferBase]())
			})
			pointer.Describe("Who owns it")
		})
	})

	wantSchema(t, schema,
		`{"properties":{"items":{"items":{"properties":{"id":{"type":"string"}},"required":["id"],"type":"object"},"type":"array"},`+
			`"owner":{"description":"Who owns it","properties":{"id":{"type":"string"}},"required":["id"],"type":"object"}},"type":"object"}`)
}

// TestInferDocumentIsNotSharedWithCallers checks that annotations on an
// adapted schema cannot change the source document.
//
// Example:
//
//	go test -run "^TestInferDocumentIsNotSharedWithCallers$" .
func TestInferDocumentIsNotSharedWithCallers(t *testing.T) {
	t.Parallel()

	inner := validate.Infer[inferBase]()

	_ = validate.Ptr[inferBase](inner, func(pointer *validate.PtrBuilder[inferBase]) {
		pointer.Describe("changed")
	})

	wantSchema(t, validate.Ptr[inferBase](inner), `{"properties":{"id":{"type":"string"}},"required":["id"],"type":"object"}`)
}

// TestReuseBuilderPointerInferRemainsTypeOnly preserves the documented Infer
// reuse path while applying the same pointer/value normalization at the boundary.
func TestReuseBuilderPointerInferRemainsTypeOnly(t *testing.T) {
	t.Parallel()

	shared := validate.Infer[[]int]()
	schema := validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Use(&shared)
	})

	if err := schema.Validate(context.Background(), []int{-1}); err != nil {
		t.Fatal(err)
	}

	document, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(document) != `{"items":{"type":"integer"},"type":"array"}` {
		t.Fatalf("document = %s", document)
	}
}

// TestInferredRecursiveEmbeddingMatchesEncoding checks actual JSON-v2 output
// and the complete inferred document; open subschemas cannot conceal an extra
// invented property for the recursive embedded pointer.
func TestInferredRecursiveEmbeddingMatchesEncoding(t *testing.T) {
	t.Parallel()

	// RecursiveOwner supplies a recursive promoted pointer and one JSON leaf.
	type RecursiveOwner struct {
		// RecursiveOwner is promoted rather than exposed under its Go type name.
		*RecursiveOwner
		// Name is the only field the JSON-v2 encoder emits.
		Name string `json:"name"`
	}

	encoded, err := json.Marshal(RecursiveOwner{Name: "Ada"})

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"name":"Ada"}` {
		t.Fatalf("JSON-v2 recursive embedding = %s", encoded)
	}

	wantSchema(t, validate.Infer[RecursiveOwner](),
		`{"properties":{"name":{"type":"string"}},"required":["name"],"type":"object"}`)
}

// TestJSONFieldRejectsMalformedName checks Go 1.27's JSON-v2 tag grammar.
// Quoted field names are invalid; accepting one would invent diagnostic and
// schema names for a field that the actual encoder cannot represent.
func TestJSONFieldRejectsMalformedName(t *testing.T) {
	t.Parallel()

	// form supplies an invalid quoted name rather than a valid tag option.
	type form struct {
		// Name cannot be represented by the current JSON-v2 encoder.
		Name string `json:"'first,last'"`
	}

	if encoded, err := json.Marshal(form{Name: "Ada"}); err == nil {
		t.Fatalf("JSON-v2 accepted malformed tag: %s", encoded)
	}

	requirePanic(t, func() {
		validate.Struct(func(object *validate.StructBuilder[form]) {
			object.Field("Name", func(text *validate.StringBuilder[string]) {
				text.Required()
			})
		})
	})
}

// TestJSONFieldRejectsUnsupportedEmbed checks Go 1.27's explicit flattening
// option. The validator cannot treat an embedded fallback map as an ordinary
// named field: that would publish paths absent from the encoded object.
func TestJSONFieldRejectsUnsupportedEmbed(t *testing.T) {
	t.Parallel()

	// form flattens its fallback map into the parent JSON object.
	type form struct {
		// Extra has no JSON field named Extra; its map keys become object names.
		Extra map[string]string `json:",embed"`
	}

	encoded, err := json.Marshal(form{Extra: map[string]string{"name": "Ada"}})

	if err != nil {
		t.Fatal(err)
	}

	if string(encoded) != `{"name":"Ada"}` {
		t.Fatalf("JSON-v2 embed = %s", encoded)
	}

	requirePanic(t, func() {
		validate.Struct(func(object *validate.StructBuilder[form]) {
			object.Field("Extra", func(entries *validate.MapBuilder[string, string]) {
			})
		})
	})
}

// TestInferredPromotedPointerFieldsAreOptional checks schema requiredness
// against actual encoded output. JSON-v2 omits promoted children of a nil
// embedded pointer even when the child has no omit option of its own.
func TestInferredPromotedPointerFieldsAreOptional(t *testing.T) {
	t.Parallel()

	// Owner contributes a promoted field only when its pointer is present.
	type Owner struct {
		// Name has no local omission option, but its owner can be absent.
		Name string `json:"name"`
	}
	// form leaves the embedded owner nil in this encoded instance.
	type form struct {
		// Owner is omitted as a whole when nil.
		*Owner
	}

	document, err := validate.Infer[form]().JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	schema, err := validate.FromJSONSchema(document)

	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(form{})

	if err != nil {
		t.Fatal(err)
	}

	var instance any

	if err := json.Unmarshal(encoded, &instance); err != nil {
		t.Fatal(err)
	}

	if err := schema.Validate(context.Background(), instance); err != nil {
		t.Fatalf("inferred schema rejects its encoded nil owner %s: %v", encoded, err)
	}
}
