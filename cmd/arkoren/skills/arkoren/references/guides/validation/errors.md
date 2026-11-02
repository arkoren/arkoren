---
title: Validation Errors
description: Read structured violations, translate their messages, and distinguish input failures from invalid definitions.
order: 80
summary:
  - Classify input violations separately from operational and definition errors.
  - Read stable codes and JSON Pointers instead of parsing error summaries.
  - Translate and copy diagnostics with explicit ownership limits.
---

# Validation Errors

A form needs field-level correction advice, while a failed lookup needs
operational handling. Validation keeps those outcomes separate. Rule failures
return `validate.Errors`, a map from JSON Pointers to violation records; custom
checks can instead return their operational error unchanged.

Examples import `arkoren.dev/foundation/validate` and use an existing context
named `ctx`. Standard `errors` and `fmt` imports are named where used.

## Classify Failures

After `schema.Validate` or `schema.Parse`, use the standard `errors` package to
extract violations rather than treating every error as invalid input:

```go
var violations validate.Errors
if errors.As(err, &violations) {
	// Use violations to identify input that needs correction.
}
```

Other errors include dependency failures and cancellation. Do not translate
them into field advice. Success returns nil, never an empty `validate.Errors`.
`Parse` returns the zero typed result on failure; the decoded-JSON importer
returns nil. Neither result is partially accepted input.

HTTP handlers should use the [request validation adapter](overview.md#http-input)
instead of returning raw `validate.Errors`, which has no HTTP status.
For strict byte checks, also distinguish malformed JSON and
[resource refusals](encoded-json.md#classify-failures).

## Read Violations

A `Violation` has a stable `Code`, optional `Params`, and a localized `Message`.
Match codes in application logic and show messages to people. With `fmt`
imported, iterate in deterministic path order:

```go
for pointer, failures := range violations.All() {
	for _, failure := range failures {
		fmt.Println(pointer, failure.Code, failure.Message)
	}
}
```

The root pointer is `""`; `/items/0/quantity` selects a nested value. Pointer
segments escape `/` as `~1` and `~` as `~0`. Struct paths use JSON-v2 field names,
although declarations and `At` use Go names. `All` orders digit-only segments
numerically, so `/items/2` precedes `/items/10`.

`Error()` is a deterministic English summary, not a wire format or the current
locale's message representation. Do not parse it. Pointers and parameters may
reveal private member names or application data; redact diagnostics before
exposing them beyond their intended audience.

## Translate Messages

Keep codes stable across languages. Configure messages on a context rather
than changing global state:

```go
catalog := validate.Catalog{
	Default: "en",
	Locales: map[string]validate.Messages{
		"en": {"username.reserved": "This username is reserved."},
		"es": {"username.reserved": "Este nombre de usuario está reservado."},
	},
}
ctx = validate.NewCatalogContext(ctx, catalog)
ctx = validate.NewLocaleContext(ctx, "es")
```

Checks using this context render Spanish messages for `username.reserved`
without changing its code. Templates interpolate `{param}` placeholders from
the violation parameters. To change a built-in message, start from the mutable
copy returned by `validate.English()` and edit the relevant code.

Lookup tries the selected locale, its base language, the catalog default and
its base language, built-in English, then the code. `LocaleFromContext(ctx)`
returns the selected locale and whether one exists; it does not negotiate a
language. Keep catalog maps immutable while requests read them. HTTP
[locale middleware](../http-capabilities/localization.md#select-languages) supplies per-request context.

For existing diagnostics, `localized := violations.Localize(ctx)` returns a
copy with rerendered messages. Codes, locations, and parameter values remain
the same; an unknown code retains its existing message. `Describe` is unrelated:
it annotates JSON Schema and never overrides a violation message.

## Add Diagnostics

If application code already knows a field-level failure, initialize the map
before appending it:

```go
failures := make(validate.Errors)
failures.Add("/email", validate.Violation{
	Code:    "email.taken",
	Message: "This email address is already registered.",
})
```

`Add` takes a JSON Pointer; [custom checks](custom-rules.md#object-invariants)
use `At` with a Go field name. `Add` does not copy supplied parameters, and
`All` yields borrowed slices and parameters. Neither creates a snapshot.

## Diagnostic Ownership

`Fail` and `At` capture their parameter containers. The engine detaches supported
containers again before rendering/publishing violations, including built-in and
imported enum/const parameters. `Localize` also copies them, even when an unknown
code keeps its existing message. Mutating a copied parameter container does not
rewrite a reusable failure, schema, or another result.

Copying traverses acyclic slices, arrays, and maps with string-kind keys,
including nested containers reached through interfaces. Named types and
nil-versus-empty distinctions are preserved. Cycles along these copied
containers are unsupported and are not detected.

This is not JSON conversion or a general object deep copy. Struct fields,
pointers, and non-string-keyed maps are not traversed; opaque native objects,
functions, and captures remain application-owned. Copying invokes no custom
marshaling methods. Keep shared opaque data immutable or synchronized, and do
not mutate borrowed diagnostics concurrently with readers. Parameters need to
be JSON-serializable if you encode them; the helpers do not enforce that.

## Definition Errors

A malformed schema is a programming error, not user input needing correction.
`ErrInvalidSchema` identifies these failures, with different reporting stages:

| Operation | Invalid definition outcome |
| --- | --- |
| `Struct` construction | Aggregates recorded problems after callbacks, then panics |
| Standalone explicit `Parse` or `Validate` | Panics during compilation |
| `JSONSchema` export | Returns nil bytes and an error |
| `Infer` inspection, execution, or export | Returns its inference error rather than panicking |
| JSON Schema import | Returns a construction error |

These failures wrap `ErrInvalidSchema`. Recorded problems include missing or
duplicate fields, wrong types or paths, `Use(nil)`, negative string lengths or
multiple factors, and non-finite floating bounds/factors. They survive later
valid settings, reuse, and publication. Aggregated struct diagnostics sort by
Go field name, retaining declaration order for ties.

Some failures are immediate: `MultipleOf(0)` panics at the setter before later
callbacks run. Supplied nil functions and application callback panics also
propagate; they are not collected as ordinary registration problems.

Construct composite schemas with constructors. A zero `StructSchema[T]` remains
invalid even through `Ptr`; use `Struct[T]()` for a valid empty registration.
Constructed slices/maps may deliberately omit child rules. Struct construction
checks registered defaults against their rules, whereas export does not execute
those checks or prove default validity. Check export errors before publishing
documents; JSON encoding failures preserve the encoder's cause instead of
pretending every failure is a bad rule.

For exact error and localization contracts, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
