---
title: Normalizers
description: Prepare cleaned values before checking presence and rules, without changing caller-owned input.
order: 60
summary:
  - Choose deliberate transformations instead of rejecting the original value.
  - Clean text, numbers, times, and lists in the parsed result.
  - Account for child preparation, defaults, diagnostic indices, and callback ownership.
---

# Normalizers

An imported tag such as `" Go "` may be usable if you store it as `"go"`.
A **normalizer** makes that deliberate replacement before validation. Use an
assertion instead when the original spelling or range must be rejected.

`Parse` returns the prepared value; `Validate` checks it and discards it.
Neither modifies caller-owned input. Examples import
`arkoren.dev/foundation/validate` and use an existing context named `ctx`.

## Clean Text

Accept surrounding whitespace and mixed case for imported tags:

```go
tag := validate.String(func(text *validate.StringBuilder[string]) {
	text.Trim()
	text.ToLower()
	text.Required()
})
parsed, err := tag.Parse(ctx, " Go ")
```

On success, `parsed` is `"go"`. `Lowercase` would instead reject uppercase
letters without changing them.

| Setting | Replacement |
| --- | --- |
| `Trim()` | Remove surrounding Unicode whitespace |
| `ToLower()`, `ToUpper()` | Change case |
| `TrimPrefix(prefix)`, `TrimSuffix(suffix)` | Remove one matching prefix or suffix |
| `Truncate(n)` | Keep at most n runes |

These preserve the builder's exact string type. Truncation does not report a
length violation: use `Max` to reject long text. Zero truncates to empty;
negative lengths are invalid definitions.

## Adjust Numbers

For an import policy that intentionally caps percentages rather than rejecting
out-of-range input, use `Clamp`:

```go
percentage := validate.Int(func(number *validate.IntBuilder[int]) {
	number.Clamp(0, 100)
})
parsed, err := percentage.Parse(ctx, 120)
```

The result is 100 on success. `Max(100)` would reject 120 instead. Signed,
unsigned, and floating builders support inclusive `Clamp`; reversed bounds
panic. Signed and floating builders also support `Abs`; the minimum signed
integer stays unchanged because its positive counterpart cannot fit.

Floating builders additionally support decimal rounding:

```go
measurement := validate.Float(func(number *validate.FloatBuilder[float64]) {
	number.Round(2)
})
parsed, err := measurement.Parse(ctx, 12.345)
```

The result is 12.35. Halves round away from zero; negative decimal places panic.
Rounding changes the quantity and cannot restore precision already lost. Do not
apply it when the original precision is part of the contract.

## Normalize Time

To store an accepted timestamp with UTC location, import the standard `time`
package for your input and configure `Time`:

```go
timestamp := validate.Time(func(instant *validate.TimeBuilder) {
	instant.UTC()
})
```

Parsing changes its location to UTC without changing the instant or original
value. Time assertions still compare instants.

## Clean Lists

Normalize entries before removing empty tags and duplicates:

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

The result is `[]string{"go", "queues"}` on success. Entry cleanup happens
first, so deduplication sees identical `"go"` values. Diagnostics use the cleaned
slice's indices, not original import-row positions.

`Sort` takes `func(E, E) int`, such as `cmp.Compare[int]` from the standard
`cmp` package. `Filter` keeps entries matching `func(int, E) bool`; `Reject`
drops matches. `RejectZero` drops zero entries. `Dedupe` keeps the first
occurrence and requires comparable, non-interface elements.

Filtering is not `Every`, and deduplication is not `Unique`: the former change
data while the latter [report invalid collections](collections.md). Maps have
custom `Normalize`, but no built-in sort/filter/deduplication operations. Map
keys never normalize, default, or exclude because that could change identity.

## Custom Replacement

For an application-specific decoration, return a same-type replacement from
`Normalize`. With the standard `strings` package imported:

```go
name := validate.String(func(text *validate.StringBuilder[string]) {
	text.Normalize(func(input string) string {
		return strings.TrimPrefix(input, "@")
	})
})
```

Parsing `"@ada"` produces `"ada"`. Every explicit rule builder supports typed
`Normalize`, including structs, pointers, maps, files, text values, and dynamic
values. Named-container adapters instead attach a configured source schema.

There is no error return. Use a [custom check](custom-rules.md) if an operation
can fail. Keep callbacks concurrency-safe and do not mutate nested maps, slices,
or pointers reachable from the input: arbitrary captured/default objects remain
application-owned, not recursively cloned.

## Preparation Order

Unconditional normalizers run in their own declaration order before presence,
defaults, and ordinary rules. Placing `Trim` after `Required` in the declaration
does not make presence run first. Whitespace-only text becomes absent after
trimming, so a requirement fails or a default applies.

Defaults are not normalized again at the value where they are assigned. Supply
them in final form. Nested children prepare before enclosing conditions; slice
elements prepare before slice normalizers, and pointer pointees and map values
likewise prepare before their enclosing checks. Each normalizer runs once along
that path. Container assertions and child validation then see prepared values.

Conditional transformations remain [order-sensitive](conditions.md#effect-order).
Map-key rules, condition observations, and `CountWhere` count constraints cannot
normalize or default values: they test identity or actual observations rather
than rewrite the data deciding a rule.

For exact transformation contracts, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
