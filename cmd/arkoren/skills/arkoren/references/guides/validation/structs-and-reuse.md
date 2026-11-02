---
title: Structs and Reuse
description: Compose exact-type field rules, nested objects, shared policies, and optional pointer values.
order: 30
summary:
  - Register selected fields and reuse policies without modifying the source schema.
  - Validate nested objects and distinguish omitted values from explicit zeros.
  - Respect exact-type and child-definition boundaries when composing schemas.
---

# Structs and Reuse

A form can share an email policy with other forms while imposing its own
requirements. Use `Struct` to select the fields to validate and `Use` to import
an existing policy into a typed child builder. Neither operation infers rules
for unregistered fields.

Examples import `arkoren.dev/foundation/validate` and use an existing
`context.Context` named `ctx`. The [overview](overview.md) covers execution.

## Select Fields

Register the Go name even when your JSON name differs:

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

An email failure uses `/email`. The callback's builder must match the field's
exact Go type: a named string type requires that named type in `StringBuilder`.
Fields run in Go struct order, not registration order.

`Struct[Contact]()` is a valid schema with no registered fields. A zero
`StructSchema[Contact]` is uninitialized and invalid, including through `Ptr`.
Unknown or duplicate registrations, wrong callback types, and bad paths are
[definition errors](errors.md#definition-errors).

## Reuse Policies

Define cleanup and syntax once, leaving requiredness to each form:

```go
email := validate.String(func(address *validate.StringBuilder[string]) {
	address.Trim()
	address.Email()
})
```

For the `Contact` type above, this alternative construction adds a requirement
without changing `email`:

```go
contact := validate.Struct(func(object *validate.StructBuilder[Contact]) {
	object.Field("EmailAddress", func(address *validate.StringBuilder[string]) {
		address.Use(email)
		address.Required()
	})
})
```

Pass schemas to `Use`, not `Field`. The same pattern works inside collection
`Keys` or `Values` and pointer `Value` callbacks.

If reuse concerns declaration settings rather than a published schema, supply
several callbacks to a constructor or child declaration:

```go
var policy validate.StringBuilderFunc[string] = func(text *validate.StringBuilder[string]) {
	text.Trim()
	text.Max(80)
}
name := validate.String(policy, func(text *validate.StringBuilder[string]) {
	text.Required()
})
```

Callbacks run once, synchronously, in argument order on the same fresh draft.
Every exported builder role has a matching `BuilderFunc` alias. An expanded nil
or empty callback slice makes no edits; an explicitly supplied nil function
panics, and application callback panics propagate.

## Nest Objects

A nested value object can declare its own selected fields inline:

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

`Customer{}` fails at `/address/city`: a nested value struct checks its fields
even when every field is zero. Make the address a pointer if nil should skip
the entire child schema.

Promoted fields behind a nil embedded pointer are checked as zero values.
Their `Required` rules can fail, but normalizers and defaults do not allocate
the missing intermediate pointer. Nested paths likewise never allocate merely
to reach a field.

## Explicit Zero Values

An update may omit its retry limit or explicitly set it to zero to disable
retries. A pointer preserves those two states:

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

Nil passes without checking the pointee. A pointer to zero is supplied and
passes the range; a pointer to -1 fails. Add `pointer.Required()` to reject nil.
A plain pointer does not distinguish a missing JSON member from explicit null:
both decode to nil.

## Pointer Schemas

Use `Ptr` when a standalone policy already exists and you need its pointer form:

```go
count := validate.Int(func(quantity *validate.IntBuilder[int]) {
	quantity.Min(1)
})
quantity := validate.Ptr(count, func(pointer *validate.PtrBuilder[int]) {
	pointer.Required()
})
```

`quantity` accepts `*int`: nil fails presence and a pointer to zero fails the
minimum. `PtrBuilder.Default` takes a pointee value, not a pointer. Configure
pointee rules through `Value`; pointer-level checks receive `*T` instead of `T`.

## Reuse Boundaries

Ordinary `Use` requires the same concrete schema role and exact value type.
It accepts published values and non-nil pointers to them. An inferred schema
can contribute type-only documentation, but never missing child validation.
The decoded-JSON importer is a separate API, not an interchangeable typed schema.

Schema-level rules accumulate. Imported children cannot be overwritten: two
slice imports defining elements conflict, as does adding `Values` after importing
an element definition. Instead, use one local child callback and call `Use`
inside it. Map imports can contribute disjoint key and value definitions.
Repeated local child setters can replace local definitions, not imported ones.
Struct imports add registrations, so repeated fields remain errors.

A complete pointer schema can replace only a genuinely unconfigured scalar
adapter; it cannot discard a configured or composite pointee. For named slices
and maps, use the explicit [named-container adapter](collections.md#named-containers).

Publication detaches owned rule containers, not arbitrary defaults or function
captures. Keep shared state immutable or synchronized, and never mutate
caller-owned nested data inside a callback.

For exact composition contracts, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
