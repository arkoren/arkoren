package validate

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"time"

	"arkoren.dev/foundation/validate/internal/fields"
	"arkoren.dev/foundation/validate/internal/numbers"
)

// JSONSchema validates decoded JSON values against a JSON Schema document.
// Create one with [FromJSONSchema]; the zero value is not usable. Like the
// other schemas it is immutable and safe for concurrent use.
//
// Values are the ones encoding/json/v2 decodes into an any: map[string]any,
// []any, string, float64, bool and nil. Go int and int64 and valid numeric
// [encoding/json/jsontext.Value] tokens are also accepted as numbers. Other Go
// types match no "type" and are not checked by kind-specific keywords.
// Input must be acyclic. uniqueItems requires finite
// numbers and these JSON-like types throughout each item; other values report
// "type" at that item's pointer instead of using an unbounded fallback.
//
// uniqueItems uses exact structural keys, including sorted object keys.
// For n fixed-size scalar items, expected work and key storage are O(n),
// including when maxItems is absent. Nested values add their encoded size
// and O(k log k) sorting per object of k keys. Numeric items compare by their
// exact decimal value at every nesting level, without changing concrete Go
// input values. Keys describe pre-default items.
// See [JSONSchema.Parse] for cancellation and oversized-array behavior.
//
// Imported numeric literals retain their exact decimal value and spelling for
// bounds, multipleOf, enum, const, and schema export. Go floats are interpreted
// as their shortest decimal JSON encoding; precision lost before Parse cannot
// be recovered. Numeric defaults and diagnostic parameters use float64 only
// when its JSON spelling preserves that value; otherwise they use an owned
// jsontext.Value. Existing Go numeric inputs retain their concrete types.
// Very large exponents outside math/big's supported range are schema errors.
// Bound untrusted document size and numeric magnitude as well as input size.
//
// The semantics are those of JSON Schema, with these choices:
//
//   - null is absent, as nil is elsewhere in this package: it satisfies
//     every keyword, whatever "type" says, and is replaced by "default"
//     in Parse. A key listed in "required" must be present in the object,
//     and null counts as present.
//   - Zero values are values. Unlike [Struct], where false, 0 and ""
//     are absent and skip rules, a "minLength" of 3 rejects "". Likewise
//     "required" is about the key, so "" and 0 satisfy it.
//   - Keywords apply to the kinds they belong to and are ignored for the
//     others, so "minLength" without "type" only checks strings.
//   - "format" checks date-time (RFC 3339), email, uri (absolute, with a
//     host), uuid, duration (RFC 3339 Appendix A) and go-duration
//     (time.ParseDuration). Other formats, including binary, are annotations
//     and accept any string.
//
// Example:
//
//	check, err := validate.FromJSONSchema([]byte(`{"uniqueItems":true,"maxItems":100}`))
//	if err != nil {
//		return err
//	}
//	return check.Validate(ctx, []any{"blue", "green"})
type JSONSchema struct {
	// root retains the compiled root document used for validation and parsing.
	root *jsonNode
}

// jsonNode is a compiled schema or subschema. Keyword values are read
// from doc, which [FromJSONSchema] has already checked for types.
type jsonNode struct {
	// doc retains the checked keyword values for this schema node.
	doc map[string]any
	// pattern caches the compiled string pattern, or nil when no pattern is
	// configured.
	pattern *regexp.Regexp
	// items holds the array child schema, or nil for unconstrained elements.
	items *jsonNode
	// additional holds the schema for object properties not explicitly listed.
	additional *jsonNode
	// properties maps JSON property names to compiled child schemas.
	properties map[string]*jsonNode
}

// formatCheck is the violation for a string that is not in a format.
type formatCheck struct {
	// code identifies the violation emitted when this check fails.
	code string
	// params supplies fixed interpolation values for a format violation.
	params map[string]any
	// valid accepts strings that satisfy the supported format.
	valid func(string) bool
}

// jsonKeywordRule describes one accepted keyword's value grammar. A nil check
// means any literal is accepted here or recursive compilation checks the value.
// The static table is the importer's complete allowlist, not an extension API.
type jsonKeywordRule struct {
	// valid checks a decoded keyword value without executing the schema.
	valid func(any) bool
	// problem is the keyword-specific diagnostic for a malformed value.
	problem string
}

