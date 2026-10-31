---
title: Conditional Validation
nav: Conditions
description: Require, prohibit, exclude, or configure fields with typed conditions and nested Go field paths.
order: 50
---

# Conditional Validation

## Introduction

A customer form may require a tax identifier for companies but not individuals.
A **condition** observes the current input without changing it or reporting
violations. The field's builder decides what to do with that observation:
require the field, prohibit it, exclude it, or apply extra settings. Build the
observation with a `ConditionBuilder` callback inside `RequiredIf`,
`RequiredUnless`, `ProhibitedIf`, `ProhibitedUnless`, `ExcludeIf`, or `When`.

Conditions read fields from the nearest enclosing struct. Examples are construction fragments rather
than HTTP-specific logic. See the [overview](overview.md) for running schemas.

## Requiring a field conditionally

To require a tax identifier only for a company:

```go
type Customer struct {
	Kind  string `json:"kind"`
	TaxID string `json:"tax_id"`
}

customer := validate.Struct(func(object *validate.StructBuilder[Customer]) {
	object.Field("TaxID", func(identifier *validate.StringBuilder[string]) {
		identifier.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.Eq("Kind", "company")
		})
	})
})
```

An empty `TaxID` fails when `Kind` is `"company"`. For another kind, the
field is optional, not prohibited. `RequiredUnless` reverses the condition.
Both methods accept multiple condition callbacks on one fresh condition draft.

## Builder operations

### Field builder operations

