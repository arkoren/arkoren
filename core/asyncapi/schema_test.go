package asyncapi_test

import (
	"strings"
	"testing"

	"arkoren.dev/core/asyncapi"
)

func TestRawSchemaPreservesPrecisionAndWhitespace(t *testing.T) {
	t.Parallel()

	source := ` {"const":90071992547409931234567890,"minimum":9007199254740993} `

	data, err := asyncapi.RawSchema(source).JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != source {
		t.Fatalf("schema changed: %s", data)
	}
}

func TestRawSchemaCopiesBytes(t *testing.T) {
	t.Parallel()

	source := asyncapi.RawSchema(`{"type":"string"}`)

	data, err := source.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	source[0] = '['

	if string(data) != `{"type":"string"}` {
		t.Fatalf("result aliases source: %s", data)
	}
}

func TestRawSchemaAcceptsBooleans(t *testing.T) {
	t.Parallel()

	for _, source := range []string{`true`, `false`, `{"properties":{"a":false,"b":true}}`} {
		if _, err := asyncapi.RawSchema(source).JSONSchema(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRawSchemaRejectsMalformedRoots(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"", " ", "null", "[]", `"string"`, "1", "{", "{} {}", `{"type":"string","type":"number"}`} {
		if _, err := asyncapi.RawSchema(source).JSONSchema(); err == nil {
			t.Fatalf("accepted invalid root %q", source)
		}
	}

	if _, err := asyncapi.RawSchema(nil).JSONSchema(); err == nil {
		t.Fatal("accepted nil schema")
	}
}

func TestRawSchemaTreatsDataAndPropertyNamesAsOpaque(t *testing.T) {
	t.Parallel()

	source := asyncapi.RawSchema(`{
		"properties":{"$defs":{"type":"string"},"$ref":true,"prefixItems":false},
		"required":["$defs"],
		"const":{"$ref":"https://example.com"},
		"default":{"$defs":{},"prefixItems":[]},
		"enum":[{"$schema":"other"}],
		"examples":[{"$id":"opaque"}],
		"x-extra":{"$ref":"opaque"}
	}`)

	if _, err := source.JSONSchema(); err != nil {
		t.Fatal(err)
	}
}

func TestRawSchemaRejectsReferencesEvenWithSiblings(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`{"$ref":"#/properties/a","type":"string"}`,
		`{"$id":"urn:example:a"}`,
		`{"$defs":{}}`,
		`{"definitions":{}}`,
	} {
		_, err := asyncapi.RawSchema(source).JSONSchema()

		if err == nil || !strings.Contains(err.Error(), "unsupported keyword") {
			t.Fatalf("expected actionable unsupported keyword error, got %v", err)
		}
	}
}

func TestRawSchemaRejectsNestedNewerDialect(t *testing.T) {
	t.Parallel()

	_, err := asyncapi.RawSchema(`{"properties":{"a/b~c":{"prefixItems":[]}}}`).JSONSchema()

	if err == nil || !strings.Contains(err.Error(), "/properties/a~1b~0c/prefixItems") {
		t.Fatalf("expected escaped keyword pointer, got %v", err)
	}
}

func TestRawSchemaAcceptsExplicitDraft7(t *testing.T) {
	t.Parallel()

	_, err := asyncapi.RawSchema(`{"$schema":"http://json-schema.org/draft-07/schema#"}`).JSONSchema()

	if err != nil {
		t.Fatal(err)
	}
}

func TestRawSchemaRejectsConflictingDialect(t *testing.T) {
	t.Parallel()

	_, err := asyncapi.RawSchema(`{"items":{"$schema":"https://json-schema.org/draft/2020-12/schema"}}`).JSONSchema()

	if err == nil || !strings.Contains(err.Error(), "/items/$schema") {
		t.Fatalf("expected nested dialect error, got %v", err)
	}
}

func TestRawSchemaSupportsDraft7CompositionAndTupleItems(t *testing.T) {
	t.Parallel()

	source := asyncapi.RawSchema(`{
		"allOf":[{"type":["array","null"]},true],
		"anyOf":[false,{}],"oneOf":[{}],"not":false,
		"if":{"type":"array"},"then":{"minItems":1},"else":true,
		"items":[{"type":"string"},false],"additionalItems":false,
		"contains":{"const":1},"propertyNames":{"pattern":"a"},
		"patternProperties":{"^x":true},"additionalProperties":false,
		"required":[]
	}`)

	if _, err := source.JSONSchema(); err != nil {
		t.Fatal(err)
	}
}

func TestRawSchemaRejectsInvalidKeywordShapes(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`{"properties":[]}`, `{"properties":{"a":null}}`,
		`{"allOf":[]}`, `{"anyOf":true}`, `{"items":[]}`,
		`{"type":[]}`, `{"type":["string","string"]}`, `{"type":"date"}`,
		`{"type":[null]}`, `{"required":[null]}`, `{"required":["a","a"]}`,
		`{"minLength":-1}`, `{"maxItems":1.5}`, `{"minimum":"1"}`,
		`{"multipleOf":0}`, `{"exclusiveMinimum":true}`, `{"enum":[]}`,
		`{"enum":{}}`, `{"examples":{}}`, `{"readOnly":1}`, `{"format":null}`,
	} {
		if _, err := asyncapi.RawSchema(source).JSONSchema(); err == nil {
			t.Fatalf("accepted invalid keyword shape %s", source)
		}
	}
}

