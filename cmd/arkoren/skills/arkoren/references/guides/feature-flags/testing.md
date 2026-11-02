---
title: Feature Flag Testing
nav: Testing
description: Arrange deterministic flag states, exercise evaluation failures, and check custom drivers with a disposable conformance harness.
order: 30
summary:
  - Arrange independent memory fixtures and explicit subject-key overrides.
  - Distinguish disabled flags, missing keys, and failed evaluations in application tests.
  - Use a small failure fake without depending on a live flag provider.
  - Check custom drivers with disposable conformance fixtures and bounded propagation waits.
---

# Feature Flag Testing

Test a rollout by controlling its inputs, not by waiting for a provider's rules
to change. The memory driver gives each test independent state while retaining
the real facade's type checks and fallback behavior. Pass that facade into your
application code to assert the selected behavior; use an error-returning fake
when the test needs a failure memory cannot produce.

## Memory Fixtures

Share the application's flag definition with its tests. Here `expressCheckout`
is the `feature.Flag[bool]` declared by the checkout package with key
`checkout-express` and fallback `false`. In your `_test.go` file, import `feature`
from `arkoren.dev/core/service/feature` and `memoryfeature` from its
`memoryfeature` subpackage.

With an existing `*testing.T` named `t`, create a fresh driver and install the
enabled value:

```go
driver := memoryfeature.New()
if err := driver.Set(expressCheckout, true); err != nil {
	t.Fatal(err)
}
flags := feature.New(driver)
```

Pass `flags` to the checkout service under test. Every subject initially receives
`true`; assert that the service selects the express flow. A second test can
install `false` and assert the standard flow. Keep fixtures local to each test
so parallel tests do not change one another's decisions.

`Set` creates the stored entry or replaces its default value, retaining existing
overrides and disabled state. Its value must have the definition's exact Go type,
including named types. Invalid definitions return `ErrInvalidFlag` without
mutation. The application's fallback is not installed as a provider default.

## Subject Overrides

If a rollout targets one account, arrange that identity explicitly rather than
trying to reproduce percentage bucketing. Using the memory `driver` and `flags`
above, override one stable account key:

```go
if err := driver.SetFor(expressCheckout, "account-river", false); err != nil {
	t.Fatal(err)
}
subject := feature.Subject{Key: "account-river"}
detail, err := flags.ValueDetail(t.Context(), expressCheckout, subject)
if err != nil || detail.Value || detail.Reason != feature.ReasonTargetingMatch {
	t.Fatalf("override = %+v, %v; want false, targeting match, nil", detail, err)
}
```

This account receives `false`; other accounts still receive the stored `true`.
`SetFor` creates an override-only flag if the key is absent. In that case an
unmatched subject receives the definition's fallback with `ReasonDefault` and
no error. An empty subject key never matches an override, even one stored under
the empty string.