These methods belong to the field's concrete builder, not `ConditionBuilder`.
They need an enclosing struct for their field references.

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `RequiredIf(callbacks...)`, `RequiredUnless(callbacks...)` | Require a value when a condition is true or false | [Conditional presence](#requiring-a-field-conditionally) |
| `RequiredWith(paths...)`, `RequiredWithAll(paths...)`, `RequiredWithout(paths...)` | Require a field based on other fields' presence; arguments are strings | [Presence dependencies](#checking-presence-dependencies) |
| `ProhibitedIf(callbacks...)`, `ProhibitedUnless(callbacks...)` | Reject a supplied value under the selected condition | [Prohibition](#prohibiting-and-excluding-fields) |
| `ExcludeIf(callbacks...)` | Skip and clear a field when the condition matches | [Exclusion](#prohibiting-and-excluding-fields) |
| `When(condition, callbacks...)` | Apply scoped settings using callbacks of the same builder role | [Conditional settings](#adding-conditional-settings) |
| `EqField(path)`, `NeField(path)` | Compare with an exact-type field | [Field comparisons](available-methods.md#comparing-fields) |
| `GtField`, `GteField`, `LtField`, `LteField` | Numeric field comparisons; numeric builders only | [Field comparisons](available-methods.md#comparing-fields) |
| `AfterField(path)`, `BeforeField(path)` | Time field comparisons; `TimeBuilder` only | [Time rules](available-methods.md#time-rules) |

For example, the `StringBuilder[S].When` parameters are
`condition ConditionBuilderFunc, configure ...func(*StringBuilder[S])`.
The condition callback configures an observation; the remaining callbacks
configure a string branch. Other roles use their own concrete callback type.

### ConditionBuilder operations

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Eq(path, value)`, `NotEq(path, value)` | Compare a reachable Go field value | [Condition method index](#condition-method-index) |
| `In(path, values...)`, `NotIn(path, values...)` | Observe membership in a copied value list | [Condition method index](#condition-method-index) |
| `Present(path)`, `Absent(path)` | Observe typed presence, not JSON key existence | [Paths and presence](#nested-field-paths) |
| `And(callbacks...)`, `Or(callbacks...)`, `Not(callbacks...)` | Add explicit nonempty logical groups | [Groups](#combining-conditions) |
| `If(func(T) bool)` | Observe a pure predicate on the nearest exact struct type T | [Object predicates](#typed-object-predicates) |
| `Has(path, key)`, `HasAny(path, keys...)`, `HasAll(path, keys...)` | Observe map key existence, independent of values | [Collection conditions](#collection-conditions) |
| `Contains(path, predicate)`, `Every(path, predicate)` | Observe matching entries; slice `(int, E)` or map `(K, V)` predicates | [Collection conditions](#collection-conditions) |
| `UniqueBy(path, projection)` | Observe comparable projected identities | [Collection conditions](#collection-conditions) |
| `CountWhere(path, predicate, countCallbacks...)` | Observe pure constraints on the actual matching count | [Collection conditions](#collection-conditions) |

`ConditionBuilder` has no `Required`, `Default`, `Normalize`, or `Check`:
observations do not rewrite or validate the selected value themselves.

## Checking presence dependencies

Use the string-path shortcuts when only presence matters. Within a struct's
string field callback:

```go
phone.RequiredWithout("Email")
```

Here `phone` is a `*validate.StringBuilder[string]`. Phone is required if
email is absent. The shortcuts use the same presence rules as other conditions:

| Method | Requires this field when |
| --- | --- |
| `RequiredWith("City", "PostalCode")` | Any listed field is present |
| `RequiredWithAll("City", "Street")` | Every listed field is present |
| `RequiredWithout("Email", "Phone")` | Any listed field is absent |

These shortcuts still take strings, not condition callbacks.

## Prohibiting and excluding fields

For a pickup order, a delivery address may be a mistake you want to report,
rather than silently accept. `ProhibitedIf` rejects a supplied address when the
pickup condition matches. Set it inside the shipping form's address callback:

```go
address.ProhibitedIf(func(condition *validate.ConditionBuilder) {
	condition.Eq("Delivery", "pickup")
})
```

Here `address` is a `*validate.StringBuilder[string]`, and `Delivery` is a
string field in the same struct. A supplied address fails for pickup orders.

`ProhibitedUnless` expresses the reverse: the value is allowed only when its
condition matches.

If the form keeps a previously entered address after the customer switches to
pickup, you may want to discard that stale value instead of showing an error.
Use `ExcludeIf` for that behavior:

```go
address.ExcludeIf(func(condition *validate.ConditionBuilder) {
	condition.Eq("Delivery", "pickup")
})
```

The field is skipped when the condition matches; parsing clears its value.
Exclusion is an effect, not a predicate assertion. Do not use it for a value
whose presence must be reported as an error.

## Adding conditional settings

Some fields need stricter rules only in certain circumstances. For example,
administrator passwords may need a higher minimum length than ordinary
passwords. `When` adds settings for inputs that match a condition; configure
the extra minimum inside the password callback:

```go
password.Min(12)
password.When(func(condition *validate.ConditionBuilder) {
	condition.Eq("Role", "admin")
}, func(value *validate.StringBuilder[string]) {
	value.Min(20)
})
```

Here `password` is a `*validate.StringBuilder[string]`, and `Role` is a string
field in the same struct. The ordinary minimum remains 12; the admin branch
adds a minimum of 20. Configure the branch inside `When`, not by passing a
published schema. `When` settings are scoped to that branch.

Construction callbacks run once while the schema is built. The resulting
condition is evaluated for each input; the branch callbacks are not rerun.

## Combining conditions

Conditions added to one draft are combined with AND. For an explicit OR group,
within a field's builder callback:

```go
approval.RequiredIf(func(condition *validate.ConditionBuilder) {
	condition.Or(func(group *validate.ConditionBuilder) {
		group.Eq("Plan", "enterprise")
		group.Eq("Role", "admin")
	})
})
```

Here `approval` is a `*validate.StringBuilder[string]`; `Plan` and `Role` are
string fields. Approval is required when either comparison matches.
`And` creates an explicit AND group. `Not` complements its nested group.
An empty condition is invalid, including an empty explicit group.

## Nested field paths

Paths use dot-separated Go names. To require a tax identifier for a particular
billing country:

```go
type Address struct {
	Country string `json:"country"`
}

type Invoice struct {
	Billing *Address `json:"billing"`
	TaxID   string   `json:"tax_id"`
}

invoice := validate.Struct(func(object *validate.StructBuilder[Invoice]) {
	object.Field("TaxID", func(identifier *validate.StringBuilder[string]) {
		identifier.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.Eq("Billing.Country", "DE")
		})
	})
})
```

The path walks the `Billing` pointer and then its `Country` field. Invalid
paths and incompatible comparison types are construction errors. Paths
cannot index maps or slices, select a parent, or use JSON tag names.
Within an `Address` child schema, paths start at `Address`, not `Invoice`.

A nil intermediate pointer makes the path absent; it is not allocated.
Comparisons with a missing target return false, including `NotEq` and
`NotIn`. A reachable zero scalar is still compared normally. To complement
a condition including its missing case, use an
explicit `Not` group rather than assuming a negative comparison does so.

## Condition method index

The calls below belong to `*ConditionBuilder`, not package-level functions:

| Method | Meaning |
| --- | --- |
| `Eq("Plan", "pro")` | Target equals value |
| `NotEq("Plan", "free")` | Reachable target differs from value |
| `In("Plan", "pro", "enterprise")` | Target matches a listed value |
| `NotIn("Plan", "free", "trial")` | Reachable target matches none |
| `Present("Email")` | Target is present |
| `Absent("Email")` | Target is absent |
| `And(callbacks...)` | All nested conditions |
| `Or(callbacks...)` | Any nested condition |
| `Not(callbacks...)` | Complement of nested condition group |
| `If[T](func(T) bool)` | Typed predicate over the nearest struct |

Zero scalars and nil pointers or containers are absent. Non-nil pointers
to zero and non-nil empty collections are present. A zero `time.Time`
is absent regardless of its location.

## Typed object predicates

Use `If` for an expression that is clearer as Go logic. Within a `Purchase`
field callback:

```go
type Purchase struct {
	Quantity int
	Price    int
	Approval string
}

approval.RequiredIf(func(condition *validate.ConditionBuilder) {
	condition.If(func(value Purchase) bool {
		return value.Quantity*value.Price > 1_000
	})
})
```

Here `approval` is a `*validate.StringBuilder[string]` for `Purchase.Approval`.
The predicate's type must match the enclosing struct. The predicate runs
for each input; it does not run while configuring the schema.

## Collection conditions

Condition counterparts take a field path before the predicate or keys.
For an approval field beside `Labels map[string]string`:

```go
approval.RequiredIf(func(condition *validate.ConditionBuilder) {
	condition.Has("Labels", "regulated")
})
```

Approval is required when the key exists, even if its value is empty.
`HasAny` and `HasAll` similarly check one or all listed keys.

To require approval when a member is an administrator:

```go
type Member struct {
	Admin bool
}

approval.RequiredIf(func(condition *validate.ConditionBuilder) {
	condition.Contains("Members", func(_ int, member Member) bool {
		return member.Admin
	})
})
```

Here `Members` is `[]Member` on the enclosing struct and `approval` is its
string field builder. The slice predicate must take `int` and the exact
element type; map predicates take the exact key and value types.

| Condition | Test on the selected collection |
| --- | --- |
| `Has(path, key)` | Map key exists, independent of its value |
| `HasAny(path, keys...)` | Any listed map key exists |
| `HasAll(path, keys...)` | Every listed map key exists |
| `Contains(path, predicate)` | At least one entry matches |
| `Every(path, predicate)` | Every entry matches |
| `UniqueBy(path, projection)` | Projected comparable identities are unique |
| `CountWhere(path, predicate, countCallbacks...)` | Matching count satisfies pure integer constraints |

Paths and callback signatures are validated at construction. Count callbacks
cannot normalize or default their count. Collection predicates and conditions
are runtime-only; JSON Schema does not encode them.

Unlike an optional collection's own assertions, a collection condition does
not skip a reachable nil slice or map. It observes empty iteration: `Contains`
is false, `Every` and `UniqueBy` are true, and `CountWhere` checks zero.
`Has` is false for a nil map; `HasAny` with no keys is false and `HasAll`
with no keys is true. An unreachable path behind a nil intermediate pointer
returns false for all these observations, including `Every` and `HasAll`.

## Preparation and effect order

Conditions see unconditional normalization and defaults, including nested
preparation, before field-condition evaluation. They do not see raw strings
when a sibling has an unconditional `Trim`.

Conditional `When` settings and `ExcludeIf` effects execute in their defined
declaration and field execution order. Do not design mutually dependent
branches that assume every conditional effect has already completed. For
multi-field invariants independent of those conditional effects, use an
[object-wide check](advanced.md#object-wide-checks).

For exact condition signatures, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
