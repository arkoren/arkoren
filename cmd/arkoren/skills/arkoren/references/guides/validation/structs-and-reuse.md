---
title: Validation Structs and Reuse
nav: Structs & Reuse
description: Configure nested fields with typed child builders, reuse published schemas, and distinguish omission from zero.
order: 30
---

# Validation Structs and Reuse

## Introduction

Forms often contain nested addresses or reuse the same email policy in several
places. Struct schemas let you keep those rules together without duplicating
them. `Struct` registers the fields you want to check. Each `Field` callback receives
the concrete builder for that field's exact Go type. Nested declarations use
the same pattern as scalar declarations.

Examples use `ctx` for an existing
`context.Context`. Start with the [overview](overview.md) for parsing and errors.

## Selecting fields

Register Go field names, even when the JSON name differs:

```go
type Contact struct {
	EmailAddress string `json:"email"`
}

contact := validate.Struct(func(object *validate.StructBuilder[Contact]) {
	object.Field("EmailAddress", func(address *validate.StringBuilder[string]) {
		address.Required()
		address.Email()
	})
})
```

A failure is reported at `/email`. An unregistered field is not validated.
`Struct[Contact]()` registers nothing; it does not infer rules.
A zero `StructSchema[Contact]` is not an empty registration: it is uninitialized.
Its `JSONSchema` returns nil bytes and an error wrapping `ErrInvalidSchema`, and
its standalone `Parse` or `Validate` panics with an error wrapping that sentinel.
Adapting the zero schema with `Ptr` does not make the definition valid or make
export succeed. Construct the pointee with `Struct[Contact]()` first.

Unknown names, duplicate registrations, incompatible callback types, invalid
conditions, and invalid rules are construction mistakes. `Struct` reports
them with a panic wrapping `ErrInvalidSchema`, rather than treating them as
bad user input. Define stable schemas at package initialization to detect
these mistakes at startup. Field validation runs in Go struct order.

