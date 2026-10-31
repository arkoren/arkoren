package validate

import (
	"maps"
	"math/big"
	"slices"
	"unicode/utf8"

	"arkoren.dev/foundation/validate/internal/numbers"
)

// run checks v against the node, recording violations in st, and returns
// v with defaults applied. Containers are copied; scalars are shared.
// Cancellation and oversized arrays return before content checks or copies.
func (jsonNode *jsonNode) run(st *state, v any) any {
	if st.stopped() {
		return nil
	}

	if v == nil {
		v = jsonNode.doc["default"]

		if v == nil {
			return nil
		}
	}

	if !jsonNode.admitValue(st, v) {
		return v
	}

	jsonNode.runChoices(st, v)

	if st.stopped() {
		return nil
	}

	return jsonNode.runValue(st, v)
}

// admitValue checks type and array size before content work or copying. A type
// failure and an oversized array retain their existing single-error precedence;
// sibling values can still run. False prevents all further work for this value.
func (jsonNode *jsonNode) admitValue(st *state, v any) bool {
	if want, ok := jsonNode.doc["type"].(string); ok && !jsonTypeMatches(want, v) {
		st.violate("type", map[string]any{"expected": want})

		return false
	}

	if items, ok := v.([]any); ok {
		if limit, ok := numbers.Exact(jsonNode.doc["maxItems"]); ok && new(big.Rat).SetInt64(int64(len(items))).Cmp(limit) > 0 {
			st.violate("slice.max", map[string]any{"max": numbers.Public(jsonNode.doc["maxItems"])})

			return false
		}
	}

	return true
}

// runChoices applies enum and const using exact recursive numeric equality.
// Diagnostics own copied literal containers; cancellation stops choice searches
// instead of producing a misleading mismatch after comparison was interrupted.
func (jsonNode *jsonNode) runChoices(st *state, v any) {
	if values, ok := jsonNode.doc["enum"].([]any); ok && !slices.ContainsFunc(values, func(allowed any) bool {
		// A canceled comparison also stops the search; Parse returns its error.
		return jsonEqual(st, allowed, v) || st.fatal != nil
	}) {
		st.violate("one_of", map[string]any{"values": cloneJSON(st, values)})
	}

	if constant, ok := jsonNode.doc["const"]; ok && !jsonEqual(st, constant, v) && st.fatal == nil {
		st.violate("one_of", map[string]any{"values": cloneJSON(st, []any{constant})})
	}
}

// runValue dispatches kind-specific keywords only after absence, admission and
// choice checks. Concrete supplied numbers keep their public representation;
// schema-owned exact numbers are materialized without losing decimal digits.
func (jsonNode *jsonNode) runValue(st *state, v any) any {
	switch value := v.(type) {
	case string:
		jsonNode.runString(st, value)
	case []any:
		return jsonNode.runArray(st, value)
	case map[string]any:
		return jsonNode.runObject(st, value)
	default:
		if _, ok := numbers.Exact(v); ok {
			jsonNode.checkLimits(st, numbers.Numbers, v)
		}
	}

	return numbers.Public(v)
}

// checkLimits records a violation for each exact numeric constraint that value
// breaks. Callers supply a supported finite number or a nonnegative Go count;
// compiled limits are immutable rationals and operands are never modified.
func (jsonNode *jsonNode) checkLimits(st *state, checks []numbers.Limit, value any) {
	number, _ := numbers.Exact(value)

	for _, check := range checks {
		if limit, ok := numbers.Exact(jsonNode.doc[check.Keyword]); ok && check.Fails(number, limit) {
			st.violate(check.Code, map[string]any{check.Param: numbers.Public(jsonNode.doc[check.Keyword])})
		}
	}
}

// runString applies the string keywords.
func (jsonNode *jsonNode) runString(st *state, value string) {
	jsonNode.checkLimits(st, numbers.Strings, utf8.RuneCountInString(value))

	if jsonNode.pattern != nil && !jsonNode.pattern.MatchString(value) {
		st.violate("string.regex", map[string]any{"pattern": jsonNode.pattern.String()})
	}

	name, _ := jsonNode.doc["format"].(string)

	if check, ok := formatChecks[name]; ok && !check.valid(value) {
		st.violate(check.code, maps.Clone(check.params))
	}
}

