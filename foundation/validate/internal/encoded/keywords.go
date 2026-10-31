package encoded

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"arkoren.dev/foundation/validate/internal/faults"
	"arkoren.dev/foundation/validate/internal/fields"
	"arkoren.dev/foundation/validate/internal/numbers"
)

// compileEncodedMetadata checks string annotations and URI/anchor spellings.
// URI resolution and anchor registration belong to identify/references.
func compileEncodedMetadata(compiler *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	text, ok := node.doc[keyword].(string)

	if !ok {
		return fmt.Errorf("%w: expected string keyword", faults.ErrInvalidSchema)
	}

	if keyword == "$schema" {
		other := encodedCompiler{}

		if err := other.dialect(node.doc); err != nil {
			return err
		}

		if other.draft07 != compiler.draft07 {
			return fmt.Errorf("%w: mixed schema dialects", faults.ErrInvalidSchema)
		}
	}

	if keyword == "$anchor" || keyword == "$dynamicAnchor" {
		if !validEncodedAnchor(text) {
			return fmt.Errorf("%w: invalid anchor", faults.ErrInvalidSchema)
		}
	}

	return nil
}

// compileEncodedAnnotation validates container annotations without interpreting
// their values as assertions. Invalid defaults remain legal annotations.
func compileEncodedAnnotation(_ *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	if keyword == "examples" {
		if _, ok := node.doc[keyword].([]any); !ok {
			return fmt.Errorf("%w: examples must be an array", faults.ErrInvalidSchema)
		}
	}

	return nil
}

// compileEncodedBoolean checks both boolean assertions and typed annotations.
func compileEncodedBoolean(_ *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	if _, ok := node.doc[keyword].(bool); !ok {
		return fmt.Errorf("%w: expected boolean keyword", faults.ErrInvalidSchema)
	}

	return nil
}

// compileEncodedType accepts a recognized type or a nonempty unique union.
// The exact integer test is deferred to evaluation, never a float conversion.
func compileEncodedType(_ *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	if name, ok := node.doc[keyword].(string); ok {
		return checkEncodedTypeName(name)
	}

	names, ok := node.doc[keyword].([]any)

	if !ok || len(names) == 0 {
		return fmt.Errorf("%w: invalid type union", faults.ErrInvalidSchema)
	}

	if !validRequiredNames(names) {
		return fmt.Errorf("%w: invalid type union", faults.ErrInvalidSchema)
	}

	for _, name := range names {
		if err := checkEncodedTypeName(name.(string)); err != nil {
			return err
		}
	}

	return nil
}

// checkEncodedTypeName rejects unsupported type names rather than weakening an
// assertion. Null extends the application schema's recognized type vocabulary.
func checkEncodedTypeName(name string) error {
	if slices.Contains([]string{"null", "string", "integer", "number", "boolean", "object", "array"}, name) {
		return nil
	}

	return fmt.Errorf("%w: unknown type name", faults.ErrInvalidSchema)
}

// validRequiredNames checks distinct strings without changing decoded input.
// Empty names and empty lists are valid in required and dependency keywords.
func validRequiredNames(raw any) bool {
	names, ok := raw.([]any)

	if !ok {
		return false
	}

	seen := make(map[string]bool, len(names))

	for _, value := range names {
		name, ok := value.(string)

		if !ok || seen[name] {
			return false
		}

		seen[name] = true
	}

	return true
}

// compileEncodedEnum requires a nonempty array. Duplicate enum values do not
// weaken validation, so no quadratic compile-time uniqueness pass is needed.
func compileEncodedEnum(_ *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	values, ok := node.doc[keyword].([]any)

	if !ok || len(values) == 0 {
		return fmt.Errorf("%w: enum must be a nonempty array", faults.ErrInvalidSchema)
	}

	return nil
}

