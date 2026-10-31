package encoded

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"arkoren.dev/foundation/validate/internal/faults"
)

// Node owns a checked schema and its compiled applicators. A nil doc
// represents a boolean schema; object schemas always have a nonnil doc. All
// maps become immutable when FromJSONSchemaStrict returns.
type Node struct {
	// doc contains owned exact literals and checked keyword arguments.
	doc map[string]any
	// accept gives a boolean schema's result, unused for object schemas.
	accept bool
	// children indexes direct subschemas by escaped keyword-relative pointer.
	children map[string]*Node
	// patterns caches pattern and patternProperties RE2 expressions.
	patterns map[string]*regexp.Regexp
	// reference is the statically resolved $ref, if present.
	reference *Node
	// dynamic is the statically resolved $dynamicRef, if present.
	dynamic *Node
	// dynamicName identifies a dynamic target anchor, empty for static targets.
	dynamicName string
	// resource owns this node's dynamic anchors and base URI.
	resource *encodedResource
	// draft07 selects the supported legacy sibling/tuple/dependencies semantics.
	draft07 bool
}

// encodedResource represents an in-document $id boundary. Its URI is used only
// for local lookup; neither compilation nor evaluation fetches that URI.
type encodedResource struct {
	// base is the fragment-free resolved resource identifier.
	base *url.URL
	// anchors holds dynamic anchors declared anywhere within this resource.
	anchors map[string]*Node
}

// encodedLocation records an ancestor resource's pointer to the current node.
// Retaining ancestor aliases allows references through embedded $id boundaries.
type encodedLocation struct {
	// resource supplies the URI to which this pointer fragment belongs.
	resource *encodedResource
	// pointer is an RFC 6901 pointer relative to that resource's root.
	pointer string
}

// encodedCompiler builds a closed graph and resolves links only after every
// local resource and anchor has been indexed, permitting forward references.
type encodedCompiler struct {
	// vocabulary supplies the closed keyword dispatch table for this compilation.
	vocabulary map[string]encodedKeywordCompiler
	// nodes preserves construction order for deterministic reference resolution.
	nodes []*Node
	// index maps absolute URI/fragment pairs to their owned compiled targets.
	index map[string]*Node
	// indexBytes bounds cumulative alias key storage, not just each URI's size.
	indexBytes int
	// budget bounds compilation independently from schema token decoding.
	budget encodedBudget
	// draft07 is inherited by all nodes; mixed embedded dialects are refused.
	draft07 bool
	// explicit2020 permits $vocabulary only with the explicit 2020-12 dialect.
	explicit2020 bool
}

// encodedKeywordCompiler validates and compiles one keyword at its owning
// schema node. The dispatch table is the complete accepted vocabulary.
type encodedKeywordCompiler func(*encodedCompiler, *Node, string, []encodedLocation) error

// encodedKeywords classifies known keywords without silently skipping unknown
// assertions. Only documented annotations and x-* extensions have no evaluator.
func encodedKeywords() map[string]encodedKeywordCompiler {
	return map[string]encodedKeywordCompiler{
		"$schema": compileEncodedMetadata, "$id": compileEncodedMetadata,
		"$anchor": compileEncodedMetadata, "$dynamicAnchor": compileEncodedMetadata,
		"$ref": compileEncodedMetadata, "$dynamicRef": compileEncodedMetadata,
		"$vocabulary": compileEncodedVocabulary,
		"$comment":    compileEncodedMetadata, "title": compileEncodedMetadata,
		"description": compileEncodedMetadata, "format": compileEncodedMetadata,
		"contentEncoding": compileEncodedMetadata, "contentMediaType": compileEncodedMetadata,
		"default": compileEncodedAnnotation, "examples": compileEncodedAnnotation,
		"deprecated": compileEncodedBoolean, "readOnly": compileEncodedBoolean,
		"writeOnly": compileEncodedBoolean, "uniqueItems": compileEncodedBoolean,
		"type": compileEncodedType, "enum": compileEncodedEnum, "const": compileEncodedAnnotation,
		"minimum": compileEncodedNumber, "maximum": compileEncodedNumber,
		"exclusiveMinimum": compileEncodedNumber, "exclusiveMaximum": compileEncodedNumber,
		"multipleOf": compileEncodedNumber,
		"minLength":  compileEncodedCount, "maxLength": compileEncodedCount,
		"minItems": compileEncodedCount, "maxItems": compileEncodedCount,
		"minProperties": compileEncodedCount, "maxProperties": compileEncodedCount,
		"minContains": compileEncodedCount, "maxContains": compileEncodedCount,
		"pattern":  compileEncodedPattern,
		"required": compileEncodedRequired, "dependentRequired": compileEncodedRequiredMap,
		"properties": compileEncodedMap, "patternProperties": compileEncodedMap,
		"$defs": compileEncodedMap, "definitions": compileEncodedMap,
		"dependentSchemas": compileEncodedMap, "dependencies": compileEncodedDependencies,
		"additionalProperties": compileEncodedChild, "propertyNames": compileEncodedChild,
		"unevaluatedProperties": compileEncodedChild, "unevaluatedItems": compileEncodedChild,
		"contains": compileEncodedChild, "additionalItems": compileEncodedChild,
		"not": compileEncodedChild, "if": compileEncodedChild,
		"then": compileEncodedChild, "else": compileEncodedChild,
		"contentSchema": compileEncodedChild, "items": compileEncodedItems,
		"allOf": compileEncodedList, "anyOf": compileEncodedList,
		"oneOf": compileEncodedList, "prefixItems": compileEncodedList,
	}
}

