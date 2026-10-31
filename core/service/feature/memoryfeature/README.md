# Arkoren Feature Flag Memory Driver

`arkoren.dev/core/service/feature/memoryfeature` is an in-process implementation of
`feature.Driver` using only the standard library.

It is suitable for tests, local development, and single-process applications.
Use it in application tests instead of a fake: set the flags a test needs,
then hand the driver to `feature.New`.

## Using the driver

Run `go get arkoren.dev/core` in your application's Go module. With your
operation context `ctx`, populate a flag and evaluate it through the facade:

```go
import (
	"arkoren.dev/core/service/feature"
	"arkoren.dev/core/service/feature/memoryfeature"
)

driver := memoryfeature.New()
checkout := feature.NewFlag("new-checkout", false)
err := driver.Set(checkout, true)
flags := feature.New(driver)
on, err := flags.Value(ctx, checkout, feature.Subject{Key: "user-42"})
```

`on` is `true` with a nil error. Unknown flags return the chosen default and
`feature.ErrNotFound`. The driver has no lifecycle hooks, and its state is not
persistent or shared between processes.

Each flag has an optional default value and optional overrides for single
subject keys. `Set` creates or replaces the default and keeps the overrides;
`SetFor` adds an override and creates the flag, with no default value, when it
is missing; `Disable` turns a flag off without forgetting it and `Enable`
turns it back on; `Delete` removes the flag and its overrides. `Set` and `SetFor`
share the application's immutable `feature.Flag[T]` definition and accept only
values of its Go type, including named primitive types. Unsupported or mismatched
types fail to compile. Invalid or zero definitions return `feature.ErrInvalidFlag`
without mutation. The definition's fallback is not installed as a default by
`SetFor`; only evaluation applies it when no value matches. Definitions do not
create external provider schemas or remove runtime provider type checks.

Optional `feature.NewNamespace(prefix, fallback)` joins with `":"` by default
and returns ordinary flags through `Flag(name)`. Share that same child definition
with `Set`, `SetFor`, and evaluation; its fallback is inherited exactly, not
installed as a stored provider value. Namespaces add no targeting or isolation.

`Evaluate` answers in this order:

1. A canceled context fails with the context's error.
2. An empty or unknown `Kind` fails with `feature.ErrTypeMismatch` before lookup.
3. An unknown key fails with `feature.ErrNotFound`.
4. A disabled flag returns no value with `ReasonDisabled`.
5. An override for the subject key returns its value with `ReasonTargetingMatch`.
6. A default value returns `ReasonStatic`.
7. A flag with only overrides, none matching, returns no value with `ReasonDefault`.

Raw requests accept only `KindBool`, `KindString`, `KindInt`, and `KindFloat`.
Disabled flags and default-only results do not bypass kind validation. Unknown
kind strings still round-trip unchanged through JSON and `String`; this does
not make them valid for evaluation. Typed flag definitions retain their separate
`feature.ErrInvalidFlag` validation before driver access.

A stored value that cannot be read as the requested kind fails with
`feature.ErrTypeMismatch`; integers are stored as `int64`, floats as
`float64`, and an integer read as a float is converted. Subject attributes and
anonymous subjects are accepted and ignored: an empty subject key never matches
an override.

See the [feature flag testing guide](../../../../cmd/arkoren/skills/arkoren/references/guides/feature-flags/testing.md)
and [memory driver reference](https://pkg.go.dev/arkoren.dev/core/service/feature/memoryfeature).
