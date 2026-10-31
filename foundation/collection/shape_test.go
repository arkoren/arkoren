package collection_test

import (
	"encoding/json/v2"
	"reflect"
	"testing"

	"arkoren.dev/foundation/collection"
)

// TestSliceJSONSchemaType matches the native wire type without materializing or
// examining collection contents. A nil collection is a valid provider.
func TestSliceJSONSchemaType(t *testing.T) {
	t.Parallel()

	var items collection.Slice[string]

	if got := items.JSONSchemaType(); got != reflect.TypeFor[[]string]() {
		t.Fatalf("wire type = %v", got)
	}
}

// TestMapJSONSchemaType retains exact key and value identities in schema metadata.
func TestMapJSONSchemaType(t *testing.T) {
	t.Parallel()

	var entries collection.Map[int, string]

	if got := entries.JSONSchemaType(); got != reflect.TypeFor[map[int]string]() {
		t.Fatalf("wire type = %v", got)
	}
}

// TestByteSliceJSONSchemaType preserves the encoding's base64 special case.
// The metadata is native []byte, not an invented generic array representation.
func TestByteSliceJSONSchemaType(t *testing.T) {
	t.Parallel()

	items := collection.NewSlice([]byte{1, 2})

	if got := items.JSONSchemaType(); got != reflect.TypeFor[[]byte]() {
		t.Fatalf("wire type = %v", got)
	}

	data, err := json.Marshal(items)

	if err != nil || string(data) != `"AQI="` {
		t.Fatalf("encoded = %s, %v", data, err)
	}
}