// Compile builds an immutable, self-contained strict schema graph.
func Compile(document []byte) (*Node, error) {
	raw, err := decodeEncoded(context.Background(), document)

	if err != nil {
		if errors.Is(err, faults.ErrEncodedLimit) {
			return nil, fmt.Errorf("%w: %w", faults.ErrInvalidSchema, err)
		}

		return nil, fmt.Errorf("%w: malformed encoded schema", faults.ErrInvalidSchema)
	}

	compiler := encodedCompiler{
		vocabulary: encodedKeywords(),
		index:      make(map[string]*Node),
		budget:     encodedBudget{ctx: context.Background()},
	}

	if err := compiler.dialect(raw); err != nil {
		return nil, err
	}

	base, err := url.Parse("https://arkoren.invalid/encoded-schema")

	if err != nil {
		return nil, fmt.Errorf("%w: base URI", faults.ErrInvalidSchema)
	}

	resource := &encodedResource{base: base, anchors: make(map[string]*Node)}

	root, err := compiler.node(raw, []encodedLocation{{resource: resource}})

	if err != nil {
		return nil, fmt.Errorf("%w: %w", faults.ErrInvalidSchema, err)
	}

	if err := compiler.references(); err != nil {
		return nil, fmt.Errorf("%w: %w", faults.ErrInvalidSchema, err)
	}

	return root, nil
}

// dialect recognizes only implemented dialects rather than compiling an
// unknown dialect into a validator that silently changes assertion meaning.
func (compiler *encodedCompiler) dialect(raw any) error {
	doc, _ := raw.(map[string]any)
	value, present := doc["$schema"]

	if !present {
		return nil
	}

	switch value {
	case "":
		return nil
	case "http://json-schema.org/draft-07/schema#", "https://json-schema.org/draft-07/schema#":
		compiler.draft07 = true
	case "https://json-schema.org/draft/2020-12/schema":
		compiler.explicit2020 = true
	default:
		return fmt.Errorf("%w: unsupported schema dialect", faults.ErrInvalidSchema)
	}

	return nil
}

// node checks a schema value, publishes its locations before visiting children,
// and compiles each known keyword. Forward/cyclic refs are linked afterwards.
func (compiler *encodedCompiler) node(raw any, locations []encodedLocation) (*Node, error) {
	if err := compiler.budget.tick(); err != nil {
		return nil, err
	}

	if len(compiler.nodes) >= encodedMaxNodes {
		return nil, fmt.Errorf("%w: schema nodes", faults.ErrEncodedLimit)
	}

	node := &Node{
		children: make(map[string]*Node),
		patterns: make(map[string]*regexp.Regexp),
		draft07:  compiler.draft07,
	}

	switch value := raw.(type) {
	case bool:
		node.accept = value
	case map[string]any:
		node.doc = value
	default:
		return nil, fmt.Errorf("%w: expected boolean or object schema", faults.ErrInvalidSchema)
	}

	locations, err := compiler.identify(node, locations)

	if err != nil {
		return nil, err
	}

	compiler.nodes = append(compiler.nodes, node)

	if err := compiler.keywords(node, locations); err != nil {
		return nil, err
	}

	return node, nil
}

// keywords dispatches checked values and then compiles all schema-valued
// descendants with their resource-relative pointer aliases intact.
func (compiler *encodedCompiler) keywords(node *Node, locations []encodedLocation) error {
	for _, keyword := range slices.Sorted(maps.Keys(node.doc)) {
		if err := compiler.budget.tick(); err != nil {
			return err
		}

		if strings.HasPrefix(keyword, "x-") {
			continue
		}

		compile := compiler.vocabulary[keyword]

		if compile == nil {
			return fmt.Errorf("%w: unsupported schema keyword", faults.ErrInvalidSchema)
		}

		if err := compile(compiler, node, keyword, locations); err != nil {
			return err
		}
	}

	return nil
}

// child compiles one direct descendant with aliases for each enclosing resource.
// Its pointer is already escaped relative to the parent schema, not a URI.
func (compiler *encodedCompiler) child(
	node *Node, pointer string, raw any, locations []encodedLocation,
) error {
	childLocations := make([]encodedLocation, len(locations))

	for index, location := range locations {
		if len(location.pointer)+len(pointer)+1 > encodedMaxLocationBytes {
			return fmt.Errorf("%w: schema pointer bytes", faults.ErrEncodedLimit)
		}

		childLocations[index] = encodedLocation{location.resource, location.pointer + "/" + pointer}
	}

	child, err := compiler.node(raw, childLocations)

	if err != nil {
		return err
	}

	node.children[pointer] = child

	return nil
}
