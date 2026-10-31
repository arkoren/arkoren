# Arkoren Feature Flag LaunchDarkly Driver

`arkoren.dev/driver/launchdarkly/launchdarklyfeature` implements
`feature.Driver` on top of the official
[LaunchDarkly Go server SDK](https://github.com/launchdarkly/go-server-sdk)
(`github.com/launchdarkly/go-server-sdk/v7`). The SDK streams flag changes and
evaluates them locally, so an evaluation makes no network call.

Run `go get arkoren.dev/driver/launchdarkly` in your application's Go module.
Use the server-side SDK key of your LaunchDarkly environment, not a client-side
ID. The [LaunchDarkly guide](../../../cmd/arkoren/skills/arkoren/references/guides/feature-flags/drivers.md#launchdarkly)
shows facade wiring, startup checks, native analytics, and a service-free local
data source for verification.

`New(sdkKey)` owns the SDK client: `Start` builds it and waits up to a
configurable start wait for the first flags, and `Stop` closes it.
`NewFrom(client)` wraps a client you built, and then `Start` and `Stop` do
nothing.

Optional core `feature.Namespace` definitions compose ordinary flag keys with
an inherited fallback, not native LaunchDarkly namespaces. Create the exact
final key in your environment. Prefer subject targeting over per-user dynamic
keys; constructing a namespace neither creates flags nor changes targeting.

Set the startup wait and native SDK policy with a construction callback:

```go
import (
	"time"

	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"

	"arkoren.dev/driver/launchdarkly/launchdarklyfeature"
)

driver, err := launchdarklyfeature.New(sdkKey, func(features *launchdarklyfeature.Builder) {
	features.StartWait(10 * time.Second)
	features.SDK(func(config *ld.Config) {
		config.Events = ldcomponents.NoEvents()
	})
})
```

Here `sdkKey` comes from your secret configuration. Handle `err`, register the
driver's lifecycle hooks as shown in the guide, and pass it to `feature.New`.
`Start` verifies the SDK has initial flags; a negative wait still fails if the
client is not immediately initialized. `NoEvents` disables analytics, not flag
evaluation. Keep SDK analytics enabled when you need experiment reporting.

Callbacks run directly once in order on one fresh draft before key validation. Nil entries panic;
expanded nil or empty slices omit edits. Retained drafts cannot change constructed settings.
The native SDK hook is stored until `Start`, not invoked during construction. Zero `StartWait`
uses five seconds; a negative wait returns immediately. `NewFrom(client)` has no optional settings
and leaves client lifecycle to the caller. Native SDK internals and function captures stay shared.

Owned `Start` and `Stop` calls serialize: shutdown waits for in-progress startup
before closing the acquired client. The serialization wait and native SDK close
are not bounded by the supplied context. Native configuration hooks must not
recursively call the driver's lifecycle methods.

A flag that is off, or whose prerequisite failed, returns the caller's default
instead of LaunchDarkly's off variation. Subjects become contexts of kind
`user`; an empty key becomes one shared anonymous context. See the package
documentation for the reasons, errors, and attributes.

Raw `Evaluate` checks cancellation first, then rejects empty or unknown value
kinds with `feature.ErrTypeMismatch`, before readiness or flag lookup. The
typed facade continues to validate flag definitions before driver access.

See the [driver API reference](https://pkg.go.dev/arkoren.dev/driver/launchdarkly/launchdarklyfeature).
