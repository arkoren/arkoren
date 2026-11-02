---
title: Custom Rules
description: Add application-specific value and object checks while preserving operational errors.
order: 70
summary:
  - Return stable violation codes from typed custom checks.
  - Keep operational failures distinct and avoid costly checks after invalid input.
  - Check object invariants at the correct preparation stage.
---

# Custom Rules

A username can satisfy every format rule and still be reserved by your
application. Add that policy with `Check`, whose callback receives the prepared
value and caller's context. Use built-in rules when they already express the
constraint; custom checks are for application logic, not a replacement schema
implementation.

Examples import `arkoren.dev/foundation/validate` and the standard `context`
package. The [overview](overview.md) covers schema execution.

## Check a Value

Return nil to accept a value or `validate.Fail` to report a violation:

```go
username := validate.String(func(name *validate.StringBuilder[string]) {
	name.Trim()
	name.Required()
	name.Check(func(_ context.Context, value string) error {
		if value == "admin" {
			return validate.Fail("username.reserved", nil)
		}
		return nil
	})
})
```

`" admin "` fails at the root pointer with code `username.reserved` after
trimming. Add that code to your [message catalog](errors.md#translate-messages).
Optional absent scalars skip ordinary checks; `Required` above handles absence.

`Fail` accepts a parameter map for message placeholders. Parameters need to be
JSON-serializable if you encode diagnostics; the helper does not encode or
validate them. Do not include secrets merely to improve an error message.

Every other returned error is operational: validation stops and returns it
unchanged. If a storage lookup fails, return that failure rather than reporting
that the username is unavailable. A failed dependency does not establish a fact
about user input. Pass the callback's context to storage and network calls.

## Stop Failed Checks

Put cheap syntax rules before an expensive custom check and configure `Bail`
when a malformed value must not reach that check. Inside the same typed builder,
`name.Bail()` stops this value after its first violation.

Rules otherwise collect violations in declaration order. `Bail` is a setting,
not a step whose position changes its effect; it does not stop unrelated sibling
fields. An operational error stops the whole validation regardless of `Bail`.

## Object Invariants

For a relationship involving multiple fields, a struct-level check can select
the field that needs correction. Define a booking interval:

```go
type Interval struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
```

Attach a failed ordering check to the Go field name `End`:

```go
interval := validate.Struct(func(object *validate.StructBuilder[Interval]) {
	object.Check(func(_ context.Context, value Interval) error {
		if value.End < value.Start {
			return validate.At("End", "interval.order", nil)
		}
		return nil
	})
})
```

The diagnostic uses `/end`. `At` takes a Go field name, not a JSON Pointer;
an unknown name produces an error wrapping `ErrInvalidSchema` during execution.
For this simple comparison, `GteField("Start")` on an end-field builder is the
smaller [built-in alternative](available-methods.md#field-comparisons). Use an
object check when the invariant needs broader application logic.

An object check sees unconditional normalization and defaults, but runs before
its child field rules. It does not see their later `When` or `ExcludeIf` effects
and is not a final post-validation hook. Keep the invariant independent of those
effects, and do not assume a child has already passed its own constraints.

## Context and Ownership

Checks run for every applicable input, unlike construction callbacks. Published
schemas support concurrent calls, so checks and captured dependencies must be
safe for concurrent use. Do not mutate the input or objects reachable from it.
Validation is not authorization, and checking a value does not make a later
database mutation atomic with that check.

Cancellation is observed between normalizers, rules, and child callbacks.
Pass the context through operations that can block; the engine cannot interrupt
an arbitrary application callback. Cancellation discards partial diagnostics
and parsed results. An operational error that already aborted execution retains
precedence over cancellation.

`Fail` and `At` errors can be reused. They snapshot supported parameter
containers, and the engine detaches them again when publishing violations.
Pointers and opaque objects remain shared; follow the full
[diagnostic ownership rules](errors.md#diagnostic-ownership) rather than assuming
a general deep copy.

For all custom-check contracts, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
