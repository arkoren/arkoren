package validate_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/foundation/validate"
)

// shapeSlice models a native collection's explicit schema protocol without an
// import between foundation modules. Its marshaler deliberately remains opaque
// to reflection unless the protocol is consulted first.
type shapeSlice[T any] []T

// JSONSchemaType declares that JSON methods preserve the native container shape.
// No receiver data is needed when describing a Go type.
func (shapeSlice[T]) JSONSchemaType() reflect.Type {
	return reflect.TypeFor[[]T]()
}

// MarshalJSON satisfies the custom marshaler guard for protocol regression tests.
// Inference must never invoke this method to discover a type's shape.
func (shapeSlice[T]) MarshalJSON() ([]byte, error) {
	panic("inference invoked MarshalJSON")
}

// shapeMap supplies the equivalent map declaration through a pointer receiver.
// This checks the zero, non-nil receiver contract of inference.
type shapeMap[K comparable, V any] map[K]V

// JSONSchemaType declares the exact native map type and rejects nil receivers.
// Pointer-only protocol methods must work for both value and pointer inference.
func (mapping *shapeMap[K, V]) JSONSchemaType() reflect.Type {
	if mapping == nil {
		panic("nil provider receiver")
	}

	return reflect.TypeFor[map[K]V]()
}

// MarshalJSON marks maps as custom JSON values without providing runtime data.
func (shapeMap[K, V]) MarshalJSON() ([]byte, error) {
	panic("inference invoked MarshalJSON")
}

// shapeNode forces recursion to pass through both an explicit provider and its
// underlying native slice before returning to the original struct type.
type shapeNode struct {
	// Children recursively refer to the same node type.
	Children shapeSlice[shapeNode] `json:"children"`
}

// opaqueSlice must remain unconstrained despite its apparently slice-like kind.
type opaqueSlice []int

// MarshalJSON emits a scalar, demonstrating why kind-only inference is unsafe.
func (opaqueSlice) MarshalJSON() ([]byte, error) {
	return []byte(`"not an array"`), nil
}

// badShape declares a different element type, which must be a schema error.
type badShape []int

// JSONSchemaType intentionally violates the identical-container requirement.
func (badShape) JSONSchemaType() reflect.Type {
	return reflect.TypeFor[[]string]()
}

// nilShape models an incomplete protocol implementation.
type nilShape []int

// JSONSchemaType intentionally returns no wire type.
func (nilShape) JSONSchemaType() reflect.Type {
	return nil
}

// panicShape models a malformed receiver-dependent provider.
type panicShape []int

// JSONSchemaType panics to check conversion into an ordinary inference error.
func (panicShape) JSONSchemaType() reflect.Type {
	panic("broken shape")
}

// selfShape attempts to redirect inference to itself rather than a native type.
type selfShape []int

// JSONSchemaType intentionally returns a named container and must be rejected.
func (selfShape) JSONSchemaType() reflect.Type {
	return reflect.TypeFor[selfShape]()
}

// TestInferProvidedSliceShape preserves nested element metadata through a custom
// marshaler without importing a collection implementation.
func TestInferProvidedSliceShape(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[shapeSlice[shapeMap[string, int]]](),
		`{"items":{"additionalProperties":{"type":"integer"},"type":"object"},"type":"array"}`)
}

// TestInferProvidedByteShape preserves native base64 behavior rather than
// inventing an array of numbers for byte-valued collections.
func TestInferProvidedByteShape(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[*shapeSlice[byte]](), `{"type":"string"}`)
}

// shapeOctet is a defined uint8 element, which JSON-v2 treats as an array
// element rather than builtin byte storage.
type shapeOctet uint8

// encodedShapeOctets models a collection that delegates encoding to its native
// container. Keeping it local avoids imports between foundation modules.
type encodedShapeOctets []shapeOctet

// JSONSchemaType declares the native container whose JSON representation this
// type preserves. It depends only on the element type, not receiver data.
func (encodedShapeOctets) JSONSchemaType() reflect.Type {
	return reflect.TypeFor[[]shapeOctet]()
}

