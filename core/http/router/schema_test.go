package router_test

import (
	"bytes"
	"testing"

	"arkoren.dev/core/http/router"
)

// failedSchema supplies a stable rendering failure without execution behavior.
type failedSchema struct {
	// err is returned unchanged for identity assertions.
	err error
}

// JSONSchema returns the injected construction failure.
func (schema failedSchema) JSONSchema() ([]byte, error) {
	return nil, schema.err
}

// TestRawSchemaPreservesArbitraryKeywordsAndCopiesBytes checks the raw escape
// hatch without evaluating vocabularies, references, or arbitrary keywords.
func TestRawSchemaPreservesArbitraryKeywordsAndCopiesBytes(t *testing.T) {
	t.Parallel()

	schema := router.RawSchema(`{"$schema":"https://example.invalid/dialect","$dynamicRef":"#node","custom":{"large":9999999999999999999999999}}`)

	data, err := schema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(data, schema) {
		t.Fatalf("raw JSON changed: %s", data)
	}

	data[0] = '!'

	if schema[0] != '{' {
		t.Fatal("schema output aliases caller input")
	}
}

// TestRawSchemaAcceptsBooleanRoots checks both valid boolean-schema forms.
func TestRawSchemaAcceptsBooleanRoots(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"true", "false"} {
		if _, err := router.RawSchema(input).JSONSchema(); err != nil {
			t.Fatalf("%s: %v", input, err)
		}
	}
}

// TestRawSchemaRejectsInvalidRootsAndSyntax checks the schema root boundary,
// not any JSON Schema keyword's validation semantics.
func TestRawSchemaRejectsInvalidRootsAndSyntax(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "null", "[]", `"string"`, "42", "{", "{} {}"} {
		if _, err := router.RawSchema(input).JSONSchema(); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