// compileEncodedNumber validates exact numeric assertions without materializing
// diagnostic literals. Only multipleOf requires a positive value.
func compileEncodedNumber(_ *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	number, ok := numbers.Exact(node.doc[keyword])

	if !ok {
		return fmt.Errorf("%w: expected numeric keyword", faults.ErrInvalidSchema)
	}

	if keyword == "multipleOf" && number.Sign() <= 0 {
		return fmt.Errorf("%w: multipleOf must be positive", faults.ErrInvalidSchema)
	}

	return nil
}

// compileEncodedCount checks nonnegative mathematical integers. Counts are
// retained as rationals so schema limits need not fit a machine integer.
func compileEncodedCount(_ *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	number, ok := numbers.Exact(node.doc[keyword])

	if !ok {
		return fmt.Errorf("%w: expected nonnegative integer keyword", faults.ErrInvalidSchema)
	}

	if number.Sign() < 0 || !number.IsInt() {
		return fmt.Errorf("%w: expected nonnegative integer keyword", faults.ErrInvalidSchema)
	}

	return nil
}

// compileEncodedPattern compiles the string assertion once with Go's bounded
// RE2 engine. Compiler errors are redacted because they can contain schema text.
func compileEncodedPattern(_ *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	text, ok := node.doc[keyword].(string)

	if !ok {
		return fmt.Errorf("%w: pattern must be a string", faults.ErrInvalidSchema)
	}

	pattern, err := regexp.Compile(text)

	if err != nil {
		return fmt.Errorf("%w: invalid RE2 pattern", faults.ErrInvalidSchema)
	}

	node.patterns[keyword] = pattern

	return nil
}

// compileEncodedRequired checks presence lists, including the legal empty name.
// It shares the application importer's exact string/duplicate grammar.
func compileEncodedRequired(_ *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	if !validRequiredNames(node.doc[keyword]) {
		return fmt.Errorf("%w: expected unique string array", faults.ErrInvalidSchema)
	}

	return nil
}

// compileEncodedRequiredMap checks every dependent-required presence list.
func compileEncodedRequiredMap(_ *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	entries, ok := node.doc[keyword].(map[string]any)

	if !ok {
		return fmt.Errorf("%w: expected dependency object", faults.ErrInvalidSchema)
	}

	for _, value := range entries {
		if !validRequiredNames(value) {
			return fmt.Errorf("%w: expected unique string dependency array", faults.ErrInvalidSchema)
		}
	}

	return nil
}

// compileEncodedChild compiles one schema-valued keyword, including ignored
// contentSchema annotations whose syntax must nevertheless be valid.
func compileEncodedChild(
	compiler *encodedCompiler, node *Node, keyword string, locations []encodedLocation,
) error {
	return compiler.child(node, keyword, node.doc[keyword], locations)
}

// compileEncodedMap compiles named subschemas and property-name regexes. Map
// keys are escaped only for pointer lookup, never for matching instance names.
func compileEncodedMap(
	compiler *encodedCompiler, node *Node, keyword string, locations []encodedLocation,
) error {
	entries, ok := node.doc[keyword].(map[string]any)

	if !ok {
		return fmt.Errorf("%w: expected schema object", faults.ErrInvalidSchema)
	}

	if keyword == "patternProperties" {
		if err := compileEncodedPropertyPatterns(node, entries); err != nil {
			return err
		}
	}

	for _, name := range slices.Sorted(maps.Keys(entries)) {
		pointer := keyword + "/" + fields.EscapePointer(name)

		if err := compiler.child(node, pointer, entries[name], locations); err != nil {
			return err
		}
	}

	return nil
}

// compileEncodedPropertyPatterns compiles the map's name expressions before
// child schemas. Matching later visits the same deterministic name ordering.
func compileEncodedPropertyPatterns(node *Node, entries map[string]any) error {
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		pattern, err := regexp.Compile(name)

		if err != nil {
			return fmt.Errorf("%w: invalid property RE2 pattern", faults.ErrInvalidSchema)
		}

		node.patterns["patternProperties/"+fields.EscapePointer(name)] = pattern
	}

	return nil
}

