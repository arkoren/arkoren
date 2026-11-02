---
title: Feature Flag Drivers
nav: Drivers
description: Choose and construct memory, OpenFeature, or LaunchDarkly drivers with explicit lifecycle and native-client ownership.
order: 20
summary:
  - Compare memory, OpenFeature, and LaunchDarkly evaluation capabilities and availability limits.
  - Construct drivers with explicit startup, readiness, cleanup, and restart policies.
  - Configure ambient context or native SDK settings and retain the correct client owner.
  - Exercise local SDK fixtures and understand provider-specific results and analytics.
---

# Feature Flag Drivers

Choose memory for deterministic application tests or process-local settings.
Choose OpenFeature when your flag service has an OpenFeature Go provider or your
application already manages an OpenFeature client. Choose the native LaunchDarkly
driver when you want direct integration with LaunchDarkly's official server SDK.
All three expose the same [typed evaluation](overview.md#flag-decisions), but
targeting rules, update delivery, and outage behavior are not interchangeable.

## Compatibility Matrix

This comparison covers Arkoren's exposed evaluation contract, not provider flag
administration. Scalar flags are booleans, strings, signed integers, and floats.
Every driver accepts standalone and namespace-derived definitions; neither form
creates provider keys or enforces a provider schema.

| Driver | Scalar Flags | Key Targeting | Attribute Targeting | Object Flags | Native Client | Remote-Free Evaluation |
| --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | Yes | Yes — Explicit nonempty-key overrides. | No | No | No | Yes |
| [OpenFeature](#openfeature) | Yes | Partial — Depends on the configured provider. | Partial — Attributes pass through; rule support depends on the provider. | No | Yes | Partial — Depends on the provider's evaluation and refresh model. |
| [LaunchDarkly](#launchdarkly) | Yes | Yes — User contexts; anonymous subjects share one key. | Yes — Subject attributes become user-context attributes. | No | Yes | Yes — Evaluates locally after initial synchronization. |

No driver guarantees atomic rule changes across processes or identical rollout
assignments across providers. Memory has independent state in each instance.
OpenFeature's provider determines remote availability requirements. LaunchDarkly
can evaluate its last received data during a connection outage, but that does not
prove the data is current or let a new process start without initial data.

Run installation commands from your application's Go module. Examples use
`feature` from `arkoren.dev/core/service/feature`; `ctx` is an operation context.
The facade itself has no lifecycle. Register driver startup, cleanup, and
readiness with your existing [application lifecycle](../application/lifecycle-and-ownership.md),
and do not evaluate an owned provider until startup succeeds.

## Memory

`memoryfeature` is included in `arkoren.dev/core` and needs no external service.
Use it when your application controls the stored values and needs neither
provider targeting rules nor shared state between processes.

```go
import "arkoren.dev/core/service/feature/memoryfeature"

driver := memoryfeature.New()
flags := feature.New(driver)
```

Construction performs no I/O, has no options, and starts no background work.
There are no `Start`, `Stop`, or `Ping` hooks to register.

To supply the checkout decision, share the application's typed definition:

```go
expressCheckout := feature.NewFlag("checkout-express", false)
err := driver.Set(expressCheckout, true)
```

The next [facade evaluation](overview.md#flag-decisions) returns `true` unless
the flag is disabled or a subject override selects another value. Setup errors
must be handled before the value is used. `Set` retains existing overrides and
disabled state; it does not install the definition's fallback as a stored value.
[Testing](testing.md#memory-fixtures) covers overrides and state changes.

The driver is concurrent-safe and loses all state when discarded. It ignores
subject attributes, has no rollout engine, and never matches an override whose
key is empty. Separately constructed definitions with the same provider name
still address the same stored entry; they do not establish a schema.

See the [`memoryfeature` API](https://pkg.go.dev/arkoren.dev/core/service/feature/memoryfeature).

## OpenFeature

OpenFeature connects an evaluation API to a provider. Supply an
`openfeature.FeatureProvider` from your chosen vendor's Go integration, configured
with that vendor's endpoint and credentials. Provider documentation determines
targeting support, refresh intervals, network calls, caching, and outage recovery;
Arkoren does not add missing capabilities.

### Provider Setup

Install the Arkoren module and the Go provider module you selected:

```sh
go get arkoren.dev/driver/openfeature
```

With the configured provider named `provider`, construct an owned driver:

```go
import "arkoren.dev/driver/openfeature/openfeaturefeature"

driver := openfeaturefeature.New(provider)
flags := feature.New(driver)
```

`New` performs no I/O. `Start` registers and initializes the provider in a private
OpenFeature API instance; it neither uses nor replaces the global provider.
One provider belongs to one owned driver. Register `Stop` for cleanup, `Start`
for startup, and `Ping` for readiness. `Ping` performs no network call and
requires the SDK's provider state to be `READY`; a stale provider is not ready
even if it can still serve a cached evaluation.

An owned driver cannot restart after `Stop` or failed `Start`; construct a new
one to retry initialization. Stopping before startup also makes it terminal,
without initializing or shutting down the unused provider. Failed startup
attempts provider cleanup and leaves evaluation unavailable.

Owned lifecycle calls serialize, and waiting for another lifecycle call is not
bounded by `ctx`. Context-aware providers must cooperate with initialization and
cleanup cancellation. Legacy contextless shutdown is not bounded by the cleanup
context, and a legacy initializer can outlive canceled startup inside the SDK.
Do not call the same driver's lifecycle recursively from provider callbacks.

### Ambient Context

If every rule needs a deployment fact, supply it once at construction. Import
`openfeature` from `github.com/open-feature/go-sdk/openfeature`:

```go
driver := openfeaturefeature.New(provider, func(features *openfeaturefeature.Builder) {
	features.EvaluationContext(openfeature.NewTargetlessEvaluationContext(
		map[string]any{"environment": "production"},
	))
})
```

This is an alternative construction. Per-evaluation subject attributes replace
ambient attributes of the same name; a nonempty subject key replaces the ambient
targeting key. An empty key leaves the ambient key in effect. If neither provides
a key, providers that require one report `feature.ErrInvalidSubject`.

Construction copies outer attribute maps at input and publication, not nested
values. Retained drafts cannot change the published context; keep shared nested
values immutable. Construction callbacks run once in order; nil callbacks panic.
Configure credentials, polling, and network budgets on the provider, not on the
Arkoren builder.

### Evaluation Results

After startup, use the shared [flag decision](overview.md#flag-decisions).
The subject key becomes the targeting key, and accepted attributes pass through
unchanged. Each provider decides which types its rules support. Provider-side
segments retain that provider's own identifiers and rules.

Missing flags map to `ErrNotFound`; type and parse errors to `ErrTypeMismatch`;
not-ready providers to `ErrNotReady`; missing targeting keys and invalid contexts
to `ErrInvalidSubject`. Other failures retain the provider error code in the
error. A reported disabled result supplies no value, so the facade uses the
definition's fallback without an error.

Known reasons map to their portable equivalents; `STALE` maps to `ReasonCached`.
Empty or unrecognized reasons become `ReasonUnknown`. Variants pass through
unchanged. An OpenFeature default result can include a real provider value:
`ReasonDefault` alone does not mean the application fallback was used.

### Native Ownership

If your application already registers an OpenFeature provider and manages its
hooks, domains, and shutdown, borrow its initialized `*openfeature.Client`
named `client`:

```go
driver := openfeaturefeature.NewFrom(client)
flags := feature.New(driver)
```

`NewFrom` accepts no options. `Start` and `Stop` do nothing; the application
retains provider registration and cleanup. `Ping` still reports provider state.
Do not rebind or shut down a `New` driver's owned provider through native access.

To attach an existing `openfeature.Hook` named `hook` during setup, use the native
client before concurrent evaluation:

```go
driver.Client().AddHooks(hook)
```

Hooks can receive subject data and values omitted by facade telemetry. Review
their export policy before use. The owned native client is available before
startup, but direct evaluation then returns SDK no-op results rather than the
facade's `ErrNotReady`. Native calls bypass facade validation and telemetry.
Native access does not add portable object flags,
and Arkoren ships no OpenFeature flag-administration or webhook adapter.

### Local Provider

The SDK's memory provider exercises the OpenFeature path without a remote
service. Define the fixture separately from driver construction:

```go
import "github.com/open-feature/go-sdk/openfeature/memprovider"

provider := memprovider.NewInMemoryProvider(map[string]memprovider.InMemoryFlag{
	"checkout-express": {
		Key:            "checkout-express",
		State:          memprovider.Enabled,
		DefaultVariant: "express",
		Variants:       map[string]any{"express": true},
	},
})
```

Pass this provider to `New` and run the lifecycle described above. After startup,
evaluating `feature.NewFlag("checkout-express", false)` returns `true` with no
error. `Stop` releases the provider; no server needs cleanup. This proves SDK
wiring, not your production provider's targeting or refresh behavior.

See the [`openfeaturefeature` API](https://pkg.go.dev/arkoren.dev/driver/openfeature/openfeaturefeature).

## LaunchDarkly

Choose this driver for LaunchDarkly's official Go server SDK. The SDK receives
flag updates and evaluates locally. You need a LaunchDarkly environment, its
server-side SDK key, and the application's flags configured in that environment.
Load the key from approved secret configuration rather than embedding it in code.

### Provider Setup

```sh
go get arkoren.dev/driver/launchdarkly
```

With the environment's key already loaded as `sdkKey`, construct an owned driver:

```go
import "arkoren.dev/driver/launchdarkly/launchdarklyfeature"

driver, err := launchdarklyfeature.New(sdkKey)
```

`New` performs no I/O and rejects an empty key. Handle that error before
constructing the facade:

```go
flags := feature.New(driver)
```

Register `Start`, `Stop`, and `Ping`. Startup builds the SDK client and waits for
initial flag data; failure closes the acquired client instead of publishing it.
The default SDK receives updates from LaunchDarkly; an SDK-configured persistent
store can provide last-known data. Treat that data as potentially stale, not as
proof of a live remote connection.
`Ping` performs no I/O and checks that a client exists and reports initialized.
It is not a remote connection or freshness check. Before startup and after stop,
evaluation returns `ErrNotReady`.

Unlike OpenFeature, an owned LaunchDarkly driver can retry failed startup or
start again after stop. Starting an already running driver fails. `Stop` closes
the client and flushes pending analytics; the SDK's close is not bounded by the
cleanup context. Owned lifecycle calls serialize without a context-bounded wait.
Native construction hooks must not recursively call the driver's lifecycle.

### Startup Budget

Startup waits five seconds by default. If your deployment needs a longer initial
synchronization budget, configure it when constructing the driver. Import
standard-library `time`:

```go
driver, err := launchdarklyfeature.New(sdkKey, func(features *launchdarklyfeature.Builder) {
	features.StartWait(10 * time.Second)
})
```

Zero selects the five-second default; a negative wait returns immediately and
fails unless the source is already ready. Startup checks `ctx` before client
construction, but subsequent cancellation does not interrupt the SDK's wait.
`StartWait` is not an evaluation timeout.

For native endpoints, storage, event settings, or network budgets, supply an SDK
hook. This alternative construction disables SDK analytics, not facade telemetry:

```go
import (
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
)

driver, err := launchdarklyfeature.New(sdkKey, func(features *launchdarklyfeature.Builder) {
	features.SDK(func(config *ld.Config) {
		config.Events = ldcomponents.NoEvents()
	})
})
```

The hook runs at `Start`, not at construction. Published settings are detached
from the construction draft, but native objects and captured state remain shared.
All construction callbacks run once in order; nil callbacks panic.

### Evaluation Results

Create `checkout-express` as a boolean flag in the selected environment, then use
the shared [flag decision](overview.md#flag-decisions) after startup.

> [!WARNING]
> An off flag or failed prerequisite returns the definition's fallback, not
> LaunchDarkly's configured off variation. Choose a safe application fallback.
> If a configured provider value must be served, keep the flag on with its
> prerequisites satisfied and select that value through targeting rules.

Each subject becomes a context of kind `user`. An empty key becomes the anonymous
key `anonymous`, so all anonymous subjects share one identity and rollout bucket.
Attributes named `key` or `kind` cannot replace this identity. String lists become
arrays, and `time.Time` becomes Unix milliseconds for date rules. The SDK can
reject reserved attributes with incompatible types as `ErrInvalidSubject`.

Target and rule matches report `ReasonTargetingMatch`; fallthrough reports
`ReasonDefault`. Experiment rule matches and fallthrough report `ReasonSplit`;
ordinary percentage rollouts need not. Variants are variation indexes formatted
as strings, such as `"1"`, not named segments or variation labels.

Missing keys, wrong types, uninitialized clients, and invalid contexts map to the
corresponding portable errors. Malformed flags and SDK exceptions retain their
error kind. Null variations supply no value and therefore use the fallback.
Fractional numbers requested as integers fail rather than truncate. Evaluation
is local and checks cancellation only before SDK evaluation.

### Native Ownership

If your application already owns a configured `*ld.LDClient` named `client`,
wrap it with `launchdarklyfeature.NewFrom(client)`. The wrapper accepts no
options, and its `Start` and `Stop` do nothing. You must close the client after
all users finish, including any client returned alongside an initialization
error. An owned driver's `Client()` is nil before successful startup and after
stop; do not close its client yourself.

For custom analytics, first configure the matching metric in LaunchDarkly. After
successful startup, track the metric's event name with the same stable account
identity used for evaluation:

```go
import "github.com/launchdarkly/go-sdk-common/v3/ldcontext"

err := driver.Client().TrackEvent("checkout-completed", ldcontext.New(accountID))
```

A nil error confirms SDK acceptance, not remote delivery. Analytics can transmit
context data outside facade telemetry's redaction policy; review permitted
attributes and event configuration before enabling it. Arkoren ships no native
flag-administration or webhook adapter.

### Local Data

The SDK's test data source exercises local evaluation without a LaunchDarkly
connection. Using the SDK imports above, define a checkout fixture:

```go
import "github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldtestdata"

data := ldtestdata.DataSource()
data.Update(data.Flag("checkout-express").VariationForAll(true))
```

Build a client with remote analytics disabled. Retain cleanup even if client
initialization fails:

```go
client, err := ld.MakeCustomClient("local-checkout", ld.Config{
	DataSource: data,
	Events:     ldcomponents.NoEvents(),
}, 0)
if client != nil {
	defer client.Close()
}
```

Handle `err` before wrapping the client with `NewFrom` and constructing the
facade. Evaluating `feature.NewFlag("checkout-express", false)` then returns
`true` without a remote service. The deferred close is yours because the client
is borrowed; there is no server to remove. Use [memory fixtures](testing.md)
when only application behavior, rather than SDK integration, is under test.

See the [`launchdarklyfeature` API](https://pkg.go.dev/arkoren.dev/driver/launchdarkly/launchdarklyfeature).
