package validate

import (
	"fmt"
	"reflect"
)

// JSONTypeProvider explicitly declares that a named slice or map uses the JSON
// shape of its identical unnamed container type, despite custom JSON methods.
// Infer recognizes the protocol structurally; implementations need not import
// validate. Arbitrary custom marshalers without this opt-in remain opaque.
//
// Example:
//
//	var provider validate.JSONTypeProvider = collection.Slice[string](nil)
//	fmt.Println(provider.JSONSchemaType() == reflect.TypeFor[[]string]()) // true
type JSONTypeProvider interface {
	// JSONSchemaType returns the identical unnamed slice or map type. Infer
	// invokes it on a fresh non-nil pointer to the zero value, before inspecting
	// custom JSON or text marshalers. The method must be deterministic, pure,
	// and independent of receiver contents. A nil result, mismatched type, or
	// panic becomes an error wrapping ErrInvalidSchema. Native byte slices
	// describe base64 strings. Encoding options and nil formatting are not modeled.
	//
	// Example:
	//
	//	var provider validate.JSONTypeProvider = collection.Slice[int](nil)
	//	fmt.Println(provider.JSONSchemaType() == reflect.TypeFor[[]int]()) // true
	JSONSchemaType() reflect.Type
}

// inferProvidedShape checks an explicit wire-shape declaration before the
// custom-marshaler guard. Requiring the exact native container prevents a
// malformed declaration from causing type redirection or provider cycles.
func inferProvidedShape(typ reflect.Type, path map[reflect.Type]bool) (doc map[string]any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			doc = nil
			err = fmt.Errorf("JSONSchemaType for %s panicked: %v", typ, recovered)
		}
	}()

	provider := reflect.New(typ).Interface().(JSONTypeProvider)
	wire := provider.JSONSchemaType()

	if wire == nil {
		return nil, fmt.Errorf("JSONSchemaType for %s returned nil", typ)
	}

	if typ.Kind() != reflect.Slice && typ.Kind() != reflect.Map {
		return nil, fmt.Errorf("JSONSchemaType requires a slice or map, got %s", typ)
	}

	if wire.Name() != "" || !reflect.PointerTo(typ).ConvertibleTo(reflect.PointerTo(wire)) {
		return nil, fmt.Errorf("JSONSchemaType for %s must return its identical unnamed container type, got %s", typ, wire)
	}

	return inferDoc(wire, path)
}