func TestRawSchemaAcceptsAnnotationsAndNumericShapes(t *testing.T) {
	t.Parallel()

	source := asyncapi.RawSchema(`{
		"title":"sample","description":"payload","$comment":"notes",
		"format":"custom-format","pattern":"(?not-transpiled)",
		"readOnly":true,"writeOnly":false,"examples":[],
		"contentEncoding":"base64","contentMediaType":"text/plain",
		"minLength":0,"maxLength":1e2,"minimum":-1.5,"maximum":2,
		"exclusiveMinimum":0,"exclusiveMaximum":10,"multipleOf":0.1,
		"minItems":0,"maxItems":2,"uniqueItems":true,
		"minProperties":0,"maxProperties":3
	}`)

	if _, err := source.JSONSchema(); err != nil {
		t.Fatal(err)
	}
}

func TestRawSchemaRejectsUnknownKeywords(t *testing.T) {
	t.Parallel()

	for _, keyword := range []string{"contentSchema", "unevaluatedProperties", "dependentSchemas", "unknown"} {
		_, err := asyncapi.RawSchema(`{"` + keyword + `":true}`).JSONSchema()

		if err == nil || !strings.Contains(err.Error(), "/"+keyword) {
			t.Fatalf("expected unsupported keyword pointer for %s, got %v", keyword, err)
		}
	}
}

func TestRawSchemaChecksEveryRecursivePosition(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`{"patternProperties":{"a":{"$ref":"x"}}}`,
		`{"additionalProperties":{"$ref":"x"}}`,
		`{"additionalItems":{"$ref":"x"}}`,
		`{"contains":{"$ref":"x"}}`,
		`{"propertyNames":{"$ref":"x"}}`,
		`{"not":{"$ref":"x"}}`,
		`{"if":{"$ref":"x"}}`,
		`{"then":{"$ref":"x"}}`,
		`{"else":{"$ref":"x"}}`,
		`{"allOf":[{"$ref":"x"}]}`,
		`{"anyOf":[{"$ref":"x"}]}`,
		`{"oneOf":[{"$ref":"x"}]}`,
		`{"items":[{"$ref":"x"}]}`,
	} {
		_, err := asyncapi.RawSchema(source).JSONSchema()

		if err == nil || !strings.Contains(err.Error(), "/$ref") {
			t.Fatalf("expected nested reference rejection for %s, got %v", source, err)
		}
	}
}
