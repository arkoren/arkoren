package validate_test

import (
	"context"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestDurationMetadataSeparatesGoText checks that native Go duration strings
// do not claim JSON Schema's different ISO-8601 duration syntax. A custom
// annotation keeps the Go grammar while avoiding a false standard-format claim.
func TestDurationMetadataSeparatesGoText(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Text[validate.Duration](), `{"format":"go-duration","type":"string"}`)

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Duration()
	})

	wantSchema(t, schema, `{"format":"go-duration","type":"string"}`)

	if _, err := schema.Parse(context.Background(), "1h30m"); err != nil {
		t.Fatal(err)
	}

	requireCode(t, schema.Validate(context.Background(), "PT1H30M"), "string.duration")
}

// TestImportedGoDurationKeepsExportedGrammar checks the full application
// export/import boundary. The custom format must enforce Go duration text,
// rather than silently becoming an unknown-format annotation on import.
func TestImportedGoDurationKeepsExportedGrammar(t *testing.T) {
	t.Parallel()

	source := validate.String(func(text *validate.StringBuilder[string]) {
		text.Duration()
	})

	document, err := source.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if string(document) != `{"format":"go-duration","type":"string"}` {
		t.Fatalf("Go duration schema = %s", document)
	}

	imported, err := validate.FromJSONSchema(document)

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := imported.Parse(context.Background(), "1h30m")

	if err != nil || parsed != "1h30m" {
		t.Fatalf("Go duration Parse = %v, %v", parsed, err)
	}

	requireCode(t, imported.Validate(context.Background(), "PT1H30M"), "string.duration")
}

// TestImportedStandardDurationAcceptsRFC3339Grammar checks the normative
// RFC 3339 Appendix A syntax referenced by JSON Schema 2020-12, not Go's
// time.ParseDuration units or its bounded nanosecond representation.
func TestImportedStandardDurationAcceptsRFC3339Grammar(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"format":"duration"}`)

	for _, input := range []string{
		"PT0S", "P4DT12H30M5S", "P1Y2M3DT4H5M6S", "P1M2D", "PT1H2M", "P2W",
		"P999999999999999999999999999999999999999999D",
	} {
		if _, err := schema.Parse(context.Background(), input); err != nil {
			t.Fatalf("valid standard duration %q rejected: %v", input, err)
		}
	}
}

// TestImportedStandardDurationRejectsOtherGrammars protects the same ABNF's
// component order and adjacency. Go text, fractional components, mixed weeks,
// skipped intermediate units and surrounding whitespace are not that syntax.
func TestImportedStandardDurationRejectsOtherGrammars(t *testing.T) {
	t.Parallel()

	schema := fromDoc(t, `{"format":"duration"}`)

	for _, input := range []string{"1h", "PT0.5S", "P1Y2D", "PT1H2S", "P1WT1H", "PT", "P", "P1D\n"} {
		requireCode(t, schema.Validate(context.Background(), input), "string.duration")
	}
}
