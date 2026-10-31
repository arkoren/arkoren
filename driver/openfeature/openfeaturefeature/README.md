# Arkoren Feature Flag OpenFeature Driver

`arkoren.dev/driver/openfeature/openfeaturefeature` implements `feature.Driver`
on top of the [OpenFeature](https://openfeature.dev) Go SDK
(`github.com/open-feature/go-sdk`). It reaches every vendor that publishes an
OpenFeature Go provider, such as flagd, GO Feature Flag, ConfigCat, and
Flagsmith. Flag semantics, targeting rules, caching, and reliability are the
provider's, and so is its quality.

Run `go get arkoren.dev/driver/openfeature` in your application's Go module,
then install and configure your chosen provider. The
[OpenFeature guide](../../../cmd/arkoren/skills/arkoren/references/guides/feature-flags/drivers.md#openfeature)
includes a service-free provider, facade evaluation, hooks, lifecycle wiring,
and a success check.

`New(provider)` owns the provider: `Start` registers it in a private OpenFeature
API instance, so the global OpenFeature provider is never replaced, and waits
until it is ready; `Stop` shuts it down. `NewFrom(client)` wraps a client you
configured, and then `Start` and `Stop` do nothing.

Optional core `feature.Namespace` definitions compose ordinary flag keys and
fallbacks, not native provider namespaces. Create the final literal key in the
configured provider. Prefer subject targeting over dynamically generated keys
when the provider supports it; namespace construction adds no targeting rules.

Set ambient context with a construction callback:

```go
import (
	"github.com/open-feature/go-sdk/openfeature"

	"arkoren.dev/driver/openfeature/openfeaturefeature"
)

driver := openfeaturefeature.New(provider, func(features *openfeaturefeature.Builder) {
	features.EvaluationContext(openfeature.NewTargetlessEvaluationContext(map[string]any{"env": "prod"}))
})
```

Callbacks run directly once in order on one fresh draft. Nil entries panic; expanded nil or empty
slices omit edits. The ambient attribute map is copied at input and publication; arbitrary nested
values stay shared. Retained drafts cannot change the driver's context. Per-evaluation subjects
override ambient attributes and nonempty targeting keys. An owned provider cannot restart after
`Stop`. `NewFrom(client)` retains no optional settings and leaves provider lifecycle to the caller.

Owned lifecycle calls serialize. Stopping before the first start also makes the
driver terminal, but does not initialize or shut down the unused provider.
Context-aware providers control cancellation of their shutdown; legacy native
shutdown and lifecycle serialization waits are not bounded by `ctx`. A legacy
initializer can outlive a canceled startup inside the SDK, so providers remain
responsible for safe cleanup. Native callbacks must not recursively call the
driver's lifecycle methods.

Here `provider` is your configured `openfeature.FeatureProvider`; `Start`
initializes it. Pass
`driver` to `feature.New` after registering its lifecycle as shown in the
guide. Subject attributes override `env`; other subjects receive the ambient
value. See the [driver API reference](https://pkg.go.dev/arkoren.dev/driver/openfeature/openfeaturefeature).

Raw `Evaluate` checks cancellation first, then rejects empty or unknown value
kinds with `feature.ErrTypeMismatch`, before readiness or provider evaluation.
Typed flag definitions and their declared fallback behavior are unchanged.