// compileEncodedList checks nonempty composition or prefix lists and compiles
// children in array order. Array order is preserved during evaluation.
func compileEncodedList(
	compiler *encodedCompiler, node *Node, keyword string, locations []encodedLocation,
) error {
	values, ok := node.doc[keyword].([]any)

	if !ok || len(values) == 0 {
		return fmt.Errorf("%w: expected nonempty schema array", faults.ErrInvalidSchema)
	}

	for index, value := range values {
		if err := compiler.child(node, keyword+"/"+strconv.Itoa(index), value, locations); err != nil {
			return err
		}
	}

	return nil
}

// compileEncodedItems permits tuple arrays only in draft-07. Modern items is
// always a schema; prefixItems supplies positional constraints instead.
func compileEncodedItems(
	compiler *encodedCompiler, node *Node, keyword string, locations []encodedLocation,
) error {
	if _, ok := node.doc[keyword].([]any); ok {
		if !node.draft07 {
			return fmt.Errorf("%w: tuple items requires draft-07", faults.ErrInvalidSchema)
		}

		return compileEncodedList(compiler, node, keyword, locations)
	}

	return compileEncodedChild(compiler, node, keyword, locations)
}

// compileEncodedDependencies distinguishes draft-07 presence arrays from schema
// dependencies. The legacy keyword is rejected in modern dialects, not ignored.
func compileEncodedDependencies(
	compiler *encodedCompiler, node *Node, keyword string, locations []encodedLocation,
) error {
	if !node.draft07 {
		return fmt.Errorf("%w: dependencies requires draft-07", faults.ErrInvalidSchema)
	}

	entries, ok := node.doc[keyword].(map[string]any)

	if !ok {
		return fmt.Errorf("%w: expected dependency object", faults.ErrInvalidSchema)
	}

	for _, name := range slices.Sorted(maps.Keys(entries)) {
		value := entries[name]
		_, array := value.([]any)

		if array && !validRequiredNames(value) {
			return fmt.Errorf("%w: invalid dependency names", faults.ErrInvalidSchema)
		}

		if array {
			continue
		}

		if err := compiler.child(node, keyword+"/"+fields.EscapePointer(name), value, locations); err != nil {
			return err
		}
	}

	return nil
}

// compileEncodedVocabulary refuses a required vocabulary unless every assertion
// in that vocabulary is implemented. Optional unknown vocabularies do not make
// unknown keywords acceptable; the normal strict keyword gate still applies.
func compileEncodedVocabulary(compiler *encodedCompiler, node *Node, keyword string, _ []encodedLocation) error {
	if !compiler.explicit2020 {
		return fmt.Errorf("%w: vocabulary requires explicit 2020-12", faults.ErrInvalidSchema)
	}

	entries, ok := node.doc[keyword].(map[string]any)

	if !ok {
		return fmt.Errorf("%w: vocabulary must be an object", faults.ErrInvalidSchema)
	}

	for name, value := range entries {
		required, ok := value.(bool)

		if !ok {
			return fmt.Errorf("%w: vocabulary entry must be boolean", faults.ErrInvalidSchema)
		}

		if required && !supportedEncodedVocabulary(name) {
			return fmt.Errorf("%w: unsupported required vocabulary", faults.ErrInvalidSchema)
		}
	}

	return nil
}

// supportedEncodedVocabulary recognizes the implemented 2020-12 vocabularies.
// Format assertion is intentionally absent: format is always an annotation.
func supportedEncodedVocabulary(name string) bool {
	suffix, ok := strings.CutPrefix(name, "https://json-schema.org/draft/2020-12/vocab/")

	if !ok {
		return false
	}

	return slices.Contains([]string{
		"core", "applicator", "unevaluated", "validation", "meta-data", "format-annotation", "content",
	}, suffix)
}