var (
	// jsonTypes are the "type" values this package emits.
	jsonTypes = []string{"string", "integer", "number", "boolean", "object", "array"}

	// jsonKeywordRules keeps supported keyword names, value grammars and error
	// messages together. Child/pattern compilation supplies its own diagnostics.
	jsonKeywordRules = map[string]jsonKeywordRule{
		"type": {func(value any) bool {
			name, _ := value.(string)

			return slices.Contains(jsonTypes, name)
		}, fmt.Sprintf("type must be one of %v", jsonTypes)},
		"description": {validJSONString, "description must be a string"},
		"format":      {validJSONString, "format must be a string"},
		"enum": {func(value any) bool {
			values, ok := value.([]any)

			return ok && len(values) > 0
		}, "enum must be a non-empty array"},
		"required": {validRequiredNames, "required must be an array of unique strings"},
		"uniqueItems": {func(value any) bool {
			_, ok := value.(bool)

			return ok
		}, "uniqueItems must be a boolean"},
		"minimum":          {validJSONNumber, "minimum must be a number"},
		"maximum":          {validJSONNumber, "maximum must be a number"},
		"exclusiveMinimum": {validJSONNumber, "exclusiveMinimum must be a number"},
		"exclusiveMaximum": {validJSONNumber, "exclusiveMaximum must be a number"},
		"multipleOf": {func(value any) bool {
			factor, ok := numbers.Exact(value)

			return ok && factor.Sign() > 0
		}, "multipleOf must be a number greater than zero"},
		"minLength":            {validJSONCount, "minLength must be a non-negative integer"},
		"maxLength":            {validJSONCount, "maxLength must be a non-negative integer"},
		"minItems":             {validJSONCount, "minItems must be a non-negative integer"},
		"maxItems":             {validJSONCount, "maxItems must be a non-negative integer"},
		"minProperties":        {validJSONCount, "minProperties must be a non-negative integer"},
		"maxProperties":        {validJSONCount, "maxProperties must be a non-negative integer"},
		"default":              {},
		"const":                {},
		"items":                {},
		"additionalProperties": {},
		"properties":           {},
		"pattern":              {},
	}

	// formatChecks maps the formats that are enforced to their violation.
	formatChecks = map[string]formatCheck{
		"date-time": {
			"type",
			map[string]any{"expected": "RFC 3339 date-time"},
			func(v string) bool {
				_, err := time.Parse(time.RFC3339, v)

				return err == nil
			},
		},
		"email": {
			"string.email",
			nil,
			validEmail,
		},
		"uri": {
			"string.url",
			nil,
			func(v string) bool {
				parsed, err := url.Parse(v)

				return err == nil && parsed.Scheme != "" && parsed.Host != ""
			},
		},
		"uuid": {
			"string.uuid",
			nil,
			validUUID,
		},
		"duration": {
			"string.duration",
			nil,
			jsonDurationPattern.MatchString,
		},
		goDurationFormat: {
			"string.duration",
			nil,
			validDuration,
		},
	}
)

// FromJSONSchema builds a validator for decoded JSON values from a JSON
// Schema document. It accepts the following self-contained keyword subset;
// this is not a general JSON Schema implementation or a guarantee that every
// exported definition can be imported unchanged:
//
//	type, properties, required, additionalProperties, items, enum, const,
//	default, description, format, pattern, minLength, maxLength, minimum,
//	maximum, exclusiveMinimum, exclusiveMaximum, multipleOf, minItems,
//	maxItems, uniqueItems, minProperties, maxProperties
//
// "type" is a single string (string, integer, number, boolean, object or
// array): the package never renders type arrays, because nil is not
// modeled. "integer" rejects numbers with a fractional part. Anything
// else is an error rather than being ignored, so a document that depends
// on a keyword the validator would skip cannot pass for checked:
// FromJSONSchema returns an error wrapping [ErrInvalidSchema] that names
// the keyword and its JSON Pointer location, such as
// `unsupported keyword "$ref" at "/properties/a"`. The same error reports
// a keyword with a value of the wrong type, a pattern that does not
// compile, or a default that fails its own schema.
// Required names must be unique strings; an empty property name is valid.
// Numeric literals are kept exactly rather than decoded through float64.
//
// Export omits runtime normalizers and checks and does not verify defaults.
// Import therefore need not accept an exported default that violates the
// emitted constraints, and it cannot reproduce omitted runtime behavior.
// Import retains the application-oriented null/default rules of [JSONSchema];
// it is not strict validation of an already encoded instance.
//
// Violations are keyed by the JSON Pointer of the offending value and use
// the codes of the equivalent rule ("string.min", "number.max",
// "slice.unique", "required", "one_of", ...), so messages translate
// through the same [Catalog]. A value of the wrong kind reports "type",
// and a date-time string that does not parse reports "type" with the
// expected value "RFC 3339 date-time". Rules that [Struct] enforces but
// JSON Schema cannot express (field comparisons, conditions, Check) are
// not in the document and are not checked.
//
// "pattern" must be a string; an empty pattern matches every string. Patterns
// use Go regular expressions (RE2), which can differ from the ECMA 262 dialect
// that JSON Schema names.
//
// Example:
//
//	check, err := validate.FromJSONSchema(doc)
//	if err != nil {
//		return err // unsupported keyword or malformed document
//	}
//
//	if err := check.Validate(ctx, decoded); err != nil {
//		errs, ok := errors.AsType[validate.Errors](err)
//		if !ok {
//			return err // canceled context or deadline
//		}
//		// errs["/items/0/name"][0].Code == "string.min"
//		return errs
//	}
func FromJSONSchema(doc []byte) (JSONSchema, error) {
	var decoded any

	if err := json.Unmarshal(doc, &decoded, json.WithUnmarshalers(json.UnmarshalFromFunc(numbers.Unmarshal))); err != nil {
		return JSONSchema{}, fmt.Errorf("%w: JSON Schema: %w", ErrInvalidSchema, err)
	}

	root, err := buildJSONNode(decoded, "")

	if err != nil {
		return JSONSchema{}, err
	}

	return JSONSchema{
		root: root,
	}, nil
}

