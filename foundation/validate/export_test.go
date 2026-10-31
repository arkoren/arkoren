package validate_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/validate"
)

// requireInvalidExport checks the public metadata boundary without invoking
// Parse: invalid definitions must return a domain error and no usable document.
func requireInvalidExport(t *testing.T, schema interface{ JSONSchema() ([]byte, error) }) {
	t.Helper()

	document, err := schema.JSONSchema()

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("JSONSchema = %s, %v; want ErrInvalidSchema", document, err)
	}

	if document != nil {
		t.Fatalf("invalid definition exported a document: %s", document)
	}
}

// TestExportRejectsRecordedNilSchema prevents a recorded Use error from
// disappearing when the definition is used only as downstream metadata.
//
// Example:
//
//	go test -run '^TestExportRejectsRecordedNilSchema$' .
func TestExportRejectsRecordedNilSchema(t *testing.T) {
	t.Parallel()

	var schema validate.Schema[string] = validate.String(func(text *validate.StringBuilder[string]) {
		text.Use(nil)
	})

	requireInvalidExport(t, schema)
}

// TestExportRejectsNestedNilSchema covers a child definition, not just errors
// on the root schema's own base state.
//
// Example:
//
//	go test -run '^TestExportRejectsNestedNilSchema$' .
func TestExportRejectsNestedNilSchema(t *testing.T) {
	t.Parallel()

	schema := validate.Slice(func(items *validate.SliceBuilder[string]) {
		items.Values(func(text *validate.StringBuilder[string]) {
			text.Use(nil)
		})
	})

	requireInvalidExport(t, schema)
}

// TestExportRejectsInvalidMapKeys ensures omitted key metadata cannot conceal
// an invalid executable key definition from downstream schema consumers.
//
// Example:
//
//	go test -run '^TestExportRejectsInvalidMapKeys$' .
func TestExportRejectsInvalidMapKeys(t *testing.T) {
	t.Parallel()

	schema := validate.Map(func(entries *validate.MapBuilder[string, int]) {
		entries.Keys(func(text *validate.StringBuilder[string]) {
			text.Trim()
		})
	})

	requireInvalidExport(t, schema)
}

// TestExportRejectsZeroStruct distinguishes an unconstructed struct definition
// from the valid, explicitly constructed schema with no registered fields.
//
// Example:
//
//	go test -run '^TestExportRejectsZeroStruct$' .
func TestExportRejectsZeroStruct(t *testing.T) {
	t.Parallel()

	requireInvalidExport(t, validate.StructSchema[struct{}]{})
}

// TestExportRejectsNestedZeroStruct checks zero structs through Ptr's explicit
// public Schema adapter rather than relying on direct export alone.
//
// Example:
//
//	go test -run '^TestExportRejectsNestedZeroStruct$' .
func TestExportRejectsNestedZeroStruct(t *testing.T) {
	t.Parallel()

	schema := validate.Ptr(validate.StructSchema[struct{}]{})

	requireInvalidExport(t, schema)
}

// TestExportRejectsUnboundFieldReference ensures omitted runtime-only rules
// still receive definition checks without actually executing those rules.
//
// Example:
//
//	go test -run '^TestExportRejectsUnboundFieldReference$' .
func TestExportRejectsUnboundFieldReference(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.EqField("Name")
	})

	requireInvalidExport(t, schema)
}

// TestExportRejectsUnboundStructRule covers object-wide rules whose standalone
// compilation failed during sealing but whose schema remains usable in a parent.
//
// Example:
//
//	go test -run '^TestExportRejectsUnboundStructRule$' .
func TestExportRejectsUnboundStructRule(t *testing.T) {
	t.Parallel()

	schema := validate.Struct(func(object *validate.StructBuilder[struct{}]) {
		object.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.Eq("Enabled", true)
		})
	})

	requireInvalidExport(t, schema)
}

// TestExportKeepsParentBoundStructRules preserves context-sensitive object
// definitions: export checks their already bound parent plan, not each child
// as a new standalone schema with its enclosing field context removed.
//
// Example:
//
//	go test -run '^TestExportKeepsParentBoundStructRules$' .
func TestExportKeepsParentBoundStructRules(t *testing.T) {
	t.Parallel()

	type form struct {
		// Enabled is the enclosing field observed by the child object's rule.
		Enabled bool
		// Child has an object-wide condition referring to its parent.
		Child struct{}
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Child", func(child *validate.StructBuilder[struct{}]) {
			child.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Eq("Enabled", true)
			})
		})
	})

	if _, err := schema.JSONSchema(); err != nil {
		t.Fatal(err)
	}
}

