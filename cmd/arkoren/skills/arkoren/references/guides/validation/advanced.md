---
title: Advanced Validation
nav: Advanced Use Cases
description: Custom checks, error translation, files, JSON Schema limits, and safe schema ownership.
order: 80
---

# Advanced Validation

## Introduction

Use built-in rules for ordinary input constraints. This page covers the
cases that need application logic or an explicit boundary: custom checks,
file inspection, translated errors, and JSON Schema interchange.
For checking an emitted document without transformations, use
[Encoded JSON Validation](encoded-json.md).

Examples use an existing `context.Context` named `ctx`.
The [overview](overview.md) covers the smaller
validation workflow; [normalizers](normalizers.md) explains preparation order.

## Custom checks

A username can satisfy every format rule and still be reserved by your
application. Add that application-specific rule with `Check`. Its callback
receives the prepared value and the caller's context; return nil to accept it
or `Fail` to report a violation:

```go
username := validate.String(func(name *validate.StringBuilder[string]) {
	name.Check(func(_ context.Context, value string) error {
		if value == "admin" {
			return validate.Fail("username.reserved", nil)
		}
		return nil
	})
})
```

`"admin"` produces `username.reserved`. Add that code to your message catalog.
`Fail` parameters supply message placeholders. They must be JSON-serializable
if you encode the diagnostics; `Fail` itself does not validate or encode them.
An absent scalar skips its ordinary custom checks.

Any other returned error is operational: validation stops and returns it
unchanged instead of converting it into a user-input violation. Pass `ctx`
to storage or network calls. Do not report a failed lookup as proof that a
value is invalid. Apply `Bail` to skip an expensive check after cheap rules fail.

### Diagnostic ownership

You can reuse an error returned by `Fail` or `At` across checks. Each helper
captures its parameter containers. Before rendering and publishing a violation,
the engine detaches supported parameter containers again, including parameters
from built-in and imported `enum` or `const` rules. Mutating one result's copied
parameters does not rewrite the schema, a reusable failure, or another result.
`Errors.Localize(ctx)` also copies those containers, including when an unknown
code keeps its existing message.

Copying traverses acyclic slices, arrays, and maps with string-kind keys,
including nested containers reached through interfaces. Named container and
string-key types, scalar values, and nil-versus-empty distinctions are preserved.
This is container copying, not JSON conversion or a general object deep copy;
cycles along copied containers are unsupported and are not detected.

Struct fields, pointers, and non-string-keyed maps are not traversed. Opaque
native objects, functions, and captures remain application-owned and may still
refer to shared data. Keep them immutable or synchronize access yourself.
The copying helpers do not invoke custom marshaling methods.

`Errors` itself is mutable. `Errors.Add` retains a violation's supplied parameter
storage, and `Errors.All` yields borrowed violation slices and parameters. Neither
operation makes a snapshot. Use `Localize` when you need a separate diagnostic
container copy, within the limits above, and do not mutate shared diagnostics
concurrently with readers.

## Builder operations and integration helpers

