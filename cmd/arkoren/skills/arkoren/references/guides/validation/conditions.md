---
title: Conditions
description: Select field requirements and scoped rules from prepared values in the enclosing struct.
order: 50
summary:
  - Require, prohibit, exclude, or further constrain a field conditionally.
  - Combine observations using Go field paths and exact-type predicates.
  - Account for missing paths, empty collections, and conditional effect order.
---

# Conditions

A delivery form needs an address for shipping, but not for pickup. A
**condition** observes input without changing it or producing a violation.
The field builder chooses the effect: require, prohibit, exclude, or add rules.
Conditions read the nearest enclosing struct, so start with a
[struct schema](structs-and-reuse.md).

Examples import `arkoren.dev/foundation/validate`. Construction callbacks run
once; their recorded observations run for each input.

## Require a Field

Define a delivery form whose address requirement depends on its mode:

```go
type Delivery struct {
	Mode    string `json:"mode"`
	Address string `json:"address"`
	Email   string `json:"email"`
	Phone   string `json:"phone"`
}
delivery := validate.Struct(func(object *validate.StructBuilder[Delivery]) {
	object.Field("Address", func(address *validate.StringBuilder[string]) {
		address.RequiredIf(func(condition *validate.ConditionBuilder) {
			condition.Eq("Mode", "shipping")
		})
	})
})
```

An empty address fails for shipping. Otherwise it is optional, not prohibited.
`RequiredUnless` reverses the condition. Multiple condition callbacks configure
one fresh condition draft, with observations combined using AND.

## Presence Dependencies

If contact details need at least one channel, configure the phone field with
`phone.RequiredWithout("Email")`. Here `phone` is its
`*validate.StringBuilder[string]` inside the `Delivery` declaration.

| Setting | Requires this field when |
| --- | --- |
| `RequiredWith("Email", "Address")` | Any listed field is present |
| `RequiredWithAll("Email", "Address")` | Every listed field is present |
| `RequiredWithout("Email", "Address")` | Any listed field is absent |

These shortcuts take string paths, not condition callbacks. Presence follows
typed rules: zero scalars and nil pointers/containers are absent; non-nil
pointers to zero and allocated empty collections are present. Zero time is
absent regardless of location. This is not JSON-key existence.

## Prohibit or Exclude

For pickup, decide whether an entered address is a mistake or stale form state.
To report it as a mistake, add this setting inside the address callback:

```go
address.ProhibitedIf(func(condition *validate.ConditionBuilder) {
	condition.Eq("Mode", "pickup")
})
```

A supplied address now fails for pickup. `ProhibitedUnless` allows a supplied
value only when its condition matches.

If you deliberately want to discard stale state instead, use this alternative:

```go
address.ExcludeIf(func(condition *validate.ConditionBuilder) {
	condition.Eq("Mode", "pickup")
})
```

Matching input skips the field's later rules and `Parse` clears the value in
the result. Exclusion is not rejection and does not modify the caller's input.
Do not use it when the presence of that data must be reported.

## Add Branch Rules

Some modes need stronger constraints while retaining ordinary rules. Within
the same string address callback, add a minimum only for shipping:

```go
address.Max(200)
address.When(func(condition *validate.ConditionBuilder) {
	condition.Eq("Mode", "shipping")
}, func(text *validate.StringBuilder[string]) {
	text.Min(10)
})
```

The maximum applies to every supplied address; the minimum applies only in
the branch. `When` takes one condition callback followed by callbacks for the
same concrete builder role. Supply settings, not a published schema. Branch
callbacks configure the schema once and are not rerun per input.

## Combine Observations

An explicit group expresses alternatives. For an approval field beside string
fields `Plan` and `Role`, use an OR group:

```go
approval.RequiredIf(func(condition *validate.ConditionBuilder) {
	condition.Or(func(group *validate.ConditionBuilder) {
		group.Eq("Plan", "enterprise")
		group.Eq("Role", "admin")
	})
})
```

Here `approval` is its string field builder. Either comparison requires
approval. Ungrouped observations use AND; `And` makes that grouping explicit,
and `Not` complements its nested group. Empty conditions and empty explicit
groups are definition errors.

`Eq` and `NotEq` compare a selected field with an assignable value. `In` and
`NotIn` compare against a captured value list. `Present` and `Absent` observe
typed presence. For logic clearer in Go, configure a typed predicate. Within
the delivery address callback, this requires an address for shipping when email
is absent:

```go
address.RequiredIf(func(condition *validate.ConditionBuilder) {
	condition.If(func(value Delivery) bool {
		return value.Mode == "shipping" && value.Email == ""
	})
})
```

The predicate argument must exactly match the nearest struct type. It is a pure
observation: it cannot normalize data or return an operational error.

## Field Paths

Paths use dot-separated Go names, such as `"Billing.Country"` for a field
`Billing *Address` whose address has a string `Country`. They walk structs and
pointers, including promoted fields, but cannot index collections, select
parents, or use JSON tag names. Inside an address child schema, paths begin at
that address rather than its outer form.

Bad paths and incompatible comparison types are construction errors. A nil
intermediate pointer is unreachable and is never allocated. `Eq`, `NotEq`,
`In`, and `NotIn` all return false for an unreachable target. Use an explicit
`Not` group if the missing case must count as the complement. Reachable scalar
zeros are compared normally.

`Present` is false for an unreachable path; `Absent` is true. Field assertions
such as `EqField` use the same resolver but fail when the target is unreachable,
rather than comparing an invented zero.

## Observe Collections

To require approval when any team member is an administrator, assume the
enclosing form has `Members []Member`, where `Member` contains `Admin bool`:

```go
approval.RequiredIf(func(condition *validate.ConditionBuilder) {
	condition.Contains("Members", func(_ int, member Member) bool {
		return member.Admin
	})
})
```

Slice predicates take the exact `(int, E)` types; maps take `(K, V)`. Their
results observe the collection, not validate its entries:

| Observation | Condition is true when |
| --- | --- |
| `Has`, `HasAny`, `HasAll` | A map has the selected key, any listed key, or all listed keys |
| `Contains` | At least one entry matches |
| `Every` | Every entry matches |
| `UniqueBy` | Comparable projected identities are unique |
| `CountWhere` | The actual matching count satisfies pure integer constraints |

Each takes the field path first. `CountWhere` uses the same restricted
[count rules](collections.md#count-matches) as a collection assertion.

A reachable nil collection is observed as empty, unlike an optional
collection's own assertions: `Contains` is false; `Every` and `UniqueBy` are
true; count rules check zero. Nil maps lack every key. Empty key lists make
`HasAny` false and `HasAll` true. An unreachable path instead returns false for
all collection observations, including `Every` and `HasAll`.

## Effect Order

Conditions see unconditional normalization and defaults in reachable validated
children before field rules run, regardless of Go field order. A sibling with
`Trim` is observed after trimming, not as raw text.

Conditional `When` and `ExcludeIf` effects remain sensitive to declaration and
field execution order. They do not repeatedly evaluate until every dependent
field settles. Avoid mutually dependent branches that assume later effects have
already happened. An [object check](custom-rules.md#object-invariants) sees
unconditional preparation, but is not a final hook after conditional effects.

Conditions and arbitrary predicates are runtime-only and omitted from JSON
Schema. Keep predicates read-only and concurrency-safe.

For exact settings and observations, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