// TestExportRejectsNegativeStringLengths covers the three length keywords,
// all of which require nonnegative integer limits in JSON Schema.
//
// Example:
//
//	go test -run '^TestExportRejectsNegativeStringLengths$' .
func TestExportRejectsNegativeStringLengths(t *testing.T) {
	t.Parallel()

	requireInvalidExport(t, validate.String(func(text *validate.StringBuilder[string]) {
		text.Min(-1)
	}))
	requireInvalidExport(t, validate.String(func(text *validate.StringBuilder[string]) {
		text.Max(-1)
	}))
	requireInvalidExport(t, validate.String(func(text *validate.StringBuilder[string]) {
		text.Len(-1)
	}))
}

// TestExportRejectsNegativeMultiples prevents factors accepted by Go's modulo
// operations from producing the illegal JSON Schema multipleOf value -2.
//
// Example:
//
//	go test -run '^TestExportRejectsNegativeMultiples$' .
func TestExportRejectsNegativeMultiples(t *testing.T) {
	t.Parallel()

	requireInvalidExport(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.MultipleOf(-2)
	}))
	requireInvalidExport(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.MultipleOf(-0.5)
	}))
}

// TestExportKeepsOverwrittenDefinitionErrors ensures a later legal annotation
// cannot repair an earlier invalid declaration whose rule is still retained.
//
// Example:
//
//	go test -run '^TestExportKeepsOverwrittenDefinitionErrors$' .
func TestExportKeepsOverwrittenDefinitionErrors(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Min(-1).Min(1)
	})

	requireInvalidExport(t, schema)

	reused := validate.String(func(text *validate.StringBuilder[string]) {
		text.Use(schema).Min(2)
	})

	requireInvalidExport(t, reused)
}

// TestExportKeepsNonFiniteDefinitionErrors checks a non-JSON numeric bound
// before a later annotation would otherwise hide it from the JSON encoder.
//
// Example:
//
//	go test -run '^TestExportKeepsNonFiniteDefinitionErrors$' .
func TestExportKeepsNonFiniteDefinitionErrors(t *testing.T) {
	t.Parallel()

	schema := validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Min(math.NaN()).Min(1)
	})

	requireInvalidExport(t, schema)
	requireInvalidExport(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.MultipleOf(math.Inf(1))
	}))
}

// TestExportDefinitionDiagnosticsAggregate checks that invalid arguments are
// recorded until all construction callbacks complete, then reported together.
//
// Example:
//
//	go test -run '^TestExportDefinitionDiagnosticsAggregate$' .
func TestExportDefinitionDiagnosticsAggregate(t *testing.T) {
	t.Parallel()

	type form struct {
		// Name receives invalid string-length declarations.
		Name string
		// Count receives an invalid divisibility declaration.
		Count int
	}

	calls := 0
	message := requirePanic(t, func() {
		validate.Struct(func(object *validate.StructBuilder[form]) {
			object.Field("Name", func(text *validate.StringBuilder[string]) {
				text.Min(-1).Max(-2).Len(-3)
			})
		}, func(object *validate.StructBuilder[form]) {
			calls++
			object.Field("Count", func(number *validate.IntBuilder[int]) {
				number.MultipleOf(-2)
			})
			object.Field("Missing", func(text *validate.StringBuilder[string]) {
			})
		})
	})

	if calls != 1 {
		t.Fatalf("later callbacks = %d, want 1", calls)
	}

	for _, diagnostic := range []string{"Min(-1)", "Max(-2)", "Len(-3)", "MultipleOf(-2)", "Missing:"} {
		if !strings.Contains(message, diagnostic) {
			t.Fatalf("missing %q in aggregate report: %s", diagnostic, message)
		}
	}
}

// TestExportDoesNotExecuteValidationCallbacks separates metadata compilation
// from default verification and runtime preparation, including custom rules.
//
// Example:
//
//	go test -run '^TestExportDoesNotExecuteValidationCallbacks$' .
func TestExportDoesNotExecuteValidationCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Default("Ada")
		text.Normalize(func(value string) string {
			calls++

			return value
		})
		text.Check(func(context.Context, string) error {
			calls++

			return errors.New("runtime only")
		})
	})

	document, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if calls != 0 || string(document) != `{"default":"Ada","type":"string"}` {
		t.Fatalf("metadata executed callbacks: calls=%d, document=%s", calls, document)
	}
}

