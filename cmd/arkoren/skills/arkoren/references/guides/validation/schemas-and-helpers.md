---
title: Validation Schemas and Helpers
nav: Schemas & Helpers
description: Choose typed construction callbacks, run schemas, and use locale, clock, and duration helpers.
order: 70
---

# Validation Schemas and Helpers

## Introduction

Choose a schema that matches the exact type of your input. Construction
callbacks configure a temporary builder; the result is a reusable schema.
The [overview](overview.md) teaches the ordinary validation workflow.

Examples use a `context.Context` named `ctx`. This page indexes constructors and helpers;
[scalar rules](available-methods.md), [collections](collections.md), and
[conditions](conditions.md) cover builder methods.

## Schema and helper operations

Constructors publish schemas. Execution methods belong to those schemas;
context and error helpers are separate APIs, not builder settings.

### Constructors and published schemas

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `String`, `Int`, `Uint`, `Float`, `Bool`, `Time`, `File`, `Text[T]`, `Any` | Construct scalar schemas with the corresponding concrete builders | [Scalar constructors](#scalar-constructors) |
| `Struct`, `Slice`, `Map`, `Ptr` | Construct nested or optional schemas | [Composite constructors](#composite-constructors) |
| `Validate(ctx, value)` | Published schema: check prepared input without returning it | [Validate](#validate) |
| `Parse(ctx, value)` | Published schema: check and return the prepared copy | [Parse](#parse) |
| `JSONSchema()` | Published schema: return representable document bytes and an error | [Schema documents](#jsonschema-and-describe) |
| `Infer[T]()` and `InferSchema[T].Err()` | Describe a type without input rules; inspect unsupported-type errors | [Inference](#infer-and-err) |
| `FromJSONSchema(document)` | Construct a separate validator for decoded JSON | [Decoded JSON](#fromjsonschema) |
| `FromJSONSchemaStrict(document)` and `EncodedSchema.Validate(ctx, document)` | Compile and check exact JSON bytes without transformations | [Encoded JSON](encoded-json.md) |

### Text and Any builders

`TextBuilder[T]` and `AnyBuilder` have no additional format or numeric rule
methods. Their common operations act on an already decoded T or `any` value.

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Use(schema)`, `Required()`, `Default(value)` | Import supported rules or configure presence/defaults | [Common operations](overview.md#builder-operations) |
| `Check(fn)`, `Bail()`, `Normalize(fn)`, `Describe(text)` | Custom validation, failure control, replacement, and annotation | [Common operations](overview.md#builder-operations) |
| `EqField`, `NeField`, `RequiredIf`, `RequiredUnless`, `RequiredWith`, `RequiredWithAll`, `RequiredWithout`, `ProhibitedIf`, `ProhibitedUnless`, `ExcludeIf`, `When` | Field-dependent rules in an enclosing struct | [Field conditions](conditions.md#field-builder-operations) |

### Error, context, and text helpers

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Fail(code, params)`, `At(field, code, params)` | Package helpers returned by custom checks | [Custom-check errors](#errors-and-custom-checks) |
| `Errors.Error()`, `Errors.All()`, `Errors.Add(pointer, violation)`, `Errors.Localize(ctx)` | Error value: summarize, iterate, append, or translate violations | [Error operations](#errors-and-custom-checks) |
| `English()`, `NewCatalogContext`, `NewLocaleContext`, `LocaleFromContext` | Package helpers: copy messages and select/read context-local translation | [Locale helpers](#locale-helpers) |
| `NewClockContext(ctx, now)` | Package helper: supply the clock for time rules | [Clock helpers](#clock-helpers) |
| `Duration.MarshalText`, `(*Duration).UnmarshalText`, `Duration.Std`, `Duration.JSONSchemaFormat` | Duration value/pointer methods, not validation-builder methods | [Duration text](#duration-text) |

## Scalar constructors

`String`, `Int`, `Uint`, `Float`, and `Bool` preserve named Go types as well
as built-in types. The callback's concrete builder type lets Go infer the
constructor's type argument. To validate a named identifier:

```go
type RecordID uint64

identifier := validate.Uint(func(value *validate.UintBuilder[RecordID]) {
	value.Positive()
})
```

Parsing returns a `RecordID`, not a `uint64`. There is no automatic conversion
between a field type and a schema type.

When no callback supplies a type, write the type argument explicitly:

```go
text := validate.String[string]()
count := validate.Int[int64]()
ratio := validate.Float[float32]()
flag := validate.Bool[bool]()
```

These schemas add no rules. They replace neither decoding nor type conversion.

| Constructor | Builder | Accepted type |
| --- | --- | --- |
| `String` | `StringBuilder[S]` | `~string` |
| `Int` | `IntBuilder[N]` | Signed integers |
| `Uint` | `UintBuilder[N]` | Unsigned integers |
| `Float` | `FloatBuilder[N]` | `~float32` or `~float64` |
| `Bool` | `BoolBuilder[B]` | `~bool` |
| `Time` | `TimeBuilder` | `time.Time` |
| `File` | `FileBuilder` | `*multipart.FileHeader` |
| `Any` | `AnyBuilder` | `any` |
| `Text[T]` | `TextBuilder[T]` | Pointer to T implements text marshaling and unmarshaling |

There is no separate `Enum` constructor: use `String` with `OneOf`.
`StringOf`, `IntOf`, `UintOf`, `FloatOf`, and `BoolOf` are not separate APIs.

## Composite constructors

| Constructor | Configuration | Purpose |
| --- | --- | --- |
| `Struct` | `StructBuilder[T]`, with `Field` callbacks | Selected fields of T |
| `Slice` | `SliceBuilder[E]`, with `Values` callbacks | `[]E` |
| `Map` | `MapBuilder[K, V]`, with `Keys` and `Values` callbacks | `map[K]V` |
| `Ptr(inner, ...)` | `PtrBuilder[T]` callbacks after a `Schema[T]` | `*T` |

Child callbacks receive the concrete role for that child. For a `[]string`,
`Values` receives a `*StringBuilder[string]`; for a nested struct field,
`Field` receives a `*StructBuilder[Child]`. Reuse a published schema with
the child's `Use(schema)` method, as shown in
[structs and reuse](structs-and-reuse.md#reusing-a-schema).

## Callback execution

Constructors, `Field`, `Keys`, `Values`, and pointer `Value` accept variadic
callbacks. Each supplied callback runs directly once, synchronously, in order on
the same fresh draft for that declaration. To share a string policy:

```go
var policy validate.StringBuilderFunc[string] = func(name *validate.StringBuilder[string]) {
	name.Trim()
	name.Max(80)
}

name := validate.String(policy, func(value *validate.StringBuilder[string]) {
	value.Required()
})
```

Both callbacks configure `name`; neither runs again when you validate input.
Each exported builder role has a matching `BuilderFunc` alias with the same
type parameters. Expanded nil or empty callback slices supply no callbacks.
A supplied nil function panics; application callback panics propagate.

Builders are construction-only. Publication detaches owned schema state so
a retained draft cannot edit a published schema. It does not recursively
clone arbitrary defaults or function captures. See
[ownership](advanced.md#concurrency-and-ownership).

## Definition errors

A bad schema is a programming error, not invalid user input. Recorded problems,
such as `Use(nil)`, negative string lengths, negative `MultipleOf` factors, or
non-finite floating bounds and factors, remain attached to the definition.
Later valid settings, `Use`, and detached publication cannot clear them.
`Struct` collects recorded rule and registration problems after declaration
callbacks finish, then panics with an error wrapping `ErrInvalidSchema`.
Its diagnostics sort by Go field name, retaining declaration order for ties.

Standalone `Parse` and `Validate` on an invalid explicit schema panic when they
compile the definition. They do not return `validate.Errors` for that mistake.
`JSONSchema` instead returns nil bytes and an error wrapping `ErrInvalidSchema`.
`Infer[T]` is different: unsupported-type errors are returned by its `Err`,
execution, and export methods rather than raised as construction panics.

Not every invalid argument is deferred. `MultipleOf(0)` still panics at the
setter, before later callbacks run. Other existing immediate checks, supplied
nil callbacks, and application panics are not converted into recorded errors.
Construct composites with their constructors: in particular, a zero
`StructSchema[T]` remains invalid through the `Ptr` adapter. Omitted child rules
on constructed slice and map schemas are valid and mean no per-entry validation.

## Running and describing schemas

### Validate

`Validate(ctx, value)` checks the normalized, defaulted value without returning
it. To check an integer:

```go
count := validate.Int(func(value *validate.IntBuilder[int]) {
	value.Min(1)
})
err := count.Validate(ctx, 2)
```

This passes. For a valid definition, the result is nil, `validate.Errors`, or an
operational error. See [definition errors](#definition-errors) for the separate
standalone-compilation panic behavior.

### Parse

`Parse(ctx, value)` runs the same checks and returns the normalized copy.
To keep trimmed input:

```go
name := validate.String(func(value *validate.StringBuilder[string]) {
	value.Trim()
})
parsed, err := name.Parse(ctx, " Ada ")
// On success, parsed is "Ada"; the input is unchanged.
```

Always check the error before using the returned value.

### JSONSchema and Describe

When a client or tool needs a machine-readable description of accepted input,
export the representable rules with `JSONSchema()`. It returns bytes and an
error. Set `Describe` inside the builder callback to annotate the document:

```go
name := validate.String(func(value *validate.StringBuilder[string]) {
	value.Describe("Public display name")
})
document, err := name.JSONSchema()
```

Check `err` before publishing `document`. `Describe` sets `description`; it is
not a custom validation message. Runtime predicates and normalizers are not
exported, but their definitions are still checked. Invalid definitions return
nil bytes and an error wrapping `ErrInvalidSchema`; JSON encoding failures
return nil bytes with the encoder's cause preserved.

Export does not run checks, normalizers, or runtime predicates, replay
construction callbacks, or validate defaults against their rules. Defaults are
emitted as supplied, not normalized. A successful export does not guarantee
numeric or default equivalence after import. See
[JSON Schema limits](advanced.md#runtime-only-rules) before using the document
as a replacement for the Go schema.

### Infer and Err

A response type needs a description, but not necessarily input-validation
rules. `Infer[T]()` describes that Go type without enforcing rules. To describe
a response:

```go
type Result struct {
	Name string `json:"name"`
}

result := validate.Infer[Result]()
document, err := result.JSONSchema()
```

The document describes `Name` as a string. It does not check that a name is
nonempty. Unsupported types produce an error wrapping `ErrInvalidSchema`;
`result.Err()`, `Validate`, `Parse`, and `JSONSchema` report that error.

### FromJSONSchema

`FromJSONSchema` constructs a validator for decoded JSON, not arbitrary typed
structs. To check a decoded string:

```go
name, err := validate.FromJSONSchema([]byte(`{"type":"string","minLength":3}`))
err = name.Validate(ctx, "Ada")
```

This passes. Unsupported or malformed keywords fail construction instead of
being silently ignored. In particular, `pattern` must be a string; an empty
string is valid, but null and other JSON kinds return an error wrapping
`ErrInvalidSchema` at the keyword's JSON Pointer. Read
[JSON Schema limits](advanced.md#json-schema) before accepting documents or
decoded values from untrusted callers.

For an already encoded contract boundary, use
[Encoded JSON Validation](encoded-json.md) instead. It distinguishes null from
absence and never applies application defaults or normalization.

## Errors and custom checks

| Helper or method | Purpose |
| --- | --- |
| `Check(func(context.Context, T) error)` | Builder setting for a typed custom check |
| `Fail(code, params)` | Violation at the checked value |
| `At(field, code, params)` | Object-wide violation attached to a Go field name |
| `Errors.Error()` | Deterministic English summary, not a wire format |
| `Errors.All()` | Iterate paths in stable order; numeric segments sort numerically |
| `Errors.Add(pointer, violation)` | Append to an initialized error map |
| `Errors.Localize(ctx)` | Return a copy with localized messages |

To accumulate an application-level violation without a schema:

```go
failures := make(validate.Errors)
failures.Add("/email", validate.Violation{
	Code:    "email.taken",
	Message: "This email address is already registered.",
})
localized := failures.Localize(ctx)
```

`Add` takes a JSON Pointer, while `At` takes a Go field name. `Localize`
preserves codes, parameter values, and locations; an unknown code retains its
existing message. It copies supported parameter containers even for an unknown
code, leaving the source unchanged. `Add` does not copy the supplied parameters,
and `All` yields borrowed violation slices and parameters rather than snapshots.
Do not mutate borrowed diagnostics concurrently with readers. See
[diagnostic ownership](advanced.md#diagnostic-ownership) for copied containers,
opaque shared values, and reusable `Fail` and `At` errors.

## Locale helpers

`English()` returns a copy of the built-in messages. To customize one message:

```go
messages := validate.English()
messages["required"] = "Please supply a value."
catalog := validate.Catalog{
	Default: "en",
	Locales: map[string]validate.Messages{"en": messages},
}
ctx = validate.NewCatalogContext(ctx, catalog)
ctx = validate.NewLocaleContext(ctx, "en")
locale, ok := validate.LocaleFromContext(ctx)
```

`locale` is `"en"` and `ok` is true. Message lookup tries the selected locale,
its base language, the catalog default and its base language, built-in English,
then the code.
`LocaleFromContext` reads the selected locale; it does not negotiate a language.
Do not mutate catalog maps while validation is running.

## Clock helpers

`NewClockContext` supplies the clock read by `Future` and `Past`. Pin it for a
deterministic check:

```go
now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
fixed := validate.NewClockContext(ctx, func() time.Time {
	return now
})
appointment := validate.Time(func(value *validate.TimeBuilder) {
	value.Future()
})
err := appointment.Validate(fixed, now.Add(time.Hour))
```

This passes. Equality with `now` fails `Future`; no sleep is needed.

## Duration text

`validate.Duration` reads and writes Go duration text. Convert between text and
standard-library durations:

```go
var timeout validate.Duration
err := timeout.UnmarshalText([]byte("250ms"))
duration := timeout.Std()
text, err := timeout.MarshalText()
```

`duration` is a `time.Duration` of 250 milliseconds; on success, `text` is
`[]byte("250ms")`. `UnmarshalText` changes the receiver only on success.
`JSONSchemaFormat()` returns `"go-duration"`, used by `Text` and `Infer`.
Application `FromJSONSchema` enforces that format with `time.ParseDuration`.
Standard `"duration"` instead uses RFC 3339 Appendix A, not Go duration text.

Use `Text[validate.Duration]()` for an already decoded duration field.
Use `String` with `Duration()` when the field itself is a string. A raw
`time.Duration` has no representation in `encoding/json/v2`; use the text
wrapper in types that pass through JSON.

For exact signatures, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
