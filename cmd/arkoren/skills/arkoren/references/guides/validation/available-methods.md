---
title: Validation Rules and Normalizers
nav: Scalar Rules
description: Common string, number, boolean, time, and file rules, with a complete rule index by subject.
order: 20
---

# Validation Rules and Normalizers

## Introduction

Rules reject values without changing them. Configure rules inside a typed
builder callback; call `Parse` or `Validate` on the published schema.
Start with the [overview](overview.md) if you have not built a schema yet.

This page covers scalar rules. [Normalizers](normalizers.md) change values,
[collection schemas](collections.md) check slices and maps, and
[conditions](conditions.md) select rules from other fields. These separate
references keep each subject close to its examples.

Examples use a `context.Context` named `ctx`. Unless stated otherwise, absent values skip
ordinary rules. Add `Required` to reject absence.

## Requiring a value

`Required` rejects a zero scalar or nil container or pointer. To require a name:

```go
name := validate.String(func(value *validate.StringBuilder[string]) {
	value.Required()
})
err := name.Validate(ctx, "")
```

This fails with `required`. `Required` does not merely check whether a JSON
key exists. For slices and maps, a non-nil empty container is present; use
`Min(1)` to reject it.

`Default(value)` fills absence instead. It is available on explicit builders
except `FileBuilder`. Pointer defaults take a pointee value. Slice and map
defaults copy their outer containers, not arbitrary nested values.
Combining `Required` and `Default` is invalid.

## Builder operations