// TestExportDoesNotRecheckStructDefaults preserves the one construction-time
// default check and ensures metadata export neither repeats it nor normalizes
// the exported default annotation.
//
// Example:
//
//	go test -run '^TestExportDoesNotRecheckStructDefaults$' .
func TestExportDoesNotRecheckStructDefaults(t *testing.T) {
	t.Parallel()

	type form struct {
		// Name receives a default whose callbacks count real execution.
		Name string `json:"name"`
	}

	calls := 0

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(text *validate.StringBuilder[string]) {
			text.Default(" Ada ")
			text.Normalize(func(value string) string {
				calls++

				return strings.TrimSpace(value)
			})
			text.Check(func(context.Context, string) error {
				calls++

				return nil
			})
		})
	})

	before := calls

	document, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if before == 0 || calls != before {
		t.Fatalf("callback counts before=%d after=%d; export repeated default checking", before, calls)
	}

	if !strings.Contains(string(document), `"default":" Ada "`) {
		t.Fatalf("export normalized the default: %s", document)
	}
}

// TestExportRetainedDraftCannotClearErrors checks diagnostics are detached at
// publication rather than depending on the current state of a retained draft.
//
// Example:
//
//	go test -run '^TestExportRetainedDraftCannotClearErrors$' .
func TestExportRetainedDraftCannotClearErrors(t *testing.T) {
	t.Parallel()

	var retained *validate.StringBuilder[string]

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		retained = text
		text.Min(-1)
	})

	*retained = validate.StringBuilder[string]{}
	retained.Min(1)

	requireInvalidExport(t, schema)
}

// TestExportInvalidArgumentsRemainExecutionErrors checks that annotation input
// validation also rejects runtime use instead of weakening only the metadata.
//
// Example:
//
//	go test -run '^TestExportInvalidArgumentsRemainExecutionErrors$' .
func TestExportInvalidArgumentsRemainExecutionErrors(t *testing.T) {
	t.Parallel()

	schema := validate.Int(func(number *validate.IntBuilder[int]) {
		number.MultipleOf(-2).MultipleOf(2)
	})

	requirePanic(t, func() {
		schema.Validate(context.Background(), 4)
	})
	requireInvalidExport(t, schema)
}

// TestExportMetadataRemainsUsableDownstream exercises legal annotations through
// the public FromJSONSchema consumer, not just a textual export comparison.
//
// Example:
//
//	go test -run '^TestExportMetadataRemainsUsableDownstream$' .
func TestExportMetadataRemainsUsableDownstream(t *testing.T) {
	t.Parallel()

	type form struct {
		// Name is constrained by its zero lower length bound and finite maximum.
		Name string `json:"name"`
		// Count accepts negative values while using a positive multiple factor.
		Count int `json:"count"`
	}

	schema := validate.Struct(func(object *validate.StructBuilder[form]) {
		object.Field("Name", func(text *validate.StringBuilder[string]) {
			text.Min(0).Max(3)
		})
		object.Field("Count", func(number *validate.IntBuilder[int]) {
			number.Min(-4).MultipleOf(2)
		})
	})

	document, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	consumer, err := validate.FromJSONSchema(document)

	if err != nil {
		t.Fatal(err)
	}

	if err := consumer.Validate(context.Background(), map[string]any{"name": "Ada", "count": -4}); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Validate(context.Background(), map[string]any{"name": "Ada", "count": -3}); err == nil {
		t.Fatal("exported multipleOf did not reach the downstream consumer")
	}
}

// schemaTier is a named string used in enum rendering tests. The generated
// JSON Schema must retain string values rather than Go type names.
type schemaTier string

// schemaAddress is the nested shipping object in rendering tests. Its JSON
// names differ from the Go field selectors used by registration.
type schemaAddress struct {
	// City holds a nested string whose diagnostic path uses the JSON field name.
	City string `json:"city"`
	// Zip holds the postal value constrained by the nested address schema.
	Zip string `json:"zip_code"`
}

// schemaOrder combines scalar and composite fields for nested JSON Schema
// rendering. It checks required fields, defaults and child annotations in one
// document.
type schemaOrder struct {
	// Email holds the address checked by email rules and JSON-name discovery.
	Email string `json:"email"`
	// Tier holds the named string enum used in the object schema.
	Tier schemaTier `json:"tier"`
	// Notes provides optional or conditionally constrained text.
	Notes *string `json:"notes"`
	// Tags provides slice storage for child rules and copy-isolation checks.
	Tags []string `json:"tags"`
	// Ship holds the nested address rendered under its JSON property name.
	Ship schemaAddress `json:"ship_to"`
}