// runArray applies uniqueness and item rules after run has admitted the
// array's size. Full structural keys, rather than pairwise comparisons or
// unchecked digests, keep distinct decoded items from causing quadratic work.
// Keys describe original items, before defaults, as in the previous checks.
func (jsonNode *jsonNode) runArray(st *state, items []any) any {
	jsonNode.checkLimits(st, numbers.Arrays, len(items))

	if unique, _ := jsonNode.doc["uniqueItems"].(bool); unique {
		seen := make(map[string]struct{}, len(items))
		// key is scratch space; each stored string owns its bytes independently.
		var key []byte

		for index, item := range items {
			st.pushIndex(index)
			encoded, ok := appendJSONKey(st, key[:0], item)
			st.pop()

			if !ok {
				return nil
			}

			key = encoded
			encodedKey := string(key)

			if _, duplicate := seen[encodedKey]; duplicate {
				st.violate("slice.unique", nil)

				break
			}

			seen[encodedKey] = struct{}{}
		}
	}

	parsed := make([]any, len(items))

	for index, item := range items {
		st.pushIndex(index)

		if jsonNode.items != nil {
			parsed[index] = jsonNode.items.run(st, item)
		} else {
			parsed[index] = cloneJSON(st, item)
		}

		st.pop()

		if st.fatal != nil {
			return nil
		}
	}

	return parsed
}

// runObject applies the object keywords, checks every property and fills
// the defaults of missing ones. Key collection, sorting, and each traversal
// observe cancellation without changing the sorted order of violations.
func (jsonNode *jsonNode) runObject(st *state, object map[string]any) any {
	jsonNode.checkLimits(st, numbers.Objects, len(object))
	jsonNode.checkRequired(st, object)

	parsed := make(map[string]any, len(object))

	for _, key := range jsonObjectKeys(st, object) {
		child := jsonNode.properties[key]

		if child == nil {
			child = jsonNode.additional
		}

		st.push(key)

		if child != nil {
			parsed[key] = child.run(st, object[key])
		} else {
			parsed[key] = cloneJSON(st, object[key])
		}

		st.pop()

		if st.fatal != nil {
			return nil
		}
	}

	for key, child := range jsonNode.properties {
		if st.stopped() {
			return nil
		}

		if _, present := object[key]; present {
			continue
		}

		if def, ok := child.doc["default"]; ok {
			st.push(key)
			parsed[key] = child.run(st, def)
			st.pop()
		}
	}

	return parsed
}

// checkRequired reports missing object keys before defaults are inserted. Null
// still counts as a present key in the application importer. Pushing a segment
// explicitly preserves the legal empty property name as the child pointer /.
func (jsonNode *jsonNode) checkRequired(st *state, object map[string]any) {
	required, _ := jsonNode.doc["required"].([]any)

	for _, name := range required {
		if st.stopped() {
			return
		}

		key, _ := name.(string)

		if _, present := object[key]; !present {
			st.push(key)
			st.violate("required", nil)
			st.pop()
		}
	}
}

// jsonTypeMatches reports whether v has the JSON Schema type want.
func jsonTypeMatches(want string, v any) bool {
	number, isNumber := numbers.Exact(v)

	switch want {
	case "integer":
		return isNumber && number.IsInt()
	case "number":
		return isNumber
	case "string":
		_, ok := v.(string)

		return ok
	case "boolean":
		_, ok := v.(bool)

		return ok
	case "object":
		_, ok := v.(map[string]any)

		return ok
	default:
		_, ok := v.([]any)

		return ok
	}
}

// cloneJSON deep-copies a decoded value, so that defaults and results
// never alias the schema or the input. Cancellation abandons partial copies;
// the caller returns st.fatal instead of exposing them to the application.
func cloneJSON(st *state, v any) any {
	if st.stopped() {
		return nil
	}

	switch value := v.(type) {
	case []any:
		cloned := make([]any, len(value))

		for index, item := range value {
			cloned[index] = cloneJSON(st, item)

			if st.fatal != nil {
				return nil
			}
		}

		return cloned
	case map[string]any:
		cloned := make(map[string]any, len(value))

		for key, item := range value {
			cloned[key] = cloneJSON(st, item)

			if st.fatal != nil {
				return nil
			}
		}

		return cloned
	default:
		return numbers.Public(v)
	}
}
