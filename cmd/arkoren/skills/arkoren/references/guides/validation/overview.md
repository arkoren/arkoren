---
title: Overview
description: Define reusable rules for typed input and retain prepared values with Parse.
order: 10
summary:
  - Construct a typed schema once and reuse it across input boundaries.
  - Choose presence rules and defaults without confusing omission with zero.
  - Keep parsed values only after validation succeeds.
---

# Overview

A decoded request can have the correct Go type but still contain an empty email
or an unsupported plan. A validation **schema** defines which values your
application accepts. It can also prepare a cleaned, defaulted copy of the input.
Use the same schema for requests, configuration, CLI input, or imported records.

Install the standalone module from your application's module directory:

```sh
go get arkoren.dev/foundation/validate
```

Import `arkoren.dev/foundation/validate` as `validate`. Examples use an existing
`context.Context` named `ctx`. Focused fragments show error results without
repeating routine propagation; application code must check them before using
parsed input.

## Build a Schema

Start with the type your application already decodes. JSON tags determine
diagnostic names; schema registration uses Go field names.

```go
type Signup struct {
	Email string `json:"email"`
	Plan  string `json:"plan"`
}
```

Configure the accepted input inside `Struct`. Each callback receives a temporary,
typed **builder**, not the published schema:

```go
signup := validate.Struct(func(object *validate.StructBuilder[Signup]) {
	object.Field("Email", func(address *validate.StringBuilder[string]) {
		address.Trim()
		address.Required()
		address.Email()
	})
	object.Field("Plan", func(plan *validate.StringBuilder[string]) {
		plan.Default("free")
		plan.OneOf("free", "pro")
	})
})
```

Only registered fields are checked. Construction callbacks run once,
synchronously, in argument order; there is no separate `Build` call.
The published schema exposes `Parse`, `Validate`, and `JSONSchema`.
For nested input and shared field policies, use [Structs and Reuse](structs-and-reuse.md).

## Parse Input

Use `Parse` when the application needs the prepared value rather than the
original input:

```go
input := Signup{Email: " ada@example.com "}
parsed, err := signup.Parse(ctx, input)
```

On success, `parsed.Email` is `"ada@example.com"` and `parsed.Plan` is `"free"`.
`input` is unchanged. On failure, `Parse` returns the zero result and an error,
not a partially accepted form.

If you only need a decision, call `signup.Validate(ctx, input)`. It performs the
same preparation and checks but discards the prepared value. Neither method
decodes JSON or converts an incompatible Go type.

## Choose Presence

Values are optional by default. Empty strings, zero numbers, false booleans,
nil pointers, and nil slices or maps count as absent and skip ordinary rules.
A zero `time.Time` is absent regardless of its location. `Required` rejects
absence, rather than merely requiring a JSON key.

For example, this schema rejects a missing quantity:

```go
quantity := validate.Int(func(count *validate.IntBuilder[int]) {
	count.Required()
	count.Min(1)
})
err := quantity.Validate(ctx, 0)
```

Without `Required`, zero would skip `Min(1)` and pass. Use a
[pointer field](structs-and-reuse.md#explicit-zero-values) when zero is a supplied
value that must be checked. Non-nil pointees and collection entries are checked
even when zero. An allocated empty slice or map is present: `Required` accepts
it, while `Min(1)` rejects it. Nested value structs always check registered fields.

## Supply Defaults

Use `Default` when absence should select an application policy rather than fail:

```go
limit := validate.Int(func(size *validate.IntBuilder[int]) {
	size.Default(20)
	size.Between(1, 100)
})
size, err := limit.Parse(ctx, 0)
```

On success, `size` is 20. Defaults must satisfy their rules and must already be
in their final form: normalizers run before defaults, not again afterward.
`Required` and `Default` cannot coexist on one definition. `FileBuilder` has no
default setting. Slice and map defaults copy their outer containers, not
arbitrary nested objects; pointer defaults take a pointee value.
See [Normalizers](normalizers.md) for preparation order.

## Share Schemas

Construct stable definitions once and share their published values across
goroutines. Publication detaches schema-owned rule containers; retained builders
are not runtime configuration objects.

This does not recursively clone arbitrary default objects or captured data.
Keep shared maps, pointers, catalogs, and callback state immutable or synchronize
access. Custom callbacks must not mutate data reachable from their input.

Bad definitions are programming errors, not rejected user input. `Struct`
aggregates recorded construction problems and panics with `ErrInvalidSchema`;
invalid standalone explicit schemas can panic when executed. See
[Definition Errors](errors.md#definition-errors) before assembling dynamic rules.

## HTTP Input

With `request` imported from `arkoren.dev/core/http/request`, an HTTP handler can
decode and validate its body using the same schema:

```go
input, err := request.ParseJSON(r, signup)
```

Successful input includes normalization and defaults. Validation failures become
422 responses. If you validate separately, convert failures with
`request.ValidationProblem(err)`; raw `validate.Errors` has no HTTP status and
otherwise becomes a 500. Configure decoding and body limits through the
[request guide](../http-fundamentals/requests.md).

Continue with [Scalar Rules](available-methods.md) for constraints or
[Validation Errors](errors.md) for structured diagnostics.
For all public contracts, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
