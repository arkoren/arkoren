---
title: Validation Normalizers
nav: Normalizers
description: Trim text, clamp numbers, and clean lists before checking the parsed copy.
order: 60
---

# Validation Normalizers

## Introduction

An imported tag such as `" Go "` may be acceptable if you store it as `"go"`.
A **normalizer** makes that deliberate change before checking the value.
Use an assertion instead when you must reject the original input rather than
silently clean it.

`Parse` returns the normalized copy. `Validate` checks the same prepared value
but discards the result. Neither modifies the caller's input. Examples use an
existing `context.Context` named `ctx`.

## String normalizers

To trim and lowercase an imported tag:

```go
tag := validate.String(func(value *validate.StringBuilder[string]) {
	value.Trim()
	value.ToLower()
})
parsed, err := tag.Parse(ctx, " Go ")
```

On success, `parsed` is `"go"`. `Lowercase()` would instead reject uppercase
letters without changing them.

| Normalizer | Small call | Change |
| --- | --- | --- |
| `Trim` | `value.Trim()` | Remove surrounding Unicode whitespace |
| `ToLower` | `value.ToLower()` | Lowercase the text |
| `ToUpper` | `value.ToUpper()` | Uppercase the text |
| `TrimPrefix` | `value.TrimPrefix("acct_")` | Remove one matching prefix |
| `TrimSuffix` | `value.TrimSuffix(".csv")` | Remove one matching suffix |
| `Truncate` | `value.Truncate(80)` | Keep at most 80 runes |

These settings belong to `*StringBuilder[S]`, preserving S in the result.
`Truncate` does not report a length violation. Use `Max` to reject long input.
`Truncate(0)` produces an empty string; a negative length panics with an error
matching `validate.ErrInvalidSchema` during construction.

## Builder operations

These are transforming settings, not assertions. Configure them on the
builder for the value you intend to replace.

