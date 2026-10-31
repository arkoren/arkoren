---
title: Validating Slices and Maps
nav: Slices & Maps
description: Configure collection children and assert membership, uniqueness, and matching counts without rewriting input.
order: 40
---

# Validating Slices and Maps

## Introduction

An email list needs two kinds of checks: whether the list has enough recipients,
and whether each recipient is valid. Collection schemas check both the container
and its entries. `Values` configures
each entry; `Keys` configures map keys. Assertions such as `Contains`, `Every`,
and `CountWhere` check the prepared collection without removing or changing it.

Examples use an existing `context.Context` named `ctx`.
Use [normalizers](normalizers.md#slice-normalizers)
only when you intend to filter, sort, or deduplicate the parsed result.

## Checking each slice element

To require at least one email address:

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
Every element is treated as supplied, so an empty entry is checked rather
than silently skipped. Entry failures use numeric paths such as `/0`.

`Min(n)`, `Max(n)`, and `Len(n)` constrain the number of elements. Bounds are
inclusive. These names also apply to maps, where they count entries.

## Checking named containers

Fields typed as `collection.Slice[T]`, `collection.Map[K, V]`, or your own named
containers do not have the exact Go type accepted by `SliceBuilder` or
`MapBuilder`. To keep those field types and reuse ordinary collection rules,
attach the source schema through `NamedBuilder`. Import
`arkoren.dev/foundation/validate` for this application-owned type:

```go
type Names []string

type Request struct {
	Names Names `json:"names"`
}

names := validate.Slice(func(items *validate.SliceBuilder[string]) {
	items.Required()
	items.Values(func(name *validate.StringBuilder[string]) {
		name.Trim().Min(1)
	})
})
request := validate.Struct(func(object *validate.StructBuilder[Request]) {
	object.Field("Names", func(values *validate.NamedBuilder[Names]) {
		values.Use(names)
	})
})
parsed, err := request.Parse(ctx, Request{Names: Names{" Ada "}})
```

On success, `parsed.Names` still has type `Names` and contains `"Ada"`. The
input is not modified; empty names fail at paths such as `/names/0`. The same
adapter works for named maps. For standalone validation, use
`validate.Named[Names](names)` instead of a child callback.

Source and target must have identical underlying container types; element or
key conversions and scalar adaptation are not supported. Source callbacks still
receive ordinary slices/maps, not the named container. Field comparisons retain
their source schema's exact-type requirements; the adapter does not convert
comparison targets. Configure all rules before attaching the source: `Use`
cannot replace an already attached schema.

`Named` exports its source constraints, not a custom marshaler's wire shape.
For output descriptions, [`Infer`](advanced.md#describing-types-without-rules)
recognizes eager `collection.Slice` and `collection.Map` directly. Inference
adds no runtime rules. In particular, collections of builtin `byte` encode as base64 strings;
a slice-rule schema instead describes logical element constraints and is not
a wire schema for builtin-byte collections. A defined `uint8` element type
instead uses ordinary JSON array encoding.

## Builder operations

### SliceBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Values(callbacks...)` | Configure each E with its concrete child builder | [Element rules](#checking-each-slice-element) |
| `Use(schema)` | Import a supported `Schema[[]E]`, preserving imported children | [Reuse constraints](structs-and-reuse.md#reusing-a-schema) |
| `Min(n)`, `Max(n)`, `Len(n)` | Assert element count | [Collection bounds](#checking-each-slice-element) |
| `Contains(func(int, E) bool)` | Require at least one matching index/element pair | [Matching entries](#requiring-a-matching-entry) |
| `Every(func(int, E) bool)` | Assert all entries; failures identify indices | [Every entry](#requiring-every-entry-to-match) |
| `Unique()` | Reject repeated comparable, non-interface elements | [Unique values](#rejecting-duplicate-values) |
| `UniqueBy(func(int, E) U)` | Reject repeated comparable projected identities | [Unique identities](#rejecting-duplicate-identities) |
| `CountWhere(predicate, countCallbacks...)` | Assert the actual matching count with integer rules | [Matching counts](#counting-matching-entries) |
| `Sort(compare)`, `Filter(keep)`, `Reject(drop)`, `RejectZero()`, `Dedupe()` | Transform the parsed slice rather than assert facts | [Slice normalizers](normalizers.md#slice-normalizers) |

### MapBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Keys(callbacks...)`, `Values(callbacks...)` | Configure exact K and V child builders | [Key and value rules](#checking-map-keys-and-values) |
| `Use(schema)` | Import a supported `Schema[map[K]V]`, preserving imported children | [Reuse constraints](structs-and-reuse.md#reusing-a-schema) |
| `Min(n)`, `Max(n)`, `Len(n)` | Assert entry count | [Collection bounds](#checking-each-slice-element) |
| `Has(key)`, `HasAny(keys...)`, `HasAll(keys...)` | Assert key existence regardless of stored values | [Map keys](#requiring-a-map-key) |
| `Contains(func(K, V) bool)` | Require a matching key/value pair; does not mean key membership | [Matching entries](#requiring-a-matching-entry) |
| `Every(func(K, V) bool)` | Assert all entries; failures identify keys | [Every entry](#requiring-every-entry-to-match) |
| `UniqueBy(func(K, V) U)` | Reject repeated comparable projected identities | [Unique identities](#rejecting-duplicate-identities) |
| `CountWhere(predicate, countCallbacks...)` | Assert the actual matching count with integer rules | [Matching counts](#counting-matching-entries) |

### Shared collection settings

These settings apply to the entire slice or map. Use a child callback for an
individual entry's settings.

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Required()`, `Default(value)` | Reject nil or supply an absent collection | [Presence](#empty-and-absent-collections) |
| `Check(fn)`, `Bail()`, `Normalize(fn)`, `Describe(text)` | Custom checks, failure control, replacement, and annotation | [Common operations](overview.md#builder-operations) |
| `EqField`, `NeField`, `RequiredIf`, `RequiredUnless`, `RequiredWith`, `RequiredWithAll`, `RequiredWithout`, `ProhibitedIf`, `ProhibitedUnless`, `ExcludeIf`, `When` | Compare or configure a collection field in its enclosing struct | [Field conditions](conditions.md#field-builder-operations) |

Map keys cannot use defaults, exclusion, or normalization. `MapBuilder` has
no `Sort`, `Filter`, `Reject`, `RejectZero`, `Dedupe`, or `Unique` method;
its keys are already unique. U in `UniqueBy` is the comparable projection
result type, inferred from your function.

## Checking map keys and values

To check quantities keyed by product code:

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

The `book` quantity fails `Min(1)` because map entries are supplied even when
zero. Map entries are visited in deterministic key order. Paths escape `/`
as `~1` and `~` as `~0`.

Do not wrap `Keys` or `Values` callbacks in scalar constructors. They already
receive the appropriate typed builder. Key schemas cannot normalize, default,
or exclude keys: changing a key could cause a collision or change its identity.

## Requiring a map key

`Has` checks key existence, regardless of the stored value. To require a label:

```go
labels := validate.Map(func(entries *validate.MapBuilder[string, string]) {
	entries.Has("regulated")
})
err := labels.Validate(ctx, map[string]string{"regulated": ""})
```

This passes. A present key with an empty value is still a key. `HasAny(keys...)`
requires at least one listed key; `HasAll(keys...)` requires every listed key.
These rules do not inspect or modify values.

## Requiring a matching entry

`Contains` requires at least one entry to match a predicate. A slice predicate
receives its index and exact element type:

```go
scores := validate.Slice(func(items *validate.SliceBuilder[int]) {
	items.Contains(func(_ int, score int) bool {
		return score >= 90
	})
})
err := scores.Validate(ctx, []int{70, 95})
```

This passes because 95 matches. A failed assertion is attached to the whole
collection, not an invented missing element. The slice still contains both
scores after parsing.

For a map, the predicate receives its exact key and value types:

```go
quantities := validate.Map(func(entries *validate.MapBuilder[string, int]) {
	entries.Contains(func(_ string, quantity int) bool {
		return quantity > 0
	})
})
err := quantities.Validate(ctx, map[string]int{"book": 2, "pen": 0})
```

This passes because `book` has a positive quantity. Collection `Contains`
differs from string `Contains`, which takes a substring.

## Requiring every entry to match

`Every` checks all entries and reports failures at their entry paths:

```go
scores := validate.Slice(func(items *validate.SliceBuilder[int]) {
	items.Every(func(_ int, score int) bool {
		return score >= 0
	})
})
err := scores.Validate(ctx, []int{5, -1})
```

This fails at `/1`. Map `Every` instead takes `func(K, V) bool` and reports
the failing key paths. Use a typed `Values` schema when each entry needs
several ordinary rules or nested field errors.

## Rejecting duplicate values

`Unique` rejects duplicate slice elements; it does not deduplicate them:

```go
tags := validate.Slice(func(items *validate.SliceBuilder[string]) {
	items.Unique()
})
err := tags.Validate(ctx, []string{"go", "go"})
```

This fails. `Unique` requires a comparable, non-interface element type.
Use [`Dedupe`](normalizers.md#slice-normalizers) only when repeated values
should be removed instead of rejected.

## Rejecting duplicate identities

Two member records can have different names but refer to the same account.
To reject that duplicate account, use `UniqueBy` to select the identifier used
for comparison rather than comparing entire records:

```go
type Member struct {
	ID   string
	Name string
}

members := validate.Slice(func(items *validate.SliceBuilder[Member]) {
	items.UniqueBy(func(_ int, member Member) string {
		return member.ID
	})
})
```

Two different members with the same `ID` fail even if their names differ.
The projection must return a comparable key. Map `UniqueBy` takes
`func(K, V) Key`; it checks projected identities, not map keys, which are
already unique. Duplicate reporting is deterministic.

## Counting matching entries

If a list must contain exactly two passing scores, its total length is not
enough: failing scores should not count toward that requirement. `CountWhere`
counts predicate matches, then checks that count using a restricted integer
builder:

```go
scores := validate.Slice(func(items *validate.SliceBuilder[int]) {
	items.CountWhere(
		func(_ int, score int) bool {
			return score >= 60
		},
		func(count *validate.IntBuilder[int]) {
			count.Min(2)
			count.Max(2)
		},
	)
})
err := scores.Validate(ctx, []int{40, 70, 80})
```

This passes. A failed count rule belongs to the collection. Map predicates
receive `K` and `V` instead of an index and element. Count constraints do
not normalize or default the count; they assert its actual value. Presence
rules, field references, conditions, and operational `Check` functions are
also invalid there. The numeric assertions are `Min`, `Max`, `Between`, `Gt`,
`Gte`, `Lt`, `Lte`, `Positive`, `NonNegative`, `MultipleOf`, `OneOf`, and
`NotOneOf`. `Bail` and `Describe` do not change the count. Zero matches is a
present count, so it is checked against `Min` and `Max`.

## Empty and absent collections

Optional nil slices and maps skip collection assertions. Add `Required` to
reject nil. An allocated empty collection is present and has these results:

| Assertion | Empty collection |
| --- | --- |
| `Contains(predicate)` | Fails: no entry matches |
| `Every(predicate)` | Passes: no entry fails |
| `Unique()` / `UniqueBy(projection)` | Passes: no duplicates |
| `CountWhere(predicate, rules...)` | Checks the supplied count 0 |
| `Has(key)` | Fails: key is absent |

Predicates run during validation, not construction. Keep them deterministic
and safe for concurrent use. They are runtime checks omitted from JSON Schema.
For collection-dependent field rules, see
[collection conditions](conditions.md#collection-conditions).

For exact types and errors, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
