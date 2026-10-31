package validate

import (
	"encoding/binary"
	"reflect"

	"arkoren.dev/foundation/validate/internal/numbers"
)

// appendJSONKey appends an unambiguous structural encoding of v to key.
// Type tags, lengths, and sorted object keys make equality of full keys exact,
// so Go's string map resolves hash collisions without a pairwise fallback.
// Numbers use canonical exact rational keys at every depth, so equivalent JSON
// spellings and supported Go numeric types compare equally without rounding.
// Signed zero is normalized. Nonfinite numbers and non-JSON Go values record
// a type violation at the containing item; cyclic input is outside the API.
// A false result means a violation or cancellation stopped key construction.
func appendJSONKey(st *state, key []byte, v any) ([]byte, bool) {
	if st.stopped() {
		return nil, false
	}

	if number, ok := numbers.Exact(v); ok {
		return appendJSONText(st, append(key, 'd'), number.RatString()), st.fatal == nil
	}

	switch value := v.(type) {
	case nil:
		return append(key, 'n'), true
	case bool:
		if value {
			return append(key, 't'), true
		}

		return append(key, 'f'), true
	case string:
		return appendJSONText(st, append(key, 's'), value), st.fatal == nil
	case []any:
		return appendJSONArrayKey(st, key, value)
	case map[string]any:
		return appendJSONObjectKey(st, key, value)
	}

	st.violate("type", map[string]any{"expected": "finite decoded JSON value"})

	return nil, false
}

// appendJSONArrayKey preserves nil, empty and ordered array identities while
// delegating each item to the same exact scalar/container key representation.
// Cancellation abandons the partial key; caller-owned items are never changed.
func appendJSONArrayKey(st *state, key []byte, value []any) ([]byte, bool) {
	if value == nil {
		return append(key, 'a', '0'), true
	}

	key = binary.AppendUvarint(append(key, 'a', '1'), uint64(len(value)))

	for _, item := range value {
		var ok bool

		key, ok = appendJSONKey(st, key, item)

		if !ok {
			return nil, false
		}
	}

	return key, true
}

// appendJSONObjectKey sorts member names so object identity does not depend on
// map iteration order. Names and child values are length-delimited, and nil
// remains different from empty. Cancellation never publishes a partial key.
func appendJSONObjectKey(st *state, key []byte, value map[string]any) ([]byte, bool) {
	if value == nil {
		return append(key, 'o', '0'), true
	}

	key = binary.AppendUvarint(append(key, 'o', '1'), uint64(len(value)))

	for _, name := range jsonObjectKeys(st, value) {
		key = appendJSONText(st, key, name)
		var ok bool

		key, ok = appendJSONKey(st, key, value[name])

		if !ok {
			return nil, false
		}
	}

	return key, st.fatal == nil
}

// appendJSONText appends a byte-length-prefixed string, checking cancellation
// between bounded copies. This handles both string values and object names
// without allowing one very large string to hide a long encoding loop.
func appendJSONText(st *state, key []byte, text string) []byte {
	// chunkSize limits each uncancelable append, not the accepted string size.
	const chunkSize = 4096

	key = binary.AppendUvarint(key, uint64(len(text)))

	for len(text) > 0 {
		if st.stopped() {
			return nil
		}

		count := min(len(text), chunkSize)
		key = append(key, text[:count]...)
		text = text[count:]
	}

	return key
}

// jsonObjectKeys returns sorted keys without modifying object. Both collection
// and merging check cancellation, unlike an uninterrupted library sort of a
// potentially large object. Bottom-up merges use O(k) extra memory and
// O(k log k) string comparisons; cancellation returns nil, setting st.fatal.
// Individual string comparisons and allocations are not preempted.
func jsonObjectKeys(st *state, object map[string]any) []string {
	keys := make([]string, 0, len(object))

	for key := range object {
		if st.stopped() {
			return nil
		}

		keys = append(keys, key)
	}

	if len(keys) < 2 {
		return keys
	}

	buffer := make([]string, len(keys))

	for width := 1; width < len(keys); width *= 2 {
		if !mergeJSONKeyRuns(st, buffer, keys, width) {
			return nil
		}

		keys, buffer = buffer, keys
	}

	return keys
}

// mergeJSONKeyRuns completes one bottom-up merge-sort pass over object names.
// Each adjacent pair of width-sized sorted runs fills the same output window;
// the final run may be shorter. False abandons a canceled pass before reuse.
func mergeJSONKeyRuns(st *state, output, keys []string, width int) bool {
	for start := 0; start < len(keys); start += 2 * width {
		middle := min(start+width, len(keys))
		end := min(start+2*width, len(keys))

		if !mergeJSONKeys(st, output[start:end], keys[start:middle], keys[middle:end]) {
			return false
		}
	}

	return true
}

// mergeJSONKeys merges two sorted name runs into the supplied output window.
// The output must have their combined length and must not overlap either run.
// Checking state for each name keeps large-object sorting cancelable.
func mergeJSONKeys(st *state, output, left, right []string) bool {
	for index := range output {
		if st.stopped() {
			return false
		}

		if len(left) > 0 && (len(right) == 0 || left[0] <= right[0]) {
			output[index], left = left[0], left[1:]
			continue
		}

		output[index], right = right[0], right[1:]
	}

	return true
}

// jsonEqual compares decoded values with cancellation between child values.
// Supported numbers compare by exact decimal value at every depth; nil
// containers still differ from empty ones. Container identity shortcuts avoid
// repeated traversal of identical storage without changing input ownership.
// Unknown Go types retain reflect.DeepEqual's behavior, but its work cannot be
// interrupted; only acyclic decoded JSON is the supported traversal domain.
func jsonEqual(st *state, left, right any) bool {
	if st.stopped() {
		return false
	}

	leftNumber, leftNumeric := numbers.Exact(left)
	rightNumber, rightNumeric := numbers.Exact(right)

	if leftNumeric || rightNumeric {
		return leftNumeric && rightNumeric && leftNumber.Cmp(rightNumber) == 0
	}

	switch value := left.(type) {
	case []any:
		return jsonEqualArray(st, value, right)
	case map[string]any:
		return jsonEqualObject(st, value, right)
	default:
		return reflect.DeepEqual(left, right)
	}
}

// jsonEqualArray compares shape before traversing ordered child values. Shared
// storage is already equal; separate arrays use exact recursive JSON equality.
// Nil and allocated empty arrays retain their application-level distinction.
func jsonEqualArray(st *state, value []any, right any) bool {
	other, ok := right.([]any)

	if !ok || len(value) != len(other) {
		return false
	}

	if (value == nil) != (other == nil) {
		return false
	}

	if reflect.ValueOf(value).Pointer() == reflect.ValueOf(other).Pointer() {
		return true
	}

	for index, item := range value {
		if !jsonEqual(st, item, other[index]) {
			return false
		}
	}

	return true
}

// jsonEqualObject checks the same member set and then compares child values.
// Map traversal order cannot change equality; child comparisons preserve
// cancellation and exact numbers without converting caller-owned values.
func jsonEqualObject(st *state, value map[string]any, right any) bool {
	other, ok := right.(map[string]any)

	if !ok || len(value) != len(other) {
		return false
	}

	if (value == nil) != (other == nil) {
		return false
	}

	if reflect.ValueOf(value).UnsafePointer() == reflect.ValueOf(other).UnsafePointer() {
		return true
	}

	for key, item := range value {
		candidate, present := other[key]

		if !present || !jsonEqual(st, item, candidate) {
			return false
		}
	}

	return true
}
