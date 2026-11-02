---
title: Slices and Maps
description: Check collection entries, membership, identities, and matching counts without removing data.
order: 40
summary:
  - Validate collection sizes and exact-type keys or values.
  - Assert membership, uniqueness, and matching counts on prepared entries.
  - Adapt rules to named containers while preserving their types.
---

# Slices and Maps

A recipient list needs a minimum size and a valid address in every position.
Collection schemas separate these responsibilities: container rules check the
list or map, while `Values` and `Keys` configure its children. Assertions leave
the collection intact; use [Normalizers](normalizers.md#clean-lists) if removing
or reordering entries is intentional.

Examples import `arkoren.dev/foundation/validate` and use an existing context
named `ctx`.

## Validate Entries

Require a nonempty list and check every supplied email address:

```go
recipients := validate.Slice(func(items *validate.SliceBuilder[string]) {
	items.Required()
	items.Min(1)
	items.Values(func(address *validate.StringBuilder[string]) {
		address.Email()
	})
})
err := recipients.Validate(ctx, []string{"ada@example.com"})
```

This passes. Nil fails `Required`; an allocated empty slice fails `Min(1)`.
Every entry is supplied, so an empty address is checked rather than skipped.
Entry diagnostics use indices such as `/0`. `Min`, `Max`, and `Len` count
entries, with inclusive bounds; they also apply to maps.

`Values` takes a concrete child callback, not a published schema. To reuse a
policy, call `address.Use(email)` inside that callback. Omitted child rules mean
no per-entry validation.

## Check Map Entries

An inventory import can constrain product codes separately from quantities:

```go
quantities := validate.Map(func(entries *validate.MapBuilder[string, int]) {
	entries.Keys(func(code *validate.StringBuilder[string]) {
		code.AlphaDash()
	})
	entries.Values(func(quantity *validate.IntBuilder[int]) {
		quantity.Min(1)
	})
})
err := quantities.Validate(ctx, map[string]int{"book": 0})
```

The supplied zero fails at `/book`. Map keys and values are checked even when
zero. Keys cannot normalize, default, or exclude: rewriting a key could change
its identity or cause a collision.

Traversal uses sorted diagnostic names. Encodable keys use their JSON-v2 object
names, including custom marshaling; other keys use `fmt.Sprint`. Names that
collide share a pointer without losing violations, but their relative order is
unspecified. Pointer segments escape `/` as `~1` and `~` as `~0`.

## Require Membership

For a map label whose existence matters regardless of its value, use `Has`:

```go
labels := validate.Map(func(entries *validate.MapBuilder[string, string]) {
	entries.Has("regulated")
})
err := labels.Validate(ctx, map[string]string{"regulated": ""})
```

This passes. `HasAny` requires at least one listed key and `HasAll` requires
every listed key. Empty key lists make `HasAny` false and `HasAll` true.

When the requirement concerns an entry's contents, use a predicate instead.
For a team roster, define the entry type once:

```go
type Member struct {
	ID    string `json:"id"`
	Admin bool   `json:"admin"`
}
```

Require at least one administrator without removing other members:

```go
team := validate.Slice(func(items *validate.SliceBuilder[Member]) {
	items.Contains(func(_ int, member Member) bool {
		return member.Admin
	})
})
```

A failed `Contains` belongs to the collection. `Every` instead requires each
entry to match and reports every failing entry path. Use `Values` when each
entry needs several rules or nested field diagnostics. Slice predicates take
`func(int, E) bool`; map predicates take the exact `func(K, V) bool`.

## Reject Duplicates

Use a projected identity when two records can differ but still name the same
account. This alternative team schema rejects repeated IDs:

```go
team := validate.Slice(func(items *validate.SliceBuilder[Member]) {
	items.UniqueBy(func(_ int, member Member) string {
		return member.ID
	})
})
```

The projection must return a comparable identity. Duplicate diagnostics identify
the later entry and its first conflict. Map `UniqueBy` takes `func(K, V) U`
and checks projected identities, not keys, which are already unique.

For plain comparable, non-interface slice elements, `Unique()` compares the
values directly. It rejects duplicates; `Dedupe()` deliberately removes them.
Maps have no `Unique` method.

## Count Matches

Total team size cannot express an exact administrator count. Count the matches
and constrain that actual integer:

```go
team := validate.Slice(func(items *validate.SliceBuilder[Member]) {
	items.CountWhere(func(_ int, member Member) bool {
		return member.Admin
	}, func(count *validate.IntBuilder[int]) {
		count.Min(2)
		count.Max(2)
	})
})
```

Exactly two administrators pass; a failed count rule is attached to the
collection. Zero matches is supplied and is checked. Map predicates receive
their exact key and value types.

Count callbacks accept pure numeric assertions: `Min`, `Max`, `Between`, `Gt`,
`Gte`, `Lt`, `Lte`, `Positive`, `NonNegative`, `MultipleOf`, `OneOf`, and
`NotOneOf`. They cannot default, normalize, require, exclude, reference fields,
add conditions, or run `Check`. `Bail` and `Describe` do not change the count.

## Empty Collections

Optional nil collections skip their own assertions. Allocated empty collections
are present: `Contains` fails, `Every` and uniqueness pass, `CountWhere` checks
zero, and `Has` fails. Add both `Required` and `Min(1)` when neither nil nor
empty is acceptable.

Predicates run during validation, not declaration. They must be pure and safe
for concurrent use. Child preparation precedes container normalizers, then
container assertions and child checks run. Diagnostics use the resulting
indices. Arbitrary Go predicates are not exported to JSON Schema; `Unique`
can export `uniqueItems`.

## Named Containers

A named slice does not have the exact type of `[]string`. Adapt the source
schema explicitly rather than converting your application field:

```go
type Names []string
names := validate.Slice(func(items *validate.SliceBuilder[string]) {
	items.Values(func(name *validate.StringBuilder[string]) {
		name.Trim()
		name.Min(1)
	})
})
named := validate.Named[Names](names)
parsed, err := named.Parse(ctx, Names{" Ada "})
```

On success, `parsed` retains type `Names` and contains `"Ada"`. For a struct
field, use `func(values *validate.NamedBuilder[Names])` and `values.Use(names)`.
This also supports named maps, including eager foundation collection types.

Underlying container types must be identical: there are no element/key
conversions or scalar adaptation. Source callbacks receive ordinary containers,
and field comparisons retain their exact-type constraints. Configure all source
rules before attachment; a second `Use` cannot replace an attached schema.

`Named` exports logical source constraints, not a custom marshaler's wire shape.
In particular, builtin-byte slices encode as base64 strings, while slice rules
describe elements. A defined `uint8` element encodes as an array instead. Use
[type inference](json-schema.md#describe-output) for output descriptions, not
runtime collection validation.

For every collection assertion and adapter, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
