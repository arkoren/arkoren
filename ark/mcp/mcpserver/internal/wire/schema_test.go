package wire

import (
	"context"
	"encoding/json/jsontext"
	"testing"

	"arkoren.dev/core/mcp"
	"arkoren.dev/core/pagination"
	"arkoren.dev/foundation/collection"
)

func TestSchemaDefaultsAreNotApplied(t *testing.T) {
	t.Parallel()

	schema, err := compileOutput(jsontext.Value(`{
		"type":"object",
		"properties":{"text":{"type":"string","default":"fallback"}},
		"required":["text"]
	}`))

	if err != nil {
		t.Fatal(err)
	}

	if err := validateOutput(t.Context(), schema, jsontext.Value(`{}`)); err == nil {
		t.Fatal("missing required output was filled with its default")
	}
}

func TestExternalSchemaReferencesDoNotLoad(t *testing.T) {
	t.Parallel()

	_, err := compileOutput(jsontext.Value(`{"$ref":"https://example.invalid/schema"}`))

	if err == nil {
		t.Fatal("external schema reference accepted without a loader")
	}
}

// collectionDocument gives inferred page elements a concrete object shape.
type collectionDocument struct {
	// Name is required to remain a string in emitted tool output.
	Name string `json:"name"`
}

// collectionCounts exercises inferred map values inside an object envelope.
type collectionCounts struct {
	// Counts maps application labels to integer observations.
	Counts collection.Map[string, int] `json:"counts"`
}

// collectionBytes exercises the underlying byte slice's base64 JSON encoding.
type collectionBytes struct {
	// Data is a string on the wire, unlike most collection slices.
	Data collection.Slice[byte] `json:"data"`
}

// collectionOctet is a defined byte-like type, not the builtin byte alias.
type collectionOctet uint8

// collectionOctets preserves JSON v2's array encoding for defined byte elements.
type collectionOctets struct {
	// Data is an array on the wire, unlike a collection of builtin bytes.
	Data collection.Slice[collectionOctet] `json:"data"`
}

// TestCollectionPageSchemaValidatesEncodedElements prevents collection-backed
// page items from silently weakening managed tool output validation to {}.
func TestCollectionPageSchemaValidatesEncodedElements(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("documents", func(context.Context, mcp.NoInput) (pagination.Page[collectionDocument], error) {
		return pagination.Paginate([]collectionDocument{{Name: "invoice"}}, 1, 1, 10), nil
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	schema, err := compileOutput(tool.Describe().OutputSchema)

	if err != nil {
		t.Fatal(err)
	}

	output, err := tool.Call(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	if err := validateOutput(t.Context(), schema, output); err != nil {
		t.Fatalf("collection page rejected: %v", err)
	}

	invalid := jsontext.Value(`{"items":[{"name":9}],"total":1,"per_page":10,"current_page":1,"last_page":1}`)

	if err := validateOutput(t.Context(), schema, invalid); err == nil {
		t.Fatal("collection page schema accepted a numeric document name")
	}
}

// TestCollectionMapSchemaValidatesEncodedValues prevents custom collection
// marshaling from erasing the map's value schema in a managed tool result.
func TestCollectionMapSchemaValidatesEncodedValues(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("counts", func(context.Context, mcp.NoInput) (collectionCounts, error) {
		return collectionCounts{Counts: collection.NewMap(map[string]int{"ready": 2})}, nil
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	schema, err := compileOutput(tool.Describe().OutputSchema)

	if err != nil {
		t.Fatal(err)
	}

	output, err := tool.Call(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	if err := validateOutput(t.Context(), schema, output); err != nil {
		t.Fatalf("collection map rejected: %v", err)
	}

	if err := validateOutput(t.Context(), schema, jsontext.Value(`{"counts":{"ready":"two"}}`)); err == nil {
		t.Fatal("collection map schema accepted a string count")
	}
}

// TestCollectionBytesSchemaMatchesEncodedString prevents the schema-shape
// protocol from promising an array for a collection that encodes as base64.
func TestCollectionBytesSchemaMatchesEncodedString(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("bytes", func(context.Context, mcp.NoInput) (collectionBytes, error) {
		return collectionBytes{Data: collection.NewSlice([]byte{0, 1, 2})}, nil
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	schema, err := compileOutput(tool.Describe().OutputSchema)

	if err != nil {
		t.Fatal(err)
	}

	output, err := tool.Call(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	if string(output) != `{"data":"AAEC"}` {
		t.Fatalf("encoded bytes=%s", output)
	}

	if err := validateOutput(t.Context(), schema, output); err != nil {
		t.Fatalf("encoded byte string rejected: %v", err)
	}

	if err := validateOutput(t.Context(), schema, jsontext.Value(`{"data":[0,1,2]}`)); err == nil {
		t.Fatal("byte collection schema accepted an array")
	}
}

// TestCollectionNamedBytesSchemaMatchesEncodedArray keeps schema inference
// aligned with JSON v2's different dispatch for defined byte-like elements.
func TestCollectionNamedBytesSchemaMatchesEncodedArray(t *testing.T) {
	t.Parallel()

	tool := mcp.NewTool("octets", func(context.Context, mcp.NoInput) (collectionOctets, error) {
		return collectionOctets{Data: collection.NewSlice([]collectionOctet{1, 2})}, nil
	})

	if err := tool.Err(); err != nil {
		t.Fatal(err)
	}

	schema, err := compileOutput(tool.Describe().OutputSchema)

	if err != nil {
		t.Fatal(err)
	}

	output, err := tool.Call(t.Context(), nil)

	if err != nil {
		t.Fatal(err)
	}

	if string(output) != `{"data":[1,2]}` {
		t.Fatalf("encoded octets=%s", output)
	}

	if err := validateOutput(t.Context(), schema, output); err != nil {
		t.Fatalf("encoded octet array rejected: %v", err)
	}

	if err := validateOutput(t.Context(), schema, jsontext.Value(`{"data":"AQI="}`)); err == nil {
		t.Fatal("defined-octet collection schema accepted base64 instead of an array")
	}
}
