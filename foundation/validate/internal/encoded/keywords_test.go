package encoded_test

import (
	"errors"
	"testing"

	"arkoren.dev/foundation/validate"
)

func TestEncodedUnknownAssertionsFailCompilation(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"minLenght":2}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedExtensionsAreExplicitAnnotations(t *testing.T) {
	check, err := validate.FromJSONSchemaStrict([]byte(`{"x-owner":{"private":true},"type":"null"}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := check.Validate(t.Context(), []byte(`null`)); err != nil {
		t.Fatal(err)
	}
}

func TestEncodedMalformedTypeFailsCompilation(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"type":["integer","unknown"]}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedRequiredFormatAssertionVocabularyFails(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$vocabulary":{"https://json-schema.org/draft/2020-12/vocab/format-assertion":true}}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedKnownVocabularyCompiles(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$vocabulary":{"https://json-schema.org/draft/2020-12/vocab/validation":true},"type":"integer"}`))

	if err != nil {
		t.Fatal(err)
	}
}

func TestEncodedVocabularyRequiresExplicitDialect(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"$vocabulary":{}}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedIgnoredContentSchemaStillChecksKeywords(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"contentSchema":{"unknownAssertion":true}}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}
