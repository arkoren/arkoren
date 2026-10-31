---
title: Feature Flags Overview
nav: Overview
description: Create a feature flag client, evaluate flags for a subject with a safe default, read variants and reasons, and know the guarantees.
order: 10
---

# Feature Flags Overview

## Introduction

A feature flag lets you change what your application does without deploying
it. You wrap new behavior in a check, ship the code switched off, and turn it
on for yourself, then for a few users, then for everyone. A flag can also
carry a value instead of a yes or no, such as an upload limit or the name of a
plan, so you can tune it while the service runs.

Arkoren's feature flag contract is read-only. Your code asks for a flag's value
for a given user; creating flags, editing their rules, and rolling them
out happen in the tool that stores them. The **facade**, `*feature.Flags`, is
what your code uses, and a **driver** talks to the place where the flags
live. The [memory driver](testing.md#testing-your-application) holds flags in
the process, which suits tests and local development, and the
[OpenFeature driver](drivers.md#openfeature) reaches any flag service that has
an OpenFeature provider. The [LaunchDarkly driver](drivers.md#launchdarkly)
uses LaunchDarkly's own SDK. Switching drivers changes driver construction and
lifecycle setup, not your application's calls to the facade. Targeting rules
remain specific to the chosen provider.

## Creating the facade

Build a driver and wrap it with `feature.New`. The memory driver needs no
lifecycle setup:

Install `arkoren.dev/core` with `go get` in your application's module. Import
`feature` from `arkoren.dev/core/service/feature` and `memoryfeature` from
`arkoren.dev/core/service/feature/memoryfeature`. Examples use your operation
context `ctx`; later snippets also use standard-library `errors` and `log/slog`
for failure classification and logging. Focused snippets omit routine error
handling but preserve errors where they explain fallback behavior:

```go
driver := memoryfeature.New()
flags := feature.New(driver)
```

The facade has no lifecycle. For a driver that connects to a service, register
its `Start`, `Stop`, and `Ping` with your application as described under
[facades and drivers](../core-concepts/facades-and-drivers.md); the memory driver
has none. `Builder.Name` labels the facade in [telemetry](#telemetry), which matters
only when a process builds more than one.

## Evaluating a flag

Choose the fallback first: it must be the behavior your application can safely
use if the flag service is unavailable. Declare one immutable `feature.Flag[T]`
with `NewFlag(name, fallback)` and share it between evaluation and test setup.
The definition binds the literal provider name, Go type, and fallback; a caller
cannot select a different type or fallback while evaluating that definition.
Put shared definitions in the application package that owns the feature.

`Value` takes the request context, definition, and subject. It returns the
definition's Go type. Here `user.ID` is your application's stable user ID:

```go
checkout := feature.NewFlag("new-checkout", false)
subject := feature.Subject{Key: user.ID}

on, err := flags.Value(ctx, checkout, subject)
```

The default is the value your code uses when the flag cannot answer. That
makes it possible to continue without branching on the error: whenever the
error is non-nil, `on` is `false`, so your checkout code can take the old path.
The empty memory driver above returns `feature.ErrNotFound`; use
[`driver.Set`](testing.md#testing-your-application) to supply a test value.
The default also applies, with a nil error, when the flag exists but is off,
or has no value for this
subject. The driver never sees your default; the facade applies it.

Check the error when you want to tell a missing flag from an off one, or when
you log problems:

```go
uploadLimit := feature.NewFlag("max-upload-mb", 10)
limit, err := flags.Value(ctx, uploadLimit, subject)
if errors.Is(err, feature.ErrNotFound) {
	slog.WarnContext(ctx, "flag missing, using default", "flag", "max-upload-mb")
}

// Apply limit in your upload code; it is 10 when the flag is missing.
```

The errors are `feature.ErrNotFound` (no such flag), `feature.ErrTypeMismatch`
(the value does not fit the type you asked for), `feature.ErrNotReady` (the
driver has not loaded its flags yet), and `feature.ErrInvalidSubject` (see
[Subjects](#subjects)). A flag that is switched off is not an error.
An empty name or a zero `Flag[T]` returns `feature.ErrInvalidFlag` before
driver access; call the definition's `Err()` when validating application setup.
Names are literal and are not trimmed, escaped, or normalized. Defining a flag
does not create it in an external service or enforce that service's schema.
Runtime provider type checks still reject incompatible values.
This typed-definition check is separate from raw request-kind validation below;
an invalid typed flag still returns `ErrInvalidFlag`, not `ErrTypeMismatch`.

### Value types

A flag holds a bool, a string, a whole number, or a floating-point number. You
choose which by the type of the definition's fallback. Any signed Go integer
type reads a whole number, `float32` and `float64` read a floating-point number, and a named type
such as `type Plan string` reads a string:

```go
type Plan string

planFlag := feature.NewFlag("plan", Plan("free"))
sampleRatio := feature.NewFlag("sample-ratio", 0.25)
plan, err := flags.Value(ctx, planFlag, subject)
ratio, err := flags.Value(ctx, sampleRatio, subject)
```

Conversion between numbers follows the value. An integer flag reads as a
float. A float flag reads as an integer only when it holds a whole number such
as `3.0`; `3.5` fails with `ErrTypeMismatch` and returns your default, as does
a value too large for the type, such as `300` read as an `int8`. Flags cannot
hold JSON objects yet.

### Variants and reasons

If a user gets a different plan than expected, the value alone does not tell
you whether it came from an override, a rollout, or a default. `ValueDetail`
returns the value together with the reason it was chosen:

```go
detail, err := flags.ValueDetail(ctx, planFlag, subject)
if err == nil && detail.Reason == feature.ReasonTargetingMatch {
	slog.InfoContext(ctx, "plan override", "variant", detail.Variant)
}
```

`Reason` says how the driver chose the result: a single value for everyone
(`ReasonStatic`), a targeting rule or per-user override (`ReasonTargetingMatch`),
a percentage rollout (`ReasonSplit`), no matching rule (`ReasonDefault`), a
flag that is off (`ReasonDisabled`), or a value served from a cache
(`ReasonCached`). When evaluation fails, the reason is `ReasonError` and the
value is your default. `Variant` is the name the flag service gives the chosen
value, empty when it has none. Drivers differ in how much they report, so use
the reason for logging and diagnostics rather than for branching.

### Raw driver requests

Use the typed facade for application decisions: it selects the kind from your
definition and applies the fallback. If you call `flags.Driver().Evaluate`
directly, supply a complete `feature.Request`. With the `checkout` definition
and `subject` above, a raw boolean request is:

```go
result, err := flags.Driver().Evaluate(ctx, feature.Request{
	Key:     checkout.Name(),
	Kind:    feature.KindBool,
	Subject: subject,
})
```

The raw result has no application fallback. For a valid kind, a disabled flag
returns a nil value, `ReasonDisabled`, and no error; the facade turns that nil
into the definition's fallback. A no-value default result follows the same
fallback rule. Direct driver calls bypass facade validation, events, and telemetry.

Raw evaluation checks an already-canceled context first. It then accepts only
`KindBool`, `KindString`, `KindInt`, and `KindFloat`. An empty or unknown kind
returns an error wrapping `feature.ErrTypeMismatch` before readiness checks,
flag lookup, disabled/default shortcuts, or provider I/O. This applies even to
a missing flag or an unstarted driver. Unknown kind strings still round-trip
unchanged through JSON and `String`; preserving their representation does not
make them valid for evaluation.

## Sharing a flag prefix and fallback

Keep standalone `NewFlag` definitions for fixed flags. If several flags share a
provider prefix and the same safe fallback, a namespace supplies both without
an application helper:

```go
checkoutFlags := feature.NewNamespace("checkout", false)
express := checkoutFlags.Flag("express") // express.Name() == "checkout:express"
on, err := flags.Value(ctx, express, subject)
```

Here `subject` is the subject from the evaluation example above. Every child
inherits the exact fallback and its Go type; `Flag` has no callback or child
fallback override. Use a separate namespace or standalone definition when a
flag needs another fallback. Creating the definition does not register a flag:
`checkout:express` must exist in the selected provider. Namespaces do not add
provider targeting, isolation, authorization, or a schema.

The default separator is `":"`; configure a different join through a
nongeneric namespace callback when your provider's key convention requires it:

```go
dotted := checkoutFlags.Derive(func(namespace *feature.NamespaceBuilder) {
	namespace.Separator(".")
})
payments := dotted.Subnamespace("payments")
refunds := payments.Flag("refunds") // refunds.Name() == "checkout.payments.refunds"
```

`Derive` leaves the original unchanged. `Subnamespace` joins with the parent's
separator before running the child's callbacks; an override affects only future
joins. `Name`, `Separator`, `Fallback`, and `Err` inspect the published definition.
Ordinary formatting and structured logging redact namespace names and fallbacks;
the explicit accessors expose them.
An empty separator concatenates names. Names are literal, without escaping or
normalization: `a:b` plus `c` and `a` plus `b:c` both become `a:b:c`. Validate or
encode untrusted components before joining them; convert numeric suffixes with
`strconv.Itoa` or `strconv.FormatInt` from the standard library.

Empty namespace or child names and zero namespaces are invalid with
`feature.ErrInvalidFlag`. The first error is inherited by derived namespaces
and flags. Callbacks run once in order even for invalid definitions; nil entries
panic. Publication is detached from retained drafts.

For user- or tenant-specific behavior, prefer one stable flag and subject
targeting when your provider supports it. Generate dynamic flag names only when
those distinct flags actually exist in the provider. Flag names appear in
telemetry, so avoid personal identifiers and unbounded cardinality in them.

## Subjects

Every evaluation names the subject it is for, usually a user. A `Subject` has a
key, which identifies the subject stably (a user ID), and attributes, which
targeting rules can match:

```go
subject := feature.Subject{
	Key: user.ID,
	Attributes: map[string]any{
		"plan":    "pro",
		"seats":   int64(5),
		"beta":    true,
		"regions": []string{"eu", "uk"},
	},
}
```

The key is what percentage rollouts hash, so the same user always lands in the
same bucket. Keep it stable: a session ID that changes on every login moves the
user in and out of a rollout. An empty key is an anonymous subject and is
valid. A flag then returns its default value, or a value chosen without
bucketing, depending on the flag service.

Attribute values must be a `string`, `bool`, `int64`, `float64`, `[]string`, or
`time.Time`. Other types, including a plain `int`, fail with
`ErrInvalidSubject` before any driver is called, and the error names the
attribute, never its value. Convert with `int64(n)` when you build the map.
Do not change the map after passing it, and do not put anything in it that the
flag service should not see.

> [!NOTE]
> The subject is an explicit argument, not read from the request. Build it once
> where you know the user, such as in a handler or middleware, and pass it down.

## Events

A flag that fails to evaluate returns your default value, so a missing flag or
a driver that has not synced yet can go unnoticed. Connect your existing
application's event bus when you build the facade to hear about it. Each call whose
driver evaluation fails publishes `feature.evaluation.failed` with the flag key
and the error. The payload has no structured subject, attribute, or flag-value
fields, but its error retains the provider's raw diagnostic text, which can
contain sensitive details. Inspect error identities with `errors.Is` rather
than forwarding the whole payload to untrusted logs or transports.

```go
flags := feature.New(driver, func(client *feature.Builder) {
	client.Events(bus)
})
```

Here `bus` is your existing `*event.Bus` from `arkoren.dev/core/event`.
Every failing call publishes, so a missing flag on a hot path publishes at the
rate of that path; count or sample in the listener rather than alerting on each
event. Failures the facade detects itself—an invalid definition, an invalid
subject, or a value that does not convert—publish nothing. See
[Built-in Events](../events/built-in-events.md#feature-flags).

## Telemetry

Pass `Builder.Telemetry` to choose a handle explicitly; otherwise, evaluations use
the [process default](../observability/telemetry.md#the-process-default), which
is a no-op until configured. Each evaluation records
one span named `feature evaluate`, a counter, and a duration histogram, for
every driver. The span is internal because the facade cannot tell whether a
driver calls a remote service.

The span and the counter carry the flag key, the provider name from `Builder.Name`
(`default` when unset), and the reason; failures also set `error.type` to the
class of the error, such as `feature.ErrNotFound`, and never its text. On a
failure the reason is `error`. The variant appears on the span only. The
histogram has the key, provider, and `error.type`. You can chart how often each
reason occurs per flag and spot a flag that errors everywhere.

Flag keys are recorded because you choose them and there are few. Subject keys,
subject attributes, flag values, and error text are never recorded, since they
can identify people. [Instrumented components](../observability/instrumented-components.md)
lists every attribute and metric for the services that record them.

For every function and option, see [the `feature` API reference](https://pkg.go.dev/arkoren.dev/core/service/feature),
[`memoryfeature`](https://pkg.go.dev/arkoren.dev/core/service/feature/memoryfeature),
[`openfeaturefeature`](https://pkg.go.dev/arkoren.dev/driver/openfeature/openfeaturefeature), and
[`launchdarklyfeature`](https://pkg.go.dev/arkoren.dev/driver/launchdarkly/launchdarklyfeature).