// MarshalJSON delegates to JSON-v2 using the exact declared native slice.
// Unlike shapeSlice's guard, this fixture also supports encoded-instance tests.
func (values encodedShapeOctets) MarshalJSON() ([]byte, error) {
	return json.Marshal([]shapeOctet(values))
}

// TestInferProvidedNamedByteShape checks the structural provider against actual
// encoding as well as exact schema keywords, so an opaque fallback cannot hide
// an incorrect base64 assumption.
func TestInferProvidedNamedByteShape(t *testing.T) {
	t.Parallel()

	schema := validate.Infer[encodedShapeOctets]()

	wantSchema(t, schema, `{"items":{"minimum":0,"type":"integer"},"type":"array"}`)
	requireInferredEncoding(t, schema, encodedShapeOctets{1, 2})
}

// TestInferProvidedNestedStringifiedNumber preserves field-local numeric
// stringification inside a collection's inferred element DTO.
func TestInferProvidedNestedStringifiedNumber(t *testing.T) {
	t.Parallel()

	// result exposes a stringified integer through a native-shape provider.
	type result struct {
		// Count is encoded as a JSON string rather than a numeric instance.
		Count int `json:"count,string"`
	}

	wantSchema(t, validate.Infer[shapeSlice[result]](),
		`{"items":{"properties":{"count":{"type":"string"}},"required":["count"],"type":"object"},"type":"array"}`)
	requireInferredEncoding(t, validate.Infer[[]result](), []result{{Count: 7}})
}

// TestInferProvidedShapeRecursion truncates repeated types and does not loop
// when an element points back through a provider to its enclosing DTO.
func TestInferProvidedShapeRecursion(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[shapeNode](),
		`{"properties":{"children":{"items":{},"type":"array"}},"required":["children"],"type":"object"}`)
}

// TestInferOpaqueSliceRetainsCustomMarshalerGuard proves opt-in is required.
func TestInferOpaqueSliceRetainsCustomMarshalerGuard(t *testing.T) {
	t.Parallel()

	wantSchema(t, validate.Infer[opaqueSlice](), `{}`)
}

// TestInferProvidedShapeMismatch rejects declarations with different elements.
func TestInferProvidedShapeMismatch(t *testing.T) {
	t.Parallel()

	err := validate.Infer[badShape]().Err()

	if !errors.Is(err, validate.ErrInvalidSchema) || !strings.Contains(err.Error(), "identical unnamed") {
		t.Fatalf("mismatched provider error = %v", err)
	}
}

// TestInferProvidedShapeNil rejects missing wire types at every execution entry.
func TestInferProvidedShapeNil(t *testing.T) {
	t.Parallel()

	schema := validate.Infer[nilShape]()

	_, err := schema.JSONSchema()

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("export error = %v", err)
	}

	_, err = schema.Parse(context.Background(), nil)

	if !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("parse error = %v", err)
	}
}

// TestInferProvidedShapePanic converts malformed providers to contextual errors.
func TestInferProvidedShapePanic(t *testing.T) {
	t.Parallel()

	type Result struct {
		// Items locates the invalid provider in the construction diagnostic.
		Items panicShape
	}

	err := validate.Infer[Result]().Err()

	if !errors.Is(err, validate.ErrInvalidSchema) || !strings.Contains(err.Error(), `field "Items"`) {
		t.Fatalf("provider panic error = %v", err)
	}
}

// TestInferProvidedShapeSelf rejects self-reference rather than hiding it as an
// unconstrained recursion leaf.
func TestInferProvidedShapeSelf(t *testing.T) {
	t.Parallel()

	if err := validate.Infer[selfShape]().Err(); !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("self provider error = %v", err)
	}
}

// TestInferProvidedShapeInvalidMapKey still rejects native unsupported keys.
func TestInferProvidedShapeInvalidMapKey(t *testing.T) {
	t.Parallel()

	if err := validate.Infer[shapeMap[bool, int]]().Err(); !errors.Is(err, validate.ErrInvalidSchema) {
		t.Fatalf("invalid map key error = %v", err)
	}
}
