---
title: Feature Flags Overview
nav: Overview
description: Evaluate typed flags for explicit subjects and choose safe behavior when evaluation supplies no value or fails.
order: 10
summary:
  - Declare typed flag decisions and distinguish successful fallbacks from evaluation failures.
  - Build explicit targeting subjects without treating flags as account permissions.
  - Interpret provider details and compose literal keys with shared namespace fallbacks.
  - Observe failures through events and telemetry while protecting private data and cardinality.
---

# Feature Flags Overview

A checkout rollout needs to select the new flow without removing the old one.
A **feature flag** supplies that decision, or a scalar setting, for a particular
subject. Your application reads flags through `*feature.Flags`; the selected
[driver](drivers.md) evaluates values managed in memory or in a provider's tools.
The portable contract does not create flags or change provider rules.

Flags are not authorization. Enforce [account permissions](../authorization/overview.md)
regardless of which flow a flag selects, including when evaluation fails.

## Flag Decisions

Choose the safe behavior before declaring a flag. A `feature.Flag[T]` binds the
literal provider key, Go value type, and fallback in one immutable definition.
Keep the definition in the application package that owns the behavior and share
it with tests. Import `feature` from `arkoren.dev/core/service/feature`:

```go
var expressCheckout = feature.NewFlag("checkout-express", false)
```

This definition does not register a provider flag. Configure that exact key in
your provider; its schema is still checked at evaluation time. Empty names and
zero definitions are invalid: `Err()` reports `feature.ErrInvalidFlag` without
contacting a driver.

With an existing configured facade `flags`, operation context `ctx`, and stable
account ID `accountID`, read the decision:

```go
subject := feature.Subject{Key: accountID}
enabled, err := flags.Value(ctx, expressCheckout, subject)
```

Use `enabled` to select the checkout flow, after permission checks. Every non-nil
error comes with the declared fallback, so this flag selects the old flow on
failure. Focused snippets keep error results visible but omit routine propagation;
complete application code still handles its errors.

The same fallback can mean several different things:

| Evaluation | Value | Error |
| --- | --- | --- |
| Disabled flag | Declared fallback | Nil |
| No value applies to this subject | Declared fallback | Nil |
| Missing provider key | Declared fallback | `feature.ErrNotFound` |
| Incompatible value or numeric conversion | Declared fallback | `feature.ErrTypeMismatch` |
| Driver still loading | Declared fallback | `feature.ErrNotReady` |
| Invalid subject or definition | Declared fallback | `feature.ErrInvalidSubject` or `feature.ErrInvalidFlag` |

