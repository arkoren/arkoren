---
title: Encoded JSON Validation
nav: Encoded JSON
description: Check an exact JSON document without defaults, normalization, or floating-point rounding.
order: 75
---

# Encoded JSON Validation

## Introduction

An emitted JSON document must match its contract as written. Filling a missing
member or rounding a number before checking it can approve a different document.
Use `validate.FromJSONSchemaStrict` to compile a schema, then check the original
bytes without transforming them.

This is separate from [application validation](schemas-and-helpers.md#fromjsonschema).
Typed schemas and `FromJSONSchema` retain their existing presence, normalization,
and default behavior. `EncodedSchema` has no `Parse` method and does not implement
the typed `Schema[T]` interface.

## Check the emitted document

Import `arkoren.dev/foundation/validate`. With an existing `context.Context`
named `ctx`, compile a contract before checking documents:

```go
check, err := validate.FromJSONSchemaStrict([]byte(`{
	"type":"object",
	"required":["count"],
	"properties":{"count":{"type":"integer","minimum":0,"default":1}}
}`))
err = check.Validate(ctx, []byte(`{"count":9007199254740993}`))
```

This document passes without changing its bytes. An omitted `count` fails
`required`; `"count":null` fails the integer constraint. Zero is a value and
passes this nonnegative bound. The default is an annotation: it never supplies
a missing member or replaces null, and it need not satisfy the schema.

Published checks own their compiled state and support concurrent use. Editing
the original schema bytes afterward cannot change the check. Validation leaves
the supplied document untouched; a zero `EncodedSchema` is invalid.

## Keep numbers exact

Numeric bounds, integer checks, `multipleOf`, and nested `enum`, `const`, and
`uniqueItems` compare exact decimal values. A maximum of `9007199254740992`
rejects `9007199254740993`; a fractional token does not become an integer through
rounding. Equivalent numeric spellings, such as `1e3` and `1000`, compare equally.

Numbers outside `float64` range, such as `1e1000`, work within the resource
limits below. This cannot recover precision already lost in a Go `float64`
before encoding. Use an exact application representation or retain raw numeric
tokens when that distinction matters.

## Choose a supported contract

The compiler supports self-contained draft-07 and 2020-12 schemas, including
boolean schemas, type unions and null, compositions, conditional branches,
object/array constraints, and local references and anchors. Omitted `$schema`
selects 2020-12. Draft-07 `$ref` ignores assertion siblings; 2020-12 checks them.
Local references never fetch a URL or read a file. Recursive schemas that
descend through the document are supported; unproductive cycles fail a resource
check instead of recursing indefinitely.

Unknown assertion keywords, unsupported type names or dialects, malformed
keyword values, and unresolved references fail compilation with
`ErrInvalidSchema`. Recognized `x-*` extensions are annotations. Patterns use
Go's RE2 syntax, not the complete ECMA-262 regex dialect. Consult
[FromJSONSchemaStrict](https://pkg.go.dev/arkoren.dev/foundation/validate#FromJSONSchemaStrict)
for the complete vocabulary and dialect restrictions.

Formats, defaults, and content keywords remain annotations, not format or
decoded-content assertions. No application callbacks, normalizers, or custom
checks run. If your application needs those rules, run its typed
validation separately; a wire check does not replace business validation.

## Separate violations from operational failures

A mismatch returns `validate.Errors`. Malformed UTF-8/JSON, duplicate names,
and trailing documents instead wrap `ErrEncodedJSON`. Cancellation and deadlines
retain their context error identities. Resource refusals wrap `ErrEncodedLimit`,
never `validate.Errors`.

Inside a function returning `error`, use the standard `errors` package to
recognize a contract mismatch without treating every failure as invalid data:

```go
var violations validate.Errors
if errors.As(err, &violations) {
	return errors.New("output does not match its contract")
}
return err
```

Violation pointers can contain private object-member names. Redact them before
sending diagnostics to an untrusted client. Keep cancellation and resource
failures distinct from advice to correct a value.

Validation after an application mutation cannot undo it. A failed output check
does not prove rollback and is not a reason to replay the operation automatically.
The [managed MCP adapter](../mcp/tools.md#numeric-validation-and-limits) uses this
strict check and keeps operational failures separate from schema violations.

## Account for resource limits

Both schema and instance documents are limited to 8 MiB and 256 container levels.
Compilation allows 16,384 schema nodes. Decoding and evaluation each allow
1,000,000 work steps; evaluation allows 512 nested calls. Numeric coefficients
allow 4096 digits, with explicit exponents from -4096 to 4096, including for zero.

Before rational allocation, each decoded document is limited to a sum of
64 × 1024 × 1024 coefficient digits plus absolute exponents. Equality and
uniqueness structural-key encoding has a cumulative 64 MiB data budget per
validation. Tags and length prefixes are charged conservatively. This is not an
exact Go heap-allocation limit: capacity growth, copies, and runtime metadata
also consume memory.

URI/reference and traversed-pointer lengths are limited to 16 KiB, and the
compiled URI index has a 32 MiB key budget. Exceeding a limit returns an error;
the validator never rounds a value or accepts an incomplete check as a fallback.
Keep application request-size and concurrency limits as well.

Cancellation is checked between tokens, child visits, comparisons, branches,
and references. Bounded standard-library operations, including regex matching
and rational arithmetic, are not individually preemptible. Compilation has no
context parameter and performs no lifecycle I/O.

For exact contracts and limits, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
