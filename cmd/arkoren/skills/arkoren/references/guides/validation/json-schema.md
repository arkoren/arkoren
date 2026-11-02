---
title: JSON Schema
description: Export representable rules, infer output shapes, or validate decoded JSON with explicit interchange limits.
order: 90
summary:
  - Export rule metadata without mistaking it for the complete Go validator.
  - Infer descriptive output shapes without adding runtime validation.
  - Import decoded-JSON contracts with explicit presence, precision, and resource policies.
---

# JSON Schema

Clients and tools may need an input description without executing Go rules.
`JSONSchema` exports the representable constraints of your typed schema.
`Infer` instead describes a Go output type, while `FromJSONSchema` constructs
a validator for decoded JSON. These are different tasks, not a lossless
round trip. For exact document bytes without defaulting, use
[Encoded JSON](encoded-json.md).

Examples import `arkoren.dev/foundation/validate` and use an existing context
named `ctx`.

## Export Rules

Describe a display name alongside its runtime bounds:

```go
name := validate.String(func(text *validate.StringBuilder[string]) {
	text.Min(3)
	text.Max(80)
	text.Describe("Public display name")
})
document, err := name.JSONSchema()
```

On success, the document describes a string, both length bounds, and the
description. `Describe` sets metadata, not an error message. Export is
deterministic with sorted object keys and a 2020-12 subset; it does not add
provider-specific strict mode or `additionalProperties: false`.

Check `err` before publishing bytes. Export checks executable definitions,
including omitted runtime-only and map-key rules. Invalid definitions return
nil bytes and an error wrapping `ErrInvalidSchema`; encoding failures preserve
the JSON encoder's cause. Export never runs custom checks, normalizers,
condition/collection predicates, or declaration callbacks. It does not verify
defaults against their rules or normalize default annotations.

## Understand Export Limits

Keep the Go schema authoritative for application validation. Conditions,
cross-field comparisons, custom checks, normalizers, and predicate collection
rules are omitted. Not every time, string, file, or map-key constraint can be
represented. `URL` does not preserve its allowed scheme list; `Unique` can
export `uniqueItems`, but `UniqueBy` cannot export a Go projection.

Even representable settings need not round-trip. Runtime rules accumulate,
while repeated annotations use the last value. Go zeros can be absent where
decoded JSON zeros are checked. An exported default may violate emitted
constraints and fail import even though export succeeded. A Go regexp is
copied verbatim, but RE2-only syntax may differ from another validator's
ECMA-262 interpretation. Check downstream import and validation results.

## Describe Output

A response type needs shape metadata without input-presence rules. Use `Infer`:

```go
type Result struct {
	Name    string            `json:"name"`
	Elapsed validate.Duration `json:"elapsed"`
	Note    string            `json:"note,omitempty"`
}
result := validate.Infer[Result]()
document, err := result.JSONSchema()
```

The document describes `Elapsed` as a string with format `go-duration`.
Ordinary fields are listed as required unless omitted by `omitempty`,
`omitzero`, or a nil-capable embedded owner. This is descriptive: inferred
`Validate` adds no value checks, and `Parse` returns its input unchanged rather
than copying arbitrary objects. Both still respect cancellation.

Inference follows default JSON-v2 type encoding: text-marshaled values are
strings, `time.Time` is `date-time`, builtin-byte sequences are strings, and
ordinary structs, arrays, slices, maps, and pointers compose. Defined `uint8`
elements become array elements rather than builtin bytes. Numeric fields tagged
`string` become strings; fields with a `format` option remain `{}` because
formatter-specific encodings are not modeled. Recursive types stop at a repeated
occurrence with `{}`, not `$ref`.

Custom JSON marshalers remain opaque `{}` unless a named container explicitly
declares its native shape through `JSONSchemaType() reflect.Type`. Eager
foundation `collection.Slice` and `collection.Map` implement that protocol;
lazy collections do not. A provider must deterministically return its identical
unnamed container type independent of receiver contents. Nil, mismatched results,
or provider panics become inference errors.