Use the same [namespace-derived definition](overview.md#flag-namespaces) in
evaluation and fixture setup when the application uses one. `Set` and `SetFor`
address its assembled provider key without special namespace configuration.

Memory ignores subject attributes: these tests prove application behavior for
chosen identities, not provider attribute rules, segment membership, or rollout
distribution. Use the [local SDK fixtures](drivers.md#local-provider) to test
adapter wiring, and a disposable provider integration environment when the
provider's actual rules are the subject of the test.

## Disabled Flags

A disabled flag and a missing flag both select the fallback, but only the missing
flag is an error. To test that distinction, first install the shared checkout
definition in memory, then disable it:

```go
driver.Disable(expressCheckout.Name())
detail, err := flags.ValueDetail(t.Context(), expressCheckout, feature.Subject{})
if err != nil || detail.Value || detail.Reason != feature.ReasonDisabled {
	t.Fatalf("disabled = %+v, %v; want false, disabled, nil", detail, err)
}
```

Disabling retains the entry's default and overrides; `Enable` makes them visible
again. Disabling or enabling a missing key does nothing.

To exercise a missing definition in the provider instead, delete the entry.
Import standard-library `errors` for identity checks:

```go
driver.Delete(expressCheckout.Name())
enabled, err := flags.Value(t.Context(), expressCheckout, feature.Subject{})
if enabled || !errors.Is(err, feature.ErrNotFound) {
	t.Fatalf("missing = %v, %v; want false, ErrNotFound", enabled, err)
}
```

Deletion removes both the stored value and overrides and is harmless if repeated.
Use these distinct fixtures to verify whether your application tolerates a
missing flag or treats it as configuration failure.

## Failed Evaluations

The fallback is available even when evaluation fails; continuing the request is
an application policy, not an automatic retry or permission grant. For checkout,
a safe policy can keep the standard flow available while observing the failure.
Assert both the chosen flow and the error reporting your application requires.
Account permissions must remain enforced on both paths.

To force readiness or backend errors without a network dependency, implement a
small test-only driver. Import standard-library `context` as well as `errors`:

```go
type unavailableFlags struct{}

func (unavailableFlags) Evaluate(ctx context.Context, _ feature.Request) (feature.Result, error) {
	if err := ctx.Err(); err != nil {
		return feature.Result{}, err
	}
	return feature.Result{}, feature.ErrNotReady
}
```

This fake deliberately models one failure path; it is not a production driver
or a conforming implementation for arbitrary requests. Wrap it with the real
facade to check that your shared definition supplies the fallback:

```go
flags := feature.New(unavailableFlags{})
detail, err := flags.ValueDetail(t.Context(), expressCheckout, feature.Subject{})
if detail.Value || detail.Reason != feature.ReasonError ||
	!errors.Is(err, feature.ErrNotReady) {
	t.Fatalf("unavailable = %+v, %v; want false, error reason, ErrNotReady", detail, err)
}
```

Pass this facade into your application test to verify the standard flow and its
failure policy. Also test canceled contexts if your policy distinguishes request
cancellation from provider unavailability. Do not add sleeps to create failures.

For observability assertions, driver errors publish
[evaluation-failed events](overview.md#events) when a bus is configured. Invalid
definitions, invalid attributes, and facade conversion errors do not publish
that event. [Telemetry test helpers](../testing/fakes.md) can inspect spans and
metrics, which cover those facade failures too. Prefer an explicit test telemetry
handle over changing the process default in parallel tests.

## Driver Conformance

If you [write a custom driver](../drivers/writing-a-driver.md), use
`featuretest` from `arkoren.dev/core/service/feature/featuretest` to exercise the
portable contract. Its harness owns a ready driver and a way to install and
remove disposable backend fixtures; production `feature.Driver` stays read-only.
Never point a fixture harness at production flags.

Implement `featuretest.Harness` with `Driver() feature.Driver`,
`Set(*testing.T, featuretest.Flag)`, and `Delete(*testing.T, string)`. With an
existing factory `newHarness func(*testing.T) featuretest.Harness`, invoke it
from your test:

```go
featuretest.Run(t, newHarness)
```

Each subtest gets a fresh harness and unique keys, and subtests run in parallel.
The factory must register cleanup for any acquired resources. A harness returns
the same ready driver throughout its lifetime. Its fixture operations must:

- Replace the whole flag on `Set`, not merge with previous state. Install the
  value and overrides before applying `Disabled`.
- Accept `bool`, `string`, `int64`, and `float64` fixtures. A fixture has a default
  value or at least one override; it never has neither.
- Allow changes after evaluation and make `Delete` tolerate absent keys,
  including repeated cleanup deletion.
- Match an override independently of the subject's attributes. An unmatched
  default value reports static, default, or unknown; a matching override reports
  `ReasonTargetingMatch`, and a disabled flag reports `ReasonDisabled`.

`featuretest.Flag` is a raw backend fixture, not the application's typed
`feature.Flag[T]`. It separates stored and requested types so the suite can test
mismatches. A harness adapting typed memory setup must switch on fixture types
and construct corresponding typed definitions.

For asynchronously refreshed fixtures, give the suite an observation budget.
With standard-library `time` imported:

```go
featuretest.Run(t, newHarness, featuretest.WithPropagation(2*time.Second))
```

Reads after fixture installation or deletion retry every 100 milliseconds until
the expected state appears or that budget expires. This does not configure
provider retries. `WithoutUpdates()` skips cases that update or delete a served
flag when the backend cannot expose those changes in the test; report that
coverage limit rather than treating it as verified refresh behavior.

Raw invalid-kind checks run immediately without propagation retries. They cover
missing, default-only, disabled, matched, and unmatched fixtures, and require
cancellation to win over kind rejection. Because the harness supplies a ready
driver, separately test invalid kinds before startup and before provider calls
with a call recorder. Variants are not checked by the suite. Run remote-backed
conformance as an isolated [integration test](../testing/integration-tests.md),
not as a prerequisite for deterministic application tests.

See the [`feature` API](https://pkg.go.dev/arkoren.dev/core/service/feature),
[`memoryfeature` API](https://pkg.go.dev/arkoren.dev/core/service/feature/memoryfeature),
and [`featuretest` API](https://pkg.go.dev/arkoren.dev/core/service/feature/featuretest).
