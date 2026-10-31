---
title: Feature Flag Testing
nav: Testing
description: Set flags in application tests with the memory driver, and check a flag driver against the contract with the featuretest suite.
order: 20
---

# Feature Flag Testing

## Introduction

To test both sides of a feature flag, set its value in the memory driver and
run your application through the real facade. The driver implements the same
contract as every other driver and needs no external service. If you are
writing a driver, use the `featuretest` conformance suite to check it against
the contract.

## Testing your application

Import `testing`, `feature` from `arkoren.dev/core/service/feature`, and
`memoryfeature` from `arkoren.dev/core/service/feature/memoryfeature` in your
application's `_test.go` file. The conformance example below also imports
`arkoren.dev/core/service/feature/featuretest`.

Share the application's flag definition with its test. `Set` and `SetFor` take
that same definition and a value of its Go type, so test setup cannot
accidentally assign an integer to a boolean flag. This complete test checks a
per-user override through the facade; in your application, pass `flags` to the
checkout code under test instead:

```go
package checkout_test

import (
	"testing"

	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/memoryfeature"
)

var checkoutFlag = feature.NewFlag("new-checkout", false)

func TestCheckoutUsesNewFlow(t *testing.T) {
	driver := memoryfeature.New()
	if err := driver.Set(checkoutFlag, false); err != nil {
		t.Fatal(err)
	}
	if err := driver.SetFor(checkoutFlag, "user-42", true); err != nil {
		t.Fatal(err)
	}

	flags := feature.New(driver)
	on, err := flags.Value(t.Context(), checkoutFlag, feature.Subject{Key: "user-42"})
	if err != nil || !on {
		t.Fatalf("checkout = %v, %v; want true, nil", on, err)
	}
}
```

`Set` creates a flag or replaces its default value, and keeps any overrides.
`SetFor` gives one subject key its own value, and creates the flag with no
default value when it does not exist. A subject without an override gets the
default value; a flag that has only overrides returns the definition's fallback
to everyone else. The fallback is never installed as a provider default by
`SetFor`. Unsupported value types and mismatched setup values fail to compile.
Named primitive types work too. Invalid definitions return `ErrInvalidFlag`
without changing stored state; handle setup errors before running the test.

If application code uses a [namespace-derived flag](overview.md#sharing-a-flag-prefix-and-fallback),
share that definition with the test too. For example, with an existing memory
`driver` and facade `flags`:

```go
checkoutFlags := feature.NewNamespace("checkout", false)
express := checkoutFlags.Flag("express")
if err := driver.SetFor(express, "user-42", true); err != nil {
	t.Fatal(err)
}
on, err := flags.Value(t.Context(), express, feature.Subject{Key: "user-42"})
if err != nil || !on {
	t.Fatalf("express = %v, %v; want true, nil", on, err)
}
```

This installs the exact key `checkout:express` for the subject; it does not
install the namespace fallback as a stored value. `Set(express, true)` sets
the default for that same definition. No namespace-specific driver setup exists.

To test the "flag is off" path, use `Disable`, which keeps the flag known so
that evaluation returns your default instead of `feature.ErrNotFound`. `Enable`
undoes it and `Delete` removes the flag:

```go
driver.Disable(checkoutFlag.Name())
// Value returns the caller's default and a nil error
driver.Delete(checkoutFlag.Name())
// Value returns the caller's default and feature.ErrNotFound
```

The driver ignores subject attributes, so it cannot test targeting rules based
on them; give each subject you care about an override with `SetFor`. It is safe
for concurrent use, and changes apply to the next evaluation. For asserting on
the spans and metrics an evaluation records, use `telemetrytest`, as described
in [Fakes](../testing/fakes.md).

## Checking a driver against the contract

If you [write a driver](../drivers/writing-a-driver.md), `featuretest.Run`
checks portable evaluation behavior against the contract. Feature flags are
read-only, so the suite cannot create flags through the driver. You supply a
**harness**: a small type that gives the suite a ready driver and installs and
removes flags in the backend.

Implement `featuretest.Harness` with `Driver() feature.Driver`,
`Set(*testing.T, featuretest.Flag)`, and `Delete(*testing.T, string)`.
Unlike an application `feature.Flag[T]`, the suite's `featuretest.Flag` is a raw
provider fixture: it deliberately separates stored type from requested type to
test mismatches. Its `Key`, `Value`, `Overrides`, and `Disabled` fields describe
the backend state to install. If you adapt the typed memory driver, switch on
the canonical fixture value type and construct the corresponding typed
definition before calling `Set` or `SetFor`.

With your factory `newHarness func(*testing.T) featuretest.Harness`, pass a
fresh harness to the conformance suite for each test:

```go
func TestConformance(t *testing.T) {
	featuretest.Run(t, newHarness)
}
```

A real backend's harness calls its admin API, writes a test data source, or
writes a file. The rules for a harness follow from how the suite works:

- `Set` replaces the whole flag and never merges. It may run after the driver
  has already served evaluations. Install the value first, then disable the
  flag when `Flag.Disabled` is true.
- `Delete` must accept keys that do not exist, because cleanup deletes each
  key again.
- Fixture values are bool, string, `int64`, and `float64`. A flag may have only
  overrides, but never neither a value nor an override.
- A value that matches no override must read back with the reason static,
  default, or unknown. An override for subject `u1` must match whatever the
  subject's attributes are.

Each subtest gets its own harness and unique flag keys, so a shared backend
works, and subtests run in parallel. If your backend picks up changes
asynchronously, for example by polling, pass `featuretest.WithPropagation(timeout)`:
fixture-state reads are then retried every 100 ms until they show the expected
state or the timeout passes. If a change after the first evaluation cannot be
observed at all, pass `featuretest.WithoutUpdates()` to skip the update and delete
cases. Options describe what a backend can do, never a different meaning of
the contract.

The raw-driver cases reject empty and unknown kinds with `ErrTypeMismatch` for
missing flags, stored defaults, disabled flags, and matching or unmatched
overrides. An already-canceled context must win over kind rejection. These
checks run immediately without propagation retries: kind validity cannot depend
on backend state. Known kinds retain the normal disabled/fallback behavior, and
typed invalid definitions still fail with `ErrInvalidFlag` before driver access.

Because the harness supplies a ready driver, also test your adapter before
startup and with a provider-call recorder. Invalid kinds must fail before
readiness checks, lookup, or provider I/O, as specified for
[raw driver requests](overview.md#raw-driver-requests). Passing the ready-driver
suite alone does not establish those lifecycle and no-I/O properties.

The suite checks the reason only where the contract fixes it: an override must
report `ReasonTargetingMatch` and a disabled flag `ReasonDisabled`. Variants
are never checked. Run a vendor driver's suite as an
[integration test](../testing/integration-tests.md), skipped with `-short`;
[Driver Conformance Suites](../testing/conformance-suites.md) explains the
pattern.

For every function and option, see [the `feature` API reference](https://pkg.go.dev/arkoren.dev/core/service/feature).
