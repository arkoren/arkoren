---
title: Validation Overview
nav: Overview
description: Build a schema, check typed input, and keep normalized values with Parse.
order: 10
---

# Validation Overview

## Introduction

Input can have the right Go type and still be unusable: an email can be empty,
or a page size can exceed your application's limit. A validation **schema**
describes which values your application accepts. Use the same schema for HTTP
bodies, configuration, CLI input, and imported records.
Failures identify each value by JSON Pointer, with a stable code and a message.

`validate` is a standalone foundation module. It has no dependencies and does
not need an application or HTTP server. Construct a schema once, then reuse it.

From your application's Go module directory, install the standalone module:

```sh
go get arkoren.dev/foundation/validate
```

Import `arkoren.dev/foundation/validate` as `validate`. The examples use
standard-library `context`, `errors`, and `fmt` when needed.

Focused snippets leave error results visible without repeating routine error
checks. In application code, check the error before using a parsed value or
accepting the input.

You configure rules once, rather than rebuilding them for each input.
A construction callback receives a temporary **builder** for those settings.
Run the published schema
afterward; `Parse`, `Validate`, and `JSONSchema` are not builder methods.

## Checking a value

To check an email address, configure a
string schema in its construction callback:

```go
email := validate.String(func(address *validate.StringBuilder[string]) {
	address.Trim()
	address.Required()
	address.Email()
})
err := email.Validate(ctx, " ada@example.com ")
```

Here and in the following fragments, `ctx` is a `context.Context`. Validation
passes: whitespace is removed before checking presence and email syntax. The
original string is unchanged.

Use `Parse` when you need the cleaned value:

```go
address, err := email.Parse(ctx, " ada@example.com ")
// address is "ada@example.com".
```

`Parse` runs the same checks as `Validate`. Use its result only after checking
the error; it is not a partial-success API.

## Builder operations

Use this table to find settings and constructors after checking your first value.

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `String`, `Int`, `Uint`, `Float`, `Bool`, `Time` | Construct scalar schemas with their concrete builders | [Scalar constructors](schemas-and-helpers.md#scalar-constructors) |
| `File`, `Text[T]`, `Any` | Construct upload, text-marshaled, or dynamic-value schemas | [Scalar constructors](schemas-and-helpers.md#scalar-constructors) |
| `Struct`, `Slice`, `Map`, `Ptr` | Construct nested or optional schemas | [Composite constructors](schemas-and-helpers.md#composite-constructors) |
| `Required()` | Reject absence; all explicit builders | [Presence](#required-and-optional-values) |
| `Default(value)` | Supply absence; all explicit builders except `FileBuilder` | [Defaults](#defaulting-a-value) |
| `Use(schema)` | Import a supported schema of the same role and value type | [Reuse](structs-and-reuse.md#reusing-a-schema) |
| `Check(fn)`, `Bail()` | Add a typed check; stop a value after its first violation | [Custom checks](advanced.md#custom-checks) |
| `Normalize(fn)` | Prepare a replacement value; forbidden on map keys | [Normalization](normalizers.md#custom-normalization) |
| `Describe(text)` | Set a JSON Schema description, not an error message | [Descriptions](schemas-and-helpers.md#jsonschema-and-describe) |
| `EqField(path)`, `NeField(path)` | Compare with an exact-type field in an enclosing struct | [Field comparisons](available-methods.md#comparing-fields) |
| `RequiredIf`, `RequiredUnless`, `RequiredWith`, `RequiredWithAll`, `RequiredWithout` | Require a field under a condition or presence dependency | [Conditions](conditions.md#field-builder-operations) |
| `ProhibitedIf`, `ProhibitedUnless`, `ExcludeIf`, `When` | Reject, clear, or configure a field conditionally | [Conditions](conditions.md#field-builder-operations) |

Role-specific methods appear in the tables for
[scalar builders](available-methods.md#builder-operations),
[struct and pointer builders](structs-and-reuse.md#builder-operations), and
[collection builders](collections.md#builder-operations).

## Defining a form

When input has several fields, a struct schema keeps their rules together and
reports which field failed. Register each field by its Go name, and configure
it with a callback for its exact Go type:

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

`Email` is required. An omitted plan becomes `"free"` when you parse the form.
Only registered fields are checked. JSON tags control error paths, so an email
failure appears at `/email`, not `/Email`.

Construction callbacks edit temporary **builders**. The published schema has
only `Parse`, `Validate`, and `JSONSchema`; configuration stays inside the
callbacks. There is no `Build` step. See [structs and reuse](structs-and-reuse.md)
for nested fields and shared schemas.

## Required and optional values

Fields are optional by default. Empty strings, zero numbers, false booleans,
and nil pointers, slices, and maps count as absent. Absent values skip ordinary
rules. `Required` rejects absence.

To require a positive quantity:

```go
quantity := validate.Int(func(count *validate.IntBuilder[int]) {
	count.Required()
	count.Min(1)
})
err := quantity.Validate(ctx, 0)
```

This fails `Required`. Without it, zero skips `Min(1)` and passes. Use a pointer
when zero is a supplied value that needs checking; see
[explicit zero values](structs-and-reuse.md#explicit-zero-values).

A non-nil empty slice or map is present. `Required` rejects nil, but use
`Min(1)` to reject an allocated empty collection. Container entries and
non-nil pointees are checked even when their values are zero.

## Defaulting a value

To supply a page size when the input is zero:

```go
limit := validate.Int(func(size *validate.IntBuilder[int]) {
	size.Default(20)
	size.Between(1, 100)
})
size, err := limit.Parse(ctx, 0)
// On success, size is 20.
```

`Validate` also checks the defaulted value, but does not return it. Do not
combine `Required` and `Default` on one schema. Defaults must already satisfy
their schema and are not sent back through normalizers.

## Handling failures

Rule failures return `validate.Errors`; custom checks can instead return an
operational error. Print
structured failures in stable path order:

```go
if failures, ok := errors.AsType[validate.Errors](err); ok {
	for pointer, violations := range failures.All() {
		fmt.Println(pointer, violations[0].Message)
	}
}
```

Use violation codes for program decisions and messages for people. A scalar's
root pointer is `""`. Do not parse the English summary from `err.Error()`.

Keep bad input separate from a bad definition. `Struct` reports recorded
construction problems together; standalone execution of an invalid explicit
schema can panic with an error wrapping `ErrInvalidSchema`. Export reports
definition errors as nil bytes plus an error wrapping that sentinel instead.
See [definition errors](schemas-and-helpers.md#definition-errors) before accepting
dynamically assembled schemas.

## Validating a request body

Inside a router handler, parse the request body with the `signup` schema above:

```go
input, err := request.ParseJSON(r, signup)
// input contains the validated form, including its default plan.
```

`ParseJSON` decodes, validates, and returns the parsed value. Rule failures
become `422 Unprocessable Content` responses. For input validated separately,
return `request.ValidationProblem(err)` instead of a raw `validate.Errors`;
a raw validation error has no HTTP status and otherwise becomes a 500.
See [requests](../http-fundamentals/requests.md) for decoding and body limits.

## Where to go next

Use [scalar rules](available-methods.md) for common checks,
[collections](collections.md) for slices and maps, and
[conditions](conditions.md) for rules that depend on other fields.
[Advanced validation](advanced.md) covers custom checks, file uploads,
normalization order, localization, and JSON Schema limits.

For the complete contract, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
