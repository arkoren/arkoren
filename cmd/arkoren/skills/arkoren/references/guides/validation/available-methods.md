---
title: Scalar Rules
description: Reject unsuitable text, numbers, booleans, times, and uploaded files without rewriting them.
order: 20
summary:
  - Apply text formats, membership rules, and inclusive or strict numeric bounds.
  - Compare fields and test boolean intent or time windows.
  - Validate duration text and multipart files while retaining security boundaries.
---

# Scalar Rules

Rules reject input; they do not repair it. Choose them when an invalid value
must produce a diagnostic rather than a silently changed result. The
[overview](overview.md) establishes construction and execution; examples import
`arkoren.dev/foundation/validate` and use an existing context named `ctx`.

Absent scalar values skip ordinary rules. Add `Required` to reject absence, or
use a [pointer](structs-and-reuse.md#explicit-zero-values) when an explicit zero
must be checked. `String`, `Int`, `Uint`, `Float`, and `Bool` preserve named Go
types as well as built-in types. Without a callback to infer the type, write
`validate.String[string]()` or `validate.Int[int64]()`.

## Text Constraints

For a public display name, reject blank or overlong text without changing its
spelling:

```go
name := validate.String(func(text *validate.StringBuilder[string]) {
	text.Required()
	text.NotBlank()
	text.Max(80)
})
err := name.Validate(ctx, "Ada Lovelace")
```

This passes. Lengths count Unicode runes, not bytes. `NotBlank` rejects
whitespace-only text but does not trim it; [normalizers](normalizers.md) own
deliberate transformations.

| Rule | Accepted supplied text |
| --- | --- |
| [`Min`, `Max`, `Len`](#text-constraints) | Minimum, maximum, or exact nonnegative rune count |
| [`NotBlank`](#text-constraints) | At least one non-whitespace character |
| [`OneOf`, `NotOneOf`](#text-constraints) | Membership or nonmembership in the supplied typed values |
| [`Regex`](#text-constraints) | Matches a supplied `*regexp.Regexp`; anchor it to match the whole string |
| [`Prefix`, `Suffix`, `Contains`](#text-constraints) | Has the supplied prefix, suffix, or substring |
| [`Lowercase`, `Uppercase`](#text-constraints) | Contains no uppercase or no lowercase letters; no rewriting |
| [`Alpha`, `AlphaNumeric`, `AlphaDash`](#text-constraints) | ASCII letters; letters/digits; or letters/digits/hyphens/underscores |
| [`ASCII`](#text-constraints) | ASCII code points only |

For an application plan, `plan.OneOf("free", "pro")` inside its string callback
accepts only those supplied values. There is no separate enum constructor.
`NotOneOf("admin", "root")` excludes reserved names. Negative length bounds
record [definition errors](errors.md#definition-errors), not input violations.

## String Formats

A callback URL should use a scheme your application intends to handle:

```go
callback := validate.String(func(address *validate.StringBuilder[string]) {
	address.Required()
	address.URL("https")
})
err := callback.Validate(ctx, "https://example.com/events")
```

This passes. `URL` requires an absolute URL with a host. Its default schemes are
HTTP and HTTPS, compared case-insensitively. An explicit list replaces those
defaults and is copied when the setting is declared. An empty list selects the
defaults; an empty string entry never permits relative or scheme-relative URLs.
Syntax validation does not authorize a network destination: apply your own
destination policy before fetching an untrusted URL.

| Rule | What it checks |
| --- | --- |
| [`Email`](#string-formats) | Email syntax, not mailbox existence |
| [`URL`](#string-formats) | Absolute URL, host, and allowed scheme |
| [`UUID`](#string-formats) | UUID text |
| [`Domain`](#string-formats) | Domain name, not an entire URL |
| [`IP`](#string-formats) | IPv4 or IPv6 text |
| [`Duration`](#duration-values) | Go duration text accepted by `time.ParseDuration` |

These checks do not trim whitespace. Add `Trim` only when accepting and returning
cleaned text is intentional.

## Numeric Bounds

Use integral units when a quantity is naturally a count. A batch size can be
bounded without floating-point calculations:

```go
batch := validate.Int(func(count *validate.IntBuilder[int]) {
	count.Required()
	count.Between(1, 100)
	count.MultipleOf(5)
})
err := batch.Validate(ctx, 25)
```

This passes. Arguments use the schema's exact numeric type. `Int`, `Uint`, and
`Float` share these assertions:

| Rule | Supplied number must be |
| --- | --- |
| [`Min`, `Gte`](#numeric-bounds) | Greater than or equal to the bound |
| [`Max`, `Lte`](#numeric-bounds) | Less than or equal to the bound |
| [`Between`](#numeric-bounds) | Inside both inclusive bounds |
| [`Gt`, `Lt`](#numeric-bounds) | Strictly greater or strictly less |
| [`Positive`, `NonNegative`](#numeric-bounds) | Greater than zero or greater than/equal to zero |
| [`MultipleOf`](#numeric-bounds) | An exact multiple of a positive factor |
| [`OneOf`, `NotOneOf`](#numeric-bounds) | In or outside the listed typed values |

`MultipleOf(0)` panics immediately. Negative factors and non-finite floating
bounds or factors record sticky definition errors. Floating `MultipleOf` compares
exact decimal values from each Go float's shortest JSON spelling: `0.3` passes a
factor of `0.1`, without epsilon tolerance. Float32 retains its own encoding
precision. This cannot recover precision lost during an earlier calculation.

## Field Comparisons

To check confirmation input, compare the field with another exact-type field in
the nearest enclosing struct:

```go
type Passwords struct {
	Password string `json:"password"`
	Confirm  string `json:"confirm"`
}
passwords := validate.Struct(func(object *validate.StructBuilder[Passwords]) {
	object.Field("Confirm", func(text *validate.StringBuilder[string]) {
		text.Required()
		text.EqField("Password")
	})
})
```

A supplied confirmation must match `Password`. `NeField` checks inequality;
numeric builders additionally have `GtField`, `GteField`, `LtField`, and
`LteField`. Paths use Go names, not JSON names, and an unreachable target fails
the comparison. See [Field Paths](conditions.md#field-paths) for nested scope.

## Boolean Intent

Agreement needs an affirmative value, not merely a non-nil field:

```go
terms := validate.Bool(func(consent *validate.BoolBuilder[bool]) {
	consent.Accepted()
})
err := terms.Validate(ctx, false)
```

This fails: `Accepted` requires true even though false normally counts as absent.
`Declined` rejects true. To require an explicitly supplied false, require a
pointer and apply `Declined` to its pointee.

## Time Windows

`Time` validates `time.Time`. `After` and `Before` are strict; `Between` includes
both bounds. `AfterField` and `BeforeField` compare other exact-type time fields.
For a future appointment, pin the clock in tests rather than sleeping:

```go
now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
fixed := validate.NewClockContext(ctx, func() time.Time {
	return now
})
appointment := validate.Time(func(instant *validate.TimeBuilder) {
	instant.Required()
	instant.Future()
})
err := appointment.Validate(fixed, now.Add(time.Hour))
```

With the standard `time` package imported, this passes. Equality with the clock
fails both `Future` and `Past`. A zero instant is absent in every location.

## Duration Values

If your field remains a string, use `String` with `Duration()`. If your decoded
type needs a duration value that also passes through JSON, use
`validate.Duration` and `Text[validate.Duration]()`:

```go
var timeout validate.Duration
err := timeout.UnmarshalText([]byte("250ms"))
duration := timeout.Std()
text, err := timeout.MarshalText()
```

On success, `duration` is a standard `time.Duration` of 250 milliseconds and
`text` is `[]byte("250ms")`. Failed unmarshaling leaves the receiver unchanged.
Raw `time.Duration` has no representation in `encoding/json/v2`.
The wrapper's `JSONSchemaFormat()` returns `"go-duration"`; standard JSON Schema
`"duration"` uses a different grammar. `Text[T]` requires text marshaling and
unmarshaling on `*T`; it and `Any` offer common settings and custom checks,
not string-format methods.

## File Uploads

After multipart decoding, constrain the `*multipart.FileHeader` itself:

```go
avatar := validate.File(func(upload *validate.FileBuilder) {
	upload.Required()
	upload.Max(2 << 20)
	upload.Extensions("png")
	upload.Types("image/png")
})
```

Nil fails `Required`. `Min` and `Max` use the multipart parser's size in bytes.
`Extensions` ignores case and an optional leading dot. `Types` opens the file,
checks its first 512 bytes using `http.DetectContentType`, and accepts exact
types or wildcards with media-type parameters ignored. Open/read failures are
operational errors, not invalid-input violations.

Content detection is coarse: Office files detect as ZIP, and SVG can contain
scripts and is not treated as an image. There is no `Image` shorthand. Keep
antivirus checks, safe storage names, and
[multipart body limits](../http-fundamentals/requests.md#parse-uploads); these rules
do not replace them.

For every scalar method, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