// jsonSchemaErrorf reports a problem at the JSON Pointer at of the
// document.
func jsonSchemaErrorf(at, format string, args ...any) error {
	return fmt.Errorf("%w: JSON Schema at %q: %s", ErrInvalidSchema, at, fmt.Sprintf(format, args...))
}

// buildJSONNode checks raw, the decoded schema at the pointer at, and
// compiles it and its subschemas. It stops at the first problem.
func buildJSONNode(raw any, at string) (*jsonNode, error) {
	doc, ok := raw.(map[string]any)

	if !ok {
		return nil, jsonSchemaErrorf(at, "a schema must be an object")
	}

	n := &jsonNode{
		doc: doc,
	}

	for _, keyword := range slices.Sorted(maps.Keys(doc)) {
		where := at + "/" + fields.EscapePointer(keyword)

		if err := checkJSONKeyword(keyword, doc[keyword], where); err != nil {
			return nil, err
		}

		if err := n.compileKeyword(keyword, doc[keyword], where); err != nil {
			return nil, err
		}
	}

	if def, ok := doc["default"]; ok {
		st := &state{
			ctx: context.Background(),
		}

		n.run(st, def)

		if len(st.errs) > 0 {
			return nil, jsonSchemaErrorf(at+"/default", "default fails its own schema: %v", st.errs)
		}
	}

	return n, nil
}

// compileKeyword binds child schemas and compiles a pattern. Literal
// keyword grammars are checked separately; default verification remains a final
// construction step after every keyword is compiled without running defaults.
func (jsonNode *jsonNode) compileKeyword(keyword string, value any, where string) error {
	var err error

	switch keyword {
	case "items":
		jsonNode.items, err = buildJSONNode(value, where)

		return err
	case "additionalProperties":
		jsonNode.additional, err = buildJSONNode(value, where)

		return err
	case "properties":
		jsonNode.properties, err = buildJSONProperties(value, where)

		return err
	case "pattern":
		jsonNode.pattern, err = compileJSONPattern(value, where)

		return err
	}

	return nil
}

// compileJSONPattern rejects nonstring keyword values before RE2 compilation.
// The returned expression is immutable and reused for every instance; an empty
// string remains a valid expression matching any input string.
func compileJSONPattern(value any, where string) (*regexp.Regexp, error) {
	text, ok := value.(string)

	if !ok {
		return nil, jsonSchemaErrorf(where, "pattern must be a string")
	}

	compiled, err := regexp.Compile(text)

	if err != nil {
		return nil, jsonSchemaErrorf(where, "pattern does not compile: %v", err)
	}

	return compiled, nil
}

// checkJSONKeyword rejects unknown keywords and malformed literal operands at
// the keyword's JSON Pointer. It never executes validation or default insertion;
// recursive schemas and patterns are checked by compileKeyword afterward.
func checkJSONKeyword(keyword string, value any, where string) error {
	rule, ok := jsonKeywordRules[keyword]

	if !ok {
		return jsonSchemaErrorf(where, "unsupported keyword %q", keyword)
	}

	if rule.valid != nil && !rule.valid(value) {
		return jsonSchemaErrorf(where, "%s", rule.problem)
	}

	return nil
}