| Operation | Permitted builder roles and scope | Details |
| --- | --- | --- |
| `Trim()`, `ToLower()`, `ToUpper()`, `TrimPrefix(prefix)`, `TrimSuffix(suffix)`, `Truncate(n)` | `StringBuilder[S]`; preserve the exact string type S | [String normalizers](#string-normalizers) |
| `Abs()`, `Clamp(low, high)` | `IntBuilder[N]`; no rounding method | [Numeric normalizers](#numeric-normalizers) |
| `Clamp(low, high)` | `UintBuilder[N]`; no `Abs` or rounding method | [Numeric normalizers](#numeric-normalizers) |
| `Abs()`, `Clamp(low, high)`, `Round(places)` | `FloatBuilder[N]`; nonnegative decimal places | [Numeric normalizers](#numeric-normalizers) |
| `UTC()` | `TimeBuilder`; preserve the instant | [Time normalizers](#time-normalizers) |
| `Sort(compare)`, `Filter(keep)`, `Reject(drop)`, `RejectZero()`, `Dedupe()` | `SliceBuilder[E]`; preserve or remove entries in the parsed copy | [Slice normalizers](#slice-normalizers) |
| `Normalize(fn)` | All explicit builders, including `BoolBuilder`, `TextBuilder`, `AnyBuilder`, `FileBuilder`, `MapBuilder`, `PtrBuilder`, and `StructBuilder`; fn preserves the role's exact value type | [Custom normalization](#custom-normalization) |

No normalizer is permitted on a map-key child, even when its concrete scalar
builder normally has that method. Count constraints and condition observations
also prohibit normalization. `MapBuilder` has custom `Normalize` but no
built-in filtering, sorting, or deduplication methods.

## Numeric normalizers

To constrain an imported percentage by changing out-of-range values:

```go
percentage := validate.Int(func(value *validate.IntBuilder[int]) {
	value.Clamp(0, 100)
})
parsed, err := percentage.Parse(ctx, 120)
```

On success, `parsed` is 100. A `Max(100)` assertion would reject 120 instead.

| Normalizer | Builders | Behavior |
| --- | --- | --- |
| `Abs()` | `Int`, `Float` | Absolute value; minimum signed integer stays unchanged |
| `Clamp(low, high)` | `Int`, `Uint`, `Float` | Clamp to the inclusive range; low greater than high panics |
| `Round(places)` | `Float` | Round to nonnegative decimal places, halves away from zero; negative places panic |

To round a measured value:

```go
measurement := validate.Float(func(value *validate.FloatBuilder[float64]) {
	value.Round(2)
})
parsed, err := measurement.Parse(ctx, 12.345)
```

On success, the result is 12.35. Rounding changes the value; do not use it
when retaining the original precision is required.

## Time normalizers

`UTC` changes a time's location without changing its instant:

```go
timestamp := validate.Time(func(value *validate.TimeBuilder) {
	value.UTC()
})
```

Parsing returns the time in UTC. The original time value is unchanged.

## Slice normalizers

To clean a list of tags before checking its size:

```go
tags := validate.Slice(func(items *validate.SliceBuilder[string]) {
	items.Values(func(tag *validate.StringBuilder[string]) {
		tag.Trim()
		tag.ToLower()
	})
	items.RejectZero()
	items.Dedupe()
	items.Min(1)
})
parsed, err := tags.Parse(ctx, []string{" Go ", "", "go", "Queues"})
```

On success, `parsed` is `[]string{"go", "queues"}`. Entries normalize first,
so the duplicate test sees two identical `"go"` values. Error indices refer
to the cleaned slice rather than original row positions.

For numeric lists, these predicates keep positive values or identify negative
ones to drop:

```go
positive := func(_ int, n int) bool {
	return n > 0
}
negative := func(_ int, n int) bool {
	return n < 0
}
```

Use them on a `*validate.SliceBuilder[int]` named `items`:

| Normalizer | Small call | Behavior |
| --- | --- | --- |
| `Sort` | `items.Sort(cmp.Compare[int])` | Sort using the supplied comparator |
| `Filter` | `items.Filter(positive)` | Keep matching entries |
| `Reject` | `items.Reject(negative)` | Drop matching entries |
| `RejectZero` | `items.RejectZero()` | Drop zero entries |
| `Dedupe` | `items.Dedupe()` | Keep the first occurrence of each comparable, non-interface value |

`Filter` is not `Every`: filtering removes entries, while `Every` reports
invalid ones. `Dedupe` is not `Unique`: deduplication changes the result,
while uniqueness rejects duplicates. See [collection assertions](collections.md).

## Custom normalization

`Normalize` accepts a typed function that returns a replacement value.
Remove a known decoration:

```go
name := validate.String(func(value *validate.StringBuilder[string]) {
	value.Normalize(func(input string) string {
		return strings.TrimPrefix(input, "@")
	})
})
```

Parsing `"@ada"` returns `"ada"`. All explicit builders support `Normalize`;
the function must preserve the builder's value type. Do not mutate shared
maps, slices, or pointers inside a custom normalizer. There is no error return;
use `Check` when an operation can fail.

## Execution order

Normalizers run in their declaration order relative to one another, before
presence, defaults, and ordinary rules. Their placement among ordinary rules
does not change that order. Whitespace-only text becomes absent after `Trim`,
so `Required` fails or `Default` applies.

Defaults are not automatically normalized again. Supply defaults already in
their final form. On slices, element normalization precedes slice
normalization; collection assertions and element checks run afterward.
Map values and pointer pointees likewise prepare their children before checks.
Each normalizer runs once per value along that path.

Normalizers and defaults are forbidden on map keys. They are also forbidden
in condition predicates and `CountWhere` count constraints: those APIs test
values and must not rewrite the data that decides a rule.

For custom-check ordering and shared-state requirements, see
[advanced validation](advanced.md). For exact signatures, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