Errors wrap these identities; classify them with standard-library `errors.Is`.
Cancellation and other provider errors also return the fallback. A successful
provider result can itself equal the fallback, so the value alone does not prove
whether evaluation succeeded. Test your application's
[failed-evaluation policy](testing.md#failed-evaluations) explicitly.

### Scalar Settings

Use a value-bearing flag when a rollout needs more than a boolean. Definitions
support booleans, strings, signed integers, and floating-point values, including
named types:

```go
type CheckoutLayout string

var checkoutLayout = feature.NewFlag("checkout-layout", CheckoutLayout("standard"))

layout, err := flags.Value(ctx, checkoutLayout, subject)
```

`layout` retains the `CheckoutLayout` type. JSON objects and unsigned integers
are not supported. Integer values can be read as floats; float values can be read
as integers only when whole and representable. Reading `3.5` as an integer, or
`300` as `int8`, returns the fallback with `ErrTypeMismatch`, not a truncated value.

## Subject Targeting

A **subject** identifies who the decision is for and supplies facts that provider
rules can match. Build those facts from trusted application state rather than
letting request fields choose account identity or permissions. For an account
already loaded by your application, use a small typed input:

```go
type CheckoutAccount struct {
	ID    string
	Plan  string
	Seats int64
	Beta  bool
}
```

Convert an existing `account CheckoutAccount` into the contract's attribute map:

```go
subject := feature.Subject{
	Key: account.ID,
	Attributes: map[string]any{
		"plan":  account.Plan,
		"seats": account.Seats,
		"beta":  account.Beta,
	},
}
enabled, err := flags.Value(ctx, expressCheckout, subject)
```

The facade accepts only `string`, `bool`, `int64`, `float64`, `[]string`, and
`time.Time` attribute values. A plain `int` is invalid; convert it explicitly.
Invalid attributes fail before driver access, and validation errors name the
attribute rather than its value. Do not mutate the map or its slices during use.
Send only facts approved for the provider to receive.

Keep subject keys stable when your provider uses them for rollout bucketing.
Arkoren does not guarantee identical buckets across providers, unchanged
assignments after rule changes, exact percentages in a population, or atomic
rollout changes across replicas. A zero subject is valid and anonymous; provider
handling differs, especially for [LaunchDarkly](drivers.md#launchdarkly).

Segments and targeting rules remain provider-owned. A subject key identifies the
evaluated entity, not a segment; attributes supply facts, not portable segment
membership instructions. The memory driver supports key overrides but ignores
attributes. The subject is always an explicit argument, not inferred from `ctx`.

## Evaluation Details

When investigating an unexpected flow, read the value and its explanation in one
evaluation rather than evaluating again for diagnostics:

```go
detail, err := flags.ValueDetail(ctx, expressCheckout, subject)
```

`detail.Value` has the same fallback behavior as `Value`. `detail.Reason` describes
the driver's choice: static value, targeting match, split, default, disabled,
cached, or unknown. On failure it is `ReasonError`, the variant is empty, and the
error remains available separately. A no-value success preserves the driver's
reason and variant while substituting the fallback.

`detail.Variant` is optional provider metadata, not the flag key or subject ID.
OpenFeature passes through a provider's variant; LaunchDarkly reports a variation
index as text. Reason coverage differs too: not every percentage rollout reports
`ReasonSplit`. Use these fields for diagnostics, not authorization or a portable
business branch. [Drivers](drivers.md) explains each mapping.

## Flag Namespaces

If several flags share a key prefix and the same typed fallback, a namespace
keeps that naming policy together:

```go
checkoutFlags := feature.NewNamespace("checkout", false)
express := checkoutFlags.Flag("express")
```

`express.Name()` is `checkout:express`, and its fallback is `false`. Each child
inherits the exact fallback and Go type; use another namespace or standalone
definition when either differs. Create the assembled key in the provider.
Namespaces add neither provider isolation nor targeting or authorization.

If the provider uses dotted keys, derive a separate naming policy:

```go
dotted := checkoutFlags.Derive(func(names *feature.NamespaceBuilder) {
	names.Separator(".")
})
refunds := dotted.Subnamespace("payments").Flag("refunds")
```

The resulting key is `checkout.payments.refunds`; the original namespace is
unchanged. A subnamespace joins its own name using the parent's separator before
applying child callbacks to future joins. An empty separator concatenates names.
Names are literal: no trimming, escaping, or normalization prevents collisions
such as `a:b` plus `c` and `a` plus `b:c`.

Empty prefixes or child names and zero namespaces retain `ErrInvalidFlag` through
all descendants. Construction callbacks run once in order, including for invalid
definitions; nil callbacks panic, and retained drafts cannot change published
values. Namespace formatting and structured logging redact names and fallbacks;
explicit `Name()` and `Fallback()` accessors expose them.

Prefer stable flag keys with subject targeting over per-account keys. If dynamic
keys really exist in the provider, validate or encode components before joining
them and format numeric suffixes explicitly. Keys enter telemetry: personal IDs
and unbounded names create privacy and cardinality problems.

## Raw Requests

Application decisions should use the typed facade. A custom adapter or contract
test can call a driver directly with an explicit request:

```go
result, err := flags.Driver().Evaluate(ctx, feature.Request{
	Key:     expressCheckout.Name(),
	Kind:    feature.KindBool,
	Subject: subject,
})
```

This bypasses facade validation, fallback conversion, events, and telemetry.
`result.Value == nil` means no value was supplied; it does not install your
application fallback. Drivers first reject an already-canceled context, then
reject empty or unknown kinds with `ErrTypeMismatch` before readiness, lookup,
disabled/default shortcuts, or provider I/O. Only `KindBool`, `KindString`,
`KindInt`, and `KindFloat` are valid. Unknown kind strings can round-trip through
JSON and `String()` without becoming valid requests.

## Events

Fallbacks let requests continue, but repeated provider failures still need
attention. At facade construction, connect an existing `*event.Bus` named `bus`
from `arkoren.dev/core/event`:

```go
flags := feature.New(driver, func(client *feature.Builder) {
	client.Events(bus)
})
```

Each driver evaluation error publishes `feature.evaluation.failed` with the key
and error. Successful disabled/default results publish nothing. Invalid
definitions, invalid attributes, and conversion errors detected by the facade
also publish nothing; use telemetry to observe those failures.

The payload has no structured subject, attribute, or value fields, but provider
error text can contain sensitive information. Classify errors instead of
forwarding raw payloads to untrusted destinations. Count or sample hot-path
failures rather than alerting once per evaluation. See
[built-in events](../events/built-in-events.md#feature-flags) for the event contract.

## Telemetry

Evaluations follow the [shared telemetry handle](../observability/telemetry.md#shared-handle)
unless construction explicitly supplies `Builder.Telemetry`. The process default
is resolved on each evaluation and is a no-op until configured. `Builder.Name`
sets a fixed provider label; its default is `default`.

When enabled, each evaluation records an internal `feature evaluate` span,
`arkoren.feature.evaluations` counter, and
`arkoren.feature.evaluation.duration` histogram. Spans and counters include the
key, provider label, and reason; the histogram includes key and provider label.
Failures add the error class as `error.type`, never the error text. Nonempty
variants appear on spans only.

Facade telemetry does not record subject keys, attributes, or flag values.
Keep keys and provider labels bounded and nonpersonal, and review variant names
too. Native SDK hooks and analytics have their own data policy; facade redaction
does not sanitize them. [Instrumented components](../observability/instrumented-components.md)
lists the metric and attribute contracts.

For every function and option, see the
[`feature` API reference](https://pkg.go.dev/arkoren.dev/core/service/feature).