// validJSONString supplies the shared JSON string grammar of description and
// format annotations. No conversion or fmt rendering can make another kind a
// valid string keyword value.
func validJSONString(value any) bool {
	_, ok := value.(string)

	return ok
}

// validJSONNumber checks exact numeric bounds without changing their spelling.
// The same grammar serves inclusive and exclusive upper/lower constraints.
func validJSONNumber(value any) bool {
	_, ok := numbers.Exact(value)

	return ok
}

// validJSONCount checks all size and length constraints without narrowing an
// arbitrary-precision JSON integer to a machine-sized count. Fractions and
// negative values are invalid even when their float64 rounding would hide them.
func validJSONCount(value any) bool {
	count, ok := numbers.Exact(value)

	return ok && count.IsInt() && count.Sign() >= 0
}

// validRequiredNames checks the complete supported required-keyword grammar:
// an array of distinct strings, including the legal empty property name. It
// leaves decoded input untouched and accepts an empty list as no requirement.
func validRequiredNames(raw any) bool {
	names, ok := raw.([]any)

	if !ok {
		return false
	}

	seen := make(map[string]bool, len(names))

	for _, value := range names {
		name, ok := value.(string)

		if !ok {
			return false
		}

		if seen[name] {
			return false
		}

		seen[name] = true
	}

	return true
}

// buildJSONProperties compiles the subschemas of a "properties" keyword.
func buildJSONProperties(raw any, at string) (map[string]*jsonNode, error) {
	entries, ok := raw.(map[string]any)

	if !ok {
		return nil, jsonSchemaErrorf(at, "properties must be an object")
	}

	properties := make(map[string]*jsonNode, len(entries))

	for _, name := range slices.Sorted(maps.Keys(entries)) {
		child, err := buildJSONNode(entries[name], at+"/"+fields.EscapePointer(name))

		if err != nil {
			return nil, err
		}

		properties[name] = child
	}

	return properties, nil
}

// Validate checks the decoded JSON value v and returns nil, or an [Errors]
// keyed by JSON Pointer. See [JSONSchema] for the value types and
// [FromJSONSchema] for the codes. v is never modified. Cancellation returns
// ctx.Err() instead of any accumulated violations; see [JSONSchema.Parse]
// for traversal and size limits.
//
// Example:
//
//	check, err := validate.FromJSONSchema([]byte(`{"type":"array","maxItems":100}`))
//	if err != nil {
//		return err
//	}
//	return check.Validate(ctx, []any{1.0, 2.0})
func (schema JSONSchema) Validate(ctx context.Context, v any) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse runs the checks of [JSONSchema.Validate] and also returns a deep
// copy of v with defaults applied: a property that is missing or null
// takes its "default", as does a null value anywhere else that has one.
// v is never modified. On failure the result is nil. After the type check,
// an array exceeding maxItems reports only slice.max for that array, skipping
// its other checks and children; validation of sibling values still continues.
//
// Traversal, equality checks, structural uniqueness keys, object-key sorting,
// and copying observe ctx cancellation. Cancellation returns ctx.Err()
// unchanged and discards any accumulated violations. Individual scalar
// checks (such as a regexp match) and runtime allocations are not preempted.
// Input must be acyclic; bound document size and nesting before validation.
//
// Example:
//
//	check, err := validate.FromJSONSchema([]byte(`{"type":"string","default":"free"}`))
//	if err != nil {
//		return err
//	}
//	parsed, err := check.Parse(ctx, nil) // parsed is "free" on success
func (schema JSONSchema) Parse(ctx context.Context, v any) (any, error) {
	st := &state{
		ctx: ctx,
	}

	parsed := schema.root.run(st, v)

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := st.result(); err != nil {
		return nil, err
	}

	return parsed, nil
}

// JSONSchema renders the accepted document with sorted object keys and the
// original spelling of numeric literals. It does not run validation or default
// insertion. This is deterministic rendering, not RFC 8785 canonicalization:
// equivalent numeric spellings such as 1.0 and 1e0 remain distinct in the output.
//
// Example:
//
//	schema, err := validate.FromJSONSchema([]byte(`{"type":"string"}`))
//	if err != nil {
//		panic(err)
//	}
//	document, err := schema.JSONSchema()
//	if err != nil {
//		panic(err)
//	}
//	_ = document
func (schema JSONSchema) JSONSchema() ([]byte, error) {
	return renderJSONSchema(schema.root.doc)
}
