package encoded_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"arkoren.dev/foundation/validate"
)

func TestEncodedUnsupportedDraftFailsCompilation(t *testing.T) {
	_, err := validate.FromJSONSchemaStrict([]byte(`{"$schema":"https://example.invalid/schema"}`))

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedSchemaNodeCountIsBounded(t *testing.T) {
	var document strings.Builder

	document.WriteString(`{"$defs":{`)

	for index := range 16384 {
		if index > 0 {
			document.WriteByte(',')
		}

		document.WriteString(`"n` + strconv.Itoa(index) + `":true`)
	}

	document.WriteString(`}}`)

	_, err := validate.FromJSONSchemaStrict([]byte(document.String()))

	if !errors.Is(err, validate.ErrEncodedLimit) || !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("compile = %v", err)
	}
}

func TestEncodedSchemaLocationExpansionIsBounded(t *testing.T) {
	document := `{"$id":"https://example.invalid/` + strings.Repeat("a", 16384) + `"}`

	_, err := validate.FromJSONSchemaStrict([]byte(document))

	if !errors.Is(err, validate.ErrEncodedLimit) {
		t.Fatalf("compile = %v", err)
	}
}