These tables list role-specific settings. Every role also has the
[common builder operations](overview.md#builder-operations), including
`Use`, `Required`, `Check`, `Bail`, `Normalize`, `Describe`, field comparisons,
and conditional settings. All roles here except `FileBuilder` have `Default`.

### StringBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Min(n)`, `Max(n)`, `Len(n)`, `NotBlank()` | Rune counts or non-whitespace text | [Lengths](#string-lengths) |
| `OneOf(values...)`, `NotOneOf(values...)` | Allow or reject listed S values | [Membership](#allowed-values) |
| `Regex(pattern)`, `Prefix(text)`, `Suffix(text)`, `Contains(text)` | Pattern, prefix, suffix, or substring assertions | [String rule index](#string-rule-index) |
| `Email()`, `URL(schemes...)`, `UUID()`, `Duration()`, `Domain()`, `IP()` | Validate string formats without rewriting them | [Formats](#string-formats) |
| `Lowercase()`, `Uppercase()`, `Alpha()`, `AlphaNumeric()`, `AlphaDash()`, `ASCII()` | Case and character-class assertions | [String rule index](#string-rule-index) |
| `Trim()`, `ToLower()`, `ToUpper()`, `TrimPrefix(prefix)`, `TrimSuffix(suffix)`, `Truncate(n)` | Transform the prepared string | [String normalizers](normalizers.md#string-normalizers) |

### IntBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Min`, `Max`, `Between`, `Gt`, `Gte`, `Lt`, `Lte`, `Positive`, `NonNegative`, `MultipleOf` | Signed-number constraints; arguments have type N | [Numeric rule index](#numeric-rule-index) |
| `OneOf(values...)`, `NotOneOf(values...)` | Allow or reject listed N values | [Membership](#allowed-values) |
| `GtField`, `GteField`, `LtField`, `LteField` | Compare with an exact-type field | [Field comparisons](#comparing-fields) |
| `Abs()`, `Clamp(low, high)` | Transform signed values before checking | [Numeric normalizers](normalizers.md#numeric-normalizers) |

### UintBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Min`, `Max`, `Between`, `Gt`, `Gte`, `Lt`, `Lte`, `Positive`, `NonNegative`, `MultipleOf` | Unsigned-number constraints; arguments have type N | [Numeric rule index](#numeric-rule-index) |
| `OneOf(values...)`, `NotOneOf(values...)` | Allow or reject listed N values | [Membership](#allowed-values) |
| `GtField`, `GteField`, `LtField`, `LteField` | Compare with an exact-type field | [Field comparisons](#comparing-fields) |
| `Clamp(low, high)` | Clamp to inclusive bounds; no unsigned `Abs` | [Numeric normalizers](normalizers.md#numeric-normalizers) |

### FloatBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Min`, `Max`, `Between`, `Gt`, `Gte`, `Lt`, `Lte`, `Positive`, `NonNegative`, `MultipleOf` | Floating-point constraints; arguments have type N | [Numeric rule index](#numeric-rule-index) |
| `OneOf(values...)`, `NotOneOf(values...)` | Allow or reject listed N values | [Membership](#allowed-values) |
| `GtField`, `GteField`, `LtField`, `LteField` | Compare with an exact-type field | [Field comparisons](#comparing-fields) |
| `Abs()`, `Clamp(low, high)`, `Round(places)` | Transform, clamp, or round the value | [Numeric normalizers](normalizers.md#numeric-normalizers) |

### BoolBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Accepted()`, `Declined()` | Require true or reject true | [Boolean rules](#boolean-rules) |

### TimeBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `After(instant)`, `Before(instant)`, `Between(start, end)` | Compare instants; `Between` includes its bounds | [Time rules](#time-rules) |
| `Future()`, `Past()` | Compare with the context's clock | [Time rules](#time-rules) |
| `AfterField(path)`, `BeforeField(path)` | Compare with another time field | [Time rules](#time-rules) |
| `UTC()` | Change location, not instant | [Time normalizers](normalizers.md#time-normalizers) |

### FileBuilder

| Operation | Purpose and scope | Details |
| --- | --- | --- |
| `Min(bytes)`, `Max(bytes)` | Compare multipart parser file size | [File rules](#file-rules) |
| `Extensions(exts...)`, `Types(types...)` | Check filename extensions and detected content types | [File rules](#file-rules) |

`TextBuilder[T]` and `AnyBuilder` have the common operations, not these
role-specific format methods; see [their operation table](schemas-and-helpers.md#text-and-any-builders).

## String lengths

`Min`, `Max`, and `Len` count Unicode runes, not bytes. To require a short code:

```go
code := validate.String(func(value *validate.StringBuilder[string]) {
	value.Required()
	value.Len(6)
})
err := code.Validate(ctx, "ABC123")
```

This passes. `Min(3)` accepts three or more runes; `Max(80)` accepts up to 80.
All three length bounds must be nonnegative; zero is valid. A negative bound
records a sticky [definition error](schemas-and-helpers.md#definition-errors),
not a violation against the next input. A later valid bound does not clear it.
`NotBlank` rejects whitespace-only text without trimming it. Use
[`Trim`](normalizers.md#string-normalizers) when the parsed result should change.

## Allowed values

`OneOf` permits only the listed values; `NotOneOf` rejects the listed values.
String and numeric builders support both. To restrict a plan:

```go
plan := validate.String(func(value *validate.StringBuilder[string]) {
	value.Required()
	value.OneOf("free", "pro")
})
err := plan.Validate(ctx, "trial")
```

This fails `one_of`. Without `Required`, an empty string remains optional.
Use this pattern instead of a separate enum constructor.

## String formats

`Email` checks email syntax; it does not prove that a mailbox exists. To accept
surrounding whitespace and return a clean address:

```go
email := validate.String(func(address *validate.StringBuilder[string]) {
	address.Trim()
	address.Email()
})
parsed, err := email.Parse(ctx, " ada@example.com ")
```

On success, `parsed` is `"ada@example.com"`. Format rules themselves stay
strict; they do not trim input.

`URL()` requires an absolute URL with a host and an `http` or `https` scheme,
compared case-insensitively. To require HTTPS:

```go
website := validate.String(func(value *validate.StringBuilder[string]) {
	value.URL("https")
})
err := website.Validate(ctx, "https://example.com")
```

This passes. Supplying schemes replaces the default set, rather than adding
to it. The rule snapshots the scheme slice when `URL` is called: editing the
slice in a later construction callback or after publication cannot change it.
`javascript:` and `data:` are not accepted by the default rule.

An empty argument list, including an expanded nil or empty slice, selects the
default schemes. An explicit empty string entry matches no absolute URL; it
does not allow relative paths or scheme-relative references such as
`//example.com/path`. `URL("", "https")` still allows only absolute HTTPS URLs
with a host. Empty input remains absent; add `Required` to reject it.

### String rule index

The calls below are settings on `*StringBuilder[S]`. Every call checks rather
than rewrites the string. Use the linked normalizers to change case or text.

| Rule | Small call | Result for supplied input |
| --- | --- | --- |
| [`Min`](#string-lengths) | `value.Min(3)` | At least three runes |
| [`Max`](#string-lengths) | `value.Max(80)` | At most 80 runes |
| [`Len`](#string-lengths) | `value.Len(6)` | Exactly six runes |
| [`NotBlank`](#string-lengths) | `value.NotBlank()` | Whitespace-only input fails |
| [`Regex`](available-methods.md) | `value.Regex(regexp.MustCompile("^[a-z]+$"))` | Go regexp must match; use anchors for a whole-string match |
| [`Prefix`](available-methods.md) | `value.Prefix("acct_")` | Must start with `acct_` |
| [`Suffix`](available-methods.md) | `value.Suffix(".csv")` | Must end with `.csv` |
| [`Contains`](available-methods.md) | `value.Contains("@")` | Must contain the substring |
| [`Email`](#string-formats) | `value.Email()` | Email syntax |
| [`URL`](#string-formats) | `value.URL("https")` | Allowed absolute URL with a host |
| [`UUID`](available-methods.md) | `value.UUID()` | UUID text |
| [`Duration`](schemas-and-helpers.md#duration-text) | `value.Duration()` | Text accepted by `time.ParseDuration` |
| [`Domain`](#string-formats) | `value.Domain()` | Domain-name syntax |
| [`IP`](available-methods.md) | `value.IP()` | IPv4 or IPv6 text |
| [`Lowercase`](normalizers.md#string-normalizers) | `value.Lowercase()` | Reject uppercase letters; does not lowercase |
| [`Uppercase`](normalizers.md#string-normalizers) | `value.Uppercase()` | Reject lowercase letters; does not uppercase |
| [`Alpha`](available-methods.md) | `value.Alpha()` | ASCII letters only |
| [`AlphaNumeric`](available-methods.md) | `value.AlphaNumeric()` | ASCII letters and digits |
| [`AlphaDash`](available-methods.md) | `value.AlphaDash()` | ASCII letters, digits, hyphens, underscores |
| [`ASCII`](available-methods.md) | `value.ASCII()` | ASCII code points only |
| [`OneOf`](#allowed-values) | `value.OneOf("free", "pro")` | Only listed values |
| [`NotOneOf`](#allowed-values) | `value.NotOneOf("admin", "root")` | None of the listed values |

To check a domain without accepting an entire URL:

```go
domain := validate.String(func(value *validate.StringBuilder[string]) {
	value.Domain()
})
err := domain.Validate(ctx, "example.com")
```

This passes; `"https://example.com"` is not a domain name.

## Numeric bounds

`Int`, `Uint`, and `Float` builders share numeric rules. Bounds have the same
Go type as the value. To constrain a percentage:

```go
percentage := validate.Int(func(value *validate.IntBuilder[int]) {
	value.Between(1, 100)
})
err := percentage.Validate(ctx, 100)
```

This passes: both bounds are inclusive. Zero is absent and skips `Between`.
Use `Required` to reject zero, or a [pointer](structs-and-reuse.md#explicit-zero-values)
when zero must count as supplied.

### Numeric rule index

| Rule | Small call | Supplied value must be |
| --- | --- | --- |
| [`Min`](#numeric-bounds) | `value.Min(1)` | Greater than or equal to 1 |
| [`Max`](#numeric-bounds) | `value.Max(100)` | Less than or equal to 100 |
| [`Between`](#numeric-bounds) | `value.Between(1, 100)` | Within both inclusive bounds |
| [`Gt`](#numeric-bounds) | `value.Gt(0)` | Strictly greater than 0 |
| [`Gte`](#numeric-bounds) | `value.Gte(18)` | Greater than or equal to 18 |
| [`Lt`](#numeric-bounds) | `value.Lt(100)` | Strictly less than 100 |
| [`Lte`](#numeric-bounds) | `value.Lte(100)` | Less than or equal to 100 |
| [`Positive`](#numeric-bounds) | `value.Positive()` | Strictly positive |
| [`NonNegative`](#numeric-bounds) | `value.NonNegative()` | Zero or positive |
| [`MultipleOf`](#numeric-bounds) | `value.MultipleOf(5)` | An exact multiple of 5 |
| [`OneOf`](#allowed-values) | `value.OneOf(10, 25, 50)` | One of the listed numbers |
| [`NotOneOf`](#allowed-values) | `value.NotOneOf(13)` | Not one of the listed numbers |

`MultipleOf` requires a positive factor; the input itself may be negative.
Zero factors panic immediately during construction. Negative factors record
sticky [definition errors](schemas-and-helpers.md#definition-errors). Floating
bounds for `Min`, `Max`, `Between`, `Gt`, `Gte`, `Lt`, and `Lte`, and floating
`MultipleOf` factors, must also be finite; NaN and infinities are recorded errors.

Floating-point `MultipleOf` compares the exact decimal values of the value's and
factor's shortest JSON spellings. A value of `0.3` passes `MultipleOf(0.1)`;
a nearby decimal nonmultiple fails without an epsilon tolerance. Float32 values
retain their own encoding precision rather than being widened first. This cannot
recover precision already lost in an earlier calculation. For quantities defined
in integral units, use an integer schema to avoid that earlier rounding.

## Comparing fields

`EqField` and `NeField` compare against a field of the same type. Numeric
builders also provide `GtField`, `GteField`, `LtField`, and `LteField`.
To confirm a password:

```go
type Passwords struct {
	Password string `json:"password"`
	Confirm  string `json:"confirm"`
}

passwords := validate.Struct(func(object *validate.StructBuilder[Passwords]) {
	object.Field("Confirm", func(value *validate.StringBuilder[string]) {
		value.Required()
		value.EqField("Password")
	})
})
```

`Confirm` must be supplied and equal `Password`. Use Go names, not JSON names.
Field paths are relative to the [nearest struct](conditions.md#nested-field-paths).

| Comparison | Meaning |
| --- | --- |
| `EqField("Other")` | Equal |
| `NeField("Other")` | Unequal |
| `GtField("Other")` | Strictly greater |
| `GteField("Other")` | Greater than or equal |
| `LtField("Other")` | Strictly less |
| `LteField("Other")` | Less than or equal |

## Boolean rules

Use `Accepted` when a user must agree:

```go
terms := validate.Bool(func(value *validate.BoolBuilder[bool]) {
	value.Accepted()
})
err := terms.Validate(ctx, false)
```

This fails; `Accepted` requires true even though false ordinarily counts as
absent. `Declined` rejects true. To require an explicitly supplied false,
put `Required` on a pointer and `Declined` on its pointee.

## Time rules

`After` and `Before` compare strict instants; `Between` includes both bounds.
`Future` and `Past` compare with the context's clock. To require a future time:

```go
appointment := validate.Time(func(value *validate.TimeBuilder) {
	value.Required()
	value.Future()
})
```

A zero `time.Time` is absent regardless of location. Equality with the current
instant fails `Future` and `Past`. Use
[`NewClockContext`](schemas-and-helpers.md#clock-helpers) for deterministic tests.

| Rule | Comparison |
| --- | --- |
| `After(instant)` | Strictly after a `time.Time` |
| `Before(instant)` | Strictly before a `time.Time` |
| `Between(start, end)` | Inclusive range |
| `Future()` | Strictly after the context's current time |
| `Past()` | Strictly before the context's current time |
| `AfterField("Start")` | Strictly after another time field |
| `BeforeField("End")` | Strictly before another time field |

## File rules

`FileBuilder` checks `*multipart.FileHeader`. To require a PNG no larger than
two mebibytes:

```go
avatar := validate.File(func(upload *validate.FileBuilder) {
	upload.Required()
	upload.Max(2 << 20)
	upload.Extensions("png")
	upload.Types("image/png")
})
```

`Min` and `Max` take bytes. `Extensions` compares the client filename's
extension, ignoring case and an optional leading dot. `Types` detects content
from the first 512 bytes; it accepts exact types or wildcards such as `image/*`.
It does not trust the supplied content-type header. Read failures return
operational errors, not violations. See [file limits](advanced.md#validating-files).

## Stopping after a failure

An expensive custom lookup is wasted when the input already has malformed
email syntax. Set `Bail` to stop this value's checks after its first violation;
add any custom checks after the cheap syntax rules:

```go
email := validate.String(func(value *validate.StringBuilder[string]) {
	value.Required()
	value.Email()
	value.Bail()
})
```

Rules otherwise collect all failures for the value in declaration order.
`Bail` is a setting, not a rule whose position changes its effect.

## Complete method map

Use these subject references for the remaining builder methods:

| Subject | Methods |
| --- | --- |
| Schema construction | [`Use`](structs-and-reuse.md#reusing-a-schema), [`Field`](structs-and-reuse.md#selecting-fields), [`Keys`](collections.md#checking-map-keys-and-values), [`Values`](collections.md#checking-each-slice-element), [`Value`](structs-and-reuse.md#explicit-zero-values), [`Parse`](schemas-and-helpers.md#parse), [`Validate`](schemas-and-helpers.md#validate), [`JSONSchema`](schemas-and-helpers.md#jsonschema-and-describe), [`Describe`](schemas-and-helpers.md#jsonschema-and-describe) |
| Common presence and control | [`Required`](overview.md#required-and-optional-values), [`Default`](overview.md#defaulting-a-value), [`Bail`](#stopping-after-a-failure) |
| Conditions | [`RequiredIf`](conditions.md#requiring-a-field-conditionally), [`RequiredUnless`](conditions.md#requiring-a-field-conditionally), [`RequiredWith`](conditions.md#checking-presence-dependencies), [`RequiredWithAll`](conditions.md#checking-presence-dependencies), [`RequiredWithout`](conditions.md#checking-presence-dependencies), [`ProhibitedIf`](conditions.md#prohibiting-and-excluding-fields), [`ProhibitedUnless`](conditions.md#prohibiting-and-excluding-fields), [`ExcludeIf`](conditions.md#prohibiting-and-excluding-fields), [`When`](conditions.md#adding-conditional-settings) |
| Condition expressions | [`Eq`](conditions.md#condition-method-index), [`NotEq`](conditions.md#condition-method-index), [`In`](conditions.md#condition-method-index), [`NotIn`](conditions.md#condition-method-index), [`Present`](conditions.md#nested-field-paths), [`Absent`](conditions.md#nested-field-paths), [`And`](conditions.md#combining-conditions), [`Or`](conditions.md#combining-conditions), [`Not`](conditions.md#combining-conditions), [`If`](conditions.md#typed-object-predicates) |
| Collections | [`Min`](collections.md#checking-each-slice-element), [`Max`](collections.md#checking-each-slice-element), [`Len`](collections.md#checking-each-slice-element), [`Has`](collections.md#requiring-a-map-key), [`HasAny`](collections.md#requiring-a-map-key), [`HasAll`](collections.md#requiring-a-map-key), [`Contains`](collections.md#requiring-a-matching-entry), [`Every`](collections.md#requiring-every-entry-to-match), [`Unique`](collections.md#rejecting-duplicate-values), [`UniqueBy`](collections.md#rejecting-duplicate-identities), [`CountWhere`](collections.md#counting-matching-entries) |
| String normalizers | [`Normalize`](normalizers.md#custom-normalization), [`Trim`](normalizers.md#string-normalizers), [`ToLower`](normalizers.md#string-normalizers), [`ToUpper`](normalizers.md#string-normalizers), [`TrimPrefix`](normalizers.md#string-normalizers), [`TrimSuffix`](normalizers.md#string-normalizers), [`Truncate`](normalizers.md#string-normalizers) |
| Other normalizers | [`Abs`](normalizers.md#numeric-normalizers), [`Clamp`](normalizers.md#numeric-normalizers), [`Round`](normalizers.md#numeric-normalizers), [`UTC`](normalizers.md#time-normalizers), [`Sort`](normalizers.md#slice-normalizers), [`Filter`](normalizers.md#slice-normalizers), [`Reject`](normalizers.md#slice-normalizers), [`RejectZero`](normalizers.md#slice-normalizers), [`Dedupe`](normalizers.md#slice-normalizers) |
| Custom checks and helpers | [`Check`](advanced.md#custom-checks), [`Fail`](schemas-and-helpers.md#errors-and-custom-checks), [`At`](advanced.md#object-wide-checks), [`Error`](schemas-and-helpers.md#errors-and-custom-checks), [`All`](schemas-and-helpers.md#errors-and-custom-checks), [`Add`](schemas-and-helpers.md#errors-and-custom-checks), [`Localize`](schemas-and-helpers.md#errors-and-custom-checks) |
| Locale, clock, and text helpers | [`English`](schemas-and-helpers.md#locale-helpers), [`NewCatalogContext`](schemas-and-helpers.md#locale-helpers), [`NewLocaleContext`](schemas-and-helpers.md#locale-helpers), [`LocaleFromContext`](schemas-and-helpers.md#locale-helpers), [`NewClockContext`](schemas-and-helpers.md#clock-helpers), [`MarshalText`](schemas-and-helpers.md#duration-text), [`UnmarshalText`](schemas-and-helpers.md#duration-text), [`JSONSchemaFormat`](schemas-and-helpers.md#duration-text), [`Std`](schemas-and-helpers.md#duration-text) |

For exact signatures and error contracts, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
