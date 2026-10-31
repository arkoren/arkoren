# Validate

[![Go Reference](https://pkg.go.dev/badge/arkoren.dev/foundation/validate.svg)](https://pkg.go.dev/arkoren.dev/foundation/validate)

An API, an import job, and an admin tool can accept the same data. To keep their
input rules consistent, `validate` checks typed Go values with reusable schemas.
It has no dependencies.
Construct schemas with typed builder callbacks; published schemas expose
`Validate`, `Parse`, and `JSONSchema`, not configuration setters.

## Installation

From your application's module directory, with Go 1.27 or later:

```sh
go get arkoren.dev/foundation/validate
```

## Check a value

Import `arkoren.dev/foundation/validate`. To require an email address:

```go
email := validate.String(func(address *validate.StringBuilder[string]) {
	address.Trim()
	address.Required()
	address.Email()
})
parsed, err := email.Parse(ctx, " ada@example.com ")
```

Here `ctx` is a `context.Context`. On success, `parsed` is `"ada@example.com"`.
`Parse` checks and returns a normalized copy; `Validate` checks the same prepared
value without returning it. Neither changes the input. Always check the error
before using a parsed result.

## Check a struct

Register Go field names with concrete child-builder callbacks:

```go
type Signup struct {
	Email string `json:"email"`
	Plan  string `json:"plan"`
}

var signup = validate.Struct(func(object *validate.StructBuilder[Signup]) {
	object.Field("Email", func(address *validate.StringBuilder[string]) {
		address.Required()
		address.Email()
	})
	object.Field("Plan", func(plan *validate.StringBuilder[string]) {
		plan.OneOf("free", "pro")
		plan.Default("free")
	})
})
```

Email is required; parsing an omitted plan supplies `"free"`. Errors use JSON
names, such as `/email`. Only registered fields are checked. Unknown fields,
wrong callback types, duplicate registrations, and invalid conditions are
construction errors: `Struct` panics with an error wrapping `ErrInvalidSchema`.
`Struct[Signup]()` registers no fields; it does not infer rules.
Unlike that constructed definition, a zero `StructSchema[Signup]` is invalid,
including when wrapped with `Ptr`.

## Reuse schemas

Child builders reuse a published schema with `Use`:

```go
recipients := validate.Slice(func(items *validate.SliceBuilder[string]) {
	items.Required()
	items.Min(1)
	items.Values(func(address *validate.StringBuilder[string]) {
		address.Use(email)
	})
})
```

Every address uses the earlier email schema. Nil and allocated empty slices
both fail here, for different reasons. Map `Keys` and `Values`, struct `Field`,
and pointer `Value` callbacks use the same typed-child pattern. `Ptr(inner, ...)`
adapts an existing `Schema[T]` to `*T`.

## Presence and assertions

Zero scalars and nil pointers or containers are absent. Ordinary rules skip
absence; `Required` rejects it. A non-nil empty collection is present, as is a
non-nil pointer to zero. Pointees and collection entries are always checked
as supplied. Thus an optional integer with `Min(1)` accepts zero; use
`Required` or a pointer when zero must be checked.

Collection `Contains`, `Every`, `Unique`, `UniqueBy`, and `CountWhere` assert
facts without rewriting input. Map `Has`, `HasAny`, and `HasAll` check keys,
independent of their values. `Filter`, `Sort`, and `Dedupe` instead transform
the parsed copy. Conditions use a `ConditionBuilder` callback and can walk
dot-separated Go field paths through structs and pointers.

## Errors and schema documents

Rule failures return `validate.Errors` keyed by RFC 6901 JSON Pointer. Each
violation has `Code`, `Params`, and `Message`. Use `errors.AsType[validate.Errors]`
and `Errors.All` for structured reporting. `Check` returns `Fail` or `At` for
violations; any other error aborts validation unchanged.

Map diagnostic paths use the JSON-encoded names of compatible keys. If keys
encode to the same name, their violations share that pointer without being lost;
tie order is unspecified. Unsupported non-JSON keys retain formatted Go names.

`Fail` and `At` snapshot acyclic parameter containers. Engine-created violations
detach supported containers before rendering and publication, including imported
`enum` and `const` parameters. `Errors.Localize` returns another detached container
copy; `Errors.Add` and `Errors.All` instead retain or lend mutable storage.
This is not arbitrary-object deep copying: see
[diagnostic ownership](../../cmd/arkoren/skills/arkoren/references/guides/validation/advanced.md#diagnostic-ownership)
for the supported containers and shared-object boundary.

`JSONSchema` describes representable rules. Conditions, custom checks,
normalizers, and collection predicates are runtime-only and omitted. `Describe`
adds a description, not a custom error message. `Infer[T]` describes Go types
without enforcing input rules; `FromJSONSchema` checks decoded JSON using a
limited keyword subset. Neither substitutes for runtime-only checks.

`Infer` preserves the native element/value shape of `collection.Slice` and
`collection.Map` through their structural `JSONSchemaType() reflect.Type`
declaration. Other custom JSON marshalers stay opaque unless they explicitly
declare the same protocol. Slices of builtin `byte` remain base64 strings;
slices of a defined `uint8` element type become JSON arrays.
Providers must return their identical unnamed slice/map type; malformed
declarations return an error wrapping `ErrInvalidSchema`.

Inference honors numeric fields tagged `string` and accepts floating map keys,
as JSON v2 does. Fields with a `format` option remain `{}` because inference
does not model formatter-specific representations. This does not imply that
your encoder supports that option; encoding can still fail.

Float32 bounds and factors retain their float32 JSON encoding precision in
exported metadata. `validate.Duration` uses the custom `go-duration` format,
enforced with `time.ParseDuration` on application import. Standard `duration`
instead uses RFC 3339 Appendix A; the payload strings themselves are unchanged.

For runtime rules on a named container, use `Named[YourType](sourceSchema)` or
a `NamedBuilder[YourType]` child callback with `Use(sourceSchema)`. The source
and target must have identical underlying slice/map types. Parsing preserves
the named result and the source's normalization, defaults, and input ownership.
See [named containers](../../cmd/arkoren/skills/arkoren/references/guides/validation/collections.md#checking-named-containers)
for field registration and the distinction between runtime rules and wire metadata.

Invalid explicit definitions return nil document bytes and an error wrapping
`ErrInvalidSchema` from `JSONSchema`; standalone `Parse` and `Validate` instead
panic when compilation finds such errors. Export does not run validation
callbacks, replay construction callbacks, or verify or normalize default
annotations. JSON encoding failures preserve the encoder's cause. A successful
export is not a promise of numeric, default, or runtime-rule equivalence with a
JSON Schema consumer; check that consumer's errors separately.

## Check encoded JSON without transformations

Use `FromJSONSchemaStrict` when the contract must describe the original JSON
bytes, not a normalized/defaulted application value:

```go
check, err := validate.FromJSONSchemaStrict([]byte(`{
	"type":"object","required":["count"],
	"properties":{"count":{"type":"integer"}}
}`))
err = check.Validate(ctx, []byte(`{"count":9007199254740993}`))
```

This passes without changing the document. Missing, null, and zero remain
distinct. Numeric constraints and nested equality/uniqueness compare exact
decimal values; defaults, formats, and content keywords are annotations.
Unsupported assertions and external references fail compilation without I/O.
Existing typed schemas and `FromJSONSchema` keep their application semantics.

`validate.Errors` identifies a mismatch. Malformed JSON wraps `ErrEncodedJSON`,
resource refusals wrap `ErrEncodedLimit`, and cancellation retains its context
error identity; none of those operational failures is a schema violation.
The immutable `EncodedSchema` is safe for concurrent use and has no `Parse`.

Validation has fixed byte/depth/work/numeric limits. Before rational allocation,
each decoded document is limited to a sum of 64 × 1024 × 1024 coefficient digits
plus absolute exponents. Structural-key encoding has a cumulative 64 MiB data
budget per validation, not an exact Go heap cap. See
[Encoded JSON Validation](../../cmd/arkoren/skills/arkoren/references/guides/validation/encoded-json.md)
for the supported profile, complete resource bounds, and safe error handling.

## Construction and ownership

Configurable constructors and child declarations take variadic callbacks. They run once,
synchronously, in order on one fresh draft per declaration. An expanded empty
or nil callback slice supplies no edits; a supplied nil function panics.
Every role has a matching `BuilderFunc` alias, including generic roles such
as `StringBuilderFunc[S]` and `StructBuilderFunc[T]`.

Recorded construction problems are sticky: later settings, `Use`, and publication
do not erase them. `Struct` aggregates them after declaration callbacks finish.
String length bounds must be nonnegative; floating constraint bounds and factors
must be finite, and `MultipleOf` factors must be positive. Negative string
lengths, negative factors, and non-finite floating bounds or factors are recorded
for that report, or for standalone execution/export to reject. `MultipleOf(0)`
retains its immediate construction panic; not every invalid setter is deferred.

Type arguments are inferred from callback parameter types. Without callbacks,
write `String[string]()`, `Int[int]()`, or another explicit type. There are
no separate `Of` or enum constructors; use `String` with `OneOf` for enums.

Published schemas detach owned state and are safe to share. Arbitrary defaults
and function captures are not recursively cloned; custom checks and predicates
must be concurrency-safe. Normalizers run before presence, defaults, and rules;
defaults must already be normalized. Map keys cannot be rewritten.

Preparation runs once per value during validation, including nested struct
fields and collection children. Rules consume that prepared value without
replaying normalizers or child preparation. A fatal error stops subsequent
execution before another unprepared value can be mutated.

## Documentation

Start with the
[validation overview](../../cmd/arkoren/skills/arkoren/references/guides/validation/overview.md).
Continue with
[scalar rules](../../cmd/arkoren/skills/arkoren/references/guides/validation/available-methods.md),
[structs and reuse](../../cmd/arkoren/skills/arkoren/references/guides/validation/structs-and-reuse.md),
[collections](../../cmd/arkoren/skills/arkoren/references/guides/validation/collections.md), and
[conditions](../../cmd/arkoren/skills/arkoren/references/guides/validation/conditions.md).
The [advanced guide](../../cmd/arkoren/skills/arkoren/references/guides/validation/advanced.md)
covers custom checks, files, JSON Schema limits, and ownership. The
[API reference](https://pkg.go.dev/arkoren.dev/foundation/validate)
records the exact contracts.