Recorded problems are aggregated after declaration callbacks finish, sorted by
Go field name with declaration order retained for ties. They survive later
settings, reuse, and publication. Some setters still panic immediately, including
`MultipleOf(0)`; see [definition errors](schemas-and-helpers.md#definition-errors)
for the distinction between construction, standalone execution, and export.

## Builder operations

### StructBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Field(name, callbacks...)` | Configure an exact-type child using its Go field name | [Selecting fields](#selecting-fields) |
| `Use(schema)` | Import supported struct rules and registrations without replacing fields | [Reuse constraints](#reusing-a-schema) |
| `Check(func(context.Context, T) error)` | Check an object-wide invariant before child field rules | [Object-wide checks](advanced.md#object-wide-checks) |
| `Required()`, `Default(value)`, `Bail()`, `Normalize(fn)`, `Describe(text)` | Common settings on the struct value, not individual fields | [Common operations](overview.md#builder-operations) |
| `EqField`, `NeField`, `RequiredIf`, `RequiredUnless`, `RequiredWith`, `RequiredWithAll`, `RequiredWithout`, `ProhibitedIf`, `ProhibitedUnless`, `ExcludeIf`, `When` | Compare or conditionally configure a nested struct in its enclosing context | [Field conditions](conditions.md#field-builder-operations) |

### PtrBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Value(callbacks...)` | Configure the exact T pointee with its concrete builder | [Explicit zero values](#explicit-zero-values) |
| `Use(schema)` | Import a supported `Schema[*T]`; composite-role constraints still apply | [Reuse constraints](#reusing-a-schema) |
| `Required()`, `Default(value)` | Reject nil or default nil with a T pointee value | [Pointer adapter](#adapting-a-standalone-schema-to-a-pointer) |
| `Check(fn)`, `Bail()`, `Normalize(fn)`, `Describe(text)` | Common settings on `*T`, not the pointee | [Common operations](overview.md#builder-operations) |
| `EqField`, `NeField`, `RequiredIf`, `RequiredUnless`, `RequiredWith`, `RequiredWithAll`, `RequiredWithout`, `ProhibitedIf`, `ProhibitedUnless`, `ExcludeIf`, `When` | Compare or configure the pointer in its enclosing struct | [Field conditions](conditions.md#field-builder-operations) |

`Ptr(inner, callbacks...)` is a constructor, not a builder method. To configure
the pointee's scalar rules, use `Value`, rather than calling those rules on
the pointer draft.

## Reusing a schema

If several forms accept email addresses, define their shared cleanup and syntax
rules once. `Use` imports that published schema into a child builder of the same
value type, where you can add a form-specific requirement:

```go
email := validate.String(func(address *validate.StringBuilder[string]) {
	address.Trim()
	address.Email()
})

type Contact struct {
	Email string `json:"email"`
}

contact := validate.Struct(func(object *validate.StructBuilder[Contact]) {
	object.Field("Email", func(address *validate.StringBuilder[string]) {
		address.Use(email)
		address.Required()
	})
})
```

The reusable email schema remains unchanged. `contact` additionally requires
its email field. Pass `Schema[T]` to `Use`, not to `Field`; `Field` accepts
construction callbacks. The same reuse pattern works in `Values`, `Keys`,
and pointer `Value` callbacks.

Reuse explicit package schemas with the same builder role, such as a
`StringSchema[S]` in `StringBuilder[S]` or a `PtrSchema[T]` in
`PtrBuilder[T]`. Both published values and non-nil pointers to them are accepted.
Matching a value type alone does not convert between distinct roles;
unsupported roles and typed-nil schema pointers fail at construction.
`Infer[T]` supplies type information only, not validation rules or missing
child definitions. `FromJSONSchema` remains a separate API.

Schema-level rules accumulate. Imported child definitions cannot be replaced:
using two slice schemas with element definitions, or calling `Values` after
importing an element definition, is a construction error. Map imports may
supply disjoint key and value definitions. Repeated local child setters can
replace local definitions, but not imported ones. To extend a shared element
rule, configure one `Values` callback and call `Use` inside it.

For `Ptr(inner, callbacks...)`, importing a complete pointer schema may replace
only a genuinely unconfigured scalar adapter. It cannot discard a configured
or composite pointee. Owned rule containers detach; arbitrary defaults and
function captures are not recursively cloned.

## Nested structs

Configure a child struct directly when it has no separate reusable schema:

```go
type Address struct {
	City string `json:"city"`
}

type Customer struct {
	Address Address `json:"address"`
}

customer := validate.Struct(func(object *validate.StructBuilder[Customer]) {
	object.Field("Address", func(address *validate.StructBuilder[Address]) {
		address.Field("City", func(city *validate.StringBuilder[string]) {
			city.Required()
		})
	})
})
```

`Customer{}` fails at `/address/city`. A nested value struct is checked even
when all its fields are zero. An optional pointer to that struct instead
skips its child rules when nil.

## Explicit zero values

An update may leave the retry limit unchanged when omitted, but set it to zero
when the caller explicitly disables retries. Use a pointer to preserve that
distinction: nil means omitted and a non-nil pointer means supplied:

```go
type Update struct {
	Retries *int `json:"retries"`
}

update := validate.Struct(func(object *validate.StructBuilder[Update]) {
	object.Field("Retries", func(pointer *validate.PtrBuilder[int]) {
		pointer.Value(func(count *validate.IntBuilder[int]) {
			count.Between(0, 10)
		})
	})
})
```

Nil passes. A pointer to zero passes the range check; a pointer to -1 fails.
Put `Required` on `pointer` if nil must fail too. A non-nil pointee is always
treated as supplied. A plain pointer does not distinguish a missing JSON key
from explicit JSON null: both decode to nil.

## Adapting a standalone schema to a pointer

Use `Ptr` when you already have a standalone schema and need its pointer form:

```go
count := validate.Int(func(value *validate.IntBuilder[int]) {
	value.Min(1)
})
quantity := validate.Ptr(count, func(pointer *validate.PtrBuilder[int]) {
	pointer.Required()
})
```

`quantity` validates `*int`: nil fails `Required`, and a pointer to zero
fails `Min(1)`. `Ptr` takes an inner `Schema[T]` followed by optional
`PtrBuilderFunc[T]` callbacks. A pointer child can reuse a complete pointer
schema with `pointer.Use(quantity)`, which accepts `Schema[*T]`.

## Paths and nil intermediates

Conditions and comparisons can use dot-separated Go paths such as
`"Billing.Country"`. Paths walk structs and pointers from the nearest
enclosing struct; they cannot index a collection or reach a parent.
See [nested field paths](conditions.md#nested-field-paths) before adding
cross-field rules to nested objects.

Fields behind a nil embedded pointer are checked as zero values, so their
`Required` rules can fail. Defaults do not allocate that pointer. Parsing
does not allocate nil intermediates merely to satisfy a path.

For constructors and callback aliases, see
[schemas and helpers](schemas-and-helpers.md). For the full contract, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
