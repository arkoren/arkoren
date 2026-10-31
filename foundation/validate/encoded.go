package validate

import (
	"context"
	"errors"

	"arkoren.dev/foundation/validate/internal/encoded"
	"arkoren.dev/foundation/validate/internal/faults"
)

// EncodedSchema checks an already encoded JSON instance without changing it.
// Construct it with [FromJSONSchemaStrict]. The zero value is invalid. Published
// schemas own their compiled state and are safe for concurrent use; no caller
// bytes, application callbacks, normalizers, or mutable defaults are retained.
// Unlike [JSONSchema], null is a value, missing properties remain missing, and
// default and format are annotations, never transformations or assertions.
//
// This is a bounded, self-contained validator, not an unrestricted JSON Schema
// implementation. See [FromJSONSchemaStrict] for its vocabulary and dialects,
// and [EncodedSchema.Validate] for resource and error contracts.
//
// Example:
//
//	check, err := validate.FromJSONSchemaStrict([]byte(`{"type":"integer"}`))
//	if err != nil {
//		return err
//	}
//	return check.Validate(context.Background(), []byte(`9007199254740993`))
type EncodedSchema struct {
	// root owns the immutable compiled graph; nil identifies the invalid zero.
	root *encoded.Node
}

// ErrEncodedLimit identifies a resource refusal, not a schema violation.
// Both compilation and validation can wrap it. No operation approximates a
// number or accepts an incomplete traversal when a limit is reached. Adapters
// should classify this as an internal/resource failure, not invalid output.
//
// Example:
//
//	check, err := validate.FromJSONSchemaStrict([]byte(`true`))
//	if err != nil {
//		return err
//	}
//	err = check.Validate(context.Background(), []byte(`1e4097`))
//	if errors.Is(err, validate.ErrEncodedLimit) {
//		return errors.New("output validation exceeded its resource budget")
//	}
//	return err
var ErrEncodedLimit = faults.ErrEncodedLimit

// ErrEncodedJSON identifies malformed instance JSON, including duplicate object
// names and trailing documents. Its message contains no document text. Schema
// syntax failures instead wrap [ErrInvalidSchema]. Neither error is an [Errors]
// value. Adapters must not send raw validation errors to an untrusted client:
// schema violations contain JSON Pointers, which can include private names.
//
// Example:
//
//	check, err := validate.FromJSONSchemaStrict([]byte(`true`))
//	if err != nil {
//		return err
//	}
//	err = check.Validate(context.Background(), []byte(`{"name":1,"name":2}`))
//	if errors.Is(err, validate.ErrEncodedJSON) {
//		return errors.New("output is not valid JSON")
//	}
//	return err
var ErrEncodedJSON = faults.ErrEncodedJSON

// FromJSONSchemaStrict compiles a self-contained draft-07 or 2020-12 schema for
// strict wire validation. Omitted or empty $schema selects 2020-12; identifiers
// are http(s)://json-schema.org/draft-07/schema# and
// https://json-schema.org/draft/2020-12/schema. Mixed embedded dialects fail.
// Draft-07 $ref ignores assertion siblings; 2020-12 evaluates them. Local $id,
// JSON Pointer refs, definitions/$defs, anchors, and dynamic references resolve
// only within the supplied document. External targets fail without any I/O.
// Pointers must name a compiled schema location (the root or a recognized
// schema-valued keyword), not a value embedded in an annotation.
//
// Supported assertions are boolean schemas; type (including unions and null);
// enum/const; minimum/maximum/exclusiveMinimum/exclusiveMaximum/multipleOf;
// minLength/maxLength/pattern; minItems/maxItems/uniqueItems; items/prefixItems/
// additionalItems (legacy tuple rules for draft-07); contains/minContains/
// maxContains; minProperties/maxProperties/required/properties/patternProperties/
// propertyNames/additionalProperties; dependencies (draft-07), dependentRequired/
// dependentSchemas; allOf/anyOf/oneOf/not/if/then/else; and unevaluatedProperties/
// unevaluatedItems with successful-branch annotation propagation. All numeric
// comparisons are exact. String lengths count Unicode code points. Patterns use
// Go's RE2 dialect, not the full ECMA-262 regex dialect.
// As in the dialect-specific array vocabularies, draft-07 ignores prefixItems,
// and additionalItems has no effect without draft-07 tuple items. Legacy
// dependencies is rejected outside draft-07 rather than silently skipped.
//
// Annotation keywords are $comment/title/description/default/examples/format/
// deprecated/readOnly/writeOnly/contentEncoding/contentMediaType/contentSchema.
// Defaults need not satisfy their schema and are never applied. contentSchema
// is compiled for well-formedness and references but never checks decoded
// content. x-* extensions are ignored annotations. Every other unknown keyword
// fails compilation, deliberately stricter than implementations that ignore
// unknown assertions. $vocabulary requires explicit 2020-12; unsupported
// required vocabularies, including format-assertion, fail compilation.
//
// The returned value owns all retained state. Syntax, unsupported vocabulary,
// malformed keyword values, and unresolved references wrap [ErrInvalidSchema]
// without including schema text. Resource failures also retain
// [ErrEncodedLimit]. See [EncodedSchema.Validate] for fixed allocation/work
// bounds. Compilation never validates defaults or calls application code.
//
// Example:
//
//	document := []byte(`{
//		"type":"object", "required":["n"],
//		"properties":{"n":{"type":"integer"}}
//	}`)
//	check, err := validate.FromJSONSchemaStrict(document)
//	if err != nil {
//		return err
//	}
//	return check.Validate(context.Background(), []byte(`{"n":1e1000}`))
func FromJSONSchemaStrict(document []byte) (EncodedSchema, error) {
	root, err := encoded.Compile(document)

	return EncodedSchema{root: root}, err
}

