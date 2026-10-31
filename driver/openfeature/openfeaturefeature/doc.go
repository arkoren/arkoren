// Package openfeaturefeature provides a feature flag driver for any
// OpenFeature provider written for the Go SDK.
//
// [OpenFeature] is a vendor-neutral standard for flag evaluation, so this one
// driver reaches every vendor with a Go provider, such as flagd, GO Feature
// Flag, ConfigCat, or Flagsmith. The driver translates the contract to the
// SDK's typed value-details calls; flag semantics, targeting rules, caching,
// and reliability are the provider's, and so is its quality.
//
// # Usage
//
// [New] takes a provider and owns it: [Driver.Start] registers it in a
// private OpenFeature API instance, so the global default provider of the
// application is untouched, and waits until the provider is ready;
// [Driver.Stop] shuts it down. Register the pair with the application
// lifecycle:
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		telemetry := compose.App().Telemetry()
//
//		driver := openfeaturefeature.New(provider)
//		compose.Cleanup(driver.Stop)
//		compose.Startup(driver.Start)
//		compose.Check("flags", driver.Ping)
//		flags := feature.New(driver, func(flags *feature.Builder) {
//			flags.Telemetry(telemetry)
//		})
//		// Pass flags to your application services here.
//		_ = flags
//	})
//	if err != nil {
//		return err
//	}
//
// [NewFrom] wraps an [openfeature.Client] the caller configured and
// registered a provider for. The driver then registers and shuts down
// nothing: [Driver.Start] and [Driver.Stop] do nothing, and the caller
// remains responsible for the provider.
//
// New accepts Builder callbacks that run directly once in order on a fresh
// draft. Nil entries panic; expanded nil or empty slices omit edits. Ambient
// attribute maps are copied at input and publication, not nested application
// values. Retained drafts cannot change published context. Owned providers
// cannot restart after Stop; NewFrom has no optional settings.
// Owned lifecycle calls serialize and readiness is published only after native
// initialization succeeds. Legacy provider shutdown and serialization waits are
// not bounded by ctx; cancellation of context-aware cleanup remains cooperative.
// A legacy initializer can outlive cancellation inside the SDK. Stopping an
// unstarted owned driver also makes it terminal, without calling the provider.
//
// # Mapping
//
// The subject key becomes the targeting key and the attributes are copied to
// the evaluation context as they are; string, bool, int64, float64, []string,
// and [time.Time] values pass through without conversion, and each provider
// decides how it reads them.
//
// OpenFeature error codes map to the contract's errors: FLAG_NOT_FOUND to
// [feature.ErrNotFound]; TYPE_MISMATCH and PARSE_ERROR to
// [feature.ErrTypeMismatch]; PROVIDER_NOT_READY to [feature.ErrNotReady];
// TARGETING_KEY_MISSING and INVALID_CONTEXT to [feature.ErrInvalidSubject].
// Any other code fails with an error that names it. Resolution reasons map
// one to one; STALE becomes [feature.ReasonCached]. A flag with the DISABLED
// reason returns no value, so the facade hands the caller's default back.
// Cancellation precedes request-kind validation; empty or unknown raw kinds
// fail with ErrTypeMismatch before readiness or any native provider operation.
//
// [OpenFeature]: https://openfeature.dev
package openfeaturefeature
