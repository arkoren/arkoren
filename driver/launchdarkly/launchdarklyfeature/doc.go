// Package launchdarklyfeature provides a feature flag driver for
// [LaunchDarkly], built on the official LaunchDarkly Go server SDK.
//
// Use it when LaunchDarkly is your flag service and you want the vendor's own
// SDK: streaming updates, local evaluation, and LaunchDarkly's targeting
// semantics, without a translation layer. For a vendor-neutral setup, or a
// service with no native driver, see the openfeaturefeature package.
//
// # Usage
//
// [New] takes the server-side SDK key of one environment and owns the SDK
// client: [Driver.Start] builds it and waits up to [Builder.StartWait] for the
// first flags, failing if the client is not initialized by then, and
// [Driver.Stop] closes it, flushing analytics events. Register the pair with
// the application lifecycle:
//
//	err := app.Run(ctx, func(compose *application.Composer) {
//		telemetry := compose.App().Telemetry()
//
//		driver := compose.Must(launchdarklyfeature.New(os.Getenv("LAUNCHDARKLY_SDK_KEY")))
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
// [Builder.SDK] adjusts the SDK configuration, for example to point at a Relay
// Proxy or change event settings. [NewFrom] wraps an [ld.LDClient] the caller
// built: [Driver.Start] and [Driver.Stop] then do nothing and the caller closes
// the client. [Driver.Client] returns the client for what the contract does
// not cover, such as tracking events.
//
// New invokes Builder callbacks directly once in order on one fresh draft
// before key validation. Nil entries panic; expanded nil or empty slices omit
// edits. Retained drafts cannot change published settings. Native SDK hooks
// are stored until Start; SDK internals and function captures stay shared.
// NewFrom has no optional settings.
// Owned Start and Stop calls serialize, including SDK hooks and acquisition;
// this wait and native shutdown are not canceled by their context. Native hooks
// must not recursively invoke the same driver's lifecycle methods.
//
// # Subjects
//
// Every subject becomes a LaunchDarkly context of the default kind "user",
// keyed by [feature.Subject.Key]. An empty key makes an anonymous context
// whose key is [AnonymousKey]; all anonymous subjects share that key, so they
// share one percentage rollout bucket and are all targeted together.
//
// Attributes are set by name. Strings, bools, int64 and float64 values become
// JSON values, a []string becomes an array (a rule matches if any element
// matches), and a [time.Time] becomes its Unix time in milliseconds, which
// LaunchDarkly's date operators accept. An attribute named "key" or "kind"
// never replaces the subject's identity. An attribute the SDK rejects, such
// as a "name" that is not a string, fails the evaluation with
// [feature.ErrInvalidSubject].
//
// # Mapping
//
// The driver evaluates with the SDK's JSON detail method and converts the
// variation to the requested kind itself. A KindInt request is answered with
// an int64 when the number is whole and with the float64 otherwise, which the
// facade rejects with [feature.ErrTypeMismatch].
//
// Evaluation reasons map as follows:
//
//   - OFF and PREREQUISITE_FAILED return no value with [feature.ReasonDisabled].
//     LaunchDarkly would serve the flag's "off variation" here; the driver does
//     not, because the contract says a disabled flag yields the caller's
//     default. To serve a value while a flag is off, use targeting rules
//     instead of switching the flag off.
//   - TARGET_MATCH and RULE_MATCH return [feature.ReasonTargetingMatch].
//   - FALLTHROUGH returns [feature.ReasonDefault].
//   - A rule match or fallthrough that is part of an experiment returns
//     [feature.ReasonSplit]. The SDK exposes no other rollout information, so
//     an ordinary percentage rollout reports targeting_match or default.
//   - A null variation returns no value with the reason above.
//
// [feature.Result.Variant] is the variation index as a decimal string, such
// as "0", because LaunchDarkly variations have no names; it is empty when
// the SDK reports no index.
//
// Evaluation errors map as follows: FLAG_NOT_FOUND to [feature.ErrNotFound];
// WRONG_TYPE, or a variation that cannot be read as the requested kind, to
// [feature.ErrTypeMismatch]; CLIENT_NOT_READY to [feature.ErrNotReady];
// USER_NOT_SPECIFIED and a context the SDK rejects to
// [feature.ErrInvalidSubject]. MALFORMED_FLAG, EXCEPTION, and any other kind
// fail with a plain error that names the kind.
// A canceled evaluation fails first with the context error. Empty or unknown
// raw request kinds then fail with ErrTypeMismatch before readiness, flag lookup,
// or the disabled shortcut. Known kinds retain their normal fallback semantics.
//
// # Caveats
//
// The SDK evaluates locally against flags it keeps in memory, so Evaluate
// does no I/O and does not observe cancellation after the context check. Until
// the first flags arrive the SDK may serve last-known data from a persistent
// store if one is configured; without one, evaluations fail with
// [feature.ErrNotReady].
//
// [LaunchDarkly]: https://launchdarkly.com
package launchdarklyfeature