// wantSchema fails unless the schema renders exactly want and the output
// parses as JSON.
func wantSchema(t *testing.T, schema interface{ JSONSchema() ([]byte, error) }, want string) {
	t.Helper()

	got, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	var parsed any

	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}

	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// TestJSONSchemaString checks how string constraints map to length, pattern,
// format and default keywords.
//
// Example:
//
//	go test -run "^TestJSONSchemaString$" .
func TestJSONSchemaString(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(2)
		value.Max(8)
	}), `{"maxLength":8,"minLength":2,"type":"string"}`)
	wantSchema(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Len(4)
	}), `{"maxLength":4,"minLength":4,"type":"string"}`)
	wantSchema(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Email()
	}), `{"format":"email","type":"string"}`)
	wantSchema(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.URL()
	}), `{"format":"uri","type":"string"}`)
	wantSchema(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.UUID()
	}), `{"format":"uuid","type":"string"}`)
	wantSchema(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Regex(regexp.MustCompile(`^a+$`))
	}), `{"pattern":"^a+$","type":"string"}`)
	wantSchema(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Default("x")
	}), `{"default":"x","type":"string"}`)
}

// TestJSONSchemaEnum checks enum rendering for named strings and numeric
// values.
//
// Example:
//
//	go test -run "^TestJSONSchemaEnum$" .
func TestJSONSchemaEnum(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.String(func(value *validate.StringBuilder[schemaTier]) {
		value.OneOf("free", "pro")
	}), `{"enum":["free","pro"],"type":"string"}`)
	wantSchema(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.OneOf(1, 2)
	}), `{"enum":[1,2],"type":"integer"}`)
}

// TestJSONSchemaNumbers checks inclusive, exclusive and divisibility keywords
// for signed, unsigned and floating-point schemas.
//
// Example:
//
//	go test -run "^TestJSONSchemaNumbers$" .
func TestJSONSchemaNumbers(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Between(1, 5)
		number.MultipleOf(2)
	}), `{"maximum":5,"minimum":1,"multipleOf":2,"type":"integer"}`)
	wantSchema(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Gt(1)
		number.Lt(9)
	}), `{"exclusiveMaximum":9,"exclusiveMinimum":1,"type":"integer"}`)
	wantSchema(t, validate.Int(func(number *validate.IntBuilder[int]) {
		number.Positive()
	}), `{"exclusiveMinimum":0,"type":"integer"}`)
	wantSchema(t, validate.Uint(func(number *validate.UintBuilder[uint]) {
		number.Max(7)
	}), `{"maximum":7,"minimum":0,"type":"integer"}`)
	wantSchema(t, validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Gte(0.5)
		number.Lte(2.5)
	}), `{"maximum":2.5,"minimum":0.5,"type":"number"}`)
}

// TestJSONSchemaNonFiniteLimitFails checks that a non-finite numeric limit
// cannot be rendered as JSON.
//
// Example:
//
//	go test -run "^TestJSONSchemaNonFiniteLimitFails$" .
func TestJSONSchemaNonFiniteLimitFails(t *testing.T) {
	t.Parallel()

	if _, err := validate.Float(func(number *validate.FloatBuilder[float64]) {
		number.Min(math.NaN())
	}).JSONSchema(); err == nil {
		t.Fatal("NaN limit must not render")
	}
}

// TestJSONSchemaBoolTimeFile checks scalar documents for boolean intent, time
// values and uploaded files.
//
// Example:
//
//	go test -run "^TestJSONSchemaBoolTimeFile$" .
func TestJSONSchemaBoolTimeFile(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Bool[bool](), `{"type":"boolean"}`)
	wantSchema(t, validate.Bool(func(value *validate.BoolBuilder[bool]) {
		value.Accepted()
	}), `{"const":true,"type":"boolean"}`)
	wantSchema(t, validate.Time(func(moment *validate.TimeBuilder) {
		moment.After(time.Now())
	}), `{"format":"date-time","type":"string"}`)
	wantSchema(t, validate.File(func(file *validate.FileBuilder) {
		file.Max(10)
	}), `{"format":"binary","type":"string"}`)
}

