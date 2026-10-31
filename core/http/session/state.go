package session

import "encoding/json/jsontext"

// persistedValue marks bytes decoded from a stored record, not bytes supplied
// through New or Put. Only this private provenance permits typed JSON decoding;
// callers cannot impersonate persisted state with a jsontext.Value. All unwraps
// the marker before public snapshots or serialization, keeping JSON unchanged.
type persistedValue jsontext.Value

// runtimeValue reads an exact in-memory Go value without a JSON conversion.
// A nil interface has no dynamic type for a type assertion; it is valid only
// when T is itself an interface. Only an interface's zero T boxes to nil;
// concrete nil pointers, slices, and functions keep their concrete dynamic type.
// Missing fields are handled by Get before this step, not represented by nil.
func runtimeValue[T any](value any) (T, error) {
	var zero T

	if value == nil && any(zero) == nil {
		return zero, nil
	}

	result, ok := value.(T)

	if !ok {
		return zero, ErrInvalidValueType
	}

	return result, nil
}