Raw `time.Duration`, channels, functions, complex numbers, and unsupported map
keys cannot be inferred. `result.Err()`, execution, and export return an error
wrapping `ErrInvalidSchema` for unsupported types. Shape metadata neither proves
that a particular value encodes nor models every encoder option. For runtime
named-container constraints, use [Named](collections.md#named-containers).

## Import a Contract

Use `FromJSONSchema` when your input is already decoded JSON rather than a typed
Go form. A small contract can bound a list of tags:

```go
tags, err := validate.FromJSONSchema([]byte(`{
	"type": "array",
	"maxItems": 10,
	"items": {"type": "string", "minLength": 1}
}`))
```

Check construction errors before using the validator. With `tags` successfully
constructed, validate decoded values directly:

```go
err = tags.Validate(ctx, []any{"go", "queues"})
```

This passes. Use `Parse` to retain a copied result with defaults. The input is
never modified. A zero imported validator is not usable.

The importer accepts a deliberate subset: single-string `type`; `properties`,
`required`, schema-valued `additionalProperties`, and `items`; `enum`, `const`,
`default`, `description`, `format`, and `pattern`; string-length, numeric,
array-size, and object-size bounds; `multipleOf`; and `uniqueItems`. It rejects
unknown keywords, `$schema`, `$ref`, compositions such as `oneOf`, boolean
schemas, type unions, and malformed keyword values with `ErrInvalidSchema`.
The error identifies the keyword's JSON Pointer. A pattern must be a string:
empty is valid, null is not. Patterns use Go regular expressions.

Input types are `map[string]any`, `[]any`, `string`, `float64`, `bool`, and nil.
Go `int`, `int64`, and valid numeric `encoding/json/jsontext.Value` tokens also
count as numbers. This is not reflection-based validation of arbitrary structs.
Integer schemas reject fractions; keywords without `type` apply only to their
own kinds.

## Decoded Presence

Imported application validation deliberately differs from strict JSON Schema
instance checking. `required` checks whether an object key exists before
defaults: null, empty text, and zero satisfy key existence. Null is otherwise
absent and satisfies every constraint regardless of `type`; `Parse` replaces it
with a default when one exists. Missing properties can also receive defaults,
but a missing required key still fails. Zero scalars are supplied, so
`minLength: 1` rejects an empty string.

The importer asserts `date-time` (RFC 3339), `email`, `uri` (absolute, with host,
any scheme), `uuid`, `go-duration` (`time.ParseDuration`), and standard `duration`
(RFC 3339 Appendix A). The duration grammars are not interchangeable. Other
formats annotate only. The separate encoded validator treats all formats as
annotations, so choose the boundary deliberately.

## Precision and Uniqueness

Imported numeric literals retain exact decimal values and their original spelling
for comparisons and rerendering. Numeric defaults and diagnostic parameters use
`float64` only when its JSON spelling preserves the value; otherwise they use
owned `jsontext.Value` tokens. Supplied Go numbers retain their concrete types.
Already rounded floating-point input cannot recover lost digits.

`uniqueItems` uses exact structural keys rather than pairwise comparison.
Object key order is irrelevant, array order matters, and numbers compare by
exact decimal value at every nesting level, including equivalent spellings and
signed zero. Uniqueness checks original items before defaults: parsed defaults
can make previously distinct items equal. Unsupported values or non-finite
numbers inside an item report `type` at its pointer.

For fixed-size scalar entries, expected uniqueness work/storage is linear;
nested objects add encoded-size and key-sorting work. Rerendering sorts object
keys while preserving numeric spelling; it is not RFC 8785 canonicalization.

## Bound Resource Use

For untrusted contracts or decoded input, independently limit document bytes,
numeric digits/exponents, collection sizes, nesting, and concurrent validations.
Input must be finite and acyclic. The decoded validator has no implicit global
work or memory budget; compilation has no context parameter. Context cancellation
alone is not a resource policy.

Use `maxItems` when the array cardinality is known. After type admission, an
oversized array reports only `slice.max` for that array and skips uniqueness
and child traversal; sibling values are still checked.

Pass the caller's context to execution. Traversal, equality, uniqueness, sorting,
and copying observe cancellation, which returns `ctx.Err()` and discards
accumulated violations; `Parse` returns nil. Individual scalar operations and
allocations are not preempted. Use [Encoded JSON](encoded-json.md) when fixed
byte-validation budgets and exact wire semantics are the required boundary.

For the full export, inference, and import contracts, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
