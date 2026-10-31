---
title: Feature Flag Drivers
nav: Drivers
description: Choose between the memory, OpenFeature, and LaunchDarkly drivers, then wire and configure each; OpenFeature reaches flagd and others.
order: 30
---

# Feature Flag Drivers

## Introduction

The feature flag facade works the same with every driver, so the choice is
about where your flags live. The **memory** driver holds flags in the process
and suits tests and local development. The **LaunchDarkly** driver evaluates
flags with LaunchDarkly's official Go SDK.

The **OpenFeature** driver evaluates
flags through [OpenFeature](https://openfeature.dev), a vendor-neutral standard
with providers for many flag services, so one driver reaches flagd, GO Feature
Flag, ConfigCat, Flagsmith, and any other service that publishes an OpenFeature
Go provider, including LaunchDarkly's community-maintained one. See [Feature Flags Overview](overview.md) for using the facade,
and [Choosing a Driver](../drivers/choosing-a-driver.md) for every service's
drivers side by side.

If you use LaunchDarkly, prefer the native driver over OpenFeature's provider
for it: the provider is community-maintained, while the native driver uses the
SDK LaunchDarkly publishes and supports. Choose OpenFeature for LaunchDarkly
only if you want to switch services without changing code, or already use
OpenFeature hooks.

## Compatibility matrix

Compare flag evaluation and access exposed by Arkoren, not each vendor's flag
administration API. Scalar flags are booleans, strings, integers, and floats;
object-valued flags are outside the portable contract.

Optional [namespaces](overview.md#sharing-a-flag-prefix-and-fallback) join literal
names in core, not in the provider. Every row evaluates the final `Flag[T]`
definition and its fallback; no driver gains native namespaces, flag creation,
or schema enforcement. Create the final key in external providers before use.

| Driver | Scalar flags | Typed application definitions | Subject-key targeting | Attribute-based targeting | Object flags | Native SDK client |
| --- | --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | Yes | Yes — Standalone and namespace-derived flags work in evaluation and typed setup. | Yes — Explicit per-key overrides through SetFor. | No | No | No |
| [OpenFeature](#openfeature) | Yes | Yes — Standalone and namespace-derived flags do not enforce provider schemas. | Partial — The configured provider decides which targeting rules apply. | Partial — Attributes pass through; provider rule support varies. | No | Yes |
| [LaunchDarkly](#launchdarkly) | Yes | Yes — Standalone and namespace-derived flags do not enforce provider schemas. | Yes | Yes | No | Yes |

Memory ignores subject attributes and keeps independent process-local state.
OpenFeature forwards the subject key and attributes to your chosen Go provider;
it does not add targeting rules that provider lacks. The services named above
are reached through that provider, not separate Arkoren drivers. LaunchDarkly
uses user contexts; all anonymous subjects share the same anonymous key. Native
client access is provider-specific and does not expand the portable flag types.
All three drivers check cancellation first, then reject empty or unknown raw
request kinds with `feature.ErrTypeMismatch` before readiness, lookup,
disabled/default shortcuts, or provider I/O. Only the four scalar kinds are
valid. This is [raw-request validation](overview.md#raw-driver-requests), not a
new flag capability or a change to typed definitions and their fallbacks.

Run installation commands from your application's Go module. Examples use
`feature` from `arkoren.dev/core/service/feature` and your operation context
`ctx`. Register driver hooks using your existing
[application lifecycle](../application/lifecycle-and-ownership.md).
Focused snippets omit routine error checks;
handle constructor errors before registering hooks or evaluating flags.
The LaunchDarkly examples also use standard-library `os` and `time`.

## Memory

The memory driver keeps flags in the process. It is part of `arkoren.dev/core`,
so there is nothing to install, and it is how application tests set the flags
they need. [Feature Flag Testing](testing.md#testing-your-application) shows
how to set values, per-subject overrides, and disabled flags. It ignores
subject attributes, so it cannot exercise targeting rules; use a real provider
for that.

### Connecting

`New` takes no settings, performs no I/O, and has no lifecycle hooks:

```go
import "arkoren.dev/core/service/feature/memoryfeature"

driver := memoryfeature.New()
checkout := feature.NewFlag("new-checkout", false)
err := driver.Set(checkout, true)

flags := feature.New(driver)
```

### Configuration

There are no constructor options. Populate flags using `Set` and `SetFor`;
updates affect the next evaluation and disappear when this driver is discarded.

### Evaluating flags

Use the shared [evaluation example](overview.md#evaluating-a-flag). With the flag
above it returns `true`; [testing](testing.md#testing-your-application) shows
per-subject overrides and disabled flags.

### Behavior and limits

Each driver has independent concurrent-safe state, with no persistence or
cross-process distribution. Subject attributes are ignored. `Set` and `SetFor`
require a shared `feature.Flag[T]` and a value of that type; unsupported types
fail to compile, and invalid definitions return `feature.ErrInvalidFlag` before
mutation. Runtime provider checks remain in place; a second independently
constructed definition with the same name does not declare a backend schema.
Raw kind validation runs before reading the flag, including before a disabled
or unmatched flag can return no value. For a known kind, those no-value results
still select the typed definition's fallback without an error.

For every function and option, see
[`memoryfeature`](https://pkg.go.dev/arkoren.dev/core/service/feature/memoryfeature).

## OpenFeature

The OpenFeature driver turns an OpenFeature provider into an Arkoren feature
flag driver. Choose it when your flags live in a service that has an
OpenFeature Go provider. What a flag means, how targeting rules are evaluated,
how often the provider refreshes, and what happens when the service is down are
all the provider's behavior, so the quality of the integration is the quality
of the provider you pick. Read its documentation as well as this page.

### Installation

Install the driver, then the provider module of your vendor:

```sh
go get arkoren.dev/driver/openfeature
```

### Connecting

`openfeaturefeature.New` takes the provider and owns it. It does no I/O: `Start`
registers the provider and waits until it is ready, so a misconfigured provider
fails startup, and `Stop` shuts it down. Construct the driver and facade:

Here `provider` is the `openfeature.FeatureProvider` built by your vendor's
provider package, configured with that service's endpoint and credentials.

```go
import "arkoren.dev/driver/openfeature/openfeaturefeature"

driver := openfeaturefeature.New(provider)
flags := feature.New(driver)
```

Register `driver.Stop` for cleanup, `driver.Start` for startup, and `driver.Ping`
for readiness in your existing composer. Evaluate only after startup succeeds.
The driver registers the provider in a private OpenFeature API instance, so it
never replaces the global OpenFeature provider, and you can run it next to code
that uses OpenFeature directly. `Ping` backs a readiness check; it reports the
provider's state without calling the service, and it fails until the state is
`READY`. [Lifecycle and ownership](../application/lifecycle-and-ownership.md)
explains the hooks and checks.

If you already configure OpenFeature yourself, with hooks, domains, or a
provider shared with other code, pass the client instead and keep ownership:

```go
import "github.com/open-feature/go-sdk/openfeature"

client := openfeature.NewClient("checkout")
driver := openfeaturefeature.NewFrom(client)
```

`NewFrom` registers and shuts down nothing, so `Start` and `Stop` do nothing and
you register and stop the provider yourself.
Do not rebind or shut down a `New` driver's owned provider through the native
client; use its lifecycle hooks instead.

### Configuration

If every flag rule needs the environment or service name, supply those facts
once rather than repeating them on each subject. The driver's
`Builder.EvaluationContext` adds these **ambient attributes** to
every evaluation, such as the environment or the service name, which your
targeting rules can match:

```go
driver := openfeaturefeature.New(provider, func(features *openfeaturefeature.Builder) {
	features.EvaluationContext(openfeature.NewTargetlessEvaluationContext(map[string]any{"env": "prod"}))
})
```

The subject you pass to the facade has precedence: its attributes replace
ambient attributes of the same name, and its nonempty key replaces the ambient targeting key.
Everything else, such as the service address, the polling interval, and the
credentials, is configured on the provider.

Callbacks run directly once in order on one fresh draft. Nil entries panic;
expanded nil/empty slices omit edits. Ambient outer attribute maps copy at input
and publication, not arbitrary nested values. Methods return the same draft;
retained drafts cannot change the driver's context. NewFrom has no optional
settings.

### Evaluating flags

Use the shared [evaluation example](overview.md#evaluating-a-flag) after startup.
A successful evaluation returns the provider's value; a provider error returns
your application default and the mapped error below.

### Behavior and limits

The subject key becomes OpenFeature's targeting key, and the attributes are
copied into the evaluation context as they are. Strings, booleans, numbers,
string lists, and times are passed unchanged, and each provider decides which
of them its rules can match. An empty subject key leaves any ambient targeting
key in effect. If neither supplies a key, a provider that requires one fails
with `feature.ErrInvalidSubject`.

OpenFeature's error codes map to the contract's errors: an unknown flag is
`feature.ErrNotFound`, a value of the wrong type is `feature.ErrTypeMismatch`,
a provider that is not ready is `feature.ErrNotReady`, and a missing targeting
key or invalid context is `feature.ErrInvalidSubject`. Any other code fails
with an error that names it. A flag the provider reports as disabled returns the
fallback declared in your flag definition. The reasons map one to one, except that
OpenFeature's `STALE` becomes `feature.ReasonCached`, because both mean a value
that may be out of date. Variants pass through, and how much a provider reports
is up to it.

Only booleans, strings, integers, and floats can be read, like every Arkoren
driver. OpenFeature's object flags are not available.
Invalid raw kinds are rejected before the driver's readiness check or any
provider call. For a known kind, readiness failures still return `ErrNotReady`,
and a provider-reported disabled flag still yields the application's fallback.

> [!NOTE]
> The driver cannot be restarted after `Stop` or a failed `Start`. Build a new
> driver to retry initialization. Stopping before `Start` also makes an owned
> driver terminal, without initializing or shutting down the unused provider.

Owned lifecycle calls serialize; waiting for an in-progress lifecycle call is
not bounded by `ctx`. Context-aware providers receive the cleanup context and
must cooperate with cancellation. Legacy contextless shutdown is not bounded by
that context, and a legacy initializer can outlive canceled startup inside the
SDK. Do not recursively call `Start` or `Stop` from native provider callbacks.

### Adding OpenFeature hooks

When you need provider-specific evaluation observations, attach your
application's `openfeature.Hook` implementation to the native client:

```go
driver.Client().AddHooks(hook)
```

Here `hook` is your initialized SDK hook. Add hooks during setup before
concurrent evaluations; the SDK invokes them for subsequent evaluations.
Hooks can receive subjects and values that Arkoren telemetry deliberately
omits, so filter sensitive data before exporting it. The client is available
before startup, but evaluate through the facade only after `Start` succeeds.
Use `NewFrom` for an externally managed client with domains or event handlers;
you then own provider registration and shutdown. No separate Arkoren webhook
or flag administration adapter is shipped.

### Running locally

The SDK ships an in-memory provider that needs no service, which suits local
development and tests that should go through the real OpenFeature path:

```go
import "github.com/open-feature/go-sdk/openfeature/memprovider"

provider := memprovider.NewInMemoryProvider(map[string]memprovider.InMemoryFlag{
	"new-checkout": {
		Key:            "new-checkout",
		State:          memprovider.Enabled,
		DefaultVariant: "on",
		Variants:       map[string]any{"on": true, "off": false},
	},
})
driver := openfeaturefeature.New(provider)
flags := feature.New(driver)
```

Register this driver's start and stop hooks as shown above. After startup,
`flags.Value(ctx, feature.NewFlag("new-checkout", false), feature.Subject{})` returns `true`
with a nil error. Shutdown releases the provider. For application tests that
only need flag values, the [memory driver](testing.md) is simpler.

For every function and option, see
[`openfeaturefeature`](https://pkg.go.dev/arkoren.dev/driver/openfeature/openfeaturefeature).

## LaunchDarkly

The LaunchDarkly driver evaluates flags with LaunchDarkly's official Go server
SDK. The SDK streams flag changes into memory and evaluates them locally, so
an evaluation makes no network call and keeps working through a short outage
with the last flags it received. Choose it when LaunchDarkly is your flag
service. It maps the SDK's reasons and errors to the contract directly, with
no provider in between.

You need a LaunchDarkly account, an environment, and that environment's
server-side SDK key. Keep the key in secret configuration rather than source
code. Create the boolean flag used by your application in that environment;
the SDK evaluates flags but does not create them.

### Installation

```sh
go get arkoren.dev/driver/launchdarkly
```

### Connecting

`launchdarklyfeature.New` takes the server-side SDK key of one LaunchDarkly
environment and owns the SDK client. It does no I/O and fails only if the key
is empty. `Start` builds the client and waits for the first flags, so a wrong
key or an unreachable service fails startup instead of serving defaults, and
`Stop` closes the client after flushing analytics events. Construct the driver
and facade:

```go
import "arkoren.dev/driver/launchdarkly/launchdarklyfeature"

driver, err := launchdarklyfeature.New(os.Getenv("LAUNCHDARKLY_SDK_KEY"))
flags := feature.New(driver)
```

Handle the constructor error, then register `driver.Stop` for cleanup,
`driver.Start` for startup, and `driver.Ping` for readiness in your existing
composer. Evaluate only after startup succeeds.
`Ping` backs a readiness check. It does no I/O and fails with an error wrapping
`feature.ErrNotReady` until the client is initialized, so it also fails before
`Start` and after `Stop`. [Lifecycle and ownership](../application/lifecycle-and-ownership.md)
explains the hooks and checks.

If you already build the client, for instance to share it with code that
tracks events, pass it instead and keep ownership. Here `sdkKey` is the
environment's server-side SDK key:

```go
import ld "github.com/launchdarkly/go-server-sdk/v7"

client, err := ld.MakeClient(sdkKey, 5*time.Second)
if client != nil {
	defer client.Close()
}
driver := launchdarklyfeature.NewFrom(client)
```

`NewFrom` creates and closes nothing, so `Start` and `Stop` do nothing and you
close the client yourself. The SDK can return a client together with an
initialization error; the cleanup above releases it in either case.

`Start` checks `ctx` before constructing the SDK client, but the SDK's startup
wait is bounded by `StartWait`, not subsequent context cancellation. `Stop`
waits for the SDK's analytics flush and is not bounded by its context. Set SDK
network and event options to match your shutdown budget.
Owned lifecycle calls also serialize, and that wait is not canceled by `ctx`.
An SDK construction hook must not recursively call the same driver's `Start`
or `Stop`.

### Configuration

| Builder method | Default | Notes |
|---|---|---|
| `Builder.StartWait(d)` | 5 seconds | How long `Start` waits for the first flags before failing. Zero selects the default; negative returns immediately and fails unless the data source is already ready. |
| `Builder.SDK(fn)` | none | Stores a hook that changes the SDK's `ld.Config` at Start before the client is built, for a Relay Proxy or federal endpoint, a persistent store, event settings, or logging. |

To allow more time for the first flags, set `StartWait`:

```go
driver, err := launchdarklyfeature.New(sdkKey, func(features *launchdarklyfeature.Builder) {
	features.StartWait(10 * time.Second)
})
```

This changes the startup wait, not evaluation deadlines. For native event
settings, add an SDK hook to that construction callback. Import `ld` as
`github.com/launchdarkly/go-server-sdk/v7` and `ldcomponents` as
`github.com/launchdarkly/go-server-sdk/v7/ldcomponents`:

```go
features.SDK(func(config *ld.Config) {
	config.Events = ldcomponents.SendEvents().FlushInterval(10 * time.Second)
})
```

The SDK uses a ten-second event flush interval after startup.
Handle the constructor's error before using `driver`. New callbacks run directly
once in order on one fresh draft before validation.
Nil entries panic; expanded nil/empty slices omit edits. Methods return the same
draft; retaining it cannot change published settings. The SDK hook runs during
Start, never construction. NewFrom has no optional settings because its client
is already configured. Native SDK objects and function captures stay shared.
NewFrom creates and closes nothing.

### Evaluating flags

Create `new-checkout` as a boolean flag in the LaunchDarkly environment whose
server-side SDK key you configured. Use the shared
[evaluation example](overview.md#evaluating-a-flag) after startup. A flag that
is off or whose prerequisite fails returns your application default, **not**
LaunchDarkly's off variation. Choose the default to match your safe off path.

### Behavior and limits

Every subject becomes a LaunchDarkly context of kind `user`, keyed by the
subject key. An anonymous subject becomes one anonymous context with the key
`anonymous`, so all anonymous subjects share a single percentage-rollout bucket
and match the same targeting. Attributes keep their types; a list of strings is
an array that a rule matches if any element matches, and a time becomes Unix
milliseconds, which LaunchDarkly's date operators accept. An attribute named
`key` or `kind` never replaces the subject's identity.

> [!WARNING]
> A flag that is switched off returns the fallback in your flag definition,
> not LaunchDarkly's off variation, because the contract says a disabled flag
> yields the caller's default. The same holds when a prerequisite fails. If a
> flag must serve a specific configured value, keep it on with its prerequisites
> satisfied and use targeting rules. To use that value while the flag is off,
> make the application default match the off variation.

Reasons map as follows: a target or rule match is `targeting_match`, the
fallthrough is `default`, and a rule or fallthrough that is part of an
experiment is `split`. The SDK does not report ordinary percentage rollouts, so
they show as `targeting_match` or `default`. The variant is the variation's
index as text, such as `"1"`, because LaunchDarkly variations have no names.

Errors map to the contract's: an unknown flag is `feature.ErrNotFound`, a
variation of the wrong type is `feature.ErrTypeMismatch`, a client without
flags is `feature.ErrNotReady`, and a context the SDK rejects is
`feature.ErrInvalidSubject`. A malformed flag or an SDK exception fails with an
error that names it. A variation that is JSON `null` returns the caller's
default. Integer reads accept whole numbers; LaunchDarkly stores numbers as
floats, so `3.5` read as an integer is `feature.ErrTypeMismatch`.

Only booleans, strings, integers, and floats can be read. LaunchDarkly's JSON
flags are not available.
Invalid raw kinds fail before readiness, SDK lookup, or the disabled/null
shortcut. For known kinds, off flags, failed prerequisites, and null variations
retain the fallback behavior above. Evaluation is local: after its initial
context check, it does not observe later cancellation.

### Tracking a native SDK event

For LaunchDarkly analytics beyond flag evaluation, create a custom metric in
your LaunchDarkly environment, then track its event name with the native client
after successful driver startup:

```go
import "github.com/launchdarkly/go-sdk-common/v3/ldcontext"

err := driver.Client().TrackEvent("checkout-completed", ldcontext.New(userID))
```

Here `userID` is the same stable string identity used for flag evaluation.
A nil error means the SDK accepted the event; it does not confirm remote
delivery. The SDK queues analytics and flushes during shutdown; avoid sending
personal attributes you have not approved for LaunchDarkly. Do not close an
owned client yourself: `driver.Stop` does that. With `NewFrom`, close the
client yourself after all users finish. The driver ships no webhook receiver
or flag administration adapter.

### Running locally

The SDK ships a test data source that needs no network, which suits tests that
should go through the real SDK path. Build a client on it and wrap it:

```go
import (
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldtestdata"
)

data := ldtestdata.DataSource()
data.Update(data.Flag("new-checkout").VariationForAll(true))

client, err := ld.MakeCustomClient("test-key", ld.Config{
	DataSource: data,
	Events:     ldcomponents.NoEvents(),
}, 0)
if client != nil {
	defer client.Close()
}
driver := launchdarklyfeature.NewFrom(client)
flags := feature.New(driver)
```

Reading `new-checkout` through `flags` for any subject returns
`true` with a nil error. The deferred close belongs to you because this uses
`NewFrom`; there is no server to clean up. For application tests that only need
flag values, the [memory driver](testing.md) is simpler.

For every function and option, see
[`launchdarklyfeature`](https://pkg.go.dev/arkoren.dev/driver/launchdarkly/launchdarklyfeature).