### Builder settings

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Check(func(context.Context, T) error)` | Rule builders (not `NamedBuilder`): validate the role's prepared value | [Custom checks](#custom-checks) |
| `StructBuilder[T].Check(fn)` | Object-wide invariant before child field rules, not a final hook | [Object-wide checks](#object-wide-checks) |
| `Bail()` | Stop this value's checks after its first violation | [Check ordering](#custom-checks) |
| `Describe(text)` | Annotate a schema; does not customize violation messages | [JSON Schema](#json-schema) |
| `Normalize(fn)` | Replace a value before checks; no map keys or count constraints | [Preparation order](#preparation-and-rule-order) |
| `FileBuilder.Min(bytes)`, `Max(bytes)`, `Extensions(exts...)`, `Types(types...)` | File-size, filename-extension, and detected-content assertions | [Files](#validating-files) |

For the rest of each role's permitted settings, use the
[scalar](available-methods.md#builder-operations),
[struct/pointer](structs-and-reuse.md#builder-operations), and
[collection](collections.md#builder-operations) tables.

### Helpers and schema execution

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Fail(code, params)` | Package helper: return a violation at the checked value | [Custom checks](#custom-checks) |
| `At(field, code, params)` | Package helper: attach an object-check violation to a Go field | [Object-wide checks](#object-wide-checks) |
| `NewCatalogContext`, `NewLocaleContext`, `Errors.Localize(ctx)` | Context helpers and error-value translation, not builder methods | [Translation](#localizing-custom-errors) |
| `schema.JSONSchema()` | Published schema: export only representable constraints | [JSON Schema](#json-schema) |
| `Infer[T]()` | Constructor: describe a Go type without input rules | [Output descriptions](#describing-types-without-rules) |
| `FromJSONSchema(document)` | Constructor: validate decoded JSON with a limited keyword subset | [Decoded JSON](#validating-decoded-json) |
| `schema.Validate(ctx, value)`, `schema.Parse(ctx, value)` | Published schema: check input; parsing also returns the prepared copy | [Schema execution](schemas-and-helpers.md#running-and-describing-schemas) |

## Object-wide checks

Some rules concern the relationship between values rather than either field on
its own. An interval, for example, must not end before it starts. An object-wide
`Check` can inspect both values and attach the failure to the end field:

```go
type Interval struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

interval := validate.Struct(func(object *validate.StructBuilder[Interval]) {
	object.Check(func(_ context.Context, value Interval) error {
		if value.End < value.Start {
			return validate.At("End", "interval.order", nil)
		}
		return nil
	})
})
```

The violation appears at `/end`. `At` takes a Go field name, not a JSON
Pointer; an unknown name is an invalid-schema error. For a simple numeric
comparison, prefer `GteField("Start")` on an end-field builder.

An object-wide check runs after unconditional preparation and defaults but
before its child fields' rules. It does not see those fields' later `When`
or `ExcludeIf` effects. Keep the invariant independent of conditional effects;
an object-wide check is not a final post-validation hook.

## Localizing custom errors

If your interface supports several languages, callers need the same failure in
their selected language without changing the code your application matches.
Attach translations to a context, using the same codes returned by `Fail`:

```go
catalog := validate.Catalog{
	Default: "en",
	Locales: map[string]validate.Messages{
		"en": {"username.reserved": "This username is reserved."},
		"es": {"username.reserved": "Este nombre de usuario está reservado."},
	},
}
ctx = validate.NewCatalogContext(ctx, catalog)
ctx = validate.NewLocaleContext(ctx, "es")
err := username.Validate(ctx, "admin")
```

The message is Spanish; the violation code stays `username.reserved`.
Fallback proceeds through the selected locale's base language, the catalog
default and its base language, built-in English, and finally the code.
See [locale helpers](schemas-and-helpers.md#locale-helpers)
for reading and copying catalogs. In HTTP applications, the
[locale middleware](../http-capabilities/locale.md) attaches these values per request.

`Describe` is unrelated to translation. It adds a JSON Schema description;
it does not override an error message.

## Validating files

`File` checks a decoded multipart file header. To require a JPEG upload:

```go
avatar := validate.File(func(upload *validate.FileBuilder) {
	upload.Required()
	upload.Max(2 << 20)
	upload.Extensions("jpg", "jpeg")
	upload.Types("image/jpeg")
})
```

Nil is absent. `Max` uses the size counted by the multipart parser, in bytes.
`Types` opens the file and detects content from the first 512 bytes with
`http.DetectContentType`; it does not trust the client's header. It accepts
exact types or wildcards, ignoring parameters such as `charset`.
An open or read failure aborts validation with that error.

Detection is coarse. Office documents detect as `application/zip`, so combine
that type with a specific extension such as `docx`. SVG can contain scripts
and is not treated as an image; there is no `Image` shorthand. File validation
does not replace antivirus checks, safe storage names, or request body limits.
See [multipart requests](../http-fundamentals/requests.md#file-uploads) for decoding.

## JSON Schema

Clients and tools may need to inspect your input contract without running Go
validation. `JSONSchema` renders the representable subset as a document. To
describe a bounded string:

```go
name := validate.String(func(value *validate.StringBuilder[string]) {
	value.Min(3)
	value.Max(80)
	value.Describe("Public display name")
})
document, err := name.JSONSchema()
```

On success, the document has `type: string`, length bounds, and a description.
Check `err` before publishing it. The output is provider-neutral; it does not
add strict mode or `additionalProperties: false`.

Export checks the executable definition, including omitted runtime-only rules
and map-key rules. Recorded errors, unbound field references, or an uninitialized
composite return nil bytes and an error wrapping `ErrInvalidSchema`, rather than
a usable document. A zero `StructSchema[T]` is invalid even through `Ptr`; use
`Struct[T]()` for a constructed schema with no registered fields. See
[definition errors](schemas-and-helpers.md#definition-errors) for invalid rule
arguments and the separate standalone-execution panic behavior.

These definition checks never run `Check`, `Normalize`, condition predicates, or
collection predicates, and never replay construction callbacks. Export does not
verify defaults against their rules or normalize default annotations. Encoding
a value that JSON cannot represent instead returns nil bytes and preserves the
encoder's error cause.

### Runtime-only rules

Conditions, field comparisons, custom checks, normalizers, and predicate-based
collection assertions are omitted. The document also cannot express every
string, time, file, or map-key rule. In particular, `URL`'s allowed scheme set
is not preserved. `Unique` can render `uniqueItems`; `UniqueBy` cannot render
an arbitrary Go projection.

Keep the Go schema as the authoritative validator. Exporting a document and
importing it again does not preserve those runtime checks. A Go regexp is
copied verbatim; RE2-only syntax may mean something different to another
JSON Schema implementation. Non-finite numeric bounds and factors are recorded
definition errors, not valid exported limits.

Even represented rules are not a general lossless round trip. Go scalar zeros
can count as absent while decoded JSON zeros are checked. Repeated annotations
use the last value, even though runtime rules accumulate. Default annotations
are not checked against the exported constraints, so an importer can reject a
default after export succeeds. Numeric consumers can also differ: this package's
importer retains schema literals exactly, but cannot recover values already
rounded by a caller's `float64` decoder. [Typed floating multiples](available-methods.md#numeric-bounds)
use the exact decimal values of the Go floats' shortest JSON spellings. Check
import errors and do not assume numeric or default equivalence between the Go
schema and a downstream consumer.

### Describing types without rules

`Infer[T]` describes a type without imposing input rules. To describe output:

```go
type Result struct {
	Name    string            `json:"name"`
	Elapsed validate.Duration `json:"elapsed"`
}

document, err := validate.Infer[Result]().JSONSchema()
```

The Go duration is a string with the custom format `go-duration`. Struct fields are listed as
required unless tagged `omitempty` or `omitzero`, but this is descriptive:
inferred-schema `Validate` accepts supported typed values without checking
presence. Unsupported types return an error wrapping `ErrInvalidSchema`.

Types follow JSON v2 encoding: text-marshaled types are strings, `time.Time`
is `date-time`, `[]byte` is a string, and ordinary structs, slices, arrays,
maps, and pointers compose. Recursive types stop at their repeated occurrence
with `{}`, not `$ref`. Raw `time.Duration`, channels, functions, complex
numbers, and unsupported map keys are invalid.

Eager `collection.Slice[T]` and `collection.Map[K, V]` retain their native
element/value shapes, including when nested in result structs. They declare
`JSONSchemaType() reflect.Type`, a structural protocol recognized without a
dependency between foundation modules. A provider must return its identical
unnamed slice/map type, independently of receiver contents. Nil results,
mismatched types, and provider panics become errors wrapping `ErrInvalidSchema`.
Slices of builtin `byte` remain base64 strings; slices of a defined `uint8`
element type become arrays. Custom JSON marshalers
without this explicit declaration remain `{}`; their underlying Go kind does
not prove their wire shape. Lazy collection types do not implement the protocol.

Numeric fields tagged `string` are described as strings, including inside
collection elements. Floating map keys are supported. Fields with a `format`
option remain `{}`: inference does not model formatter-specific representations
or guarantee that your encoder accepts the option.

This describes default type-level encoding, not every encoder option or runtime
value. To enforce runtime rules on a named container, use the
[named-container adapter](collections.md#checking-named-containers) instead.

### Validating decoded JSON

Use `FromJSONSchema` for decoded JSON values rather than typed Go structs:

```go
schema, err := validate.FromJSONSchema([]byte(`{"type":"array","maxItems":10,"items":{"type":"string"}}`))
err = schema.Validate(ctx, []any{"go", "queues"})
```

Check the construction error before using `schema` in application code. This
input passes. The API supports a deliberate subset, not the complete JSON
Schema specification. Unsupported keywords such as `$ref`, `$schema`, and
`oneOf` fail construction, rather than being silently ignored. See the
[accepted keyword profile](#decoded-json-keywords).

`Parse` returns a copied decoded value with defaults applied. Presence differs
from typed schemas: `required` means a key exists, so `null`, `""`, and `0`
satisfy it. Null is absent and takes a default; zero scalars are checked,
so `minLength: 1` rejects `""`.

### Decoded JSON keywords

The importer accepts `type` as one string, object `properties`, `required`,
schema-valued `additionalProperties`, `items`, `enum`, `const`, `default`,
`description`, `format`, `pattern`, string-length bounds, numeric inclusive and
exclusive bounds, `multipleOf`, array-size bounds, `uniqueItems`, and object-size
bounds. It rejects type arrays, boolean schemas, unknown keywords, and malformed
keyword shapes with `ErrInvalidSchema`, naming the keyword and JSON Pointer.
Integer schemas reject fractions.

`pattern` must contain a string. An empty string is valid and matches every
string; null, booleans, numbers, arrays, and objects fail construction instead
of becoming an empty pattern. Errors wrap `ErrInvalidSchema` and identify the
schema location, such as `/properties/name/pattern`; `/` and `~` in property
names are escaped as `~1` and `~0`. Invalid regexp syntax also fails at that
keyword location. Patterns use Go regular expressions, with the dialect caveat
[described above](#runtime-only-rules).

Input uses decoded JSON types: `map[string]any`, `[]any`, `string`, `float64`,
`bool`, and nil; Go `int`, `int64`, and valid numeric `jsontext.Value` tokens
also count as numbers. Imported numeric defaults and diagnostic parameters use
`float64` only when its JSON spelling preserves the exact value; otherwise they
use owned `jsontext.Value` tokens. Runtime formats
enforced are RFC 3339 `date-time`, `email`, `uri` (an absolute URL with host and
any scheme), `uuid`, `go-duration` (`time.ParseDuration`), and standard `duration`
(RFC 3339 Appendix A). These two duration grammars are not interchangeable;
changing the annotation does not change the payload string. Other formats only
annotate. This application importer asserts these formats; the separate
[encoded validator](encoded-json.md) treats formats as annotations.
`JSONSchema` rerenders sorted object keys while retaining numeric spelling;
it does not recover runtime-only Go rules.

### Resource limits

When accepting untrusted documents or decoded values, independently limit
document bytes, numeric digit/exponent sizes, decoded collection sizes, nesting,
and concurrency. Use finite,
acyclic JSON-like input. Context cancellation is not an overall memory or
work budget, and schema construction has no context.

Pass the caller's context to `Validate` and `Parse`. Decoded-JSON traversal,
equality, uniqueness, object-key sorting, and copying check cancellation.
A canceled call returns `ctx.Err()` instead of accumulated violations;
`Parse` returns nil. Individual scalar operations and allocations are not
preempted.

Set `maxItems` for known cardinality limits. An oversized decoded array reports
only `slice.max` for that array, skipping uniqueness and child traversal;
siblings are still checked. Without that bound, `uniqueItems` uses structural
keys rather than pairwise comparisons. Fixed-size scalar items have expected
linear work and key storage; nested objects add encoded-size and key-sorting
work. There is no implicit global budget.

Object order does not affect structural uniqueness; array order does. Supported
numbers compare by exact decimal value at every nesting level, including
equivalent numeric spellings and signed zero, without changing their concrete
Go types. Valid numeric `jsontext.Value` tokens retain digits beyond `float64`
precision; previously rounded Go values cannot recover those digits.
Uniqueness is checked before defaults, so parsed defaults can make formerly
distinct items equal. Unsupported values or non-finite numbers inside a
uniqueness item report `type` at that item's pointer.

## Preparation and rule order

Unconditional normalizers run before presence and defaults, in declaration
order among themselves. Defaults are not renormalized. Nested children prepare
before enclosing field conditions, so conditions see prepared sibling values.

On slices, entry normalizers run before container normalizers; container
assertions and entry rules follow. Paths use resulting indices. On maps,
values prepare before collection assertions; keys are never rewritten.
Rules collect violations in declaration order unless `Bail` stops the value
after its first failure. Conditional effects are not an unordered fixed point;
see [condition order](conditions.md#preparation-and-effect-order).

## Concurrency and ownership

Published schemas are immutable and reusable across goroutines. Construction
builders are mutable drafts, not runtime configuration objects. Do not use
a retained builder to coordinate requests. Publication detaches owned schema
containers, so later draft edits cannot change a published schema.

Immutability does not recursively clone arbitrary defaults, functions, or
their captures. Treat shared maps, pointers, catalogs, and captured state as
immutable or synchronize access yourself. Custom checks, predicates,
projections, and normalizers must be concurrency-safe. Normalizers must not
mutate caller-owned nested data.

For the complete contract and keyword list, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