// Validate checks exactly one UTF-8 JSON document. It preserves the supplied
// bytes, distinguishes null, absent keys, and zero, and compares numbers by
// exact decimal value, including numbers outside binary64's range.
//
// A mismatch returns [Errors] (fail-fast, with a JSON Pointer and keyword code,
// without instance values or schema literals). Malformed JSON wraps
// [ErrEncodedJSON]. The zero schema wraps [ErrInvalidSchema]. Cancellation and
// deadlines retain context error identities. Resource refusals wrap
// [ErrEncodedLimit], never [Errors]; callers must keep those classes distinct.
// Do not expose returned pointers to an untrusted client without redaction.
//
// Each schema/instance is limited to 8 MiB and 256 container levels. Compilation
// allows 16,384 schema nodes. Decoding and evaluation each allow 1,000,000 work
// steps; evaluation allows 512 nested calls. Every numeric coefficient allows
// 4,096 digits and its explicit decimal exponent must be within [-4096,4096],
// including for zero. Limits are checked before big.Rat allocation; excess is
// refused, never rounded. Ordinary out-of-float64 numbers such as 1e1000 work.
// The sum of coefficient digits plus absolute exponents is limited to 64 Mi
// digits per decoded document. Structural keys for equality and uniqueness
// have a cumulative 64 MiB encoding budget per validation, including repeated
// comparisons and conservatively charged prefixes. These are data/work bounds,
// not an exact Go heap bound: slice capacity, copies, and runtime metadata also
// use memory. The limits prevent short exponent tokens multiplying storage.
// Schema URIs/references and traversed JSON Pointers are limited to 16 KiB;
// this prevents multiplicative storage growth from long nested names or IDs.
// The compiled URI index is limited to 32 MiB of keys, including aliases.
// Unproductive reference cycles are resource failures; recursive schemas that
// descend through the instance are supported. Cancellation is checked between
// tokens, child visits, branches, and references, not inside bounded standard
// library operations. A nil context is caller misuse, as with other Go APIs.
//
// Example:
//
//	check, err := validate.FromJSONSchemaStrict([]byte(`{
//		"properties":{"count":{"type":"integer"}}
//	}`))
//	if err != nil {
//		return err
//	}
//	err = check.Validate(context.Background(), []byte(`{"count":null}`))
//	var failures validate.Errors
//	if errors.As(err, &failures) {
//		_ = failures // schema mismatch; redact paths at a protocol boundary
//		return errors.New("output does not match its schema")
//	}
//	return err // cancellation, resource refusal, malformed JSON, or nil
func (schema EncodedSchema) Validate(ctx context.Context, document []byte) error {
	err := encoded.Validate(ctx, schema.root, document)

	if mismatch, ok := errors.AsType[faults.Mismatch](err); ok {
		return Errors{mismatch.Pointer: {{Code: mismatch.Keyword}}}
	}

	return err
}
