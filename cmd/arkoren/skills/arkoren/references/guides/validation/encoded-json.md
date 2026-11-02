---
title: Encoded JSON
description: Check exact JSON document bytes without applying defaults, normalizers, or binary64 rounding.
order: 100
summary:
  - Compile a self-contained contract and validate unchanged JSON bytes.
  - Preserve exact decimal values and distinguish missing members from null.
  - Classify mismatches separately from malformed JSON, cancellation, and resource refusals.
---

# Encoded JSON

An emitted document must satisfy its contract as written. Adding a missing
member or rounding its number before checking would approve a different
document. Use `FromJSONSchemaStrict` to compile a contract, then validate the
original bytes. This is separate from [decoded application validation](json-schema.md#import-a-contract),
which permits defaults and treats null as absent.

Import `arkoren.dev/foundation/validate`. Examples use an existing
`context.Context` named `ctx`.

## Compile a Contract

Require a nonnegative integer count while retaining a default as documentation:

```go
check, err := validate.FromJSONSchemaStrict([]byte(`{
	"type": "object",
	"required": ["count"],
	"properties": {
		"count": {"type": "integer", "minimum": 0, "default": 1}
	}
}`))
```

Check the construction error before using `check`. The published `EncodedSchema`
owns its compiled state, supports concurrent use, and retains no caller bytes,
application callbacks, or mutable defaults. Editing the schema byte slice
afterward cannot change it. Its zero value is invalid.

## Check Exact Bytes

With a successfully compiled check, validate a count beyond binary64's exact
integer range:

```go
err = check.Validate(ctx, []byte(`{"count":9007199254740993}`))
```

This passes without changing the document. A missing count fails `required`;
null fails the integer type; zero is supplied and passes the minimum. Defaults
never fill missing values or replace null, and default annotations need not
satisfy their schema. There is no `Parse` method and this check does not
implement typed `Schema[T]`.

Bounds, integer assertions, `multipleOf`, and nested `enum`, `const`, and
`uniqueItems` compare exact decimal values. A maximum of `9007199254740992`
rejects `9007199254740993`; a fractional token cannot round into an integer.
Equivalent spellings such as `1e3` and `1000` compare equally. Numbers such as
`1e1000` work within the budgets below, even outside `float64` range.

No byte check can recover precision already lost before encoding a Go float.
Keep an exact application representation or raw numeric tokens when those
digits are part of the contract.

## Choose Vocabulary

The strict compiler accepts self-contained draft-07 and 2020-12 documents.
Omitted or empty `$schema` selects 2020-12. Boolean schemas, type unions and
null, compositions and conditional branches, object/array constraints, and
local references/anchors are supported. Draft-07 `$ref` ignores assertion
siblings; 2020-12 evaluates them. Mixed embedded dialects fail.

References resolve only inside the supplied document: the compiler never
fetches a URL or reads a file. References must select compiled schema locations,
not arbitrary annotation values. Recursive schemas that descend through the
instance work; unproductive cycles produce a resource failure.

Unknown keywords, unsupported dialects/types, malformed keyword values, and
unresolved references fail compilation with `ErrInvalidSchema`. `x-*` extensions
are annotations. Patterns use Go's RE2 syntax, not the complete ECMA-262 dialect.
Consult the [strict vocabulary contract](https://pkg.go.dev/arkoren.dev/foundation/validate#FromJSONSchemaStrict)
before importing contracts intended for another validator.

Formats, defaults, and content keywords annotate rather than assert formats or
decoded content. `contentSchema` is compiled for validity and references but
does not inspect decoded content. No custom check or normalizer executes. Run
typed business validation separately when the application needs it; strict wire
validation does not replace application policy.

## Classify Failures

An instance mismatch returns fail-fast `validate.Errors` with a JSON Pointer
and keyword code, without instance values or schema literals. Do not assume
the typed validator's full localized messages or exhaustive violation collection.
Other failures remain distinct:

| Failure | Identity |
| --- | --- |
| Invalid UTF-8/JSON, duplicate names, trailing documents | `ErrEncodedJSON` |
| Resource refusal | `ErrEncodedLimit`, never `validate.Errors` |
| Cancellation or deadline | The context error |
| Invalid compiled definition or zero schema | `ErrInvalidSchema` |

Using the standard `errors` package inside a function returning `error`, classify
a mismatch without swallowing operational failures:

```go
var violations validate.Errors
if errors.As(err, &violations) {
	return errors.New("output does not match its contract")
}
return err
```

Returned pointers can reveal private member names. Redact them before exposing
diagnostics to an untrusted client. Do not describe resource or cancellation
failures as instructions to correct a field.

Checking output after a mutation cannot undo that mutation. A failed check is
not proof of rollback and does not justify automatic replay. The
[managed MCP adapter](../mcp/tools.md#schema-limits) uses this
strict boundary and separates operational failures from mismatches.

## Apply Budgets

Both compilation and validation reject excess rather than round numbers or
accept incomplete work:

| Budget | Limit |
| --- | --- |
| Schema or instance document | 8 MiB each |
| Container nesting | 256 levels |
| Compiled schema nodes | 16,384 |
| Decoding work and evaluation work | 1,000,000 steps each |
| Nested evaluation calls | 512 |
| Numeric coefficient | 4,096 digits |
| Explicit decimal exponent | -4096 through 4096, including for zero |
| Sum of coefficient digits plus absolute exponents | 64 × 1024 × 1024 per decoded document |
| Equality/uniqueness structural-key encoding | 64 MiB cumulatively per validation |
| URI/reference and traversed-pointer lengths | 16 KiB |
| Compiled URI-index keys, including aliases | 32 MiB |

Numeric limits are checked before rational allocation. Structural-key budgets
include repeated comparisons and conservatively charged tags/length prefixes.
These are data/work limits, not exact heap limits: copies, capacity growth, and
runtime metadata also use memory. Keep application request-size and concurrency
limits as well.

Cancellation is checked between tokens, visits, comparisons, branches, and
references, not within bounded standard-library operations such as regex or
rational arithmetic. Compilation has no context parameter and performs no
lifecycle I/O. Compilation resource failures also retain `ErrEncodedLimit`.

For the full vocabulary, error identities, and limits, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