// TestJSONSchemaSliceMapPtr checks child documents and collection bounds
// across slices, maps and pointers.
//
// Example:
//
//	go test -run "^TestJSONSchemaSliceMapPtr$" .
func TestJSONSchemaSliceMapPtr(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Slice(func(items *validate.SliceBuilder[int]) {
		items.Values(func(number *validate.IntBuilder[int]) {
			number.Min(1)
		})

		items.Min(1)
		items.Max(3)
		items.Unique()
	}),
		`{"items":{"minimum":1,"type":"integer"},"maxItems":3,"minItems":1,"type":"array","uniqueItems":true}`)
	wantSchema(t, validate.Map(func(entries *validate.MapBuilder[string, bool]) {
		entries.Keys(func(value *validate.StringBuilder[string]) {
		})
		entries.Values(func(value *validate.BoolBuilder[bool]) {
		})

		entries.Min(1)
	}),
		`{"additionalProperties":{"type":"boolean"},"minProperties":1,"type":"object"}`)
	wantSchema(t, validate.Ptr[string](validate.String(func(value *validate.StringBuilder[string]) {
		value.Max(3)
	}), func(pointer *validate.PtrBuilder[string]) {
		pointer.Describe("short")
	}),
		`{"description":"short","maxLength":3,"type":"string"}`)
}

// TestJSONSchemaDescribe checks that descriptions coexist with constraint
// annotations.
//
// Example:
//
//	go test -run "^TestJSONSchemaDescribe$" .
func TestJSONSchemaDescribe(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Describe("Contact address")
		value.Email()
	}),
		`{"description":"Contact address","format":"email","type":"string"}`)
}

// TestJSONSchemaNestedStruct checks a complete nested document with defaults,
// required fields and JSON field names.
//
// Example:
//
//	go test -run "^TestJSONSchemaNestedStruct$" .
func TestJSONSchemaNestedStruct(t *testing.T) {
	t.Parallel()

	address := validate.Struct[schemaAddress](func(object *validate.StructBuilder[schemaAddress]) {
		object.Field("City", func(value *validate.StringBuilder[string]) {
			value.Required()
		})
		object.Field("Zip", func(value *validate.StringBuilder[string]) {
			value.Len(5)
		})
	})
	order := validate.Struct[schemaOrder](func(object *validate.StructBuilder[schemaOrder]) {
		object.Field("Email", func(value *validate.StringBuilder[string]) {
			value.Required()
			value.Email()
		})
		object.Field("Tier", func(value *validate.StringBuilder[schemaTier]) {
			value.OneOf("free", "pro")
			value.Default("free")
		})
		object.Field("Notes", func(pointer *validate.PtrBuilder[string]) {
			pointer.Value(func(value *validate.StringBuilder[string]) {
				value.Max(10)
			})
		})
		object.Field("Tags", func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
			})
			items.Describe("Free-form tags")
		})
		object.Field("Ship", func(object *validate.StructBuilder[schemaAddress]) {
			object.Use(address)
			object.Describe("Where to send it")
			object.Required()
		})
	})

	wantSchema(t, order, `{"properties":{`+
		`"email":{"format":"email","type":"string"},`+
		`"notes":{"maxLength":10,"type":"string"},`+
		`"ship_to":{"description":"Where to send it","properties":{`+
		`"city":{"type":"string"},"zip_code":{"maxLength":5,"minLength":5,"type":"string"}},`+
		`"required":["city"],"type":"object"},`+
		`"tags":{"description":"Free-form tags","items":{"type":"string"},"type":"array"},`+
		`"tier":{"default":"free","enum":["free","pro"],"type":"string"}},`+
		`"required":["email","ship_to"],"type":"object"}`)
}

// TestJSONSchemaSkipsInexpressibleRules checks that runtime-only Check
// callbacks do not change the rendered constraint document.
//
// Example:
//
//	go test -run "^TestJSONSchemaSkipsInexpressibleRules$" .
func TestJSONSchemaSkipsInexpressibleRules(t *testing.T) {
	t.Parallel()

	plain := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(2)
	})
	custom := validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(plain)
		value.Check(func(context.Context, string) error {
			return nil
		})
	})

	a, _ := plain.JSONSchema()
	b, _ := custom.JSONSchema()

	if string(a) != string(b) {
		t.Fatalf("Check changed the schema: %s vs %s", a, b)
	}
}

// TestJSONSchemaMissingElementSchemaFails checks invalid-schema reporting for
// an unconstructed composite schema.
//
// Example:
//
//	go test -run "^TestJSONSchemaMissingElementSchemaFails$" .
func TestJSONSchemaMissingElementSchemaFails(t *testing.T) {
	t.Parallel()

	_, err := (validate.SliceSchema[string]{}).JSONSchema()

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("got %v, want ErrInvalidSchema", err)
	}
}
